// Package capsuledoc adapts captured documentation runs to the isolated Pi
// capsule without making the documentation domain depend on the capsule.
package capsuledoc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/artifacts"
	"github.com/cyr1en/ref0/internal/capsule"
	docgen "github.com/cyr1en/ref0/internal/documentation"
	"github.com/cyr1en/ref0/internal/providers"
)

type ProviderReader interface {
	GetProfile(context.Context, providers.ProfileID) (providers.Profile, error)
	GetEndpoint(context.Context, providers.EndpointID) (providers.Endpoint, error)
}

type SourceArtifactResolver interface {
	ResolveArtifactKey(string) (string, error)
}

type Options struct {
	Capsule            capsule.FactoryOptions
	SourceTools        SourceToolLimits
	ValidationAttempts int
}

func DefaultOptions() Options {
	return Options{SourceTools: DefaultSourceToolLimits(), ValidationAttempts: 2}
}

type modelSession interface {
	Invoke(context.Context, string) (capsule.Invocation, error)
}

type modelFactory interface {
	NewSession(capsule.Role, string, []capsule.Tool, map[string]any) (modelSession, error)
}

type capsuleFactory struct{ value *capsule.Factory }

func (factory capsuleFactory) NewSession(role capsule.Role, prompt string, tools []capsule.Tool, schema map[string]any) (modelSession, error) {
	return factory.value.NewSession(role, prompt, tools, schema)
}

type factoryBinder func(context.Context, docgen.CapturedModel) (modelFactory, error)

// Runtime is the narrow AgentRuntime implementation used by the central
// documentation worker. The supplied pool must already have passed Start's
// fail-closed topology validation.
type Runtime struct {
	providers          ProviderReader
	sources            SourceArtifactResolver
	pool               *capsule.SlotPool
	secrets            capsule.SecretReader
	applicationVersion string
	options            Options
	bind               factoryBinder
}

func NewRuntime(
	providerReader ProviderReader,
	sourceResolver SourceArtifactResolver,
	pool *capsule.SlotPool,
	secrets capsule.SecretReader,
	applicationVersion string,
	options Options,
) (*Runtime, error) {
	if providerReader == nil || sourceResolver == nil || pool == nil {
		return nil, errors.New("capsule documentation runtime dependencies are incomplete")
	}
	if pool.State() != capsule.PoolReady {
		return nil, errors.New("capsule slot pool is not ready")
	}
	if options.SourceTools == (SourceToolLimits{}) {
		options.SourceTools = DefaultSourceToolLimits()
	}
	if options.ValidationAttempts == 0 {
		options.ValidationAttempts = 2
	}
	if err := options.SourceTools.validate(); err != nil || options.ValidationAttempts < 1 || options.ValidationAttempts > 5 {
		return nil, errors.New("capsule documentation runtime options are invalid")
	}
	if applicationVersion == "" || utf8.RuneCountInString(applicationVersion) > 64 ||
		strings.IndexFunc(applicationVersion, func(character rune) bool {
			return character == ' ' || character == '\t' || character == '\r' || character == '\n'
		}) >= 0 {
		return nil, errors.New("application version is invalid")
	}
	runtime := &Runtime{
		providers: providerReader, sources: sourceResolver, pool: pool, secrets: secrets,
		applicationVersion: applicationVersion, options: options,
	}
	runtime.bind = runtime.bindCaptured
	return runtime, nil
}

func (runtime *Runtime) Plan(ctx context.Context, detail docgen.RunDetail) (docgen.PagePlan, docgen.ModelUsage, error) {
	snapshots, err := runtime.snapshots(detail)
	if err != nil {
		return docgen.PagePlan{}, docgen.ModelUsage{}, &docgen.RuntimeFailure{}
	}
	planner, err := runtime.capturedModel(detail, providers.DocumentationPlanner)
	if err != nil {
		return docgen.PagePlan{}, docgen.ModelUsage{}, &docgen.RuntimeFailure{}
	}
	writer, err := runtime.capturedModel(detail, providers.DocumentationWriter)
	if err != nil {
		return docgen.PagePlan{}, docgen.ModelUsage{}, &docgen.RuntimeFailure{}
	}
	plannerFactory, err := runtime.bind(ctx, planner)
	if err != nil {
		return docgen.PagePlan{}, docgen.ModelUsage{}, runtime.bindingFailure(ctx, err)
	}
	// Fence both captured roles before the planner can spend a model call. An
	// unsupported writer cannot leave a partially accepted plan behind.
	if _, err = runtime.bind(ctx, writer); err != nil {
		return docgen.PagePlan{}, docgen.ModelUsage{}, runtime.bindingFailure(ctx, err)
	}
	required, err := requiredWebsiteSourcePaths(snapshots)
	if err != nil {
		return docgen.PagePlan{}, docgen.ModelUsage{}, &docgen.RuntimeFailure{}
	}
	prompt, err := planningPrompt(detail, snapshots, required)
	if err != nil {
		return docgen.PagePlan{}, docgen.ModelUsage{}, &docgen.RuntimeFailure{}
	}
	plan, usage, err := runtime.runPlanner(ctx, plannerFactory, snapshots, required, prompt)
	if err != nil {
		return docgen.PagePlan{}, docgen.ModelUsage{}, err
	}
	return plan, usage, nil
}

func (runtime *Runtime) GeneratePage(ctx context.Context, detail docgen.RunDetail, page docgen.Page, correction string) (docgen.PageSubmission, docgen.ModelUsage, error) {
	snapshots, err := runtime.snapshots(detail)
	if err != nil {
		return docgen.PageSubmission{}, docgen.ModelUsage{}, &docgen.RuntimeFailure{}
	}
	plan := docgen.PagePlan{Pages: make([]docgen.PlannedPage, len(detail.Pages))}
	matched := 0
	for index, candidate := range detail.Pages {
		plan.Pages[index] = candidate.Target
		if reflect.DeepEqual(candidate.Target, page.Target) {
			matched++
		}
	}
	if plan.Validate() != nil || matched != 1 {
		return docgen.PageSubmission{}, docgen.ModelUsage{}, &docgen.RuntimeFailure{}
	}
	writer, err := runtime.capturedModel(detail, providers.DocumentationWriter)
	if err != nil {
		return docgen.PageSubmission{}, docgen.ModelUsage{}, &docgen.RuntimeFailure{}
	}
	factory, err := runtime.bind(ctx, writer)
	if err != nil {
		return docgen.PageSubmission{}, docgen.ModelUsage{}, runtime.bindingFailure(ctx, err)
	}
	instructions := pageInstructions(detail.Run.Instructions, correction)
	prompt, err := pagePrompt(detail, page.Target, instructions)
	if err != nil {
		return docgen.PageSubmission{}, docgen.ModelUsage{}, &docgen.RuntimeFailure{}
	}
	submission, usage, err := runtime.runWriter(ctx, factory, snapshots, page.Target, prompt)
	if err != nil {
		return docgen.PageSubmission{}, docgen.ModelUsage{}, err
	}
	return submission, usage, nil
}

func (runtime *Runtime) ValidatePage(ctx context.Context, detail docgen.RunDetail, page docgen.Page, submission docgen.PageSubmission) (artifacts.Page, error) {
	snapshots, err := runtime.snapshots(detail)
	if err != nil {
		return artifacts.Page{}, &docgen.RuntimeFailure{}
	}
	byRevision := make(map[docgen.ID]string, len(snapshots))
	captured := make(map[docgen.ID]docgen.CapturedSource, len(snapshots))
	for _, snapshot := range snapshots {
		byRevision[snapshot.Captured.RevisionID] = snapshot.Root
		captured[snapshot.Captured.SourceID] = snapshot.Captured
	}
	return docgen.ValidateConceptPage(
		ctx, page.Target, submission, captured,
		snapshotReader{roots: byRevision}, page.CreatedAt, runtime.applicationVersion,
	)
}

func (runtime *Runtime) runPlanner(ctx context.Context, factory modelFactory, snapshots []sourceSnapshot, required []requiredSourcePath, prompt string) (docgen.PagePlan, docgen.ModelUsage, error) {
	correction := ""
	usage := docgen.ModelUsage{}
	for attempt := 0; attempt < runtime.options.ValidationAttempts; attempt++ {
		tools, err := newSourceToolSession(snapshots, runtime.options.SourceTools)
		if err != nil {
			return docgen.PagePlan{}, docgen.ModelUsage{}, err
		}
		session, err := factory.NewSession(capsule.Planner, plannerSystemPrompt, tools.capsuleTools(), planSchema())
		if err != nil {
			return docgen.PagePlan{}, docgen.ModelUsage{}, err
		}
		invocation, invokeErr := session.Invoke(ctx, prompt+correction)
		if invokeErr != nil {
			if contextError(ctx, invokeErr) != nil {
				return docgen.PagePlan{}, docgen.ModelUsage{}, contextError(ctx, invokeErr)
			}
			var failure *capsule.InvocationError
			if !errors.As(invokeErr, &failure) {
				return docgen.PagePlan{}, docgen.ModelUsage{}, invokeErr
			}
			usage = usage.Add(modelUsage(failure.Usage))
			correction = missingOutputCorrection
			continue
		}
		usage = usage.Add(modelUsage(invocation.Usage))
		plan, parseErr := parsePlan(invocation.Output, tools)
		if parseErr == nil {
			parseErr = validatePlanCoverage(plan, required)
		}
		if parseErr == nil {
			return plan, usage, nil
		}
		correction = fmt.Sprintf(validationCorrection, safeValidationError(parseErr))
	}
	return docgen.PagePlan{}, docgen.ModelUsage{}, &docgen.RuntimeFailure{Usage: usage}
}

func (runtime *Runtime) runWriter(ctx context.Context, factory modelFactory, snapshots []sourceSnapshot, target docgen.PlannedPage, prompt string) (docgen.PageSubmission, docgen.ModelUsage, error) {
	correction := ""
	usage := docgen.ModelUsage{}
	for attempt := 0; attempt < runtime.options.ValidationAttempts; attempt++ {
		tools, err := newSourceToolSession(snapshots, runtime.options.SourceTools)
		if err != nil {
			return docgen.PageSubmission{}, docgen.ModelUsage{}, err
		}
		session, err := factory.NewSession(capsule.PageWriter, pageSystemPrompt, tools.capsuleTools(), pageSchema())
		if err != nil {
			return docgen.PageSubmission{}, docgen.ModelUsage{}, err
		}
		invocation, invokeErr := session.Invoke(ctx, prompt+correction)
		if invokeErr != nil {
			if contextError(ctx, invokeErr) != nil {
				return docgen.PageSubmission{}, docgen.ModelUsage{}, contextError(ctx, invokeErr)
			}
			var failure *capsule.InvocationError
			if !errors.As(invokeErr, &failure) {
				return docgen.PageSubmission{}, docgen.ModelUsage{}, invokeErr
			}
			usage = usage.Add(modelUsage(failure.Usage))
			correction = missingOutputCorrection
			continue
		}
		usage = usage.Add(modelUsage(invocation.Usage))
		submission, parseErr := parseSubmission(invocation.Output, target, tools)
		if parseErr == nil {
			return submission, usage, nil
		}
		correction = fmt.Sprintf(validationCorrection, safeValidationError(parseErr))
	}
	return docgen.PageSubmission{}, docgen.ModelUsage{}, &docgen.RuntimeFailure{Usage: usage}
}

func (runtime *Runtime) snapshots(detail docgen.RunDetail) ([]sourceSnapshot, error) {
	if len(detail.Run.Sources) < 1 || len(detail.Run.Sources) > 100 || len([]byte(detail.Run.Instructions)) > 32_768 ||
		detail.Run.Language == "" || detail.Run.Language != strings.TrimSpace(detail.Run.Language) ||
		utf8.RuneCountInString(detail.Run.Language) > 64 || strings.IndexFunc(detail.Run.Language, func(character rune) bool {
		return character < 32 || character == 127
	}) >= 0 {
		return nil, errors.New("documentation agent request is invalid")
	}
	values := make([]sourceSnapshot, 0, len(detail.Run.Sources))
	seen := make(map[docgen.ID]struct{}, len(detail.Run.Sources))
	for _, captured := range detail.Run.Sources {
		if _, exists := seen[captured.SourceID]; exists {
			return nil, errors.New("captured source snapshots must be unique")
		}
		seen[captured.SourceID] = struct{}{}
		artifactKey := "sources/" + captured.SourceID.String() + "/snapshots/" + captured.RevisionID.String()
		root, err := runtime.sources.ResolveArtifactKey(artifactKey)
		if err != nil {
			return nil, err
		}
		snapshot, err := newSourceSnapshot(captured, root)
		if err != nil {
			return nil, err
		}
		values = append(values, snapshot)
	}
	return values, nil
}

func (runtime *Runtime) capturedModel(detail docgen.RunDetail, role providers.ModelRole) (docgen.CapturedModel, error) {
	var selected *docgen.CapturedModel
	for index := range detail.Run.Models {
		if detail.Run.Models[index].Role != role {
			continue
		}
		if selected != nil {
			return docgen.CapturedModel{}, errors.New("captured model role is duplicated")
		}
		selected = &detail.Run.Models[index]
	}
	if selected == nil {
		return docgen.CapturedModel{}, errors.New("captured model role is unavailable")
	}
	return *selected, nil
}

func (runtime *Runtime) bindCaptured(ctx context.Context, captured docgen.CapturedModel) (modelFactory, error) {
	profile, err := runtime.providers.GetProfile(ctx, captured.ProfileID)
	if err != nil {
		return nil, err
	}
	endpoint, err := runtime.providers.GetEndpoint(ctx, captured.EndpointID)
	if err != nil {
		return nil, err
	}
	profileVersion, ok := exactInt32(captured.ProfileVersion)
	if !ok {
		return nil, capsule.ErrBinding
	}
	endpointVersion, ok := exactInt32(captured.EndpointConfigurationVersion)
	if !ok {
		return nil, capsule.ErrBinding
	}
	var credentialVersion *int32
	if captured.CredentialVersion != nil {
		value, valid := exactInt32(*captured.CredentialVersion)
		if !valid {
			return nil, capsule.ErrBinding
		}
		credentialVersion = &value
	}
	factory, err := capsule.NewProviderFactory(capsule.ProviderCapture{
		Role: captured.Role, ProfileID: captured.ProfileID,
		ProfileVersionID: captured.ProfileVersionID, ProfileVersion: profileVersion,
		EndpointID: captured.EndpointID, EndpointConfigurationVersion: endpointVersion,
		CredentialVersion: credentialVersion, ReasoningEffort: captured.ReasoningEffort,
	}, profile, endpoint, runtime.pool, runtime.secrets, runtime.options.Capsule)
	if err != nil {
		return nil, err
	}
	return capsuleFactory{value: factory}, nil
}

func (runtime *Runtime) bindingFailure(ctx context.Context, err error) error {
	if contextError(ctx, err) != nil {
		return contextError(ctx, err)
	}
	return &docgen.RuntimeFailure{}
}

func exactInt32(value int) (int32, bool) {
	if value < 1 || value > math.MaxInt32 {
		return 0, false
	}
	return int32(value), true
}

func modelUsage(value capsule.Usage) docgen.ModelUsage {
	return docgen.ModelUsage{
		ModelCalls: value.ModelCalls, InputTokens: value.InputTokens,
		OutputTokens: value.OutputTokens, TotalTokens: value.TotalTokens,
		TruncatedToolResults: value.TruncatedToolResults,
	}
}

func contextError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

type snapshotReader struct{ roots map[docgen.ID]string }

func (reader snapshotReader) ReadSourceFile(ctx context.Context, revisionID docgen.ID, selectedPath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, exists := reader.roots[revisionID]
	if !exists {
		return nil, errors.New("source revision is outside the run")
	}
	if _, err := docgen.NormalizeSourcePath(selectedPath); err != nil {
		return nil, err
	}
	return readSnapshotFile(root, selectedPath, 10*1024*1024)
}

var _ docgen.AgentRuntime = (*Runtime)(nil)

package agents

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cyr1en/ref0/internal/providers"
)

const (
	receiptSettlementTimeout = 5 * time.Second
	reservationSafetyMargin  = 4 * time.Hour
	maximumExecutionDuration = executionReservationTTL - reservationSafetyMargin
)

type EngineOptions struct {
	Clock            func() time.Time
	ExecutionTimeout time.Duration
}

type Engine struct {
	repository       ExecutionRepository
	digester         RequestDigester
	model            Model
	clock            func() time.Time
	executionTimeout time.Duration
}

func NewEngine(repository ExecutionRepository, digester RequestDigester, model Model, options EngineOptions) (*Engine, error) {
	if repository == nil || digester == nil || model == nil {
		return nil, errors.New("agent executor dependencies are incomplete")
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.ExecutionTimeout == 0 {
		options.ExecutionTimeout = maximumExecutionDuration
	}
	if options.ExecutionTimeout < 0 || options.ExecutionTimeout > maximumExecutionDuration {
		return nil, errors.New("agent execution timeout is outside its reservation bound")
	}
	return &Engine{
		repository: repository, digester: digester, model: model, clock: options.Clock,
		executionTimeout: options.ExecutionTimeout,
	}, nil
}

func (engine *Engine) Execute(ctx context.Context, raw ExecuteRequest, authorizer Authorizer) (ExecuteResult, error) {
	request, err := normalizeExecuteRequest(raw)
	if err != nil || authorizer == nil {
		if err == nil {
			err = fmt.Errorf("%w: corpus authorizer is required", ErrExecutionInvalid)
		}
		return ExecuteResult{}, err
	}
	executionContext, cancelExecution := context.WithTimeout(ctx, engine.executionTimeout)
	defer cancelExecution()
	ctx = executionContext
	capture, err := engine.repository.Capture(ctx, request.Selector)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("%w: capture failed", ErrExecutionUnavailable)
	}
	settlementAttempted := false
	settle := func(record RunRecord) (RunID, error) {
		settlementAttempted = true
		return engine.recordRun(ctx, record)
	}
	defer func() {
		if !settlementAttempted {
			engine.releaseCapture(ctx, capture)
		}
	}()
	if err = validateCapture(capture, request.Selector); err != nil {
		return ExecuteResult{}, err
	}
	digest, err := engine.digester.DigestRequest(capture, request)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("%w: request digest failed", ErrExecutionUnavailable)
	}
	started := engine.clock()
	scope := AuthorizationScope{
		AgentID: capture.Agent.ID, AgentVersionID: capture.Agent.CurrentVersionID,
		AgentResourceVersion: capture.Agent.Version, AgentKey: capture.Agent.Key,
		Origin: request.Origin, Subject: request.Subject, EffectiveAccess: capture.EffectiveAccess,
		Corpus: make([]AuthorizedCorpusMember, len(capture.KnowledgeBases)),
	}
	for index, knowledgeBase := range capture.KnowledgeBases {
		scope.Corpus[index] = AuthorizedCorpusMember{
			Position: knowledgeBase.Position, KnowledgeBaseID: knowledgeBase.ID,
			KnowledgeBaseVersion: knowledgeBase.ResourceVersion, AccessPolicy: knowledgeBase.AccessPolicy,
			WikiVersionID: knowledgeBase.WikiVersionID, SourceScopeDigest: knowledgeBase.SourceScopeDigest,
		}
	}
	if authorizeErr := authorizer.Authorize(ctx, scope); authorizeErr != nil {
		result, recordErr := engine.fail(settle, capture, request, digest, nil, nil, elapsedMilliseconds(started, engine.clock()), "authorization_denied", ErrExecutionForbidden)
		if recordErr != nil && !errors.Is(recordErr, ErrExecutionForbidden) {
			return result, recordErr
		}
		return result, ErrExecutionForbidden
	}
	if err = engine.repository.AssertFresh(ctx, capture); err != nil {
		return engine.fail(settle, capture, request, digest, nil, nil, elapsedMilliseconds(started, engine.clock()), "capture_stale_before_retrieval", err)
	}
	tools, err := NewToolRuntime(engine.repository, capture)
	if err != nil {
		return engine.fail(settle, capture, request, digest, nil, nil, elapsedMilliseconds(started, engine.clock()), "tool_scope_failed", err)
	}
	question := lastUserMessage(request.Messages)
	evidence, err := tools.InitialEvidence(ctx, question, capture.Model.AnswerMode)
	if err != nil {
		return engine.fail(settle, capture, request, digest, tools.CallAudit(), nil, elapsedMilliseconds(started, engine.clock()), "initial_retrieval_failed", err)
	}
	modelLimit, err := capture.Model.MaxOutputTokens()
	if err != nil {
		return engine.fail(settle, capture, request, digest, tools.CallAudit(), nil, elapsedMilliseconds(started, engine.clock()), "model_limit_failed", err)
	}
	maxOutput := minInt(modelLimit, int(capture.Agent.CurrentVersion.Configuration.MaxAnswerTokens))
	if request.MaxTokens > 0 {
		maxOutput = minInt(maxOutput, int(request.MaxTokens))
	}
	contextWindow, err := capture.Model.ContextWindowTokens()
	if err != nil {
		return engine.fail(settle, capture, request, digest, tools.CallAudit(), nil, elapsedMilliseconds(started, engine.clock()), "model_context_failed", err)
	}
	system := systemPrompt(capture, tools.Manifest())
	selectedMessages, selectedEvidence, budgetUsage, err := budgetInitial(contextWindow, maxOutput, system, request.Messages, evidence)
	if err != nil {
		return engine.fail(settle, capture, request, digest, tools.CallAudit(), nil, elapsedMilliseconds(started, engine.clock()), "prompt_budget_failed", err)
	}
	user, err := userPrompt(selectedMessages, selectedEvidence)
	if err != nil {
		return engine.fail(settle, capture, request, digest, tools.CallAudit(), nil, elapsedMilliseconds(started, engine.clock()), "prompt_build_failed", err)
	}
	messages := []ModelMessage{{Role: "system", Content: system}, {Role: "user", Content: user}}
	definitions := []ToolDefinition(nil)
	if capture.Model.AnswerMode == ToolCalling {
		definitions = toolDefinitions(capture.Agent.CurrentVersion.Configuration.EvidenceAccess)
	}
	usage := cloneUsage(budgetUsage)
	maxTurns := int(capture.Agent.CurrentVersion.Configuration.MaxToolCalls) + 1
	if capture.Model.AnswerMode == SinglePass {
		maxTurns = 1
	}
	for turnIndex := 0; turnIndex < maxTurns; turnIndex++ {
		if err = engine.repository.AssertFresh(ctx, capture); err != nil {
			return engine.fail(settle, capture, request, digest, tools.CallAudit(), usage, elapsedMilliseconds(started, engine.clock()), "capture_stale_before_model", err)
		}
		turn, completeErr := engine.model.Complete(ctx, ModelRequest{
			Capture: capture, Messages: cloneModelMessages(messages), Tools: append([]ToolDefinition(nil), definitions...),
			MaxOutputTokens: maxOutput,
			BeforeRequest: func(current context.Context) error {
				return engine.repository.AssertFresh(current, capture)
			},
		})
		addUsage(usage, turn.Usage)
		if completeErr != nil {
			return engine.fail(settle, capture, request, digest, tools.CallAudit(), usage,
				elapsedMilliseconds(started, engine.clock()), providerFailureCategory(completeErr), completeErr)
		}
		if turn.Usage["model_calls"] == 0 {
			usage["model_calls"]++
		}
		if (turn.Draft != nil) == (len(turn.ToolCalls) != 0) {
			return engine.fail(settle, capture, request, digest, tools.CallAudit(), usage, elapsedMilliseconds(started, engine.clock()), "model_turn_invalid", ErrExecutionUnavailable)
		}
		if turn.Draft != nil {
			if err = engine.repository.AssertSecurityFresh(ctx, capture); err != nil {
				return engine.fail(settle, capture, request, digest, tools.CallAudit(), usage, elapsedMilliseconds(started, engine.clock()), "security_scope_changed", err)
			}
			status, markdown, citations := validateDraft(*turn.Draft, tools.Citations(), capture.Agent.CurrentVersion.Configuration.RefusalMarkdown)
			deliveryMarkdown, deliveryCitations := presentExecutionResult(
				capture.EffectiveAccess,
				markdown,
				citations,
				tools.Citations(),
			)
			latency := elapsedMilliseconds(started, engine.clock())
			record := RunRecord{
				Capture: capture, Origin: request.Origin, Subject: request.Subject, RequestDigest: digest,
				Outcome: status, Usage: cloneUsage(usage), LatencyMS: latency, ToolCalls: tools.CallAudit(),
				Citations: append([]Citation(nil), citations...), CompletedAt: engine.clock(),
			}
			runID, recordErr := settle(record)
			if recordErr != nil {
				return ExecuteResult{}, recordErr
			}
			if runID != capture.RunID {
				return ExecuteResult{}, ErrExecutionConflict
			}
			return ExecuteResult{
				RunID: runID, Status: status, Markdown: deliveryMarkdown, Citations: deliveryCitations,
				Usage: cloneUsage(usage), LatencyMS: latency,
			}, nil
		}
		if capture.Model.AnswerMode != ToolCalling {
			return engine.fail(settle, capture, request, digest, tools.CallAudit(), usage, elapsedMilliseconds(started, engine.clock()), "single_pass_tool_call", ErrExecutionUnavailable)
		}
		messages = append(messages, ModelMessage{Role: "assistant", ToolCalls: append([]ToolCall(nil), turn.ToolCalls...)})
		for _, call := range turn.ToolCalls {
			result, dispatchErr := tools.Dispatch(ctx, call)
			if dispatchErr != nil {
				return engine.fail(settle, capture, request, digest, tools.CallAudit(), usage, elapsedMilliseconds(started, engine.clock()), "tool_call_failed", dispatchErr)
			}
			content, boundErr := boundedToolResult(result)
			if boundErr != nil {
				return engine.fail(settle, capture, request, digest, tools.CallAudit(), usage, elapsedMilliseconds(started, engine.clock()), "tool_result_failed", boundErr)
			}
			messages = append(messages, ModelMessage{Role: "tool", ToolCallID: call.ID, Content: content})
		}
	}
	return engine.fail(settle, capture, request, digest, tools.CallAudit(), usage, elapsedMilliseconds(started, engine.clock()), "tool_loop_exhausted", ErrExecutionUnavailable)
}

func providerFailureCategory(err error) string {
	switch {
	case errors.Is(err, ErrModelRateLimit):
		return "provider_rate_limit"
	case errors.Is(err, ErrModelTimeout), errors.Is(err, context.DeadlineExceeded):
		return "provider_timeout"
	case errors.Is(err, ErrModelValidation):
		return "model_validation_failed"
	default:
		return "provider_request_failed"
	}
}

func (engine *Engine) releaseCapture(ctx context.Context, capture ExecutionCapture) {
	releaseContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), receiptSettlementTimeout)
	defer cancel()
	_ = engine.repository.ReleaseCapture(releaseContext, capture)
}

func (engine *Engine) recordRun(ctx context.Context, record RunRecord) (RunID, error) {
	settlementContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), receiptSettlementTimeout)
	defer cancel()
	return engine.repository.RecordRun(settlementContext, record)
}

func (engine *Engine) fail(
	settle func(RunRecord) (RunID, error),
	capture ExecutionCapture,
	request ExecuteRequest,
	digest [32]byte,
	toolCalls []string,
	usage map[string]int,
	latency int,
	category string,
	cause error,
) (ExecuteResult, error) {
	sanitized := "agent_execution:" + category
	record := RunRecord{
		Capture: capture, Origin: request.Origin, Subject: request.Subject, RequestDigest: digest,
		Outcome: CompletionFailed, Usage: cloneUsage(usage), LatencyMS: latency,
		ToolCalls: append([]string(nil), toolCalls...), SanitizedError: &sanitized, CompletedAt: engine.clock(),
	}
	runID, recordErr := settle(record)
	if recordErr != nil {
		return ExecuteResult{}, recordErr
	}
	if runID != capture.RunID {
		return ExecuteResult{}, ErrExecutionConflict
	}
	result := ExecuteResult{RunID: runID, Status: CompletionFailed, Usage: cloneUsage(usage), LatencyMS: latency}
	if errors.Is(cause, ErrExecutionForbidden) {
		return result, ErrExecutionForbidden
	}
	if errors.Is(cause, ErrModelRateLimit) {
		return result, fmt.Errorf("%w: %w", ErrExecutionUnavailable, ErrModelRateLimit)
	}
	return result, fmt.Errorf("%w: %s", ErrExecutionUnavailable, category)
}

func validateCapture(capture ExecutionCapture, selector string) error {
	agent := capture.Agent
	version := agent.CurrentVersion
	configuration := version.Configuration
	model := capture.Model
	profile := model.Profile
	profileVersion := profile.CurrentVersion
	endpoint := model.Endpoint
	invalid := func() error {
		return fmt.Errorf("%w: captured execution is invalid", ErrExecutionUnavailable)
	}
	normalized, configurationErr := NormalizeConfiguration(configuration)
	if capture.RunID == (RunID{}) || capture.Agent.Lifecycle != Active || capture.Agent.Selector() != selector ||
		capture.CapturedAt.IsZero() || agent.CurrentVersionID == (VersionID{}) || agent.Version <= 0 ||
		version.ID != agent.CurrentVersionID || version.AgentID != agent.ID || version.VersionNumber <= 0 ||
		configurationErr != nil || !equalConfiguration(configuration, normalized) ||
		len(capture.KnowledgeBases) == 0 || len(capture.KnowledgeBases) > MaxKnowledgeBases ||
		len(version.Memberships) != len(capture.KnowledgeBases) || len(configuration.KnowledgeBaseIDs) != len(capture.KnowledgeBases) ||
		model.ProfileVersionID == (ModelProfileVersionID{}) || model.ProfileVersionNumber <= 0 ||
		profile.ID != providers.ProfileID(configuration.ModelProfileID) || profile.ID == (providers.ProfileID{}) ||
		profile.EndpointID != endpoint.ID || profile.Version <= 0 || profile.Availability == providers.Unavailable ||
		endpoint.ID == (providers.EndpointID{}) || endpoint.Version <= 0 || endpoint.ConfigurationVersion <= 0 ||
		endpoint.Lifecycle != providers.Active || endpoint.Health != providers.Healthy ||
		profileVersion.ProfileID != profile.ID || [16]byte(profileVersion.ID) != [16]byte(model.ProfileVersionID) ||
		profileVersion.VersionNumber != model.ProfileVersionNumber || profileVersion.ConfigurationVersion != endpoint.ConfigurationVersion ||
		profileVersion.Settings.Transport != providers.ChatCompletions || model.AnswerMode != configuration.AnswerMode ||
		model.ReasoningEffort != configuration.ReasoningEffort {
		return invalid()
	}
	if model.AnswerMode == ToolCalling && (profileVersion.Settings.SupportsTools == nil || !*profileVersion.Settings.SupportsTools) {
		return invalid()
	}
	credentialID := endpoint.Configuration.CredentialID
	if (credentialID == nil) != (model.CapturedCredentialID == nil) ||
		(model.CapturedCredentialID == nil) != (model.CapturedCredentialVersion == nil) ||
		model.CapturedCredentialVersion != nil && *model.CapturedCredentialVersion <= 0 ||
		credentialID != nil && [16]byte(*credentialID) != [16]byte(*model.CapturedCredentialID) {
		return invalid()
	}
	seenKnowledgeBases := make(map[KnowledgeBaseID]struct{}, len(capture.KnowledgeBases))
	seenSourceRevisions := make(map[SourceRevisionID]struct{})
	effectiveAccess := Public
	for index, knowledgeBase := range capture.KnowledgeBases {
		if int(knowledgeBase.Position) != index || knowledgeBase.ID == (KnowledgeBaseID{}) || knowledgeBase.ResourceVersion <= 0 ||
			knowledgeBase.AccessPolicy != Public && knowledgeBase.AccessPolicy != Restricted || knowledgeBase.WikiVersionID == (WikiVersionID{}) ||
			knowledgeBase.DocumentationRunID == (DocumentationRunID{}) || knowledgeBase.SourceScopeDigest == ([32]byte{}) ||
			version.Memberships[index].Position != int32(index) || version.Memberships[index].KnowledgeBaseID != knowledgeBase.ID ||
			configuration.KnowledgeBaseIDs[index] != knowledgeBase.ID {
			return invalid()
		}
		if _, exists := seenKnowledgeBases[knowledgeBase.ID]; exists {
			return invalid()
		}
		seenKnowledgeBases[knowledgeBase.ID] = struct{}{}
		if knowledgeBase.AccessPolicy == Restricted {
			effectiveAccess = Restricted
		}
		for _, source := range knowledgeBase.Sources {
			if source.ID == (SourceID{}) || source.RevisionID == (SourceRevisionID{}) {
				return invalid()
			}
			if _, exists := seenSourceRevisions[source.RevisionID]; exists {
				return invalid()
			}
			seenSourceRevisions[source.RevisionID] = struct{}{}
		}
	}
	if capture.EffectiveAccess != effectiveAccess {
		return invalid()
	}
	return nil
}

func lastUserMessage(messages []Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == RoleUser {
			return messages[index].Content
		}
	}
	return ""
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func cloneUsage(value map[string]int) map[string]int {
	result := make(map[string]int, len(value))
	for key, item := range value {
		if item >= 0 {
			result[key] = item
		}
	}
	return result
}

func addUsage(destination map[string]int, source map[string]int) {
	for key, value := range source {
		if value >= 0 {
			destination[key] += value
		}
	}
}

func cloneModelMessages(values []ModelMessage) []ModelMessage {
	result := make([]ModelMessage, len(values))
	for index, value := range values {
		result[index] = value
		result[index].ToolCalls = append([]ToolCall(nil), value.ToolCalls...)
	}
	return result
}

func elapsedMilliseconds(start, end time.Time) int {
	value := end.Sub(start).Milliseconds()
	if value < 0 {
		return 0
	}
	return int(value)
}

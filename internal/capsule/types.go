// Package capsule owns the trusted host side of the isolated Pi runtime.
package capsule

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/cyr1en/ref0/internal/safenet"
	"github.com/cyr1en/ref0/internal/security"
)

const (
	ProtocolVersion = 1
	RuntimeRevision = "pi-0.84.4-r9"

	fetchFrameReserve = 8_192
)

var (
	ErrInvocation = errors.New("capsule attempt failed safely")
	ErrBinding    = errors.New("documentation model is unsupported")

	headerName    = regexp.MustCompile(`^[!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+$`)
	secretHeaders = map[string]struct{}{
		"authorization": {}, "cookie": {}, "proxy-authorization": {}, "x-api-key": {},
	}
	samplingNames = map[string]struct{}{
		"temperature": {}, "top_p": {}, "frequency_penalty": {},
		"presence_penalty": {}, "seed": {}, "stop": {},
	}
	hostRequestNames = map[string]struct{}{
		"max_tokens": {}, "messages": {}, "model": {}, "n": {},
		"parallel_tool_calls": {}, "stream": {}, "stream_options": {},
		"tool_choice": {}, "tools": {},
	}
)

type Role string

const (
	Planner    Role = "PLANNER"
	PageWriter Role = "PAGE_WRITER"
)

type AttemptState string

const (
	ReadyModel    AttemptState = "READY_MODEL"
	ModelInFlight AttemptState = "MODEL_IN_FLIGHT"
	AwaitTool     AttemptState = "AWAIT_TOOL"
	AwaitComplete AttemptState = "AWAIT_COMPLETE"
	Terminal      AttemptState = "TERMINAL"
	Failed        AttemptState = "FAILED"
	Cancelled     AttemptState = "CANCELLED"
)

type Usage struct {
	ModelCalls           int `json:"model_calls"`
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	TotalTokens          int `json:"total_tokens"`
	TruncatedToolResults int `json:"-"`
}

func (usage Usage) Add(other Usage) Usage {
	return Usage{
		ModelCalls:           usage.ModelCalls + other.ModelCalls,
		InputTokens:          usage.InputTokens + other.InputTokens,
		OutputTokens:         usage.OutputTokens + other.OutputTokens,
		TotalTokens:          usage.TotalTokens + other.TotalTokens,
		TruncatedToolResults: usage.TruncatedToolResults + other.TruncatedToolResults,
	}
}

type Invocation struct {
	Output map[string]any
	Usage  Usage
}

type InvocationError struct {
	Message string
	Usage   Usage
}

func (err *InvocationError) Error() string {
	if err.Message != "" {
		return err.Message
	}
	return ErrInvocation.Error()
}

func (err *InvocationError) Is(target error) bool { return target == ErrInvocation }

type ToolHandler func(context.Context, map[string]any) (any, error)

type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Handler     ToolHandler
}

type Limits struct {
	MaxFrameBytes     int
	MaxAggregateBytes int
	MaxStringBytes    int
	MaxDepth          int
	MaxKeys           int
	MaxFetchBodyBytes int
	MaxFetches        int
	MaxModelRequests  int
	AttemptTimeout    time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		MaxFrameBytes: 2_097_152, MaxAggregateBytes: 16_777_216,
		MaxStringBytes: 1_048_576, MaxDepth: 32, MaxKeys: 50_000,
		MaxFetchBodyBytes: 1_048_576, MaxFetches: 64, MaxModelRequests: 16,
		AttemptTimeout: 660 * time.Second,
	}
}

func (limits Limits) validate() error {
	if limits.MaxFrameBytes < 1_024 || limits.MaxFrameBytes > 4_194_304 ||
		limits.MaxAggregateBytes < max(4_096, limits.MaxFrameBytes) || limits.MaxAggregateBytes > 33_554_432 ||
		limits.MaxStringBytes < 256 || limits.MaxStringBytes > 2_097_152 ||
		limits.MaxDepth < 4 || limits.MaxDepth > 64 || limits.MaxKeys < 32 || limits.MaxKeys > 100_000 ||
		limits.MaxFetchBodyBytes < 1_024 || limits.MaxFetchBodyBytes > 1_048_576 ||
		limits.MaxModelRequests < 1 || limits.MaxModelRequests > limits.MaxFetches || limits.MaxFetches > 1_000 ||
		limits.MaxStringBytes > limits.MaxFrameBytes ||
		4*((limits.MaxFetchBodyBytes+2)/3)+fetchFrameReserve > limits.MaxFrameBytes ||
		limits.AttemptTimeout < time.Second || limits.AttemptTimeout > 660*time.Second {
		return errors.New("capsule protocol limits are invalid")
	}
	return nil
}

type CredentialReference struct {
	ID            credentials.ID
	SecretVersion int32
}

type Binding struct {
	ModelID                string
	BaseURL                string
	ChatCompletionsPath    string
	Headers                map[string]string
	BodyOptions            map[string]any
	ContextWindow          int
	MaxOutputTokens        int
	ReasoningEffort        providers.Effort
	ReasoningOptions       map[string]any
	Timeout                time.Duration
	MaxRetries             int
	Credential             *CredentialReference
	CapsuleRuntimeRevision string
	Limits                 Limits
	NetworkPolicy          safenet.Policy
}

type ProviderCapture struct {
	Role                         providers.ModelRole
	ProfileID                    providers.ProfileID
	ProfileVersionID             providers.ProfileVersionID
	ProfileVersion               int32
	EndpointID                   providers.EndpointID
	EndpointConfigurationVersion int32
	CredentialVersion            *int32
	ReasoningEffort              providers.Effort
}

type SecretReader interface {
	Read(context.Context, credentials.ID, credentials.Kind, int32) (*security.SecretValue, error)
}

type FactoryOptions struct {
	Resolver  safenet.Resolver
	TLSConfig *tls.Config
}

func CompileBinding(captured ProviderCapture, profile providers.Profile, endpoint providers.Endpoint) (Binding, Role, error) {
	role := Role("")
	switch captured.Role {
	case providers.DocumentationPlanner:
		role = Planner
	case providers.DocumentationWriter:
		role = PageWriter
	default:
		return Binding{}, "", ErrBinding
	}
	settings := profile.CurrentVersion.Settings
	if profile.ID != captured.ProfileID || profile.EndpointID != captured.EndpointID ||
		profile.CurrentVersion.ID != captured.ProfileVersionID || profile.CurrentVersion.VersionNumber != captured.ProfileVersion ||
		profile.CurrentVersion.ConfigurationVersion != captured.EndpointConfigurationVersion ||
		endpoint.ID != captured.EndpointID || endpoint.ConfigurationVersion != captured.EndpointConfigurationVersion ||
		endpoint.Lifecycle != providers.Active || profile.Availability == providers.Unavailable ||
		settings.Transport != providers.ChatCompletions || !knownTrue(settings.SupportsStreaming) ||
		!knownTrue(settings.SupportsTools) || !knownTrue(settings.SupportsStructuredOutput) ||
		settings.ContextWindowTokens == nil || settings.MaxOutputTokens == nil ||
		settings.TimeoutSeconds < providers.MinModelTimeoutSeconds || settings.TimeoutSeconds > providers.MaxModelTimeoutSeconds ||
		endpoint.Configuration.ChatCompletionsPath != "chat/completions" {
		return Binding{}, "", ErrBinding
	}

	reasoning, err := reasoningOptions(settings, captured.ReasoningEffort)
	if err != nil {
		return Binding{}, "", ErrBinding
	}
	body, err := validateSamplingOptions(settings.ExtraBody)
	if err != nil || body["temperature"] != nil && !knownTrue(settings.SupportsTemperature) {
		return Binding{}, "", ErrBinding
	}
	var credential *CredentialReference
	configuredCredential := endpoint.Configuration.CredentialID
	if configuredCredential == nil {
		if captured.CredentialVersion != nil {
			return Binding{}, "", ErrBinding
		}
	} else {
		if captured.CredentialVersion == nil || *captured.CredentialVersion < 1 {
			return Binding{}, "", ErrBinding
		}
		credential = &CredentialReference{ID: *configuredCredential, SecretVersion: *captured.CredentialVersion}
	}
	binding := Binding{
		ModelID: profile.ModelID, BaseURL: endpoint.Configuration.BaseURL,
		ChatCompletionsPath: endpoint.Configuration.ChatCompletionsPath,
		Headers:             map[string]string(endpoint.Configuration.Headers), BodyOptions: body,
		ContextWindow: int(*settings.ContextWindowTokens), MaxOutputTokens: int(*settings.MaxOutputTokens),
		ReasoningEffort: captured.ReasoningEffort, ReasoningOptions: reasoning,
		Timeout: time.Duration(settings.TimeoutSeconds) * time.Second, MaxRetries: int(settings.MaxRetries),
		Credential: credential, CapsuleRuntimeRevision: RuntimeRevision, Limits: DefaultLimits(),
		NetworkPolicy: safenet.Policy{
			AllowPrivateAddresses: endpoint.Configuration.AllowPrivateNetwork,
			AllowPlainHTTP:        endpoint.Configuration.AllowHTTP,
		},
	}
	normalized, err := normalizeBinding(binding)
	if err != nil {
		return Binding{}, "", ErrBinding
	}
	return normalized, role, nil
}

func normalizeBinding(binding Binding) (Binding, error) {
	if binding.Limits == (Limits{}) {
		binding.Limits = DefaultLimits()
	}
	if err := binding.Limits.validate(); err != nil {
		return Binding{}, err
	}
	if binding.ModelID == "" || binding.ModelID != strings.TrimSpace(binding.ModelID) ||
		len([]byte(binding.ModelID)) > 512 || !utf8.ValidString(binding.ModelID) ||
		binding.CapsuleRuntimeRevision == "" || binding.CapsuleRuntimeRevision != strings.TrimSpace(binding.CapsuleRuntimeRevision) ||
		len([]byte(binding.CapsuleRuntimeRevision)) > 128 || binding.CapsuleRuntimeRevision != RuntimeRevision {
		return Binding{}, errors.New("capsule model binding is invalid")
	}
	base, err := safenet.NormalizeBaseURL(binding.BaseURL, binding.NetworkPolicy)
	if err != nil {
		return Binding{}, err
	}
	path, err := safenet.NormalizeRelativePath(binding.ChatCompletionsPath)
	if err != nil || path != "chat/completions" {
		return Binding{}, errors.New("first capsule slice requires chat/completions")
	}
	if binding.ContextWindow < 1_024 || binding.ContextWindow > 10_000_000 ||
		binding.MaxOutputTokens < 1 || binding.MaxOutputTokens > binding.ContextWindow || binding.MaxOutputTokens > 1_000_000 ||
		binding.Timeout < safenet.MinModelTimeout || binding.Timeout > safenet.MaxModelTimeout ||
		binding.MaxRetries < 0 || binding.MaxRetries > 10 {
		return Binding{}, errors.New("custom model execution settings are invalid")
	}
	headers := make(map[string]string, len(binding.Headers))
	for rawName, value := range binding.Headers {
		name := strings.ToLower(rawName)
		_, secret := secretHeaders[name]
		if rawName == "" || rawName != strings.TrimSpace(rawName) || !headerName.MatchString(rawName) ||
			secret || strings.HasPrefix(name, "proxy-") || len([]byte(value)) > 8_192 ||
			strings.IndexFunc(value, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
			return Binding{}, errors.New("custom model non-secret headers are invalid")
		}
		headers[name] = value
	}
	body, err := validateSamplingOptions(binding.BodyOptions)
	if err != nil {
		return Binding{}, err
	}
	reasoning, err := normalizeJSONObject(binding.ReasoningOptions, 65_536, complexityLimits{32, 65_536, 50_000})
	if err != nil || len(reasoning) > 1 || (binding.ReasoningEffort == providers.EffortNone) != (len(reasoning) == 0) {
		return Binding{}, errors.New("custom model reasoning options are invalid")
	}
	for name := range reasoning {
		if name == "" {
			return Binding{}, errors.New("custom model reasoning options are invalid")
		}
		if _, denied := samplingNames[name]; denied {
			return Binding{}, errors.New("custom model reasoning options are invalid")
		}
		if _, denied := hostRequestNames[name]; denied {
			return Binding{}, errors.New("custom model reasoning options are invalid")
		}
	}
	switch binding.ReasoningEffort {
	case providers.EffortNone, providers.EffortMinimal, providers.EffortLow,
		providers.EffortMedium, providers.EffortHigh, providers.EffortMax:
	default:
		return Binding{}, errors.New("custom model reasoning effort is invalid")
	}
	if binding.Credential != nil && binding.Credential.SecretVersion < 1 {
		return Binding{}, errors.New("API-key reference is invalid")
	}
	binding.BaseURL, binding.ChatCompletionsPath = base.String(), path
	binding.Headers, binding.BodyOptions, binding.ReasoningOptions = headers, body, reasoning
	return binding, nil
}

func validateSamplingOptions(value map[string]any) (map[string]any, error) {
	options, err := normalizeJSONObject(value, 65_536, complexityLimits{32, 65_536, 50_000})
	if err != nil {
		return nil, errors.New("custom model sampling options are invalid")
	}
	for name := range options {
		if _, ok := samplingNames[name]; !ok {
			return nil, errors.New("custom model sampling options are invalid")
		}
	}
	for name, bounds := range map[string][2]float64{
		"temperature": {0, 2}, "top_p": {0, 1},
		"frequency_penalty": {-2, 2}, "presence_penalty": {-2, 2},
	} {
		if item, exists := options[name]; exists {
			number, ok := jsonNumber(item)
			if !ok || number < bounds[0] || number > bounds[1] || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, errors.New("custom model sampling options are invalid")
			}
		}
	}
	if item, exists := options["seed"]; exists {
		seed, ok := jsonInteger(item)
		if !ok || seed < -(1<<31) || seed >= 1<<31 {
			return nil, errors.New("custom model sampling options are invalid")
		}
	}
	if item, exists := options["stop"]; exists {
		if text, ok := item.(string); ok {
			item = []any{text}
			options["stop"] = item
		}
		stops, ok := item.([]any)
		if !ok || len(stops) < 1 || len(stops) > 4 {
			return nil, errors.New("custom model sampling options are invalid")
		}
		for _, raw := range stops {
			stop, ok := raw.(string)
			if !ok || stop == "" || len([]byte(stop)) > 1_024 {
				return nil, errors.New("custom model sampling options are invalid")
			}
		}
	}
	return options, nil
}

func reasoningOptions(settings providers.Settings, effort providers.Effort) (map[string]any, error) {
	if effort == providers.EffortNone {
		return map[string]any{}, nil
	}
	value := strings.ToLower(string(effort))
	switch settings.ReasoningTransport {
	case providers.ReasoningEffort:
		return map[string]any{"reasoning_effort": value}, nil
	case providers.CustomReasoning:
		if settings.ReasoningMapping == nil {
			return nil, ErrBinding
		}
		mapped, ok := settings.ReasoningMapping.Values[value]
		if !ok {
			return nil, ErrBinding
		}
		return map[string]any{settings.ReasoningMapping.Field: mapped}, nil
	default:
		return nil, ErrBinding
	}
}

func knownTrue(value *bool) bool { return value != nil && *value }

func bindingError(detail string) error { return fmt.Errorf("%w: %s", ErrBinding, detail) }

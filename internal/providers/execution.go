package providers

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/safenet"
	"github.com/cyr1en/ref0/internal/security"
)

const (
	redacted               = "[REDACTED]"
	probePrompt            = "Reply OK."
	toolProbePrompt        = "Call report_probe with status set to ok."
	structuredProbePrompt  = "Return a JSON object with ok set to true."
	providerProbeTool      = "report_probe"
	providerProbeMaxTokens = 64
)

var secretJSONParts = []string{"apikey", "authorization", "cookie", "credential", "password", "secret", "token"}

type SecretReader interface {
	Read(context.Context, credentials.ID, credentials.Kind, int32) (*security.SecretValue, error)
}

type NetworkClient interface {
	GetJSON(context.Context, string) (any, int, error)
	PostJSON(context.Context, string, any) (any, int, error)
	Exchange(context.Context, string, string, map[string]string, []byte, int) (safenet.Response, error)
	CloseIdleConnections()
}

type ClientFactory func(Configuration, map[string]string) (NetworkClient, error)

type ExecutionOptions struct {
	Resolver  safenet.Resolver
	TLSConfig *tls.Config
}

type Execution struct {
	secrets SecretReader
	client  ClientFactory
}

func NewExecution(secrets SecretReader, options ExecutionOptions) (*Execution, error) {
	if secrets == nil {
		return nil, errors.New("provider execution dependencies are incomplete")
	}
	factory := func(configuration Configuration, headers map[string]string) (NetworkClient, error) {
		return safenet.NewClient(configuration.BaseURL, safenet.Policy{
			AllowPrivateAddresses: configuration.AllowPrivateNetwork,
			AllowPlainHTTP:        configuration.AllowHTTP,
		}, safenet.ClientOptions{Headers: headers, Resolver: options.Resolver, TLSConfig: options.TLSConfig})
	}
	return &Execution{secrets: secrets, client: factory}, nil
}

// NewExecutionWithFactory is the narrow capsule boundary used by tests and by
// alternative runtimes. The provider domain never executes documentation
// prompts itself; it only performs catalog discovery and capability probes.
func NewExecutionWithFactory(secrets SecretReader, factory ClientFactory) (*Execution, error) {
	if secrets == nil || factory == nil {
		return nil, errors.New("provider execution dependencies are incomplete")
	}
	return &Execution{secrets: secrets, client: factory}, nil
}

func (execution *Execution) Discover(ctx context.Context, endpoint Endpoint, run DiscoveryRun) (CompleteDiscovery, error) {
	if endpoint.ID != run.EndpointID || endpoint.Lifecycle != Active || endpoint.ConfigurationVersion != run.CapturedConfigurationVersion {
		return CompleteDiscovery{RunID: run.ID, SanitizedError: "provider_discovery:configuration_changed"}, nil
	}
	headers, secret, err := execution.headers(ctx, endpoint, run.CapturedCredentialVersion)
	if errors.Is(err, credentials.ErrSecretUnavailable) {
		return CompleteDiscovery{RunID: run.ID, SanitizedError: "provider_discovery:credential_unavailable"}, nil
	}
	if err != nil {
		return CompleteDiscovery{}, err
	}
	client, err := execution.client(endpoint.Configuration, headers)
	if err != nil {
		return CompleteDiscovery{RunID: run.ID, SanitizedError: "provider_discovery:invalid_configuration"}, nil
	}
	defer client.CloseIdleConnections()
	payload, status, err := client.GetJSON(ctx, endpoint.Configuration.ModelsPath)
	if err != nil {
		return discoveryRequestFailure(run.ID, endpoint.Configuration.BaseURL, err), nil
	}
	safe, err := sanitizeProviderJSON(payload, secret)
	if err != nil {
		requestError := &safenet.RequestError{Code: safenet.InvalidJSON, HTTPStatus: status}
		return discoveryRequestFailure(run.ID, endpoint.Configuration.BaseURL, requestError), nil
	}
	models, err := safenet.ValidateModelCatalog(safe)
	if err != nil {
		requestError := &safenet.RequestError{Code: safenet.InvalidJSON, HTTPStatus: status}
		return discoveryRequestFailure(run.ID, endpoint.Configuration.BaseURL, requestError), nil
	}
	modelIDs := make([]string, 0, len(models))
	seen := map[string]struct{}{}
	for _, model := range models {
		modelID := model["id"].(string)
		if strings.Contains(modelID, redacted) {
			requestError := &safenet.RequestError{Code: safenet.InvalidJSON, HTTPStatus: status}
			return discoveryRequestFailure(run.ID, endpoint.Configuration.BaseURL, requestError), nil
		}
		if _, exists := seen[modelID]; exists {
			requestError := &safenet.RequestError{Code: safenet.InvalidJSON, HTTPStatus: status}
			return discoveryRequestFailure(run.ID, endpoint.Configuration.BaseURL, requestError), nil
		}
		seen[modelID] = struct{}{}
		modelIDs = append(modelIDs, modelID)
	}
	object, ok := safe.(map[string]any)
	if !ok {
		requestError := &safenet.RequestError{Code: safenet.InvalidJSON, HTTPStatus: status}
		return discoveryRequestFailure(run.ID, endpoint.Configuration.BaseURL, requestError), nil
	}
	tlsVerified, authentication := strings.HasPrefix(strings.ToLower(endpoint.Configuration.BaseURL), "https://"), true
	httpStatus := int32(status)
	return CompleteDiscovery{
		RunID: run.ID, ModelIDs: modelIDs, RawResponse: object,
		TLSVerified: &tlsVerified, AuthenticationSucceeded: &authentication, HTTPStatus: &httpStatus,
	}, nil
}

func (execution *Execution) Probe(ctx context.Context, endpoint Endpoint, profile Profile, run ProbeRun) (CompleteProbe, error) {
	if profile.ID != run.ProfileID || profile.EndpointID != endpoint.ID ||
		profile.CurrentVersion.ID != run.CapturedProfileVersionID ||
		profile.CurrentVersion.ConfigurationVersion != endpoint.ConfigurationVersion ||
		endpoint.Lifecycle != Active || endpoint.ConfigurationVersion != run.CapturedConfigurationVersion {
		return CompleteProbe{RunID: run.ID, SanitizedError: "provider_probe:configuration_changed"}, nil
	}
	if profile.CurrentVersion.Settings.Transport != ChatCompletions {
		return CompleteProbe{RunID: run.ID, SanitizedError: "provider_probe:unsupported_transport"}, nil
	}
	findings := ProbeFindings{}
	diagnostics := map[string]any{}
	base := map[string]any{
		"model":                 profile.ModelID,
		"messages":              []any{map[string]any{"role": "user", "content": probePrompt}},
		"max_completion_tokens": providerProbeMaxTokens,
	}
	for _, check := range run.SelectedChecks {
		headers, _, err := execution.headers(ctx, endpoint, run.CapturedCredentialVersion)
		if errors.Is(err, credentials.ErrSecretUnavailable) {
			return CompleteProbe{RunID: run.ID, SanitizedError: "provider_probe:credential_unavailable"}, nil
		}
		if err != nil {
			return CompleteProbe{}, err
		}
		client, err := execution.client(endpoint.Configuration, headers)
		if err != nil {
			return CompleteProbe{RunID: run.ID, SanitizedError: "provider_probe:invalid_configuration"}, nil
		}
		succeeded, chunks, err := executeProbeCheck(ctx, client, endpoint.Configuration.ChatCompletionsPath, check, base)
		client.CloseIdleConnections()
		if err != nil {
			var requestError *safenet.RequestError
			if errors.As(err, &requestError) {
				return CompleteProbe{RunID: run.ID, SanitizedError: "provider_probe:" + string(requestError.Code), Retryable: requestError.Retryable}, nil
			}
			return CompleteProbe{}, err
		}
		switch check {
		case ProbeChat:
			findings.ChatSucceeded = boolPointer(succeeded)
			diagnostics["chat"] = map[string]any{"succeeded": succeeded}
		case ProbeStreaming:
			findings.SupportsStreaming = boolPointer(succeeded)
			diagnostics["streaming"] = map[string]any{"succeeded": succeeded, "chunk_count": chunks}
		case ProbeTools:
			findings.SupportsTools = boolPointer(succeeded)
			diagnostics["tools"] = map[string]any{"succeeded": succeeded}
		case ProbeStructuredOutput:
			findings.SupportsStructuredOutput = boolPointer(succeeded)
			diagnostics["structured_output"] = map[string]any{"succeeded": succeeded}
		}
	}
	return CompleteProbe{RunID: run.ID, Findings: &findings, RawResponse: map[string]any{"checks": diagnostics}}, nil
}

func (execution *Execution) headers(ctx context.Context, endpoint Endpoint, capturedVersion *int32) (map[string]string, string, error) {
	headers := make(map[string]string, len(endpoint.Configuration.Headers)+1)
	for name, value := range endpoint.Configuration.Headers {
		headers[name] = value
	}
	if endpoint.Configuration.CredentialID == nil {
		if capturedVersion != nil {
			return nil, "", credentials.ErrSecretUnavailable
		}
		return headers, "", nil
	}
	if capturedVersion == nil {
		return nil, "", credentials.ErrSecretUnavailable
	}
	secret, err := execution.secrets.Read(ctx, *endpoint.Configuration.CredentialID, credentials.ProviderAPIKey, *capturedVersion)
	if err != nil {
		return nil, "", err
	}
	revealed := secret.Reveal()
	headers["Authorization"] = "Bearer " + revealed
	return headers, revealed, nil
}

func discoveryRequestFailure(runID DiscoveryRunID, baseURL string, err error) CompleteDiscovery {
	var requestError *safenet.RequestError
	if !errors.As(err, &requestError) {
		return CompleteDiscovery{RunID: runID, SanitizedError: "provider_discovery:invalid_configuration"}
	}
	result := CompleteDiscovery{
		RunID: runID, SanitizedError: "provider_discovery:" + string(requestError.Code), Retryable: requestError.Retryable,
	}
	if requestError.HTTPStatus != 0 {
		status := int32(requestError.HTTPStatus)
		result.HTTPStatus = &status
		tlsVerified := strings.HasPrefix(strings.ToLower(baseURL), "https://")
		result.TLSVerified = &tlsVerified
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			authenticated := false
			result.AuthenticationSucceeded = &authenticated
		} else if status >= 200 && status <= 299 {
			authenticated := true
			result.AuthenticationSucceeded = &authenticated
		}
	}
	return result
}

func executeProbeCheck(ctx context.Context, client NetworkClient, path string, check ProbeCheck, base map[string]any) (bool, int, error) {
	payload := cloneObject(base)
	switch check {
	case ProbeChat:
		value, _, err := client.PostJSON(ctx, path, payload)
		return chatEvidence(value), 0, err
	case ProbeStreaming:
		payload["stream"] = true
		body, err := json.Marshal(payload)
		if err != nil {
			return false, 0, &safenet.RequestError{Code: safenet.InvalidJSON}
		}
		response, err := client.Exchange(ctx, http.MethodPost, path, map[string]string{
			"Content-Type": "application/json", "Accept": "text/event-stream",
		}, body, safenet.MaxBodyBytes)
		if err != nil {
			return false, 0, err
		}
		if response.Status < 200 || response.Status >= 300 {
			return false, 0, &safenet.RequestError{Code: safenet.HTTPStatus,
				Retryable:  response.Status == http.StatusTooManyRequests || response.Status >= 500,
				HTTPStatus: response.Status}
		}
		succeeded, chunks := streamEvidence(response.Body)
		return succeeded, chunks, nil
	case ProbeTools:
		payload["messages"] = []any{map[string]any{"role": "user", "content": toolProbePrompt}}
		payload["tools"] = []any{map[string]any{
			"type": "function", "function": map[string]any{
				"name": providerProbeTool, "description": "Report probe success.",
				"parameters": map[string]any{
					"type": "object", "properties": map[string]any{"status": map[string]any{"type": "string", "const": "ok"}},
					"required": []any{"status"}, "additionalProperties": false,
				},
			},
		}}
		payload["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": providerProbeTool}}
		value, _, err := client.PostJSON(ctx, path, payload)
		return toolEvidence(value), 0, err
	case ProbeStructuredOutput:
		payload["messages"] = []any{map[string]any{"role": "user", "content": structuredProbePrompt}}
		payload["response_format"] = map[string]any{
			"type": "json_schema", "json_schema": map[string]any{
				"name": "provider_probe", "strict": true,
				"schema": map[string]any{
					"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean", "const": true}},
					"required": []any{"ok"}, "additionalProperties": false,
				},
			},
		}
		value, _, err := client.PostJSON(ctx, path, payload)
		return structuredEvidence(value), 0, err
	default:
		return false, 0, &safenet.RequestError{Code: safenet.InvalidJSON}
	}
}

func sanitizeProviderJSON(value any, secret string) (any, error) {
	var sanitize func(any) (any, error)
	redact := func(value string) string {
		if secret == "" {
			return value
		}
		return strings.ReplaceAll(value, secret, redacted)
	}
	sanitize = func(current any) (any, error) {
		switch typed := current.(type) {
		case string:
			return redact(typed), nil
		case []any:
			result := make([]any, len(typed))
			for index, item := range typed {
				value, err := sanitize(item)
				if err != nil {
					return nil, err
				}
				result[index] = value
			}
			return result, nil
		case map[string]any:
			result := make(map[string]any, len(typed))
			for name, item := range typed {
				safeName := redact(name)
				if _, collision := result[safeName]; collision {
					return nil, &safenet.RequestError{Code: safenet.InvalidJSON}
				}
				if secretJSONField(name) {
					result[safeName] = redacted
					continue
				}
				value, err := sanitize(item)
				if err != nil {
					return nil, err
				}
				result[safeName] = value
			}
			return result, nil
		default:
			return current, nil
		}
	}
	safe, err := sanitize(value)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(safe)
	if err != nil || len(raw) > safenet.MaxBodyBytes {
		code := safenet.InvalidJSON
		if len(raw) > safenet.MaxBodyBytes {
			code = safenet.ResponseTooLarge
		}
		return nil, &safenet.RequestError{Code: code}
	}
	return safenet.ParseBoundedJSON(raw)
}

func secretJSONField(name string) bool {
	var normalized strings.Builder
	for _, character := range strings.ToLower(name) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			normalized.WriteRune(character)
		}
	}
	value := normalized.String()
	for _, part := range secretJSONParts {
		if strings.Contains(value, part) {
			return true
		}
	}
	return false
}

func message(value any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	choices, ok := object["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return nil
	}
	result, _ := choice["message"].(map[string]any)
	return result
}

func chatEvidence(value any) bool {
	content, _ := message(value)["content"].(string)
	return strings.TrimSpace(content) != ""
}

func toolEvidence(value any) bool {
	calls, _ := message(value)["tool_calls"].([]any)
	for _, rawCall := range calls {
		call, ok := rawCall.(map[string]any)
		if !ok {
			continue
		}
		function, ok := call["function"].(map[string]any)
		if !ok || function["name"] != providerProbeTool {
			continue
		}
		arguments, ok := function["arguments"].(string)
		if !ok {
			continue
		}
		parsed, err := safenet.ParseBoundedJSON([]byte(arguments))
		if err != nil {
			continue
		}
		object, ok := parsed.(map[string]any)
		if ok && object["status"] == "ok" {
			return true
		}
	}
	return false
}

func structuredEvidence(value any) bool {
	content, ok := message(value)["content"].(string)
	if !ok {
		return false
	}
	parsed, err := safenet.ParseBoundedJSON([]byte(content))
	if err != nil {
		return false
	}
	object, ok := parsed.(map[string]any)
	result, _ := object["ok"].(bool)
	return ok && result
}

func streamEvidence(raw []byte) (bool, int) {
	chunks, content, done := 0, false, false
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "[DONE]" {
			done = true
			continue
		}
		payload, err := safenet.ParseBoundedJSON([]byte(data))
		if err != nil {
			return false, chunks
		}
		object, ok := payload.(map[string]any)
		if !ok {
			return false, chunks
		}
		choices, ok := object["choices"].([]any)
		if !ok || len(choices) == 0 {
			return false, chunks
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			return false, chunks
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			return false, chunks
		}
		if value, ok := delta["content"].(string); ok && value != "" {
			content = true
		}
		chunks++
	}
	return content && done && chunks > 0, chunks
}

func cloneObject(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for name, item := range value {
		result[name] = item
	}
	return result
}

func boolPointer(value bool) *bool { return &value }

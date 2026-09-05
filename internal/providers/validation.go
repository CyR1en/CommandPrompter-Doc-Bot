package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/safenet"
)

const (
	maxDiscoveryCapture = 1_048_576
	maxProbeCapture     = 65_536
)

var (
	headerNamePattern = regexp.MustCompile(`^[!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+$`)
	fieldNamePattern  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
	secretParts       = []string{"authorization", "cookie", "api-key", "apikey", "secret", "token"}
	hopHeaders        = map[string]struct{}{"connection": {}, "content-length": {}, "host": {}}
	reservedBody      = map[string]struct{}{
		"authorization": {}, "input": {}, "max_completion_tokens": {}, "max_output_tokens": {},
		"max_tokens": {}, "messages": {}, "model": {}, "reasoning_effort": {},
		"response_format": {}, "stream": {}, "temperature": {}, "timeout": {},
		"tool_choice": {}, "tools": {},
	}
	metadataFields = []string{
		"model_id", "transport", "context_window_tokens", "max_output_tokens",
		"supports_streaming", "supports_tools", "supports_structured_output",
		"supports_temperature", "reasoning_transport", "reasoning_mapping",
		"timeout_seconds", "max_retries", "max_concurrent_tasks", "extra_body",
	}
)

func (configuration Configuration) Normalize() (Configuration, error) {
	if !boundedTrimmed(configuration.DisplayName, 255) || !boundedTrimmed(configuration.DisplayKey, 255) {
		return Configuration{}, errors.New("provider display name and key are invalid")
	}
	if !boundedTrimmed(configuration.BaseURL, 2048) {
		return Configuration{}, errors.New("provider base URL is invalid")
	}
	policy := safenet.Policy{
		AllowPrivateAddresses: configuration.AllowPrivateNetwork,
		AllowPlainHTTP:        configuration.AllowHTTP,
	}
	base, err := safenet.NormalizeBaseURL(configuration.BaseURL, policy)
	if err != nil {
		return Configuration{}, errors.New("provider base URL is invalid")
	}
	if err := validateRelativePath(configuration.ChatCompletionsPath, false); err != nil {
		return Configuration{}, err
	}
	if configuration.ResponsesPath != nil {
		if err := validateRelativePath(*configuration.ResponsesPath, false); err != nil {
			return Configuration{}, err
		}
	}
	if err := validateRelativePath(configuration.ModelsPath, false); err != nil {
		return Configuration{}, err
	}
	headers, err := normalizeHeaders(configuration.Headers)
	if err != nil {
		return Configuration{}, err
	}
	configuration.BaseURL = base.String()
	configuration.Headers = headers
	return configuration, nil
}

func normalizeHeaders(values NonSecretHeaders) (NonSecretHeaders, error) {
	if len(values) > 32 {
		return nil, errors.New("provider headers must contain at most 32 entries")
	}
	result := make(NonSecretHeaders, len(values))
	seen := make(map[string]struct{}, len(values))
	for name, value := range values {
		normalized := strings.ToLower(name)
		if len(name) == 0 || len(name) > 128 || !headerNamePattern.MatchString(name) {
			return nil, errors.New("provider header name is invalid")
		}
		if _, exists := seen[normalized]; exists {
			return nil, errors.New("provider header name is invalid")
		}
		if secretName(normalized) {
			return nil, errors.New("secret-bearing provider headers are not allowed")
		}
		if utf8.RuneCountInString(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("provider header value is invalid")
		}
		seen[normalized] = struct{}{}
		result[name] = value
	}
	return result, nil
}

func validateRelativePath(value string, optional bool) error {
	if optional && value == "" {
		return nil
	}
	if value == "" || value != strings.TrimSpace(value) || strings.HasPrefix(value, "/") ||
		utf8.RuneCountInString(value) > 255 || strings.Contains(value, "://") ||
		strings.ContainsAny(value, "?#\\") {
		return errors.New("provider path must be a normalized relative path")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("provider path must be a normalized relative path")
		}
	}
	if _, err := safenet.NormalizeRelativePath(value); err != nil {
		return errors.New("provider path must be a normalized relative path")
	}
	return nil
}

func (settings Settings) Normalize() (Settings, error) {
	if settings.Transport != ChatCompletions && settings.Transport != Responses {
		return Settings{}, errors.New("model transport is invalid")
	}
	if settings.ContextWindowTokens != nil && *settings.ContextWindowTokens <= 0 ||
		settings.MaxOutputTokens != nil && *settings.MaxOutputTokens <= 0 {
		return Settings{}, errors.New("known token limits must be positive")
	}
	if settings.TimeoutSeconds < MinModelTimeoutSeconds || settings.TimeoutSeconds > MaxModelTimeoutSeconds {
		return Settings{}, fmt.Errorf("timeout_seconds must be between %d and %d", MinModelTimeoutSeconds, MaxModelTimeoutSeconds)
	}
	if settings.MaxRetries < 0 || settings.MaxRetries > 10 {
		return Settings{}, errors.New("max_retries must be between 0 and 10")
	}
	if settings.MaxConcurrentTasks < MinModelConcurrentTasks || settings.MaxConcurrentTasks > MaxModelConcurrentTasks {
		return Settings{}, fmt.Errorf("max_concurrent_tasks must be between %d and %d", MinModelConcurrentTasks, MaxModelConcurrentTasks)
	}
	if settings.ReasoningTransport != NoReasoning && settings.ReasoningTransport != ReasoningEffort && settings.ReasoningTransport != CustomReasoning {
		return Settings{}, errors.New("reasoning transport is invalid")
	}
	mapping, err := normalizeReasoningMapping(settings.ReasoningMapping)
	if err != nil {
		return Settings{}, err
	}
	if (settings.ReasoningTransport == CustomReasoning) != (mapping != nil) {
		return Settings{}, errors.New("custom reasoning transport requires exactly one field mapping")
	}
	extra, err := normalizeJSONObject(settings.ExtraBody, safenet.MaxBodyBytes)
	if err != nil {
		return Settings{}, errors.New("extra body is not valid JSON")
	}
	for name := range extra {
		normalized := strings.ToLower(name)
		if _, denied := reservedBody[normalized]; denied || containsSecretPart(strings.ReplaceAll(normalized, "_", "-")) {
			return Settings{}, errors.New("reserved or secret provider field is not allowed")
		}
	}
	if mapping != nil {
		if _, conflict := extra[mapping.Field]; conflict {
			return Settings{}, errors.New("custom reasoning field conflicts with extra body")
		}
	}
	if len(settings.MetadataOrigin) != len(metadataFields) {
		return Settings{}, errors.New("metadata_origin must name every model profile field")
	}
	origins := make(map[string]MetadataOrigin, len(settings.MetadataOrigin))
	for _, field := range metadataFields {
		origin, exists := settings.MetadataOrigin[field]
		if !exists || !validOrigin(origin) {
			return Settings{}, errors.New("metadata_origin must name every model profile field")
		}
		origins[field] = origin
	}
	settings.ReasoningMapping = mapping
	settings.ExtraBody = extra
	settings.MetadataOrigin = origins
	return settings, nil
}

func normalizeReasoningMapping(value *CustomReasoningMapping) (*CustomReasoningMapping, error) {
	if value == nil {
		return nil, nil
	}
	normalized := strings.ReplaceAll(strings.ToLower(value.Field), "_", "-")
	if !fieldNamePattern.MatchString(value.Field) || secretName(normalized) {
		return nil, errors.New("custom reasoning field is not allowed")
	}
	if _, denied := reservedBody[strings.ToLower(value.Field)]; denied {
		return nil, errors.New("custom reasoning field is not allowed")
	}
	if len(value.Values) == 0 {
		return nil, errors.New("custom reasoning efforts are invalid")
	}
	allowed := map[string]struct{}{"none": {}, "minimal": {}, "low": {}, "medium": {}, "high": {}, "max": {}}
	for effort := range value.Values {
		if _, exists := allowed[effort]; !exists {
			return nil, errors.New("custom reasoning efforts are invalid")
		}
	}
	values, err := normalizeJSONObject(value.Values, safenet.MaxBodyBytes)
	if err != nil {
		return nil, errors.New("custom reasoning efforts are invalid")
	}
	return &CustomReasoningMapping{Field: value.Field, Values: values}, nil
}

func normalizeJSONObject(value map[string]any, maximum int) (map[string]any, error) {
	if value == nil {
		value = map[string]any{}
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > maximum {
		return nil, errors.New("JSON object is invalid")
	}
	normalized, err := safenet.ParseBoundedJSON(raw)
	if err != nil {
		return nil, err
	}
	object, ok := normalized.(map[string]any)
	if !ok {
		return nil, errors.New("JSON value must be an object")
	}
	return object, nil
}

func DiscoveredUnknownSettings() Settings {
	origins := make(map[string]MetadataOrigin, len(metadataFields))
	for _, field := range metadataFields {
		origins[field] = OriginUnknown
	}
	origins["model_id"] = OriginDiscovered
	return Settings{
		Transport: ChatCompletions, ReasoningTransport: NoReasoning,
		TimeoutSeconds: 60, MaxRetries: 2, MaxConcurrentTasks: 1,
		ExtraBody: map[string]any{}, MetadataOrigin: origins,
	}
}

func NormalizeManualSettings(settings Settings) (Settings, error) {
	settings, err := settings.Normalize()
	if err != nil {
		return Settings{}, err
	}
	origins := make(map[string]MetadataOrigin, len(metadataFields))
	for _, field := range metadataFields {
		origins[field] = OriginOperator
	}
	if settings.ContextWindowTokens == nil {
		origins["context_window_tokens"] = OriginUnknown
	}
	if settings.MaxOutputTokens == nil {
		origins["max_output_tokens"] = OriginUnknown
	}
	if settings.SupportsStreaming == nil {
		origins["supports_streaming"] = OriginUnknown
	}
	if settings.SupportsTools == nil {
		origins["supports_tools"] = OriginUnknown
	}
	if settings.SupportsStructuredOutput == nil {
		origins["supports_structured_output"] = OriginUnknown
	}
	if settings.SupportsTemperature == nil {
		origins["supports_temperature"] = OriginUnknown
	}
	if settings.ReasoningMapping == nil {
		origins["reasoning_mapping"] = OriginUnknown
	}
	settings.MetadataOrigin = origins
	return settings, nil
}

func ApplyOperatorEdit(current, replacement Settings) (Settings, error) {
	replacement, err := replacement.Normalize()
	if err != nil {
		return Settings{}, err
	}
	currentJSON, _ := json.Marshal(settingsPayload(current))
	fields := map[string]any{}
	replacementJSON, _ := json.Marshal(settingsPayload(replacement))
	var currentObject, replacementObject map[string]any
	_ = json.Unmarshal(currentJSON, &currentObject)
	_ = json.Unmarshal(replacementJSON, &replacementObject)
	for _, name := range metadataFields {
		if name == "model_id" {
			continue
		}
		if !jsonEqual(currentObject[name], replacementObject[name]) {
			fields[name] = replacementObject[name]
		}
	}
	origins := cloneOrigins(current.MetadataOrigin)
	for name := range fields {
		origins[name] = OriginOperator
	}
	replacement.MetadataOrigin = origins
	return replacement, nil
}

func MergeProbeFindings(settings Settings, findings ProbeFindings) (Settings, error) {
	if err := findings.Validate(); err != nil {
		return Settings{}, err
	}
	origins := cloneOrigins(settings.MetadataOrigin)
	updates := []struct {
		name  string
		value *bool
		dest  **bool
	}{
		{"supports_streaming", findings.SupportsStreaming, &settings.SupportsStreaming},
		{"supports_tools", findings.SupportsTools, &settings.SupportsTools},
		{"supports_structured_output", findings.SupportsStructuredOutput, &settings.SupportsStructuredOutput},
	}
	for _, update := range updates {
		if update.value != nil && origins[update.name] != OriginOperator {
			copied := *update.value
			*update.dest = &copied
			origins[update.name] = OriginProbed
		}
	}
	settings.MetadataOrigin = origins
	return settings.Normalize()
}

func (findings ProbeFindings) Validate() error {
	if findings.ChatSucceeded == nil && findings.SupportsStreaming == nil && findings.SupportsTools == nil && findings.SupportsStructuredOutput == nil {
		return errors.New("probe findings must contain a capability result")
	}
	return nil
}

func (findings ProbeFindings) RequiredChecks() []ProbeCheck {
	result := []ProbeCheck{}
	if findings.ChatSucceeded != nil {
		result = append(result, ProbeChat)
	}
	if findings.SupportsStreaming != nil {
		result = append(result, ProbeStreaming)
	}
	if findings.SupportsTools != nil {
		result = append(result, ProbeTools)
	}
	if findings.SupportsStructuredOutput != nil {
		result = append(result, ProbeStructuredOutput)
	}
	return result
}

func (command CreateEndpoint) normalize() (CreateEndpoint, error) {
	configuration, err := command.Configuration.Normalize()
	command.Configuration = configuration
	return command, err
}

func (command UpdateEndpoint) normalize() (UpdateEndpoint, error) {
	if command.ExpectedVersion <= 0 {
		return UpdateEndpoint{}, errors.New("expected_version must be positive")
	}
	if command.Lifecycle != Active && command.Lifecycle != Archived {
		return UpdateEndpoint{}, errors.New("provider lifecycle is invalid")
	}
	configuration, err := command.Configuration.Normalize()
	command.Configuration = configuration
	return command, err
}

func (command CreateProfile) normalize() (CreateProfile, error) {
	if err := validateModelID(command.ModelID); err != nil {
		return CreateProfile{}, err
	}
	settings, err := NormalizeManualSettings(command.Settings)
	command.Settings = settings
	return command, err
}

func (command EditProfile) normalize() (EditProfile, error) {
	if command.ExpectedVersion <= 0 {
		return EditProfile{}, errors.New("expected_version must be positive")
	}
	settings, err := command.Settings.Normalize()
	command.Settings = settings
	return command, err
}

func (command ScheduleDiscovery) validate() error {
	if command.ExpectedVersion <= 0 {
		return errors.New("expected_version must be positive")
	}
	return nil
}

func (command CompleteDiscovery) validate() error {
	success := command.RawResponse != nil
	failed := command.SanitizedError != "" && !success
	if success == failed || command.SanitizedError != "" && success {
		return errors.New("discovery completion requires one result or error")
	}
	if utf8.RuneCountInString(command.SanitizedError) > 1000 || command.Retryable && !failed {
		return errors.New("discovery completion is invalid")
	}
	if command.HTTPStatus != nil && (*command.HTTPStatus < 100 || *command.HTTPStatus > 599) {
		return errors.New("HTTP status is invalid")
	}
	if success && (command.TLSVerified == nil || command.AuthenticationSucceeded == nil || !*command.AuthenticationSucceeded || command.HTTPStatus == nil || *command.HTTPStatus < 200 || *command.HTTPStatus > 299) {
		return errors.New("successful discovery requires transport, authentication, and 2xx evidence")
	}
	seen := map[string]struct{}{}
	for _, modelID := range command.ModelIDs {
		if err := validateModelID(modelID); err != nil {
			return err
		}
		if _, exists := seen[modelID]; exists {
			return errors.New("discovered model IDs must be unique")
		}
		seen[modelID] = struct{}{}
	}
	if success {
		normalized, err := normalizeJSONObject(command.RawResponse, maxDiscoveryCapture)
		size, sizeErr := pythonJSONDumpSize(normalized)
		if err != nil || sizeErr != nil || size > maxDiscoveryCapture {
			return errors.New("discovery response capture is too large or invalid")
		}
		command.RawResponse = normalized
	}
	return nil
}

func (command ScheduleProbe) validate() error {
	if command.ExpectedVersion <= 0 {
		return errors.New("expected_version must be positive")
	}
	if !command.AcknowledgeCost {
		return errors.New("probe cost must be acknowledged")
	}
	if len(command.SelectedChecks) == 0 || len(command.SelectedChecks) > 4 {
		return errors.New("probe checks must be non-empty and unique")
	}
	seen := map[ProbeCheck]struct{}{}
	for _, check := range command.SelectedChecks {
		if check != ProbeChat && check != ProbeStreaming && check != ProbeTools && check != ProbeStructuredOutput {
			return errors.New("probe check is invalid")
		}
		if _, exists := seen[check]; exists {
			return errors.New("probe checks must be non-empty and unique")
		}
		seen[check] = struct{}{}
	}
	return nil
}

func (command CompleteProbe) validate() error {
	success := command.Findings != nil && command.RawResponse != nil && command.SanitizedError == ""
	failed := command.Findings == nil && command.RawResponse == nil && command.SanitizedError != ""
	if !success && !failed || utf8.RuneCountInString(command.SanitizedError) > 1000 || command.Retryable && !failed {
		return errors.New("probe completion requires findings or an error")
	}
	if success {
		if err := command.Findings.Validate(); err != nil {
			return err
		}
		normalized, err := normalizeJSONObject(command.RawResponse, maxProbeCapture)
		size, sizeErr := pythonJSONDumpSize(normalized)
		if err != nil || sizeErr != nil || size > maxProbeCapture {
			return errors.New("probe result capture is too large or invalid")
		}
	}
	return nil
}

func (command AssignModel) validate() error {
	if command.Role != DocumentationPlanner && command.Role != DocumentationWriter {
		return errors.New("model role is invalid")
	}
	if command.ReasoningEffort != EffortNone && command.ReasoningEffort != EffortMinimal &&
		command.ReasoningEffort != EffortLow && command.ReasoningEffort != EffortMedium &&
		command.ReasoningEffort != EffortHigh && command.ReasoningEffort != EffortMax {
		return errors.New("reasoning effort is invalid")
	}
	if command.AnswerMode != ToolCalling && command.AnswerMode != SinglePass {
		return errors.New("answer mode is invalid")
	}
	if command.ExpectedVersion != nil && *command.ExpectedVersion <= 0 {
		return errors.New("expected_version must be positive")
	}
	return nil
}

func validateAssignment(command AssignModel, settings Settings) error {
	if settings.Transport == Responses {
		return fmt.Errorf("%w: Responses transport cannot be assigned", ErrConflict)
	}
	if command.ReasoningEffort != EffortNone {
		switch settings.ReasoningTransport {
		case NoReasoning:
			return fmt.Errorf("%w: model does not support reasoning effort", ErrConflict)
		case CustomReasoning:
			if settings.ReasoningMapping == nil {
				return fmt.Errorf("%w: custom reasoning effort is not mapped", ErrConflict)
			}
			if _, exists := settings.ReasoningMapping.Values[strings.ToLower(string(command.ReasoningEffort))]; !exists {
				return fmt.Errorf("%w: custom reasoning effort is not mapped", ErrConflict)
			}
		}
	}
	if settings.ContextWindowTokens == nil || settings.MaxOutputTokens == nil {
		return fmt.Errorf("%w: assigned models require known positive limits", ErrConflict)
	}
	if settings.SupportsTools == nil || !*settings.SupportsTools ||
		settings.SupportsStructuredOutput == nil || !*settings.SupportsStructuredOutput {
		return fmt.Errorf("%w: documentation models require known limits, tools, and structured output", ErrConflict)
	}
	if command.AnswerMode != ToolCalling {
		return fmt.Errorf("%w: documentation models require tool-calling mode", ErrConflict)
	}
	return nil
}

func validateModelID(value string) error {
	if !boundedTrimmed(value, 512) {
		return errors.New("model_id must contain 1 to 512 characters")
	}
	return nil
}

func validateChecksMatch(findings ProbeFindings, selected []ProbeCheck) bool {
	left := findings.RequiredChecks()
	if len(left) != len(selected) {
		return false
	}
	set := make(map[ProbeCheck]struct{}, len(left))
	for _, check := range left {
		set[check] = struct{}{}
	}
	for _, check := range selected {
		if _, exists := set[check]; !exists {
			return false
		}
	}
	return true
}

func boundedTrimmed(value string, maximum int) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func pythonJSONDumpSize(value any) (int, error) {
	raw, err := pythonCanonicalJSON(value)
	if err != nil {
		return 0, err
	}
	size := len(raw)
	inString := false
	escaped := false
	for _, character := range raw {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch character {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case ',', ':':
			size++
		}
	}
	return size, nil
}

func secretName(normalized string) bool {
	if _, denied := hopHeaders[normalized]; denied || strings.HasPrefix(normalized, "proxy-") {
		return true
	}
	return containsSecretPart(normalized)
}

func containsSecretPart(value string) bool {
	for _, part := range secretParts {
		if strings.Contains(value, part) {
			return true
		}
	}
	return false
}

func validOrigin(value MetadataOrigin) bool {
	return value == OriginUnknown || value == OriginDiscovered || value == OriginProbed || value == OriginOperator
}

func cloneOrigins(values map[string]MetadataOrigin) map[string]MetadataOrigin {
	result := make(map[string]MetadataOrigin, len(values))
	for name, value := range values {
		result[name] = value
	}
	return result
}

func settingsPayload(value Settings) map[string]any {
	origins := make(map[string]any, len(value.MetadataOrigin))
	for name, origin := range value.MetadataOrigin {
		origins[name] = string(origin)
	}
	var reasoning any
	if value.ReasoningMapping != nil {
		reasoning = map[string]any{"field": value.ReasoningMapping.Field, "values": value.ReasoningMapping.Values}
	}
	return map[string]any{
		"transport": value.Transport, "context_window_tokens": value.ContextWindowTokens,
		"max_output_tokens": value.MaxOutputTokens, "supports_streaming": value.SupportsStreaming,
		"supports_tools": value.SupportsTools, "supports_structured_output": value.SupportsStructuredOutput,
		"supports_temperature": value.SupportsTemperature, "reasoning_transport": value.ReasoningTransport,
		"reasoning_mapping": reasoning, "timeout_seconds": value.TimeoutSeconds,
		"max_retries": value.MaxRetries, "max_concurrent_tasks": value.MaxConcurrentTasks,
		"extra_body": value.ExtraBody, "metadata_origin": origins,
	}
}

func jsonEqual(left, right any) bool {
	a, _ := json.Marshal(left)
	b, _ := json.Marshal(right)
	return bytes.Equal(a, b)
}

func sortedChecks(values []ProbeCheck) []ProbeCheck {
	result := append([]ProbeCheck(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func validateEndpointState(value Endpoint) error {
	if value.Version <= 0 || value.ConfigurationVersion <= 0 {
		return fmt.Errorf("%w: invalid endpoint version", ErrConflict)
	}
	if (value.Health == Unknown) != (value.HealthCheckedAt == nil) {
		return fmt.Errorf("%w: provider health and check time are inconsistent", ErrConflict)
	}
	return nil
}

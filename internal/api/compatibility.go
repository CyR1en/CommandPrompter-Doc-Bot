package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/chattokens"
)

const (
	compatibilityModelsPath      = "/v1/models"
	compatibilityCompletionsPath = "/v1/chat/completions"
	compatibilityBodyLimit       = 1 << 20
	compatibilityHeaderLimit     = 64 << 10
	compatibilityAuthLimit       = 512
	// Fixture provenance: https://github.com/open-webui/open-webui/releases/tag/v0.11.3
	openWebUICompatibilityVersion = "v0.11.3"
)

type chatTokenAuthenticator interface {
	Authenticate(context.Context, string) (chattokens.Grant, error)
}

type scopedAgentService interface {
	ListReadyScoped(context.Context, []agents.AgentID) ([]agents.Agent, error)
	ResolveReadyScoped(context.Context, []agents.AgentID, string) (agents.Agent, error)
}

type agentExecutor interface {
	Execute(context.Context, agents.ExecuteRequest, agents.Authorizer) (agents.ExecuteResult, error)
}

type compatibilityHandler struct {
	tokens chatTokenAuthenticator
	agents scopedAgentService
	engine agentExecutor
	clock  func() time.Time
}

func newCompatibilityHandler(
	tokens chatTokenAuthenticator,
	agentCatalog scopedAgentService,
	engine agentExecutor,
	clock func() time.Time,
) (*compatibilityHandler, error) {
	if tokens == nil || agentCatalog == nil || engine == nil {
		return nil, errors.New("chat compatibility dependencies are incomplete")
	}
	if clock == nil {
		clock = time.Now
	}
	return &compatibilityHandler{tokens: tokens, agents: agentCatalog, engine: engine, clock: clock}, nil
}

func (handler *compatibilityHandler) register(mux *http.ServeMux) {
	mux.Handle(compatibilityModelsPath, http.HandlerFunc(handler.models))
	mux.Handle(compatibilityCompletionsPath, http.HandlerFunc(handler.completions))
	mux.Handle("/v1/", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeOpenAIError(writer, http.StatusNotFound, "invalid_request_error", "not_found", "The requested endpoint was not found.", nil)
	}))
}

type compatibilityModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type compatibilityModelList struct {
	Object string               `json:"object"`
	Data   []compatibilityModel `json:"data"`
}

func (handler *compatibilityHandler) models(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeOpenAIError(writer, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "Method not allowed.", nil)
		return
	}
	if !headersWithinCompatibilityBounds(request.Header) {
		writeOpenAIError(writer, http.StatusRequestHeaderFieldsTooLarge, "invalid_request_error", "headers_too_large", "Request headers are too large.", nil)
		return
	}
	grant, ok := handler.authenticate(writer, request)
	if !ok {
		return
	}
	values, err := handler.agents.ListReadyScoped(request.Context(), grant.AgentIDs())
	if err != nil {
		writeOpenAIError(writer, http.StatusServiceUnavailable, "server_error", "service_unavailable", "The model catalog is temporarily unavailable.", nil)
		return
	}
	response := compatibilityModelList{Object: "list", Data: make([]compatibilityModel, len(values))}
	for index, value := range values {
		response.Data[index] = compatibilityModel{
			ID: value.Selector(), Object: "model", Created: value.CreatedAt.Unix(), OwnedBy: "ref0",
		}
	}
	writeCompatibilityJSON(writer, http.StatusOK, response)
}

type compatibilityMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type compatibilityStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type compatibilityCompletionRequest struct {
	Model         string                      `json:"model"`
	Messages      []compatibilityMessage      `json:"messages"`
	Stream        bool                        `json:"stream"`
	MaxTokens     *int32                      `json:"max_tokens,omitempty"`
	StreamOptions *compatibilityStreamOptions `json:"stream_options,omitempty"`
}

type compatibilityUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type compatibilityRef0 struct {
	RunID     string            `json:"run_id"`
	Status    string            `json:"answer_status"`
	Citations []agents.Citation `json:"citations"`
}

type compatibilityCompletion struct {
	ID      string                `json:"id"`
	Object  string                `json:"object"`
	Created int64                 `json:"created"`
	Model   string                `json:"model"`
	Choices []compatibilityChoice `json:"choices"`
	Usage   compatibilityUsage    `json:"usage"`
	Ref0    compatibilityRef0     `json:"x_ref0"`
}

type compatibilityChoice struct {
	Index        int                  `json:"index"`
	Message      *compatibilityAnswer `json:"message,omitempty"`
	Delta        *compatibilityDelta  `json:"delta,omitempty"`
	FinishReason *string              `json:"finish_reason"`
}

type compatibilityAnswer struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type compatibilityDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type compatibilityChunk struct {
	ID      string                `json:"id"`
	Object  string                `json:"object"`
	Created int64                 `json:"created"`
	Model   string                `json:"model"`
	Choices []compatibilityChoice `json:"choices"`
	Usage   *compatibilityUsage   `json:"usage,omitempty"`
	Ref0    *compatibilityRef0    `json:"x_ref0,omitempty"`
}

func (handler *compatibilityHandler) completions(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeOpenAIError(writer, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "Method not allowed.", nil)
		return
	}
	if !headersWithinCompatibilityBounds(request.Header) {
		writeOpenAIError(writer, http.StatusRequestHeaderFieldsTooLarge, "invalid_request_error", "headers_too_large", "Request headers are too large.", nil)
		return
	}
	grant, ok := handler.authenticate(writer, request)
	if !ok {
		return
	}
	if len(request.Header.Values("Idempotency-Key")) != 0 {
		writeOpenAIError(writer, http.StatusBadRequest, "invalid_request_error", "unsupported_parameter", "Idempotency-Key is not supported for chat completions.", compatibilityStringPointer("Idempotency-Key"))
		return
	}
	body, err := decodeCompatibilityRequest(writer, request)
	if err != nil {
		status := http.StatusBadRequest
		code := "invalid_request"
		message := "The chat completion request is invalid."
		if errors.Is(err, errCompatibilityBodyTooLarge) {
			status, code, message = http.StatusRequestEntityTooLarge, "request_too_large", "The request body is too large."
		}
		writeOpenAIError(writer, status, "invalid_request_error", code, message, nil)
		return
	}
	messages, key, maxTokens, valid := normalizeCompatibilityRequest(body)
	if !valid {
		writeOpenAIError(writer, http.StatusBadRequest, "invalid_request_error", "invalid_request", "The chat completion request is invalid.", nil)
		return
	}
	selected, err := handler.agents.ResolveReadyScoped(request.Context(), grant.AgentIDs(), key)
	if err != nil {
		if errors.Is(err, agents.ErrChatModelUnavailable) {
			writeModelUnavailable(writer)
			return
		}
		writeOpenAIError(writer, http.StatusServiceUnavailable, "server_error", "service_unavailable", "The requested model is temporarily unavailable.", nil)
		return
	}
	authorizer := scopedGrantAuthorizer{grant: grant, agentID: selected.ID}
	result, err := handler.engine.Execute(request.Context(), agents.ExecuteRequest{
		Selector: selected.Selector(), Origin: agents.OriginHTTP, Subject: grant.Subject,
		Messages: messages, MaxTokens: maxTokens,
	}, authorizer)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			writeOpenAIError(writer, http.StatusRequestTimeout, "server_error", "request_cancelled", "The request was cancelled.", nil)
			return
		}
		if errors.Is(err, agents.ErrModelRateLimit) {
			writer.Header().Set("Retry-After", "5")
			writeOpenAIError(writer, http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded", "The provider is busy. Try again shortly.", nil)
			return
		}
		if errors.Is(err, agents.ErrExecutionInvalid) {
			writeOpenAIError(writer, http.StatusBadRequest, "invalid_request_error", "invalid_request", "The chat completion request is invalid.", nil)
			return
		}
		writeOpenAIError(writer, http.StatusServiceUnavailable, "server_error", "service_unavailable", "The chat completion could not be completed.", nil)
		return
	}
	currentGrant, ok := handler.authenticate(writer, request)
	if !ok {
		return
	}
	if currentGrant.Subject != grant.Subject || !currentGrant.Allows(selected.ID) {
		writeInvalidAPIKey(writer)
		return
	}
	created := handler.clock().Unix()
	usage := compatibilityUsageFrom(result.Usage)
	metadata := compatibilityRef0{
		RunID: result.RunID.String(), Status: strings.ToLower(string(result.Status)),
		Citations: append([]agents.Citation(nil), result.Citations...),
	}
	if body.Stream {
		handler.writeBufferedStream(writer, request, body.Model, result, created, usage, metadata, body.StreamOptions != nil && body.StreamOptions.IncludeUsage)
		return
	}
	finish := "stop"
	writeCompatibilityJSON(writer, http.StatusOK, compatibilityCompletion{
		ID: "chatcmpl-" + result.RunID.String(), Object: "chat.completion", Created: created, Model: body.Model,
		Choices: []compatibilityChoice{{
			Index: 0, Message: &compatibilityAnswer{Role: "assistant", Content: result.Markdown}, FinishReason: &finish,
		}},
		Usage: usage, Ref0: metadata,
	})
}

func (handler *compatibilityHandler) authenticate(writer http.ResponseWriter, request *http.Request) (chattokens.Grant, bool) {
	values := request.Header.Values("Authorization")
	if len(values) != 1 || len(values[0]) > compatibilityAuthLimit || len(values[0]) <= len("Bearer ") ||
		!strings.EqualFold(values[0][:len("Bearer ")], "Bearer ") {
		writeInvalidAPIKey(writer)
		return chattokens.Grant{}, false
	}
	grant, err := handler.tokens.Authenticate(request.Context(), values[0][len("Bearer "):])
	if err != nil {
		writeInvalidAPIKey(writer)
		return chattokens.Grant{}, false
	}
	return grant, true
}

func (handler *compatibilityHandler) writeBufferedStream(
	writer http.ResponseWriter,
	request *http.Request,
	model string,
	result agents.ExecuteResult,
	created int64,
	usage compatibilityUsage,
	metadata compatibilityRef0,
	includeUsage bool,
) {
	id := "chatcmpl-" + result.RunID.String()
	finish := "stop"
	chunks := []compatibilityChunk{
		{ID: id, Object: "chat.completion.chunk", Created: created, Model: model, Choices: []compatibilityChoice{{Index: 0, Delta: &compatibilityDelta{Role: "assistant"}}}},
		{ID: id, Object: "chat.completion.chunk", Created: created, Model: model, Choices: []compatibilityChoice{{Index: 0, Delta: &compatibilityDelta{Content: result.Markdown}}}},
		{ID: id, Object: "chat.completion.chunk", Created: created, Model: model, Choices: []compatibilityChoice{{Index: 0, Delta: &compatibilityDelta{}, FinishReason: &finish}}},
		{ID: id, Object: "chat.completion.chunk", Created: created, Model: model, Choices: []compatibilityChoice{}, Ref0: &metadata},
	}
	if includeUsage {
		chunks[len(chunks)-1].Usage = &usage
	}
	header := writer.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store, no-transform")
	header.Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(writer)
	for _, chunk := range chunks {
		encoded, err := json.Marshal(chunk)
		if err != nil {
			return
		}
		if _, err = writer.Write(append(append([]byte("data: "), encoded...), '\n', '\n')); err != nil {
			return
		}
		if err = controller.Flush(); err != nil {
			return
		}
	}
	_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	_ = controller.Flush()
	_ = request
}

type scopedGrantAuthorizer struct {
	grant   chattokens.Grant
	agentID agents.AgentID
}

func (authorizer scopedGrantAuthorizer) Authorize(_ context.Context, scope agents.AuthorizationScope) error {
	if scope.Origin != agents.OriginHTTP || scope.Subject != authorizer.grant.Subject ||
		scope.AgentID != authorizer.agentID || !authorizer.grant.Allows(scope.AgentID) {
		return agents.ErrExecutionForbidden
	}
	return nil
}

var errCompatibilityBodyTooLarge = errors.New("compatibility body exceeds limit")

func decodeCompatibilityRequest(writer http.ResponseWriter, request *http.Request) (compatibilityCompletionRequest, error) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return compatibilityCompletionRequest{}, errors.New("compatibility content type is invalid")
	}
	request.Body = http.MaxBytesReader(writer, request.Body, compatibilityBodyLimit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var body compatibilityCompletionRequest
	if err = decoder.Decode(&body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return compatibilityCompletionRequest{}, errCompatibilityBodyTooLarge
		}
		return compatibilityCompletionRequest{}, err
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return compatibilityCompletionRequest{}, errors.New("compatibility body contains trailing data")
	}
	return body, nil
}

func normalizeCompatibilityRequest(body compatibilityCompletionRequest) ([]agents.Message, string, int32, bool) {
	key, ok := strings.CutPrefix(body.Model, "agent:")
	if !ok {
		return nil, "", 0, false
	}
	if _, err := agents.ParseKey(key); err != nil || len(body.Messages) == 0 ||
		len(body.Messages) > agents.MaxTranscriptMessages || body.Messages[len(body.Messages)-1].Role != "user" ||
		body.StreamOptions != nil && !body.Stream || body.MaxTokens != nil && (*body.MaxTokens <= 0 || *body.MaxTokens > agents.MaxAnswerTokens) {
		return nil, "", 0, false
	}
	result := make([]agents.Message, len(body.Messages))
	total := 0
	for index, message := range body.Messages {
		if !utf8.ValidString(message.Content) || len(message.Content) == 0 || len([]byte(message.Content)) > agents.MaxMessageBytes || strings.IndexByte(message.Content, 0) >= 0 {
			return nil, "", 0, false
		}
		total += len([]byte(message.Content))
		if total > agents.MaxTranscriptBytes {
			return nil, "", 0, false
		}
		switch message.Role {
		case "system":
			result[index] = agents.Message{Role: agents.RoleSystem, Content: message.Content}
		case "user":
			result[index] = agents.Message{Role: agents.RoleUser, Content: message.Content}
		case "assistant":
			result[index] = agents.Message{Role: agents.RoleAssistant, Content: message.Content}
		default:
			return nil, "", 0, false
		}
	}
	if strings.TrimFunc(body.Messages[len(body.Messages)-1].Content, apiPythonWhitespace) == "" {
		return nil, "", 0, false
	}
	maxTokens := int32(0)
	if body.MaxTokens != nil {
		maxTokens = *body.MaxTokens
	}
	return result, key, maxTokens, true
}

func compatibilityUsageFrom(values map[string]int) compatibilityUsage {
	prompt := firstUsage(values, "prompt_tokens", "input_tokens", "estimated_input_tokens")
	completion := firstUsage(values, "completion_tokens", "output_tokens")
	total := firstUsage(values, "total_tokens")
	if total == 0 {
		total = prompt + completion
	}
	return compatibilityUsage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total}
}

func firstUsage(values map[string]int, keys ...string) int {
	for _, key := range keys {
		if value := values[key]; value > 0 {
			return value
		}
	}
	return 0
}

func headersWithinCompatibilityBounds(header http.Header) bool {
	total := 0
	for name, values := range header {
		total += len(name)
		for _, value := range values {
			total += len(value)
			if total > compatibilityHeaderLimit {
				return false
			}
		}
	}
	return true
}

type openAIErrorEnvelope struct {
	Error openAIError `json:"error"`
}

type openAIError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

func writeOpenAIError(writer http.ResponseWriter, status int, errorType, code, message string, parameter *string) {
	if status == http.StatusUnauthorized {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="chat"`)
	}
	writeCompatibilityJSON(writer, status, openAIErrorEnvelope{Error: openAIError{
		Message: message, Type: errorType, Param: parameter, Code: code,
	}})
}

func writeInvalidAPIKey(writer http.ResponseWriter) {
	writeOpenAIError(writer, http.StatusUnauthorized, "invalid_request_error", "invalid_api_key", "Invalid authentication credentials.", nil)
}

func writeModelUnavailable(writer http.ResponseWriter) {
	writeOpenAIError(writer, http.StatusNotFound, "invalid_request_error", "model_not_found", "The requested model is unavailable.", compatibilityStringPointer("model"))
}

func writeCompatibilityJSON(writer http.ResponseWriter, status int, value any) {
	header := writer.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func compatibilityStringPointer(value string) *string { return &value }

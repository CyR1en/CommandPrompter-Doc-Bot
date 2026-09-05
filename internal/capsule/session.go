package capsule

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/cyr1en/ref0/internal/safenet"
)

type networkClient interface {
	Exchange(context.Context, string, string, map[string]string, []byte, int) (safenet.Response, error)
	CloseIdleConnections()
}

type Factory struct {
	binding Binding
	role    Role
	pool    *SlotPool
	secrets SecretReader

	dial   func(context.Context, string) (net.Conn, error)
	client func(Binding) (networkClient, error)
	sleep  func(context.Context, time.Duration) error
	now    func() time.Time
}

func NewFactory(binding Binding, expectedRole Role, pool *SlotPool, secrets SecretReader, options FactoryOptions) (*Factory, error) {
	normalized, err := normalizeBinding(binding)
	if err != nil {
		return nil, err
	}
	if expectedRole != Planner && expectedRole != PageWriter || pool == nil || normalized.Credential != nil && secrets == nil {
		return nil, errors.New("capsule factory dependencies are incomplete")
	}
	factory := &Factory{binding: normalized, role: expectedRole, pool: pool, secrets: secrets, now: time.Now}
	factory.dial = func(ctx context.Context, socketPath string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}
	factory.client = func(binding Binding) (networkClient, error) {
		return safenet.NewClient(binding.BaseURL, binding.NetworkPolicy, safenet.ClientOptions{
			Headers: binding.Headers, Resolver: options.Resolver, TLSConfig: options.TLSConfig,
			Timeout: binding.Timeout,
		})
	}
	factory.sleep = func(ctx context.Context, delay time.Duration) error {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return factory, nil
}

func NewProviderFactory(captured ProviderCapture, profile providers.Profile, endpoint providers.Endpoint,
	pool *SlotPool, secrets SecretReader, options FactoryOptions,
) (*Factory, error) {
	binding, role, err := CompileBinding(captured, profile, endpoint)
	if err != nil {
		return nil, err
	}
	return NewFactory(binding, role, pool, secrets, options)
}

type Session struct {
	factory      *Factory
	role         Role
	systemPrompt string
	tools        []Tool
	outputSchema map[string]any
	invoked      atomic.Bool
}

func (factory *Factory) NewSession(role Role, systemPrompt string, tools []Tool, outputSchema map[string]any) (*Session, error) {
	if role != factory.role {
		return nil, errors.New("capsule factory role mismatch")
	}
	responseLimits := factory.responseLimits()
	probe, err := NewOpenAIAttempt(factory.binding.ModelID, systemPrompt, "", tools, outputSchema,
		factory.binding.BodyOptions, factory.binding.ReasoningOptions, factory.binding.ContextWindow, factory.binding.MaxOutputTokens,
		factory.binding.Limits.MaxModelRequests, responseLimits)
	if err != nil {
		return nil, err
	}
	return &Session{
		factory: factory, role: role, systemPrompt: systemPrompt,
		tools: append([]Tool(nil), probe.tools...), outputSchema: probe.outputSchema,
	}, nil
}

func (session *Session) Invoke(ctx context.Context, prompt string) (Invocation, error) {
	if !session.invoked.CompareAndSwap(false, true) {
		return Invocation{}, &InvocationError{Message: "capsule session is single-use"}
	}
	attemptCtx, cancel := context.WithTimeout(ctx, session.factory.binding.Limits.AttemptTimeout)
	defer cancel()
	result, err := session.invoke(attemptCtx, prompt)
	if err != nil && errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
		return Invocation{}, &InvocationError{Message: "capsule attempt timed out safely", Usage: result.Usage}
	}
	return result, err
}

func (session *Session) invoke(ctx context.Context, prompt string) (result Invocation, returnedError error) {
	attempt, err := NewOpenAIAttempt(
		session.factory.binding.ModelID, session.systemPrompt, prompt, session.tools, session.outputSchema,
		session.factory.binding.BodyOptions, session.factory.binding.ReasoningOptions,
		session.factory.binding.ContextWindow, session.factory.binding.MaxOutputTokens, session.factory.binding.Limits.MaxModelRequests,
		session.factory.responseLimits(),
	)
	if err != nil {
		return result, &InvocationError{Usage: attemptUsage(attempt)}
	}
	apiKey, err := session.factory.resolveAPIKey(ctx)
	if err != nil {
		return result, err
	}
	client, err := session.factory.client(session.factory.binding)
	if err != nil {
		return result, &InvocationError{Usage: attempt.Usage}
	}
	defer client.CloseIdleConnections()

	// The credential is resolved before this acquire. A missing or stale secret can
	// never occupy a scarce capsule slot.
	slot, err := session.factory.pool.Acquire(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, &InvocationError{Usage: attempt.Usage}
	}
	defer func() { _ = session.factory.pool.Release(slot) }()
	connection, err := session.factory.dial(ctx, slot.SocketPath)
	if err != nil {
		attempt.Fail(false)
		return result, &InvocationError{Usage: attempt.Usage}
	}
	transport := newWire(connection, session.factory.binding.Limits)
	completed := false
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.SetDeadline(time.Now())
		case <-stopped:
		}
	}()
	defer func() {
		close(stopped)
		if ctx.Err() != nil && !completed {
			attempt.Fail(true)
			_ = connection.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			_ = transport.send(map[string]any{"type": "cancel", "reason": "host cancellation"})
		}
		_ = connection.Close()
		result.Usage = attempt.Usage
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	start, err := session.startMessage(prompt)
	if err != nil {
		attempt.Fail(false)
		return result, &InvocationError{Usage: attempt.Usage}
	}
	if err := transport.send(start); err != nil {
		attempt.Fail(false)
		return result, &InvocationError{Usage: attempt.Usage}
	}
	tools := make(map[string]Tool, len(session.tools))
	for _, tool := range session.tools {
		tools[tool.Name] = tool
	}
	seenIDs := map[string]struct{}{}
	fetches, modelRequests := 0, 0
	for {
		message, err := transport.receive()
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			attempt.Fail(false)
			return result, &InvocationError{Usage: attempt.Usage}
		}
		switch message["type"] {
		case "model_request":
			operation, err := uniqueID(message, seenIDs)
			turn, turnOK := jsonInteger(message["turn"])
			if err != nil || !turnOK || turn < 1 {
				attempt.Fail(false)
				return result, &InvocationError{Usage: attempt.Usage}
			}
			fetches++
			modelRequests++
			if fetches > session.factory.binding.Limits.MaxFetches || modelRequests > session.factory.binding.Limits.MaxModelRequests {
				attempt.Fail(false)
				return result, &InvocationError{Usage: attempt.Usage}
			}
			requestBody, err := attempt.BeginModel(int(turn))
			if err != nil {
				attempt.Fail(false)
				return result, &InvocationError{Usage: attempt.Usage}
			}
			response, err := session.factory.exchange(ctx, client, requestBody, apiKey)
			if err != nil || response.Status != http.StatusOK || !eventStream(response.Headers["content-type"]) {
				attempt.Fail(false)
				return result, &InvocationError{Usage: attempt.Usage}
			}
			normalized, err := attempt.AcceptModel(response.Body)
			if err != nil {
				attempt.Fail(false)
				return result, &InvocationError{Usage: attempt.Usage}
			}
			if err := transport.send(map[string]any{
				"type": "model_result", "id": operation,
				"body_base64": base64.StdEncoding.EncodeToString(normalized),
			}); err != nil {
				attempt.Fail(false)
				return result, &InvocationError{Usage: attempt.Usage}
			}
		case "tool_call":
			operation, err := uniqueID(message, seenIDs)
			pending, validationErr := attempt.ValidateToolCall(message["provider_call_id"], message["name"], message["arguments"])
			if err != nil || validationErr != nil {
				attempt.Fail(false)
				return result, &InvocationError{Usage: attempt.Usage}
			}
			tool, exists := tools[pending.Name]
			if !exists {
				attempt.Fail(false)
				return result, &InvocationError{Usage: attempt.Usage}
			}
			toolResult, err := tool.Handler(ctx, pending.Arguments)
			if err != nil {
				attempt.Fail(false)
				return result, &InvocationError{Usage: attempt.Usage}
			}
			content, canonical, err := attempt.AcceptToolResult(toolResult)
			if err != nil || transport.send(map[string]any{
				"type": "tool_result", "id": operation, "result": canonical, "content": content,
			}) != nil {
				attempt.Fail(false)
				return result, &InvocationError{Usage: attempt.Usage}
			}
		case "complete":
			output, err := attempt.AcceptComplete(message["output"])
			if err != nil {
				attempt.Fail(false)
				return result, &InvocationError{Usage: attempt.Usage}
			}
			_ = connection.SetReadDeadline(time.Now().Add(time.Second))
			var trailing [1]byte
			n, readErr := connection.Read(trailing[:])
			if n != 0 || !errors.Is(readErr, io.EOF) {
				attempt.Fail(false)
				return result, &InvocationError{Usage: attempt.Usage}
			}
			completed = true
			return Invocation{Output: output, Usage: attempt.Usage}, nil
		case "failed":
			attempt.Fail(false)
			return result, &InvocationError{Usage: attempt.Usage}
		default:
			attempt.Fail(false)
			return result, &InvocationError{Usage: attempt.Usage}
		}
	}
}

func (session *Session) startMessage(prompt string) (map[string]any, error) {
	binding, limits := session.factory.binding, session.factory.binding.Limits
	attemptID, err := randomAttemptID()
	if err != nil {
		return nil, err
	}
	tools := make([]any, 0, len(session.tools))
	for _, tool := range session.tools {
		tools = append(tools, map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters})
	}
	return map[string]any{
		"type": "start", "protocol_version": ProtocolVersion, "attempt_id": attemptID,
		"role": string(session.role), "system_prompt": session.systemPrompt, "prompt": prompt,
		"tools": tools, "output_schema": session.outputSchema,
		"provider": map[string]any{
			"model_id": binding.ModelID, "body_options": binding.BodyOptions,
			"context_window": binding.ContextWindow, "max_output_tokens": binding.MaxOutputTokens,
			"reasoning_effort": strings.ToLower(string(binding.ReasoningEffort)),
			"timeout_ms":       binding.Timeout.Milliseconds(), "capsule_runtime_revision": binding.CapsuleRuntimeRevision,
		},
		"limits": map[string]any{
			"max_frame_bytes": limits.MaxFrameBytes, "max_aggregate_bytes": limits.MaxAggregateBytes,
			"max_string_bytes": limits.MaxStringBytes, "max_depth": limits.MaxDepth, "max_keys": limits.MaxKeys,
			"max_fetch_body_bytes": limits.MaxFetchBodyBytes, "max_fetches": limits.MaxFetches,
			"max_model_requests": limits.MaxModelRequests,
		},
	}, nil
}

func (factory *Factory) responseLimits() ResponseLimits {
	limits := factory.binding.Limits
	return ResponseLimits{
		MaxBytes: limits.MaxFetchBodyBytes, MaxEvents: min(limits.MaxKeys, 10_000),
		MaxLineBytes: limits.MaxStringBytes, MaxDepth: limits.MaxDepth,
		MaxStringBytes: limits.MaxStringBytes, MaxKeys: limits.MaxKeys,
	}
}

func (factory *Factory) resolveAPIKey(ctx context.Context) (string, error) {
	reference := factory.binding.Credential
	if reference == nil {
		return "", nil
	}
	secret, err := factory.secrets.Read(ctx, reference.ID, credentials.ProviderAPIKey, reference.SecretVersion)
	if err != nil || secret == nil {
		return "", &InvocationError{Message: "capsule credential resolution failed safely"}
	}
	value := secret.Reveal()
	if value == "" || !utf8.ValidString(value) || len([]byte(value)) > 16_384 ||
		strings.IndexFunc(value, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		return "", &InvocationError{Message: "capsule credential resolution failed safely"}
	}
	return value, nil
}

func (factory *Factory) exchange(ctx context.Context, client networkClient, body []byte, apiKey string) (safenet.Response, error) {
	headers := map[string]string{"accept": "text/event-stream", "content-type": "application/json"}
	if apiKey != "" {
		headers["authorization"] = "Bearer " + apiKey
	}
	for retry := 0; retry <= factory.binding.MaxRetries; retry++ {
		response, err := client.Exchange(ctx, http.MethodPost, factory.binding.ChatCompletionsPath, headers, body, factory.binding.Limits.MaxFetchBodyBytes)
		if err != nil {
			var requestError *safenet.RequestError
			if retry >= factory.binding.MaxRetries || !errors.As(err, &requestError) || !requestError.Retryable {
				return safenet.Response{}, err
			}
			delay, delayErr := safenet.RetryDelay(nil, retry, factory.now())
			if delayErr != nil || factory.sleep(ctx, delay) != nil {
				if delayErr != nil {
					return safenet.Response{}, delayErr
				}
				return safenet.Response{}, ctx.Err()
			}
			continue
		}
		if response.Status == http.StatusOK || retry >= factory.binding.MaxRetries || !retryableProviderStatus(response.Status) {
			return response, nil
		}
		delay, err := safenet.RetryDelay(response.Headers, retry, factory.now())
		if err != nil {
			return safenet.Response{}, err
		}
		if err := factory.sleep(ctx, delay); err != nil {
			return safenet.Response{}, err
		}
	}
	return safenet.Response{}, errors.New("provider retry loop did not return")
}

func retryableProviderStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusConflict || status == http.StatusTooManyRequests || status >= 500 && status <= 599
}

func uniqueID(message map[string]any, seen map[string]struct{}) (string, error) {
	value, ok := message["id"].(string)
	if !ok || !validOperationID(value) {
		return "", errors.New("capsule operation ID is invalid or duplicate")
	}
	if _, duplicate := seen[value]; duplicate {
		return "", errors.New("capsule operation ID is invalid or duplicate")
	}
	seen[value] = struct{}{}
	return value, nil
}

func eventStream(value string) bool {
	mediaType := strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
	return strings.EqualFold(mediaType, "text/event-stream")
}

func randomAttemptID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("secure attempt ID generation failed: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func attemptUsage(attempt *OpenAIAttempt) Usage {
	if attempt == nil {
		return Usage{}
	}
	return attempt.Usage
}

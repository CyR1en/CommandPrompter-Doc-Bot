// capsule-provider is a deterministic, strict OpenAI-compatible verifier.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"
)

const sourceID = "11111111-1111-4111-8111-111111111111"

type usage [3]int

type modelPolicy struct {
	tokenEnv  string
	reasoning string
	header    string
	usage     [2]usage
}

var policies = map[string]modelPolicy{
	"planner-verification-model":  {"PLANNER_API_TOKEN", "high", "planner", [2]usage{{11, 3, 14}, {12, 4, 16}}},
	"writer-verification-model":   {"WRITER_API_TOKEN", "medium", "writer", [2]usage{{21, 5, 26}, {22, 6, 28}}},
	"protocol-verification-model": {"PROTOCOL_API_TOKEN", "", "", [2]usage{{3, 2, 5}, {4, 2, 6}}},
	"stalled-verification-model":  {"PROTOCOL_API_TOKEN", "", "", [2]usage{{31, 7, 38}, {31, 7, 38}}},
}

var allowedFields = map[string]struct{}{
	"model": {}, "messages": {}, "stream": {}, "stream_options": {}, "n": {},
	"tools": {}, "tool_choice": {}, "parallel_tool_calls": {}, "max_tokens": {},
	"seed": {}, "reasoning_effort": {},
}

type acceptedRequest struct {
	model     string
	turn      int
	tool      string
	arguments string
	usage     usage
	reasoning string
}

func reject(message string) error { return errors.New(message) }

func acceptRequest(path, authorization, roleHeader string, request map[string]any, environment map[string]string) (acceptedRequest, error) {
	if path != "/v1/chat/completions" || request == nil {
		return acceptedRequest{}, reject("request boundary rejected")
	}
	for name := range request {
		if _, allowed := allowedFields[name]; !allowed || name == "reasoning" || name == "extra_body" {
			return acceptedRequest{}, reject("request policy rejected")
		}
	}
	model, ok := request["model"].(string)
	policy, known := policies[model]
	if !ok || !known {
		return acceptedRequest{}, reject("model rejected")
	}
	reasoning, hasReasoning := request["reasoning_effort"].(string)
	if policy.reasoning == "" && hasReasoning || policy.reasoning != "" && (!hasReasoning || reasoning != policy.reasoning) {
		return acceptedRequest{}, reject("reasoning effort rejected")
	}
	if authorization != "Bearer "+environment[policy.tokenEnv] || roleHeader != policy.header {
		return acceptedRequest{}, reject("authentication rejected")
	}
	if request["stream"] != true || !reflect.DeepEqual(request["stream_options"], map[string]any{"include_usage": true}) ||
		!numberEquals(request["n"], 1) || request["parallel_tool_calls"] != false ||
		!numberEquals(request["seed"], 7) || !numberEquals(request["max_tokens"], 1024) {
		return acceptedRequest{}, reject("body policy rejected")
	}

	tools, ok := request["tools"].([]any)
	if !ok {
		return acceptedRequest{}, reject("tool policy rejected")
	}
	expected := map[string]struct{}{"submit_result": {}}
	if model != "stalled-verification-model" {
		for _, name := range []string{"list", "glob", "grep", "read"} {
			expected[name] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	for _, raw := range tools {
		item, itemOK := raw.(map[string]any)
		function, functionOK := item["function"].(map[string]any)
		name, nameOK := function["name"].(string)
		_, parametersOK := function["parameters"].(map[string]any)
		if !itemOK || !functionOK || !nameOK || !parametersOK || item["type"] != "function" || function["strict"] != true {
			return acceptedRequest{}, reject("tool policy rejected")
		}
		if _, duplicate := seen[name]; duplicate {
			return acceptedRequest{}, reject("tool policy rejected")
		}
		seen[name] = struct{}{}
	}
	if !reflect.DeepEqual(seen, expected) {
		return acceptedRequest{}, reject("tool policy rejected")
	}

	messages, ok := request["messages"].([]any)
	if !ok {
		return acceptedRequest{}, reject("messages rejected")
	}
	turn, toolMessages := 1, []map[string]any{}
	for _, raw := range messages {
		message, messageOK := raw.(map[string]any)
		if !messageOK {
			return acceptedRequest{}, reject("messages rejected")
		}
		if message["role"] == "tool" {
			turn = 2
			toolMessages = append(toolMessages, message)
		}
	}
	if turn == 2 && (len(toolMessages) != 1 || !strings.Contains(fmt.Sprint(toolMessages[0]["content"]), "durable publication")) {
		return acceptedRequest{}, reject("source read result rejected")
	}
	namedSubmit := map[string]any{"type": "function", "function": map[string]any{"name": "submit_result"}}
	expectedChoice := any("required")
	if model == "stalled-verification-model" || model == "protocol-verification-model" && turn == 2 {
		expectedChoice = namedSubmit
	}
	if !reflect.DeepEqual(request["tool_choice"], expectedChoice) {
		return acceptedRequest{}, reject("tool choice rejected")
	}
	if turn < 1 || turn > 2 {
		return acceptedRequest{}, reject("model turn rejected")
	}
	if model == "stalled-verification-model" {
		if turn != 1 {
			return acceptedRequest{}, reject("stalled model turn rejected")
		}
		return acceptedRequest{model, turn, "submit_result", mustJSON(map[string]any{"answer": strings.Repeat("x", 700_000)}), policy.usage[0], reasoning}, nil
	}
	tool, arguments := "read", mustJSON(map[string]any{
		"path": "/sources/" + sourceID + "/verified.py", "start_line": 1, "end_line": 2,
	})
	if turn == 2 {
		tool, arguments = "submit_result", submission(strings.SplitN(model, "-", 2)[0])
	}
	return acceptedRequest{model, turn, tool, arguments, policy.usage[turn-1], reasoning}, nil
}

func numberEquals(value any, expected int64) bool {
	switch number := value.(type) {
	case float64:
		return number == float64(expected)
	case json.Number:
		parsed, err := number.Int64()
		return err == nil && parsed == expected
	default:
		return false
	}
}

func submission(role string) string {
	if role == "planner" || role == "protocol" {
		return mustJSON(map[string]any{"pages": []any{map[string]any{
			"slug": "verified-flow", "title": "Verified flow",
			"purpose": "Document the durable verified feature.", "related_pages": []any{},
			"source_seed_paths": []any{map[string]any{"source_id": sourceID, "path": "verified.py"}},
		}}})
	}
	return mustJSON(map[string]any{
		"slug":     "verified-flow",
		"markdown": "---\ntype: Concept\ntitle: Verified flow\ndescription: Durable publication verification.\n---\n\n# Verified flow\n\nThe source returns durable publication.[^durable-return]\n",
		"claims": []any{map[string]any{
			"id": "durable-return", "statement": "The source returns durable publication.",
			"evidence": []any{map[string]any{
				"id": "verified-line", "source_id": sourceID, "path": "verified.py", "start_line": 2, "end_line": 2,
			}},
		}},
	})
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func responseBody(request acceptedRequest) []byte {
	callID := fmt.Sprintf("%s-%d", request.model, request.turn)
	chunks := []any{
		map[string]any{
			"id": "chatcmpl-" + callID, "object": "chat.completion.chunk", "created": request.turn, "model": request.model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{
				"role": "assistant", "tool_calls": []any{map[string]any{
					"index": 0, "id": callID, "type": "function",
					"function": map[string]any{"name": request.tool, "arguments": request.arguments},
				}},
			}, "finish_reason": nil}},
		},
		map[string]any{
			"id": "chatcmpl-" + callID, "object": "chat.completion.chunk", "created": request.turn, "model": request.model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}},
		},
		map[string]any{
			"id": "chatcmpl-" + callID, "object": "chat.completion.chunk", "created": request.turn, "model": request.model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}}},
			"usage":   map[string]any{"prompt_tokens": request.usage[0], "completion_tokens": request.usage[1], "total_tokens": request.usage[2]},
		},
	}
	var result strings.Builder
	for _, chunk := range chunks {
		result.WriteString("data: ")
		result.WriteString(mustJSON(chunk))
		result.WriteString("\n\n")
	}
	result.WriteString("data: [DONE]\n\n")
	return []byte(result.String())
}

type providerServer struct {
	environment  map[string]string
	mu           sync.Mutex
	observations []map[string]any
}

func (server *providerServer) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/health":
		response.Header().Set("content-type", "text/plain")
		_, _ = response.Write([]byte("ok"))
		return
	case request.Method == http.MethodGet && request.URL.Path == "/observations":
		server.mu.Lock()
		body, err := json.Marshal(map[string]any{"requests": append([]map[string]any(nil), server.observations...)})
		server.mu.Unlock()
		if err != nil {
			http.Error(response, "encode observations", http.StatusInternalServerError)
			return
		}
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write(body)
		return
	case request.Method != http.MethodPost:
		http.NotFound(response, request)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, 1_048_576)
	decoder := json.NewDecoder(request.Body)
	decoder.UseNumber()
	var body map[string]any
	if err := decoder.Decode(&body); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(response, "malformed request", http.StatusBadRequest)
		return
	}
	accepted, err := acceptRequest(request.URL.Path, request.Header.Get("authorization"), request.Header.Get("x-role"), body, server.environment)
	if err != nil {
		fmt.Fprintln(os.Stderr, "provider_rejected:"+err.Error())
		http.Error(response, "request rejected", http.StatusBadRequest)
		return
	}
	server.mu.Lock()
	if len(server.observations) >= 32 {
		server.mu.Unlock()
		http.Error(response, "observation limit", http.StatusTooManyRequests)
		return
	}
	server.observations = append(server.observations, map[string]any{
		"model": accepted.model, "turn": accepted.turn, "tool": accepted.tool,
		"reasoning_effort": optionalString(accepted.reasoning),
		"input_tokens":     accepted.usage[0], "output_tokens": accepted.usage[1], "total_tokens": accepted.usage[2],
	})
	server.mu.Unlock()
	payload := responseBody(accepted)
	response.Header().Set("content-type", "text/event-stream")
	response.Header().Set("content-length", fmt.Sprint(len(payload)))
	_, _ = response.Write(payload)
}

func optionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func environment() map[string]string {
	result := map[string]string{}
	for _, name := range []string{"PLANNER_API_TOKEN", "WRITER_API_TOKEN", "PROTOCOL_API_TOKEN"} {
		result[name] = os.Getenv(name)
		if result[name] == "" {
			panic(name + " is required")
		}
	}
	return result
}

func healthcheck() error {
	client := http.Client{Timeout: time.Second}
	response, err := client.Get("http://127.0.0.1:8080/health")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("provider health returned %d", response.StatusCode)
	}
	return nil
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		if err := healthcheck(); err != nil {
			panic(err)
		}
		return
	}
	if len(os.Args) != 1 {
		panic("usage: capsule-provider [--healthcheck]")
	}
	server := &http.Server{
		Addr:              ":8080",
		Handler:           &providerServer{environment: environment()},
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       10 * time.Second,
	}
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		panic(err)
	}
}

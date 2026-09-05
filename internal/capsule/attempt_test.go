package capsule

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/modelbudget"
	"github.com/cyr1en/ref0/internal/providers"
)

var (
	testResponseLimits = ResponseLimits{MaxBytes: 1_048_576, MaxEvents: 100, MaxLineBytes: 65_536, MaxDepth: 32, MaxStringBytes: 65_536, MaxKeys: 10_000}
	testOutputSchema   = map[string]any{
		"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}},
		"required": []any{"answer"}, "additionalProperties": false,
	}
	testLookupSchema = map[string]any{
		"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}},
		"required": []any{"id"}, "additionalProperties": false,
	}
)

func testAttempt(t *testing.T, maximum int) *OpenAIAttempt {
	t.Helper()
	attempt, err := NewOpenAIAttempt(
		"custom-model", "system", "prompt",
		[]Tool{{Name: "lookup", Description: "Look up.", Parameters: testLookupSchema, Handler: func(context.Context, map[string]any) (any, error) { return nil, nil }}},
		testOutputSchema, map[string]any{"temperature": 0, "stop": []any{"END"}}, map[string]any{}, 8_192, 1_024, maximum, testResponseLimits,
	)
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func event(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := canonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return append(append([]byte("data: "), encoded...), []byte("\n\n")...)
}

func toolSSE(t *testing.T, name, arguments, callID string, usageChoices []any) []byte {
	t.Helper()
	chunks := [][]byte{
		event(t, map[string]any{
			"id": "response-1", "object": "chat.completion.chunk", "model": "custom-model",
			"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{
					"role": "assistant", "tool_calls": []any{map[string]any{
						"index": 0, "id": callID, "type": "function",
						"function": map[string]any{"name": name, "arguments": arguments},
					}},
				}, "finish_reason": nil,
			}},
		}),
		event(t, map[string]any{
			"id": "response-1", "object": "chat.completion.chunk", "model": "custom-model",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}},
		}),
		event(t, map[string]any{
			"id": "response-1", "object": "chat.completion.chunk", "model": "custom-model", "choices": usageChoices,
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14},
		}),
		[]byte("data: [DONE]\n\n"),
	}
	return append(append(append(chunks[0], chunks[1]...), chunks[2]...), chunks[3]...)
}

func assistantSSE(t *testing.T) []byte {
	return append(append(event(t, map[string]any{
		"model": "custom-model", "object": "chat.completion.chunk",
		"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{"role": "assistant", "content": "done"}, "finish_reason": "stop",
		}},
	}), event(t, map[string]any{
		"choices": []any{}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	})...), []byte("data: [DONE]\n\n")...)
}

func TestHostBuildsExactProviderTranscript(t *testing.T) {
	attempt := testAttempt(t, 2)
	first, err := attempt.BeginModel(1)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, err := parseStrictJSON(first, complexityLimits{32, 65_536, 10_000})
	if err != nil {
		t.Fatal(err)
	}
	body := parsed.(map[string]any)
	if body["model"] != "custom-model" || body["tool_choice"] != "required" || body["parallel_tool_calls"] != false {
		t.Fatalf("host request authority mismatch: %#v", body)
	}
	messages := body["messages"].([]any)
	if len(messages) != 3 || !strings.HasPrefix(messages[2].(map[string]any)["content"].(string), "Host model-request budget: 2 request(s)") {
		t.Fatalf("unexpected first transcript: %#v", messages)
	}
	tools := body["tools"].([]any)
	if len(tools) != 2 || tools[1].(map[string]any)["function"].(map[string]any)["name"] != "submit_result" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	if _, err := attempt.AcceptModel(toolSSE(t, "lookup", `{"id":7}`, "call-1", []any{})); err != nil {
		t.Fatal(err)
	}
	if _, err := attempt.ValidateToolCall("call-1", "lookup", map[string]any{"id": json.Number("7")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := attempt.AcceptToolResult(map[string]any{"found": 7}); err != nil {
		t.Fatal(err)
	}
	second, err := attempt.BeginModel(2)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, _ = parseStrictJSON(second, complexityLimits{32, 65_536, 10_000})
	body = parsed.(map[string]any)
	choice := body["tool_choice"].(map[string]any)
	if choice["function"].(map[string]any)["name"] != "submit_result" {
		t.Fatalf("final turn did not force submission: %#v", choice)
	}
	messages = body["messages"].([]any)
	if len(messages) != 5 || messages[3].(map[string]any)["tool_call_id"] != "call-1" || !strings.HasPrefix(messages[4].(map[string]any)["content"].(string), "Host model-request budget: 1 request(s)") {
		t.Fatalf("unexpected replay transcript: %#v", messages)
	}
}

func TestAttemptBoundsMaximumIterativeToolResultBeforeReplay(t *testing.T) {
	attempt := testAttempt(t, 2)
	if _, err := attempt.BeginModel(1); err != nil {
		t.Fatal(err)
	}
	if _, err := attempt.AcceptModel(toolSSE(t, "lookup", `{"id":7}`, "call-large", []any{})); err != nil {
		t.Fatal(err)
	}
	content, bounded, err := attempt.AcceptToolResult(map[string]any{
		"body": strings.Repeat("x", testResponseLimits.MaxStringBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope := bounded.(map[string]any)
	prefix, _ := envelope["content_prefix"].(string)
	originalBytes, _ := envelope["original_bytes"].(int)
	if attempt.Usage.TruncatedToolResults != 1 || envelope["truncated"] != true || prefix == "" || originalBytes <= len(prefix) {
		t.Fatalf("tool result was not explicitly bounded: usage=%#v result=%#v", attempt.Usage, bounded)
	}
	if canonical, _ := canonicalJSON(bounded); content != string(canonical) {
		t.Fatal("capsule replay content differs from the accepted truncation envelope")
	}
	second, err := attempt.BeginModel(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) > testResponseLimits.MaxBytes || !modelbudget.Fits(second, 8_192, 1_024, modelbudget.DefaultSafetyTokens) {
		t.Fatalf("iterative payload exceeded budget: bytes=%d", len(second))
	}
}

func TestSSEStrictToolUsageAndLiteLLMNormalization(t *testing.T) {
	lookup, err := compileSchema(testLookupSchema)
	if err != nil {
		t.Fatal(err)
	}
	output, err := compileSchema(testOutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	validators := map[string]*compiledSchema{"lookup": lookup, "submit_result": output}
	for _, choices := range [][]any{
		{},
		{map[string]any{"index": 0, "delta": map[string]any{}}},
		{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": nil, "logprobs": nil}},
	} {
		accepted, err := ParseOpenAISSE(toolSSE(t, "lookup", `{"id":7}`, "call-1", choices), "custom-model", validators, testResponseLimits)
		if err != nil {
			t.Fatal(err)
		}
		if accepted.Pending == nil || accepted.Pending.Name != "lookup" || accepted.Usage != (Usage{ModelCalls: 1, InputTokens: 10, OutputTokens: 4, TotalTokens: 14}) ||
			!strings.HasSuffix(string(accepted.NormalizedSSE), "data: [DONE]\n\n") {
			t.Fatalf("unexpected accepted response: %#v", accepted)
		}
	}
}

func TestSSERejectsDuplicateUnknownMixedAndIncoherentResponses(t *testing.T) {
	validator, _ := compileSchema(testLookupSchema)
	validators := map[string]*compiledSchema{"lookup": validator}
	valid := toolSSE(t, "lookup", `{"id":7}`, "call-1", []any{})
	cases := [][]byte{
		bytesReplaceOnce(valid, `"model":"custom-model"`, `"model":"wrong"`),
		bytesReplaceOnce(valid, `"model":"custom-model"`, `"model":"custom-model","model":"custom-model"`),
		bytesReplaceOnce(valid, `"role":"assistant"`, `"role":"assistant","content":"mixed"`),
		bytesReplaceOnce(valid, `"total_tokens":14`, `"total_tokens":15`),
		bytesReplaceOnce(valid, `"choices":[]`, `"choices":[],"evil":true`),
		valid[:len(valid)-1],
	}
	for index, body := range cases {
		if _, err := ParseOpenAISSE(body, "custom-model", validators, testResponseLimits); err == nil {
			t.Fatalf("case %d unexpectedly accepted", index)
		}
	}
}

func TestAttemptRetainsUsageOnPolicyInvalidResponse(t *testing.T) {
	attempt := testAttempt(t, 2)
	before := attempt.Messages()
	if _, err := attempt.BeginModel(1); err != nil {
		t.Fatal(err)
	}
	if _, err := attempt.AcceptModel(assistantSSE(t)); err == nil {
		t.Fatal("assistant content unexpectedly accepted")
	}
	if attempt.Usage.TotalTokens != 2 || !reflect.DeepEqual(attempt.Messages(), before) {
		t.Fatalf("usage/transcript policy mismatch: %#v %#v", attempt.Usage, attempt.Messages())
	}
}

func TestJSONSchemaSubsetMatchesDocumentationShapes(t *testing.T) {
	schema := map[string]any{
		"type": "object", "properties": map[string]any{
			"slug":  map[string]any{"type": "string", "pattern": `^[a-z0-9-]+$`, "maxLength": 12},
			"lines": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": []any{"integer", "null"}, "minimum": 1}},
		}, "required": []any{"slug", "lines"}, "additionalProperties": false,
	}
	compiled, err := compileSchema(schema)
	if err != nil {
		t.Fatal(err)
	}
	if !compiled.valid(map[string]any{"slug": "api-v1", "lines": []any{json.Number("1"), nil}}) ||
		compiled.valid(map[string]any{"slug": "API", "lines": []any{json.Number("0")}}) ||
		compiled.valid(map[string]any{"slug": "api", "lines": []any{json.Number("1")}, "extra": true}) {
		t.Fatal("compiled schema did not enforce the closed documentation shape")
	}
}

func TestBindingRejectsRequestAuthorityExtensions(t *testing.T) {
	binding := testBinding()
	for _, mutate := range []func(*Binding){
		func(value *Binding) { value.BodyOptions = map[string]any{"model": "evil"} },
		func(value *Binding) { value.BodyOptions = map[string]any{"temperature": 3} },
		func(value *Binding) { value.Headers = map[string]string{"authorization": "secret"} },
		func(value *Binding) { value.MaxRetries = 11 },
		func(value *Binding) { value.Timeout = 60*time.Second + time.Millisecond },
		func(value *Binding) {
			value.ReasoningEffort = providers.EffortNone
			value.ReasoningOptions = map[string]any{"reasoning_effort": "high"}
		},
	} {
		candidate := binding
		mutate(&candidate)
		if _, err := normalizeBinding(candidate); err == nil {
			t.Fatal("invalid authority extension was accepted")
		}
	}
}

func TestCanonicalJSONPreservesLiteralAndActualLineSeparators(t *testing.T) {
	encoded, err := canonicalJSON(map[string]any{"actual": "\u2028\u2029", "literal": `\u2028\u2029`})
	if err != nil {
		t.Fatal(err)
	}
	expected := []byte("{\"actual\":\"\u2028\u2029\",\"literal\":\"\\\\u2028\\\\u2029\"}")
	if !bytes.Equal(encoded, expected) {
		t.Fatalf("line separators were not encoded canonically: %q", encoded)
	}
	value, _, err := parseStrictJSON(encoded, complexityLimits{8, 1024, 20})
	if err != nil || value.(map[string]any)["literal"] != `\u2028\u2029` {
		t.Fatalf("canonical JSON changed string semantics: %#v %v", value, err)
	}
}

func bytesReplaceOnce(body []byte, old, replacement string) []byte {
	return []byte(strings.Replace(string(body), old, replacement, 1))
}

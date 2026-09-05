package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func validRequest(model string) map[string]any {
	tools := []any{}
	for _, name := range []string{"list", "glob", "grep", "read", "submit_result"} {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
			"name": name, "strict": true, "parameters": map[string]any{"type": "object"},
		}})
	}
	return map[string]any{
		"model": model, "messages": []any{map[string]any{"role": "user", "content": "verify"}},
		"stream": true, "stream_options": map[string]any{"include_usage": true}, "n": json.Number("1"),
		"tools": tools, "tool_choice": "required", "parallel_tool_calls": false,
		"max_tokens": json.Number("1024"), "seed": json.Number("7"), "reasoning_effort": "high",
	}
}

func TestAcceptRequestEnforcesProviderPolicy(t *testing.T) {
	environment := map[string]string{"PLANNER_API_TOKEN": "planner-secret"}
	request := validRequest("planner-verification-model")
	accepted, err := acceptRequest("/v1/chat/completions", "Bearer planner-secret", "planner", request, environment)
	if err != nil || accepted.model != "planner-verification-model" || accepted.turn != 1 || accepted.tool != "read" {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"reserved body", func(value map[string]any) { value["reasoning"] = map[string]any{} }, "request policy rejected"},
		{"reasoning", func(value map[string]any) { value["reasoning_effort"] = "medium" }, "reasoning effort rejected"},
		{"stream", func(value map[string]any) { value["stream"] = false }, "body policy rejected"},
		{"tool strictness", func(value map[string]any) {
			value["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["strict"] = false
		}, "tool policy rejected"},
		{"tool set", func(value map[string]any) { value["tools"] = value["tools"].([]any)[1:] }, "tool policy rejected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copy := validRequest("planner-verification-model")
			test.mutate(copy)
			_, err := acceptRequest("/v1/chat/completions", "Bearer planner-secret", "planner", copy, environment)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v want=%q", err, test.want)
			}
		})
	}
	if _, err := acceptRequest("/v1/chat/completions", "Bearer wrong", "planner", request, environment); err == nil || err.Error() != "authentication rejected" {
		t.Fatalf("wrong token err=%v", err)
	}
}

func TestStalledPolicyRequiresOnlyNamedSubmit(t *testing.T) {
	request := validRequest("stalled-verification-model")
	request["tools"] = []any{request["tools"].([]any)[4]}
	request["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": "submit_result"}}
	delete(request, "reasoning_effort")
	accepted, err := acceptRequest("/v1/chat/completions", "Bearer protocol-secret", "", request,
		map[string]string{"PROTOCOL_API_TOKEN": "protocol-secret"})
	if err != nil || accepted.tool != "submit_result" || len(accepted.arguments) < 700_000 || accepted.usage != (usage{31, 7, 38}) {
		t.Fatalf("accepted=%+v arguments=%d err=%v", accepted, len(accepted.arguments), err)
	}
}

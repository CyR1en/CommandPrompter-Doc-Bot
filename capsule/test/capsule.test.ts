import assert from "node:assert/strict";
import test from "node:test";
import { Agent } from "@earendil-works/pi-agent-core";
import type { AgentTool, StreamFn } from "@earendil-works/pi-agent-core";
import { createProvider } from "@earendil-works/pi-ai";
import type { Model } from "@earendil-works/pi-ai";
import { openAICompletionsApi } from "@earendil-works/pi-ai/api/openai-completions.lazy";
import { EventStream } from "@earendil-works/pi-ai/compat";
import type {
  AssistantMessage,
  AssistantMessageEvent,
} from "@earendil-works/pi-ai/compat";
import { Type } from "typebox";

import {
  AttemptBudget,
  SubmissionPolicy,
  submissionBatchAllowed,
  validateAttemptLimits,
} from "../src/attempt-policy.js";
import { writeAll } from "../src/framing.js";
import { compileJsonSchema } from "../src/json-schema.js";
import { piReasoningState } from "../src/reasoning.js";
import {
  parseCapsuleMessage,
  parseHostMessage,
  validateComplexity,
} from "../src/wire.js";
import { Check } from "typebox/value";

class MockAssistantStream extends EventStream<
  AssistantMessageEvent,
  AssistantMessage
> {
  constructor() {
    super(
      (event) => event.type === "done" || event.type === "error",
      (event) => {
        if (event.type === "done") return event.message;
        if (event.type === "error") return event.error;
        throw new Error("unexpected mock event");
      },
    );
  }
}

function toolUseMessage(names: readonly string[]): AssistantMessage {
  const content: Extract<
    AssistantMessage["content"][number],
    { type: "toolCall" }
  >[] = names.map((name, index) => ({
    type: "toolCall",
    id: `call-${index}`,
    name,
    arguments: {},
  }));
  return {
    role: "assistant",
    content,
    api: "openai-completions",
    provider: "custom-openai-compatible",
    model: "fixture",
    usage: {
      input: 1,
      output: 1,
      cacheRead: 0,
      cacheWrite: 0,
      totalTokens: 2,
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
    },
    stopReason: "toolUse",
    timestamp: 1,
  };
}

test("dynamic JSON schemas retain required and closed-object behavior", () => {
  const schema = compileJsonSchema({
    type: "object",
    properties: { answer: { type: "string", maxLength: 8 } },
    required: ["answer"],
    additionalProperties: false,
  });
  assert.equal(Check(schema, { answer: "ok" }), true);
  assert.equal(Check(schema, {}), false);
  assert.equal(Check(schema, { answer: "ok", extra: true }), false);
});

test("wire validation rejects open branches and complexity excess", () => {
  const start = {
    type: "start",
    protocol_version: 1,
    attempt_id: "attempt-1",
    role: "PLANNER",
    system_prompt: "system",
    prompt: "prompt",
    tools: [],
    output_schema: {
      type: "object",
      properties: {},
      additionalProperties: false,
    },
    provider: {
      model_id: "fixture",
      body_options: {},
      reasoning_effort: "high",
      context_window: 8192,
      max_output_tokens: 1024,
      timeout_ms: 60_000,
      capsule_runtime_revision: "pi-0.84.4-r9",
    },
    limits: {
      max_frame_bytes: 1024,
      max_aggregate_bytes: 4096,
      max_string_bytes: 256,
      max_depth: 8,
      max_keys: 100,
      max_fetch_body_bytes: 1024,
      max_fetches: 4,
      max_model_requests: 2,
    },
  };
  const parsed = parseHostMessage(start);
  assert.equal(parsed.type, "start");
  assert.equal(parsed.provider.reasoning_effort, "high");
  const minimumTimeout = parseHostMessage({
    ...start,
    provider: { ...start.provider, timeout_ms: 1_000 },
  });
  assert.equal(minimumTimeout.type, "start");
  assert.equal(minimumTimeout.provider.timeout_ms, 1_000);
  const writer = parseHostMessage({
    ...start,
    role: "PAGE_WRITER",
    provider: { ...start.provider, reasoning_effort: "medium" },
  });
  assert.equal(writer.type, "start");
  assert.equal(writer.provider.reasoning_effort, "medium");
  assert.throws(
    () =>
      parseHostMessage({
        ...start,
        provider: { ...start.provider, timeout_ms: 60_001 },
      }),
    /invalid host/,
  );
  for (const reasoning_effort of ["xhigh", "HIGH", "unbounded"]) {
    assert.throws(
      () =>
        parseHostMessage({
          ...start,
          provider: { ...start.provider, reasoning_effort },
        }),
      /invalid host/,
    );
  }
  assert.throws(
    () => parseHostMessage({ ...start, unexpected: true }),
    /invalid host/,
  );
  assert.throws(
    () =>
      validateComplexity(
        { a: { b: { c: { d: 1 } } } },
        { maxStringBytes: 256, maxDepth: 3, maxKeys: 100 },
      ),
    /depth/,
  );
  assert.throws(
    () =>
      parseHostMessage({
        ...start,
        provider: { ...start.provider, base_url: "https://evil.invalid" },
      }),
    /invalid host/,
  );
  assert.throws(
    () =>
      parseHostMessage({
        ...start,
        limits: { ...start.limits, max_aggregate_bytes: 2048 },
      }),
    /invalid host/,
  );
  assert.throws(
    () =>
      parseHostMessage({
        ...start,
        provider: { ...start.provider, timeout_ms: 999 },
      }),
    /invalid host/,
  );
  assert.throws(
    () =>
      parseHostMessage({
        ...start,
        provider: { ...start.provider, max_output_tokens: 1_000_001 },
      }),
    /invalid host/,
  );
});

test("model relay messages cannot carry request or usage authority", () => {
  assert.deepEqual(
    parseCapsuleMessage({ type: "model_request", id: "model-1", turn: 1 }),
    { type: "model_request", id: "model-1", turn: 1 },
  );
  for (const authority of [
    { url: "https://evil.invalid" },
    { method: "POST" },
    { headers: { authorization: "secret" } },
    { model: "evil" },
    { messages: [] },
    { tools: [] },
    { reasoning_effort: "high" },
    { thinking_level: "high" },
    { body_base64: "e30=" },
    { usage: { model_calls: 999 } },
  ]) {
    assert.throws(
      () =>
        parseCapsuleMessage({
          type: "model_request",
          id: "model-1",
          turn: 1,
          ...authority,
        }),
      /invalid capsule/,
    );
  }
  assert.throws(
    () =>
      parseHostMessage({
        type: "model_result",
        id: "model-1",
        body_base64: "ZGF0YTogW0RPTkVdXG5cbg==",
        status: 200,
      }),
    /invalid host/,
  );
});

test("bounded reasoning effort maps to Pi model and thinking state", () => {
  assert.deepEqual(piReasoningState("none"), {
    modelReasoning: false,
    thinkingLevel: "off",
  });
  const efforts: Parameters<typeof piReasoningState>[0][] = [
    "minimal",
    "low",
    "medium",
    "high",
    "max",
  ];
  for (const effort of efforts) {
    assert.deepEqual(piReasoningState(effort), {
      modelReasoning: true,
      thinkingLevel: effort,
    });
  }
});

test("pinned Pi parser accepts the host-normalized SSE corpus", async () => {
  const model: Model<"openai-completions"> = {
    id: "fixture",
    name: "fixture",
    api: "openai-completions",
    provider: "fixture-provider",
    baseUrl: "https://capsule-relay.invalid/v1",
    reasoning: true,
    input: ["text"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 8192,
    maxTokens: 1024,
  };
  const provider = createProvider({
    id: "fixture-provider",
    name: "Fixture",
    auth: {
      apiKey: {
        name: "fixture",
        async resolve() {
          return { auth: { apiKey: "placeholder" }, source: "test" };
        },
      },
    },
    models: [model],
    api: openAICompletionsApi(),
  });
  const chunks = [
    {
      id: "host-normalized",
      object: "chat.completion.chunk",
      model: "fixture",
      choices: [
        {
          index: 0,
          delta: {
            role: "assistant",
            reasoning_content: "provider-private",
            tool_calls: [
              {
                index: 0,
                id: "call-1",
                type: "function",
                function: {
                  name: "submit_result",
                  arguments: '{"answer":"ok"}',
                },
              },
            ],
          },
          finish_reason: null,
        },
      ],
    },
    {
      id: "host-normalized",
      object: "chat.completion.chunk",
      model: "fixture",
      choices: [{ index: 0, delta: {}, finish_reason: "tool_calls" }],
    },
    {
      id: "host-normalized",
      object: "chat.completion.chunk",
      model: "fixture",
      choices: [],
      usage: { prompt_tokens: 3, completion_tokens: 2, total_tokens: 5 },
    },
  ];
  const sse = `${chunks.map((chunk) => `data: ${JSON.stringify(chunk)}\n\n`).join("")}data: [DONE]\n\n`;
  const message = await provider
    .streamSimple(
      model,
      { messages: [{ role: "user", content: "prompt", timestamp: 1 }] },
      {
        apiKey: "placeholder",
        maxRetries: 0,
        reasoning: "high",
        fetch: async () =>
          new Response(sse, {
            status: 200,
            headers: { "content-type": "text/event-stream" },
          }),
      },
    )
    .result();
  assert.equal(message.stopReason, "toolUse");
  assert.equal(message.usage.totalTokens, 5);
  assert.deepEqual(message.content, [
    {
      type: "thinking",
      thinking: "provider-private",
      thinkingSignature: "reasoning_content",
    },
    {
      type: "toolCall",
      id: "call-1",
      name: "submit_result",
      arguments: { answer: "ok" },
    },
  ]);
});

test("attempt budgets and cross-field limits bound fetches and model turns", () => {
  const limits = {
    max_frame_bytes: 16_384,
    max_aggregate_bytes: 32_768,
    max_string_bytes: 4096,
    max_fetch_body_bytes: 1024,
    max_fetches: 2,
    max_model_requests: 1,
  };
  validateAttemptLimits(limits);
  const budget = new AttemptBudget(limits);
  assert.equal(budget.beginModelRequest(), 1);
  assert.throws(() => budget.beginModelRequest(), /model request budget/);
  budget.beginFetch();
  budget.beginFetch();
  assert.throws(() => budget.beginFetch(), /fetch budget/);
  assert.throws(
    () => validateAttemptLimits({ ...limits, max_frame_bytes: 9000 }),
    /cross-field/,
  );
  assert.throws(
    () => validateAttemptLimits({ ...limits, max_aggregate_bytes: 4096 }),
    /cross-field/,
  );
  assert.throws(
    () => validateAttemptLimits({ ...limits, max_model_requests: 3 }),
    /cross-field/,
  );
});

test("result submission must be one exclusive terminal tool call", () => {
  assert.equal(submissionBatchAllowed(["submit_result"], false), true);
  assert.equal(submissionBatchAllowed(["read"], false), true);
  assert.equal(submissionBatchAllowed(["read", "submit_result"], false), false);
  assert.equal(
    submissionBatchAllowed(["submit_result", "submit_result"], false),
    false,
  );
  assert.equal(submissionBatchAllowed(["submit_result"], true), false);
  assert.equal(submissionBatchAllowed(["read"], true), false);
});

test("pinned Pi loop blocks mixed submission batches and stops on one submission", async () => {
  async function run(toolNames: readonly string[]) {
    const policy = new SubmissionPolicy();
    const parameters = Type.Object({});
    let reads = 0;
    let submissions = 0;
    let modelRequests = 0;
    const tools: AgentTool<typeof parameters, unknown>[] = [
      {
        name: "read",
        label: "Read",
        description: "Read",
        parameters,
        executionMode: "sequential",
        async execute() {
          reads += 1;
          return { content: [{ type: "text", text: "read" }], details: {} };
        },
      },
      {
        name: "submit_result",
        label: "Submit",
        description: "Submit",
        parameters,
        executionMode: "sequential",
        async execute() {
          submissions += 1;
          policy.submit({ answer: "ok" });
          return {
            content: [{ type: "text", text: "submitted" }],
            details: {},
            terminate: true,
          };
        },
      },
    ];
    const streamFn: StreamFn = () => {
      modelRequests += 1;
      const stream = new MockAssistantStream();
      queueMicrotask(() => {
        stream.push({
          type: "done",
          reason: "toolUse",
          message: toolUseMessage(toolNames),
        });
      });
      return stream;
    };
    const agent = new Agent({
      streamFn,
      initialState: { tools },
      toolExecution: "sequential",
      async beforeToolCall() {
        return policy.blockToolCall();
      },
      shouldStopAfterTurn() {
        return policy.shouldStop();
      },
    });
    agent.subscribe((event) => {
      if (event.type !== "message_end" || event.message.role !== "assistant")
        return;
      policy.observeToolBatch(
        event.message.content
          .filter((content) => content.type === "toolCall")
          .map((content) => content.name),
      );
    });
    await agent.prompt("start");
    return { policy, reads, submissions, modelRequests };
  }

  const mixed = await run(["read", "submit_result"]);
  assert.deepEqual(
    {
      reads: mixed.reads,
      submissions: mixed.submissions,
      modelRequests: mixed.modelRequests,
    },
    { reads: 0, submissions: 0, modelRequests: 1 },
  );
  assert.throws(() => mixed.policy.result(), /no structured result/);

  const single = await run(["submit_result"]);
  assert.deepEqual(
    {
      reads: single.reads,
      submissions: single.submissions,
      modelRequests: single.modelRequests,
    },
    { reads: 0, submissions: 1, modelRequests: 1 },
  );
  assert.deepEqual(single.policy.result(), { answer: "ok" });
  assert.throws(
    () => single.policy.submit({ answer: "twice" }),
    /activity after/,
  );
});

test("framing writes complete headers and payloads across partial writes", () => {
  const output: number[] = [];
  writeAll(1, Buffer.from("abcdef"), (_fd, buffer, offset, length) => {
    const count = Math.min(2, length);
    output.push(...buffer.subarray(offset, offset + count));
    return count;
  });
  assert.equal(Buffer.from(output).toString("utf8"), "abcdef");
  assert.throws(() => writeAll(1, Buffer.from("x"), () => 0), /no progress/);
});

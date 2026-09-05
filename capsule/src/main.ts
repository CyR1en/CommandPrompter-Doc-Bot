import type { AgentTool, StreamFn } from "@earendil-works/pi-agent-core";
import { Check } from "typebox/value";
import type { Model, TSchema } from "@earendil-works/pi-ai";

import {
  AttemptBudget,
  SubmissionPolicy,
  validateAttemptLimits,
} from "./attempt-policy.js";
import { FrameIO } from "./framing.js";
import { compileJsonSchema } from "./json-schema.js";
import { piReasoningState } from "./reasoning.js";
import type { StartMessage } from "./wire.js";

class Cancelled extends Error {}

const CAPSULE_RUNTIME_REVISION = "pi-0.84.4-r9";

class AttemptFailure extends Error {
  readonly cancelled: boolean;

  constructor(error: unknown) {
    super("attempt failed");
    this.cancelled = error instanceof Cancelled;
  }
}

function exhaustive(value: never): never {
  throw new Error(`unexpected protocol message: ${String(value)}`);
}

function objectValue(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw new Error("expected object");
  return Object.fromEntries(Object.entries(value));
}

function safeBase64(value: string, limit: number): Uint8Array {
  if (
    !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(
      value,
    )
  ) {
    throw new Error("invalid base64 response body");
  }
  const bytes = Buffer.from(value, "base64");
  if (bytes.byteLength > limit)
    throw new Error("model response body exceeds limit");
  return bytes;
}

async function runAttempt(io: FrameIO, start: StartMessage): Promise<void> {
  if (start.provider.capsule_runtime_revision !== CAPSULE_RUNTIME_REVISION)
    throw new Error("capsule runtime revision mismatch");
  validateAttemptLimits(start.limits);
  let sequence = 0;
  const submission = new SubmissionPolicy();
  let outstanding: string | undefined;
  let activeModelRequest = 0;
  const budget = new AttemptBudget(start.limits);
  const ids = new Set<string>();
  const nextId = (prefix: string): string => {
    sequence += 1;
    const id = `${prefix}:${start.attempt_id}:${sequence}`;
    if (ids.has(id)) throw new Error("duplicate capsule operation ID");
    ids.add(id);
    return id;
  };
  try {
    const waitFor = (id: string, expected: "tool_result" | "model_result") => {
      if (outstanding !== undefined)
        throw new Error("concurrent relay operation is forbidden");
      outstanding = id;
      try {
        const message = io.readHost();
        switch (message.type) {
          case "cancel":
            throw new Cancelled("attempt cancelled");
          case "tool_result":
          case "model_result":
            if (message.type !== expected || message.id !== id)
              throw new Error("relay response is out of order");
            return message;
          case "start":
            throw new Error("duplicate start message");
        }
      } finally {
        outstanding = undefined;
      }
    };

    const brokerFetch: typeof globalThis.fetch = async () => {
      submission.assertActivityAllowed();
      budget.beginFetch();
      if (activeModelRequest < 1)
        throw new Error("fetch outside a model request is forbidden");
      const id = nextId("model");
      io.write({
        type: "model_request",
        id,
        turn: activeModelRequest,
      });
      const message = waitFor(id, "model_result");
      if (message.type !== "model_result")
        throw new Error("expected model response");
      const body = safeBase64(
        message.body_base64,
        start.limits.max_fetch_body_bytes,
      );
      return new Response(Buffer.from(body), {
        status: 200,
        headers: { "content-type": "text/event-stream" },
      });
    };

    globalThis.fetch = brokerFetch;
    const [{ Agent }, { createProvider }, { openAICompletionsApi }] =
      await Promise.all([
        import("@earendil-works/pi-agent-core"),
        import("@earendil-works/pi-ai"),
        import("@earendil-works/pi-ai/api/openai-completions.lazy"),
      ]);
    const reasoning = piReasoningState(start.provider.reasoning_effort);
    const model: Model<"openai-completions"> = {
      id: start.provider.model_id,
      name: start.provider.model_id,
      api: "openai-completions",
      provider: "custom-openai-compatible",
      baseUrl: "https://capsule-relay.invalid/v1",
      headers: {},
      reasoning: reasoning.modelReasoning,
      thinkingLevelMap: { max: "max" },
      input: ["text"],
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      contextWindow: start.provider.context_window,
      maxTokens: start.provider.max_output_tokens,
      samplingParams: start.provider.body_options,
    };
    const provider = createProvider({
      id: "custom-openai-compatible",
      name: "Custom OpenAI compatible",
      auth: {
        apiKey: {
          name: "relay placeholder",
          async resolve() {
            return {
              auth: { apiKey: "capsule-relay-placeholder" },
              source: "relay",
            };
          },
        },
      },
      models: [model],
      api: openAICompletionsApi(),
    });
    const streamFn: StreamFn = (_requestedModel, context, options) => {
      submission.assertActivityAllowed();
      activeModelRequest = budget.beginModelRequest();
      return provider.streamSimple(model, context, {
        ...options,
        apiKey: "capsule-relay-placeholder",
        fetch: brokerFetch,
        transport: "sse",
        timeoutMs: start.provider.timeout_ms,
        maxRetries: 0,
      });
    };

    const tools: AgentTool<TSchema, unknown>[] = start.tools.map((tool) => {
      const parameters = compileJsonSchema(tool.parameters);
      return {
        name: tool.name,
        label: tool.name,
        description: tool.description,
        parameters,
        executionMode: "sequential",
        async execute(toolCallId, argumentsValue) {
          submission.assertActivityAllowed();
          const argumentsObject = objectValue(argumentsValue);
          const id = nextId("tool");
          io.write({
            type: "tool_call",
            id,
            provider_call_id: toolCallId,
            name: tool.name,
            arguments: argumentsObject,
          });
          const message = waitFor(id, "tool_result");
          if (message.type !== "tool_result")
            throw new Error("expected tool response");
          return {
            content: [{ type: "text", text: message.content }],
            details: message.result,
          };
        },
      };
    });
    const outputSchema = compileJsonSchema(start.output_schema);
    tools.push({
      name: "submit_result",
      label: "Submit result",
      description: "Validate and submit the final structured result.",
      parameters: outputSchema,
      executionMode: "sequential",
      async execute(_toolCallId, argumentsValue) {
        if (!Check(outputSchema, argumentsValue))
          throw new Error("submitted result failed schema validation");
        const submitted = objectValue(argumentsValue);
        submission.submit(submitted);
        return {
          content: [{ type: "text", text: "structured result accepted" }],
          details: submitted,
          terminate: true,
        };
      },
    });

    const agent = new Agent({
      streamFn,
      initialState: {
        model,
        systemPrompt: start.system_prompt,
        tools,
        thinkingLevel: reasoning.thinkingLevel,
      },
      toolExecution: "sequential",
      transport: "sse",
      async beforeToolCall() {
        return submission.blockToolCall();
      },
      shouldStopAfterTurn() {
        return submission.shouldStop();
      },
    });
    agent.subscribe((event) => {
      if (event.type !== "message_end" || event.message.role !== "assistant")
        return;
      const toolNames = event.message.content
        .filter((content) => content.type === "toolCall")
        .map((content) => content.name);
      submission.observeToolBatch(toolNames);
    });
    await agent.prompt(start.prompt);
    const submitted = submission.result();
    io.write({ type: "complete", output: submitted });
  } catch (error) {
    throw new AttemptFailure(error);
  }
}

async function main(): Promise<void> {
  const io = new FrameIO();
  let terminal = false;
  try {
    const start = io.readHost();
    if (start.type !== "start")
      throw new Error("first protocol message must be start");
    io.applyLimits(start);
    await runAttempt(io, start);
    terminal = true;
  } catch (error) {
    if (!terminal) {
      terminal = true;
      try {
        const attempt = error instanceof AttemptFailure ? error : undefined;
        io.write({
          type: "failed",
          code: attempt?.cancelled
            ? "cancelled"
            : attempt
              ? "internal"
              : "protocol",
          message: attempt?.cancelled
            ? "Attempt cancelled."
            : attempt
              ? "Capsule attempt failed safely."
              : "Capsule protocol failed safely.",
        });
      } catch {
        process.exitCode = 1;
      }
    }
  }
}

void main();

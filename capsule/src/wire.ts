import { Type } from "typebox";
import type { Static } from "typebox";
import { Check } from "typebox/value";

import { checkCanonicalWireMessage } from "./canonical-schema.js";

const closed: { additionalProperties: false } = { additionalProperties: false };
const Id = Type.String({
  minLength: 1,
  maxLength: 128,
  pattern: "^[A-Za-z0-9._:-]+$",
});
const ReasoningEffort = Type.Union([
  Type.Literal("none"),
  Type.Literal("minimal"),
  Type.Literal("low"),
  Type.Literal("medium"),
  Type.Literal("high"),
  Type.Literal("max"),
]);
const Limits = Type.Object(
  {
    max_frame_bytes: Type.Integer({ minimum: 1024, maximum: 4_194_304 }),
    max_aggregate_bytes: Type.Integer({ minimum: 4096, maximum: 33_554_432 }),
    max_string_bytes: Type.Integer({ minimum: 256, maximum: 2_097_152 }),
    max_depth: Type.Integer({ minimum: 4, maximum: 64 }),
    max_keys: Type.Integer({ minimum: 32, maximum: 100_000 }),
    max_fetch_body_bytes: Type.Integer({ minimum: 1024, maximum: 1_048_576 }),
    max_fetches: Type.Integer({ minimum: 1, maximum: 1000 }),
    max_model_requests: Type.Integer({ minimum: 1, maximum: 1000 }),
  },
  closed,
);
const Provider = Type.Object(
  {
    model_id: Type.String({ minLength: 1, maxLength: 512 }),
    body_options: Type.Object(
      {
        temperature: Type.Optional(Type.Number({ minimum: 0, maximum: 2 })),
        top_p: Type.Optional(Type.Number({ minimum: 0, maximum: 1 })),
        frequency_penalty: Type.Optional(
          Type.Number({ minimum: -2, maximum: 2 }),
        ),
        presence_penalty: Type.Optional(
          Type.Number({ minimum: -2, maximum: 2 }),
        ),
        seed: Type.Optional(
          Type.Integer({ minimum: -2_147_483_648, maximum: 2_147_483_647 }),
        ),
        stop: Type.Optional(
          Type.Array(Type.String({ minLength: 1, maxLength: 1024 }), {
            minItems: 1,
            maxItems: 4,
          }),
        ),
      },
      closed,
    ),
    reasoning_effort: ReasoningEffort,
    context_window: Type.Integer({ minimum: 1024, maximum: 10_000_000 }),
    max_output_tokens: Type.Integer({ minimum: 1, maximum: 1_000_000 }),
    timeout_ms: Type.Integer({ minimum: 1_000, maximum: 60_000 }),
    capsule_runtime_revision: Type.String({ minLength: 1, maxLength: 128 }),
  },
  closed,
);
const Tool = Type.Object(
  {
    name: Type.String({ minLength: 1, maxLength: 128 }),
    description: Type.String({ maxLength: 4096 }),
    parameters: Type.Record(Type.String(), Type.Unknown()),
  },
  closed,
);
const Start = Type.Object(
  {
    type: Type.Literal("start"),
    protocol_version: Type.Literal(1),
    attempt_id: Id,
    role: Type.Union([Type.Literal("PLANNER"), Type.Literal("PAGE_WRITER")]),
    system_prompt: Type.String(),
    prompt: Type.String(),
    tools: Type.Array(Tool, { maxItems: 64 }),
    output_schema: Type.Record(Type.String(), Type.Unknown()),
    provider: Provider,
    limits: Limits,
  },
  closed,
);
const ToolResult = Type.Object(
  {
    type: Type.Literal("tool_result"),
    id: Id,
    result: Type.Unknown(),
    content: Type.String(),
  },
  closed,
);
const ModelResult = Type.Object(
  {
    type: Type.Literal("model_result"),
    id: Id,
    body_base64: Type.String(),
  },
  closed,
);
const Cancel = Type.Object(
  { type: Type.Literal("cancel"), reason: Type.String({ maxLength: 256 }) },
  closed,
);
const ToolCall = Type.Object(
  {
    type: Type.Literal("tool_call"),
    id: Id,
    provider_call_id: Id,
    name: Type.String({ minLength: 1, maxLength: 128 }),
    arguments: Type.Record(Type.String(), Type.Unknown()),
  },
  closed,
);
const ModelRequest = Type.Object(
  {
    type: Type.Literal("model_request"),
    id: Id,
    turn: Type.Integer({ minimum: 1, maximum: 1000 }),
  },
  closed,
);
const Complete = Type.Object(
  {
    type: Type.Literal("complete"),
    output: Type.Record(Type.String(), Type.Unknown()),
  },
  closed,
);
const Failed = Type.Object(
  {
    type: Type.Literal("failed"),
    code: Type.Union([
      Type.Literal("cancelled"),
      Type.Literal("protocol"),
      Type.Literal("provider"),
      Type.Literal("tool"),
      Type.Literal("invalid_result"),
      Type.Literal("internal"),
    ]),
    message: Type.String({ minLength: 1, maxLength: 400 }),
  },
  closed,
);

export const HostMessage = Type.Union([Start, ToolResult, ModelResult, Cancel]);
export const CapsuleMessage = Type.Union([
  ToolCall,
  ModelRequest,
  Complete,
  Failed,
]);
export type HostMessage = Static<typeof HostMessage>;
export type StartMessage = Static<typeof Start>;
export type CapsuleMessage = Static<typeof CapsuleMessage>;

export type ComplexityLimits = Readonly<{
  maxStringBytes: number;
  maxDepth: number;
  maxKeys: number;
}>;

export function validateComplexity(
  value: unknown,
  limits: ComplexityLimits,
): void {
  const pending: Array<readonly [unknown, number]> = [[value, 1]];
  let keys = 0;
  while (pending.length > 0) {
    const current = pending.pop();
    if (!current) break;
    const [item, depth] = current;
    if (depth > limits.maxDepth)
      throw new Error("protocol JSON depth exceeds limit");
    if (
      typeof item === "string" &&
      Buffer.byteLength(item) > limits.maxStringBytes
    ) {
      throw new Error("protocol JSON string exceeds limit");
    }
    if (Array.isArray(item)) {
      for (const child of item) pending.push([child, depth + 1]);
    } else if (typeof item === "object" && item !== null) {
      const entries = Object.entries(item);
      keys += entries.length;
      if (keys > limits.maxKeys)
        throw new Error("protocol JSON key count exceeds limit");
      for (const [key, child] of entries) {
        if (Buffer.byteLength(key) > limits.maxStringBytes)
          throw new Error("protocol JSON key exceeds limit");
        pending.push([child, depth + 1]);
      }
    }
  }
}

export function parseHostMessage(value: unknown): HostMessage {
  if (!checkCanonicalWireMessage(value) || !Check(HostMessage, value))
    throw new Error("invalid host protocol message");
  return value;
}

export function parseCapsuleMessage(value: unknown): CapsuleMessage {
  if (!checkCanonicalWireMessage(value) || !Check(CapsuleMessage, value))
    throw new Error("invalid capsule protocol message");
  return value;
}

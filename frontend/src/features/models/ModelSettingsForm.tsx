import { useState, type FormEvent, type ReactNode } from "react";

import type { ModelSettingsInput } from "../../api/client";
import { Select } from "../../app/Select";
import { ErrorNotice } from "../../app/StatusBadge";

type TriState = boolean | null;

export function defaultModelSettings(): ModelSettingsInput {
  return {
    context_window_tokens: null,
    extra_body: {},
    max_output_tokens: null,
    max_retries: 2,
    max_concurrent_tasks: 1,
    reasoning_mapping: null,
    reasoning_transport: "none",
    supports_streaming: null,
    supports_structured_output: null,
    supports_temperature: null,
    supports_tools: null,
    timeout_seconds: 60,
    transport: "chat_completions",
  };
}

export function ModelSettingsForm({
  initial,
  onSettingsChange,
  onSubmit,
  pending,
  submitLabel,
}: {
  initial: ModelSettingsInput;
  onSettingsChange?(): void;
  onSubmit(settings: ModelSettingsInput): void;
  pending: boolean;
  submitLabel: string;
}): ReactNode {
  const [transport, setTransport] = useState(initial.transport);
  const [contextWindow, setContextWindow] = useState(numberText(initial.context_window_tokens));
  const [maxOutput, setMaxOutput] = useState(numberText(initial.max_output_tokens));
  const [streaming, setStreaming] = useState<TriState>(initial.supports_streaming ?? null);
  const [tools, setTools] = useState<TriState>(initial.supports_tools ?? null);
  const [structured, setStructured] = useState<TriState>(initial.supports_structured_output ?? null);
  const [temperature, setTemperature] = useState<TriState>(initial.supports_temperature ?? null);
  const [reasoning, setReasoning] = useState(initial.reasoning_transport);
  const [reasoningMapping, setReasoningMapping] = useState(
    initial.reasoning_mapping === null || initial.reasoning_mapping === undefined
      ? ""
      : JSON.stringify(initial.reasoning_mapping, null, 2),
  );
  const [timeout, setTimeoutValue] = useState(String(initial.timeout_seconds));
  const [retries, setRetries] = useState(String(initial.max_retries));
  const [maxConcurrentTasks, setMaxConcurrentTasks] = useState(String(initial.max_concurrent_tasks));
  const [extraBody, setExtraBody] = useState(JSON.stringify(initial.extra_body ?? {}, null, 2));
  const [error, setError] = useState<string | null>(null);

  function submit(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault();
    setError(null);
    try {
      const mapping = reasoning === "custom"
        ? parseObject(reasoningMapping, "Custom reasoning mapping")
        : null;
      onSubmit({
        context_window_tokens: optionalPositiveInteger(contextWindow, "Context window"),
        extra_body: parseObject(extraBody, "Extra body"),
        max_output_tokens: optionalPositiveInteger(maxOutput, "Maximum output"),
        max_retries: boundedInteger(retries, "Retries", 0, 10),
        max_concurrent_tasks: boundedInteger(maxConcurrentTasks, "Concurrent tasks", 1, 32),
        reasoning_mapping: mapping,
        reasoning_transport: reasoning,
        supports_streaming: streaming,
        supports_structured_output: structured,
        supports_temperature: temperature,
        supports_tools: tools,
        timeout_seconds: boundedInteger(timeout, "Timeout", 1, 60),
        transport,
      });
    } catch (caught: unknown) {
      setError(caught instanceof Error ? caught.message : "Check the model settings.");
    }
  }

  return (
    <form className="form-grid model-settings-form" onSubmit={submit}>
      <label>
        Request transport
        <Select
          onChange={(next) => { setTransport(parseTransport(next)); onSettingsChange?.(); }}
          options={[{ label: "Chat Completions", value: "chat_completions" }, { label: "Responses (not assignable)", value: "responses" }]}
          value={transport}
        />
      </label>
      <label>
        Reasoning transport
        <Select
          onChange={(next) => { setReasoning(parseReasoning(next)); onSettingsChange?.(); }}
          options={[{ label: "None", value: "none" }, { label: "Standard reasoning effort", value: "reasoning_effort" }, { label: "Custom field mapping", value: "custom" }]}
          value={reasoning}
        />
      </label>
      <label>
        Context window tokens
        <input inputMode="numeric" min="1" onChange={(event) => { setContextWindow(event.currentTarget.value); onSettingsChange?.(); }} placeholder="Unknown" type="number" value={contextWindow} />
      </label>
      <label>
        Maximum output tokens
        <input inputMode="numeric" min="1" onChange={(event) => { setMaxOutput(event.currentTarget.value); onSettingsChange?.(); }} placeholder="Unknown" type="number" value={maxOutput} />
      </label>
      <TriStateField label="Streaming" onChange={(value) => { setStreaming(value); onSettingsChange?.(); }} value={streaming} />
      <TriStateField label="Tool calling" onChange={(value) => { setTools(value); onSettingsChange?.(); }} value={tools} />
      <TriStateField label="Structured output" onChange={(value) => { setStructured(value); onSettingsChange?.(); }} value={structured} />
      <TriStateField label="Temperature" onChange={(value) => { setTemperature(value); onSettingsChange?.(); }} value={temperature} />
      <label>
        Timeout seconds
        <input max="60" min="1" onChange={(event) => { setTimeoutValue(event.currentTarget.value); onSettingsChange?.(); }} required type="number" value={timeout} />
      </label>
      <label>
        Retry attempts
        <input max="10" min="0" onChange={(event) => { setRetries(event.currentTarget.value); onSettingsChange?.(); }} required type="number" value={retries} />
      </label>
      <label>
        Maximum concurrent tasks
        <input aria-describedby="model-concurrency-help" max="32" min="1" onChange={(event) => { setMaxConcurrentTasks(event.currentTarget.value); onSettingsChange?.(); }} required type="number" value={maxConcurrentTasks} />
        <small id="model-concurrency-help">Queued model requests run in parallel up to this limit and the available worker capacity.</small>
      </label>
      {reasoning === "custom" ? (
        <label className="full-field">
          Custom reasoning mapping (JSON)
          <textarea aria-describedby="reasoning-mapping-help" onChange={(event) => { setReasoningMapping(event.currentTarget.value); onSettingsChange?.(); }} required rows={6} value={reasoningMapping} />
          <small id="reasoning-mapping-help">Use exactly a safe field and effort values, for example {`{"field":"thinking","values":{"low":"small","high":"large"}}`}.</small>
        </label>
      ) : null}
      <label className="full-field">
        Extra request body (JSON)
        <textarea onChange={(event) => { setExtraBody(event.currentTarget.value); onSettingsChange?.(); }} required rows={6} value={extraBody} />
      </label>
      <ErrorNotice message={error} />
      <button className="button primary" disabled={pending} type="submit">
        {pending ? "Saving…" : submitLabel}
      </button>
    </form>
  );
}

function TriStateField({
  label,
  onChange,
  value,
}: {
  label: string;
  onChange(value: TriState): void;
  value: TriState;
}): ReactNode {
  return (
    <label>
      {label} support
      <Select
        onChange={(next) => onChange(parseTriState(next))}
        options={[{ label: "Unknown", value: "unknown" }, { label: "Supported", value: "yes" }, { label: "Unsupported", value: "no" }]}
        value={triStateText(value)}
      />
    </label>
  );
}

function parseObject(value: string, label: string): Record<string, unknown> {
  let parsed: unknown;
  try {
    parsed = JSON.parse(value);
  } catch {
    throw new Error(`${label} must be valid JSON.`);
  }
  if (!isJsonObject(parsed)) throw new Error(`${label} must be a JSON object.`);
  return parsed;
}

function isJsonObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function optionalPositiveInteger(value: string, label: string): number | null {
  if (value.trim() === "") return null;
  return boundedInteger(value, label, 1, Number.MAX_SAFE_INTEGER);
}

function boundedInteger(value: string, label: string, minimum: number, maximum: number): number {
  const parsed = Number(value);
  if (!Number.isInteger(parsed) || parsed < minimum || parsed > maximum) {
    throw new Error(`${label} must be a whole number from ${minimum} to ${maximum}.`);
  }
  return parsed;
}

function numberText(value: number | null | undefined): string {
  return value === null || value === undefined ? "" : String(value);
}

function parseTransport(value: string): ModelSettingsInput["transport"] {
  return value === "responses" ? "responses" : "chat_completions";
}

function parseReasoning(value: string): ModelSettingsInput["reasoning_transport"] {
  if (value === "custom") return "custom";
  if (value === "reasoning_effort") return "reasoning_effort";
  return "none";
}

function parseTriState(value: string): TriState {
  if (value === "yes") return true;
  if (value === "no") return false;
  return null;
}

function triStateText(value: TriState): "unknown" | "yes" | "no" {
  if (value === true) return "yes";
  if (value === false) return "no";
  return "unknown";
}

import { useMemo, useState, type FormEvent, type ReactNode } from "react";

import type {
  Agent,
  AgentAnswerMode,
  AgentConfigurationInput,
  AgentEvidenceAccess,
  AgentReasoningEffort,
  CreateAgentInput,
  KnowledgeBase,
  ModelProfile,
  ProviderEndpoint,
} from "../../api/client";
import { Select } from "../../app/Select";

const reasoningEfforts = ["none", "minimal", "low", "medium", "high", "max"] satisfies AgentReasoningEffort[];
const answerModes = ["tool_calling", "single_pass"] satisfies AgentAnswerMode[];
const evidenceAccess = ["wiki_only", "wiki_and_source"] satisfies AgentEvidenceAccess[];
const agentKeyPattern = /^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$/u;
const languagePattern = /^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8})*$/u;

type AgentConfigurationFormProps =
  | {
      busy: boolean;
      knowledgeBases: KnowledgeBase[];
      kind: "create";
      models: ModelProfile[];
      onSubmit(value: CreateAgentInput): void;
      providers: ProviderEndpoint[];
    }
  | {
      agent: Agent;
      busy: boolean;
      knowledgeBases: KnowledgeBase[];
      kind: "replace";
      models: ModelProfile[];
      onSubmit(value: AgentConfigurationInput): void;
      providers: ProviderEndpoint[];
    };

export function AgentConfigurationForm(props: AgentConfigurationFormProps): ReactNode {
  const initial = props.kind === "replace"
    ? props.agent.current_version.configuration
    : newAgentConfiguration();
  const [agentKey, setAgentKey] = useState("");
  const [draft, setDraft] = useState<AgentConfigurationInput>(initial);
  const [knowledgeToAdd, setKnowledgeToAdd] = useState("");
  const normalized = useMemo(() => normalizeConfiguration(draft), [draft]);
  const messages = validationMessages(
    props.kind === "create" ? agentKey : props.agent.key,
    normalized,
    props.models,
  );
  const dirty = props.kind === "create"
    ? true
    : !sameConfiguration(normalized, props.agent.current_version.configuration);
  const selectedModel = props.models.find((model) => model.id === draft.model_profile_id);
  const selectedProvider = props.providers.find((provider) => provider.id === selectedModel?.endpoint_id);
  const knownOutputLimit = selectedModel?.current_version.settings.max_output_tokens;
  const answerMaximum = knownOutputLimit === null || knownOutputLimit === undefined
    ? 262_144
    : Math.min(262_144, knownOutputLimit);
  const selectedKnowledge = draft.knowledge_base_ids.map((id) => ({
    id,
    value: props.knowledgeBases.find((knowledgeBase) => knowledgeBase.id === id),
  }));
  const availableKnowledge = props.knowledgeBases.filter(
    (knowledgeBase) => !draft.knowledge_base_ids.includes(knowledgeBase.id),
  );

  function submit(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault();
    if (messages.length > 0 || !dirty) return;
    if (props.kind === "create") {
      props.onSubmit({ key: agentKey, configuration: normalized });
    } else {
      props.onSubmit(normalized);
    }
  }

  function setAnswerMode(mode: AgentAnswerMode): void {
    setDraft((current) => mode === "single_pass"
      ? { ...current, answer_mode: mode, evidence_access: "wiki_only", max_tool_calls: 0 }
      : { ...current, answer_mode: mode, max_tool_calls: current.max_tool_calls > 0 ? current.max_tool_calls : 8 });
  }

  function addKnowledgeBase(): void {
    if (knowledgeToAdd === "" || draft.knowledge_base_ids.includes(knowledgeToAdd) || draft.knowledge_base_ids.length >= 32) return;
    setDraft((current) => ({
      ...current,
      knowledge_base_ids: [...current.knowledge_base_ids, knowledgeToAdd],
    }));
    setKnowledgeToAdd("");
  }

  function moveKnowledgeBase(index: number, direction: -1 | 1): void {
    const destination = index + direction;
    if (destination < 0 || destination >= draft.knowledge_base_ids.length) return;
    setDraft((current) => {
      const next = [...current.knowledge_base_ids];
      const sourceID = next[index];
      const destinationID = next[destination];
      if (sourceID === undefined || destinationID === undefined) return current;
      next[index] = destinationID;
      next[destination] = sourceID;
      return { ...current, knowledge_base_ids: next };
    });
  }

  return (
    <form className="agent-configuration-form" onSubmit={submit}>
      <fieldset aria-label="Agent configuration controls" className="agent-form-fields" disabled={props.busy}>
      <section aria-labelledby="agent-identity-title" className="folio-panel agent-form-section" id="identity">
        <div className="section-heading">
          <div><p className="eyebrow">Identity</p><h2 id="agent-identity-title">Stable selector and trusted persona</h2></div>
          <span>Required</span>
        </div>
        <div className="form-grid">
          <label>
            Agent key
            <input
              disabled={props.kind === "replace"}
              maxLength={64}
              onChange={(event) => setAgentKey(event.currentTarget.value)}
              pattern="[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?"
              required
              value={props.kind === "replace" ? props.agent.key : agentKey}
            />
            <span className="field-note">Immutable selector: agent:{props.kind === "replace" ? props.agent.key : agentKey || "your-key"}</span>
          </label>
          <label>
            Display name
            <input maxLength={255} onChange={(event) => { const value = event.currentTarget.value; setDraft((current) => ({ ...current, display_name: value })); }} required value={draft.display_name} />
          </label>
          <label>
            Response language
            <input maxLength={35} onChange={(event) => { const value = event.currentTarget.value; setDraft((current) => ({ ...current, response_language: value })); }} placeholder="en" required value={draft.response_language} />
          </label>
          <label className="full-field">
            Description
            <textarea maxLength={2_000} onChange={(event) => { const value = event.currentTarget.value; setDraft((current) => ({ ...current, description: value })); }} rows={3} value={draft.description} />
          </label>
          <label className="full-field">
            Identity instructions
            <textarea maxLength={16_000} onChange={(event) => { const value = event.currentTarget.value; setDraft((current) => ({ ...current, identity_instructions: value })); }} required rows={7} value={draft.identity_instructions} />
            <span className="field-note">Trusted persona instructions are placed below fixed platform policy and above caller text.</span>
          </label>
        </div>
      </section>

      <section aria-labelledby="agent-model-title" className="folio-panel agent-form-section" id="model">
        <div className="section-heading"><div><p className="eyebrow">Model</p><h2 id="agent-model-title">Answer execution</h2></div></div>
        <div className="form-grid">
          <label>
            Model profile
            <Select
              onChange={(modelProfileID) => setDraft((current) => ({ ...current, model_profile_id: modelProfileID }))}
              options={[
                { label: "Select a model profile", value: "" },
                ...props.models.map((model) => ({
                  label: `${providerName(props.providers, model.endpoint_id)} · ${model.model_id} · ${model.availability} · settings v${model.current_version.version_number}`,
                  value: model.id,
                })),
              ]}
              required
              value={draft.model_profile_id}
            />
          </label>
          <label>
            Reasoning effort
            <Select onChange={(value) => setDraft((current) => ({ ...current, reasoning_effort: parseReasoning(value) }))} options={reasoningEfforts.map((value) => ({ label: humanize(value), value }))} value={draft.reasoning_effort} />
          </label>
          <label>
            Answer mode
            <Select onChange={(value) => setAnswerMode(parseAnswerMode(value))} options={answerModes.map((value) => ({ label: humanize(value), value }))} value={draft.answer_mode} />
          </label>
          <div className="agent-model-receipt">
            <span>Selected profile receipt</span>
            <strong>{selectedModel ? `settings v${selectedModel.current_version.version_number}` : "Awaiting selection"}</strong>
            <small>{selectedModel ? `${selectedProvider?.display_name ?? selectedModel.endpoint_id} · ${selectedModel.model_id} · ${selectedModel.availability}` : "Choose a model to evaluate its known limits."}</small>
          </div>
        </div>
      </section>

      <section aria-labelledby="agent-knowledge-title" className="folio-panel agent-form-section" id="knowledge">
        <div className="section-heading">
          <div><p className="eyebrow">Knowledge</p><h2 id="agent-knowledge-title">Ordered evidence scope</h2></div>
          <span>{draft.knowledge_base_ids.length} / 32</span>
        </div>
        <div className="knowledge-adder">
          <label>
            Knowledge base
            <Select
              disabled={draft.knowledge_base_ids.length >= 32}
              onChange={setKnowledgeToAdd}
              options={[{ label: "Select knowledge", value: "" }, ...availableKnowledge.map((knowledgeBase) => ({ label: `${knowledgeBase.name} · ${knowledgeBase.access}`, value: knowledgeBase.id }))]}
              value={knowledgeToAdd}
            />
          </label>
          <button className="button secondary" disabled={knowledgeToAdd === "" || draft.knowledge_base_ids.length >= 32} onClick={addKnowledgeBase} type="button">Add knowledge base</button>
        </div>
        <ol className="ordered-membership">
          {selectedKnowledge.map(({ id, value }, index) => {
            const name = value?.name ?? id;
            return (
              <li key={id}>
                <span className="membership-position">{index + 1}</span>
                <span className="membership-copy">
                  <strong>{name}</strong>
                  <small>{value ? `${value.access} · ${value.lifecycle} · ${value.published_wiki_id ? "published" : "unpublished"}` : "Knowledge base is no longer listed"}</small>
                </span>
                <span className="membership-actions">
                  <button aria-label={`Move ${name} up`} disabled={index === 0} onClick={() => moveKnowledgeBase(index, -1)} type="button">↑</button>
                  <button aria-label={`Move ${name} down`} disabled={index === selectedKnowledge.length - 1} onClick={() => moveKnowledgeBase(index, 1)} type="button">↓</button>
                  <button aria-label={`Remove ${name}`} onClick={() => setDraft((current) => ({ ...current, knowledge_base_ids: current.knowledge_base_ids.filter((knowledgeBaseID) => knowledgeBaseID !== id) }))} type="button">Remove</button>
                </span>
              </li>
            );
          })}
        </ol>
        {selectedKnowledge.length === 0 ? <p className="notice">Add at least one knowledge base. Order is captured in every immutable Agent version.</p> : null}
      </section>

      <section aria-labelledby="agent-guardrails-title" className="folio-panel agent-form-section" id="guardrails">
        <div className="section-heading"><div><p className="eyebrow">Guardrails</p><h2 id="agent-guardrails-title">Restrictive answer policy</h2></div></div>
        <div className="form-grid">
          <label>
            Evidence access
            <Select
              onChange={(value) => setDraft((current) => ({ ...current, evidence_access: parseEvidenceAccess(value) }))}
              options={evidenceAccess.map((value) => ({ label: humanize(value), value, disabled: value === "wiki_and_source" && draft.answer_mode !== "tool_calling" }))}
              value={draft.evidence_access}
            />
          </label>
          <label>
            Maximum tool calls
            <input disabled={draft.answer_mode === "single_pass"} max={64} min={draft.answer_mode === "tool_calling" ? 1 : 0} onChange={(event) => { const value = finiteNumber(event.currentTarget.valueAsNumber); setDraft((current) => ({ ...current, max_tool_calls: value })); }} type="number" value={draft.max_tool_calls} />
          </label>
          <label>
            Maximum answer tokens
            <input max={answerMaximum} min={1} onChange={(event) => { const value = finiteNumber(event.currentTarget.valueAsNumber); setDraft((current) => ({ ...current, max_answer_tokens: value })); }} type="number" value={draft.max_answer_tokens} />
            <span className="field-note">Current known ceiling: {knownOutputLimit ?? "model limit unavailable"}.</span>
          </label>
          <label className="full-field">
            Behavioral instructions
            <textarea maxLength={16_000} onChange={(event) => { const value = event.currentTarget.value; setDraft((current) => ({ ...current, behavioral_instructions: value })); }} rows={6} value={draft.behavioral_instructions} />
            <span className="field-note">Model guidance only. This is not deterministic moderation, PII detection, or DLP.</span>
          </label>
          <label className="full-field">
            Refusal response
            <textarea maxLength={4_000} onChange={(event) => { const value = event.currentTarget.value; setDraft((current) => ({ ...current, refusal_markdown: value })); }} required rows={4} value={draft.refusal_markdown} />
          </label>
        </div>
        <aside aria-labelledby="platform-safeguards-title" className="platform-safeguards">
          <h3 id="platform-safeguards-title">Always-on platform safeguards</h3>
          <p>These protections are code-owned and cannot be disabled by an Agent.</p>
          <ul>
            <li>Answer only from the captured authorized corpus.</li>
            <li>Treat caller, source, and tool text as untrusted data.</li>
            <li>Expose bounded read-only evidence tools—never shell, write, network, process, Git, credential, or delegation tools.</li>
            <li>Verify citations against the current run ledger and suppress unsupported material.</li>
            <li>Never expose hidden reasoning or credentials.</li>
          </ul>
        </aside>
      </section>

      {messages.length > 0 ? (
        <div aria-live="polite" className="notice error agent-form-errors">
          <strong>Complete the configuration before saving.</strong>
          <ul>{messages.map((message) => <li key={message}>{message}</li>)}</ul>
        </div>
      ) : null}
      <div className="sticky-actions">
        <span>{props.kind === "replace" && !dirty ? "No configuration changes" : "Saving creates one immutable Agent version."}</span>
        <button className="button primary" disabled={props.busy || messages.length > 0 || !dirty} type="submit">
          {props.busy ? "Saving Agent…" : props.kind === "create" ? "Create Agent" : "Save new version"}
        </button>
      </div>
      </fieldset>
    </form>
  );
}

function newAgentConfiguration(): AgentConfigurationInput {
  return {
    answer_mode: "tool_calling",
    behavioral_instructions: "",
    description: "",
    display_name: "",
    evidence_access: "wiki_only",
    identity_instructions: "",
    knowledge_base_ids: [],
    max_answer_tokens: 2_048,
    max_tool_calls: 8,
    model_profile_id: "",
    reasoning_effort: "none",
    refusal_markdown: "I cannot answer that from the available evidence.",
    response_language: "en",
  };
}

function normalizeConfiguration(value: AgentConfigurationInput): AgentConfigurationInput {
  return {
    ...value,
    behavioral_instructions: normalizeText(value.behavioral_instructions ?? ""),
    description: normalizeText(value.description ?? ""),
    display_name: normalizeText(value.display_name),
    identity_instructions: normalizeText(value.identity_instructions),
    knowledge_base_ids: [...value.knowledge_base_ids],
    refusal_markdown: normalizeText(value.refusal_markdown),
    response_language: normalizeText(value.response_language),
  };
}

function normalizeText(value: string): string {
  return value.replaceAll("\r\n", "\n").replaceAll("\r", "\n").trim();
}

function validationMessages(
  key: string,
  configuration: AgentConfigurationInput,
  models: ModelProfile[],
): string[] {
  const messages: string[] = [];
  if (!agentKeyPattern.test(key)) messages.push("Agent key must use 1–64 lowercase letters, digits, or interior hyphens.");
  if (configuration.display_name === "") messages.push("Display name is required.");
  if (!languagePattern.test(configuration.response_language)) messages.push("Response language must be a normalized language tag such as en or en-US.");
  if (configuration.identity_instructions === "") messages.push("Identity instructions are required.");
  if (configuration.model_profile_id === "") messages.push("Select a model profile.");
  if (configuration.refusal_markdown === "") messages.push("Refusal response is required.");
  if (configuration.knowledge_base_ids.length < 1 || configuration.knowledge_base_ids.length > 32) messages.push("Select 1–32 knowledge bases.");
  if (new Set(configuration.knowledge_base_ids).size !== configuration.knowledge_base_ids.length) messages.push("Knowledge bases must be unique.");
  if (configuration.answer_mode === "single_pass" && configuration.max_tool_calls !== 0) messages.push("Single-pass mode cannot allow tool calls.");
  if (configuration.answer_mode === "tool_calling" && (configuration.max_tool_calls < 1 || configuration.max_tool_calls > 64)) messages.push("Tool-calling mode requires 1–64 tool calls.");
  if (configuration.evidence_access === "wiki_and_source" && configuration.answer_mode !== "tool_calling") messages.push("Source evidence requires tool-calling mode.");
  if (configuration.max_answer_tokens < 1 || configuration.max_answer_tokens > 262_144) messages.push("Maximum answer tokens must be between 1 and 262,144.");
  const model = models.find((candidate) => candidate.id === configuration.model_profile_id);
  const modelLimit = model?.current_version.settings.max_output_tokens;
  if (modelLimit !== null && modelLimit !== undefined && configuration.max_answer_tokens > modelLimit) messages.push(`Maximum answer tokens exceed the selected model limit of ${modelLimit}.`);
  return messages;
}

function sameConfiguration(left: AgentConfigurationInput, right: AgentConfigurationInput): boolean {
  return JSON.stringify(left) === JSON.stringify(normalizeConfiguration(right));
}

function parseReasoning(value: string): AgentReasoningEffort {
  return reasoningEfforts.find((effort) => effort === value) ?? "none";
}

function parseAnswerMode(value: string): AgentAnswerMode {
  return answerModes.find((mode) => mode === value) ?? "tool_calling";
}

function parseEvidenceAccess(value: string): AgentEvidenceAccess {
  return evidenceAccess.find((access) => access === value) ?? "wiki_only";
}

function finiteNumber(value: number): number {
  return Number.isFinite(value) ? value : 0;
}

function humanize(value: string): string {
  return value.replaceAll("_", " ");
}

function providerName(providers: ProviderEndpoint[], endpointID: string): string {
  return providers.find((provider) => provider.id === endpointID)?.display_name ?? endpointID;
}

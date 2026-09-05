import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";

import {
  listModelAssignments,
  listModelProfiles,
  listProviderEndpoints,
  putModelAssignment,
  safeErrorMessage,
  type AnswerMode,
  type ModelAssignment,
  type ModelProfile,
  type ModelRole,
  type ProviderEndpoint,
  type ReasoningEffort,
} from "../../api/client";
import { queryKeys } from "../../api/queries";
import { Select } from "../../app/Select";
import { ErrorNotice } from "../../app/StatusBadge";
import { useCsrfToken } from "../../app/auth";
import { useIdempotencyKey } from "../../app/useIdempotencyKey";

const roles: ReadonlyArray<{ description: string; label: string; value: ModelRole }> = [
  { description: "Plans documentation structure and tool work.", label: "Documentation planner", value: "documentation_planner" },
  { description: "Writes documentation through approved tools.", label: "Documentation writer", value: "documentation_writer" },
];
const efforts: ReasoningEffort[] = ["none", "minimal", "low", "medium", "high", "max"];

export function ModelAssignmentsPanel({ knowledgeBaseId, mutable }: { knowledgeBaseId: string; mutable: boolean }): ReactNode {
  const models = useQuery({ queryKey: queryKeys.models, queryFn: () => listModelProfiles() });
  const endpoints = useQuery({ queryKey: queryKeys.providers, queryFn: listProviderEndpoints });
  const assignments = useQuery({
    queryKey: [...queryKeys.modelAssignments, knowledgeBaseId],
    queryFn: () => listModelAssignments(knowledgeBaseId),
  });
  const queryError = models.error ?? endpoints.error ?? assignments.error;

  return (
    <section aria-labelledby="assignments-title" className="folio-panel assignment-panel">
      <p className="eyebrow">Deployment routing</p>
      <h2 id="assignments-title">Model assignments</h2>
      <p className="notice deployment-warning"><strong>Deployment warning:</strong> restricted or private source content will be sent to the selected provider endpoint when later documentation workflows use this assignment. Agent answer routing is configured on each Agent. This warning describes routing behavior; it does not claim sources are configured.</p>
      <ErrorNotice message={queryError ? safeErrorMessage(queryError) : null} />
      {!mutable ? <p>Return this knowledge base to active before changing model assignments.</p> : null}
      {models.data?.length === 0 ? <p>No eligible profiles exist. <Link to="/providers">Configure a provider and model first.</Link></p> : null}
      {models.data && endpoints.data && assignments.data ? (
        <div className="assignment-grid">
          {roles.map((role) => (
            <RoleAssignment
              description={role.description}
              endpointById={new Map(endpoints.data.map((endpoint) => [endpoint.id, endpoint]))}
              existing={assignments.data.find((assignment) => assignment.role === role.value)}
              key={`${role.value}-${assignments.data.find((assignment) => assignment.role === role.value)?.version ?? 0}`}
              knowledgeBaseId={knowledgeBaseId}
              label={role.label}
              models={models.data}
              mutable={mutable}
              role={role.value}
            />
          ))}
        </div>
      ) : null}
    </section>
  );
}

function RoleAssignment({
  description,
  endpointById,
  existing,
  knowledgeBaseId,
  label,
  models,
  mutable,
  role,
}: {
  description: string;
  endpointById: Map<string, ProviderEndpoint>;
  existing: ModelAssignment | undefined;
  knowledgeBaseId: string;
  label: string;
  models: ModelProfile[];
  mutable: boolean;
  role: ModelRole;
}): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const [profileId, setProfileId] = useState(existing?.model_profile_id ?? models[0]?.id ?? "");
  const [effort, setEffort] = useState<ReasoningEffort>(existing?.reasoning_effort ?? "none");
	const [mode, setMode] = useState<AnswerMode>(existing?.answer_mode ?? "tool_calling");
  const actionKey = useIdempotencyKey();
  const selected = models.find((profile) => profile.id === profileId);
  const selectedEndpoint = selected ? endpointById.get(selected.endpoint_id) : undefined;
  const assessment = selected === undefined
    ? { eligible: false, reasons: ["Select a model profile."] }
		: assessEligibility(selected, selectedEndpoint, effort, mode);
  const assign = useMutation({
    mutationFn: (input: { body: Parameters<typeof putModelAssignment>[0]["body"]; idempotencyKey: string }) => putModelAssignment({
      ...input,
      csrfToken,
      knowledgeBaseId,
      role,
    }),
    onSuccess: async () => {
      actionKey.reset();
      await queryClient.invalidateQueries({ queryKey: queryKeys.modelAssignments });
    },
  });

  return (
    <article className="assignment-card">
      <h3>{label}</h3>
      <p>{description}</p>
      <label>
        Model profile
        <Select
          disabled={!mutable}
          onChange={(next) => { setProfileId(next); actionKey.reset(); }}
          options={[{ label: "Select a model", value: "" }, ...models.map((profile) => ({
            label: `${endpointById.get(profile.endpoint_id)?.display_name ?? profile.endpoint_id} · ${profile.model_id} · ${profile.availability} · settings v${profile.current_version.version_number}`,
            value: profile.id,
          }))]}
          value={profileId}
        />
      </label>
      <div className="agent-model-receipt">
        <span>Selected endpoint and profile</span>
        <strong>{selected ? selectedEndpoint?.display_name ?? selected.endpoint_id : "Awaiting selection"}</strong>
        <small>{selected ? `${selected.model_id} · ${selected.availability} · settings v${selected.current_version.version_number}` : "Choose the exact provider route for this assignment."}</small>
      </div>
      <label>
        Reasoning effort
        <Select
          disabled={!mutable}
          onChange={(next) => { setEffort(parseEffort(next)); actionKey.reset(); }}
          options={efforts.map((item) => ({ label: item, value: item }))}
          value={effort}
        />
      </label>
      <label>
        Execution mode
        <Select
          disabled={!mutable}
          onChange={(next) => { setMode(next === "single_pass" ? "single_pass" : "tool_calling"); actionKey.reset(); }}
          options={[{ label: "Tool calling", value: "tool_calling" }, { label: "Single pass", value: "single_pass" }]}
          value={mode}
        />
      </label>
      <div aria-live="polite" className={`eligibility ${assessment.eligible ? "is-eligible" : "is-ineligible"}`}>
        <strong>{assessment.eligible ? "Eligible" : "Not eligible"}</strong>
        {assessment.reasons.length > 0 ? <ul>{assessment.reasons.map((reason) => <li key={reason}>{reason}</li>)}</ul> : <p>Known limits, transport, endpoint configuration, and role capabilities match.</p>}
      </div>
      <button
        className="button primary"
        disabled={!mutable || !assessment.eligible || assign.isPending}
        onClick={() => {
          if (!selected) return;
          assign.mutate({
            body: {
              answer_mode: mode,
              expected_version: existing?.version ?? null,
              profile_id: selected.id,
              reasoning_effort: effort,
            },
            idempotencyKey: actionKey.current(),
          });
        }}
        type="button"
      >{assign.isPending ? "Assigning…" : existing ? "Update assignment" : "Assign model"}</button>
      <ErrorNotice message={assign.error ? safeErrorMessage(assign.error) : null} />
    </article>
  );
}

export function assessEligibility(
	profile: ModelProfile,
	endpoint: ProviderEndpoint | undefined,
	effort: ReasoningEffort,
	mode: AnswerMode,
): { eligible: boolean; reasons: string[] } {
  const settings = profile.current_version.settings;
  const reasons: string[] = [];
  if (profile.availability === "unavailable") reasons.push("The model is unavailable.");
  if (endpoint === undefined || endpoint.lifecycle !== "active") reasons.push("The provider endpoint is not active.");
  if (endpoint && profile.current_version.configuration_version !== endpoint.configuration_version) reasons.push("The model evidence predates the endpoint configuration.");
  if (settings.transport === "responses") reasons.push("Responses transport is not assignable.");
  if (settings.context_window_tokens === null || settings.max_output_tokens === null) reasons.push("Context and output limits must be known.");
  if (effort !== "none" && settings.reasoning_transport === "none") reasons.push("Reasoning effort is unsupported.");
  if (effort !== "none" && settings.reasoning_transport === "custom" && !customReasoningMaps(settings.reasoning_mapping, effort)) reasons.push(`Custom reasoning does not map ${effort}.`);
	if (settings.supports_tools !== true) reasons.push("Planner and writer roles require confirmed tool support.");
	if (settings.supports_structured_output !== true) reasons.push("Planner and writer roles require confirmed structured output.");
	if (mode !== "tool_calling") reasons.push("Planner and writer roles require tool-calling mode.");
  return { eligible: reasons.length === 0, reasons };
}

function customReasoningMaps(mapping: Record<string, unknown> | null, effort: ReasoningEffort): boolean {
  if (mapping === null || typeof mapping.values !== "object" || mapping.values === null || Array.isArray(mapping.values)) return false;
  return effort in mapping.values;
}

function parseEffort(value: string): ReasoningEffort {
  return efforts.find((effort) => effort === value) ?? "none";
}

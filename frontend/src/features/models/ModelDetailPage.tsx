import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, type ReactNode } from "react";

import {
  editModelProfile,
  getModelProfile,
  getProviderEndpoint,
  safeErrorMessage,
  type ModelProfile,
  type ModelSettingsInput,
} from "../../api/client";
import { queryKeys } from "../../api/queries";
import { ErrorNotice, StatusBadge } from "../../app/StatusBadge";
import { useCsrfToken } from "../../app/auth";
import { useIdempotencyKey } from "../../app/useIdempotencyKey";
import { ModelSettingsForm } from "./ModelSettingsForm";

type Origin = "unknown" | "discovered" | "probed" | "operator";

const settingFields = [
  "model_id",
  "transport",
  "context_window_tokens",
  "max_output_tokens",
  "supports_streaming",
  "supports_tools",
  "supports_structured_output",
  "supports_temperature",
  "reasoning_transport",
  "reasoning_mapping",
  "timeout_seconds",
  "max_retries",
  "max_concurrent_tasks",
  "extra_body",
] as const;

export function ModelDetailPage({ profileId }: { profileId: string }): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const actionKey = useIdempotencyKey();
  const profile = useQuery({
    queryKey: [...queryKeys.models, profileId],
    queryFn: () => getModelProfile(profileId),
  });
  const edit = useMutation({
    mutationFn: (input: { body: { expected_version: number; settings: ModelSettingsInput }; id: string; idempotencyKey: string }) => editModelProfile({ ...input, csrfToken }),
    onSuccess: async () => {
      actionKey.reset();
      await queryClient.invalidateQueries({ queryKey: queryKeys.models });
    },
  });
  const record = profile.data;
  const endpoint = useQuery({
    enabled: record !== undefined,
    queryKey: [...queryKeys.providers, record?.endpoint_id ?? "pending"],
    queryFn: () => getProviderEndpoint(record?.endpoint_id ?? ""),
  });

  useEffect(() => {
    if (record) actionKey.reset();
  }, [actionKey, record?.current_version.id]);

  return (
    <section className="page narrow-page">
      <Link className="back-link" to="/models">← Model register</Link>
      <header className="page-heading">
        <div><p className="eyebrow">model detail</p><h1>{record?.model_id ?? "Loading model…"}</h1></div>
        {record ? <StatusBadge value={record.availability} /> : null}
      </header>
      <ErrorNotice message={profile.error ? safeErrorMessage(profile.error) : null} />
      {record ? (
        <>
          <ModelEvidence profile={record} />
          <section aria-labelledby="model-editor-title" className="folio-panel model-editor">
            <p className="eyebrow">Optimistic edit · current profile version {record.version}</p>
            <h2 id="model-editor-title">Operator settings</h2>
            <p>Changed values are recorded as operator-originated. Unknown fields remain explicit until an edit or probe supplies evidence.</p>
            {endpoint.data?.lifecycle === "active" ? (
              <ModelSettingsForm
                initial={requestSettings(record)}
                key={record.current_version.id}
                onSettingsChange={actionKey.reset}
                onSubmit={(settings) => edit.mutate({
                  body: { expected_version: record.version, settings },
                  id: record.id,
                  idempotencyKey: actionKey.current(),
                })}
                pending={edit.isPending}
                submitLabel="Append settings version"
              />
            ) : endpoint.data ? (
              <p className="notice">Reactivate the provider endpoint before editing this model profile.</p>
            ) : endpoint.error ? null : (
              <p aria-live="polite">Checking provider lifecycle…</p>
            )}
            <ErrorNotice message={endpoint.error ? safeErrorMessage(endpoint.error) : edit.error ? safeErrorMessage(edit.error) : null} />
          </section>
        </>
      ) : null}
    </section>
  );
}

export function ModelEvidence({ profile }: { profile: ModelProfile }): ReactNode {
  const settings = profile.current_version.settings;
  return (
    <section aria-labelledby="model-evidence-title" className="folio-panel evidence-panel">
      <div className="section-heading"><h2 id="model-evidence-title">Current metadata</h2><span>settings v{profile.current_version.version_number}</span></div>
      <p>Configuration capture {profile.current_version.configuration_version} · source {profile.current_version.source}</p>
      <dl className="metadata-ledger">
        {settingFields.map((field) => (
          <div key={field}>
            <dt>{field.replaceAll("_", " ")}</dt>
            <dd>{formatValue(field === "model_id" ? profile.model_id : settings[field])}</dd>
            <dd><ProvenanceLabel origin={settings.metadata_origin[field] ?? "unknown"} /></dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

export function ProvenanceLabel({ origin }: { origin: Origin }): ReactNode {
  const copy = {
    discovered: "Discovered evidence",
    operator: "Operator supplied",
    probed: "Probe evidence",
    unknown: "Unknown / unverified",
  } satisfies Record<Origin, string>;
  return <span className={`provenance provenance-${origin}`}><span aria-hidden="true" className="provenance-mark" />{copy[origin]}</span>;
}

function requestSettings(profile: ModelProfile): ModelSettingsInput {
  const settings = profile.current_version.settings;
  return {
    context_window_tokens: settings.context_window_tokens,
    extra_body: settings.extra_body,
    max_output_tokens: settings.max_output_tokens,
    max_retries: settings.max_retries,
    max_concurrent_tasks: settings.max_concurrent_tasks,
    reasoning_mapping: settings.reasoning_mapping,
    reasoning_transport: settings.reasoning_transport,
    supports_streaming: settings.supports_streaming,
    supports_structured_output: settings.supports_structured_output,
    supports_temperature: settings.supports_temperature,
    supports_tools: settings.supports_tools,
    timeout_seconds: settings.timeout_seconds,
    transport: settings.transport,
  };
}

function formatValue(value: unknown): string {
  if (value === null || value === undefined) return "Unknown / not established";
  if (value === true) return "Supported";
  if (value === false) return "Unsupported";
  if (typeof value === "object") return JSON.stringify(value);
  return String(value).replaceAll("_", " ");
}

import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type ReactNode } from "react";

import {
  createModelProfile,
  getProviderEndpoint,
  listCredentials,
  listModelProfiles,
  safeErrorMessage,
  scheduleDiscovery,
  scheduleProbe,
  updateProviderEndpoint,
  type ModelProfile,
  type ModelSettingsInput,
  type ProbeCheck,
  type ProviderEndpoint,
} from "../../api/client";
import { queryKeys } from "../../api/queries";
import { Select } from "../../app/Select";
import { EmptyState, ErrorNotice, StatusBadge } from "../../app/StatusBadge";
import { useCsrfToken } from "../../app/auth";
import { useIdempotencyKey } from "../../app/useIdempotencyKey";
import { defaultModelSettings, ModelSettingsForm } from "../models/ModelSettingsForm";
import { JobEnqueueNotice } from "../jobs/JobEnqueueNotice";

type EndpointAction = {
  endpointId: string;
  expectedVersion: number;
  idempotencyKey: string;
};
type ProbeAction = EndpointAction & {
  acknowledgeCost: true;
  checks: ProbeCheck[];
  profileId: string;
};

const probeChecks: ReadonlyArray<{ label: string; value: ProbeCheck }> = [
  { label: "Chat request", value: "chat" },
  { label: "Streaming", value: "streaming" },
  { label: "Tool calling", value: "tools" },
  { label: "Structured output", value: "structured_output" },
];

export function ProviderDetailPage({ endpointId }: { endpointId: string }): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const endpoint = useQuery({
    queryKey: [...queryKeys.providers, endpointId],
    queryFn: () => getProviderEndpoint(endpointId),
  });
  const credentials = useQuery({ queryKey: queryKeys.credentials, queryFn: listCredentials });
  const profiles = useQuery({
    queryKey: [...queryKeys.models, endpointId],
    queryFn: () => listModelProfiles(endpointId),
  });
  const [error, setError] = useState<string | null>(null);
  const discoveryKey = useIdempotencyKey();
  const lifecycleKey = useIdempotencyKey();

  const discover = useMutation({
    mutationFn: (input: EndpointAction) => scheduleDiscovery({ ...input, csrfToken }),
    onSuccess: () => {
      setError(null);
      discoveryKey.reset();
    },
    onError: (caught: unknown) => setError(safeErrorMessage(caught)),
  });
  const lifecycle = useMutation({
    mutationFn: (input: { body: Parameters<typeof updateProviderEndpoint>[0]["body"]; id: string; idempotencyKey: string }) => updateProviderEndpoint({ ...input, csrfToken }),
    onSuccess: async () => {
      lifecycleKey.reset();
      await queryClient.invalidateQueries({ queryKey: queryKeys.providers });
    },
    onError: (caught: unknown) => setError(safeErrorMessage(caught)),
  });

  const record = endpoint.data;
  const credential = credentials.data?.find((item) => item.id === record?.credential_id);

  useEffect(() => {
    if (record) {
      discoveryKey.reset();
      lifecycleKey.reset();
    }
  }, [discoveryKey, lifecycleKey, record?.version]);

  function changeLifecycle(): void {
    if (!record) return;
    setError(null);
    lifecycle.mutate({
      body: {
        allow_http: record.allow_http,
        allow_private_network: record.allow_private_network,
        base_url: record.base_url,
        chat_completions_path: record.chat_completions_path,
        credential_id: record.credential_id,
        display_name: record.display_name,
        expected_version: record.version,
        headers: record.headers,
        lifecycle: record.lifecycle === "active" ? "archived" : "active",
        models_path: record.models_path,
        responses_path: record.responses_path,
      },
      id: record.id,
      idempotencyKey: lifecycleKey.current(),
    });
  }

  return (
    <section className="page narrow-page">
      <Link className="back-link" to="/providers">← Provider register</Link>
      <header className="page-heading">
        <div>
          <p className="eyebrow">endpoint detail</p>
          <h1>{record?.display_name ?? "Loading endpoint…"}</h1>
        </div>
        {record ? (
          <div className="status-stack">
            <StatusBadge value={record.health} />
            <StatusBadge value={record.lifecycle} />
          </div>
        ) : null}
      </header>
      <ErrorNotice message={endpoint.error ? safeErrorMessage(endpoint.error) : credentials.error ? safeErrorMessage(credentials.error) : error} />
      {record ? (
        <>
          <section aria-labelledby="endpoint-config-title" className="folio-panel detail-ledger">
            <div className="section-heading"><h2 id="endpoint-config-title">Runtime configuration</h2><span>v{record.configuration_version}</span></div>
            <dl>
              <div><dt>Normalized base URL</dt><dd className="endpoint-url">{record.base_url}</dd></div>
              <div><dt>Credential</dt><dd>{credential ? `${credential.label} · ${credential.masked_value} · ${credential.id}` : record.credential_id ?? "No credential"}</dd></div>
              <div><dt>Chat path</dt><dd>{record.chat_completions_path}</dd></div>
              <div><dt>Models path</dt><dd>{record.models_path}</dd></div>
              <div><dt>Responses path</dt><dd>{record.responses_path ?? "Disabled"}</dd></div>
              <div><dt>Non-secret headers</dt><dd>{Object.keys(record.headers).length === 0 ? "None" : Object.keys(record.headers).join(", ")}</dd></div>
              <div><dt>Private network</dt><dd>{record.allow_private_network ? "Explicitly permitted" : "Blocked"}</dd></div>
              <div><dt>Plain HTTP</dt><dd>{record.allow_http ? "Explicitly permitted" : "Blocked"}</dd></div>
              <div><dt>Endpoint health</dt><dd>{record.health.replaceAll("_", " ")}</dd></div>
              <div><dt>Health checked</dt><dd>{record.health_checked_at ? new Date(record.health_checked_at).toLocaleString() : "Not checked"}</dd></div>
              <div><dt>Record version</dt><dd>{record.version}</dd></div>
            </dl>
            <div className="button-row">
              <button className="button primary" disabled={discover.isPending || record.lifecycle !== "active"} onClick={() => { setError(null); discover.mutate({ endpointId: record.id, expectedVersion: record.version, idempotencyKey: discoveryKey.current() }); }} type="button">
                {discover.isPending ? "Enqueueing…" : "Discover models"}
              </button>
              <button className="button secondary" disabled={lifecycle.isPending} onClick={changeLifecycle} type="button">
                {record.lifecycle === "active" ? "Archive endpoint" : "Reactivate endpoint"}
              </button>
            </div>
            {discover.data ? <JobEnqueueNotice jobId={discover.data.job_id} label="Discovery" /> : null}
          </section>
          <section aria-labelledby="endpoint-models-title" className="ledger-section">
            <div className="section-heading"><h2 id="endpoint-models-title">Models</h2><Link to="/models">Open model register</Link></div>
            <div className="record-grid">
              {profiles.data?.map((profile) => <ModelCard key={profile.id} profile={profile} />)}
            </div>
            {profiles.data?.length === 0 ? (
              <>
                <EmptyState>{record.lifecycle === "active" ? "Discovery has not listed a model. You can add a manual profile for a known model ID." : "No model profiles are registered for this endpoint."}</EmptyState>
                {record.lifecycle === "active" ? <ManualModelForm endpointId={record.id} /> : <p className="notice">Reactivate this endpoint before adding a manual model.</p>}
              </>
            ) : null}
            <ErrorNotice message={profiles.error ? safeErrorMessage(profiles.error) : null} />
          </section>
          {profiles.data && profiles.data.length > 0 && record.lifecycle === "active" ? (
            <ProbeForm endpoint={record} profiles={profiles.data} />
          ) : null}
        </>
      ) : null}
    </section>
  );
}

function ModelCard({ profile }: { profile: ModelProfile }): ReactNode {
  return (
    <Link className="record-card" params={{ profileId: profile.id }} to="/models/$profileId">
      <div><p className="eyebrow">Version {profile.version}</p><h3>{profile.model_id}</h3><p>{profile.current_version.settings.transport.replaceAll("_", " ")}</p></div>
      <StatusBadge value={profile.availability} />
    </Link>
  );
}

export function ProbeForm({ endpoint, profiles }: { endpoint: ProviderEndpoint; profiles: ModelProfile[] }): ReactNode {
  const csrfToken = useCsrfToken();
  const [profileId, setProfileId] = useState(profiles[0]?.id ?? "");
  const [checks, setChecks] = useState<ProbeCheck[]>(["chat"]);
  const [acknowledged, setAcknowledged] = useState(false);
  const probeKey = useIdempotencyKey();
  const probe = useMutation({
    mutationFn: (input: ProbeAction) => scheduleProbe({ ...input, csrfToken }),
    onSuccess: () => probeKey.reset(),
  });
  const selectedProfile = profiles.find((profile) => profile.id === profileId);

  useEffect(() => {
    if (selectedProfile) probeKey.reset();
  }, [probeKey, selectedProfile?.version]);

  function toggle(check: ProbeCheck, enabled: boolean): void {
    setChecks((current) => enabled
      ? [...current.filter((item) => item !== check), check]
      : current.filter((item) => item !== check));
    probeKey.reset();
  }

  return (
    <section aria-labelledby="probe-title" className="folio-panel probe-panel">
      <p className="eyebrow">Opt-in network action</p>
      <h2 id="probe-title">Probe model capabilities</h2>
      <p>A probe makes one bounded, potentially billable attempt against the selected model.</p>
      <label>
        Model profile
        <Select
          onChange={(next) => { setProfileId(next); probeKey.reset(); }}
          options={profiles.map((profile) => ({ label: profile.model_id, value: profile.id }))}
          value={profileId}
        />
      </label>
      <fieldset className="choice-group check-grid">
        <legend>Checks to run</legend>
        {probeChecks.map((check) => (
          <label key={check.value}><input checked={checks.includes(check.value)} onChange={(event) => toggle(check.value, event.currentTarget.checked)} type="checkbox" /> {check.label}</label>
        ))}
      </fieldset>
      <label className="cost-acknowledgement"><input checked={acknowledged} onChange={(event) => { setAcknowledged(event.currentTarget.checked); probeKey.reset(); }} type="checkbox" /> I understand this probe sends a request to the endpoint and may incur provider cost.</label>
      <button
        className="button primary"
        disabled={probe.isPending || !acknowledged || checks.length === 0 || selectedProfile === undefined}
        onClick={() => {
          if (!selectedProfile) return;
          probe.mutate({
            acknowledgeCost: true,
            checks,
            endpointId: endpoint.id,
            expectedVersion: selectedProfile.version,
            idempotencyKey: probeKey.current(),
            profileId: selectedProfile.id,
          });
        }}
        type="button"
      >{probe.isPending ? "Enqueueing probe…" : "Enqueue probe"}</button>
      {probe.data ? <JobEnqueueNotice jobId={probe.data.job_id} label="Probe" /> : null}
      <ErrorNotice message={probe.error ? safeErrorMessage(probe.error) : null} />
    </section>
  );
}

function ManualModelForm({ endpointId }: { endpointId: string }): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const [modelId, setModelId] = useState("");
  const [error, setError] = useState<string | null>(null);
  const actionKey = useIdempotencyKey();
  const create = useMutation({
    mutationFn: (input: { modelId: string; settings: ModelSettingsInput; idempotencyKey: string }) => createModelProfile({
      body: { endpoint_id: endpointId, model_id: input.modelId, settings: input.settings },
      csrfToken,
      idempotencyKey: input.idempotencyKey,
    }),
    onSuccess: async () => {
      actionKey.reset();
      setModelId("");
      await queryClient.invalidateQueries({ queryKey: queryKeys.models });
    },
    onError: (caught: unknown) => setError(safeErrorMessage(caught)),
  });

  return (
    <section aria-labelledby="manual-model-title" className="folio-panel manual-model-panel">
      <h2 id="manual-model-title">Add manual model</h2>
      <label className="model-id-field">Model ID<input maxLength={512} onChange={(event) => { setModelId(event.currentTarget.value); actionKey.reset(); }} required value={modelId} /></label>
      <ErrorNotice message={error} />
      <ModelSettingsForm
        initial={defaultModelSettings()}
        onSettingsChange={actionKey.reset}
        onSubmit={(settings) => {
          if (!modelId.trim()) { setError("Enter the provider model ID."); return; }
          setError(null);
          create.mutate({ modelId, settings, idempotencyKey: actionKey.current() });
        }}
        pending={create.isPending}
        submitLabel="Create manual model"
      />
    </section>
  );
}

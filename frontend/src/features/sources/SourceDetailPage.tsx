import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";

import {
  changeSourceLifecycle,
  getSource,
  listDocumentationRuns,
  listCredentials,
  listSourceRevisions,
  listSourceSyncs,
  safeErrorMessage,
  syncSource,
  updateRepositorySource,
  updateWebsiteSource,
  validateSource,
  type Credential,
  type DocumentationRun,
  type RepositoryRefKind,
  type RepositorySource,
  type RepositorySourceUpdateInput,
  type SourceLifecycleInput,
  type SourcePrivacy,
  type SourceRevision,
  type SourceSync,
  type WebsiteSource,
  type WebsiteSourceUpdateInput,
} from "../../api/client";
import { queryKeys } from "../../api/queries";
import { ConfirmationDialog } from "../../app/ConfirmationDialog";
import { Select, type SelectOption } from "../../app/Select";
import { EmptyState, ErrorNotice, StatusBadge } from "../../app/StatusBadge";
import { useCsrfToken } from "../../app/auth";
import { useIdempotencyKey } from "../../app/useIdempotencyKey";
import { JobEnqueueNotice } from "../jobs/JobEnqueueNotice";

type SourceActionInput = {
  expectedVersion: number;
  id: string;
  idempotencyKey: string;
};

type LifecycleActionInput = SourceActionInput & { lifecycle: SourceLifecycleInput };
type SourceUpdateActionInput = {
  body: RepositorySourceUpdateInput;
  id: string;
  idempotencyKey: string;
};
type WebsiteSourceUpdateActionInput = {
  body: WebsiteSourceUpdateInput;
  id: string;
  idempotencyKey: string;
};

type WebsiteAcquisitionMode = WebsiteSource["acquisition_mode"];

const acquisitionLabels: Record<WebsiteAcquisitionMode, string> = {
  builtin_crawl: "Website crawl",
  tinyfish_crawl: "TinyFish crawl",
  direct_json_api: "Direct JSON API",
};

const acquisitionModes = Object.keys(acquisitionLabels) as WebsiteAcquisitionMode[];

const sourceErrorMessages: Record<string, string> = {
  credential_unavailable: "The credential for this source could not be read.",
  invalid_capture: "The configuration changed during the operation. Retry it.",
  invalid_configuration: "The saved configuration is invalid for this operation.",
  failed: "The operation failed.",
  tinyfish_auth: "TinyFish rejected the API key.",
  tinyfish_connection: "The TinyFish service could not be reached.",
  tinyfish_content: "The TinyFish crawl produced no usable content.",
  tinyfish_invalid_response: "The TinyFish response could not be read.",
  tinyfish_limit: "The TinyFish crawl exceeded its configured limits.",
  tinyfish_policy: "The URL is outside the allowed TinyFish policy.",
  tinyfish_rate_limited: "The TinyFish rate limit was exceeded. Try again later.",
  tinyfish_redirect: "TinyFish reported a redirect outside the allowed policy.",
  tinyfish_response_too_large: "The TinyFish response exceeded the size limit.",
  tinyfish_robots: "robots.txt denied the TinyFish crawl.",
  tinyfish_server: "The TinyFish service reported a failure.",
  tinyfish_storage: "The TinyFish snapshot could not be stored.",
  tinyfish_timeout: "The TinyFish request timed out.",
  tinyfish_unprocessable: "TinyFish could not process the request.",
  tinyfish_unspecified: "The TinyFish request failed.",
  tinyfish_validation: "TinyFish rejected the request as invalid.",
  website_content: "The website response was not usable content.",
  website_dns: "The website host could not be resolved.",
  website_http: "The website returned an HTTP error.",
  website_limit: "The crawl exceeded its configured limits.",
  website_redirect: "The website redirected outside the allowed policy.",
  website_robots: "robots.txt denied this crawl.",
  website_ssrf: "The URL points at a forbidden network location.",
  website_storage: "The revision could not be stored.",
  website_tls: "The website certificate could not be validated.",
};

function describeSourceError(sanitized: string): string {
  const code = sanitized.replace(/^source_(?:validation|sync):/, "");
  const message = sourceErrorMessages[code]
    ?? (code.startsWith("git_") ? "The Git remote rejected the operation." : null);
  return message ? `${message} (code: ${sanitized})` : sanitized;
}

function acquisitionLabel(mode: string): string {
  return acquisitionModes.includes(mode as WebsiteAcquisitionMode)
    ? acquisitionLabels[mode as WebsiteAcquisitionMode]
    : mode;
}

export function SourceDetailPage({ sourceId }: { sourceId: string }): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const source = useQuery({
    queryKey: [...queryKeys.sources, sourceId],
    queryFn: () => getSource(sourceId),
  });
  const revisions = useQuery({
    queryKey: [...queryKeys.sources, sourceId, "revisions"],
    queryFn: () => listSourceRevisions(sourceId),
  });
  const syncs = useQuery({
    queryKey: [...queryKeys.sources, sourceId, "syncs"],
    queryFn: () => listSourceSyncs(sourceId),
  });
  const credentials = useQuery({ queryKey: queryKeys.credentials, queryFn: listCredentials });
  const documentationRuns = useQuery({
    queryKey: [...queryKeys.runs, "source-impact", source.data?.knowledge_base_id ?? "pending"],
    queryFn: () => listDocumentationRuns(source.data?.knowledge_base_id),
    enabled: source.data !== undefined,
  });
  const [confirmRemove, setConfirmRemove] = useState(false);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const validationKey = useIdempotencyKey();
  const syncKey = useIdempotencyKey();
  const lifecycleKey = useIdempotencyKey();

  async function settle(): Promise<void> {
    await queryClient.invalidateQueries({ queryKey: [...queryKeys.sources, sourceId] });
  }

  const validate = useMutation({
    mutationFn: (input: SourceActionInput) => validateSource({ ...input, csrfToken }),
    onSuccess: async () => {
      validationKey.reset();
      await settle();
    },
  });
  const sync = useMutation({
    mutationFn: (input: SourceActionInput) => syncSource({ ...input, csrfToken }),
    onSuccess: async () => {
      syncKey.reset();
      await settle();
    },
  });
  const lifecycle = useMutation({
    mutationFn: (input: LifecycleActionInput) => changeSourceLifecycle({ ...input, csrfToken }),
    onSuccess: async () => {
      lifecycleKey.reset();
      await settle();
    },
  });

  const record = source.data;
  const sourceCredentials = credentials.data?.filter((credential) => credential.kind === (record?.kind === "website" ? "website_header" : "repository_https")) ?? [];
  const selectedCredential = sourceCredentials.find((credential) => credential.id === record?.credential_id);
  const currentRevision = currentSourceRevision(revisions.data, record?.current_revision_id);
  const lastSuccessfulSync = latestSuccessfulSync(syncs.data);
  const impactedRuns = documentationRuns.data?.filter((run) =>
    run.sources.some((captured) => captured.source_id === sourceId)
  );
  const queryError = source.error ?? revisions.error ?? syncs.error ?? credentials.error ?? documentationRuns.error;
  const actionError = validate.error ?? sync.error ?? lifecycle.error;

  function changeLifecycle(target: SourceLifecycleInput): void {
    if (!record) return;
    lifecycle.mutate({ expectedVersion: record.version, id: record.id, idempotencyKey: lifecycleKey.current(), lifecycle: target });
  }

  async function remove(): Promise<void> {
    if (!record) return;
    await lifecycle.mutateAsync({ expectedVersion: record.version, id: record.id, idempotencyKey: lifecycleKey.current(), lifecycle: "removed" });
  }

  return (
    <section className="page narrow-page">
      <Link className="back-link" to="/sources">← Source register</Link>
      <header className="page-heading">
        <div>
          <p className="eyebrow">source detail</p>
          <h1 ref={headingRef} tabIndex={-1}>{record?.name ?? "Loading source…"}</h1>
        </div>
        {record ? <div className="status-stack"><StatusBadge value={record.health} /><StatusBadge value={record.lifecycle} /></div> : null}
      </header>
      <ErrorNotice message={queryError ? safeErrorMessage(queryError) : actionError ? safeErrorMessage(actionError) : null} />
      {source.isPending ? <p aria-live="polite" className="notice">Loading source detail…</p> : null}
      {record ? (
        <>
          <section aria-labelledby="source-configuration-title" className="folio-panel detail-ledger">
            <div className="section-heading"><h2 id="source-configuration-title">{record.kind === "repository" ? "Repository record" : "Website record"}</h2><span>configuration {record.configuration_version}</span></div>
            <dl>
              {record.kind === "repository" ? <>
                <div><dt>Remote host</dt><dd>{record.remote_host}</dd></div>
                <div><dt>Repository path</dt><dd>{record.repository_path}</dd></div>
                <div><dt>Selected ref</dt><dd><span className="credential-kind">{record.ref_kind}</span> <code>{record.ref_value}</code></dd></div>
              </> : <>
                <div><dt>Acquisition</dt><dd>{acquisitionLabel(record.acquisition_mode)}</dd></div>
                <div><dt>Website host</dt><dd>{record.root_host}</dd></div>
                <div><dt>Root URL</dt><dd><code>{record.root_url}</code></dd></div>
                <div><dt>Crawl budget</dt><dd>{record.max_pages.toLocaleString()} pages · depth {record.max_depth} · {record.requests_per_second} req/s</dd></div>
              </>}
              <div><dt>Privacy</dt><dd>{record.privacy}</dd></div>
              <div><dt>Latest native version</dt><dd><code>{currentRevision?.native_version ?? "No revision fetched"}</code></dd></div>
              <div><dt>Last successful sync</dt><dd>{lastSuccessfulSync?.completed_at ? formatDate(lastSuccessfulSync.completed_at) : "No successful sync yet"}</dd></div>
              <div><dt>Current health</dt><dd>{record.health}{record.checked_at ? ` · checked ${formatDate(record.checked_at)}` : " · not checked"}</dd></div>
              <div><dt>Polling</dt><dd>{record.poll_interval_seconds === null ? "Disabled" : `Every ${record.poll_interval_seconds} seconds`}</dd></div>
              <div><dt>Credential</dt><dd>{record.privacy === "private" ? selectedCredential ? `${selectedCredential.label} · ${selectedCredential.masked_value}${record.kind === "repository" ? ` · ${record.credential_username ?? "username unavailable"}` : ` · ${record.credential_header ?? "header unavailable"}`}` : "Stored credential metadata unavailable" : "Not required"}</dd></div>
              {record.kind === "repository" ? <>
                <div><dt>Include patterns</dt><dd>{patternText(record.include_patterns)}</dd></div>
                <div><dt>Exclude patterns</dt><dd>{patternText(record.exclude_patterns)}</dd></div>
              </> : <>
                <div><dt>Concurrency</dt><dd>{record.max_concurrency}</dd></div>
                <div><dt>Byte limits</dt><dd>{formatBytes(record.max_page_bytes)} per page · {formatBytes(record.max_total_bytes)} total</dd></div>
                {record.acquisition_mode === "tinyfish_crawl" && record.tinyfish_credential_id ? (
                  <div><dt>TinyFish key</dt><dd>{tinyfishCredentialDescription(record.tinyfish_credential_id, credentials.data)}</dd></div>
                ) : null}
              </>}
              <div><dt>Record version</dt><dd>{record.version}</dd></div>
            </dl>
            {record.sanitized_error ? <p className="notice error" role="alert">{describeSourceError(record.sanitized_error)}</p> : null}
            <div className="button-row source-actions">
              <button
                className="button secondary"
                disabled={validate.isPending || record.lifecycle === "removed"}
                onClick={() => validate.mutate({ expectedVersion: record.version, id: record.id, idempotencyKey: validationKey.current() })}
                type="button"
              >{validate.isPending ? "Enqueueing validation…" : "Validate access"}</button>
              <button
                className="button primary"
                disabled={sync.isPending || record.lifecycle !== "active"}
                onClick={() => sync.mutate({ expectedVersion: record.version, id: record.id, idempotencyKey: syncKey.current() })}
                type="button"
              >{sync.isPending ? "Enqueueing sync…" : "Sync now"}</button>
            </div>
            {record.lifecycle !== "active" && record.lifecycle !== "removed" ? <p className="field-note">Sync becomes available after successful validation activates this source.</p> : null}
            {validate.data ? <JobEnqueueNotice jobId={validate.data.job_id} label="Source validation" /> : null}
            {sync.data ? <JobEnqueueNotice jobId={sync.data.job_id} label="Source sync" /> : null}
          </section>
          {record.lifecycle !== "removed" ? record.kind === "repository"
            ? <SourceConfigurationForm credentials={sourceCredentials} record={record} />
            : <WebsiteSourceConfigurationForm credentials={credentials.data ?? []} record={record} />
          : null}
          <DocumentationImpact
            pending={documentationRuns.isPending}
            runs={impactedRuns}
            sourceId={sourceId}
          />
          <section aria-labelledby="source-lifecycle-title" className="folio-panel danger-zone">
            <p className="eyebrow">Lifecycle</p>
            <h2 id="source-lifecycle-title">Source state</h2>
            {record.lifecycle === "active" || record.lifecycle === "draft" ? (
              <button className="button secondary" disabled={lifecycle.isPending} onClick={() => changeLifecycle("disabled")} type="button">Disable source</button>
            ) : null}
            {record.lifecycle === "disabled" ? (
              <button className="button secondary" disabled={lifecycle.isPending} onClick={() => changeLifecycle("active")} type="button">Reactivate source</button>
            ) : null}
            {record.lifecycle !== "removed" ? (
              <button className="button danger" disabled={lifecycle.isPending} onClick={() => { lifecycleKey.reset(); setConfirmRemove(true); }} type="button">Remove source</button>
            ) : <p>This source no longer polls or syncs. Existing published wiki revisions remain intact.</p>}
          </section>
          <ConfirmationDialog
            confirmLabel="Remove source"
            error={lifecycle.error ? safeErrorMessage(lifecycle.error) : null}
            expectedText={record.name}
            fallbackFocusRef={headingRef}
            onClose={() => setConfirmRemove(false)}
            onConfirm={remove}
            open={confirmRemove}
            title={`Remove “${record.name}”?`}
          >
            <p>Removing this source is permanent. Existing published wiki versions keep their immutable revision; later runs omit this source.</p>
          </ConfirmationDialog>
          <SourceHistory revisions={revisions.data} revisionsPending={revisions.isPending} syncs={syncs.data} syncsPending={syncs.isPending} />
        </>
      ) : null}
    </section>
  );
}

function DocumentationImpact({
  pending,
  runs,
  sourceId,
}: {
  pending: boolean;
  runs: DocumentationRun[] | undefined;
  sourceId: string;
}): ReactNode {
  return (
    <section aria-labelledby="documentation-impact-title" className="folio-panel">
      <p className="eyebrow">Documentation impact</p>
      <h2 id="documentation-impact-title">Recent captured runs</h2>
      <p className="field-note">The most recent 50 runs for this knowledge base are checked for this source.</p>
      {pending ? <p aria-live="polite" className="notice">Loading documentation impact…</p> : null}
      {runs && runs.length > 0 ? (
        <div className="table-wrap">
          <table>
            <thead><tr><th>Run</th><th>Status</th><th>Captured revision</th><th>Direct page seeds</th><th>Updated</th></tr></thead>
            <tbody>
              {runs.map((run) => {
                const captured = run.sources.find((item) => item.source_id === sourceId);
                const seededPages = run.pages.filter((page) =>
                  page.source_seed_paths.some((seed) => seed.source_id === sourceId)
                ).length;
                return (
                  <tr key={run.id}>
                    <th scope="row"><Link params={{ runId: run.id }} to="/runs/$runId">Open run</Link><small>{run.id}</small></th>
                    <td><StatusBadge value={run.status} />{run.published_wiki_version_id ? <small>Published wiki version</small> : null}</td>
                    <td><code>{captured?.source_revision_id ?? "Unavailable"}</code>{captured ? <small>{captured.commit}</small> : null}</td>
                    <td>{seededPages}</td>
                    <td><time dateTime={run.updated_at}>{formatDate(run.updated_at)}</time></td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : pending ? null : <EmptyState>No recent documentation run captured this source.</EmptyState>}
    </section>
  );
}

function SourceConfigurationForm({ credentials, record }: { credentials: Credential[]; record: RepositorySource }): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const [name, setName] = useState(record.name);
  const [privacy, setPrivacy] = useState<SourcePrivacy>(record.privacy);
  const [remoteUrl, setRemoteUrl] = useState(record.remote_url);
  const [refKind, setRefKind] = useState<RepositoryRefKind>(record.ref_kind);
  const [refValue, setRefValue] = useState(record.ref_value);
  const [credentialUsername, setCredentialUsername] = useState(record.credential_username ?? "");
  const [credentialId, setCredentialId] = useState(record.credential_id ?? "");
  const [includePatterns, setIncludePatterns] = useState(record.include_patterns.join("\n"));
  const [excludePatterns, setExcludePatterns] = useState(record.exclude_patterns.join("\n"));
  const [polling, setPolling] = useState(record.poll_interval_seconds !== null);
  const [pollInterval, setPollInterval] = useState(String(record.poll_interval_seconds ?? 3600));
  const [error, setError] = useState<string | null>(null);
  const updateKey = useIdempotencyKey();

  useEffect(() => {
    setName(record.name);
    setPrivacy(record.privacy);
    setRemoteUrl(record.remote_url);
    setRefKind(record.ref_kind);
    setRefValue(record.ref_value);
    setCredentialUsername(record.credential_username ?? "");
    setCredentialId(record.credential_id ?? "");
    setIncludePatterns(record.include_patterns.join("\n"));
    setExcludePatterns(record.exclude_patterns.join("\n"));
    setPolling(record.poll_interval_seconds !== null);
    setPollInterval(String(record.poll_interval_seconds ?? 3600));
  }, [record]);

  const update = useMutation({
    mutationFn: (input: SourceUpdateActionInput) => updateRepositorySource({ ...input, csrfToken }),
    onSuccess: async () => {
      updateKey.reset();
      setError(null);
      await queryClient.invalidateQueries({ queryKey: [...queryKeys.sources, record.id] });
    },
    onError: (caught: unknown) => setError(safeErrorMessage(caught)),
  });

  function resetUpdate(): void {
    updateKey.reset();
    setError(null);
  }

  function selectPrivacy(value: SourcePrivacy): void {
    setPrivacy(value);
    if (value === "public") {
      setCredentialId("");
      setCredentialUsername("");
    }
    resetUpdate();
  }

  function save(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault();
    setError(null);
    if (privacy === "private" && (!credentialId || !credentialUsername.trim())) {
      setError("Private repositories require a credential and username.");
      return;
    }
    const interval = polling ? Number.parseInt(pollInterval, 10) : null;
    if (polling && (!Number.isInteger(interval) || interval === null || interval < 60 || interval > 604800)) {
      setError("Polling must be between 60 and 604800 seconds.");
      return;
    }
    update.mutate({
      body: {
        credential_id: privacy === "private" ? credentialId : null,
        credential_username: privacy === "private" ? credentialUsername.trim() : null,
        exclude_patterns: parsePatterns(excludePatterns),
        expected_version: record.version,
        include_patterns: parsePatterns(includePatterns),
        name,
        poll_interval_seconds: interval,
        privacy,
        ref_kind: refKind,
        ref_value: refValue,
        remote_url: remoteUrl,
      },
      id: record.id,
      idempotencyKey: updateKey.current(),
    });
  }

  return (
    <details className="folio-panel disclosure source-editor">
      <summary>Edit source configuration</summary>
      <form className="form-grid" onSubmit={save}>
        <label>Source name<input maxLength={255} onChange={(event) => { setName(event.currentTarget.value); resetUpdate(); }} required value={name} /></label>
        <label>
          Privacy
          <Select
            onChange={(next) => selectPrivacy(next === "private" ? "private" : "public")}
            options={[{ label: "Public", value: "public" }, { label: "Private", value: "private" }]}
            value={privacy}
          />
        </label>
        <label className="full-field">HTTPS repository URL<input onChange={(event) => { setRemoteUrl(event.currentTarget.value); resetUpdate(); }} required type="url" value={remoteUrl} /></label>
        <label>
          Reference type
          <Select
            onChange={(next) => { setRefKind(next === "commit" ? "commit" : "branch"); resetUpdate(); }}
            options={[{ label: "Branch", value: "branch" }, { label: "Immutable commit", value: "commit" }]}
            value={refKind}
          />
        </label>
        <label>
          {refKind === "branch" ? "Branch name" : "Full commit hash"}
          <input
            maxLength={refKind === "branch" ? 512 : 64}
            onChange={(event) => { setRefValue(event.currentTarget.value); resetUpdate(); }}
            pattern={refKind === "commit" ? "(?:[0-9A-Fa-f]{40}|[0-9A-Fa-f]{64})" : undefined}
            required
            value={refValue}
          />
        </label>
        {privacy === "private" ? (
          <>
            <label>HTTPS username<input autoComplete="username" maxLength={255} onChange={(event) => { setCredentialUsername(event.currentTarget.value); resetUpdate(); }} required value={credentialUsername} /></label>
            <label>
              Stored repository credential
              <Select
                onChange={(next) => { setCredentialId(next); resetUpdate(); }}
                options={[
                  { label: "Select a credential", value: "" },
                  ...(credentialId && !credentials.some((credential) => credential.id === credentialId) ? [{ label: "Current credential metadata unavailable", value: credentialId }] : []),
                  ...credentials.map(credentialOption),
                ]}
                required
                value={credentialId}
              />
            </label>
          </>
        ) : null}
        <label>Include patterns<textarea onChange={(event) => { setIncludePatterns(event.currentTarget.value); resetUpdate(); }} rows={5} value={includePatterns} /></label>
        <label>Exclude patterns<textarea onChange={(event) => { setExcludePatterns(event.currentTarget.value); resetUpdate(); }} rows={5} value={excludePatterns} /></label>
        <fieldset className="full-field choice-group polling-controls">
          <legend>Polling</legend>
          <label><input checked={polling} onChange={(event) => { setPolling(event.currentTarget.checked); resetUpdate(); }} type="checkbox" /> Poll this repository</label>
          <label>Interval in seconds<input disabled={!polling} max={604800} min={60} onChange={(event) => { setPollInterval(event.currentTarget.value); resetUpdate(); }} required={polling} type="number" value={pollInterval} /></label>
        </fieldset>
        <p className="full-field field-note">Changes to URL, ref, credential, or filters return an active source to draft. Validate access after saving to reactivate it.</p>
        <ErrorNotice message={error} />
        <button className="button primary" disabled={update.isPending} type="submit">{update.isPending ? "Saving changes…" : "Save source changes"}</button>
      </form>
    </details>
  );
}

function WebsiteSourceConfigurationForm({ credentials, record }: { credentials: Credential[]; record: WebsiteSource }): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const tinyfishCredentials = credentials.filter((credential) => credential.kind === "tinyfish_api_key");
  const websiteHeaderCredentials = credentials.filter((credential) => credential.kind === "website_header");
  const [name, setName] = useState(record.name);
  const [privacy, setPrivacy] = useState<SourcePrivacy>(record.privacy);
  const [rootUrl, setRootUrl] = useState(record.root_url);
  const [credentialHeader, setCredentialHeader] = useState(record.credential_header ?? "Authorization");
  const [credentialPrefix, setCredentialPrefix] = useState(record.credential_prefix ?? "Bearer ");
  const [credentialId, setCredentialId] = useState(record.credential_id ?? "");
  const [maxConcurrency, setMaxConcurrency] = useState(String(record.max_concurrency));
  const [requestsPerSecond, setRequestsPerSecond] = useState(String(record.requests_per_second));
  const [maxPages, setMaxPages] = useState(String(record.max_pages));
  const [maxPageBytes, setMaxPageBytes] = useState(String(record.max_page_bytes));
  const [maxTotalBytes, setMaxTotalBytes] = useState(String(record.max_total_bytes));
  const [maxDepth, setMaxDepth] = useState(String(record.max_depth));
  const [acquisition, setAcquisition] = useState<WebsiteAcquisitionMode>(record.acquisition_mode);
  const [tinyfishCredentialId, setTinyfishCredentialId] = useState(record.tinyfish_credential_id ?? "");
  const [polling, setPolling] = useState(record.poll_interval_seconds !== null);
  const [pollInterval, setPollInterval] = useState(String(record.poll_interval_seconds ?? 3600));
  const [error, setError] = useState<string | null>(null);
  const updateKey = useIdempotencyKey();

  const isTinyfish = acquisition === "tinyfish_crawl";
  const isDirectJson = acquisition === "direct_json_api";
  const isBuiltin = acquisition === "builtin_crawl";

  useEffect(() => {
    setName(record.name);
    setPrivacy(record.privacy);
    setRootUrl(record.root_url);
    setCredentialHeader(record.credential_header ?? "Authorization");
    setCredentialPrefix(record.credential_prefix ?? "Bearer ");
    setCredentialId(record.credential_id ?? "");
    setMaxConcurrency(String(record.max_concurrency));
    setRequestsPerSecond(String(record.requests_per_second));
    setMaxPages(String(record.max_pages));
    setMaxPageBytes(String(record.max_page_bytes));
    setMaxTotalBytes(String(record.max_total_bytes));
    setMaxDepth(String(record.max_depth));
    setAcquisition(record.acquisition_mode);
    setTinyfishCredentialId(record.tinyfish_credential_id ?? "");
    setPolling(record.poll_interval_seconds !== null);
    setPollInterval(String(record.poll_interval_seconds ?? 3600));
  }, [record]);

  const update = useMutation({
    mutationFn: (input: WebsiteSourceUpdateActionInput) => updateWebsiteSource({ ...input, csrfToken }),
    onSuccess: async () => {
      updateKey.reset();
      setError(null);
      await queryClient.invalidateQueries({ queryKey: [...queryKeys.sources, record.id] });
    },
    onError: (caught: unknown) => setError(safeErrorMessage(caught)),
  });

  function resetUpdate(): void {
    updateKey.reset();
    setError(null);
  }

  function selectPrivacy(value: SourcePrivacy): void {
    setPrivacy(value);
    if (value === "public") setCredentialId("");
    resetUpdate();
  }

  function selectAcquisition(value: WebsiteAcquisitionMode): void {
    setAcquisition(value);
    if (value === "tinyfish_crawl") {
      setPrivacy("public");
      setCredentialId("");
      setTinyfishCredentialId(tinyfishCredentials[0]?.id ?? "");
    } else {
      setTinyfishCredentialId("");
    }
    resetUpdate();
  }

  function save(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault();
    setError(null);
    if (isTinyfish && !tinyfishCredentialId) {
      setError("TinyFish crawl requires a TinyFish API key credential.");
      return;
    }
    if (privacy === "private" && (!credentialId || !credentialHeader.trim())) {
      setError("Private websites require a stored credential and header name.");
      return;
    }
    const interval = polling ? Number.parseInt(pollInterval, 10) : null;
    if (polling && (!Number.isInteger(interval) || interval === null || interval < 60 || interval > 604800)) {
      setError("Polling must be between 60 and 604800 seconds.");
      return;
    }
    update.mutate({
      body: {
        acquisition_mode: acquisition,
        credential_header: isTinyfish || privacy === "public" ? null : credentialHeader.trim(),
        credential_id: isTinyfish || privacy === "public" ? null : credentialId,
        credential_prefix: isTinyfish || privacy === "public" ? null : credentialPrefix,
        expected_version: record.version,
        max_concurrency: Number.parseInt(maxConcurrency, 10),
        max_depth: isDirectJson ? 0 : Number.parseInt(maxDepth, 10),
        max_page_bytes: Number.parseInt(maxPageBytes, 10),
        max_pages: isDirectJson ? 1 : Number.parseInt(maxPages, 10),
        max_total_bytes: Number.parseInt(maxTotalBytes, 10),
        name,
        poll_interval_seconds: interval,
        privacy,
        requests_per_second: Number.parseInt(requestsPerSecond, 10),
        root_url: rootUrl,
        tinyfish_credential_id: isTinyfish ? tinyfishCredentialId : null,
      },
      id: record.id,
      idempotencyKey: updateKey.current(),
    });
  }

  return (
    <details className="folio-panel disclosure source-editor">
      <summary>Edit website configuration</summary>
      <form className="form-grid" onSubmit={save}>
        <label>Source name<input maxLength={255} onChange={(event) => { setName(event.currentTarget.value); resetUpdate(); }} required value={name} /></label>
        <label>
          Privacy
          <Select
            disabled={isTinyfish}
            onChange={(next) => selectPrivacy(next === "private" ? "private" : "public")}
            options={isTinyfish ? [{ label: "Public", value: "public" }] : [{ label: "Public", value: "public" }, { label: "Private", value: "private" }]}
            title={isTinyfish ? "TinyFish crawl is public-only." : undefined}
            value={privacy}
          />
          {isTinyfish ? <small className="field-note">TinyFish crawl is public-only.</small> : null}
        </label>
        <label>
          Acquisition method
          <Select
            aria-label="Acquisition method"
            onChange={(next) => selectAcquisition(next as WebsiteAcquisitionMode)}
            options={acquisitionModes.map((mode) => ({ label: acquisitionLabels[mode], value: mode }))}
            value={acquisition}
          />
        </label>
        <label className="full-field">HTTPS website root URL<input onChange={(event) => { setRootUrl(event.currentTarget.value); resetUpdate(); }} required type="url" value={rootUrl} /></label>
        {privacy === "private" && !isTinyfish ? <>
          <label>Header name<input maxLength={127} onChange={(event) => { setCredentialHeader(event.currentTarget.value); resetUpdate(); }} required value={credentialHeader} /></label>
          <label>Value prefix<input maxLength={128} onChange={(event) => { setCredentialPrefix(event.currentTarget.value); resetUpdate(); }} value={credentialPrefix} /></label>
          <label>
            Stored website credential
            <Select
              onChange={(next) => { setCredentialId(next); resetUpdate(); }}
              options={[
                { label: "Select a credential", value: "" },
                ...(credentialId && !websiteHeaderCredentials.some((credential) => credential.id === credentialId) ? [{ label: "Current credential metadata unavailable", value: credentialId }] : []),
                ...websiteHeaderCredentials.map(credentialOption),
              ]}
              required
              value={credentialId}
            />
          </label>
        </> : null}
        {isTinyfish ? (
          <label>
            TinyFish key
            <Select
              onChange={(next) => { setTinyfishCredentialId(next); resetUpdate(); }}
              options={[
                { label: "Select a TinyFish API key", value: "" },
                ...(tinyfishCredentialId && !tinyfishCredentials.some((credential) => credential.id === tinyfishCredentialId) ? [{ label: "Current TinyFish key metadata unavailable", value: tinyfishCredentialId }] : []),
                ...tinyfishCredentials.map(credentialOption),
              ]}
              required
              value={tinyfishCredentialId}
            />
          </label>
        ) : null}
        {isBuiltin ? <>
          <label>Concurrent requests<input max={16} min={1} onChange={(event) => { setMaxConcurrency(event.currentTarget.value); resetUpdate(); }} required type="number" value={maxConcurrency} /></label>
          <label>Requests per second<input max={100} min={1} onChange={(event) => { setRequestsPerSecond(event.currentTarget.value); resetUpdate(); }} required type="number" value={requestsPerSecond} /></label>
          <label>Maximum pages<input max={10000} min={1} onChange={(event) => { setMaxPages(event.currentTarget.value); resetUpdate(); }} required type="number" value={maxPages} /></label>
          <label>Maximum depth<input max={10} min={0} onChange={(event) => { setMaxDepth(event.currentTarget.value); resetUpdate(); }} required type="number" value={maxDepth} /></label>
          <label>Bytes per page<input max={10485760} min={1024} onChange={(event) => { setMaxPageBytes(event.currentTarget.value); resetUpdate(); }} required type="number" value={maxPageBytes} /></label>
          <label>Total crawl bytes<input max={1073741824} min={1024} onChange={(event) => { setMaxTotalBytes(event.currentTarget.value); resetUpdate(); }} required type="number" value={maxTotalBytes} /></label>
        </> : isDirectJson ? <p className="full-field field-note">Direct JSON API fetches only the root URL once. Crawl limits are fixed at one page and depth zero.</p> : null}
        <fieldset className="full-field choice-group polling-controls">
          <legend>Polling</legend>
          <label><input checked={polling} onChange={(event) => { setPolling(event.currentTarget.checked); resetUpdate(); }} type="checkbox" /> Poll this website</label>
          <label>Interval in seconds<input disabled={!polling} max={604800} min={60} onChange={(event) => { setPollInterval(event.currentTarget.value); resetUpdate(); }} required={polling} type="number" value={pollInterval} /></label>
        </fieldset>
        <p className="full-field field-note">Changes to the root, credential, crawl limits, or acquisition method return an active source to draft. Validate access after saving.</p>
        <ErrorNotice message={error} />
        <button className="button primary" disabled={update.isPending} type="submit">{update.isPending ? "Saving changes…" : "Save website changes"}</button>
      </form>
    </details>
  );
}

function SourceHistory({
  revisions,
  revisionsPending,
  syncs,
  syncsPending,
}: {
  revisions: SourceRevision[] | undefined;
  revisionsPending: boolean;
  syncs: SourceSync[] | undefined;
  syncsPending: boolean;
}): ReactNode {
  const orderedRevisions = newestFirst(revisions);
  const orderedSyncs = newestFirst(syncs);
  const websitePages = orderedRevisions.flatMap((revision) =>
    revision.website_pages.map((page) => ({ page, revision })),
  );
  return (
    <section aria-labelledby="source-history-title" className="ledger-section">
      <div className="section-heading"><h2 id="source-history-title">Revision and operation history</h2><span>{orderedRevisions.length} revisions</span></div>
      <h3 className="history-heading">Immutable revisions</h3>
      {revisionsPending ? <p aria-live="polite" className="notice">Loading revision history…</p> : null}
      {orderedRevisions.length > 0 ? (
        <div className="table-wrap">
          <table>
            <thead><tr><th>Native version</th><th>Captured</th><th>Files</th><th>Fingerprint</th></tr></thead>
            <tbody>
              {orderedRevisions.map((revision) => (
                <tr key={revision.id}>
                  <th scope="row"><code>{revision.native_version}</code><small>{revision.observed_ref_kind} · {revision.observed_ref}</small></th>
                  <td>{formatDate(revision.created_at)}</td>
                  <td>{revision.file_count.toLocaleString()} · {formatBytes(revision.byte_count)}</td>
                  <td><code>{revision.fingerprint}</code></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : revisionsPending ? null : <EmptyState>No immutable revisions have been fetched.</EmptyState>}
      {websitePages.length > 0 ? (
        <>
          <h3 className="history-heading">Website page evidence</h3>
          <div className="table-wrap">
            <table>
              <thead><tr><th>Canonical page</th><th>Revision</th><th>Freshness</th><th>Evidence URI</th></tr></thead>
              <tbody>
                {websitePages.map(({ page, revision }) => (
                  <tr key={`${revision.id}:${page.canonical_url}`}>
                    <th scope="row"><a href={page.canonical_url} rel="noreferrer" target="_blank">{page.canonical_url}</a><small><code>{page.content_path}</code></small></th>
                    <td><code>{revision.native_version}</code></td>
                    <td><StatusBadge value={page.freshness} />{page.reused_from_revision_id ? <small>from {page.reused_from_revision_id}</small> : null}</td>
                    <td><code>{page.evidence_uri}</code></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      ) : null}
      <h3 className="history-heading">Validation and sync operations</h3>
      {syncsPending ? <p aria-live="polite" className="notice">Loading operation history…</p> : null}
      {orderedSyncs.length > 0 ? (
        <div className="table-wrap">
          <table>
            <thead><tr><th>Operation</th><th>Status</th><th>Requested</th><th>Resolved version</th><th>Captured acquisition</th><th>Job</th></tr></thead>
            <tbody>
              {orderedSyncs.map((item) => (
                <tr key={item.id}>
                  <th scope="row">{item.kind}</th>
                  <td><StatusBadge value={item.status} />{item.sanitized_error ? <small>{describeSourceError(item.sanitized_error)}</small> : null}</td>
                  <td>{formatDate(item.created_at)}</td>
                  <td><code>{item.resolved_native_version ?? "Pending"}</code></td>
                  <td>
                    {"captured_acquisition_mode" in item ? (
                      <>
                        {acquisitionLabel(item.captured_acquisition_mode)}
                        {item.captured_tinyfish_credential_version !== null ? <small>TinyFish key version {item.captured_tinyfish_credential_version}</small> : null}
                      </>
                    ) : null}
                  </td>
                  <td><Link params={{ id: item.job_id }} to="/jobs/$id">Open job</Link></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : syncsPending ? null : <EmptyState>No validation or sync operations have been requested.</EmptyState>}
    </section>
  );
}

function currentSourceRevision(revisions: SourceRevision[] | undefined, currentId: string | null | undefined): SourceRevision | undefined {
  if (!revisions || !currentId) return undefined;
  return revisions.find((revision) => revision.id === currentId);
}

function latestSuccessfulSync(syncs: SourceSync[] | undefined): SourceSync | undefined {
  return newestFirst(syncs).find((item) => item.kind === "sync" && item.status === "succeeded");
}

function newestFirst<T extends { created_at: string }>(values: T[] | undefined): T[] {
  return [...(values ?? [])].sort((left, right) => right.created_at.localeCompare(left.created_at));
}

function patternText(patterns: string[]): ReactNode {
  return patterns.length === 0 ? "All paths" : patterns.map((pattern) => <code className="pattern-token" key={pattern}>{pattern}</code>);
}

function tinyfishCredentialDescription(credentialId: string, credentials: Credential[] | undefined): ReactNode {
  const credential = credentials?.find((item) => item.id === credentialId);
  if (!credential || credential.kind !== "tinyfish_api_key") return "Stored TinyFish key metadata unavailable";
  return `${credential.label} · ${credential.masked_value} · version ${credential.secret_version}`;
}

function formatDate(value: string): string {
  return new Date(value).toLocaleString();
}

function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`;
}

function parsePatterns(value: string): string[] {
  return value.split("\n").map((pattern) => pattern.trim()).filter(Boolean);
}

function credentialOption(credential: Credential): SelectOption {
  return { label: `${credential.label} · ${credential.masked_value}`, value: credential.id };
}

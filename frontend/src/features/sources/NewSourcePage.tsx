import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type FormEvent, type ReactNode } from "react";

import {
  createCredential,
  createRepositorySource,
  createWebsiteSource,
  listCredentials,
  listKnowledgeBases,
  safeErrorMessage,
  type Credential,
  type RepositoryRefKind,
  type RepositorySourceInput,
  type SourceCreated,
  type SourcePrivacy,
  type WebsiteSourceInput,
} from "../../api/client";
import { queryKeys } from "../../api/queries";
import { Select, type SelectOption } from "../../app/Select";
import { ErrorNotice, StatusBadge } from "../../app/StatusBadge";
import { useCsrfToken } from "../../app/auth";
import { useIdempotencyKey } from "../../app/useIdempotencyKey";
import { JobEnqueueNotice } from "../jobs/JobEnqueueNotice";

type CredentialMode = "existing" | "new";
type SourceKind = "repository" | "website";
type AcquisitionMode = NonNullable<WebsiteSourceInput["acquisition_mode"]>;

const acquisitionOptions: Array<{ description: string; label: string; value: AcquisitionMode }> = [
  { value: "builtin_crawl", label: "Website crawl", description: "Honor robots.txt and follow bounded same-site links." },
  { value: "tinyfish_crawl", label: "TinyFish crawl", description: "Render JavaScript-heavy public sites with a write-only TinyFish API key." },
  { value: "direct_json_api", label: "Direct JSON API", description: "Fetch one exact JSON endpoint without following links." },
];
type SourceMutationInput =
  | { body: RepositorySourceInput & { knowledge_base_id: string }; idempotencyKey: string; kind: "repository" }
  | { body: WebsiteSourceInput & { knowledge_base_id: string }; idempotencyKey: string; kind: "website" };

class SourceFormError extends Error {}

export function NewSourcePage(): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const knowledgeBases = useQuery({ queryKey: queryKeys.knowledgeBases, queryFn: listKnowledgeBases });
  const credentials = useQuery({ queryKey: queryKeys.credentials, queryFn: listCredentials });
  const [sourceKind, setSourceKind] = useState<SourceKind>("repository");
  const sourceCredentials = credentials.data?.filter((credential) => credential.kind === (sourceKind === "repository" ? "repository_https" : "website_header")) ?? [];
  const tinyfishCredentials = credentials.data?.filter((credential) => credential.kind === "tinyfish_api_key") ?? [];
  const [knowledgeBaseId, setKnowledgeBaseId] = useState("");
  const [name, setName] = useState("");
  const [privacy, setPrivacy] = useState<SourcePrivacy>("public");
  const [remoteUrl, setRemoteUrl] = useState("");
  const [refKind, setRefKind] = useState<RepositoryRefKind>("branch");
  const [refValue, setRefValue] = useState("main");
  const [includePatterns, setIncludePatterns] = useState("");
  const [excludePatterns, setExcludePatterns] = useState("");
  const [polling, setPolling] = useState(true);
  const [pollInterval, setPollInterval] = useState("3600");
  const [credentialMode, setCredentialMode] = useState<CredentialMode>("existing");
  const [credentialId, setCredentialId] = useState("");
  const [credentialUsername, setCredentialUsername] = useState("");
  const [credentialLabel, setCredentialLabel] = useState("");
  const [credentialSecret, setCredentialSecret] = useState("");
  const [credentialHeader, setCredentialHeader] = useState("Authorization");
  const [credentialPrefix, setCredentialPrefix] = useState("Bearer ");
  const [maxConcurrency, setMaxConcurrency] = useState("4");
  const [requestsPerSecond, setRequestsPerSecond] = useState("4");
  const [maxPages, setMaxPages] = useState("500");
  const [maxPageBytes, setMaxPageBytes] = useState("2097152");
  const [maxTotalBytes, setMaxTotalBytes] = useState("104857600");
  const [maxDepth, setMaxDepth] = useState("3");
  const [acquisition, setAcquisition] = useState<AcquisitionMode>("builtin_crawl");
  const [tinyfishMode, setTinyfishMode] = useState<CredentialMode>("existing");
  const [tinyfishCredentialId, setTinyfishCredentialId] = useState("");
  const [tinyfishLabel, setTinyfishLabel] = useState("");
  const [tinyfishSecret, setTinyfishSecret] = useState("");
  const [created, setCreated] = useState<SourceCreated | null>(null);
  const [error, setError] = useState<string | null>(null);
  const sourceKey = useIdempotencyKey();
  const credentialKey = useIdempotencyKey();
  const tinyfishKey = useIdempotencyKey();

  const isTinyfish = sourceKind === "website" && acquisition === "tinyfish_crawl";
  const isDirectJson = sourceKind === "website" && acquisition === "direct_json_api";
  const isBuiltin = sourceKind === "repository" || acquisition === "builtin_crawl";

  const create = useMutation({
    mutationFn: (input: SourceMutationInput) => input.kind === "repository"
      ? createRepositorySource({ body: input.body, csrfToken, idempotencyKey: input.idempotencyKey })
      : createWebsiteSource({ body: input.body, csrfToken, idempotencyKey: input.idempotencyKey }),
    onSuccess: async (result) => {
      sourceKey.reset();
      setCreated(result);
      await queryClient.invalidateQueries({ queryKey: queryKeys.sources });
    },
  });

  function resetSourceAction(): void {
    sourceKey.reset();
    setError(null);
  }

  function selectPrivacy(value: SourcePrivacy): void {
    setPrivacy(value);
    if (value === "public") {
      setCredentialId("");
      setCredentialUsername("");
      setCredentialLabel("");
      setCredentialSecret("");
    }
    resetSourceAction();
  }

  function selectAcquisition(value: AcquisitionMode): void {
    setAcquisition(value);
    if (value === "tinyfish_crawl") {
      setPrivacy("public");
      setCredentialId("");
      setCredentialUsername("");
      setCredentialLabel("");
      setCredentialSecret("");
      setCredentialHeader("Authorization");
      setCredentialPrefix("Bearer ");
    } else {
      setTinyfishCredentialId("");
      setTinyfishMode("existing");
      setTinyfishLabel("");
      setTinyfishSecret("");
    }
    resetSourceAction();
  }

  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    setError(null);
    let selectedCredentialId = privacy === "private" && credentialMode === "existing" ? credentialId : null;
    let selectedTinyfishId = isTinyfish && tinyfishMode === "existing" ? tinyfishCredentialId : null;
    try {
      const selectedKnowledgeBase = knowledgeBases.data?.find((knowledgeBase) => knowledgeBase.id === knowledgeBaseId);
      if (privacy === "private" && selectedKnowledgeBase?.access !== "restricted") {
        throw new SourceFormError("Private sources require a restricted knowledge base.");
      }
      if (isTinyfish) {
        if (tinyfishMode === "new") {
          const credential = await createCredential({
            csrfToken,
            idempotencyKey: tinyfishKey.current(),
            kind: "tinyfish_api_key",
            label: tinyfishLabel,
            secret: tinyfishSecret,
          });
          selectedTinyfishId = credential.id;
          setTinyfishSecret("");
          setTinyfishCredentialId(credential.id);
          setTinyfishMode("existing");
          tinyfishKey.reset();
          await queryClient.invalidateQueries({ queryKey: queryKeys.credentials });
        }
        if (!selectedTinyfishId) {
          throw new SourceFormError("TinyFish crawl requires a TinyFish API key credential.");
        }
      }
      if (privacy === "private" && credentialMode === "new") {
        const credential = await createCredential({
          csrfToken,
          idempotencyKey: credentialKey.current(),
          kind: sourceKind === "repository" ? "repository_https" : "website_header",
          label: credentialLabel,
          secret: credentialSecret,
        });
        selectedCredentialId = credential.id;
        setCredentialSecret("");
        setCredentialId(credential.id);
        setCredentialMode("existing");
        credentialKey.reset();
        await queryClient.invalidateQueries({ queryKey: queryKeys.credentials });
      }
      if (privacy === "private" && (!selectedCredentialId || (sourceKind === "repository" && !credentialUsername.trim()))) {
        throw new SourceFormError(sourceKind === "repository" ? "Private repositories require a credential and username." : "Private websites require a credential header.");
      }
      const interval = polling ? Number.parseInt(pollInterval, 10) : null;
      if (polling && (!Number.isInteger(interval) || interval === null || interval < 60 || interval > 604800)) {
        throw new SourceFormError("Polling must be between 60 and 604800 seconds.");
      }
      if (sourceKind === "repository") {
        await create.mutateAsync({
          body: {
          credential_id: privacy === "private" ? selectedCredentialId : null,
          credential_username: privacy === "private" ? credentialUsername.trim() : null,
          exclude_patterns: parsePatterns(excludePatterns),
          include_patterns: parsePatterns(includePatterns),
          knowledge_base_id: knowledgeBaseId,
          name,
          poll_interval_seconds: interval,
          privacy,
          ref_kind: refKind,
          ref_value: refValue,
          remote_url: remoteUrl,
          },
          idempotencyKey: sourceKey.current(),
          kind: "repository",
        });
      } else {
        await create.mutateAsync({
          body: {
            acquisition_mode: acquisition,
            credential_header: isTinyfish || privacy === "public" ? null : credentialHeader.trim(),
            credential_id: isTinyfish || privacy === "public" ? null : selectedCredentialId,
            credential_prefix: isTinyfish || privacy === "public" ? null : credentialPrefix,
            knowledge_base_id: knowledgeBaseId,
            max_concurrency: Number.parseInt(maxConcurrency, 10),
            max_depth: isDirectJson ? 0 : Number.parseInt(maxDepth, 10),
            max_page_bytes: Number.parseInt(maxPageBytes, 10),
            max_pages: isDirectJson ? 1 : Number.parseInt(maxPages, 10),
            max_total_bytes: Number.parseInt(maxTotalBytes, 10),
            name,
            poll_interval_seconds: interval,
            privacy,
            requests_per_second: Number.parseInt(requestsPerSecond, 10),
            root_url: remoteUrl,
            tinyfish_credential_id: isTinyfish ? selectedTinyfishId : null,
          },
          idempotencyKey: sourceKey.current(),
          kind: "website",
        });
      }
    } catch (caught: unknown) {
      setError(caught instanceof SourceFormError ? caught.message : safeErrorMessage(caught));
    }
  }

  if (created !== null) {
    return (
      <section className="page narrow-page">
        <Link className="back-link" to="/sources">← Source register</Link>
        <header className="page-heading">
          <div><p className="eyebrow">source saved</p><h1>{created.source.name}</h1></div>
          <StatusBadge value={created.source.lifecycle} />
        </header>
        <section aria-live="polite" className="folio-panel setup-complete">
          <h2>Access validation enqueued</h2>
          <p>The source record is saved as a draft. Successful validation activates it without exposing its credential.</p>
          <JobEnqueueNotice jobId={created.validation.job_id} label="Source validation" />
          <Link className="button primary" params={{ sourceId: created.source.id }} to="/sources/$sourceId">Open source detail</Link>
        </section>
      </section>
    );
  }

  return (
    <section className="page narrow-page">
      <Link className="back-link" to="/sources">← Source register</Link>
      <header className="page-heading">
        <div><p className="eyebrow">source setup</p><h1>Add a source</h1></div>
        <p className="lede">Connect an HTTPS repository or website. Credentials are written once to the vault and never become source metadata.</p>
      </header>
      <form className="folio-panel form-grid setup-wizard" onSubmit={(event) => void submit(event)}>
        <fieldset className="full-field choice-group">
          <legend>Source type</legend>
          <label><input checked={sourceKind === "repository"} name="source-kind" onChange={() => { setSourceKind("repository"); setRemoteUrl(""); setCredentialId(""); resetSourceAction(); }} type="radio" /> Git repository</label>
          <label><input checked={sourceKind === "website"} name="source-kind" onChange={() => { setSourceKind("website"); setRemoteUrl(""); setCredentialId(""); resetSourceAction(); }} type="radio" /> Website</label>
        </fieldset>
        {sourceKind === "website" ? (
          <fieldset className="full-field choice-group">
            <legend>Acquisition method</legend>
            {acquisitionOptions.map((option) => (
              <label key={option.value}>
                <input checked={acquisition === option.value} name="acquisition-mode" onChange={() => selectAcquisition(option.value)} type="radio" /> {option.label}
                <span className="field-note">{option.description}</span>
              </label>
            ))}
            {isTinyfish ? <p className="field-note">TinyFish crawl is public-only. The key is stored write-only in the credential vault.</p> : null}
            {isDirectJson ? <p className="field-note">The root URL must be the exact JSON endpoint, for example <code>https://fill.papermc.io/v3/projects</code>.</p> : null}
          </fieldset>
        ) : null}
        <label>
          Knowledge base
          <Select
            onChange={(next) => { setKnowledgeBaseId(next); resetSourceAction(); }}
            options={[
              { label: "Select a knowledge base", value: "" },
              ...(knowledgeBases.data ?? []).map((knowledgeBase) => ({ label: `${knowledgeBase.name} · ${knowledgeBase.access}`, value: knowledgeBase.id })),
            ]}
            required
            value={knowledgeBaseId}
          />
        </label>
        <label>
          Source name
          <input maxLength={255} onChange={(event) => { setName(event.currentTarget.value); resetSourceAction(); }} required value={name} />
        </label>
        {sourceKind === "website" && isTinyfish ? null : (
        <fieldset className="full-field choice-group">
          <legend>Source privacy</legend>
          <label><input checked={privacy === "public"} name="privacy" onChange={() => selectPrivacy("public")} type="radio" /> Public {sourceKind}</label>
          <label><input checked={privacy === "private"} name="privacy" onChange={() => selectPrivacy("private")} type="radio" /> Private {sourceKind}</label>
          {privacy === "private" ? <p className="field-note">Private sources require a restricted knowledge base.</p> : null}
        </fieldset>
        )}
        <label className="full-field">
          {sourceKind === "repository" ? "HTTPS repository URL" : "HTTPS website root URL"}
          <input onChange={(event) => { setRemoteUrl(event.currentTarget.value); resetSourceAction(); }} placeholder={sourceKind === "repository" ? "https://github.com/acme/product.git" : "https://docs.acme.example/"} required type="url" value={remoteUrl} />
        </label>
        {sourceKind === "repository" ? <><label>
          Reference type
          <Select
            onChange={(next) => { setRefKind(next === "commit" ? "commit" : "branch"); resetSourceAction(); }}
            options={[{ label: "Branch", value: "branch" }, { label: "Immutable commit", value: "commit" }]}
            value={refKind}
          />
        </label>
        <label>
          {refKind === "branch" ? "Branch name" : "Full commit hash"}
          <input
            maxLength={refKind === "branch" ? 512 : 64}
            onChange={(event) => { setRefValue(event.currentTarget.value); resetSourceAction(); }}
            pattern={refKind === "commit" ? "(?:[0-9A-Fa-f]{40}|[0-9A-Fa-f]{64})" : undefined}
            required
            title={refKind === "commit" ? "Enter a full 40 or 64 character commit hash." : undefined}
            value={refValue}
          />
        </label></> : null}
        {privacy === "private" ? (
          <>
            <fieldset className="full-field choice-group">
              <legend>{sourceKind === "repository" ? "Repository credential" : "Website header credential"}</legend>
              <label><input checked={credentialMode === "existing"} name="credential-mode" onChange={() => { setCredentialMode("existing"); setCredentialSecret(""); resetSourceAction(); }} type="radio" /> Existing credential</label>
              <label><input checked={credentialMode === "new"} name="credential-mode" onChange={() => { setCredentialMode("new"); resetSourceAction(); }} type="radio" /> Add a write-only credential</label>
            </fieldset>
            {sourceKind === "repository" ? <label>
              HTTPS username
              <input autoComplete="username" maxLength={255} onChange={(event) => { setCredentialUsername(event.currentTarget.value); resetSourceAction(); }} required value={credentialUsername} />
            </label> : <>
              <label>
                Header name
                <input maxLength={127} onChange={(event) => { setCredentialHeader(event.currentTarget.value); resetSourceAction(); }} required value={credentialHeader} />
              </label>
              <label>
                Value prefix
                <input maxLength={128} onChange={(event) => { setCredentialPrefix(event.currentTarget.value); resetSourceAction(); }} placeholder="Bearer " value={credentialPrefix} />
              </label>
            </>}
            {credentialMode === "existing" ? (
              <label>
                Stored credential
                <Select
                  onChange={(next) => { setCredentialId(next); resetSourceAction(); }}
                  options={[
                    { label: `Select a ${sourceKind === "repository" ? "repository" : "website header"} credential`, value: "" },
                    ...sourceCredentials.map(credentialOption),
                  ]}
                  required
                  value={credentialId}
                />
              </label>
            ) : (
              <>
                <label>
                  Credential label
                  <input maxLength={255} onChange={(event) => { setCredentialLabel(event.currentTarget.value); credentialKey.reset(); }} required value={credentialLabel} />
                </label>
                <label>
                  {sourceKind === "repository" ? "Token or password" : "Header value"}
                  <input autoComplete="new-password" onChange={(event) => { setCredentialSecret(event.currentTarget.value); credentialKey.reset(); }} required type="password" value={credentialSecret} />
                </label>
              </>
            )}
          </>
        ) : null}
        {sourceKind === "repository" ? <><label>
          Include patterns
          <textarea aria-describedby="source-pattern-help" onChange={(event) => { setIncludePatterns(event.currentTarget.value); resetSourceAction(); }} placeholder="src/**&#10;docs/**" rows={5} value={includePatterns} />
        </label>
        <label>
          Exclude patterns
          <textarea aria-describedby="source-pattern-help" onChange={(event) => { setExcludePatterns(event.currentTarget.value); resetSourceAction(); }} placeholder="vendor/**&#10;**/*.min.js" rows={5} value={excludePatterns} />
        </label>
        <p className="full-field field-note" id="source-pattern-help">One pattern per line. A repository’s <code>.openwikiignore</code> is applied in addition to these filters.</p></> : <>
          {isBuiltin ? <>
            <label>Concurrent requests<input max={16} min={1} onChange={(event) => { setMaxConcurrency(event.currentTarget.value); resetSourceAction(); }} required type="number" value={maxConcurrency} /></label>
            <label>Requests per second<input max={100} min={1} onChange={(event) => { setRequestsPerSecond(event.currentTarget.value); resetSourceAction(); }} required type="number" value={requestsPerSecond} /></label>
            <label>Maximum pages<input max={10000} min={1} onChange={(event) => { setMaxPages(event.currentTarget.value); resetSourceAction(); }} required type="number" value={maxPages} /></label>
            <label>Maximum crawl depth<input max={10} min={0} onChange={(event) => { setMaxDepth(event.currentTarget.value); resetSourceAction(); }} required type="number" value={maxDepth} /></label>
            <label>Bytes per page<input max={10485760} min={1024} onChange={(event) => { setMaxPageBytes(event.currentTarget.value); resetSourceAction(); }} required type="number" value={maxPageBytes} /></label>
            <label>Total crawl bytes<input max={1073741824} min={1024} onChange={(event) => { setMaxTotalBytes(event.currentTarget.value); resetSourceAction(); }} required type="number" value={maxTotalBytes} /></label>
            <p className="full-field field-note">The crawler honors robots.txt, sitemaps, canonical URLs, and same-origin links within these limits.</p>
          </> : isDirectJson ? <p className="full-field field-note">Direct JSON API fetches only the root URL once. Crawl limits are fixed at one page and depth zero.</p> : null}
          {isTinyfish ? (
            <>
              <fieldset className="full-field choice-group">
                <legend>TinyFish key</legend>
                <label><input checked={tinyfishMode === "existing"} name="tinyfish-mode" onChange={() => { setTinyfishMode("existing"); resetSourceAction(); }} type="radio" /> Existing TinyFish credential</label>
                <label><input checked={tinyfishMode === "new"} name="tinyfish-mode" onChange={() => { setTinyfishMode("new"); resetSourceAction(); }} type="radio" /> Add a write-only TinyFish key</label>
              </fieldset>
              {tinyfishMode === "existing" ? (
                <label>
                  Stored TinyFish credential
                  <Select
                    onChange={(next) => { setTinyfishCredentialId(next); resetSourceAction(); }}
                    options={[{ label: "Select a TinyFish API key", value: "" }, ...tinyfishCredentials.map(credentialOption)]}
                    required
                    value={tinyfishCredentialId}
                  />
                </label>
              ) : (
                <>
                  <label>
                    Credential label
                    <input maxLength={255} onChange={(event) => { setTinyfishLabel(event.currentTarget.value); tinyfishKey.reset(); }} required value={tinyfishLabel} />
                  </label>
                  <label>
                    TinyFish API key
                    <input autoComplete="new-password" onChange={(event) => { setTinyfishSecret(event.currentTarget.value); tinyfishKey.reset(); }} required type="password" value={tinyfishSecret} />
                  </label>
                </>
              )}
            </>
          ) : null}
        </>}
        <fieldset className="full-field choice-group polling-controls">
          <legend>Polling</legend>
          <label><input checked={polling} onChange={(event) => { setPolling(event.currentTarget.checked); resetSourceAction(); }} type="checkbox" /> Poll this {sourceKind}</label>
          <label className="compact-field">
            Interval in seconds
            <input disabled={!polling} max={604800} min={60} onChange={(event) => { setPollInterval(event.currentTarget.value); resetSourceAction(); }} required={polling} type="number" value={pollInterval} />
          </label>
        </fieldset>
        <ErrorNotice message={knowledgeBases.error ? safeErrorMessage(knowledgeBases.error) : credentials.error ? safeErrorMessage(credentials.error) : error} />
        {knowledgeBases.isPending || credentials.isPending ? <p aria-live="polite" className="notice">Loading setup options…</p> : null}
        {knowledgeBases.data?.length === 0 ? <p className="notice">Create a knowledge base before adding a source.</p> : null}
        <button className="button primary" disabled={create.isPending || knowledgeBases.data?.length === 0} type="submit">{create.isPending ? `Saving ${sourceKind}…` : `Save and validate ${sourceKind}`}</button>
      </form>
    </section>
  );
}

function credentialOption(credential: Credential): SelectOption {
  return { label: `${credential.label} · ${credential.masked_value}`, value: credential.id };
}

function parsePatterns(value: string): string[] {
  return value.split("\n").map((pattern) => pattern.trim()).filter(Boolean);
}

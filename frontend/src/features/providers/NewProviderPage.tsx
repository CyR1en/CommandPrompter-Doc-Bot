import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";

import {
  createCredential,
  createProviderEndpoint,
  listCredentials,
  safeErrorMessage,
  scheduleDiscovery,
  type Credential,
  type ProviderEndpoint,
  type ProviderEndpointInput,
} from "../../api/client";
import { queryKeys } from "../../api/queries";
import { Select, type SelectOption } from "../../app/Select";
import { ErrorNotice, StatusBadge } from "../../app/StatusBadge";
import { useCsrfToken } from "../../app/auth";
import { useIdempotencyKey } from "../../app/useIdempotencyKey";
import { JobEnqueueNotice } from "../jobs/JobEnqueueNotice";

type EndpointMutationInput = {
  body: ProviderEndpointInput;
  idempotencyKey: string;
};
type DiscoveryMutationInput = {
  endpointId: string;
  expectedVersion: number;
  idempotencyKey: string;
};
type CredentialMode = "none" | "existing" | "new";

class HeaderValidationError extends Error {}

export function NewProviderPage(): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const credentials = useQuery({ queryKey: queryKeys.credentials, queryFn: listCredentials });
  const providerCredentials = credentials.data?.filter((item) => item.kind === "provider_api_key") ?? [];
  const [displayName, setDisplayName] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [headers, setHeaders] = useState("");
  const [allowPrivate, setAllowPrivate] = useState(false);
  const [allowHttp, setAllowHttp] = useState(false);
  const [credentialMode, setCredentialMode] = useState<CredentialMode>("existing");
  const [credentialId, setCredentialId] = useState("");
  const [credentialLabel, setCredentialLabel] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [created, setCreated] = useState<ProviderEndpoint | null>(null);
  const [error, setError] = useState<string | null>(null);
  const endpointKey = useIdempotencyKey();
  const credentialKey = useIdempotencyKey();
  const discoveryKey = useIdempotencyKey();

  const createEndpoint = useMutation({
    mutationFn: (input: EndpointMutationInput) => createProviderEndpoint({ ...input, csrfToken }),
    onSuccess: async (endpoint) => {
      endpointKey.reset();
      setCreated(endpoint);
      await queryClient.invalidateQueries({ queryKey: queryKeys.providers });
    },
  });
  const discover = useMutation({
    mutationFn: (input: DiscoveryMutationInput) => scheduleDiscovery({ ...input, csrfToken }),
    onSuccess: () => discoveryKey.reset(),
  });

  function resetEndpointAction(): void {
    endpointKey.reset();
    setError(null);
  }

  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    setError(null);
    let selectedCredential = credentialMode === "existing" && credentialId ? credentialId : null;
    try {
      const parsedHeaders = parseHeaders(headers);
      if (credentialMode === "new") {
        const credential = await createCredential({
          csrfToken,
          idempotencyKey: credentialKey.current(),
          kind: "provider_api_key",
          label: credentialLabel,
          secret: apiKey,
        });
        selectedCredential = credential.id;
        setApiKey("");
        setCredentialId(credential.id);
        setCredentialMode("existing");
        credentialKey.reset();
        await queryClient.invalidateQueries({ queryKey: queryKeys.credentials });
      }
      await createEndpoint.mutateAsync({
        body: {
          allow_http: allowHttp,
          allow_private_network: allowPrivate,
          base_url: baseUrl,
          chat_completions_path: "chat/completions",
          credential_id: selectedCredential,
          display_name: displayName,
          headers: parsedHeaders,
          models_path: "models",
          responses_path: "responses",
        },
        idempotencyKey: endpointKey.current(),
      });
    } catch (caught: unknown) {
      setError(caught instanceof HeaderValidationError ? caught.message : safeErrorMessage(caught));
    }
  }

  if (created !== null) {
    return (
      <section className="page narrow-page">
        <Link className="back-link" to="/providers">← Provider register</Link>
        <header className="page-heading">
          <div>
            <p className="eyebrow">setup complete</p>
            <h1>{created.display_name}</h1>
          </div>
          <StatusBadge value={created.lifecycle} />
        </header>
        <section className="folio-panel setup-complete" aria-live="polite">
          <h2>Endpoint saved</h2>
          <p>The normalized endpoint is <strong>{created.base_url}</strong>. Start discovery when the provider is ready to receive a model-list request.</p>
          <div className="button-row">
            <button
              className="button primary"
              disabled={discover.isPending}
              onClick={() => discover.mutate({ endpointId: created.id, expectedVersion: created.version, idempotencyKey: discoveryKey.current() })}
              type="button"
            >
              {discover.isPending ? "Enqueueing…" : "Discover models"}
            </button>
            <Link className="button secondary" params={{ endpointId: created.id }} to="/providers/$endpointId">Open endpoint detail</Link>
          </div>
          {discover.data ? <JobEnqueueNotice jobId={discover.data.job_id} label="Discovery" /> : null}
          <ErrorNotice message={discover.error ? safeErrorMessage(discover.error) : null} />
        </section>
      </section>
    );
  }

  return (
    <section className="page narrow-page">
      <Link className="back-link" to="/providers">← Provider register</Link>
      <header className="page-heading">
        <div>
          <p className="eyebrow">endpoint setup</p>
          <h1>Connect a model service</h1>
        </div>
        <p className="lede">The key is written once to the encrypted vault. Endpoint metadata remains inspectable.</p>
      </header>
      <form className="folio-panel form-grid setup-wizard" onSubmit={(event) => void submit(event)}>
        <label>
          Display name
          <input maxLength={255} onChange={(event) => { setDisplayName(event.currentTarget.value); resetEndpointAction(); }} required value={displayName} />
        </label>
        <label>
          Base URL
          <input onChange={(event) => { setBaseUrl(event.currentTarget.value); resetEndpointAction(); }} placeholder="https://models.example/v1" required type="url" value={baseUrl} />
        </label>
        <fieldset className="full-field choice-group">
          <legend>Authentication</legend>
          <label><input checked={credentialMode === "existing"} name="credential-mode" onChange={() => { setCredentialMode("existing"); resetEndpointAction(); }} type="radio" /> Existing provider credential</label>
          <label><input checked={credentialMode === "new"} name="credential-mode" onChange={() => { setCredentialMode("new"); resetEndpointAction(); }} type="radio" /> Add a write-only API key</label>
          <label><input checked={credentialMode === "none"} name="credential-mode" onChange={() => { setCredentialMode("none"); resetEndpointAction(); }} type="radio" /> No credential</label>
        </fieldset>
        {credentialMode === "existing" ? (
          <label className="full-field">
            Provider credential
            <Select
              onChange={(next) => { setCredentialId(next); resetEndpointAction(); }}
              options={[{ label: "Select a credential", value: "" }, ...providerCredentials.map(credentialOption)]}
              required
              value={credentialId}
            />
          </label>
        ) : null}
        {credentialMode === "new" ? (
          <>
            <label>
              Credential label
              <input maxLength={255} onChange={(event) => { setCredentialLabel(event.currentTarget.value); credentialKey.reset(); }} required value={credentialLabel} />
            </label>
            <label>
              API key
              <input autoComplete="off" onChange={(event) => { setApiKey(event.currentTarget.value); credentialKey.reset(); }} required type="password" value={apiKey} />
            </label>
          </>
        ) : null}
        <label className="full-field">
          Optional non-secret headers
          <textarea aria-describedby="headers-help" onChange={(event) => { setHeaders(event.currentTarget.value); resetEndpointAction(); }} placeholder="X-Tenant: documentation" rows={4} value={headers} />
          <small id="headers-help">One header per line. Authentication, cookie, host, proxy, and transport-control headers are rejected.</small>
        </label>
        <fieldset className="full-field choice-group warning-controls">
          <legend>Network exceptions</legend>
          <label><input checked={allowPrivate} onChange={(event) => { setAllowPrivate(event.currentTarget.checked); if (!event.currentTarget.checked) setAllowHttp(false); resetEndpointAction(); }} type="checkbox" /> Permit private-network addresses</label>
          <label><input checked={allowHttp} disabled={!allowPrivate} onChange={(event) => { setAllowHttp(event.currentTarget.checked); resetEndpointAction(); }} type="checkbox" /> Permit plain HTTP on an explicitly trusted private network</label>
          <p>These controls weaken the default outbound network boundary. Enable only for infrastructure you operate.</p>
        </fieldset>
        <ErrorNotice message={credentials.error ? safeErrorMessage(credentials.error) : error} />
        <button className="button primary" disabled={createEndpoint.isPending} type="submit">{createEndpoint.isPending ? "Saving endpoint…" : "Create endpoint"}</button>
      </form>
    </section>
  );
}

function credentialOption(credential: Credential): SelectOption {
  return { label: `${credential.label} · ${credential.masked_value}`, value: credential.id };
}

function parseHeaders(value: string): Record<string, string> {
  const headers: Record<string, string> = {};
  for (const line of value.split("\n")) {
    if (line.trim() === "") continue;
    const separator = line.indexOf(":");
    if (separator < 1) throw new HeaderValidationError("Each non-secret header needs a name and value.");
    const name = line.slice(0, separator).trim();
    const headerValue = line.slice(separator + 1).trim();
    if (!name || !headerValue) throw new HeaderValidationError("Each non-secret header needs a name and value.");
    const normalized = name.toLowerCase().replaceAll("_", "-");
    if (
      ["authorization", "cookie", "host", "connection", "content-length"].includes(normalized)
      || normalized.startsWith("proxy-")
      || ["api-key", "apikey", "secret", "token"].some((part) => normalized.includes(part))
    ) {
      throw new HeaderValidationError("Authentication and transport-control headers are not allowed here.");
    }
    headers[name] = headerValue;
  }
  return headers;
}

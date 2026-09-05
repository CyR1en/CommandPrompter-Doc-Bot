import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";

import {
  createCredential,
  listCredentials,
  rotateCredential,
  safeErrorMessage,
  type Credential,
  type CredentialKind,
} from "../../api/client";
import { queryKeys } from "../../api/queries";
import { Select } from "../../app/Select";
import { EmptyState, ErrorNotice } from "../../app/StatusBadge";
import { useCsrfToken } from "../../app/auth";
import { useIdempotencyKey } from "../../app/useIdempotencyKey";

const kinds: Array<{ label: string; value: CredentialKind }> = [
  { label: "Repository HTTPS", value: "repository_https" },
  { label: "Website header", value: "website_header" },
  { label: "TinyFish API key", value: "tinyfish_api_key" },
  { label: "Provider API key", value: "provider_api_key" },
  { label: "Discord bot token", value: "discord_bot_token" },
];

function parseKind(value: string): CredentialKind {
  return kinds.find((kind) => kind.value === value)?.value ?? "repository_https";
}

export function CredentialsPage(): ReactNode {
  const credentials = useQuery({ queryKey: queryKeys.credentials, queryFn: listCredentials });

  return (
    <section className="page">
      <header className="page-heading">
        <div>
          <p className="eyebrow">settings / credential vault</p>
          <h1>Write-only secrets</h1>
        </div>
        <p className="lede">Values enter the vault once. The interface returns only fixed masks and metadata.</p>
      </header>
      <CreateCredentialForm />
      <section aria-labelledby="credential-list-title" className="ledger-section">
        <div className="section-heading">
          <h2 id="credential-list-title">Stored credentials</h2>
          <span>{credentials.data?.length ?? 0} entries</span>
        </div>
        {credentials.data?.map((credential) => (
          <CredentialRow credential={credential} key={credential.id} />
        ))}
        {credentials.data?.length === 0 ? <EmptyState>No credentials are stored.</EmptyState> : null}
      </section>
    </section>
  );
}

export function CreateCredentialForm(): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const [kind, setKind] = useState<CredentialKind>("repository_https");
  const [label, setLabel] = useState("");
  const [secret, setSecret] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const actionKey = useIdempotencyKey();

  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    setSubmitting(true);
    setError(null);
    try {
      await createCredential({ csrfToken, idempotencyKey: actionKey.current(), kind, label, secret });
      setLabel("");
      setSecret("");
      actionKey.reset();
      await queryClient.invalidateQueries({ queryKey: queryKeys.credentials });
    } catch (caught: unknown) {
      setError(safeErrorMessage(caught));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <section aria-labelledby="new-credential-title" className="folio-panel">
      <h2 id="new-credential-title">Add credential</h2>
      <form className="form-grid" onSubmit={(event) => void submit(event)}>
        <label>
          Kind
          <Select
            onChange={(next) => { setKind(parseKind(next)); actionKey.reset(); }}
            options={kinds}
            value={kind}
          />
        </label>
        <label>
          Label
          <input maxLength={255} onChange={(event) => { setLabel(event.currentTarget.value); actionKey.reset(); }} required value={label} />
        </label>
        <label className="full-field">
          Secret value
          <input autoComplete="off" onChange={(event) => { setSecret(event.currentTarget.value); actionKey.reset(); }} required type="password" value={secret} />
          {kind === "tinyfish_api_key" ? <small className="field-note">Used by TinyFish website crawl. The value is written once and never shown again.</small> : null}
        </label>
        <ErrorNotice message={error} />
        <button className="button primary" disabled={submitting} type="submit">
          {submitting ? "Encrypting…" : "Store encrypted credential"}
        </button>
      </form>
    </section>
  );
}

function CredentialRow({ credential }: { credential: Credential }): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const [rotating, setRotating] = useState(false);
  const [secret, setSecret] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const actionKey = useIdempotencyKey();

  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    setSubmitting(true);
    setError(null);
    try {
      await rotateCredential({
        credentialId: credential.id,
        csrfToken,
        idempotencyKey: actionKey.current(),
        secret,
      });
      setSecret("");
      setRotating(false);
      actionKey.reset();
      await queryClient.invalidateQueries({ queryKey: queryKeys.credentials });
    } catch (caught: unknown) {
      setError(safeErrorMessage(caught));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <article className="credential-row">
      <div>
        <span className="credential-kind">{credential.kind.replaceAll("_", " ")}</span>
        <h3>{credential.label}</h3>
        <p><span aria-label="masked credential">{credential.masked_value}</span> · version {credential.secret_version} · key {credential.key_id}</p>
      </div>
      {rotating ? (
        <form className="rotate-form" onSubmit={(event) => void submit(event)}>
          <label>
            Replacement secret for {credential.label}
            <input autoComplete="off" autoFocus onChange={(event) => { setSecret(event.currentTarget.value); actionKey.reset(); }} required type="password" value={secret} />
          </label>
          <ErrorNotice message={error} />
          <div className="button-row">
            <button className="button secondary" onClick={() => { setSecret(""); actionKey.reset(); setRotating(false); }} type="button">Cancel</button>
            <button className="button danger" disabled={submitting} type="submit">{submitting ? "Rotating…" : "Rotate secret"}</button>
          </div>
        </form>
      ) : (
        <button className="button secondary" onClick={() => setRotating(true)} type="button">Rotate</button>
      )}
    </article>
  );
}

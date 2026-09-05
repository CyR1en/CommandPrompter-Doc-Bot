import { Link, useNavigate } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";

import { safeErrorMessage } from "../../api/client";
import { ErrorNotice } from "../../app/StatusBadge";
import { useAuth } from "../../app/auth";

export function SettingsPage(): ReactNode {
  const auth = useAuth();
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  if (auth.state.kind !== "authenticated") return null;

  async function signOut(): Promise<void> {
    setSubmitting(true);
    setError(null);
    try {
      await auth.logout();
      await navigate({ to: "/login" });
    } catch (caught: unknown) {
      setError(safeErrorMessage(caught));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <section className="page narrow-page">
      <header className="page-heading">
        <div>
          <p className="eyebrow">settings</p>
          <h1>Operator desk</h1>
        </div>
      </header>
      <article className="folio-panel settings-card">
        <p className="label-line">Operator</p>
        <h2>{auth.state.session.operator.username}</h2>
        <dl>
          <div>
            <dt>Session expires</dt>
            <dd>{new Date(auth.state.session.expires_at).toLocaleString()}</dd>
          </div>
        </dl>
      </article>
      <Link className="folio-panel settings-link" to="/settings/credentials">
        <span>
          <small>Encrypted configuration</small>
          <strong>Credential vault</strong>
        </span>
        <span aria-hidden="true">→</span>
      </Link>
      <Link className="folio-panel settings-link" to="/settings/chat-access-tokens">
        <span>
          <small>Open WebUI-compatible access</small>
          <strong>Chat access tokens</strong>
          <span className="field-note">Issue scoped bearer tokens after reviewing the current effective Agent scope.</span>
        </span>
        <span aria-hidden="true">→</span>
      </Link>
      <a
        className="folio-panel settings-link"
        download="ref0-configuration.json"
        href="/api/v1/settings/export"
      >
        <span>
          <small>Portable operations record</small>
          <strong>Download non-secret configuration</strong>
          <span className="field-note">Includes Wiki export links. Credential values and encrypted material are excluded.</span>
        </span>
        <span aria-hidden="true">↓</span>
      </a>
      <section aria-labelledby="session-actions-title" className="folio-panel">
        <h2 id="session-actions-title">Session</h2>
        <p>Sign out and revoke this browser session.</p>
        <ErrorNotice message={error} />
        <button className="button secondary" disabled={submitting} onClick={() => void signOut()} type="button">
          {submitting ? "Signing out…" : "Sign out"}
        </button>
      </section>
    </section>
  );
}

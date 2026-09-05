import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";

import {
  actionId,
  issueChatAccessToken,
  listKnowledgeBases,
  previewChatAccessTokenScopes,
  revokeChatAccessToken,
  safeErrorMessage,
  type ChatAccessToken,
  type ChatAccessTokenSummary,
  type ChatAccessTokenScopePreview,
  type IssuedChatAccessToken,
} from "../../api/client";
import { queryKeys } from "../../api/queries";
import { ConfirmationDialog } from "../../app/ConfirmationDialog";
import { EmptyState, ErrorNotice, StatusBadge } from "../../app/StatusBadge";
import { useCsrfToken } from "../../app/auth";
import { useIdempotencyKey } from "../../app/useIdempotencyKey";
import { useAgentPages } from "../agents/queries";
import { useChatAccessTokenPages } from "./chatTokenQueries";

export function ChatAccessTokensPage(): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const actionKey = useIdempotencyKey();
  const agents = useAgentPages();
  const tokens = useChatAccessTokenPages();
  const knowledgeBases = useQuery({ queryKey: queryKeys.knowledgeBases, queryFn: listKnowledgeBases });
  const [label, setLabel] = useState("");
  const [expiresAt, setExpiresAt] = useState(defaultExpiry());
  const [selectedAgentIDs, setSelectedAgentIDs] = useState<string[]>([]);
  const [preview, setPreview] = useState<ChatAccessTokenScopePreview | null>(null);
  const [reviewOpen, setReviewOpen] = useState(false);
  const [reviewBusy, setReviewBusy] = useState(false);
  const [issueBusy, setIssueBusy] = useState(false);
  const [issued, setIssued] = useState<IssuedChatAccessToken | null>(null);
  const [replay, setReplay] = useState<ChatAccessToken | null>(null);
  const [error, setError] = useState<string | null>(null);
  const agentItems = agents.data?.pages.flatMap((page) => page.items) ?? [];
  const tokenItems = tokens.data?.pages.flatMap((page) => page.items) ?? [];
  const queryError = agents.error ?? tokens.error ?? knowledgeBases.error;

  function invalidatePreview(): void {
    setPreview(null);
    setReviewOpen(false);
    setReplay(null);
    setError(null);
    actionKey.reset();
  }

  function updateLabel(value: string): void {
    setLabel(value);
    invalidatePreview();
  }

  function updateExpiry(value: string): void {
    setExpiresAt(value);
    invalidatePreview();
  }

  function toggleAgent(agentID: string, checked: boolean): void {
    setSelectedAgentIDs((current) => checked
      ? current.includes(agentID) || current.length >= 2_048 ? current : [...current, agentID]
      : current.filter((id) => id !== agentID));
    invalidatePreview();
  }

  async function review(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (issued !== null || label.trim() === "" || selectedAgentIDs.length === 0 || !futureDate(expiresAt)) return;
    setReviewBusy(true);
    setError(null);
    try {
      const value = await previewChatAccessTokenScopes({ agent_ids: selectedAgentIDs });
      setPreview(value);
      setReviewOpen(true);
    } catch (caught: unknown) {
      setError(safeErrorMessage(caught));
    } finally {
      setReviewBusy(false);
    }
  }

  async function issue(): Promise<void> {
    if (preview === null || issued !== null) return;
    setIssueBusy(true);
    setError(null);
    try {
      const result = await issueChatAccessToken({
        body: { agent_ids: preview.agent_ids, expires_at: new Date(expiresAt).toISOString(), label: label.trim() },
        csrfToken,
        idempotencyKey: actionKey.current(),
      });
      setReviewOpen(false);
      setPreview(null);
      if (result.kind === "issued") {
        setIssued(result.token);
        setReplay(null);
        setLabel("");
        setSelectedAgentIDs([]);
        setExpiresAt(defaultExpiry());
      } else {
        setIssued(null);
        setReplay(result.token);
      }
      actionKey.reset();
      await queryClient.invalidateQueries({ queryKey: queryKeys.chatAccessTokens });
    } catch (caught: unknown) {
      setError(safeErrorMessage(caught));
    } finally {
      setIssueBusy(false);
    }
  }

  return (
    <section className="page chat-token-page">
      <header className="page-heading">
        <div><p className="eyebrow">API credentials</p><h1>Chat access tokens</h1></div>
        <p className="lede">Issue explicit Agent scopes for Open WebUI or another OpenAI-compatible client.</p>
      </header>
      <ErrorNotice message={(reviewOpen ? null : error) ?? (queryError ? safeErrorMessage(queryError) : null)} />
      {issued ? <SecretOnce issued={issued} onDismiss={() => setIssued(null)} /> : null}
      {replay ? <p className="notice warning" role="status"><strong>Secret was already issued.</strong> Token {replay.prefix} exists, but its plaintext cannot be recovered. Revoke it and issue a new token if the secret was not saved.</p> : null}

      <div className="token-layout">
        <section aria-labelledby="new-token-title" className="folio-panel token-issuer">
          <p className="eyebrow">New credential</p>
          <h2 id="new-token-title">Create chat access token</h2>
          <form className="form-grid" onSubmit={(event) => void review(event)}>
            <label>Token label<input disabled={issued !== null} maxLength={255} onChange={(event) => updateLabel(event.currentTarget.value)} required value={label} /></label>
            <label>Expires at<input disabled={issued !== null} min={localDateTime(new Date())} onChange={(event) => updateExpiry(event.currentTarget.value)} required type="datetime-local" value={expiresAt} /></label>
            <fieldset className="full-field agent-scope-picker" disabled={issued !== null}>
              <legend>Agents</legend>
              <p className="field-note">Choose explicit Agents. Future Agents are never added to this token automatically.</p>
              {agentItems.map((agent) => (
                <label className="scope-check" key={agent.id}>
                  <input checked={selectedAgentIDs.includes(agent.id)} disabled={!selectedAgentIDs.includes(agent.id) && selectedAgentIDs.length >= 2_048} onChange={(event) => toggleAgent(agent.id, event.currentTarget.checked)} type="checkbox" />
                  <span><strong>{agent.current_version.configuration.display_name}</strong><small>{agent.selector} · {agent.lifecycle}</small></span>
                </label>
              ))}
              {!agents.isPending && agentItems.length === 0 ? <EmptyState>Create an Agent before issuing a chat token.</EmptyState> : null}
              {agents.hasNextPage ? <button className="button secondary" disabled={agents.isFetchingNextPage} onClick={() => void agents.fetchNextPage()} type="button">{agents.isFetchingNextPage ? "Loading Agents…" : "Load more Agents"}</button> : null}
              <p className="field-note">{selectedAgentIDs.length} selected · maximum 2,048 per token</p>
            </fieldset>
            <button className="button primary" disabled={issued !== null || reviewBusy || label.trim() === "" || selectedAgentIDs.length === 0 || !futureDate(expiresAt)} type="submit">{reviewBusy ? "Resolving current scope…" : "Review token scope"}</button>
            {issued !== null ? <p className="field-note full-field">Dismiss the visible secret before issuing another token.</p> : null}
          </form>
        </section>

        <section aria-labelledby="token-ledger-title" className="ledger-section token-ledger">
          <div className="section-heading"><div><p className="eyebrow">Credential ledger</p><h2 id="token-ledger-title">Issued tokens</h2></div><span>{tokenItems.length} loaded</span></div>
          {tokenItems.map((token) => <TokenCard csrfToken={csrfToken} key={token.id} onRevoked={() => queryClient.invalidateQueries({ queryKey: queryKeys.chatAccessTokens })} token={token} />)}
          {!tokens.isPending && tokenItems.length === 0 ? <EmptyState>No chat access tokens have been issued.</EmptyState> : null}
          {tokens.hasNextPage ? <button className="button secondary load-more" disabled={tokens.isFetchingNextPage} onClick={() => void tokens.fetchNextPage()} type="button">{tokens.isFetchingNextPage ? "Loading tokens…" : "Load more tokens"}</button> : null}
        </section>
      </div>

      <TokenReviewDialog
        busy={issueBusy}
        error={error}
        knowledgeBaseName={(id) => knowledgeBases.data?.find((knowledgeBase) => knowledgeBase.id === id)?.name ?? id}
        onClose={() => setReviewOpen(false)}
        onIssue={issue}
        open={reviewOpen}
        preview={preview}
      />
    </section>
  );
}

function TokenReviewDialog({ busy, error, knowledgeBaseName, onClose, onIssue, open, preview }: {
  busy: boolean;
  error: string | null;
  knowledgeBaseName(id: string): string;
  onClose(): void;
  onIssue(): Promise<void>;
  open: boolean;
  preview: ChatAccessTokenScopePreview | null;
}): ReactNode {
  const dialogRef = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return;
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
  }, [open]);

  return (
    <dialog aria-labelledby="token-review-title" className="confirmation-dialog token-review-dialog" onCancel={(event) => { event.preventDefault(); onClose(); }} ref={dialogRef}>
      <div>
        <p className="eyebrow">Current effective scope</p>
        <h2 id="token-review-title">Review chat access token</h2>
        {preview ? (
          <>
            <p>This advisory snapshot follows current Agent configurations. The token continues to follow those Agents as their configurations change.</p>
            <div className="scope-summary"><StatusBadge value={preview.effective_access} /><strong>{preview.ready ? "All Agents ready" : "One or more Agents unavailable"}</strong><span>{preview.knowledge_base_ids.length} unique knowledge bases</span></div>
            <ol className="scope-preview-list">{preview.agent_scopes.map((scope) => <li key={scope.agent_id}><header><strong>agent:{scope.agent_key}</strong><StatusBadge value={scope.ready ? "ready" : "not_ready"} /></header><p>{scope.effective_access} · {scope.knowledge_base_ids.length} knowledge bases</p><ol>{scope.knowledge_base_ids.map((id) => <li key={id}>{knowledgeBaseName(id)}</li>)}</ol></li>)}</ol>
            <section aria-labelledby="scope-union-title" className="scope-union"><h3 id="scope-union-title">Complete transitive knowledge set</h3><ol>{preview.knowledge_base_ids.map((id) => <li key={id}>{knowledgeBaseName(id)}</li>)}</ol></section>
            {!preview.ready ? <p className="notice warning">Unready Agents will not appear in <code>/v1/models</code> until they become active and ready. Issuance is still allowed.</p> : null}
          </>
        ) : null}
        <ErrorNotice message={error} />
        <div className="dialog-actions"><button className="button secondary" disabled={busy} onClick={onClose} type="button">Back to selection</button><button className="button primary" disabled={busy || preview === null} onClick={() => void onIssue()} type="button">{busy ? "Issuing token…" : "Issue token"}</button></div>
      </div>
    </dialog>
  );
}

function SecretOnce({ issued, onDismiss }: { issued: IssuedChatAccessToken; onDismiss(): void }): ReactNode {
  const [copyStatus, setCopyStatus] = useState("Copy before leaving this page.");
  const regionRef = useRef<HTMLElement>(null);
  useEffect(() => {
    let active = true;
    queueMicrotask(() => {
      if (active) regionRef.current?.focus();
    });
    return () => { active = false; };
  }, []);
  async function copy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(issued.secret);
      setCopyStatus("Copied to clipboard.");
    } catch {
      setCopyStatus("Clipboard access failed. Select and copy the token manually.");
    }
  }
  return (
    <section aria-label="Copy token now" className="secret-once" ref={regionRef} tabIndex={-1}>
      <div aria-live="assertive" role="status"><p className="eyebrow">Secret shown once</p><h2>Copy token now</h2><p>ref0 stores only a digest. This plaintext cannot be shown again.</p></div>
      <label>Chat access token secret<input onFocus={(event) => event.currentTarget.select()} readOnly value={issued.secret} /></label>
      <div className="button-row"><button className="button primary" onClick={() => void copy()} type="button">Copy token</button><button className="button secondary" onClick={onDismiss} type="button">Dismiss secret</button></div>
      <p aria-live="polite">{copyStatus}</p>
    </section>
  );
}

function TokenCard({ csrfToken, onRevoked, token }: { csrfToken: string; onRevoked(): Promise<unknown>; token: ChatAccessTokenSummary }): ReactNode {
  const [confirm, setConfirm] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const status = token.revoked_at ? "revoked" : new Date(token.expires_at) <= new Date() ? "expired" : "active";
  async function revoke(): Promise<void> {
    setError(null);
    try {
      await revokeChatAccessToken({ csrfToken, idempotencyKey: actionId(), tokenId: token.id });
      setError(null);
      setConfirm(false);
      await onRevoked();
    } catch (caught: unknown) {
      setError(safeErrorMessage(caught));
      throw caught;
    }
  }
  return (
    <article className="token-card">
      <header><div><h3 ref={headingRef}>{token.label}</h3><p>{token.prefix}</p></div><StatusBadge value={status} /></header>
      <dl><div><dt>Agents</dt><dd>{token.agent_count}</dd></div><div><dt>Expires</dt><dd>{formatDate(token.expires_at)}</dd></div><div><dt>Last used</dt><dd>{token.last_used_at ? formatDate(token.last_used_at) : "Never"}</dd></div></dl>
      <ErrorNotice message={error} />
      {status === "active" ? <button className="button danger" onClick={() => setConfirm(true)} type="button">Revoke token</button> : null}
      <ConfirmationDialog confirmLabel="Revoke token" error={error} fallbackFocusRef={headingRef} onClose={() => setConfirm(false)} onConfirm={revoke} open={confirm} title={`Revoke “${token.label}”?`}><p>This immediately rejects future compatibility API calls. The action cannot reveal or recover the plaintext secret.</p></ConfirmationDialog>
    </article>
  );
}

function defaultExpiry(): string {
  const value = new Date();
  value.setDate(value.getDate() + 30);
  return localDateTime(value);
}

function localDateTime(value: Date): string {
  const local = new Date(value.getTime() - value.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

function futureDate(value: string): boolean {
  const parsed = new Date(value);
  return Number.isFinite(parsed.getTime()) && parsed > new Date();
}

function formatDate(value: string): string {
  return new Date(value).toLocaleString();
}

import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { safeErrorMessage } from "../../api/client";
import { EmptyState, ErrorNotice, StatusBadge } from "../../app/StatusBadge";
import { useAgentPages } from "./queries";

export function AgentsPage(): ReactNode {
  const agents = useAgentPages();
  const items = agents.data?.pages.flatMap((page) => page.items) ?? [];

  return (
    <section className="page agents-page">
      <header className="page-heading agent-index-heading">
        <div>
          <p className="eyebrow">Delivery intelligence</p>
          <h1>Agents</h1>
          <p className="lede">Versioned identities that answer from an ordered, authorized knowledge set.</p>
        </div>
        <Link className="button primary" to="/agents/new">Create Agent</Link>
      </header>
      <ErrorNotice message={agents.error ? safeErrorMessage(agents.error) : null} />
      {agents.isPending ? <p aria-live="polite" className="notice">Loading Agents…</p> : null}
      {items.length > 0 ? (
        <section aria-labelledby="agent-catalog-title" className="ledger-section">
          <div className="section-heading">
            <div>
              <p className="eyebrow">Catalog</p>
              <h2 id="agent-catalog-title">Configured Agents</h2>
            </div>
            <span>{items.length} loaded</span>
          </div>
          <div className="agent-index-grid">
            {items.map((agent) => (
              <Link
                className="record-card agent-card"
                hash="identity"
                key={agent.id}
                params={{ agentId: agent.id }}
                to="/agents/$agentId"
              >
                <header>
                  <div>
                    <p className="label-line">{agent.selector}</p>
                    <h3>{agent.current_version.configuration.display_name}</h3>
                  </div>
                  <StatusBadge value={agent.lifecycle} />
                </header>
                <p>{agent.current_version.configuration.description || "No description supplied."}</p>
                <dl>
                  <div><dt>Configuration</dt><dd>v{agent.current_version.version_number}</dd></div>
                  <div><dt>Knowledge</dt><dd>{agent.current_version.configuration.knowledge_base_ids.length} bases</dd></div>
                  <div><dt>Updated</dt><dd>{formatDate(agent.updated_at)}</dd></div>
                </dl>
              </Link>
            ))}
          </div>
          {agents.hasNextPage ? (
            <button
              className="button secondary load-more"
              disabled={agents.isFetchingNextPage}
              onClick={() => void agents.fetchNextPage()}
              type="button"
            >
              {agents.isFetchingNextPage ? "Loading Agents…" : "Load more Agents"}
            </button>
          ) : null}
        </section>
      ) : null}
      {!agents.isPending && items.length === 0 ? (
        <EmptyState>No Agents exist yet. Create one to define an answer identity and delivery scope.</EmptyState>
      ) : null}
    </section>
  );
}

function formatDate(value: string): string {
  return new Date(value).toLocaleString();
}

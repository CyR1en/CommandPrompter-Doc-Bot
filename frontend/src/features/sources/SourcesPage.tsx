import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { listSources, safeErrorMessage } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { EmptyState, ErrorNotice, StatusBadge } from "../../app/StatusBadge";

export function SourcesPage(): ReactNode {
  const sources = useQuery({ queryKey: queryKeys.sources, queryFn: () => listSources() });

  return (
    <section className="page">
      <header className="page-heading">
        <div>
          <p className="eyebrow">connected sources</p>
          <h1>Source register</h1>
        </div>
        <Link className="button primary" to="/sources/new">Add source</Link>
      </header>
      <ErrorNotice message={sources.error ? safeErrorMessage(sources.error) : null} />
      {sources.isPending ? <p aria-live="polite" className="notice">Loading sources…</p> : null}
      <div className="record-grid">
        {sources.data?.map((source) => (
          <Link className="record-card source-card" key={source.id} params={{ sourceId: source.id }} to="/sources/$sourceId">
            <div>
              <p className="eyebrow">{source.privacy} · {source.kind}</p>
              <h2>{source.name}</h2>
              {source.kind === "repository" ? (
                <>
                  <p className="endpoint-url">{source.remote_host}/{source.repository_path}</p>
                  <p><code>{source.ref_kind} · {source.ref_value}</code></p>
                </>
              ) : (
                <>
                  <p className="endpoint-url">{source.root_host}</p>
                  <p><code>{source.root_url}</code></p>
                </>
              )}
            </div>
            <div className="status-stack">
              <StatusBadge value={source.health} />
              <StatusBadge value={source.lifecycle} />
            </div>
          </Link>
        ))}
      </div>
      {sources.data?.length === 0 ? (
        <EmptyState>No sources are configured. Add a public or private HTTPS repository or website.</EmptyState>
      ) : null}
    </section>
  );
}

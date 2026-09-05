import { useQuery } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";

import { getAgentRun, safeErrorMessage, type AgentRunDetail } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { EmptyState, ErrorNotice, StatusBadge } from "../../app/StatusBadge";
import { useAgentRunPages, useAgentVersionPages } from "./queries";

export function AgentRunsPanel({ agentId }: { agentId: string }): ReactNode {
  const [selectedRunID, setSelectedRunID] = useState("");
  const runs = useAgentRunPages(agentId);
  const versions = useAgentVersionPages(agentId);
  const runItems = runs.data?.pages.flatMap((page) => page.items) ?? [];
  const versionItems = versions.data?.pages.flatMap((page) => page.items) ?? [];
  const selectedRun = useQuery({
    enabled: selectedRunID !== "",
    queryKey: [...queryKeys.agentRuns, agentId, "detail", selectedRunID],
    queryFn: () => getAgentRun({ agentId, runId: selectedRunID }),
  });

  return (
    <div className="agent-history-grid">
      <section aria-labelledby="agent-runs-title" className="ledger-section" id="runs">
        <div className="section-heading">
          <div><p className="eyebrow">Operations history</p><h2 id="agent-runs-title">Recent Agent runs</h2></div>
          <button className="button secondary" disabled={runs.isFetching} onClick={() => void runs.refetch()} type="button">Refresh runs</button>
        </div>
        <ErrorNotice message={runs.error ? safeErrorMessage(runs.error) : null} />
        <div className="run-ledger">
          {runItems.map((run) => (
            <button className={`run-row${selectedRunID === run.id ? " is-selected" : ""}`} key={run.id} onClick={() => setSelectedRunID(run.id)} type="button">
              <span><strong>{run.subject}</strong><small>{run.origin} · Agent v{run.agent_version_number}</small></span>
              <StatusBadge value={run.outcome} />
              <span>{run.latency_ms} ms</span>
              <time dateTime={run.completed_at}>{formatDate(run.completed_at)}</time>
            </button>
          ))}
        </div>
        {!runs.isPending && runItems.length === 0 ? <EmptyState>No Agent runs have been recorded.</EmptyState> : null}
        {runs.hasNextPage ? (
          <button className="button secondary load-more" disabled={runs.isFetchingNextPage} onClick={() => void runs.fetchNextPage()} type="button">
            {runs.isFetchingNextPage ? "Loading runs…" : "Load more runs"}
          </button>
        ) : null}
        <ErrorNotice message={selectedRun.error ? safeErrorMessage(selectedRun.error) : null} />
        {selectedRun.data ? <RunReceipt run={selectedRun.data} /> : null}
      </section>

      <section aria-labelledby="agent-versions-title" className="ledger-section" id="versions">
        <div className="section-heading"><div><p className="eyebrow">Immutable ledger</p><h2 id="agent-versions-title">Configuration versions</h2></div><span>{versionItems.length} loaded</span></div>
        <ErrorNotice message={versions.error ? safeErrorMessage(versions.error) : null} />
        <div className="version-ledger">
          {versionItems.map((version) => (
            <article className="version-row" key={version.id}>
              <header><strong>Version {version.version_number}</strong><time dateTime={version.created_at}>{formatDate(version.created_at)}</time></header>
              <p>{version.configuration.display_name} · {version.configuration.answer_mode.replaceAll("_", " ")} · {version.configuration.knowledge_base_ids.length} knowledge bases</p>
              <small>{version.id}</small>
            </article>
          ))}
        </div>
        {versions.hasNextPage ? (
          <button className="button secondary load-more" disabled={versions.isFetchingNextPage} onClick={() => void versions.fetchNextPage()} type="button">
            {versions.isFetchingNextPage ? "Loading versions…" : "Load more versions"}
          </button>
        ) : null}
      </section>
    </div>
  );
}

function RunReceipt({ run }: { run: AgentRunDetail }): ReactNode {
  return (
    <article aria-labelledby="run-receipt-title" className="run-receipt">
      <div className="section-heading"><h3 id="run-receipt-title">Captured run receipt</h3><StatusBadge value={run.effective_access} /></div>
      <dl className="receipt-grid">
        <div><dt>Run</dt><dd>{run.id}</dd></div>
        <div><dt>Agent version</dt><dd>v{run.agent_version_number} · resource {run.agent_resource_version}</dd></div>
        <div><dt>Model profile version</dt><dd>v{run.model_profile_version_number} · {run.model_profile_version_id}</dd></div>
        <div><dt>Endpoint configuration</dt><dd>v{run.captured_endpoint_configuration_version} · {run.provider_endpoint_id}</dd></div>
        <div><dt>Credential</dt><dd>{run.captured_credential_id ? `v${run.captured_credential_version} · ${run.captured_credential_id}` : "none"}</dd></div>
        <div><dt>Usage</dt><dd>{usageSummary(run.usage)}</dd></div>
      </dl>
      {run.sanitized_error ? <p className="notice error">{run.sanitized_error}</p> : null}
      <div className="receipt-columns">
        <section aria-labelledby="receipt-knowledge-title">
          <h4 id="receipt-knowledge-title">Captured knowledge</h4>
          <ol>{run.knowledge_bases.map((knowledgeBase) => <li key={knowledgeBase.knowledge_base_id}><strong>Position {knowledgeBase.position + 1}</strong><span>{knowledgeBase.knowledge_base_id}</span><small>KB v{knowledgeBase.knowledge_base_version} · Wiki {knowledgeBase.wiki_version_id} · {knowledgeBase.access_policy}</small></li>)}</ol>
        </section>
        <section aria-labelledby="receipt-tools-title">
          <h4 id="receipt-tools-title">Tools and citations</h4>
          <p>{run.tool_calls.length === 0 ? "No tool calls recorded." : run.tool_calls.join(", ")}</p>
          <ol>{run.citations.map((citation) => <li key={citation.id}><strong>{citation.label}</strong><span>{citation.resource}</span><small>KB {citation.knowledge_base_id} · Wiki {citation.wiki_version_id}</small></li>)}</ol>
          {run.citations.length === 0 ? <p>No verified citations recorded.</p> : null}
        </section>
      </div>
    </article>
  );
}

function usageSummary(usage: Record<string, number>): string {
  const entries = Object.entries(usage);
  return entries.length === 0 ? "No token usage recorded" : entries.map(([name, value]) => `${name.replaceAll("_", " ")} ${value}`).join(" · ");
}

function formatDate(value: string): string {
  return new Date(value).toLocaleString();
}

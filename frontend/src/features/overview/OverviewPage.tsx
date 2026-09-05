import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

import {
  getOperationalOverview,
  safeErrorMessage,
} from "../../api/client";
import { queryKeys } from "../../api/queries";
import { EmptyState, ErrorNotice, StatusBadge } from "../../app/StatusBadge";

export function OverviewPage(): ReactNode {
  const overview = useQuery({
    queryKey: queryKeys.overview,
    queryFn: getOperationalOverview,
    refetchInterval: 30_000,
  });
  const data = overview.data;

  return (
    <section className="page overview-page">
      <header className="page-heading">
        <div>
          <p className="eyebrow">overview</p>
          <h1>Today’s operations ledger</h1>
        </div>
        <p className="lede">Current failures and publishing drift, read directly from the control plane.</p>
      </header>
      <ErrorNotice message={overview.error ? safeErrorMessage(overview.error) : null} />
      <div className="metric-grid operational-metrics" aria-label="Operational attention counts">
        <MetricCard count={data?.unhealthy_sources.length} label="Unhealthy sources" to="/sources" />
        <MetricCard count={data?.failed_jobs.length} label="Failed jobs" to="/jobs" />
        <MetricCard count={data?.knowledge_base_issues.length} label="KB publishing issues" to="/knowledge-bases" />
        <MetricCard count={data?.provider_errors.length} label="Provider errors" to="/providers" />
		<MetricCard count={data?.agent_failures.length} label="Failed Agent runs" to="/agents" />
      </div>

      <AttentionSection title="Knowledge bases needing publication">
        {data?.knowledge_base_issues.map((item) => (
          <Link className="operational-row" key={item.id} params={{ id: item.id }} to="/knowledge-bases/$id">
            <span><strong>{item.name}</strong><small>{item.kind === "unpublished" ? "No Wiki has been published" : "Published Wiki is behind current configuration or sources"}</small></span>
            <StatusBadge value={item.kind} />
            <time dateTime={item.updated_at}>{formatDate(item.updated_at)}</time>
          </Link>
        ))}
        {data?.knowledge_base_issues.length === 0 ? <EmptyState>Every active knowledge base has a current publication.</EmptyState> : null}
      </AttentionSection>

      <AttentionSection title="Unhealthy sources">
        {data?.unhealthy_sources.map((item) => (
          <Link className="operational-row" key={item.id} params={{ sourceId: item.id }} to="/sources/$sourceId">
            <span><strong>{item.display_name}</strong><small>{item.knowledge_base_name} · {item.sanitized_error}</small></span>
            <StatusBadge value="unhealthy" />
            <time dateTime={item.checked_at}>{formatDate(item.checked_at)}</time>
          </Link>
        ))}
        {data?.unhealthy_sources.length === 0 ? <EmptyState>No source health failures are recorded.</EmptyState> : null}
      </AttentionSection>

      <AttentionSection title="Failed jobs">
        {data?.failed_jobs.map((item) => (
          <Link className="operational-row" key={item.id} params={{ id: item.id }} to="/jobs/$id">
            <span><strong>{humanize(item.job_type)}</strong><small>{item.sanitized_error ?? "The job exhausted its attempts."}</small></span>
            <span>{item.attempt_count} / {item.max_attempts} attempts</span>
            <time dateTime={item.updated_at}>{formatDate(item.updated_at)}</time>
          </Link>
        ))}
        {data?.failed_jobs.length === 0 ? <EmptyState>No failed jobs are recorded.</EmptyState> : null}
      </AttentionSection>

      <AttentionSection title="Provider errors">
        {data?.provider_errors.map((item) => (
          <Link className="operational-row" key={item.run_id} params={{ endpointId: item.endpoint_id }} to="/providers/$endpointId">
            <span><strong>{item.endpoint_name}</strong><small>{item.sanitized_error}</small></span>
            <span>{humanize(item.operation)}</span>
            <time dateTime={item.occurred_at}>{formatDate(item.occurred_at)}</time>
          </Link>
        ))}
        {data?.provider_errors.length === 0 ? <EmptyState>No recent provider failures are recorded.</EmptyState> : null}
      </AttentionSection>

	  <AttentionSection title="Recent failed Agent runs">
		{data?.agent_failures.map((item) => (
		  <Link className="operational-row" hash="runs" key={item.id} params={{ agentId: item.agent_id }} to="/agents/$agentId">
			<span><strong>{item.display_name}</strong><small>{item.sanitized_error}</small></span>
			<span>{humanize(item.origin)} · version {item.agent_version_number}</span>
			<time dateTime={item.created_at}>{formatDate(item.created_at)}</time>
		  </Link>
		))}
		{data?.agent_failures.length === 0 ? <EmptyState>No recent Agent runs failed.</EmptyState> : null}
      </AttentionSection>

      {data ? <p className="overview-timestamp">Snapshot generated <time dateTime={data.generated_at}>{formatDate(data.generated_at)}</time>.</p> : null}
    </section>
  );
}

function MetricCard({ count, label, to }: { count: number | undefined; label: string; to: "/agents" | "/jobs" | "/knowledge-bases" | "/providers" | "/sources" }): ReactNode {
  return (
    <Link className="metric-card" to={to}>
      <span>{label}</span>
      <strong>{count ?? "—"}</strong>
      <small>{count === 0 ? "clear" : "needs attention"}</small>
    </Link>
  );
}

function AttentionSection({ children, title }: { children: ReactNode; title: string }): ReactNode {
  const id = `overview-${title.toLocaleLowerCase().replaceAll(" ", "-")}`;
  return (
    <section aria-labelledby={id} className="ledger-section">
      <div className="section-heading"><h2 id={id}>{title}</h2></div>
      {children}
    </section>
  );
}

function formatDate(value: string): string {
  return new Date(value).toLocaleString();
}

function humanize(value: string): string {
  return value.replaceAll("_", " ");
}

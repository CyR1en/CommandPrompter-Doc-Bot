import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { getDocumentationRun, safeErrorMessage } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { EmptyState, ErrorNotice, StatusBadge } from "../../app/StatusBadge";

export function RunDetailPage({ runId }: { runId: string }): ReactNode {
  const run = useQuery({ queryKey: [...queryKeys.runs, runId], queryFn: () => getDocumentationRun(runId) });
  const record = run.data;
  const pages = [...(record?.pages ?? [])].sort((left, right) => left.position - right.position);

  return (
    <section className="page narrow-page">
      <Link className="back-link" to="/runs">← Generation ledger</Link>
      <header className="page-heading">
        <div><p className="eyebrow">run detail</p><h1>{record ? `Run ${shortId(record.id)}` : "Loading run…"}</h1></div>
        {record ? <StatusBadge value={record.status} /> : null}
      </header>
      <ErrorNotice message={run.error ? safeErrorMessage(run.error) : null} />
      {run.isPending ? <p aria-live="polite" className="notice">Loading run detail…</p> : null}
      {record ? (
        <>
          <section aria-labelledby="run-summary-title" className="folio-panel detail-ledger">
            <div className="section-heading"><h2 id="run-summary-title">Captured run</h2><span>{record.status.replaceAll("_", " ")}</span></div>
            <dl>
              <div><dt>Knowledge base</dt><dd><Link params={{ id: record.knowledge_base_id }} to="/knowledge-bases/$id">{record.knowledge_base_id}</Link> · captured version {record.knowledge_base_version}</dd></div>
              <div><dt>Preparation job</dt><dd><Link params={{ id: record.prepare_job_id }} to="/jobs/$id">{record.prepare_job_id}</Link></dd></div>
              <div><dt>Language</dt><dd>{record.language}</dd></div>
              <div><dt>Plan digest</dt><dd><code>{record.plan_digest ?? "Plan not accepted"}</code></dd></div>
              <div><dt>Started</dt><dd>{formatDate(record.created_at)}</dd></div>
              <div><dt>Completed</dt><dd>{record.completed_at ? formatDate(record.completed_at) : "In progress"}</dd></div>
              <div><dt>Model usage</dt><dd>{usageLabel(record.usage)}</dd></div>
              <div><dt>Planner usage</dt><dd>{usageLabel(record.planner_usage)}</dd></div>
              <div><dt>Final result</dt><dd>{resultLabel(record.status, record.published_wiki_version_id)}</dd></div>
            </dl>
            {record.sanitized_error ? <p className="notice error" role="alert">{record.sanitized_error}</p> : null}
          </section>
          <section aria-labelledby="captured-sources-title" className="ledger-section">
            <div className="section-heading"><h2 id="captured-sources-title">Captured source revisions</h2><span>{record.sources.length}</span></div>
            {record.sources.length > 0 ? (
              <div className="table-wrap"><table><thead><tr><th>Source</th><th>Revision</th><th>Commit</th><th>Fingerprint</th></tr></thead><tbody>
                {record.sources.map((source) => <tr key={source.source_id}><th scope="row"><Link params={{ sourceId: source.source_id }} to="/sources/$sourceId">{source.source_id}</Link></th><td>{source.source_revision_id}</td><td><code>{source.commit}</code></td><td><code>{source.fingerprint}</code></td></tr>)}
              </tbody></table></div>
            ) : <EmptyState>No source revisions were captured.</EmptyState>}
          </section>
          <section aria-labelledby="captured-models-title" className="ledger-section">
            <div className="section-heading"><h2 id="captured-models-title">Captured model versions</h2><span>{record.models.length}</span></div>
            {record.models.length > 0 ? (
              <div className="table-wrap"><table><thead><tr><th>Role</th><th>Model profile</th><th>Profile version</th><th>Version record</th></tr></thead><tbody>
                {record.models.map((model) => <tr key={model.role}><th scope="row">{model.role.replaceAll("_", " ")}</th><td><Link params={{ profileId: model.model_profile_id }} to="/models/$profileId">{model.model_profile_id}</Link></td><td>{model.profile_version}</td><td>{model.model_profile_version_id}</td></tr>)}
              </tbody></table></div>
            ) : <EmptyState>No model versions were captured.</EmptyState>}
          </section>
          <section aria-labelledby="ordered-pages-title" className="ledger-section">
            <div className="section-heading"><h2 id="ordered-pages-title">Ordered page work</h2><span>{pages.length} pages</span></div>
            {pages.length > 0 ? (
              <div className="table-wrap"><table><thead><tr><th>Position</th><th>Page</th><th>Status</th><th>Attempts</th><th>Token usage</th><th>Job and result</th></tr></thead><tbody>
                {pages.map((page) => (
                  <tr key={page.id}>
                    <td>{page.position + 1}</td>
                    <th scope="row">{page.title}<small>{page.slug} · {page.purpose}</small></th>
                    <td><StatusBadge value={page.status} /></td>
                    <td>{page.attempt_count}</td>
                    <td>{usageLabel(page.usage)}</td>
                    <td><Link params={{ id: page.job_id }} to="/jobs/$id">Open job</Link>{page.sanitized_error ? <small className="error-copy">{page.sanitized_error}</small> : page.submission_digest ? <small>Submission {page.submission_digest}</small> : null}</td>
                  </tr>
                ))}
              </tbody></table></div>
            ) : <EmptyState>The page plan has not been accepted.</EmptyState>}
          </section>
        </>
      ) : null}
    </section>
  );
}

function shortId(value: string): string {
  return value.slice(0, 8);
}

function formatDate(value: string): string {
  return new Date(value).toLocaleString();
}

function usageLabel(usage: { model_calls: number; total_tokens: number }): string {
  return `${usage.total_tokens.toLocaleString()} tokens · ${usage.model_calls} ${usage.model_calls === 1 ? "call" : "calls"}`;
}

function resultLabel(status: string, wikiVersionId: string | null): string {
  if (wikiVersionId) return `Published wiki ${wikiVersionId}`;
  if (status === "no_op") return "No source or Claim changes; publication unchanged";
  if (status === "interrupted") return "Interrupted; prior publication preserved";
  if (status === "failed") return "Failed; prior publication preserved";
  return "Pending final validation";
}

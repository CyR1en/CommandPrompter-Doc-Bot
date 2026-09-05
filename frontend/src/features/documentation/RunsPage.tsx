import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";

import { listDocumentationRuns, listKnowledgeBases, safeErrorMessage } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { Select } from "../../app/Select";
import { EmptyState, ErrorNotice, StatusBadge } from "../../app/StatusBadge";

export function RunsPage(): ReactNode {
  const [knowledgeBaseId, setKnowledgeBaseId] = useState("");
  const knowledgeBases = useQuery({ queryKey: queryKeys.knowledgeBases, queryFn: listKnowledgeBases });
  const runs = useQuery({
    queryKey: [...queryKeys.runs, knowledgeBaseId || "all"],
    queryFn: () => listDocumentationRuns(knowledgeBaseId || undefined),
  });
  const knowledgeBaseNames = new Map(knowledgeBases.data?.map((item) => [item.id, item.name]));

  return (
    <section className="page">
      <header className="page-heading">
        <div><p className="eyebrow">documentation runs</p><h1>Generation ledger</h1></div>
        <label className="compact-field">
          Knowledge base
          <Select
            onChange={setKnowledgeBaseId}
            options={[{ label: "All knowledge bases", value: "" }, ...(knowledgeBases.data ?? []).map((item) => ({ label: item.name, value: item.id }))]}
            value={knowledgeBaseId}
          />
        </label>
      </header>
      <ErrorNotice message={runs.error ? safeErrorMessage(runs.error) : knowledgeBases.error ? safeErrorMessage(knowledgeBases.error) : null} />
      {runs.isPending ? <p aria-live="polite" className="notice">Loading documentation runs…</p> : null}
      {runs.data && runs.data.length > 0 ? (
        <div className="table-wrap">
          <table>
            <caption className="sr-only">Documentation runs matching the selected knowledge base</caption>
            <thead><tr><th>Knowledge base</th><th>Phase</th><th>Captured inputs</th><th>Pages</th><th>Updated</th></tr></thead>
            <tbody>
              {runs.data.map((run) => {
                const completedPages = run.pages.filter((page) => page.status === "complete").length;
                return (
                  <tr key={run.id}>
                    <th scope="row"><Link params={{ runId: run.id }} to="/runs/$runId">{knowledgeBaseNames.get(run.knowledge_base_id) ?? "Knowledge base"}</Link><small>{run.id}</small></th>
                    <td><StatusBadge value={run.status} /></td>
                    <td>{run.sources.length} sources · {run.models.length} models</td>
                    <td>{completedPages} / {run.pages.length}</td>
                    <td><time dateTime={run.updated_at}>{formatDate(run.updated_at)}</time></td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : null}
      {runs.data?.length === 0 ? <EmptyState>No documentation runs match this selection.</EmptyState> : null}
    </section>
  );
}

function formatDate(value: string): string {
  return new Date(value).toLocaleString();
}

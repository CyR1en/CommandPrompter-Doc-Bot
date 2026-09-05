import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { listModelProfiles, safeErrorMessage } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { EmptyState, ErrorNotice, StatusBadge } from "../../app/StatusBadge";

export function ModelsPage(): ReactNode {
  const models = useQuery({ queryKey: queryKeys.models, queryFn: () => listModelProfiles() });

  return (
    <section className="page">
      <header className="page-heading">
        <div><p className="eyebrow">model profiles</p><h1>Capability register</h1></div>
        <p className="lede">Discovered evidence, probe results, and operator settings remain visibly distinct.</p>
      </header>
      <ErrorNotice message={models.error ? safeErrorMessage(models.error) : null} />
      <div className="table-wrap">
        <table>
          <thead><tr><th>Model</th><th>Availability</th><th>Transport</th><th>Limits</th><th>Evidence version</th></tr></thead>
          <tbody>
            {models.data?.map((model) => (
              <tr key={model.id}>
                <th scope="row"><Link params={{ profileId: model.id }} to="/models/$profileId">{model.model_id}</Link><small>{model.endpoint_id}</small></th>
                <td><StatusBadge value={model.availability} /></td>
                <td>{model.current_version.settings.transport.replaceAll("_", " ")}</td>
                <td>{formatLimit(model.current_version.settings.context_window_tokens)} context · {formatLimit(model.current_version.settings.max_output_tokens)} output</td>
                <td>profile v{model.version} · settings v{model.current_version.version_number}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {models.data?.length === 0 ? <EmptyState>No model profiles are available. Open a provider endpoint to discover or add one.</EmptyState> : null}
    </section>
  );
}

function formatLimit(value: number | null): string {
  return value === null ? "unknown" : value.toLocaleString();
}

import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { listProviderEndpoints, safeErrorMessage } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { EmptyState, ErrorNotice, StatusBadge } from "../../app/StatusBadge";

export function ProvidersPage(): ReactNode {
  const providers = useQuery({
    queryKey: queryKeys.providers,
    queryFn: listProviderEndpoints,
  });

  return (
    <section className="page">
      <header className="page-heading">
        <div>
          <p className="eyebrow">provider endpoints</p>
          <h1>Model connections</h1>
        </div>
        <Link className="button primary" to="/providers/new">Set up endpoint</Link>
      </header>
      <ErrorNotice message={providers.error ? safeErrorMessage(providers.error) : null} />
      <div className="record-grid">
        {providers.data?.map((provider) => (
          <Link className="record-card provider-card" key={provider.id} params={{ endpointId: provider.id }} to="/providers/$endpointId">
            <div>
              <p className="eyebrow">Configuration {provider.configuration_version}</p>
              <h2>{provider.display_name}</h2>
              <p className="endpoint-url">{provider.base_url}</p>
            </div>
            <div className="status-stack">
              <StatusBadge value={provider.health} />
              <StatusBadge value={provider.lifecycle} />
            </div>
          </Link>
        ))}
      </div>
      {providers.data?.length === 0 ? (
        <EmptyState>No provider endpoints are configured. Start with a compatible base URL.</EmptyState>
      ) : null}
    </section>
  );
}

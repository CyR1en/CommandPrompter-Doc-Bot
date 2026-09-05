import {
  Navigate,
  Outlet,
  createRootRoute,
  createRoute,
  createRouter,
} from "@tanstack/react-router";
import type { ReactNode } from "react";

import { CredentialsPage } from "../features/credentials/CredentialsPage";
import { ChatAccessTokensPage } from "../features/credentials/ChatAccessTokensPage";
import { AgentConfigurationPage } from "../features/agents/AgentConfigurationPage";
import { AgentsPage } from "../features/agents/AgentsPage";
import { BootstrapPage, LoginPage } from "../features/auth/AccessPage";
import { RunDetailPage } from "../features/documentation/RunDetailPage";
import { RunsPage } from "../features/documentation/RunsPage";
import { WikiPage, parseWikiSearch } from "../features/documentation/WikiPage";
import { DiscordPage, type DiscordSearch } from "../features/discord/DiscordPage";
import { JobDetailPage } from "../features/jobs/JobDetailPage";
import { JobsPage } from "../features/jobs/JobsPage";
import { KnowledgeBaseDetailPage } from "../features/knowledge-bases/KnowledgeBaseDetailPage";
import { KnowledgeBasesPage } from "../features/knowledge-bases/KnowledgeBasesPage";
import { ModelDetailPage } from "../features/models/ModelDetailPage";
import { ModelsPage } from "../features/models/ModelsPage";
import { OverviewPage } from "../features/overview/OverviewPage";
import { NewProviderPage } from "../features/providers/NewProviderPage";
import { ProviderDetailPage } from "../features/providers/ProviderDetailPage";
import { ProvidersPage } from "../features/providers/ProvidersPage";
import { SettingsPage } from "../features/settings/SettingsPage";
import { NewSourcePage } from "../features/sources/NewSourcePage";
import { SourceDetailPage } from "../features/sources/SourceDetailPage";
import { SourcesPage } from "../features/sources/SourcesPage";
import { useAuth } from "./auth";
import { Shell } from "./Shell";

const rootRoute = createRootRoute({
  component: () => <Outlet />,
  notFoundComponent: NotFoundPage,
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  component: LoginPage,
});

const bootstrapRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/bootstrap",
  component: BootstrapPage,
});

const protectedRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "authenticated",
  component: AuthenticatedLayout,
});

const overviewRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/",
  component: OverviewPage,
});

const knowledgeBasesRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/knowledge-bases",
  component: KnowledgeBasesPage,
});

const knowledgeBaseRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/knowledge-bases/$id",
  component: () => {
    const { id } = knowledgeBaseRoute.useParams();
    return <KnowledgeBaseDetailPage id={id} />;
  },
});

const jobsRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/jobs",
  component: JobsPage,
});

const runsRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/runs",
  component: RunsPage,
});

const runRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/runs/$runId",
  component: () => {
    const { runId } = runRoute.useParams();
    return <RunDetailPage runId={runId} />;
  },
});

const wikiRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/wiki",
  validateSearch: parseWikiSearch,
  component: () => <WikiPage search={wikiRoute.useSearch()} />,
});

const agentsRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/agents",
  component: AgentsPage,
});

const newAgentRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/agents/new",
  component: () => <AgentConfigurationPage kind="create" />,
});

const agentRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/agents/$agentId",
  component: () => {
    const { agentId } = agentRoute.useParams();
    return <AgentConfigurationPage agentId={agentId} kind="detail" />;
  },
});

const discordRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/discord",
  validateSearch: parseDiscordSearch,
  component: () => <DiscordPage search={discordRoute.useSearch()} />,
});

const sourcesRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/sources",
  component: SourcesPage,
});

const newSourceRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/sources/new",
  component: NewSourcePage,
});

const sourceRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/sources/$sourceId",
  component: () => {
    const { sourceId } = sourceRoute.useParams();
    return <SourceDetailPage sourceId={sourceId} />;
  },
});

const providersRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/providers",
  component: ProvidersPage,
});

const newProviderRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/providers/new",
  component: NewProviderPage,
});

const providerRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/providers/$endpointId",
  component: () => {
    const { endpointId } = providerRoute.useParams();
    return <ProviderDetailPage endpointId={endpointId} />;
  },
});

const modelsRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/models",
  component: ModelsPage,
});

const modelRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/models/$profileId",
  component: () => {
    const { profileId } = modelRoute.useParams();
    return <ModelDetailPage profileId={profileId} />;
  },
});

const jobRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/jobs/$id",
  component: () => {
    const { id } = jobRoute.useParams();
    return <JobDetailPage id={id} />;
  },
});

const settingsRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/settings",
  component: SettingsPage,
});

const credentialsRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/settings/credentials",
  component: CredentialsPage,
});

const chatAccessTokensRoute = createRoute({
  getParentRoute: () => protectedRoute,
  path: "/settings/chat-access-tokens",
  component: ChatAccessTokensPage,
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  bootstrapRoute,
  protectedRoute.addChildren([
    overviewRoute,
    knowledgeBasesRoute,
    knowledgeBaseRoute,
    sourcesRoute,
    newSourceRoute,
    sourceRoute,
    providersRoute,
    newProviderRoute,
    providerRoute,
    modelsRoute,
    modelRoute,
    jobsRoute,
    jobRoute,
    runsRoute,
    runRoute,
    wikiRoute,
    agentsRoute,
    newAgentRoute,
    agentRoute,
    discordRoute,
    settingsRoute,
    credentialsRoute,
    chatAccessTokensRoute,
  ]),
]);

export const router = createRouter({ routeTree });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

function AuthenticatedLayout(): ReactNode {
  const { state } = useAuth();
  if (state.kind === "checking") {
    return <main className="loading-page"><p aria-live="polite">Opening control plane…</p></main>;
  }
  if (state.kind === "anonymous") return <Navigate to="/login" />;
  return <Shell />;
}

function NotFoundPage(): ReactNode {
  return (
    <main className="access-page">
      <section className="access-intro">
        <p className="eyebrow">Not found</p>
        <h1>That page is not part of this control plane.</h1>
        <p>Return to the operations overview.</p>
        <a className="button primary" href="/">Open overview</a>
      </section>
    </main>
  );
}

function parseDiscordSearch(search: Record<string, unknown>): DiscordSearch {
  const view = search.view === "connections" || search.view === "servers" || search.view === "bindings" || search.view === "health"
    ? search.view
    : undefined;
  return {
    agent_id: stringValue(search.agent_id),
    connection_id: stringValue(search.connection_id),
    server_id: stringValue(search.server_id),
    view,
  };
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined;
}

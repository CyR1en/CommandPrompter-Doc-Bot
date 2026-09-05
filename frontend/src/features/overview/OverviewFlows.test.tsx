import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRouter } from "@tanstack/react-router";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";

import type { OperationalOverview } from "../../api/client";
import { AuthProvider, useAuth } from "../../app/auth";
import { SettingsPage } from "../settings/SettingsPage";
import { OverviewPage } from "./OverviewPage";

const kbId = "00000000-0000-0000-0000-000000000010";
const sourceId = "00000000-0000-0000-0000-000000000020";
const jobId = "00000000-0000-0000-0000-000000000030";
const endpointId = "00000000-0000-0000-0000-000000000040";
const providerRunId = "00000000-0000-0000-0000-000000000050";
const agentId = "00000000-0000-0000-0000-000000000060";
const agentRunId = "00000000-0000-0000-0000-000000000070";
const now = "2026-08-28T12:00:00Z";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("overview presents every UI-002 attention category with operational links", async () => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/overview") return jsonResponse(overview());
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  renderAuthenticated(<OverviewPage />);

  expect(await screen.findByText(/Repository access failed\./)).toBeInTheDocument();
  expect(screen.getByText("Source sync failed.")).toBeInTheDocument();
  expect(screen.getByText("Published Wiki is behind current configuration or sources")).toBeInTheDocument();
  expect(screen.getByText("Provider authentication failed.")).toBeInTheDocument();
  expect(screen.getByText("Provider request timed out.")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: /Product repository/i })).toHaveAttribute("href", `/sources/${sourceId}`);
  expect(screen.getByRole("link", { name: /sync source/i })).toHaveAttribute("href", `/jobs/${jobId}`);
  expect(screen.getByRole("link", { name: /Primary provider/i })).toHaveAttribute("href", `/providers/${endpointId}`);
});

test("settings exposes a direct authenticated non-secret configuration download", async () => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  renderAuthenticated(<SettingsPage />);

  const download = await screen.findByRole("link", { name: /Download non-secret configuration/i });
  expect(download).toHaveAttribute("href", "/api/v1/settings/export");
  expect(download).toHaveAttribute("download", "ref0-configuration.json");
  expect(screen.getByText(/Credential values and encrypted material are excluded/i)).toBeInTheDocument();
});

function renderAuthenticated(page: ReactNode): void {
  function Gate(): ReactNode {
    const { state } = useAuth();
    return state.kind === "authenticated" ? page : <p>{state.kind}</p>;
  }
  const rootRoute = createRootRoute({ component: () => <AuthProvider><Gate /></AuthProvider> });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider>);
}

function overview(): OperationalOverview {
  return {
    failed_jobs: [{ attempt_count: 3, finished_at: now, id: jobId, job_type: "sync_source", max_attempts: 3, sanitized_error: "Source sync failed.", target_id: sourceId, target_type: "source", updated_at: now }],
    generated_at: now,
    knowledge_base_issues: [{ id: kbId, kind: "stale", name: "Product docs", published_wiki_id: "00000000-0000-0000-0000-000000000011", updated_at: now }],
    provider_errors: [{ endpoint_id: endpointId, endpoint_name: "Primary provider", occurred_at: now, operation: "discovery", run_id: providerRunId, sanitized_error: "Provider authentication failed." }],
	agent_failures: [{ agent_id: agentId, agent_key: "support", agent_version_number: 2, completed_at: now, created_at: now, display_name: "Support Agent", id: agentRunId, origin: "http", sanitized_error: "Provider request timed out." }],
    unhealthy_sources: [{ checked_at: now, display_name: "Product repository", id: sourceId, knowledge_base_id: kbId, knowledge_base_name: "Product docs", lifecycle: "active", sanitized_error: "Repository access failed." }],
  };
}

function session(): object {
  return { csrf_token: "csrf", expires_at: "2026-08-29T00:00:00Z", operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" } };
}

function jsonResponse(body: object): Response {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" }, status: 200 });
}

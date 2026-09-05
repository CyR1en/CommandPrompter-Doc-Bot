import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRouter } from "@tanstack/react-router";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";

import type { ModelProfile, ProviderEndpoint } from "../../api/client";
import { AuthProvider, useAuth } from "../../app/auth";
import { ProviderDetailPage, ProbeForm } from "./ProviderDetailPage";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("probe remains disabled until cost is explicitly acknowledged", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
    csrf_token: "csrf",
    expires_at: "2026-08-29T00:00:00Z",
    operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" },
  }), { status: 200, headers: { "Content-Type": "application/json" } })));
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderProbe(queryClient);
  const user = userEvent.setup();
  const button = await screen.findByRole("button", { name: "Enqueue probe" });
  expect(button).toBeDisabled();
  await user.click(screen.getByLabelText(/I understand this probe/));
  expect(button).toBeEnabled();
});

test("archived endpoint does not offer manual model mutation", async () => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === `/api/v1/provider-endpoints/${endpoint().id}`) return jsonResponse(endpoint("archived"));
    if (path === "/api/v1/credentials" || path === "/api/v1/model-profiles") return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderPage(queryClient, <ProviderDetailPage endpointId={endpoint().id} />);

  expect(await screen.findByText("Reactivate this endpoint before adding a manual model.")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Create manual model" })).not.toBeInTheDocument();
});

test("discovery retry clears a stale failure before showing the successful live job", async () => {
  let discoveryAttempts = 0;
  let finishRetry: ((response: Response) => void) | undefined;
  const retryResponse = new Promise<Response>((resolve) => {
    finishRetry = resolve;
  });
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === `/api/v1/provider-endpoints/${endpoint().id}` && request.method === "GET") return jsonResponse(endpoint());
    if (path === "/api/v1/credentials" || (path === "/api/v1/model-profiles" && request.method === "GET")) return jsonResponse([]);
    if (path === `/api/v1/provider-endpoints/${endpoint().id}/discover` && request.method === "POST") {
      discoveryAttempts += 1;
      return discoveryAttempts === 1 ? problemResponse(500) : retryResponse;
    }
    if (path === `/api/v1/jobs/${jobId}`) return jsonResponse(job());
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } });
  renderPage(queryClient, <ProviderDetailPage endpointId={endpoint().id} />);
  const user = userEvent.setup();
  const discover = await screen.findByRole("button", { name: "Discover models" });

  await user.click(discover);
  expect(await screen.findByRole("alert")).toHaveTextContent("The operation could not be completed.");
  await user.click(discover);
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();

  if (!finishRetry) throw new Error("Discovery retry resolver was not initialized");
  finishRetry(jsonResponse(discoveryRun()));
  expect(await screen.findByText(/Discovery job status: pending/)).toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});

function renderProbe(queryClient: QueryClient): void {
  renderPage(queryClient, <ProbeForm endpoint={endpoint()} profiles={[profile()]} />);
}

function renderPage(queryClient: QueryClient, page: ReactNode): void {
  function Gate(): ReactNode {
    const { state } = useAuth();
    return state.kind === "authenticated" ? page : <p>{state.kind}</p>;
  }
  const rootRoute = createRootRoute({ component: Gate });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  render(<QueryClientProvider client={queryClient}><AuthProvider><RouterProvider router={router} /></AuthProvider></QueryClientProvider>);
}

function endpoint(lifecycle: "active" | "archived" = "active"): ProviderEndpoint {
  return {
    allow_http: false,
    allow_private_network: false,
    archived_at: lifecycle === "archived" ? "2026-08-28T01:00:00Z" : null,
    base_url: "https://models.example/v1",
    chat_completions_path: "chat/completions",
    configuration_version: 1,
    created_at: "2026-08-28T00:00:00Z",
    credential_id: null,
    display_name: "Models",
    headers: {},
    health: "unknown",
    health_checked_at: null,
    id: "00000000-0000-0000-0000-000000000020",
    lifecycle,
    models_path: "models",
    responses_path: "responses",
    updated_at: "2026-08-28T00:00:00Z",
    version: 1,
  };
}

function session(): object {
  return {
    csrf_token: "csrf",
    expires_at: "2026-08-29T00:00:00Z",
    operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" },
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function problemResponse(status: number): Response {
  return new Response(JSON.stringify({ detail: "private discovery detail" }), {
    status,
    headers: { "Content-Type": "application/problem+json" },
  });
}

const jobId = "00000000-0000-0000-0000-000000000040";

function discoveryRun(): object {
  return {
    authentication_succeeded: null,
    captured_configuration_version: 1,
    captured_credential_version: null,
    completed_at: null,
    created_at: "2026-08-28T00:00:00Z",
    endpoint_id: endpoint().id,
    http_status: null,
    id: "00000000-0000-0000-0000-000000000041",
    job_id: jobId,
    model_count: null,
    model_ids: [],
    raw_response: null,
    requested_by_operator_id: "00000000-0000-0000-0000-000000000001",
    response_sha256: null,
    sanitized_error: null,
    started_at: null,
    status: "pending",
    tls_required: true,
    tls_verified: null,
  };
}

function job(): object {
  return {
    attempt_count: 0,
    created_at: "2026-08-28T00:00:00Z",
    finished_at: null,
    id: jobId,
    job_type: "discover_endpoint",
    lease_expires_at: null,
    lease_generation: 0,
    lease_owner: null,
    max_attempts: 3,
    not_before: null,
    progress: 0,
    result: null,
    sanitized_error: null,
    started_at: null,
    status: "pending",
    target_id: endpoint().id,
    target_type: "provider_endpoint",
    updated_at: "2026-08-28T00:00:00Z",
  };
}

function profile(): ModelProfile {
  return {
    availability: "manual",
    created_at: "2026-08-28T00:00:00Z",
    current_version: {
      configuration_version: 1,
      created_at: "2026-08-28T00:00:00Z",
      created_by_operator_id: null,
      id: "00000000-0000-0000-0000-000000000031",
      settings: {
        context_window_tokens: 16_000,
        extra_body: {},
        max_output_tokens: 2_000,
        max_retries: 2,
        max_concurrent_tasks: 1,
        metadata_origin: {},
        reasoning_mapping: null,
        reasoning_transport: "none",
        supports_streaming: null,
        supports_structured_output: null,
        supports_temperature: null,
        supports_tools: null,
        timeout_seconds: 60,
        transport: "chat_completions",
      },
      source: "operator",
      version_number: 1,
    },
    endpoint_id: endpoint().id,
    id: "00000000-0000-0000-0000-000000000030",
    model_id: "probe-model",
    updated_at: "2026-08-28T00:00:00Z",
    version: 1,
  };
}

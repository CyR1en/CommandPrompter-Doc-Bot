import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRouter } from "@tanstack/react-router";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";

import type { ModelProfile, ProviderEndpoint } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { AuthProvider, useAuth } from "../../app/auth";
import { ModelDetailPage, ModelEvidence } from "./ModelDetailPage";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("metadata renders explicit values and distinct textual provenance labels", () => {
  render(<ModelEvidence profile={profile()} />);

  expect(screen.getByText("Unsupported")).toBeInTheDocument();
  expect(screen.getAllByText("Unknown / not established").length).toBeGreaterThan(0);
  expect(screen.getAllByText("Operator supplied").length).toBeGreaterThan(0);
  expect(screen.getAllByText("Discovered evidence").length).toBeGreaterThan(0);
  expect(screen.getAllByText("Probe evidence").length).toBeGreaterThan(0);
  expect(screen.getAllByText("Unknown / unverified").length).toBeGreaterThan(0);
});

test("model edit key survives a retry but changes after an edit and a newer profile version", async () => {
  let currentProfile = profile(2);
  const patchKeys: string[] = [];
  const patchBodies: Array<Record<string, unknown>> = [];
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === `/api/v1/model-profiles/${currentProfile.id}` && request.method === "GET") return jsonResponse(currentProfile);
    if (path === `/api/v1/provider-endpoints/${currentProfile.endpoint_id}`) return jsonResponse(endpoint("active"));
    if (path === `/api/v1/model-profiles/${currentProfile.id}` && request.method === "PATCH") {
      patchKeys.push(request.headers.get("Idempotency-Key") ?? "");
      patchBodies.push(await request.clone().json() as Record<string, unknown>);
      return jsonResponse({}, 500);
    }
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const queryClient = renderDetail();
  const user = userEvent.setup();
  const submit = await screen.findByRole("button", { name: "Append settings version" });

  await user.click(submit);
  await waitFor(() => expect(patchKeys).toHaveLength(1));
  expect((patchBodies[0]?.settings as Record<string, unknown> | undefined)?.max_concurrent_tasks).toBe(2);
  await user.click(submit);
  await waitFor(() => expect(patchKeys).toHaveLength(2));
  expect(patchKeys[1]).toBe(patchKeys[0]);

  const concurrency = screen.getByRole("spinbutton", { name: /Maximum concurrent tasks/ });
  await user.clear(concurrency);
  await user.type(concurrency, "3");
  await user.click(submit);
  await waitFor(() => expect(patchKeys).toHaveLength(3));
  expect(patchKeys[2]).not.toBe(patchKeys[1]);
  expect((patchBodies[2]?.settings as Record<string, unknown> | undefined)?.max_concurrent_tasks).toBe(3);

  currentProfile = profile(3);
  await queryClient.invalidateQueries({ queryKey: [...queryKeys.models, currentProfile.id] });
  await screen.findByText("Optimistic edit · current profile version 3");
  await user.click(screen.getByRole("button", { name: "Append settings version" }));
  await waitFor(() => expect(patchKeys).toHaveLength(4));
  expect(patchKeys[3]).not.toBe(patchKeys[2]);
});

test("archived endpoint prevents model profile mutation", async () => {
  const currentProfile = profile(2);
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === `/api/v1/model-profiles/${currentProfile.id}`) return jsonResponse(currentProfile);
    if (path === `/api/v1/provider-endpoints/${currentProfile.endpoint_id}`) return jsonResponse(endpoint("archived"));
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  renderDetail();

  expect(await screen.findByText("Reactivate the provider endpoint before editing this model profile.")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Append settings version" })).not.toBeInTheDocument();
});

function renderDetail(): QueryClient {
  const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } });
  function Gate(): ReactNode {
    const { state } = useAuth();
    return state.kind === "authenticated" ? <ModelDetailPage profileId={profile().id} /> : <p>{state.kind}</p>;
  }
  const rootRoute = createRootRoute({ component: Gate });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  render(<QueryClientProvider client={queryClient}><AuthProvider><RouterProvider router={router} /></AuthProvider></QueryClientProvider>);
  return queryClient;
}

function profile(version = 2): ModelProfile {
  return {
    availability: "available",
    created_at: "2026-08-28T00:00:00Z",
    current_version: {
      configuration_version: 1,
      created_at: "2026-08-28T00:00:00Z",
      created_by_operator_id: null,
      id: `00000000-0000-0000-0000-${String(version).padStart(12, "0")}`,
      settings: {
        context_window_tokens: 32_000,
        extra_body: {},
        max_output_tokens: 4_000,
        max_retries: 2,
        max_concurrent_tasks: 2,
        metadata_origin: {
          context_window_tokens: "discovered",
          extra_body: "operator",
          max_output_tokens: "discovered",
          max_retries: "operator",
          max_concurrent_tasks: "operator",
          model_id: "discovered",
          reasoning_mapping: "unknown",
          reasoning_transport: "operator",
          supports_streaming: "unknown",
          supports_structured_output: "probed",
          supports_temperature: "unknown",
          supports_tools: "probed",
          timeout_seconds: "operator",
          transport: "discovered",
        },
        reasoning_mapping: null,
        reasoning_transport: "none",
        supports_streaming: null,
        supports_structured_output: true,
        supports_temperature: null,
        supports_tools: false,
        timeout_seconds: 60,
        transport: "chat_completions",
      },
      source: "probe",
      version_number: version,
    },
    endpoint_id: "00000000-0000-0000-0000-000000000002",
    id: "00000000-0000-0000-0000-000000000001",
    model_id: "folio-model",
    updated_at: "2026-08-28T00:00:00Z",
    version,
  };
}

function endpoint(lifecycle: "active" | "archived"): ProviderEndpoint {
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
    id: "00000000-0000-0000-0000-000000000002",
    lifecycle,
    models_path: "models",
    responses_path: "responses",
    updated_at: "2026-08-28T00:00:00Z",
    version: 1,
  };
}

function session(): object {
  return {
    csrf_token: "csrf-model-detail",
    expires_at: "2026-08-29T00:00:00Z",
    operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" },
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

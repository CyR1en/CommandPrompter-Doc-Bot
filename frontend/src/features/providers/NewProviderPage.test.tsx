import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRouter } from "@tanstack/react-router";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";

import { AuthProvider, useAuth } from "../../app/auth";
import { NewProviderPage } from "./NewProviderPage";

const csrfSentinel = "csrf-provider-cache-sentinel";
const apiKeySentinel = "provider-api-key-cache-sentinel";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("inline API key is cleared and never enters TanStack mutation variables", async () => {
  const requests: Request[] = [];
  const fetchMock = vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/credentials" && request.method === "GET") return jsonResponse([]);
    if (path === "/api/v1/credentials" && request.method === "POST") return jsonResponse(credential());
    if (path === "/api/v1/provider-endpoints" && request.method === "POST") return jsonResponse(endpoint());
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const user = userEvent.setup();
  renderPage(queryClient);

  await user.type(await screen.findByLabelText("Display name"), "Local inference");
  await user.type(screen.getByLabelText("Base URL"), "https://models.example/v1");
  await user.click(screen.getByLabelText("Add a write-only API key"));
  await user.type(screen.getByLabelText("Credential label"), "Local provider key");
  await user.type(screen.getByLabelText("API key"), apiKeySentinel);
  await user.click(screen.getByRole("button", { name: "Create endpoint" }));

  expect(await screen.findByRole("heading", { name: "Endpoint saved" })).toBeInTheDocument();
  expect(screen.queryByDisplayValue(apiKeySentinel)).not.toBeInTheDocument();
  const credentialRequest = requests.find((request) => new URL(request.url).pathname === "/api/v1/credentials" && request.method === "POST");
  expect(credentialRequest).toBeDefined();
  expect(await credentialRequest?.text()).toContain(apiKeySentinel);

  const variables = queryClient.getMutationCache().getAll().map((mutation) => mutation.state.variables);
  const retained = JSON.stringify(variables);
  expect(retained).not.toContain(apiKeySentinel);
  expect(retained).not.toContain(csrfSentinel);
});

test.each([
  ["Broken header", "Each non-secret header needs a name and value."],
  ["Authorization: private", "Authentication and transport-control headers are not allowed here."],
])("local header validation keeps its fixed actionable message", async (header, expectedMessage) => {
  const fetchMock = vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/credentials" && request.method === "GET") return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const user = userEvent.setup();
  renderPage(queryClient);

  await user.type(await screen.findByLabelText("Display name"), "Local inference");
  await user.type(screen.getByLabelText("Base URL"), "https://models.example/v1");
  await user.click(screen.getByLabelText("No credential"));
  await user.type(screen.getByRole("textbox", { name: /Optional non-secret headers/ }), header);
  await user.click(screen.getByRole("button", { name: "Create endpoint" }));

  expect(await screen.findByRole("alert")).toHaveTextContent(expectedMessage);
  expect(fetchMock.mock.calls.some(([request]) => request.method === "POST")).toBe(false);
});

function renderPage(queryClient: QueryClient): void {
  function Gate(): ReactNode {
    const { state } = useAuth();
    return state.kind === "authenticated" ? <NewProviderPage /> : <p>{state.kind}</p>;
  }
  const rootRoute = createRootRoute({ component: Gate });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  render(<QueryClientProvider client={queryClient}><AuthProvider><RouterProvider router={router} /></AuthProvider></QueryClientProvider>);
}

function session(): object {
  return {
    csrf_token: csrfSentinel,
    expires_at: "2026-08-29T00:00:00Z",
    operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" },
  };
}

function credential(): object {
  return {
    created_at: "2026-08-28T00:00:00Z",
    id: "00000000-0000-0000-0000-000000000020",
    key_id: "active",
    kind: "provider_api_key",
    label: "Local provider key",
    masked_value: "••••",
    rotated_at: null,
    secret_version: 1,
  };
}

function endpoint(): object {
  return {
    allow_http: false,
    allow_private_network: false,
    archived_at: null,
    base_url: "https://models.example/v1",
    chat_completions_path: "chat/completions",
    configuration_version: 1,
    created_at: "2026-08-28T00:00:00Z",
    credential_id: "00000000-0000-0000-0000-000000000020",
    display_name: "Local inference",
    headers: {},
    health: "unknown",
    health_checked_at: null,
    id: "00000000-0000-0000-0000-000000000030",
    lifecycle: "active",
    models_path: "models",
    responses_path: "responses",
    updated_at: "2026-08-28T00:00:00Z",
    version: 1,
  };
}

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

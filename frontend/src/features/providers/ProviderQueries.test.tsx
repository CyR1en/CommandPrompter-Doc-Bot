import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRouter } from "@tanstack/react-router";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";

import { AuthProvider, useAuth } from "../../app/auth";
import { ProviderDetailPage } from "./ProviderDetailPage";
import { ProvidersPage } from "./ProvidersPage";

const endpointId = "00000000-0000-0000-0000-000000000020";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("provider register exposes a sanitized accessible query error", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => problemResponse(500)));
  renderWithRouter(<ProvidersPage />);

  expect(await screen.findByRole("alert")).toHaveTextContent("The operation could not be completed.");
});

test("provider detail exposes a sanitized accessible query error", async () => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/credentials" || path === "/api/v1/model-profiles") return jsonResponse([]);
    if (path === `/api/v1/provider-endpoints/${endpointId}`) return problemResponse(500);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));

  function Gate(): ReactNode {
    const { state } = useAuth();
    return state.kind === "authenticated" ? <ProviderDetailPage endpointId={endpointId} /> : <p>{state.kind}</p>;
  }
  renderWithRouter(<AuthProvider><Gate /></AuthProvider>);

  expect(await screen.findByRole("alert")).toHaveTextContent("The operation could not be completed.");
});

function renderWithRouter(page: ReactNode): void {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: () => page });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  render(<QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider>);
}

function session(): object {
  return {
    csrf_token: "csrf-provider-query",
    expires_at: "2026-08-29T00:00:00Z",
    operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" },
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function problemResponse(status: number): Response {
  return new Response(JSON.stringify({ detail: "private backend detail" }), {
    status,
    headers: { "Content-Type": "application/problem+json" },
  });
}

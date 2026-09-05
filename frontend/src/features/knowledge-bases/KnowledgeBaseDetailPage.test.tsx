import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";

import type { KnowledgeBase } from "../../api/client";
import { AuthProvider, useAuth } from "../../app/auth";
import { KnowledgeBaseDetailPage } from "./KnowledgeBaseDetailPage";

const knowledgeBaseId = "00000000-0000-0000-0000-000000000010";
const csrfSentinel = "csrf-must-not-enter-mutation-cache";
const purgeJobId = "00000000-0000-0000-0000-000000000099";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("TanStack mutation variables never retain the CSRF token", async () => {
  const original = knowledgeBase("Original name", 1);
  const updated = knowledgeBase("Updated name", 2);
  const fetchMock = vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") {
      return jsonResponse({
        operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" },
        expires_at: "2026-08-29T00:00:00Z",
        csrf_token: csrfSentinel,
      });
    }
    if (path === `/api/v1/knowledge-bases/${knowledgeBaseId}`) {
      return jsonResponse(request.method === "PATCH" ? updated : original);
    }
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });

  function Gate(): ReactNode {
    const { state } = useAuth();
    return state.kind === "authenticated"
      ? <KnowledgeBaseDetailPage id={knowledgeBaseId} />
      : <p>{state.kind}</p>;
  }

  const rootRoute = createRootRoute({ component: Gate });
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  render(
    <QueryClientProvider client={queryClient}>
      <AuthProvider><RouterProvider router={router} /></AuthProvider>
    </QueryClientProvider>,
  );

  const user = userEvent.setup();
  const name = await screen.findByLabelText("Name");
  await user.clear(name);
  await user.type(name, "Updated name");
  await user.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() => {
    expect(fetchMock.mock.calls.some((call) => call[0].method === "PATCH")).toBe(true);
  });

  const mutationVariables = queryClient
    .getMutationCache()
    .getAll()
    .map((mutation) => mutation.state.variables);
  expect(JSON.stringify(mutationVariables)).not.toContain(csrfSentinel);
});

test("scheduled deletion exposes the purge job returned by the API", async () => {
  let deleted = false;
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") {
      return jsonResponse({
        operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" },
        expires_at: "2026-08-29T00:00:00Z",
        csrf_token: csrfSentinel,
      });
    }
    if (path === `/api/v1/knowledge-bases/${knowledgeBaseId}` && request.method === "GET") {
      return jsonResponse({
        ...knowledgeBase("Product docs", deleted ? 2 : 1),
        lifecycle: deleted ? "pending_delete" : "active",
      });
    }
    if (path === `/api/v1/knowledge-bases/${knowledgeBaseId}` && request.method === "DELETE") {
      deleted = true;
      return jsonResponse({
        ...knowledgeBase("Product docs", 2),
        job_id: purgeJobId,
        lifecycle: "pending_delete",
      }, 202);
    }
    if (path === `/api/v1/jobs/${purgeJobId}`) {
      return jsonResponse({
        id: purgeJobId,
        job_type: "purge_knowledge_base",
        status: "pending",
        target_type: "knowledge_base",
        target_id: knowledgeBaseId,
        attempt_count: 0,
        max_attempts: 3,
        sanitized_error: null,
        not_before: "2026-08-31T00:00:00Z",
        available_at: "2026-08-31T00:00:00Z",
        lease_expires_at: null,
        created_at: "2026-08-28T00:00:00Z",
        updated_at: "2026-08-28T00:00:00Z",
        completed_at: null,
      });
    }
    if (path.endsWith("/model-assignments") || path === "/api/v1/model-profiles") {
      return jsonResponse([]);
    }
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });

  function Gate(): ReactNode {
    const { state } = useAuth();
    return state.kind === "authenticated"
      ? <KnowledgeBaseDetailPage id={knowledgeBaseId} />
      : <p>{state.kind}</p>;
  }

  const rootRoute = createRootRoute({ component: Gate });
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  render(
    <QueryClientProvider client={queryClient}>
      <AuthProvider><RouterProvider router={router} /></AuthProvider>
    </QueryClientProvider>,
  );

  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Schedule deletion" }));
  const dialog = screen.getByRole("dialog");
  await user.type(within(dialog).getByLabelText(/Type Product docs exactly/i), "Product docs");
  await user.click(within(dialog).getByRole("button", { name: "Schedule deletion" }));

  expect(await screen.findByText(/Knowledge base purge job status: pending/i)).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Open job" })).toHaveAttribute("href", `/jobs/${purgeJobId}`);
});

function knowledgeBase(name: string, version: number): KnowledgeBase {
  return {
    id: knowledgeBaseId,
    name,
    access: "restricted",
    lifecycle: "active",
    instructions: "",
    language: "en",
    published_wiki_id: null,
    archived_at: null,
    delete_requested_at: null,
    purge_after: null,
    deleted_at: null,
    created_at: "2026-08-28T00:00:00Z",
    updated_at: "2026-08-28T00:00:00Z",
    version,
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

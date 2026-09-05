import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRouter } from "@tanstack/react-router";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";

import { queryKeys } from "../../api/queries";
import { JobEnqueueNotice } from "./JobEnqueueNotice";

const jobId = "00000000-0000-0000-0000-000000000040";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("enqueued action renders the live job status and follows job invalidation", async () => {
  let status: "pending" | "succeeded" = "pending";
  vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(job(status))));
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: () => <JobEnqueueNotice jobId={jobId} label="Discovery" /> });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  render(<QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider>);

  expect(await screen.findByText(/Discovery job status: pending/)).toBeInTheDocument();
  status = "succeeded";
  await queryClient.invalidateQueries({ queryKey: queryKeys.jobs });
  expect(await screen.findByText(/Discovery job status: succeeded/)).toBeInTheDocument();
});

function job(status: "pending" | "succeeded"): object {
  return {
    attempt_count: status === "succeeded" ? 1 : 0,
    created_at: "2026-08-28T00:00:00Z",
    finished_at: status === "succeeded" ? "2026-08-28T00:01:00Z" : null,
    id: jobId,
    job_type: "discover_endpoint",
    lease_expires_at: null,
    lease_generation: 0,
    lease_owner: null,
    max_attempts: 3,
    not_before: null,
    progress: status === "succeeded" ? 100 : 0,
    result: null,
    sanitized_error: null,
    started_at: null,
    status,
    target_id: "00000000-0000-0000-0000-000000000020",
    target_type: "provider_endpoint",
    updated_at: "2026-08-28T00:00:00Z",
  };
}

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

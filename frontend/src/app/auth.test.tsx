import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";

import { ApiError, listCredentials } from "../api/client";
import { clearEventStreamCursor, connectEventStream } from "../api/events";
import { AuthProvider } from "./auth";
import { router } from "./router";

class FakeEventSource {
  static latest: FakeEventSource | undefined;
  readonly listeners = new Map<string, Set<EventListener>>();
  readonly url: string;
  closed = false;

  constructor(url: string) {
    this.url = url;
    FakeEventSource.latest = this;
  }

  addEventListener(name: string, listener: EventListener): void {
    const listeners = this.listeners.get(name) ?? new Set<EventListener>();
    listeners.add(listener);
    this.listeners.set(name, listeners);
  }

  removeEventListener(name: string, listener: EventListener): void {
    this.listeners.get(name)?.delete(listener);
  }

  emit(name: string, data: string, lastEventId = ""): void {
    const event = new MessageEvent(name, { data, lastEventId });
    for (const listener of this.listeners.get(name) ?? []) listener(event);
  }

  close(): void {
    this.closed = true;
  }
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  clearEventStreamCursor();
  FakeEventSource.latest = undefined;
});

test("a protected 401 clears cache, leaves the authenticated view, and closes SSE", async () => {
  let expired = false;
  const fetchMock = vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") {
      return jsonResponse({
        operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" },
        expires_at: "2026-08-29T00:00:00Z",
        csrf_token: "csrf-memory-only",
      });
    }
    if (path === "/api/v1/credentials") {
      return expired
        ? jsonResponse({ title: "Authentication required", status: 401 }, 401)
        : jsonResponse([]);
    }
    if (path === "/api/v1/jobs" || path === "/api/v1/knowledge-bases") {
      return jsonResponse([]);
    }
    if (path === "/health/ready") return jsonResponse({ ready: true });
    throw new Error(`Unexpected test request: ${path}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal("EventSource", FakeEventSource);
  seedEventCursor("41");
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(["seeded-protected-data"], { present: true });

  render(
    <QueryClientProvider client={queryClient}>
      <AuthProvider><RouterProvider router={router} /></AuthProvider>
    </QueryClientProvider>,
  );

  expect(await screen.findByRole("heading", { name: "Today’s operations ledger" })).toBeVisible();
  const source = FakeEventSource.latest;
  if (source === undefined) throw new Error("event stream was not opened");
  expired = true;
  let caught: unknown;
  await act(async () => {
    try {
      await listCredentials();
    } catch (error: unknown) {
      caught = error;
    }
  });

  expect(caught).toBeInstanceOf(ApiError);
  expect(await screen.findByRole("heading", { name: "Sign in" })).toBeVisible();
  expect(queryClient.getQueryCache().getAll()).toHaveLength(0);
  expect(source.closed).toBe(true);
  expect(reconstructedEventURL()).toBe("/api/v1/events");
});

test("explicit logout closes SSE and clears the resume cursor", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") {
      return jsonResponse({
        operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" },
        expires_at: "2026-08-30T23:00:00Z",
        csrf_token: "csrf-memory-only",
      });
    }
    if (path === "/api/v1/auth/logout" && request.method === "POST") {
      return new Response(null, { status: 204 });
    }
    if (path === "/api/v1/jobs" || path === "/api/v1/knowledge-bases") return jsonResponse([]);
    if (path === "/health/ready") return jsonResponse({ ready: true });
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  vi.stubGlobal("EventSource", FakeEventSource);
  seedEventCursor("41");
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const user = userEvent.setup();

  render(
    <QueryClientProvider client={queryClient}>
      <AuthProvider><RouterProvider router={router} /></AuthProvider>
    </QueryClientProvider>,
  );

  expect(await screen.findByRole("heading", { name: "Today’s operations ledger" })).toBeVisible();
  const source = FakeEventSource.latest;
  if (source === undefined) throw new Error("event stream was not opened");
  await act(async () => { await router.navigate({ to: "/settings" }); });
  await user.click(await screen.findByRole("button", { name: "Sign out" }));

  expect(await screen.findByRole("heading", { name: "Sign in" })).toBeVisible();
  expect(requests.some((request) => new URL(request.url).pathname === "/api/v1/auth/logout" && request.method === "POST")).toBe(true);
  expect(source.closed).toBe(true);
  expect(reconstructedEventURL()).toBe("/api/v1/events");
});

function seedEventCursor(cursor: string): void {
  const disconnect = connectEventStream(new QueryClient());
  const source = FakeEventSource.latest;
  if (source === undefined) throw new Error("event stream was not opened");
  source.emit("credential.rotated", JSON.stringify({ id: "credential-id" }), cursor);
  disconnect();
}

function reconstructedEventURL(): string {
  const disconnect = connectEventStream(new QueryClient());
  const url = FakeEventSource.latest?.url;
  disconnect();
  if (url === undefined) throw new Error("event stream was not reconstructed");
  return url;
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

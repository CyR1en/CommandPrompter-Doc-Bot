import { afterEach, expect, test, vi } from "vitest";

import { createCredential, createKnowledgeBase, logout } from "./client";

afterEach(() => {
  vi.unstubAllGlobals();
});

test("authenticated mutations carry CSRF and one action id", async () => {
  const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(JSON.stringify({
    id: "00000000-0000-0000-0000-000000000001",
    name: "Handbook",
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
    version: 1,
  }), { status: 201, headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetchMock);

  await createKnowledgeBase({
    access: "restricted",
    csrfToken: "csrf-value",
    idempotencyKey: "00000000-0000-4000-8000-000000000002",
    instructions: "",
    language: "en",
    name: "Handbook",
  });

  const request = fetchMock.mock.calls[0]?.[0];
  expect(request).toBeInstanceOf(Request);
  if (!(request instanceof Request)) throw new Error("expected Request");
  expect(request.headers.get("X-CSRF-Token")).toBe("csrf-value");
  expect(request.headers.get("Idempotency-Key")).toBe("00000000-0000-4000-8000-000000000002");
  expect(request.credentials).toBe("include");
});

test("credential writes send the secret only in the request body", async () => {
  const sentinel = "secret-high-entropy-sentinel";
  const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(JSON.stringify({
    id: "00000000-0000-0000-0000-000000000003",
    kind: "provider_api_key",
    label: "Provider",
    masked_value: "••••",
    secret_version: 1,
    key_id: "active",
    created_at: "2026-08-28T00:00:00Z",
    rotated_at: null,
  }), { status: 201, headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetchMock);

  const result = await createCredential({
    csrfToken: "csrf",
    idempotencyKey: "00000000-0000-4000-8000-000000000004",
    kind: "provider_api_key",
    label: "Provider",
    secret: sentinel,
  });

  expect(JSON.stringify(result)).not.toContain(sentinel);
  expect(result.masked_value).toBe("••••");
  const request = fetchMock.mock.calls[0]?.[0];
  if (!(request instanceof Request)) throw new Error("expected Request");
  expect(await request.clone().text()).toContain(sentinel);
  expect(request.url).not.toContain(sentinel);
});

test("logout sends the in-memory CSRF token without a body", async () => {
  const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetchMock);
  await logout("csrf-only-in-memory");
  const request = fetchMock.mock.calls[0]?.[0];
  if (!(request instanceof Request)) throw new Error("expected Request");
  expect(request.headers.get("X-CSRF-Token")).toBe("csrf-only-in-memory");
  expect(await request.clone().text()).toBe("");
});

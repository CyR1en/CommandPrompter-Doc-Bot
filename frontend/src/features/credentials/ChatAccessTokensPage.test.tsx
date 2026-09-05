import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRouter } from "@tanstack/react-router";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";

import type { Agent, ChatAccessToken, ChatAccessTokenScopePreview, ChatAccessTokenSummary, IssuedChatAccessToken, KnowledgeBase } from "../../api/client";
import { AuthProvider, useAuth } from "../../app/auth";
import { ChatAccessTokensPage } from "./ChatAccessTokensPage";

const firstAgentId = "00000000-0000-0000-0000-000000000010";
const secondAgentId = "00000000-0000-0000-0000-000000000011";
const firstKnowledgeId = "00000000-0000-0000-0000-000000000020";
const secondKnowledgeId = "00000000-0000-0000-0000-000000000021";
const tokenId = "00000000-0000-0000-0000-000000000030";
const csrfSentinel = "chat-token-csrf-sentinel";
const secretSentinel = "ref0_chat_secret_once_sentinel";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("preview is advisory and unprotected, issuance uses exactly reviewed Agents, and dismiss destroys plaintext", async () => {
  const requests: Request[] = [];
  let issued = false;
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/auth/session") return jsonResponse(session());
    if (url.pathname === "/api/v1/agents") return jsonResponse({ items: [agent(firstAgentId, "First Agent", "first-agent"), agent(secondAgentId, "Second Agent", "second-agent")] });
    if (url.pathname === "/api/v1/knowledge-bases") return jsonResponse([knowledgeBase(firstKnowledgeId, "Alpha docs"), knowledgeBase(secondKnowledgeId, "Beta docs")]);
    if (url.pathname === "/api/v1/chat-access-tokens/preview") return jsonResponse({
      ...preview(),
      agent_scopes: preview().agent_scopes.map((scope, index) => index === 0 ? { ...scope, ready: false } : scope),
      ready: false,
    });
    if (url.pathname === "/api/v1/chat-access-tokens" && request.method === "POST") {
      issued = true;
      return jsonResponse(issuedToken(), 201);
    }
    if (url.pathname === "/api/v1/chat-access-tokens") return jsonResponse({ items: issued ? [tokenSummary()] : [] });
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  const client = testQueryClient();
  const user = userEvent.setup();
  const rendered = renderAuthenticated(client, <ChatAccessTokensPage />);

  await user.type(await screen.findByLabelText("Token label"), "Open WebUI");
  await user.click(screen.getByRole("checkbox", { name: /First Agent/ }));
  await user.click(screen.getByRole("checkbox", { name: /Second Agent/ }));
  await user.click(screen.getByRole("button", { name: "Review token scope" }));
  expect(await screen.findByRole("heading", { name: "Review chat access token" })).toBeInTheDocument();
  expect(screen.getByText("Current effective scope")).toBeInTheDocument();
  expect(screen.getByText("One or more Agents unavailable")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Issue token" })).toBeEnabled();

  const previewRequest = findRequest(requests, "/api/v1/chat-access-tokens/preview", "POST");
  expect(await previewRequest.json()).toEqual({ agent_ids: [firstAgentId, secondAgentId] });
  expect(previewRequest.headers.has("X-CSRF-Token")).toBe(false);
  expect(previewRequest.headers.has("Idempotency-Key")).toBe(false);

  await user.click(screen.getByRole("button", { name: "Issue token" }));
  const secretRegion = await screen.findByRole("region", { name: "Copy token now" });
  expect(secretRegion).toBeInTheDocument();
  await waitFor(() => expect(secretRegion).toHaveFocus());
  expect(within(secretRegion).getByRole("status")).toHaveAttribute("aria-live", "assertive");
  expect(screen.getByDisplayValue(secretSentinel)).toBeInTheDocument();
  const issueRequest = findRequest(requests, "/api/v1/chat-access-tokens", "POST");
  expect(requests.indexOf(previewRequest)).toBeLessThan(requests.indexOf(issueRequest));
  expect(await issueRequest.json()).toEqual(expect.objectContaining({ agent_ids: [secondAgentId, firstAgentId], label: "Open WebUI" }));
  expect(issueRequest.headers.get("X-CSRF-Token")).toBe(csrfSentinel);
  expect(issueRequest.headers.get("Idempotency-Key")).toBeTruthy();
  expect(screen.getByLabelText("Token label")).toBeDisabled();
  expect(screen.getByRole("button", { name: "Review token scope" })).toBeDisabled();
  expect(requests.filter((request) => new URL(request.url).pathname === "/api/v1/chat-access-tokens" && request.method === "POST")).toHaveLength(1);

  const cached = JSON.stringify({
    mutations: client.getMutationCache().getAll().map((mutation) => mutation.state.variables),
    queries: client.getQueryCache().getAll().map((query) => query.state.data),
  });
  expect(cached).not.toContain(secretSentinel);
  await user.click(screen.getByRole("button", { name: "Dismiss secret" }));
  expect(screen.queryByRole("region", { name: "Copy token now" })).not.toBeInTheDocument();
  expect(screen.queryByDisplayValue(secretSentinel)).not.toBeInTheDocument();
  expect(screen.getByLabelText("Token label")).toBeEnabled();
  expect(JSON.stringify(client.getQueryData(["chat-access-tokens"]))).not.toContain(secretSentinel);
  rendered.unmount();
  expect(JSON.stringify(storedBrowserValues())).not.toContain(secretSentinel);
});

test("editing Agent selection, label, or expiry closes and invalidates the effective-scope review", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", standardFetch(requests, () => jsonResponse(issuedToken(), 201)));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <ChatAccessTokensPage />);

  await prepareReview(user);
  const reviewDialog = screen.getByRole("heading", { name: "Review chat access token" }).closest("dialog");
  expect(reviewDialog).toHaveAttribute("open");
  await user.clear(screen.getByLabelText("Token label"));
  await user.type(screen.getByLabelText("Token label"), "Changed label");
  expect(reviewDialog).not.toHaveAttribute("open");

  await user.click(screen.getByRole("button", { name: "Review token scope" }));
  await waitFor(() => expect(reviewDialog).toHaveAttribute("open"));
  const expiry = screen.getByLabelText("Expires at");
  await user.clear(expiry);
  await user.type(expiry, "2026-10-15T12:00");
  expect(reviewDialog).not.toHaveAttribute("open");

  await user.click(screen.getByRole("button", { name: "Review token scope" }));
  await waitFor(() => expect(reviewDialog).toHaveAttribute("open"));
  await user.click(screen.getByRole("checkbox", { name: /First Agent/ }));
  expect(reviewDialog).not.toHaveAttribute("open");
  expect(screen.getByRole("button", { name: "Review token scope" })).toBeDisabled();
  expect(requests.filter((request) => new URL(request.url).pathname === "/api/v1/chat-access-tokens/preview")).toHaveLength(3);
});

test("issuance failure is rendered without an unhandled rejection and keeps the review available", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", standardFetch(requests, () => problemResponse(500)));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <ChatAccessTokensPage />);

  await prepareReview(user);
  await user.click(await screen.findByRole("button", { name: "Issue token" }));

  const dialog = screen.getByRole("dialog", { name: "Review chat access token" });
  expect(await within(dialog).findByRole("alert")).toHaveTextContent("The operation could not be completed.");
  expect(screen.getByRole("heading", { name: "Review chat access token" })).toBeInTheDocument();
  expect(screen.queryByRole("region", { name: "Copy token now" })).not.toBeInTheDocument();
});

test("idempotent replay never fabricates plaintext and directs the operator to revoke", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", standardFetch(requests, () => problemResponse(409, {
    code: "secret_already_issued",
    detail: "The plaintext was already returned.",
    instance: "/api/v1/chat-access-tokens",
    status: 409,
    title: "Conflict",
    token: publicToken(),
    type: "about:blank",
  })));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <ChatAccessTokensPage />);

  await prepareReview(user);
  await user.click(await screen.findByRole("button", { name: "Issue token" }));

  expect(await screen.findByText("Secret was already issued.")).toBeInTheDocument();
  expect(screen.getByText(/Revoke it and issue a new token/)).toBeInTheDocument();
  expect(screen.queryByRole("region", { name: "Copy token now" })).not.toBeInTheDocument();
});

test("token desk paginates Agents and tokens explicitly with 50-item cursors", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/auth/session") return jsonResponse(session());
    if (url.pathname === "/api/v1/knowledge-bases") return jsonResponse([]);
    if (url.pathname === "/api/v1/agents") return jsonResponse(url.searchParams.has("cursor") ? { items: [] } : { items: [agent(firstAgentId, "First Agent", "first-agent")], next_cursor: "agents-token-next" });
    if (url.pathname === "/api/v1/chat-access-tokens") return jsonResponse(url.searchParams.has("cursor") ? { items: [] } : { items: [tokenSummary()], next_cursor: "tokens-next" });
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <ChatAccessTokensPage />);

  await user.click(await screen.findByRole("button", { name: "Load more Agents" }));
  await user.click(await screen.findByRole("button", { name: "Load more tokens" }));
  await waitFor(() => expect(requests.filter((request) => new URL(request.url).searchParams.has("cursor"))).toHaveLength(2));
  expectPageRequests(requests, "/api/v1/agents", "agents-token-next");
  expectPageRequests(requests, "/api/v1/chat-access-tokens", "tokens-next");
});

test("token revoke keeps failures in its dialog and clears stale errors after a successful retry", async () => {
  let revokeAttempts = 0;
  let revoked = false;
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/auth/session") return jsonResponse(session());
    if (url.pathname === "/api/v1/agents" || url.pathname === "/api/v1/knowledge-bases") return jsonResponse(url.pathname.endsWith("agents") ? { items: [] } : []);
    if (url.pathname === `/api/v1/chat-access-tokens/${tokenId}`) {
      revokeAttempts += 1;
      if (revokeAttempts === 1) return problemResponse(500);
      revoked = true;
      return jsonResponse({ ...tokenSummary(), revoked_at: "2026-08-31T13:00:00Z" });
    }
    if (url.pathname === "/api/v1/chat-access-tokens") return jsonResponse({ items: [{ ...tokenSummary(), revoked_at: revoked ? "2026-08-31T13:00:00Z" : null }] });
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <ChatAccessTokensPage />);

  await user.click(await screen.findByRole("button", { name: "Revoke token" }));
  const dialog = screen.getByRole("dialog", { name: "Revoke “Open WebUI”?" });
  await user.click(within(dialog).getByRole("button", { name: "Revoke token" }));
  expect(await within(dialog).findByRole("alert")).toHaveTextContent("The operation could not be completed.");
  expect(dialog).toHaveAttribute("open");

  await user.click(within(dialog).getByRole("button", { name: "Revoke token" }));
  await waitFor(() => expect(revokeAttempts).toBe(2));
  await waitFor(() => expect(dialog).not.toHaveAttribute("open"));
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Revoke token" })).not.toBeInTheDocument();
});

async function prepareReview(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  await user.type(await screen.findByLabelText("Token label"), "Failure case");
  await user.click(screen.getByRole("checkbox", { name: /First Agent/ }));
  await user.click(screen.getByRole("button", { name: "Review token scope" }));
  await screen.findByRole("heading", { name: "Review chat access token" });
}

function standardFetch(requests: Request[], issue: () => Response) {
  return vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/auth/session") return jsonResponse(session());
    if (url.pathname === "/api/v1/agents") return jsonResponse({ items: [agent(firstAgentId, "First Agent", "first-agent")] });
    if (url.pathname === "/api/v1/knowledge-bases") return jsonResponse([knowledgeBase(firstKnowledgeId, "Alpha docs")]);
    if (url.pathname === "/api/v1/chat-access-tokens/preview") return jsonResponse({ ...preview(), agent_ids: [firstAgentId], agent_scopes: [preview().agent_scopes[1]], knowledge_base_ids: [firstKnowledgeId] });
    if (url.pathname === "/api/v1/chat-access-tokens" && request.method === "POST") return issue();
    if (url.pathname === "/api/v1/chat-access-tokens") return jsonResponse({ items: [] });
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  });
}

function storedBrowserValues(): Array<string | null> {
  return ["local", "session"].flatMap((prefix) => {
    const storage: unknown = Reflect.get(window, `${prefix}Storage`);
    if (!(storage instanceof Storage)) return [];
    return Array.from({ length: storage.length }, (_, index) => {
      const key = storage.key(index);
      return key === null ? null : storage.getItem(key);
    });
  });
}

function renderAuthenticated(queryClient: QueryClient, page: ReactNode): ReturnType<typeof render> {
  function Gate(): ReactNode {
    const { state } = useAuth();
    return state.kind === "authenticated" ? page : <p>{state.kind}</p>;
  }
  const rootRoute = createRootRoute({ component: () => <AuthProvider><Gate /></AuthProvider> });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(<QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider>);
}

function testQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

function agent(id: string, displayName: string, key: string): Agent {
  return {
    activated_at: "2026-08-31T12:00:00Z",
    archived_at: null,
    created_at: "2026-08-31T12:00:00Z",
    current_version: {
      agent_id: id,
      configuration: { answer_mode: "single_pass", behavioral_instructions: "", description: "", display_name: displayName, evidence_access: "wiki_only", identity_instructions: "Answer from evidence.", knowledge_base_ids: [id === firstAgentId ? firstKnowledgeId : secondKnowledgeId], max_answer_tokens: 1_024, max_tool_calls: 0, model_profile_id: "00000000-0000-0000-0000-000000000040", reasoning_effort: "none", refusal_markdown: "Cannot answer.", response_language: "en" },
      created_at: "2026-08-31T12:00:00Z",
      created_by_operator_id: "00000000-0000-0000-0000-000000000001",
      id: id === firstAgentId ? "00000000-0000-0000-0000-000000000050" : "00000000-0000-0000-0000-000000000051",
      version_number: 1,
    },
    current_version_id: id === firstAgentId ? "00000000-0000-0000-0000-000000000050" : "00000000-0000-0000-0000-000000000051",
    id,
    key,
    lifecycle: "active",
    selector: `agent:${key}`,
    updated_at: "2026-08-31T12:00:00Z",
    version: 2,
  };
}

function preview(): ChatAccessTokenScopePreview {
  return {
    agent_ids: [secondAgentId, firstAgentId],
    agent_scopes: [
      { agent_id: secondAgentId, agent_key: "second-agent", effective_access: "public", knowledge_base_ids: [secondKnowledgeId], ready: true },
      { agent_id: firstAgentId, agent_key: "first-agent", effective_access: "restricted", knowledge_base_ids: [firstKnowledgeId], ready: true },
    ],
    effective_access: "restricted",
    knowledge_base_ids: [firstKnowledgeId, secondKnowledgeId],
    ready: true,
  };
}

function publicToken(): ChatAccessToken {
  return { agent_ids: [secondAgentId, firstAgentId], agent_scopes: preview().agent_scopes, created_at: "2026-08-31T12:00:00Z", expires_at: "2026-10-01T12:00:00Z", id: tokenId, label: "Open WebUI", last_used_at: null, prefix: "ref0_abc123", revoked_at: null };
}

function tokenSummary(): ChatAccessTokenSummary {
  return { agent_count: 2, created_at: "2026-08-31T12:00:00Z", expires_at: "2026-10-01T12:00:00Z", id: tokenId, label: "Open WebUI", last_used_at: null, prefix: "ref0_abc123", revoked_at: null };
}

function issuedToken(): IssuedChatAccessToken {
  return { ...publicToken(), secret: secretSentinel };
}

function knowledgeBase(id: string, name: string): KnowledgeBase {
  return { access: "restricted", archived_at: null, created_at: "2026-08-31T12:00:00Z", delete_requested_at: null, deleted_at: null, id, instructions: "", language: "en", lifecycle: "active", name, published_wiki_id: "00000000-0000-0000-0000-000000000060", purge_after: null, updated_at: "2026-08-31T12:00:00Z", version: 2 };
}

function session(): object {
  return { csrf_token: csrfSentinel, expires_at: "2026-09-01T00:00:00Z", operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" } };
}

function findRequest(requests: Request[], path: string, method: string): Request {
  const request = requests.find((candidate) => new URL(candidate.url).pathname === path && candidate.method === method);
  if (!request) throw new Error(`Missing ${method} ${path}`);
  return request;
}

function expectPageRequests(requests: Request[], path: string, cursor: string): void {
  const matching = requests.filter((request) => new URL(request.url).pathname === path);
  expect(matching).toHaveLength(2);
  expect(new URL(matching[0]?.url ?? "http://invalid").searchParams.get("limit")).toBe("50");
  expect(new URL(matching[0]?.url ?? "http://invalid").searchParams.has("cursor")).toBe(false);
  expect(new URL(matching[1]?.url ?? "http://invalid").searchParams.get("cursor")).toBe(cursor);
}

function problemResponse(status: number, body: unknown = { detail: "private detail", status, title: "Failure", type: "about:blank" }): Response {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/problem+json" }, status });
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" }, status });
}

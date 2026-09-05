import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRouter } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState, type ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";

import type { Agent, AgentReadiness, Credential, Job } from "../../api/client";
import { connectEventStream } from "../../api/events";
import { queryKeys } from "../../api/queries";
import { AuthProvider, useAuth } from "../../app/auth";
import { AgentDiscordPanel, DiscordPage } from "./DiscordPage";
import type { DiscordBinding, DiscordChannel, DiscordConnection, DiscordRole, DiscordServer } from "./types";

const knowledgeBaseId = "00000000-0000-0000-0000-000000000010";
const agentId = "00000000-0000-0000-0000-000000000012";
const otherAgentId = "00000000-0000-0000-0000-000000000017";
const modelProfileId = "00000000-0000-0000-0000-000000000013";
const connectionId = "00000000-0000-0000-0000-000000000020";
const otherConnectionId = "00000000-0000-0000-0000-000000000021";
const credentialId = "00000000-0000-0000-0000-000000000030";
const discordJobId = "00000000-0000-0000-0000-000000000031";
const bindingId = "00000000-0000-0000-0000-000000000040";
const serverId = "300000000000000001";
const channelId = "400000000000000001";
const otherChannelId = "400000000000000002";
const roleId = "500000000000000001";
const csrfSentinel = "csrf-m5-sentinel";
const tokenSentinel = "discord-token-sentinel";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.replaceState(window.history.state, "", "/");
});

test("Discord connection setup writes the bot token only to the credential vault", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/auth/session") return jsonResponse(session());
    if (url.pathname === "/api/v1/agents") return jsonResponse({ items: [agent()] });
    if (url.pathname === "/api/v1/discord/connections" && request.method === "GET") return jsonResponse([]);
    if (url.pathname === "/api/v1/discord/bindings") return jsonResponse([]);
    if (url.pathname === "/api/v1/credentials" && request.method === "POST") return jsonResponse(credential());
    if (url.pathname === "/api/v1/discord/connections" && request.method === "POST") return jsonResponse(connection());
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <DiscordPage />);

  await user.type(await screen.findByLabelText("Connection name"), "Docs bot");
  await user.type(screen.getByLabelText("Credential label"), "Discord production token");
  await user.type(screen.getByLabelText("Discord bot token"), tokenSentinel);
  await user.click(screen.getByRole("button", { name: "Create Discord connection" }));
  await waitFor(() => expect(requests.some((request) => request.method === "POST" && new URL(request.url).pathname === "/api/v1/discord/connections")).toBe(true));
  const credentialRequest = findRequest(requests, "/api/v1/credentials", "POST");
  const connectionRequest = findRequest(requests, "/api/v1/discord/connections", "POST");
  expect(await credentialRequest.text()).toContain(tokenSentinel);
  const connectionBody = await connectionRequest.text();
  expect(JSON.parse(connectionBody)).toEqual({ credential_id: credentialId, display_name: "Docs bot" });
  expect(connectionBody).not.toContain(tokenSentinel);
  expect(screen.queryByDisplayValue(tokenSentinel)).not.toBeInTheDocument();
});

test("switching Discord targets clears connection secrets, installation, and queued jobs", async () => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/auth/session") return jsonResponse(session());
    if (url.pathname === "/api/v1/agents") return jsonResponse({ items: [agent()] });
    if (url.pathname === "/api/v1/discord/connections" && request.method === "GET") return jsonResponse([connection(), connection(otherConnectionId, "Billing bot")]);
    if (url.pathname === "/api/v1/discord/bindings") return jsonResponse([]);
    if (url.pathname.endsWith("/servers")) return jsonResponse([]);
    if (url.pathname.endsWith("/validate") || url.pathname.endsWith("/refresh")) return jsonResponse(discordJob());
    if (url.pathname.endsWith("/installation-url")) return jsonResponse({ permissions: 2_048, scopes: ["bot"], url: "https://discord.com/oauth2/authorize?fixture=1" });
    if (url.pathname === `/api/v1/jobs/${discordJobId}`) return jsonResponse(discordJob());
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <DiscordPage />);

  await waitFor(() => expect(screen.getByLabelText("Working connection")).toHaveValue(connectionId));
  await user.type(screen.getByLabelText("New credential label"), "Replacement A");
  await user.type(screen.getByLabelText("Replacement token"), tokenSentinel);
  await user.click(screen.getByRole("button", { name: "Validate token and identity" }));
  expect(await screen.findByText(/Discord operation job status: succeeded/)).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Generate installation URL" }));
  expect(await screen.findByRole("link", { name: "Open Discord authorization" })).toBeInTheDocument();

  await user.selectOptions(screen.getByLabelText("Working connection"), otherConnectionId);
  await waitFor(() => expect(screen.getByLabelText("Display name")).toHaveValue("Billing bot"));
  expect(screen.getByLabelText("New credential label")).toHaveValue("");
  expect(screen.getByLabelText("Replacement token")).toHaveValue("");
  expect(screen.queryByRole("link", { name: "Open Discord authorization" })).not.toBeInTheDocument();
  expect(screen.queryByText(/Discord operation job status/)).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "Servers" }));
  await user.click(screen.getByRole("button", { name: "Refresh from Discord" }));
  expect(await screen.findByText(/Server refresh job status: succeeded/)).toBeInTheDocument();
  await user.selectOptions(screen.getByLabelText("Working connection"), connectionId);
  expect(screen.queryByText(/Server refresh job status/)).not.toBeInTheDocument();
});

test("a late failure from the prior Discord target cannot surface under the newly selected connection", async () => {
  let rejectValidation: ((reason: unknown) => void) | undefined;
  let validationStarted = false;
  const pendingValidation = new Promise<Response>((_resolve, reject) => { rejectValidation = reject; });
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/auth/session") return jsonResponse(session());
    if (url.pathname === "/api/v1/agents") return jsonResponse({ items: [agent()] });
    if (url.pathname === "/api/v1/discord/connections" && request.method === "GET") return jsonResponse([connection(), connection(otherConnectionId, "Billing bot")]);
    if (url.pathname === "/api/v1/discord/bindings") return jsonResponse([]);
    if (url.pathname.endsWith("/servers")) return jsonResponse([]);
    if (url.pathname === `/api/v1/discord/connections/${connectionId}/validate`) {
      validationStarted = true;
      return pendingValidation;
    }
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <DiscordPage />);

  await user.click(await screen.findByRole("button", { name: "Validate token and identity" }));
  await waitFor(() => expect(validationStarted).toBe(true));
  await user.selectOptions(screen.getByLabelText("Working connection"), otherConnectionId);
  await waitFor(() => expect(screen.getByLabelText("Display name")).toHaveValue("Billing bot"));

  await act(async () => {
    rejectValidation?.(new Error("target A failed after the switch"));
    await pendingValidation.catch(() => undefined);
  });
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(screen.getByLabelText("Working connection")).toHaveValue(otherConnectionId);
});

test("Discord context switches clear action errors, connection disable is reversible, and binding delete failures stay in the dialog", async () => {
  const requests: Request[] = [];
  let currentConnection = connection();
  let validateAttempts = 0;
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/auth/session") return jsonResponse(session());
    if (url.pathname === "/api/v1/agents") return jsonResponse({ items: [agent()] });
    if (url.pathname === `/api/v1/agents/${agentId}/readiness`) return jsonResponse(agentReadiness());
    if (url.pathname === "/api/v1/discord/connections") return jsonResponse([currentConnection]);
    if (url.pathname === "/api/v1/discord/bindings") return jsonResponse([binding()]);
    if (url.pathname.endsWith("/servers")) return jsonResponse([server()]);
    if (url.pathname.endsWith("/channels")) return jsonResponse([channel()]);
    if (url.pathname.endsWith("/roles")) return jsonResponse([role()]);
    if (url.pathname.endsWith("/validate") && url.pathname.includes("/connections/")) {
      validateAttempts += 1;
      return validateAttempts === 1 ? problemResponse(500) : jsonResponse(discordJob());
    }
    if (url.pathname === `/api/v1/jobs/${discordJobId}`) return jsonResponse(discordJob());
    if (request.method === "PATCH" && url.pathname === `/api/v1/discord/connections/${connectionId}`) {
      currentConnection = { ...currentConnection, lifecycle: "disabled", state: "disabled", version: 3 };
      return jsonResponse(currentConnection);
    }
    if (request.method === "DELETE" && url.pathname.includes("/bindings/")) return problemResponse(500);
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <DiscordPage />);

  await user.click(await screen.findByRole("button", { name: "Validate token and identity" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("The operation could not be completed.");
  await user.click(screen.getByRole("button", { name: "Servers" }));
  await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
  await user.click(screen.getByRole("button", { name: "Connections" }));
  await user.click(screen.getByRole("button", { name: "Validate token and identity" }));
  expect(await screen.findByText(/Discord operation job status: succeeded/)).toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();

  expect(screen.queryByRole("button", { name: "Delete connection" })).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Disable connection" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Enable connection" })).toBeInTheDocument());
  const lifecycleRequest = findRequest(requests, `/api/v1/discord/connections/${connectionId}`, "PATCH");
  expect(await lifecycleRequest.json()).toEqual({ expected_version: 2, lifecycle: "disabled" });
  expect(requests.some((request) => request.method === "DELETE" && new URL(request.url).pathname.startsWith("/api/v1/discord/connections/"))).toBe(false);

  await user.selectOptions(screen.getByLabelText("Agent"), agentId);
  await user.click(screen.getByRole("button", { name: "Bindings" }));
  const bindingCard = (await screen.findByRole("heading", { name: "#docs-help" })).closest("article");
  if (!bindingCard) throw new Error("binding card is missing");
  await user.click(within(bindingCard).getByRole("button", { name: "Delete" }));
  const bindingDialog = screen.getByRole("dialog", { name: "Delete #docs-help binding?" });
  await user.click(within(bindingDialog).getByRole("button", { name: "Delete binding" }));
  expect(await within(bindingDialog).findByRole("alert")).toHaveTextContent("The operation could not be completed.");
  expect(bindingDialog).toHaveAttribute("open");
});

test("Discord deep link resolves an Agent beyond the loaded page and creates a restricted draft", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/auth/session") return jsonResponse(session());
    if (url.pathname === "/api/v1/agents") return jsonResponse({ items: [agent(otherAgentId, "billing-guide", "Billing guide")], next_cursor: "agents-next" });
    if (url.pathname === `/api/v1/agents/${agentId}`) return jsonResponse(agent());
    if (url.pathname === `/api/v1/agents/${agentId}/readiness`) return jsonResponse(agentReadiness());
    if (url.pathname === "/api/v1/discord/connections") return jsonResponse([connection()]);
    if (url.pathname === "/api/v1/discord/bindings" && request.method === "GET") return jsonResponse([binding()]);
    if (url.pathname === "/api/v1/discord/bindings" && request.method === "POST") return jsonResponse(binding());
    if (url.pathname.endsWith("/servers")) return jsonResponse([server()]);
    if (url.pathname.endsWith("/channels")) return jsonResponse([channel()]);
    if (url.pathname.endsWith("/roles")) return jsonResponse([role()]);
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <DiscordPage search={{ agent_id: agentId, connection_id: connectionId, server_id: serverId, view: "bindings" }} />);

  expect(await screen.findByRole("button", { name: "Bindings", current: "page" })).toBeInTheDocument();
  await waitFor(() => expect(screen.getByLabelText("Agent")).toHaveValue(agentId));
  expect(findRequest(requests, `/api/v1/agents/${agentId}`, "GET")).toBeDefined();
  await user.selectOptions(screen.getByLabelText("Listen channel"), channelId);
  await user.click(screen.getByLabelText(new RegExp(`Docs readers ${roleId}`)));
  await user.click(screen.getByLabelText("Slash command"));
  expect(screen.getByText(/Required bot permissions and audience policy are satisfied/i)).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Create draft binding" }));
  const request = await waitFor(() => findRequest(requests, "/api/v1/discord/bindings", "POST"));
  expect(await request.json()).toMatchObject({ agent_id: agentId, allowed_role_ids: [roleId], connection_id: connectionId, enabled: false, listen_channel_id: channelId, reply_policy: "same_channel", server_id: serverId, triggers: ["mention", "slash_command"] });
});

test("embedded Discord derives its binding scope from changing Agent props", async () => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/auth/session") return jsonResponse(session());
    if (url.pathname === "/api/v1/discord/connections") return jsonResponse([connection()]);
    if (url.pathname === "/api/v1/discord/bindings") return jsonResponse([binding(), binding(otherAgentId)]);
    if (url.pathname.endsWith("/servers")) return jsonResponse([server()]);
    if (url.pathname.endsWith("/channels")) return jsonResponse([channel()]);
    if (url.pathname.endsWith("/roles")) return jsonResponse([role()]);
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  function Harness(): ReactNode {
    const [selected, setSelected] = useState(agent());
    return <><button onClick={() => setSelected(agent(otherAgentId, "billing-guide", "Billing guide"))} type="button">Switch Agent</button><AgentDiscordPanel agent={selected} readiness={agentReadiness()} /></>;
  }
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <Harness />);

  expect(await screen.findByText("agent:product-guide", { exact: true })).toBeInTheDocument();
  await screen.findByRole("option", { name: "#docs-help · Text channel" });
  await user.selectOptions(screen.getByLabelText("Listen channel"), channelId);
  await user.click(screen.getByLabelText(new RegExp(`Docs readers ${roleId}`)));
  await user.click(screen.getByLabelText("Slash command"));
  expect(screen.getByLabelText("Listen channel")).toHaveValue(channelId);
  expect(screen.getByLabelText("Slash command")).toBeChecked();
  const deepLink = await screen.findByRole("link", { name: "Open full Discord desk" });
  await waitFor(() => {
    const target = new URL(deepLink.getAttribute("href") ?? "", "http://test.invalid");
    expect(target.searchParams.get("agent_id")).toBe(agentId);
    expect(target.searchParams.get("connection_id")).toBe(connectionId);
    expect(JSON.parse(target.searchParams.get("server_id") ?? "null")).toBe(serverId);
    expect(target.searchParams.get("view")).toBe("bindings");
  });
  await user.click(screen.getByRole("button", { name: "Switch Agent" }));
  expect(await screen.findByRole("heading", { name: `#${otherChannelId}` })).toBeInTheDocument();
  expect(screen.queryByRole("heading", { name: "#docs-help" })).not.toBeInTheDocument();
  expect(screen.getByText("Fixed Agent").parentElement).toHaveTextContent("agent:billing-guide");
  expect(screen.getByLabelText("Listen channel")).toHaveValue("");
  expect(screen.getByLabelText(new RegExp(`Docs readers ${roleId}`))).not.toBeChecked();
  expect(screen.getByLabelText("Slash command")).not.toBeChecked();
});

test("ready draft and archived Agents cannot enable a Discord binding until lifecycle activation", async () => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/auth/session") return jsonResponse(session());
    if (url.pathname === "/api/v1/discord/connections") return jsonResponse([connection()]);
    if (url.pathname === "/api/v1/discord/bindings") return jsonResponse([binding()]);
    if (url.pathname.endsWith("/servers")) return jsonResponse([server()]);
    if (url.pathname.endsWith("/channels")) return jsonResponse([channel()]);
    if (url.pathname.endsWith("/roles")) return jsonResponse([role()]);
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  function Harness(): ReactNode {
    const [lifecycle, setLifecycle] = useState<Agent["lifecycle"]>("draft");
    const selected = { ...agent(), activated_at: null, archived_at: lifecycle === "archived" ? "2026-08-31T13:00:00Z" : null, lifecycle };
    return <><button onClick={() => setLifecycle("archived")} type="button">Archive fixture</button><AgentDiscordPanel agent={selected} readiness={agentReadiness()} /></>;
  }
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <Harness />);

  expect(await screen.findByRole("button", { name: "Enable" })).toBeDisabled();
  expect(screen.getByText("Activate the Agent before enabling this binding.")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Archive fixture" }));
  expect(screen.getByRole("button", { name: "Enable" })).toBeDisabled();
  expect(await screen.findByText("Reactivate the Agent before enabling this binding.")).toBeInTheDocument();
});

test("Discord connection, binding, and server events invalidate their operator views", async () => {
  const stream = new FakeEventSource();
  vi.stubGlobal("EventSource", class { constructor() { return stream; } });
  const queryClient = testQueryClient();
  const invalidate = vi.spyOn(queryClient, "invalidateQueries");
  const close = connectEventStream(queryClient);

  stream.emit("discord.connection.state_changed");
  stream.emit("discord.binding.unhealthy");
  stream.emit("discord.directory.refreshed");
  await waitFor(() => {
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.discordConnections });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.discordBindings });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.discordServers });
  });
  close();
});

function renderAuthenticated(queryClient: QueryClient, page: ReactNode): void {
  function Gate(): ReactNode {
    const { state } = useAuth();
    return state.kind === "authenticated" ? page : <p>{state.kind}</p>;
  }
  const rootRoute = createRootRoute({ component: () => <AuthProvider><Gate /></AuthProvider> });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  render(<QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider>);
}

function testQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

function agent(id = agentId, key = "product-guide", displayName = "Product guide"): Agent {
  return {
    activated_at: "2026-08-28T12:00:00Z",
    archived_at: null,
    created_at: "2026-08-28T12:00:00Z",
    current_version: {
      agent_id: id,
      configuration: {
        answer_mode: "tool_calling",
        behavioral_instructions: "",
        description: "Answers product documentation questions.",
        display_name: displayName,
        evidence_access: "wiki_only",
        identity_instructions: "You are the product guide.",
        knowledge_base_ids: [knowledgeBaseId],
        max_answer_tokens: 2_048,
        max_tool_calls: 8,
        model_profile_id: modelProfileId,
        reasoning_effort: "none",
        refusal_markdown: "I cannot answer that.",
        response_language: "en",
      },
      created_at: "2026-08-28T12:00:00Z",
      created_by_operator_id: "00000000-0000-0000-0000-000000000001",
      id: id === agentId ? "00000000-0000-0000-0000-000000000014" : "00000000-0000-0000-0000-000000000018",
      version_number: 1,
    },
    current_version_id: id === agentId ? "00000000-0000-0000-0000-000000000014" : "00000000-0000-0000-0000-000000000018",
    id,
    key,
    lifecycle: "active",
    selector: `agent:${key}`,
    updated_at: "2026-08-28T12:00:00Z",
    version: 2,
  };
}

function agentReadiness(): AgentReadiness {
  return {
    effective_access: "restricted",
    endpoint_configuration_version: 2,
    issues: [],
    model_profile_version_id: "00000000-0000-0000-0000-000000000015",
    model_profile_version_number: 3,
    provider_endpoint_id: "00000000-0000-0000-0000-000000000016",
    ready: true,
  };
}

function credential(): Credential {
  return { created_at: "2026-08-28T12:00:00Z", id: credentialId, key_id: "primary", kind: "discord_bot_token", label: "Discord production token", masked_value: "••••", rotated_at: null, secret_version: 1 };
}

function connection(id = connectionId, displayName = "Docs bot"): DiscordConnection {
  return { application_id: "100000000000000001", avatar_hash: null, bot_user_id: "200000000000000001", bot_username: "DocsBot", created_at: "2026-08-28T12:00:00Z", credential_id: credentialId, display_name: displayName, gateway_latency_ms: 42, id, last_event_at: "2026-08-28T12:05:00Z", last_heartbeat_at: "2026-08-28T12:05:10Z", lifecycle: "enabled", sanitized_error: null, state: "ready", updated_at: "2026-08-28T12:05:10Z", version: 2 };
}

function discordJob(): Job {
  return { attempt_count: 1, created_at: "2026-08-28T12:00:00Z", finished_at: "2026-08-28T12:00:01Z", id: discordJobId, job_type: "refresh_discord", lease_expires_at: null, lease_generation: 1, lease_owner: null, max_attempts: 3, not_before: null, progress: 100, result: {}, sanitized_error: null, started_at: "2026-08-28T12:00:00Z", status: "succeeded", target_id: connectionId, target_type: "discord_connection", updated_at: "2026-08-28T12:00:01Z" };
}

function server(): DiscordServer {
  return { connection_id: connectionId, icon_hash: null, name: "Product server", owner: false, refreshed_at: "2026-08-28T12:05:00Z", server_id: serverId };
}

function channel(): DiscordChannel {
  return { channel_id: channelId, channel_type: 0, connection_id: connectionId, effective_bot_permissions: Number((1n << 10n) | (1n << 11n) | (1n << 14n) | (1n << 16n) | (1n << 35n) | (1n << 38n)), everyone_can_view: false, viewer_role_ids: ["500000000000000001"], viewer_user_ids: [], name: "docs-help", parent_id: null, permission_status: "ready", position: 1, refreshed_at: "2026-08-28T12:05:00Z", server_id: serverId };
}

function role(): DiscordRole {
  return { connection_id: connectionId, name: "Docs readers", position: 4, refreshed_at: "2026-08-28T12:05:00Z", role_id: roleId, server_id: serverId };
}

function binding(targetAgentId = agentId): DiscordBinding {
  return { agent_id: targetAgentId, allowed_role_ids: [roleId], allowed_user_ids: [], connection_id: connectionId, created_at: "2026-08-28T12:10:00Z", enabled: false, health: "healthy", id: targetAgentId === agentId ? bindingId : "00000000-0000-0000-0000-000000000041", listen_channel_id: targetAgentId === agentId ? channelId : otherChannelId, rate_requests: 5, rate_window_seconds: 60, reply_channel_id: null, reply_policy: "same_channel", sanitized_error: null, server_id: serverId, triggers: ["mention"], updated_at: "2026-08-28T12:10:00Z", version: 2 };
}

function session(): object {
  return { csrf_token: csrfSentinel, expires_at: "2026-08-29T00:00:00Z", operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" } };
}

function findRequest(requests: Request[], path: string, method: string): Request {
  const request = requests.find((item) => new URL(item.url).pathname === path && item.method === method);
  if (!request) throw new Error(`Missing ${method} ${path}`);
  return request;
}

class FakeEventSource {
  private readonly listeners = new Map<string, Set<EventListener>>();
  addEventListener(name: string, listener: EventListener): void { const selected = this.listeners.get(name) ?? new Set<EventListener>(); selected.add(listener); this.listeners.set(name, selected); }
  removeEventListener(name: string, listener: EventListener): void { this.listeners.get(name)?.delete(listener); }
  emit(name: string): void { const event = new MessageEvent(name, { data: JSON.stringify({ id: connectionId }) }); for (const listener of this.listeners.get(name) ?? []) listener(event); }
  close(): void {}
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function problemResponse(status: number): Response {
  return new Response(JSON.stringify({ detail: "private detail", status, title: "Failure", type: "about:blank" }), { status, headers: { "Content-Type": "application/problem+json" } });
}

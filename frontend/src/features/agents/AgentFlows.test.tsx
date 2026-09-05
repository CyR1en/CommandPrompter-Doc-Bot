import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRouter } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState, type ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";

import type { Agent, AgentConfigurationInput, KnowledgeBase, ModelProfile, ProviderEndpoint } from "../../api/client";
import { AuthProvider, useAuth } from "../../app/auth";
import { AgentConfigurationForm } from "./AgentConfigurationForm";
import { AgentConfigurationPage } from "./AgentConfigurationPage";
import { AgentRunsPanel } from "./AgentRunsPanel";
import { AgentsPage } from "./AgentsPage";

const agentId = "00000000-0000-0000-0000-000000000010";
const agentVersionId = "00000000-0000-0000-0000-000000000011";
const modelId = "00000000-0000-0000-0000-000000000020";
const endpointId = "00000000-0000-0000-0000-000000000021";
const secondModelId = "00000000-0000-0000-0000-000000000023";
const secondEndpointId = "00000000-0000-0000-0000-000000000024";
const firstKnowledgeId = "00000000-0000-0000-0000-000000000030";
const secondKnowledgeId = "00000000-0000-0000-0000-000000000031";
const csrfSentinel = "agent-csrf-sentinel";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("create form submits the exact reordered knowledge configuration and enforces single-pass invariants", async () => {
  const submitted = vi.fn<(value: { configuration: AgentConfigurationInput; key: string }) => void>();
  const user = userEvent.setup();
  render(
    <AgentConfigurationForm
      busy={false}
      kind="create"
      knowledgeBases={[knowledgeBase(firstKnowledgeId, "Alpha docs"), knowledgeBase(secondKnowledgeId, "Beta docs")]}
      models={[modelProfile()]}
      onSubmit={submitted}
      providers={[providerEndpoint()]}
    />,
  );

  await user.type(screen.getByLabelText(/^Agent key/), "support-agent");
  await user.type(screen.getByLabelText("Display name"), "Support Agent");
  await user.type(screen.getByLabelText(/^Identity instructions/), "Answer as the support specialist.");
  await user.selectOptions(screen.getByLabelText("Model profile"), modelId);
  await addKnowledge(user, firstKnowledgeId);
  await addKnowledge(user, secondKnowledgeId);
  await user.click(screen.getByRole("button", { name: "Move Beta docs up" }));
  await user.selectOptions(screen.getByLabelText("Answer mode"), "single_pass");

  expect(screen.getByLabelText("Maximum tool calls")).toBeDisabled();
  expect(screen.getByLabelText("Maximum tool calls")).toHaveValue(0);
  await user.click(screen.getByRole("button", { name: "Create Agent" }));

  expect(submitted).toHaveBeenCalledWith(expect.objectContaining({
    configuration: expect.objectContaining({
      answer_mode: "single_pass",
      evidence_access: "wiki_only",
      knowledge_base_ids: [secondKnowledgeId, firstKnowledgeId],
      max_tool_calls: 0,
    }),
    key: "support-agent",
  }));
});

test("model profiles with the same model ID remain distinguishable by provider endpoint", async () => {
  const submitted = vi.fn<(value: { configuration: AgentConfigurationInput; key: string }) => void>();
  const user = userEvent.setup();
  render(
    <AgentConfigurationForm
      busy={false}
      kind="create"
      knowledgeBases={[knowledgeBase(firstKnowledgeId, "Alpha docs")]}
      models={[modelProfile(), modelProfile(secondModelId, secondEndpointId)]}
      onSubmit={submitted}
      providers={[providerEndpoint(), providerEndpoint(secondEndpointId, "Secondary provider")]}
    />,
  );

  expect(screen.getByRole("option", { name: /Primary provider · support-model/ })).toBeInTheDocument();
  expect(screen.getByRole("option", { name: /Secondary provider · support-model/ })).toBeInTheDocument();
  await user.type(screen.getByLabelText(/^Agent key/), "provider-specific");
  await user.type(screen.getByLabelText("Display name"), "Provider specific");
  await user.type(screen.getByLabelText(/Identity instructions/), "Use the selected provider.");
  await user.selectOptions(screen.getByLabelText("Model profile"), secondModelId);
  await addKnowledge(user, firstKnowledgeId);
  expect(screen.getByText("Secondary provider · support-model · available", { selector: ".agent-model-receipt small" })).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Create Agent" }));
  expect(submitted).toHaveBeenCalledWith(expect.objectContaining({
    configuration: expect.objectContaining({ model_profile_id: secondModelId }),
  }));
});

test("the complete Agent configuration stays inert while an asynchronous replacement is pending", async () => {
  let finishSave: (() => void) | undefined;
  const pending = new Promise<void>((resolve) => { finishSave = resolve; });
  const submitted = vi.fn<(value: AgentConfigurationInput) => void>();
  function Harness(): ReactNode {
    const [busy, setBusy] = useState(false);
    return (
      <AgentConfigurationForm
        agent={agent()}
        busy={busy}
        kind="replace"
        knowledgeBases={[knowledgeBase(firstKnowledgeId, "Alpha docs"), knowledgeBase(secondKnowledgeId, "Beta docs")]}
        models={[modelProfile()]}
        onSubmit={(value) => {
          submitted(value);
          setBusy(true);
          void pending.then(() => setBusy(false));
        }}
        providers={[providerEndpoint()]}
      />
    );
  }
  const user = userEvent.setup();
  render(<Harness />);

  const displayName = screen.getByLabelText("Display name");
  await user.clear(displayName);
  await user.type(displayName, "Pending Agent");
  await user.click(screen.getByRole("button", { name: "Save new version" }));

  expect(submitted).toHaveBeenCalledWith(expect.objectContaining({ display_name: "Pending Agent" }));
  expect(displayName).toBeDisabled();
  expect(screen.getByLabelText("Model profile")).toBeDisabled();
  expect(screen.getByRole("button", { name: "Move Beta docs up" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Saving Agent…" })).toBeDisabled();
  await user.type(displayName, " ignored");
  expect(displayName).toHaveValue("Pending Agent");

  await act(async () => {
    finishSave?.();
    await pending;
  });
  await waitFor(() => expect(displayName).toBeEnabled());
});

test("active configuration and lifecycle readiness races render typed issues without mislabeling stale conflicts", async () => {
  let replacementAttempts = 0;
  const requests: Request[] = [];
  const current = { ...agent(), activated_at: "2026-08-31T13:00:00Z", lifecycle: "active" as const };
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/auth/session") return jsonResponse(session());
    if (url.pathname === "/api/v1/knowledge-bases") return jsonResponse([knowledgeBase(firstKnowledgeId, "Alpha docs"), knowledgeBase(secondKnowledgeId, "Beta docs")]);
    if (url.pathname === "/api/v1/model-profiles") return jsonResponse([modelProfile()]);
    if (url.pathname === "/api/v1/provider-endpoints") return jsonResponse([providerEndpoint()]);
    if (url.pathname === `/api/v1/agents/${agentId}` && request.method === "GET") return jsonResponse(current);
    if (url.pathname === `/api/v1/agents/${agentId}/readiness`) return jsonResponse(readiness());
    if (url.pathname === `/api/v1/agents/${agentId}/configuration`) {
      replacementAttempts += 1;
      return replacementAttempts === 1
        ? candidateNotReadyProblem("knowledge_base_unpublished", firstKnowledgeId)
        : problemResponse(409);
    }
    if (url.pathname === `/api/v1/agents/${agentId}/runs` || url.pathname === `/api/v1/agents/${agentId}/versions`) return jsonResponse({ items: [] });
    if (url.pathname === "/api/v1/discord/connections" || url.pathname === "/api/v1/discord/bindings") return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <AgentConfigurationPage agentId={agentId} kind="detail" />);

  await user.clear(await screen.findByLabelText("Display name"));
  await user.type(screen.getByLabelText("Display name"), "Unready candidate");
  await user.click(screen.getByRole("button", { name: "Save new version" }));
  expect(await screen.findByText("Candidate Agent is not ready.")).toBeInTheDocument();
  expect(screen.getByText(new RegExp(`Knowledge base unpublished · ${firstKnowledgeId}`))).toBeInTheDocument();
  expect(screen.getByText(/active Agent was left unchanged/i)).toBeInTheDocument();
  expect(findRequest(requests, `/api/v1/agents/${agentId}/configuration`, "PUT")).toBeDefined();

  await user.click(screen.getByRole("button", { name: "Save new version" }));
  await waitFor(() => expect(replacementAttempts).toBe(2));
  expect(screen.queryByText("Candidate Agent is not ready.")).not.toBeInTheDocument();
  expect(screen.getByRole("alert")).not.toHaveTextContent("candidate");
});

test("activation race surfaces the exact candidate readiness snapshot and leaves the Agent draft", async () => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/auth/session") return jsonResponse(session());
    if (url.pathname === "/api/v1/knowledge-bases") return jsonResponse([knowledgeBase(firstKnowledgeId, "Alpha docs"), knowledgeBase(secondKnowledgeId, "Beta docs")]);
    if (url.pathname === "/api/v1/model-profiles") return jsonResponse([modelProfile()]);
    if (url.pathname === "/api/v1/provider-endpoints") return jsonResponse([providerEndpoint()]);
    if (url.pathname === `/api/v1/agents/${agentId}`) return jsonResponse(agent());
    if (url.pathname === `/api/v1/agents/${agentId}/readiness`) return jsonResponse(readiness());
    if (url.pathname === `/api/v1/agents/${agentId}/lifecycle`) return candidateNotReadyProblem("endpoint_unavailable");
    if (url.pathname === `/api/v1/agents/${agentId}/runs` || url.pathname === `/api/v1/agents/${agentId}/versions`) return jsonResponse({ items: [] });
    if (url.pathname === "/api/v1/discord/connections" || url.pathname === "/api/v1/discord/bindings") return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <AgentConfigurationPage agentId={agentId} kind="detail" />);

  await user.click(await screen.findByRole("button", { name: "Activate Agent" }));
  expect(await screen.findByText("Candidate Agent is not ready.")).toBeInTheDocument();
  expect(within(screen.getByRole("alert")).getByText("Endpoint unavailable")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Activate Agent" })).toBeDisabled();
  expect(screen.queryByRole("button", { name: "Archive Agent" })).not.toBeInTheDocument();
});

test("detail page replaces the complete configuration with expected version, then activates only when ready", async () => {
  let current = agent();
  const originalConfiguration = current.current_version.configuration;
  const requests: Request[] = [];
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/auth/session") return jsonResponse(session());
    if (url.pathname === "/api/v1/knowledge-bases") return jsonResponse([knowledgeBase(firstKnowledgeId, "Alpha docs"), knowledgeBase(secondKnowledgeId, "Beta docs")]);
    if (url.pathname === "/api/v1/model-profiles") return jsonResponse([modelProfile()]);
    if (url.pathname === "/api/v1/provider-endpoints") return jsonResponse([providerEndpoint()]);
    if (url.pathname === `/api/v1/agents/${agentId}` && request.method === "GET") return jsonResponse(current);
    if (url.pathname === `/api/v1/agents/${agentId}/readiness`) return jsonResponse(readiness());
    if (url.pathname === `/api/v1/agents/${agentId}/configuration` && request.method === "PUT") {
      const body: unknown = await request.json();
      if (!isRecord(body) || !isRecord(body.configuration)) throw new Error("replacement body is invalid");
      current = nextAgent(current, body.configuration, current.version + 1);
      return jsonResponse(current);
    }
    if (url.pathname === `/api/v1/agents/${agentId}/lifecycle` && request.method === "PATCH") {
      current = { ...current, activated_at: "2026-08-31T18:00:00Z", lifecycle: "active", version: current.version + 1 };
      return jsonResponse(current);
    }
    if (url.pathname === `/api/v1/agents/${agentId}/runs`) return jsonResponse({ items: [] });
    if (url.pathname === `/api/v1/agents/${agentId}/versions`) return jsonResponse({ items: [current.current_version] });
    if (url.pathname === "/api/v1/discord/connections" || url.pathname === "/api/v1/discord/bindings") return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <AgentConfigurationPage agentId={agentId} kind="detail" />);

  expect(await screen.findByText("Ready for delivery")).toBeInTheDocument();
  expect(screen.getByText("restricted access")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Save new version" })).toBeDisabled();
  await user.clear(screen.getByLabelText("Display name"));
  await user.type(screen.getByLabelText("Display name"), "Reordered Agent");
  await user.click(screen.getByRole("button", { name: "Move Beta docs up" }));
  await user.click(screen.getByRole("button", { name: "Save new version" }));

  const replaceRequest = await waitFor(() => findRequest(requests, `/api/v1/agents/${agentId}/configuration`, "PUT"));
  expect(await replaceRequest.json()).toEqual({
    configuration: {
      ...originalConfiguration,
      display_name: "Reordered Agent",
      knowledge_base_ids: [secondKnowledgeId, firstKnowledgeId],
    },
    expected_version: 4,
  });
  expect(replaceRequest.headers.get("X-CSRF-Token")).toBe(csrfSentinel);
  expect(replaceRequest.headers.get("Idempotency-Key")).toBeTruthy();

  await user.click(await screen.findByRole("button", { name: "Activate Agent" }));
  const lifecycleRequest = await waitFor(() => findRequest(requests, `/api/v1/agents/${agentId}/lifecycle`, "PATCH"));
  expect(await lifecycleRequest.json()).toEqual({ expected_version: 5, lifecycle: "active" });
  expect(await screen.findByRole("button", { name: "Archive Agent" })).toBeInTheDocument();
});

test("an unready draft explains its issue and cannot be activated", async () => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/auth/session") return jsonResponse(session());
    if (url.pathname === "/api/v1/knowledge-bases") return jsonResponse([knowledgeBase(firstKnowledgeId, "Alpha docs"), knowledgeBase(secondKnowledgeId, "Beta docs")]);
    if (url.pathname === "/api/v1/model-profiles") return jsonResponse([modelProfile()]);
    if (url.pathname === "/api/v1/provider-endpoints") return jsonResponse([providerEndpoint()]);
    if (url.pathname === `/api/v1/agents/${agentId}`) return jsonResponse(agent());
    if (url.pathname === `/api/v1/agents/${agentId}/readiness`) return jsonResponse({ ...readiness(), issues: [{ code: "knowledge_base_unpublished", knowledge_base_id: firstKnowledgeId }], ready: false });
    if (url.pathname === `/api/v1/agents/${agentId}/runs` || url.pathname === `/api/v1/agents/${agentId}/versions`) return jsonResponse({ items: [] });
    if (url.pathname === "/api/v1/discord/connections" || url.pathname === "/api/v1/discord/bindings") return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  renderAuthenticated(testQueryClient(), <AgentConfigurationPage agentId={agentId} kind="detail" />);

  expect(await screen.findByText("Configuration needs attention")).toBeInTheDocument();
  expect(screen.getByText(new RegExp(`Knowledge base unpublished · ${firstKnowledgeId}`))).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Activate Agent" })).toBeDisabled();
  expect(screen.getByText(/Resolve every readiness issue before activation/)).toBeInTheDocument();
});

test("Agent, run, and version ledgers use explicit cursor pagination with 50/25/25 limits", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/agents") {
      return jsonResponse(url.searchParams.has("cursor")
        ? { items: [agent("second-agent")] }
        : { items: [agent()], next_cursor: "agents-next" });
    }
    if (url.pathname === `/api/v1/agents/${agentId}/runs`) return jsonResponse(url.searchParams.has("cursor") ? { items: [] } : { items: [], next_cursor: "runs-next" });
    if (url.pathname === `/api/v1/agents/${agentId}/versions`) return jsonResponse(url.searchParams.has("cursor") ? { items: [] } : { items: [], next_cursor: "versions-next" });
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  const user = userEvent.setup();
  const client = testQueryClient();
  renderWithClient(client, <><AgentsPage /><AgentRunsPanel agentId={agentId} /></>);

  await user.click(await screen.findByRole("button", { name: "Load more Agents" }));
  await user.click(await screen.findByRole("button", { name: "Load more runs" }));
  await user.click(await screen.findByRole("button", { name: "Load more versions" }));
  await waitFor(() => expect(requests.filter((request) => new URL(request.url).searchParams.has("cursor"))).toHaveLength(3));

  expectQuery(requests, "/api/v1/agents", "50", "agents-next");
  expectQuery(requests, `/api/v1/agents/${agentId}/runs`, "25", "runs-next");
  expectQuery(requests, `/api/v1/agents/${agentId}/versions`, "25", "versions-next");
});

async function addKnowledge(user: ReturnType<typeof userEvent.setup>, id: string): Promise<void> {
  await user.selectOptions(screen.getByLabelText("Knowledge base"), id);
  await user.click(screen.getByRole("button", { name: "Add knowledge base" }));
}

function renderAuthenticated(queryClient: QueryClient, page: ReactNode): void {
  function Gate(): ReactNode {
    const { state } = useAuth();
    return state.kind === "authenticated" ? page : <p>{state.kind}</p>;
  }
  renderWithClient(queryClient, <AuthProvider><Gate /></AuthProvider>);
}

function renderWithClient(queryClient: QueryClient, page: ReactNode): void {
  const rootRoute = createRootRoute({ component: () => page });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  render(<QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider>);
}

function testQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

function agent(key = "support-agent"): Agent {
  const configuration: AgentConfigurationInput = {
    answer_mode: "tool_calling",
    behavioral_instructions: "",
    description: "Answers support questions.",
    display_name: key === "support-agent" ? "Support Agent" : "Second Agent",
    evidence_access: "wiki_only",
    identity_instructions: "Answer as the support specialist.",
    knowledge_base_ids: [firstKnowledgeId, secondKnowledgeId],
    max_answer_tokens: 2_048,
    max_tool_calls: 8,
    model_profile_id: modelId,
    reasoning_effort: "none",
    refusal_markdown: "I cannot answer that from the available evidence.",
    response_language: "en",
  };
  return {
    activated_at: null,
    archived_at: null,
    created_at: "2026-08-31T12:00:00Z",
    current_version: {
      agent_id: agentId,
      configuration: { ...configuration, behavioral_instructions: configuration.behavioral_instructions ?? "", description: configuration.description ?? "" },
      created_at: "2026-08-31T12:00:00Z",
      created_by_operator_id: "00000000-0000-0000-0000-000000000001",
      id: agentVersionId,
      version_number: 1,
    },
    current_version_id: agentVersionId,
    id: key === "support-agent" ? agentId : "00000000-0000-0000-0000-000000000012",
    key,
    lifecycle: "draft",
    selector: `agent:${key}`,
    updated_at: "2026-08-31T12:00:00Z",
    version: 4,
  };
}

function nextAgent(current: Agent, configuration: Record<string, unknown>, version: number): Agent {
  const typed = configurationInput(configuration);
  return {
    ...current,
    current_version: {
      ...current.current_version,
      configuration: { ...typed, behavioral_instructions: typed.behavioral_instructions ?? "", description: typed.description ?? "" },
      id: "00000000-0000-0000-0000-000000000013",
      version_number: current.current_version.version_number + 1,
    },
    current_version_id: "00000000-0000-0000-0000-000000000013",
    updated_at: "2026-08-31T17:00:00Z",
    version,
  };
}

function configurationInput(value: Record<string, unknown>): AgentConfigurationInput {
  const fallback = agent().current_version.configuration;
  return {
    answer_mode: value.answer_mode === "single_pass" ? "single_pass" : "tool_calling",
    behavioral_instructions: typeof value.behavioral_instructions === "string" ? value.behavioral_instructions : "",
    description: typeof value.description === "string" ? value.description : "",
    display_name: typeof value.display_name === "string" ? value.display_name : fallback.display_name,
    evidence_access: value.evidence_access === "wiki_and_source" ? "wiki_and_source" : "wiki_only",
    identity_instructions: typeof value.identity_instructions === "string" ? value.identity_instructions : fallback.identity_instructions,
    knowledge_base_ids: Array.isArray(value.knowledge_base_ids) && value.knowledge_base_ids.every((id) => typeof id === "string") ? value.knowledge_base_ids : fallback.knowledge_base_ids,
    max_answer_tokens: typeof value.max_answer_tokens === "number" ? value.max_answer_tokens : fallback.max_answer_tokens,
    max_tool_calls: typeof value.max_tool_calls === "number" ? value.max_tool_calls : fallback.max_tool_calls,
    model_profile_id: typeof value.model_profile_id === "string" ? value.model_profile_id : fallback.model_profile_id,
    reasoning_effort: value.reasoning_effort === "minimal" || value.reasoning_effort === "low" || value.reasoning_effort === "medium" || value.reasoning_effort === "high" || value.reasoning_effort === "max" ? value.reasoning_effort : "none",
    refusal_markdown: typeof value.refusal_markdown === "string" ? value.refusal_markdown : fallback.refusal_markdown,
    response_language: typeof value.response_language === "string" ? value.response_language : fallback.response_language,
  };
}

function readiness(): object {
  return { effective_access: "restricted", endpoint_configuration_version: 2, issues: [], model_profile_version_id: "00000000-0000-0000-0000-000000000022", model_profile_version_number: 3, provider_endpoint_id: endpointId, ready: true };
}

function knowledgeBase(id: string, name: string): KnowledgeBase {
  return { access: "restricted", archived_at: null, created_at: "2026-08-31T12:00:00Z", delete_requested_at: null, deleted_at: null, id, instructions: "", language: "en", lifecycle: "active", name, published_wiki_id: "00000000-0000-0000-0000-000000000040", purge_after: null, updated_at: "2026-08-31T12:00:00Z", version: 2 };
}

function modelProfile(id = modelId, endpoint = endpointId): ModelProfile {
  return {
    availability: "available",
    created_at: "2026-08-31T12:00:00Z",
    current_version: {
      configuration_version: 2,
      created_at: "2026-08-31T12:00:00Z",
      created_by_operator_id: null,
      id: "00000000-0000-0000-0000-000000000022",
      settings: { context_window_tokens: 16_000, extra_body: {}, max_concurrent_tasks: 1, max_output_tokens: 4_096, max_retries: 2, metadata_origin: {}, reasoning_mapping: null, reasoning_transport: "none", supports_streaming: true, supports_structured_output: true, supports_temperature: true, supports_tools: true, timeout_seconds: 60, transport: "chat_completions" },
      source: "operator",
      version_number: 3,
    },
    endpoint_id: endpoint,
    id,
    model_id: "support-model",
    updated_at: "2026-08-31T12:00:00Z",
    version: 2,
  };
}

function candidateNotReadyProblem(code: string, knowledgeBaseID?: string): Response {
  return problemResponse(409, {
    code: "candidate_not_ready",
    detail: "The candidate Agent configuration is not ready.",
    instance: `/api/v1/agents/${agentId}`,
    readiness: {
      effective_access: "restricted",
      issues: [{ code, ...(knowledgeBaseID ? { knowledge_base_id: knowledgeBaseID } : {}) }],
      ready: false,
    },
    status: 409,
    title: "Conflict",
    type: "about:blank",
  });
}

function problemResponse(status: number, body: unknown = { detail: "private detail", status, title: "Conflict", type: "about:blank" }): Response {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/problem+json" }, status });
}

function providerEndpoint(id = endpointId, displayName = "Primary provider"): ProviderEndpoint {
  return {
    allow_http: false,
    allow_private_network: false,
    archived_at: null,
    base_url: "https://models.example.test",
    chat_completions_path: "chat/completions",
    configuration_version: 2,
    created_at: "2026-08-31T12:00:00Z",
    credential_id: null,
    display_name: displayName,
    headers: {},
    health: "healthy",
    health_checked_at: "2026-08-31T12:00:00Z",
    id,
    lifecycle: "active",
    models_path: "models",
    responses_path: "responses",
    updated_at: "2026-08-31T12:00:00Z",
    version: 2,
  };
}

function session(): object {
  return { csrf_token: csrfSentinel, expires_at: "2026-09-01T00:00:00Z", operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" } };
}

function expectQuery(requests: Request[], path: string, limit: string, cursor: string): void {
  const matching = requests.filter((request) => new URL(request.url).pathname === path);
  expect(matching).toHaveLength(2);
  expect(new URL(matching[0]?.url ?? "http://invalid").searchParams.get("limit")).toBe(limit);
  expect(new URL(matching[0]?.url ?? "http://invalid").searchParams.has("cursor")).toBe(false);
  expect(new URL(matching[1]?.url ?? "http://invalid").searchParams.get("cursor")).toBe(cursor);
}

function findRequest(requests: Request[], path: string, method: string): Request {
  const request = requests.find((candidate) => new URL(candidate.url).pathname === path && candidate.method === method);
  if (!request) throw new Error(`Missing ${method} ${path}`);
  return request;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" }, status });
}

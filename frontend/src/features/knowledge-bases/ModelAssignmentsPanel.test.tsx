import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRouter } from "@tanstack/react-router";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";

import type { ModelProfile, ModelRole, ProviderEndpoint } from "../../api/client";
import { AuthProvider, useAuth } from "../../app/auth";
import { ModelAssignmentsPanel, assessEligibility } from "./ModelAssignmentsPanel";

const csrfSentinel = "csrf-assignment-cache-sentinel";
const knowledgeBaseId = "00000000-0000-0000-0000-000000000010";
const secondEndpointId = "00000000-0000-0000-0000-000000000021";
const secondModelId = "00000000-0000-0000-0000-000000000032";
const documentationRoles: ModelRole[] = ["documentation_planner", "documentation_writer"];
const queryFailures = [
  { label: "models", path: "/api/v1/model-profiles" },
  { label: "endpoints", path: "/api/v1/provider-endpoints" },
  { label: "assignments", path: `/api/v1/knowledge-bases/${knowledgeBaseId}/model-assignments` },
] satisfies ReadonlyArray<{ label: string; path: string }>;

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("eligibility explains role mode and capability constraints", () => {
  const withoutTools = model(false);
  const endpointRecord = endpoint();
	const planner = assessEligibility(withoutTools, endpointRecord, "none", "tool_calling");
  expect(planner.eligible).toBe(false);
  expect(planner.reasons).toContain("Planner and writer roles require confirmed tool support.");

  const withoutStructuredOutput = model(true, false);
	const unstructuredPlanner = assessEligibility(withoutStructuredOutput, endpointRecord, "none", "tool_calling");
  expect(unstructuredPlanner.eligible).toBe(false);
  expect(unstructuredPlanner.reasons).toContain("Planner and writer roles require confirmed structured output.");

	expect(assessEligibility(model(true), endpointRecord, "none", "single_pass").reasons).toContain("Planner and writer roles require tool-calling mode.");
});

test.each(documentationRoles)("%s accepts supported high reasoning", () => {
	expect(assessEligibility(model(true), endpoint(), "high", "tool_calling").eligible).toBe(true);
});

test("knowledge bases expose planner and writer routing while Agent answer models live on Agents", async () => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/model-profiles") return jsonResponse([model(true)]);
    if (path === "/api/v1/provider-endpoints") return jsonResponse([endpoint()]);
    if (path === `/api/v1/knowledge-bases/${knowledgeBaseId}/model-assignments`) return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  renderPanel(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

	expect(await screen.findByRole("heading", { name: "Documentation planner" })).toBeInTheDocument();
	expect(screen.getByRole("heading", { name: "Documentation writer" })).toBeInTheDocument();
	expect(screen.getAllByRole("button", { name: "Assign model" })).toHaveLength(2);
});

test.each([
  { buttonIndex: 0, role: "documentation_planner" },
  { buttonIndex: 1, role: "documentation_writer" },
] satisfies ReadonlyArray<{ buttonIndex: number; role: ModelRole }>)("$role submits its selected reasoning effort", async ({ buttonIndex, role }) => {
  let submitted: unknown;
  const fetchMock = vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/model-profiles") return jsonResponse([model(true)]);
    if (path === "/api/v1/provider-endpoints") return jsonResponse([endpoint()]);
    if (path === `/api/v1/knowledge-bases/${knowledgeBaseId}/model-assignments` && request.method === "GET") return jsonResponse([]);
    if (path.endsWith(`/model-assignments/${role}`) && request.method === "PUT") {
      submitted = await request.json();
      return jsonResponse({});
    }
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderPanel(queryClient);
  const user = userEvent.setup();

  const reasoningSelectors = await screen.findAllByLabelText("Reasoning effort");
  const assignButtons = screen.getAllByRole("button", { name: "Assign model" });
  const reasoningSelector = reasoningSelectors[buttonIndex];
  const assignButton = assignButtons[buttonIndex];
  if (reasoningSelector === undefined || assignButton === undefined) throw new Error(`${role} controls are missing`);
  await user.selectOptions(reasoningSelector, "high");
  await user.click(assignButton);

  await vi.waitFor(() => expect(submitted).toEqual({
    answer_mode: "tool_calling",
    expected_version: null,
    profile_id: model(true).id,
    reasoning_effort: "high",
  }));
});

test("duplicate model IDs remain distinguishable by provider endpoint and the selected route is receipted", async () => {
  let submitted: unknown;
  const primary = endpoint();
  const secondary = endpoint(secondEndpointId, "Secondary provider");
  const primaryModel = model(true);
  const secondaryModel = model(true, true, secondModelId, secondEndpointId);
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/model-profiles") return jsonResponse([primaryModel, secondaryModel]);
    if (path === "/api/v1/provider-endpoints") return jsonResponse([primary, secondary]);
    if (path === `/api/v1/knowledge-bases/${knowledgeBaseId}/model-assignments` && request.method === "GET") return jsonResponse([]);
    if (path.endsWith("/model-assignments/documentation_planner") && request.method === "PUT") {
      submitted = await request.json();
      return jsonResponse({});
    }
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  renderPanel(new QueryClient({ defaultOptions: { queries: { retry: false } } }));
  const user = userEvent.setup();

  const plannerCard = (await screen.findByRole("heading", { name: "Documentation planner" })).closest("article");
  if (!plannerCard) throw new Error("Planner assignment card is missing");
  const profile = within(plannerCard).getByLabelText("Model profile");
  expect(within(plannerCard).getByRole("option", { name: /Models · assignment-model · available · settings v1/ })).toBeInTheDocument();
  expect(within(plannerCard).getByRole("option", { name: /Secondary provider · assignment-model · available · settings v1/ })).toBeInTheDocument();
  await user.selectOptions(profile, secondModelId);
  const receipt = within(plannerCard).getByText("Selected endpoint and profile").parentElement;
  expect(receipt).toHaveTextContent("Secondary provider");
  expect(receipt).toHaveTextContent("assignment-model · available · settings v1");
  await user.click(within(plannerCard).getByRole("button", { name: "Assign model" }));

  await vi.waitFor(() => expect(submitted).toEqual({
    answer_mode: "tool_calling",
    expected_version: null,
    profile_id: secondModelId,
    reasoning_effort: "none",
  }));
});

test("assignment failures are accessible and cached variables contain no CSRF", async () => {
  const fetchMock = vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/model-profiles") return jsonResponse([model(true)]);
    if (path === "/api/v1/provider-endpoints") return jsonResponse([endpoint()]);
    if (path === `/api/v1/knowledge-bases/${knowledgeBaseId}/model-assignments` && request.method === "GET") return jsonResponse([]);
    if (path.includes("/model-assignments/documentation_planner") && request.method === "PUT") return jsonResponse({ title: "Conflict" }, 409);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderPanel(queryClient);
  const user = userEvent.setup();

  const assignButtons = await screen.findAllByRole("button", { name: "Assign model" });
  const first = assignButtons[0];
  if (first === undefined) throw new Error("Planner assignment button is missing");
  await user.click(first);

  expect(await screen.findByRole("alert")).toHaveTextContent("The record changed. Refresh and try again.");
  const retained = JSON.stringify(queryClient.getMutationCache().getAll().map((mutation) => mutation.state.variables));
  expect(retained).not.toContain(csrfSentinel);
});

test.each(queryFailures)("$label query failure is sanitized and accessible", async ({ path: failingPath }) => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === failingPath) return problemResponse(500);
    if (path === "/api/v1/model-profiles") return jsonResponse([model(true)]);
    if (path === "/api/v1/provider-endpoints") return jsonResponse([endpoint()]);
    if (path === `/api/v1/knowledge-bases/${knowledgeBaseId}/model-assignments`) return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderPanel(queryClient);

  expect(await screen.findByRole("alert")).toHaveTextContent("The operation could not be completed.");
  expect(screen.queryByText("private query detail")).not.toBeInTheDocument();
});

function renderPanel(queryClient: QueryClient): void {
  function Gate(): ReactNode {
    const { state } = useAuth();
    return state.kind === "authenticated"
      ? <ModelAssignmentsPanel knowledgeBaseId={knowledgeBaseId} mutable />
      : <p>{state.kind}</p>;
  }
  const rootRoute = createRootRoute({ component: Gate });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  render(<QueryClientProvider client={queryClient}><AuthProvider><RouterProvider router={router} /></AuthProvider></QueryClientProvider>);
}

function model(
  supportsTools: boolean,
  supportsStructuredOutput = true,
  id = "00000000-0000-0000-0000-000000000030",
  endpointId = endpoint().id,
): ModelProfile {
  return {
    availability: "available",
    created_at: "2026-08-28T00:00:00Z",
    current_version: {
      configuration_version: 1,
      created_at: "2026-08-28T00:00:00Z",
      created_by_operator_id: null,
      id: "00000000-0000-0000-0000-000000000031",
      settings: {
        context_window_tokens: 16_000,
        extra_body: {},
        max_output_tokens: 2_000,
        max_retries: 2,
        max_concurrent_tasks: 1,
        metadata_origin: {},
        reasoning_mapping: null,
        reasoning_transport: "reasoning_effort",
        supports_streaming: true,
        supports_structured_output: supportsStructuredOutput,
        supports_temperature: true,
        supports_tools: supportsTools,
        timeout_seconds: 60,
        transport: "chat_completions",
      },
      source: "operator",
      version_number: 1,
    },
    endpoint_id: endpointId,
    id,
    model_id: "assignment-model",
    updated_at: "2026-08-28T00:00:00Z",
    version: 1,
  };
}

function endpoint(id = "00000000-0000-0000-0000-000000000020", displayName = "Models"): ProviderEndpoint {
  return {
    allow_http: false,
    allow_private_network: false,
    archived_at: null,
    base_url: "https://models.example/v1",
    chat_completions_path: "chat/completions",
    configuration_version: 1,
    created_at: "2026-08-28T00:00:00Z",
    credential_id: null,
    display_name: displayName,
    headers: {},
    health: "unknown",
    health_checked_at: null,
    id,
    lifecycle: "active",
    models_path: "models",
    responses_path: "responses",
    updated_at: "2026-08-28T00:00:00Z",
    version: 1,
  };
}

function session(): object {
  return {
    csrf_token: csrfSentinel,
    expires_at: "2026-08-29T00:00:00Z",
    operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" },
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function problemResponse(status: number): Response {
  return new Response(JSON.stringify({ detail: "private query detail" }), {
    status,
    headers: { "Content-Type": "application/problem+json" },
  });
}

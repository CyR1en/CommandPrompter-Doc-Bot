import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";

import type { DocumentationRun, Job, KnowledgeBase, WikiResponse, WikiVersion } from "../../api/client";
import { connectEventStream } from "../../api/events";
import { queryKeys } from "../../api/queries";
import { AuthProvider, useAuth } from "../../app/auth";
import { GenerateWikiPanel } from "./GenerateWikiPanel";
import { RunDetailPage } from "./RunDetailPage";
import { RunsPage } from "./RunsPage";
import { WikiPage, parseWikiSearch } from "./WikiPage";

const knowledgeBaseId = "00000000-0000-0000-0000-000000000010";
const runId = "00000000-0000-0000-0000-000000000020";
const jobId = "00000000-0000-0000-0000-000000000030";
const wikiVersionId = "00000000-0000-0000-0000-000000000040";
const csrfSentinel = "csrf-documentation-sentinel";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("run ledger shows phase, captured inputs, page progress, and knowledge-base filter", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/knowledge-bases") return jsonResponse([knowledgeBase()]);
    if (path === "/api/v1/runs") return jsonResponse([documentationRun()]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  renderPage(testQueryClient(), <RunsPage />);

  expect(await screen.findByRole("link", { name: "Product docs" })).toHaveAttribute("href", `/runs/${runId}`);
  expect(screen.getByText("generating")).toBeInTheDocument();
  expect(screen.getByText("1 sources · 2 models")).toBeInTheDocument();
  expect(screen.getByText("1 / 2")).toBeInTheDocument();
  await userEvent.setup().selectOptions(screen.getByLabelText("Knowledge base"), knowledgeBaseId);
  await waitFor(() => expect(requests.some((request) => new URL(request.url).searchParams.get("knowledge_base_id") === knowledgeBaseId)).toBe(true));
});

test("run detail exposes immutable captures, ordered page attempts, errors, and result", async () => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    if (new URL(request.url).pathname === `/api/v1/runs/${runId}`) return jsonResponse(documentationRun());
    throw new Error(`Unexpected test request: ${request.method} ${request.url}`);
  }));
  renderPage(testQueryClient(), <RunDetailPage runId={runId} />);

  expect(await screen.findByRole("heading", { name: /Run 00000000/i })).toBeInTheDocument();
  expect(screen.getByText("documentation planner")).toBeInTheDocument();
  expect(screen.getByText("documentation writer")).toBeInTheDocument();
  expect(screen.getByText("Architecture")).toBeInTheDocument();
  expect(screen.getByText("Page worker timed out.")).toBeInTheDocument();
  expect(screen.getByText("450 tokens · 3 calls")).toBeInTheDocument();
  expect(screen.getByText("120 tokens · 1 call")).toBeInTheDocument();
  expect(screen.getByText("130 tokens · 1 call")).toBeInTheDocument();
  expect(screen.getByText("200 tokens · 1 call")).toBeInTheDocument();
  expect(screen.getByRole("row", { name: /Architecture.*skipped.*2.*Open job/i })).toBeInTheDocument();
  expect(screen.getAllByRole("link", { name: "Open job" })).toHaveLength(2);
  expect(screen.getByText("Pending final validation")).toBeInTheDocument();
});

test("wiki reader selects versions and pages while exposing provenance, Claims, evidence, and export", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const url = new URL(request.url);
    if (url.pathname === "/api/v1/knowledge-bases") return jsonResponse([knowledgeBase()]);
    if (url.pathname.endsWith("/wiki/evidence")) return jsonResponse({ text: "def main():", start_line: 10, end_line: 24, truncated: false });
    if (url.pathname.endsWith("/wiki/versions")) return jsonResponse([wikiVersion()]);
    if (url.pathname.endsWith("/wiki")) return jsonResponse(wikiResponse(url.searchParams.get("slug") ?? "overview"));
    throw new Error(`Unexpected test request: ${request.method} ${url.pathname}`);
  }));
  const user = userEvent.setup();
  const wikiRouter = renderWiki();

  await waitFor(() => expect(wikiRouter.state.location.search.slug).toBe("overview"));
  await waitFor(() => expect(screen.getByRole("heading", { name: "Overview", level: 2 })).toBeInTheDocument());
  expect(screen.getByText("claim-overview")).toBeInTheDocument();
  expect(screen.getByText("src/main.py")).toBeInTheDocument();
  expect(screen.getByText(/repo:\/\//)).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Read evidence: src/main.py" }));
  expect(await screen.findByText("def main():")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Export Markdown bundle" })).toHaveAttribute("href", `/api/v1/knowledge-bases/${knowledgeBaseId}/wiki/export`);

  await user.selectOptions(screen.getByLabelText("Published version"), wikiVersionId);
  await user.click(screen.getByRole("link", { name: /Architecture/i }));
  expect(await screen.findByRole("heading", { name: "Architecture", level: 2 })).toBeInTheDocument();
  expect(requests.some((request) => {
    const url = new URL(request.url);
    return url.searchParams.get("version_id") === wikiVersionId && url.searchParams.get("slug") === "architecture";
  })).toBe(true);
  expect(screen.getByRole("link", { name: "Export Markdown bundle" })).toHaveAttribute("href", `/api/v1/knowledge-bases/${knowledgeBaseId}/wiki/export?version_id=${wikiVersionId}`);
  expect(wikiRouter.state.location.search).toMatchObject({ knowledge_base_id: knowledgeBaseId, version_id: wikiVersionId, slug: "architecture" });
});

test("knowledge-base generation uses protected idempotent write and returns a job notice", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === `/api/v1/knowledge-bases/${knowledgeBaseId}/generate`) return jsonResponse(job(), 202);
    if (path === `/api/v1/jobs/${jobId}`) return jsonResponse(job());
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const queryClient = testQueryClient();
  const user = userEvent.setup();
  renderAuthenticated(queryClient, <GenerateWikiPanel knowledgeBase={knowledgeBase()} />);

  await user.click(await screen.findByRole("button", { name: "Generate first wiki" }));
  expect(await screen.findByText(/Documentation preparation job status: pending/i)).toBeInTheDocument();
  const request = requests.find((item) => new URL(item.url).pathname.endsWith("/generate"));
  expect(request).toBeDefined();
  expect(await request?.json()).toEqual({ expected_version: 3 });
  expect(request?.headers.get("X-CSRF-Token")).toBe(csrfSentinel);
  expect(request?.headers.get("Idempotency-Key")).toBeTruthy();
  expect(JSON.stringify(queryClient.getMutationCache().getAll().map((mutation) => mutation.state.variables))).not.toContain(csrfSentinel);
});

test("run, wiki, and job server events invalidate documentation queries", async () => {
  const stream = new FakeEventSource();
  vi.stubGlobal("EventSource", class { constructor() { return stream; } });
  const queryClient = testQueryClient();
  const invalidate = vi.spyOn(queryClient, "invalidateQueries");
  const close = connectEventStream(queryClient);

  stream.emit("documentation_run.generating");
  stream.emit("documentation_run.published");
  stream.emit("job.succeeded");
  await waitFor(() => {
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.runs });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.wiki });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.jobs });
  });
  close();
  expect(stream.closed).toBe(true);
});

function renderAuthenticated(queryClient: QueryClient, page: ReactNode): void {
  function Gate(): ReactNode {
    const { state } = useAuth();
    return state.kind === "authenticated" ? page : <p>{state.kind}</p>;
  }
  renderPage(queryClient, <AuthProvider><Gate /></AuthProvider>);
}

function renderPage(queryClient: QueryClient, page: ReactNode): void {
  const rootRoute = createRootRoute({ component: () => page });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  render(<QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider>);
}

function testQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

function knowledgeBase(): KnowledgeBase {
  return {
    access: "restricted",
    archived_at: null,
    created_at: "2026-08-28T12:00:00Z",
    delete_requested_at: null,
    deleted_at: null,
    id: knowledgeBaseId,
    instructions: "Document the architecture.",
    language: "en",
    lifecycle: "active",
    name: "Product docs",
    published_wiki_id: null,
    purge_after: null,
    updated_at: "2026-08-28T12:00:00Z",
    version: 3,
  };
}

function documentationRun(): DocumentationRun {
  return {
    completed_at: null,
    created_at: "2026-08-28T13:00:00Z",
    id: runId,
    instructions: "Document the architecture.",
    knowledge_base_id: knowledgeBaseId,
    knowledge_base_version: 3,
    language: "en",
    models: [
      { captured_credential_version: 3, captured_endpoint_configuration_version: 5, model_profile_id: "00000000-0000-0000-0000-000000000071", model_profile_version_id: "00000000-0000-0000-0000-000000000072", profile_version: 2, provider_endpoint_id: "00000000-0000-0000-0000-000000000075", role: "documentation_planner" },
      { captured_credential_version: 3, captured_endpoint_configuration_version: 5, model_profile_id: "00000000-0000-0000-0000-000000000073", model_profile_version_id: "00000000-0000-0000-0000-000000000074", profile_version: 4, provider_endpoint_id: "00000000-0000-0000-0000-000000000075", role: "documentation_writer" },
    ],
    planner_usage: { input_tokens: 90, model_calls: 1, output_tokens: 30, total_tokens: 120, truncated_tool_results: 0 },
    usage: { input_tokens: 320, model_calls: 3, output_tokens: 130, total_tokens: 450, truncated_tool_results: 1 },
    pages: [
      { attempt_count: 1, claims_sha256: "cd".repeat(32), completed_at: "2026-08-28T13:04:00Z", content_sha256: "ab".repeat(32), created_at: "2026-08-28T13:01:00Z", id: "00000000-0000-0000-0000-000000000081", job_id: "00000000-0000-0000-0000-000000000083", position: 0, purpose: "System overview", related_pages: ["architecture"], sanitized_error: null, slug: "overview", source_seed_paths: [{ source_id: "00000000-0000-0000-0000-000000000061", path: "README.md" }], status: "complete", submission_digest: "ef".repeat(32), title: "Overview", updated_at: "2026-08-28T13:04:00Z", usage: { input_tokens: 100, model_calls: 1, output_tokens: 30, total_tokens: 130, truncated_tool_results: 1 } },
      { attempt_count: 2, claims_sha256: null, completed_at: "2026-08-28T13:06:00Z", content_sha256: null, created_at: "2026-08-28T13:01:00Z", id: "00000000-0000-0000-0000-000000000082", job_id: "00000000-0000-0000-0000-000000000084", position: 1, purpose: "Runtime architecture", related_pages: ["overview"], sanitized_error: "Page worker timed out.", slug: "architecture", source_seed_paths: [{ source_id: "00000000-0000-0000-0000-000000000061", path: "src/main.py" }], status: "skipped", submission_digest: null, title: "Architecture", updated_at: "2026-08-28T13:06:00Z", usage: { input_tokens: 130, model_calls: 1, output_tokens: 70, total_tokens: 200, truncated_tool_results: 0 } },
    ],
    plan_digest: "12".repeat(32),
    prepare_job_id: jobId,
    prior_wiki_version_id: null,
    published_wiki_version_id: null,
    sanitized_error: null,
    sources: [{ commit: "a".repeat(40), fingerprint: "34".repeat(32), source_id: "00000000-0000-0000-0000-000000000061", source_revision_id: "00000000-0000-0000-0000-000000000062" }],
    status: "generating",
    updated_at: "2026-08-28T13:06:00Z",
  };
}

function wikiVersion(): WikiVersion {
  return { artifact_key: `knowledge-bases/${knowledgeBaseId}/wiki/${wikiVersionId}`, created_at: "2026-08-28T14:00:00Z", documentation_run_id: runId, id: wikiVersionId, knowledge_base_id: knowledgeBaseId, manifest_sha256: "56".repeat(32), page_count: 2, published_at: "2026-08-28T14:00:00Z" };
}

function wikiResponse(slug: string): WikiResponse {
  const architecture = slug === "architecture";
  const title = architecture ? "Architecture" : "Overview";
  return {
    page: {
      claims: [{ evidence: [{ commit: "a".repeat(40), end_line: 24, id: "evidence-overview", path: "src/main.py", resource: `repo://00000000-0000-0000-0000-000000000061@${"a".repeat(40)}/src/main.py#L10-L24`, source_fingerprint: "34".repeat(32), source_id: "00000000-0000-0000-0000-000000000061", source_revision_id: "00000000-0000-0000-0000-000000000062", start_line: 10 }], id: "claim-overview", statement: "The application starts from the main module." }],
      claims_sha256: "78".repeat(32),
      content_sha256: "90".repeat(32),
      description: `${title} of the product.`,
      markdown: `# ${title}\n\nThe application starts here.[^claim-overview]`,
      page_type: architecture ? "Architecture" : "Concept",
      slug: architecture ? "architecture" : "overview",
      title,
    },
    pages: [
      { description: "Overview of the product.", page_type: "Concept", slug: "overview", title: "Overview" },
      { description: "Architecture of the product.", page_type: "Architecture", slug: "architecture", title: "Architecture" },
    ],
    version: wikiVersion(),
  };
}

function job(): Job {
  return { attempt_count: 0, created_at: "2026-08-28T13:00:00Z", finished_at: null, id: jobId, job_type: "prepare_run", lease_expires_at: null, lease_generation: 0, lease_owner: null, max_attempts: 3, not_before: null, progress: 0, result: null, sanitized_error: null, started_at: null, status: "pending", target_id: knowledgeBaseId, target_type: "knowledge_base", updated_at: "2026-08-28T13:00:00Z" };
}

function session(): object {
  return { csrf_token: csrfSentinel, expires_at: "2026-08-29T00:00:00Z", operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" } };
}

class FakeEventSource {
  closed = false;
  private readonly listeners = new Map<string, Set<EventListener>>();

  addEventListener(name: string, listener: EventListener): void {
    const selected = this.listeners.get(name) ?? new Set<EventListener>();
    selected.add(listener);
    this.listeners.set(name, selected);
  }

  removeEventListener(name: string, listener: EventListener): void {
    this.listeners.get(name)?.delete(listener);
  }

  emit(name: string): void {
    const event = new MessageEvent(name, { data: JSON.stringify({ id: runId }) });
    for (const listener of this.listeners.get(name) ?? []) listener(event);
  }

  close(): void {
    this.closed = true;
  }
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function renderWiki(initial = "/wiki") {
 const root = createRootRoute();
 const wiki = createRoute({getParentRoute: () => root, path: "/wiki", validateSearch: parseWikiSearch, component: () => <WikiPage search={wiki.useSearch()} />});
 const router = createRouter({routeTree: root.addChildren([wiki]), history: createMemoryHistory({initialEntries:[initial]})});
 render(<QueryClientProvider client={testQueryClient()}><RouterProvider router={router} /></QueryClientProvider>);
 return router;
}

test("wiki deep links render Markdown safely and preserve version through internal links", async () => {
 vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
  const url = new URL(request.url);
  if(url.pathname === "/api/v1/knowledge-bases") return jsonResponse([knowledgeBase()]);
  if(url.pathname.endsWith("/wiki/versions")) return jsonResponse([wikiVersion()]);
  if(url.pathname.endsWith("/wiki")) {
   const response = wikiResponse(url.searchParams.get("slug") ?? "overview");
   if(response.page) response.page.markdown = "# Guide\n\n**Important** [Architecture](architecture.md)\n\n[Unsafe](javascript:alert(1))\n\n<script>alert(1)</script>\n\n![Tracker](https://tracker.invalid/pixel)\n\n| Setting | Value |\n| --- | --- |\n| Mode | safe |";
   return jsonResponse(response);
  }
  throw new Error("Unexpected request");
 }));
 const router=renderWiki(`/wiki?knowledge_base_id=${knowledgeBaseId}&version_id=${wikiVersionId}&slug=overview`);
 expect(await screen.findByRole("heading",{name:"Guide"})).toHaveAttribute("id","wiki-guide");
 expect(screen.getByText("Important").tagName).toBe("STRONG");
 expect(screen.getByRole("table")).toBeInTheDocument();
 expect(document.querySelector('script, img, a[href^="javascript:"]')).toBeNull();
 const links=screen.getAllByRole("link",{name:"Architecture"});
 const link = links[0];
 if (!link) throw new Error("Architecture link missing");
 await userEvent.setup().click(link);
 await waitFor(()=>expect(router.state.location.search).toMatchObject({knowledge_base_id:knowledgeBaseId,version_id:wikiVersionId,slug:"architecture"}));
 router.history.back();
 await waitFor(()=>expect(router.state.location.search.slug).toBe("overview"));
});

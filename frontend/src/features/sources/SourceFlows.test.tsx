import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRouter } from "@tanstack/react-router";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";

import { AuthProvider, useAuth } from "../../app/auth";
import { NewSourcePage } from "./NewSourcePage";
import { SourceDetailPage } from "./SourceDetailPage";
import { SourcesPage } from "./SourcesPage";

const sourceId = "00000000-0000-0000-0000-000000000030";
const credentialId = "00000000-0000-0000-0000-000000000020";
const knowledgeBaseId = "00000000-0000-0000-0000-000000000010";
const csrfSentinel = "csrf-source-sentinel";
const secretSentinel = "repository-secret-sentinel";
const validationJobId = "00000000-0000-0000-0000-000000000060";
const nativeVersion = "a".repeat(40);
const sourceFingerprint = "ab".repeat(32);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("private setup writes the secret only to credentials and keeps source mutation variables clean", async () => {
  const requests: Request[] = [];
  const fetchMock = vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/knowledge-bases") return jsonResponse([knowledgeBase()]);
    if (path === "/api/v1/credentials" && request.method === "GET") return jsonResponse([]);
    if (path === "/api/v1/credentials" && request.method === "POST") return jsonResponse(credential());
    if (path === "/api/v1/sources/repositories" && request.method === "POST") return jsonResponse(createdSource(), 201);
    if (path === `/api/v1/jobs/${validationJobId}`) return jsonResponse(job(validationJobId));
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  const queryClient = testQueryClient();
  const user = userEvent.setup();
  renderAuthenticated(queryClient, <NewSourcePage />);

  await screen.findByRole("option", { name: "Product docs · restricted" });
  await user.selectOptions(screen.getByLabelText("Knowledge base"), knowledgeBaseId);
  await user.type(screen.getByLabelText("Source name"), "Private product repository");
  await user.click(screen.getByLabelText("Private repository"));
  await user.type(screen.getByLabelText("HTTPS repository URL"), "https://git.example/acme/product.git");
  await user.type(screen.getByLabelText("HTTPS username"), "git-reader");
  await user.click(screen.getByLabelText("Add a write-only credential"));
  await user.type(screen.getByLabelText("Credential label"), "Product repository token");
  await user.type(screen.getByLabelText("Token or password"), secretSentinel);
  await user.type(screen.getByLabelText("Include patterns"), "src/**\ndocs/**");
  await user.type(screen.getByLabelText("Exclude patterns"), "vendor/**");
  await user.click(screen.getByRole("button", { name: "Save and validate repository" }));

  expect(await screen.findByRole("heading", { name: "Access validation enqueued" })).toBeInTheDocument();
  expect(screen.queryByDisplayValue(secretSentinel)).not.toBeInTheDocument();
  const credentialRequest = findRequest(requests, "/api/v1/credentials", "POST");
  const sourceRequest = findRequest(requests, "/api/v1/sources/repositories", "POST");
  expect(await credentialRequest.text()).toContain(secretSentinel);
  const sourceBody = await sourceRequest.text();
  expect(sourceBody).not.toContain(secretSentinel);
  expect(JSON.parse(sourceBody)).toMatchObject({
    credential_id: credentialId,
    credential_username: "git-reader",
    exclude_patterns: ["vendor/**"],
    include_patterns: ["src/**", "docs/**"],
    knowledge_base_id: knowledgeBaseId,
    poll_interval_seconds: 3600,
    privacy: "private",
    ref_kind: "branch",
    ref_value: "main",
  });
  const retainedVariables = JSON.stringify(queryClient.getMutationCache().getAll().map((mutation) => mutation.state.variables));
  expect(retainedVariables).not.toContain(secretSentinel);
  expect(retainedVariables).not.toContain(csrfSentinel);
});

test("website setup stores a write-only header and sends bounded crawl policy", async () => {
  const requests: Request[] = [];
  const websiteSecret = "website-header-secret-sentinel";
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/knowledge-bases") return jsonResponse([knowledgeBase()]);
    if (path === "/api/v1/credentials" && request.method === "GET") return jsonResponse([]);
    if (path === "/api/v1/credentials" && request.method === "POST") return jsonResponse(websiteCredential());
    if (path === "/api/v1/sources/websites" && request.method === "POST") return jsonResponse(createdWebsiteSource(), 201);
    if (path === `/api/v1/jobs/${validationJobId}`) return jsonResponse(job(validationJobId));
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const queryClient = testQueryClient();
  const user = userEvent.setup();
  renderAuthenticated(queryClient, <NewSourcePage />);

  await screen.findByRole("option", { name: "Product docs · restricted" });
  await user.click(screen.getByLabelText("Website"));
  await user.click(screen.getByRole("radio", { name: /^Website crawl/ }));
  await user.selectOptions(screen.getByLabelText("Knowledge base"), knowledgeBaseId);
  await user.type(screen.getByLabelText("Source name"), "Product website");
  await user.click(screen.getByLabelText("Private website"));
  await user.type(screen.getByLabelText("HTTPS website root URL"), "https://docs.example/product/");
  await user.click(screen.getByLabelText("Add a write-only credential"));
  await user.type(screen.getByLabelText("Credential label"), "Website bearer token");
  await user.type(screen.getByLabelText("Header value"), websiteSecret);
  await user.clear(screen.getByLabelText("Maximum pages"));
  await user.type(screen.getByLabelText("Maximum pages"), "250");
  await user.click(screen.getByRole("button", { name: "Save and validate website" }));

  expect(await screen.findByRole("heading", { name: "Access validation enqueued" })).toBeInTheDocument();
  const credentialRequest = findRequest(requests, "/api/v1/credentials", "POST");
  const sourceRequest = findRequest(requests, "/api/v1/sources/websites", "POST");
  expect(await credentialRequest.text()).toContain(websiteSecret);
  const sourceBody = await sourceRequest.text();
  expect(sourceBody).not.toContain(websiteSecret);
  expect(JSON.parse(sourceBody)).toMatchObject({
    credential_header: "Authorization",
    credential_id: credentialId,
    credential_prefix: "Bearer ",
    knowledge_base_id: knowledgeBaseId,
    max_concurrency: 4,
    max_depth: 3,
    max_pages: 250,
    poll_interval_seconds: 3600,
    privacy: "private",
    requests_per_second: 4,
    root_url: "https://docs.example/product/",
  });
  const retainedVariables = JSON.stringify(queryClient.getMutationCache().getAll().map((mutation) => mutation.state.variables));
  expect(retainedVariables).not.toContain(websiteSecret);
  expect(retainedVariables).not.toContain(csrfSentinel);
});

test("website built-in crawl stays default and submits explicit builtin mode", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/knowledge-bases") return jsonResponse([knowledgeBase()]);
    if (path === "/api/v1/credentials" && request.method === "GET") return jsonResponse([]);
    if (path === "/api/v1/sources/websites" && request.method === "POST") return jsonResponse(createdWebsiteSource({ acquisition_mode: "builtin_crawl", tinyfish_credential_id: null }), 201);
    if (path === `/api/v1/jobs/${validationJobId}`) return jsonResponse(job(validationJobId));
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <NewSourcePage />);

  await screen.findByRole("option", { name: "Product docs · restricted" });
  await user.click(screen.getByLabelText("Website"));
  await user.selectOptions(screen.getByLabelText("Knowledge base"), knowledgeBaseId);
  await user.type(screen.getByLabelText("Source name"), "Public docs site");
  expect(screen.getByLabelText("Public website")).toBeChecked();
  await user.type(screen.getByLabelText("HTTPS website root URL"), "https://docs.example/product/");
  await user.click(screen.getByRole("radio", { name: /^Website crawl/ }));
  await user.click(screen.getByRole("button", { name: "Save and validate website" }));

  expect(await screen.findByRole("heading", { name: "Access validation enqueued" })).toBeInTheDocument();
  const sourceBody = await findRequest(requests, "/api/v1/sources/websites", "POST").text();
  expect(JSON.parse(sourceBody)).toMatchObject({
    acquisition_mode: "builtin_crawl",
    tinyfish_credential_id: null,
    max_concurrency: 4,
    max_depth: 3,
    max_pages: 500,
    privacy: "public",
    root_url: "https://docs.example/product/",
  });
});

test("tinyfish mode forces public, filters credentials, creates key inline, and clears the secret", async () => {
  const requests: Request[] = [];
  const tinyfishSecret = "tinyfish-key-sentinel";
  const tinyfishCredential = {
    created_at: "2026-08-28T12:00:00Z",
    id: "00000000-0000-0000-0000-000000000021",
    key_id: "active",
    kind: "tinyfish_api_key",
    label: "TinyFish production key",
    masked_value: "••••",
    rotated_at: null,
    secret_version: 1,
  };
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/knowledge-bases") return jsonResponse([knowledgeBase()]);
    if (path === "/api/v1/credentials" && request.method === "GET") return jsonResponse([websiteCredential(), tinyfishCredential]);
    if (path === "/api/v1/credentials" && request.method === "POST") return jsonResponse({ ...tinyfishCredential, label: "TinyFish inline key" });
    if (path === "/api/v1/sources/websites" && request.method === "POST") return jsonResponse(createdWebsiteSource({ acquisition_mode: "tinyfish_crawl", privacy: "public", tinyfish_credential_id: tinyfishCredential.id, credential_id: null, credential_header: null, credential_prefix: null }), 201);
    if (path === `/api/v1/jobs/${validationJobId}`) return jsonResponse(job(validationJobId));
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const queryClient = testQueryClient();
  const user = userEvent.setup();
  renderAuthenticated(queryClient, <NewSourcePage />);

  await screen.findByRole("option", { name: "Product docs · restricted" });
  await user.click(screen.getByLabelText("Website"));
  await user.selectOptions(screen.getByLabelText("Knowledge base"), knowledgeBaseId);
  await user.type(screen.getByLabelText("Source name"), "Rendered docs site");
  await user.click(screen.getByRole("radio", { name: /^TinyFish crawl/ }));
  await user.click(screen.getByLabelText("Add a write-only TinyFish key"));
  await user.type(screen.getByLabelText("Credential label"), "TinyFish inline key");
  await user.type(screen.getByLabelText("TinyFish API key"), tinyfishSecret);
  await user.type(screen.getByLabelText("HTTPS website root URL"), "https://docs.example/app/");
  await user.click(screen.getByRole("button", { name: "Save and validate website" }));

  expect(await screen.findByRole("heading", { name: "Access validation enqueued" })).toBeInTheDocument();
  expect(screen.queryByDisplayValue(tinyfishSecret)).not.toBeInTheDocument();
  const credentialRequest = findRequest(requests, "/api/v1/credentials", "POST");
  const credentialBody = await credentialRequest.text();
  expect(credentialBody).toContain(tinyfishSecret);
  expect(JSON.parse(credentialBody)).toMatchObject({ kind: "tinyfish_api_key", label: "TinyFish inline key" });
  const sourceRequest = findRequest(requests, "/api/v1/sources/websites", "POST");
  const sourceBody = await sourceRequest.text();
  expect(sourceBody).not.toContain(tinyfishSecret);
  expect(JSON.parse(sourceBody)).toMatchObject({
    acquisition_mode: "tinyfish_crawl",
    privacy: "public",
    tinyfish_credential_id: tinyfishCredential.id,
    credential_id: null,
    credential_header: null,
    credential_prefix: null,
  });
  const retainedVariables = JSON.stringify(queryClient.getMutationCache().getAll().map((mutation) => mutation.state.variables));
  expect(retainedVariables).not.toContain(tinyfishSecret);
  expect(retainedVariables).not.toContain(csrfSentinel);

  cleanup();
  renderAuthenticated(testQueryClient(), <NewSourcePage />);
  await user.click(await screen.findByLabelText("Website"));
  await user.click(screen.getByRole("radio", { name: /^TinyFish crawl/ }));
  const options = within(screen.getByLabelText(/Stored TinyFish credential/i)).getAllByRole("option");
  expect(options.some((option) => option.textContent?.includes("Website bearer token"))).toBe(false);
  expect(options.some((option) => option.textContent?.includes("TinyFish production key"))).toBe(true);
});

test("direct json api submits fixed limits, no tinyfish credential, and unmodified endpoint URL", async () => {
  const requests: Request[] = [];
  const paperMcUrl = "https://fill.papermc.io/v3/projects";
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/knowledge-bases") return jsonResponse([knowledgeBase()]);
    if (path === "/api/v1/credentials" && request.method === "GET") return jsonResponse([]);
    if (path === "/api/v1/sources/websites" && request.method === "POST") return jsonResponse(createdWebsiteSource({ acquisition_mode: "direct_json_api", root_url: paperMcUrl, privacy: "public", max_pages: 1, max_depth: 0, tinyfish_credential_id: null }), 201);
    if (path === `/api/v1/jobs/${validationJobId}`) return jsonResponse(job(validationJobId));
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const queryClient = testQueryClient();
  const user = userEvent.setup();
  renderAuthenticated(queryClient, <NewSourcePage />);

  await screen.findByRole("option", { name: "Product docs · restricted" });
  await user.click(screen.getByLabelText("Website"));
  await user.selectOptions(screen.getByLabelText("Knowledge base"), knowledgeBaseId);
  await user.type(screen.getByLabelText("Source name"), "PaperMC projects index");
  await user.click(screen.getByRole("radio", { name: /^Direct JSON API/ }));
  await user.type(screen.getByLabelText("HTTPS website root URL"), paperMcUrl);
  await user.click(screen.getByRole("button", { name: "Save and validate website" }));

  expect(await screen.findByRole("heading", { name: "Access validation enqueued" })).toBeInTheDocument();
  const sourceBody = await findRequest(requests, "/api/v1/sources/websites", "POST").text();
  expect(JSON.parse(sourceBody)).toMatchObject({
    acquisition_mode: "direct_json_api",
    max_pages: 1,
    max_depth: 0,
    privacy: "public",
    root_url: paperMcUrl,
    tinyfish_credential_id: null,
    credential_id: null,
  });
  expect(sourceBody).not.toContain("tinyfish_api_key");
  expect(screen.queryByLabelText("Maximum pages")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("Maximum crawl depth")).not.toBeInTheDocument();
});

test("switching acquisition modes clears incompatible credential state", async () => {
  const tinyfishCredential = {
    created_at: "2026-08-28T12:00:00Z",
    id: "00000000-0000-0000-0000-000000000021",
    key_id: "active",
    kind: "tinyfish_api_key",
    label: "TinyFish production key",
    masked_value: "••••",
    rotated_at: null,
    secret_version: 1,
  };
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/knowledge-bases") return jsonResponse([knowledgeBase()]);
    if (path === "/api/v1/credentials") return jsonResponse([tinyfishCredential]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <NewSourcePage />);

  await screen.findByRole("option", { name: "Product docs · restricted" });
  await user.selectOptions(screen.getByLabelText("Knowledge base"), knowledgeBaseId);
  await user.click(screen.getByLabelText("Website"));
  await user.click(screen.getByRole("radio", { name: /^TinyFish crawl/ }));
  await user.click(screen.getByLabelText("Add a write-only TinyFish key"));
  await user.type(screen.getByLabelText("Credential label"), "Stale key");
  await user.type(screen.getByLabelText("TinyFish API key"), "tf-key-stale");
  await user.click(screen.getByRole("radio", { name: /^Website crawl/ }));
  await user.click(screen.getByRole("radio", { name: /^TinyFish crawl/ }));
  expect(screen.queryByDisplayValue("tf-key-stale")).not.toBeInTheDocument();
  expect(screen.getByLabelText("Stored TinyFish credential")).toHaveValue("");
});

test("detail joins current revision and successful sync history into the source ledger", async () => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/credentials") return jsonResponse([credential()]);
    if (path === `/api/v1/sources/${sourceId}`) return jsonResponse(source());
    if (path === `/api/v1/sources/${sourceId}/revisions`) return jsonResponse([revision({
      native_version: "b".repeat(64),
      observed_ref: "https://docs.example/product/",
      observed_ref_kind: "root",
      website_pages: [{
        canonical_url: "https://docs.example/product/guide",
        content_path: "pages/guide.md",
        content_sha256: "cd".repeat(32),
        evidence_uri: `web://${sourceId}@${"b".repeat(64)}/https%3A%2F%2Fdocs.example%2Fproduct%2Fguide`,
        etag: '"guide"',
        freshness: "fresh",
        last_modified: null,
        reused_from_revision_id: null,
      }],
    })]);
    if (path === `/api/v1/sources/${sourceId}/syncs`) return jsonResponse([successfulSync()]);
    if (path === "/api/v1/runs") return jsonResponse([documentationRun()]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  renderAuthenticated(testQueryClient(), <SourceDetailPage sourceId={sourceId} />);

  expect(await screen.findByRole("heading", { name: "Private product repository" })).toBeInTheDocument();
  expect(screen.getByText("git.example")).toBeInTheDocument();
  expect(screen.getByText("acme/product.git")).toBeInTheDocument();
  expect(screen.getAllByText("main").length).toBeGreaterThan(0);
  expect(await screen.findByRole("link", { name: "Open run" })).toHaveAttribute("href", `/runs/${documentationRun().id}`);
  expect(screen.getAllByText(nativeVersion).length).toBeGreaterThan(1);
  expect(screen.getByText(sourceFingerprint)).toBeInTheDocument();
  expect(screen.getByText(/healthy · checked/i)).toBeInTheDocument();
  expect(screen.getByText(/4.0 KiB/i)).toBeInTheDocument();
  expect(screen.getAllByText(/Product repository token · ••••/i).length).toBeGreaterThan(0);
  expect(screen.queryByText(credentialId)).not.toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Open job" })).toHaveAttribute("href", `/jobs/${successfulSync().job_id}`);
  expect(screen.getByRole("heading", { name: "Recent captured runs" })).toBeInTheDocument();
  expect(screen.getByText("Published wiki version")).toBeInTheDocument();
});

test("validate and sync actions send the current source version with protected write headers", async () => {
  const requests: Request[] = [];
  const fetchMock = vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/credentials") return jsonResponse([credential()]);
    if (path === `/api/v1/sources/${sourceId}` && request.method === "GET") return jsonResponse(source());
    if (path === `/api/v1/sources/${sourceId}/revisions`) return jsonResponse([revision()]);
    if (path === `/api/v1/sources/${sourceId}/syncs`) return jsonResponse([successfulSync()]);
    if (path === "/api/v1/runs") return jsonResponse([]);
    if (path === `/api/v1/sources/${sourceId}/validate` && request.method === "POST") return jsonResponse(validationSync(), 202);
    if (path === `/api/v1/sources/${sourceId}/sync` && request.method === "POST") return jsonResponse(pendingSync(), 202);
    if (path.startsWith("/api/v1/jobs/")) return jsonResponse(job(path.split("/").at(-1) ?? ""));
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <SourceDetailPage sourceId={sourceId} />);

  await user.click(await screen.findByRole("button", { name: "Validate access" }));
  expect(await screen.findByText(/Source validation job status/i)).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Sync now" }));
  expect(await screen.findByText(/Source sync job status/i)).toBeInTheDocument();

  for (const path of [`/api/v1/sources/${sourceId}/validate`, `/api/v1/sources/${sourceId}/sync`]) {
    const request = findRequest(requests, path, "POST");
    expect(await request.json()).toEqual({ expected_version: source().version });
    expect(request.headers.get("X-CSRF-Token")).toBe(csrfSentinel);
    expect(request.headers.get("Idempotency-Key")).toBeTruthy();
  }
});

test("source configuration edit sends ref, filters, credential reference, and disabled polling", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/credentials") return jsonResponse([credential()]);
    if (path === `/api/v1/sources/${sourceId}` && request.method === "GET") return jsonResponse(source());
    if (path === `/api/v1/sources/${sourceId}` && request.method === "PATCH") return jsonResponse({ ...source(), lifecycle: "draft", ref_value: "release/2026", poll_interval_seconds: null, version: 5 });
    if (path === `/api/v1/sources/${sourceId}/revisions`) return jsonResponse([revision()]);
    if (path === `/api/v1/sources/${sourceId}/syncs`) return jsonResponse([successfulSync()]);
    if (path === "/api/v1/runs") return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <SourceDetailPage sourceId={sourceId} />);

  await user.click(await screen.findByText("Edit source configuration"));
  await user.clear(screen.getByLabelText("Branch name"));
  await user.type(screen.getByLabelText("Branch name"), "release/2026");
  await user.clear(screen.getByLabelText("Include patterns"));
  await user.type(screen.getByLabelText("Include patterns"), "app/**\ndocs/**");
  await user.click(screen.getByLabelText("Poll this repository"));
  await user.click(screen.getByRole("button", { name: "Save source changes" }));

  const request = await waitForRequest(requests, `/api/v1/sources/${sourceId}`, "PATCH");
  expect(await request.json()).toMatchObject({
    credential_id: credentialId,
    credential_username: "git-reader",
    expected_version: 4,
    include_patterns: ["app/**", "docs/**"],
    poll_interval_seconds: null,
    ref_kind: "branch",
    ref_value: "release/2026",
  });
  expect(request.headers.get("X-CSRF-Token")).toBe(csrfSentinel);
  expect(request.headers.get("Idempotency-Key")).toBeTruthy();
});

test("website detail shows masked credential metadata and edits crawl limits", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/credentials") return jsonResponse([websiteCredential()]);
    if (path === `/api/v1/sources/${sourceId}` && request.method === "GET") return jsonResponse(websiteSource());
    if (path === `/api/v1/sources/${sourceId}` && request.method === "PATCH") return jsonResponse({ ...websiteSource(), max_depth: 5, poll_interval_seconds: null, version: 5 });
    if (path === `/api/v1/sources/${sourceId}/revisions`) return jsonResponse([websiteRevision()]);
    if (path === `/api/v1/sources/${sourceId}/syncs`) return jsonResponse([websiteSync()]);
    if (path === "/api/v1/runs") return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <SourceDetailPage sourceId={sourceId} />);

  expect(await screen.findByRole("heading", { name: "Product website" })).toBeInTheDocument();
  expect(screen.getAllByText(/Website bearer token · •••• · Authorization/i).length).toBeGreaterThan(0);
  expect(screen.queryByText(credentialId)).not.toBeInTheDocument();
  expect(screen.getAllByText("Website crawl").length).toBeGreaterThan(0);
  expect(await screen.findByRole("link", { name: "https://docs.example/product/guide" })).toBeInTheDocument();
  expect(screen.getByText("fresh")).toBeInTheDocument();
  await user.click(screen.getByText("Edit website configuration"));
  expect(screen.getByLabelText("Acquisition method")).toHaveValue("builtin_crawl");
  await user.clear(screen.getByLabelText("Maximum depth"));
  await user.type(screen.getByLabelText("Maximum depth"), "5");
  await user.click(screen.getByLabelText("Poll this website"));
  await user.click(screen.getByRole("button", { name: "Save website changes" }));

  const request = await waitForRequest(requests, `/api/v1/sources/${sourceId}`, "PATCH");
  expect(await request.json()).toMatchObject({
    acquisition_mode: "builtin_crawl",
    credential_header: "Authorization",
    credential_id: credentialId,
    expected_version: 4,
    max_depth: 5,
    max_pages: 500,
    poll_interval_seconds: null,
    root_url: "https://docs.example/product/",
    tinyfish_credential_id: null,
  });
});

const tinyfishCredentialId = "00000000-0000-0000-0000-000000000022";

function tinyfishCredential(): object {
  return {
    created_at: "2026-08-28T12:00:00Z",
    id: tinyfishCredentialId,
    key_id: "active",
    kind: "tinyfish_api_key",
    label: "TinyFish production key",
    masked_value: "••••abcd",
    rotated_at: null,
    secret_version: 3,
  };
}

function tinyfishWebsiteSource(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return websiteSource({
    acquisition_mode: "tinyfish_crawl",
    credential_header: null,
    credential_id: null,
    credential_prefix: null,
    name: "Rendered docs",
    privacy: "public",
    tinyfish_credential_id: tinyfishCredentialId,
    ...overrides,
  });
}

test("tinyfish detail shows plain acquisition label and masked key metadata only", async () => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/credentials") return jsonResponse([tinyfishCredential()]);
    if (path === `/api/v1/sources/${sourceId}`) return jsonResponse(tinyfishWebsiteSource());
    if (path === `/api/v1/sources/${sourceId}/revisions`) return jsonResponse([websiteRevision()]);
    if (path === `/api/v1/sources/${sourceId}/syncs`) return jsonResponse([websiteSync()]);
    if (path === "/api/v1/runs") return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  renderAuthenticated(testQueryClient(), <SourceDetailPage sourceId={sourceId} />);

  expect(await screen.findByRole("heading", { name: "Rendered docs" })).toBeInTheDocument();
  expect(screen.getAllByText("TinyFish crawl").length).toBeGreaterThan(0);
  expect(screen.getByText(/TinyFish production key · ••••abcd · version 3/i)).toBeInTheDocument();
  expect(screen.queryByText(/tf-live-key/)).not.toBeInTheDocument();
  expect(screen.queryByText(tinyfishCredentialId)).not.toBeInTheDocument();
});

test("direct json detail shows acquisition label without TinyFish key row", async () => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/credentials") return jsonResponse([]);
    if (path === `/api/v1/sources/${sourceId}`) return jsonResponse(websiteSource({ acquisition_mode: "direct_json_api", credential_id: null, credential_header: null, credential_prefix: null, max_depth: 0, max_pages: 1, privacy: "public", tinyfish_credential_id: null }));
    if (path === `/api/v1/sources/${sourceId}/revisions`) return jsonResponse([websiteRevision()]);
    if (path === `/api/v1/sources/${sourceId}/syncs`) return jsonResponse([]);
    if (path === "/api/v1/runs") return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  renderAuthenticated(testQueryClient(), <SourceDetailPage sourceId={sourceId} />);

  expect(await screen.findByRole("heading", { name: "Product website" })).toBeInTheDocument();
  expect(screen.getAllByText("Direct JSON API").length).toBeGreaterThan(0);
  expect(screen.queryByText("TinyFish key")).not.toBeInTheDocument();
});

test("sync history shows captured acquisition mode and TinyFish key version", async () => {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/credentials") return jsonResponse([tinyfishCredential()]);
    if (path === `/api/v1/sources/${sourceId}`) return jsonResponse(tinyfishWebsiteSource());
    if (path === `/api/v1/sources/${sourceId}/revisions`) return jsonResponse([websiteRevision()]);
    if (path === `/api/v1/sources/${sourceId}/syncs`) return jsonResponse([websiteSync({ captured_acquisition_mode: "tinyfish_crawl", captured_tinyfish_credential_id: tinyfishCredentialId, captured_tinyfish_credential_version: 7 })]);
    if (path === "/api/v1/runs") return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  renderAuthenticated(testQueryClient(), <SourceDetailPage sourceId={sourceId} />);

  expect(await screen.findAllByText("TinyFish crawl").then((items) => items.length)).toBeGreaterThan(1);
  const capturedCell = screen.getByText(/TinyFish key version 7/i);
  expect(capturedCell).toBeInTheDocument();
  expect(screen.queryByText(/tf-key-version/)).not.toBeInTheDocument();
  expect(screen.queryByText(/website_sync:tinyfish_auth/)).not.toBeInTheDocument();
});

test("website edit switches to tinyfish and sends public mode with key reference and no secret", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/credentials") return jsonResponse([tinyfishCredential()]);
    if (path === `/api/v1/sources/${sourceId}` && request.method === "GET") return jsonResponse(websiteSource({ acquisition_mode: "builtin_crawl", privacy: "private" }));
    if (path === `/api/v1/sources/${sourceId}` && request.method === "PATCH") return jsonResponse({ ...websiteSource({ acquisition_mode: "tinyfish_crawl", privacy: "public", tinyfish_credential_id: tinyfishCredentialId, credential_id: null, credential_header: null, credential_prefix: null, max_pages: 500, max_depth: 3 }), version: 5 });
    if (path === `/api/v1/sources/${sourceId}/revisions`) return jsonResponse([revision()]);
    if (path === `/api/v1/sources/${sourceId}/syncs`) return jsonResponse([]);
    if (path === "/api/v1/runs") return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <SourceDetailPage sourceId={sourceId} />);

  await user.click(await screen.findByText("Edit website configuration"));
  await user.selectOptions(screen.getByLabelText("Acquisition method"), "tinyfish_crawl");
  await user.click(screen.getByRole("button", { name: "Save website changes" }));

  const request = await waitForRequest(requests, `/api/v1/sources/${sourceId}`, "PATCH");
  const body = await request.json();
  expect(body).toMatchObject({
    acquisition_mode: "tinyfish_crawl",
    credential_header: null,
    credential_id: null,
    credential_prefix: null,
    expected_version: 4,
    privacy: "public",
    tinyfish_credential_id: tinyfishCredentialId,
  });
  expect(JSON.stringify(body)).not.toContain("tf-key-secret-sentinel");
  expect(request.headers.get("X-CSRF-Token")).toBe(csrfSentinel);
  expect(request.headers.get("Idempotency-Key")).toBeTruthy();
});

test("direct json edit fixes crawl limits and clears TinyFish key", async () => {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/credentials") return jsonResponse([tinyfishCredential()]);
    if (path === `/api/v1/sources/${sourceId}` && request.method === "GET") return jsonResponse(websiteSource({ acquisition_mode: "tinyfish_crawl", privacy: "public", tinyfish_credential_id: tinyfishCredentialId }));
    if (path === `/api/v1/sources/${sourceId}` && request.method === "PATCH") return jsonResponse(websiteSource({ acquisition_mode: "direct_json_api", max_depth: 0, max_pages: 1, privacy: "public", tinyfish_credential_id: null, version: 5 }));
    if (path === `/api/v1/sources/${sourceId}/revisions`) return jsonResponse([websiteRevision()]);
    if (path === `/api/v1/sources/${sourceId}/syncs`) return jsonResponse([websiteSync()]);
    if (path === "/api/v1/runs") return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <SourceDetailPage sourceId={sourceId} />);

  await user.click(await screen.findByText("Edit website configuration"));
  await user.selectOptions(screen.getByLabelText("Acquisition method"), "direct_json_api");
  await user.click(screen.getByRole("button", { name: "Save website changes" }));

  const request = await waitForRequest(requests, `/api/v1/sources/${sourceId}`, "PATCH");
  expect(await request.json()).toMatchObject({
    acquisition_mode: "direct_json_api",
    expected_version: 4,
    max_depth: 0,
    max_pages: 1,
    privacy: "public",
    tinyfish_credential_id: null,
  });
  expect(screen.queryByLabelText("Maximum pages")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("Maximum depth")).not.toBeInTheDocument();
});

test("sanitized errors render concise operator messages and keep the stable code", async () => {
  const failing = websiteSource({ sanitized_error: "source_sync:tinyfish_auth" });
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/credentials") return jsonResponse([tinyfishCredential()]);
    if (path === `/api/v1/sources/${sourceId}`) return jsonResponse(failing);
    if (path === `/api/v1/sources/${sourceId}/revisions`) return jsonResponse([websiteRevision()]);
    if (path === `/api/v1/sources/${sourceId}/syncs`) return jsonResponse([websiteSync({ status: "failed", sanitized_error: "source_sync:website_robots" })]);
    if (path === "/api/v1/runs") return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  renderAuthenticated(testQueryClient(), <SourceDetailPage sourceId={sourceId} />);

  expect(await screen.findByRole("alert")).toHaveTextContent("TinyFish rejected the API key.");
  expect(screen.getByRole("alert")).toHaveTextContent("code: source_sync:tinyfish_auth");
  expect(screen.getByText(/robots.txt denied this crawl. \(code: source_sync:website_robots\)/i)).toBeInTheDocument();
});

test("lifecycle controls disable, reactivate, and confirm permanent removal", async () => {
  const requests: Request[] = [];
  let lifecycle = "active";
  let version = 4;
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    requests.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") return jsonResponse(session());
    if (path === "/api/v1/credentials") return jsonResponse([credential()]);
    if (path === `/api/v1/sources/${sourceId}` && request.method === "GET") return jsonResponse(source({ lifecycle, version }));
    if (path === `/api/v1/sources/${sourceId}/lifecycle` && request.method === "POST") {
      const body: { lifecycle: string } = await request.clone().json();
      lifecycle = body.lifecycle;
      version += 1;
      return jsonResponse(source({ lifecycle, version }));
    }
    if (path === `/api/v1/sources/${sourceId}/revisions`) return jsonResponse([revision()]);
    if (path === `/api/v1/sources/${sourceId}/syncs`) return jsonResponse([successfulSync()]);
    if (path === "/api/v1/runs") return jsonResponse([]);
    throw new Error(`Unexpected test request: ${request.method} ${path}`);
  }));
  const user = userEvent.setup();
  renderAuthenticated(testQueryClient(), <SourceDetailPage sourceId={sourceId} />);

  await user.click(await screen.findByRole("button", { name: "Disable source" }));
  await user.click(await screen.findByRole("button", { name: "Reactivate source" }));
  await user.click(await screen.findByRole("button", { name: "Remove source" }));
  const dialog = screen.getByRole("dialog");
  expect(dialog).toBeInTheDocument();
  await user.type(screen.getByLabelText(/Type Private product repository exactly/i), "Private product repository");
  await user.click(within(dialog).getByRole("button", { name: "Remove source" }));
  expect(await screen.findByText(/Existing published wiki revisions remain intact/i)).toBeInTheDocument();

  const lifecycleRequests = requests.filter((request) => new URL(request.url).pathname.endsWith("/lifecycle") && request.method === "POST");
  expect(lifecycleRequests).toHaveLength(3);
  expect(await lifecycleRequests[0]?.json()).toEqual({ expected_version: 4, lifecycle: "disabled" });
  expect(await lifecycleRequests[1]?.json()).toEqual({ expected_version: 5, lifecycle: "active" });
  expect(await lifecycleRequests[2]?.json()).toEqual({ expected_version: 6, lifecycle: "removed" });
  for (const request of lifecycleRequests) {
    expect(request.headers.get("X-CSRF-Token")).toBe(csrfSentinel);
    expect(request.headers.get("Idempotency-Key")).toBeTruthy();
  }
});

test("source register presents loading, empty, and sanitized error states", async () => {
  let finish: ((response: Response) => void) | undefined;
  vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>((resolve) => { finish = resolve; })));
  renderPage(testQueryClient(), <SourcesPage />);
  expect(await screen.findByText("Loading sources…")).toBeInTheDocument();
  finish?.(jsonResponse([]));
  expect(await screen.findByText(/No sources are configured/i)).toBeInTheDocument();

  cleanup();
  vi.stubGlobal("fetch", vi.fn(async () => problemResponse(500)));
  renderPage(testQueryClient(), <SourcesPage />);
  expect(await screen.findByRole("alert")).toHaveTextContent("The operation could not be completed.");
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

function findRequest(requests: Request[], path: string, method: string): Request {
  const request = requests.find((candidate) => new URL(candidate.url).pathname === path && candidate.method === method);
  if (!request) throw new Error(`Missing ${method} ${path}`);
  return request;
}

async function waitForRequest(requests: Request[], path: string, method: string): Promise<Request> {
  return vi.waitFor(() => findRequest(requests, path, method));
}

function session(): object {
  return {
    csrf_token: csrfSentinel,
    expires_at: "2026-08-29T00:00:00Z",
    operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" },
  };
}

function knowledgeBase(): object {
  return {
    access: "restricted",
    archived_at: null,
    created_at: "2026-08-28T12:00:00Z",
    delete_requested_at: null,
    deleted_at: null,
    id: knowledgeBaseId,
    instructions: "",
    language: "en",
    lifecycle: "active",
    name: "Product docs",
    published_wiki_id: null,
    purge_after: null,
    updated_at: "2026-08-28T12:00:00Z",
    version: 1,
  };
}

function credential(): object {
  return {
    created_at: "2026-08-28T12:00:00Z",
    id: credentialId,
    key_id: "active",
    kind: "repository_https",
    label: "Product repository token",
    masked_value: "••••",
    rotated_at: null,
    secret_version: 1,
  };
}

function websiteCredential(): object {
  return {
    ...credential(),
    kind: "website_header",
    label: "Website bearer token",
  };
}

function source(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    checked_at: "2026-08-28T13:00:00Z",
    configuration_version: 2,
    created_at: "2026-08-28T12:00:00Z",
    credential_id: credentialId,
    credential_username: "git-reader",
    current_revision_id: revision().id,
    disabled_at: null,
    exclude_patterns: ["vendor/**"],
    health: "healthy",
    id: sourceId,
    include_patterns: ["src/**", "docs/**"],
    kind: "repository",
    knowledge_base_id: knowledgeBaseId,
    lifecycle: "active",
    name: "Private product repository",
    poll_interval_seconds: 3600,
    privacy: "private",
    ref_kind: "branch",
    ref_value: "main",
    remote_host: "git.example",
    remote_url: "https://git.example/acme/product.git",
    removed_at: null,
    repository_path: "acme/product.git",
    sanitized_error: null,
    updated_at: "2026-08-28T13:00:00Z",
    validated_configuration_version: 2,
    version: 4,
    ...overrides,
  };
}

function websiteSource(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    acquisition_mode: "builtin_crawl",
    checked_at: "2026-08-28T13:00:00Z",
    configuration_version: 2,
    created_at: "2026-08-28T12:00:00Z",
    credential_header: "Authorization",
    credential_id: credentialId,
    credential_prefix: "Bearer ",
    current_revision_id: revision().id,
    disabled_at: null,
    health: "healthy",
    id: sourceId,
    kind: "website",
    knowledge_base_id: knowledgeBaseId,
    lifecycle: "active",
    max_concurrency: 4,
    max_depth: 3,
    max_page_bytes: 2097152,
    max_pages: 500,
    max_total_bytes: 104857600,
    name: "Product website",
    poll_interval_seconds: 3600,
    privacy: "private",
    removed_at: null,
    requests_per_second: 4,
    root_host: "docs.example",
    root_url: "https://docs.example/product/",
    sanitized_error: null,
    updated_at: "2026-08-28T13:00:00Z",
    validated_configuration_version: 2,
    version: 4,
    ...overrides,
  };
}

function revision(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    artifact_key: `sources/${sourceId}/snapshots/revision-1`,
    byte_count: 4096,
    created_at: "2026-08-28T13:00:00Z",
    file_count: 8,
    fingerprint: sourceFingerprint,
    id: "00000000-0000-0000-0000-000000000040",
    ignored_paths: ["vendor/cache.js"],
    native_version: nativeVersion,
    observed_ref: "main",
    observed_ref_kind: "branch",
    source_id: sourceId,
    website_pages: [],
    ...overrides,
  };
}

function websiteRevision(): Record<string, unknown> {
  const websiteVersion = "b".repeat(64);
  return revision({
    native_version: websiteVersion,
    observed_ref: "https://docs.example/product/",
    observed_ref_kind: "root",
    website_pages: [{
      canonical_url: "https://docs.example/product/guide",
      content_path: "pages/guide.md",
      content_sha256: "cd".repeat(32),
      evidence_uri: `web://${sourceId}@${websiteVersion}/https%3A%2F%2Fdocs.example%2Fproduct%2Fguide`,
      etag: '"guide"',
      freshness: "fresh",
      last_modified: null,
      reused_from_revision_id: null,
    }],
  });
}

function validationSync(): Record<string, string | null> {
  return {
    completed_at: null,
    created_at: "2026-08-28T12:01:00Z",
    id: "00000000-0000-0000-0000-000000000050",
    job_id: validationJobId,
    kind: "validation",
    resolved_native_version: null,
    sanitized_error: null,
    source_id: sourceId,
    started_at: null,
    status: "pending",
  };
}

function pendingSync(): Record<string, string | null> {
  return {
    ...validationSync(),
    id: "00000000-0000-0000-0000-000000000051",
    job_id: "00000000-0000-0000-0000-000000000061",
    kind: "sync",
  };
}

function successfulSync(): Record<string, string | null> {
  return {
    ...pendingSync(),
    completed_at: "2026-08-28T13:00:00Z",
    created_at: "2026-08-28T12:58:00Z",
    resolved_native_version: nativeVersion,
    started_at: "2026-08-28T12:59:00Z",
    status: "succeeded",
  };
}

function websiteSync(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    ...successfulSync(),
    captured_acquisition_mode: "builtin_crawl",
    captured_credential_header: null,
    captured_credential_id: null,
    captured_credential_prefix: null,
    captured_credential_version: null,
    captured_max_concurrency: 4,
    captured_max_depth: 3,
    captured_max_page_bytes: 2097152,
    captured_max_pages: 500,
    captured_max_total_bytes: 104857600,
    captured_previous_revision_id: null,
    captured_requests_per_second: 4,
    captured_root_url: "https://docs.example/product/",
    captured_tinyfish_credential_id: null,
    captured_tinyfish_credential_version: null,
    ...overrides,
  };
}

function documentationRun(): Record<string, unknown> {
  return {
    completed_at: "2026-08-28T14:05:00Z",
    created_at: "2026-08-28T14:00:00Z",
    id: "00000000-0000-0000-0000-000000000070",
    instructions: "",
    knowledge_base_id: knowledgeBaseId,
    knowledge_base_version: 4,
    language: "en",
    models: [],
    pages: [{
      attempt_count: 1,
      claims_sha256: "cd".repeat(32),
      completed_at: "2026-08-28T14:04:00Z",
      content_sha256: "ab".repeat(32),
      created_at: "2026-08-28T14:01:00Z",
      id: "00000000-0000-0000-0000-000000000071",
      job_id: "00000000-0000-0000-0000-000000000072",
      position: 0,
      purpose: "Overview",
      related_pages: [],
      sanitized_error: null,
      slug: "overview",
      source_seed_paths: [{ source_id: sourceId, path: "README.md" }],
      status: "complete",
      submission_digest: "ef".repeat(32),
      title: "Overview",
      updated_at: "2026-08-28T14:04:00Z",
    }],
    plan_digest: "12".repeat(32),
    prepare_job_id: "00000000-0000-0000-0000-000000000073",
    prior_wiki_version_id: null,
    published_wiki_version_id: "00000000-0000-0000-0000-000000000074",
    sanitized_error: null,
    sources: [{
      commit: nativeVersion,
      fingerprint: sourceFingerprint,
      source_id: sourceId,
      source_revision_id: revision().id,
    }],
    status: "published",
    updated_at: "2026-08-28T14:05:00Z",
  };
}

function createdSource(): object {
  return { source: { ...source(), lifecycle: "draft", health: "unknown", checked_at: null, current_revision_id: null, validated_configuration_version: null, version: 1, configuration_version: 1 }, validation: validationSync() };
}

function createdWebsiteSource(overrides: Record<string, unknown> = {}): object {
  return {
    source: {
      checked_at: null,
      configuration_version: 1,
      created_at: "2026-08-28T12:00:00Z",
      credential_header: "Authorization",
      credential_id: credentialId,
      credential_prefix: "Bearer ",
      current_revision_id: null,
      disabled_at: null,
      health: "unknown",
      id: sourceId,
      kind: "website",
      knowledge_base_id: knowledgeBaseId,
      lifecycle: "draft",
      max_concurrency: 4,
      max_depth: 3,
      max_page_bytes: 2097152,
      max_pages: 250,
      max_total_bytes: 104857600,
      name: "Product website",
      poll_interval_seconds: 3600,
      privacy: "private",
      removed_at: null,
      requests_per_second: 4,
      root_host: "docs.example",
      root_url: "https://docs.example/product/",
      sanitized_error: null,
      updated_at: "2026-08-28T12:00:00Z",
      validated_configuration_version: null,
      version: 1,
      ...overrides,
    },
    validation: validationSync(),
  };
}

function job(id: string): object {
  return {
    attempt_count: 0,
    created_at: "2026-08-28T12:01:00Z",
    finished_at: null,
    id,
    job_type: "validate_source",
    lease_expires_at: null,
    lease_generation: 0,
    lease_owner: null,
    max_attempts: 3,
    not_before: null,
    progress: 0,
    result: null,
    sanitized_error: null,
    started_at: null,
    status: "pending",
    target_id: sourceId,
    target_type: "source",
    updated_at: "2026-08-28T12:01:00Z",
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

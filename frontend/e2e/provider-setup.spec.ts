import { expect, test, type Locator, type Page } from "@playwright/test";
import { execFile, spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import {
  createServer,
  type IncomingMessage,
  type Server,
  type ServerResponse,
} from "node:http";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

import {
  acceptanceScope,
  assertScopedContainer,
  exactApplicationImage,
  exactProject,
  providerProxyName,
  secretVerifierName,
  type AcceptanceScope,
} from "./acceptance-scope";

const execFileAsync = promisify(execFile);
const repositoryRoot = fileURLToPath(new URL("../../", import.meta.url));

test.use({ trace: "off" });

type FakeCall = {
  body: unknown;
  path: string;
};

type FakeProvider = {
  baseUrl: string;
  calls: FakeCall[];
  close(): Promise<void>;
};

test("provider setup, probe, documentation assignments, Agent draft, restart, and secret containment", async ({ context, page }) => {
  test.setTimeout(180_000);
  const scope = acceptanceScope();
  const sentinel = process.env.CONTROL_PLANE_PROVIDER_SECRET_SENTINEL;
  const fakePort = Number(process.env.CONTROL_PLANE_PROVIDER_FAKE_PORT);
  const scoped = scope !== null
    && typeof sentinel === "string"
    && sentinel.length >= 24
    && Number.isInteger(fakePort)
    && fakePort >= 1_024
    && fakePort <= 65_535
    && ![5_432, 8_000, scope.apiPort, scope.databasePort].includes(fakePort);
  test.skip(
    !scoped,
    "the exact disposable ref0 stack, fixed fake port, and operator credentials are required",
  );
  if (!scoped || scope === null || !sentinel) return;

  await assertScopedContainer(scope, "api", {
    application: true,
    containerPort: 8_000,
    hostPort: scope.apiPort,
    image: exactApplicationImage,
    user: "ref0",
  });
  await assertScopedContainer(scope, "postgres", {
    containerPort: 5_432,
    hostPort: scope.databasePort,
    image: "postgres:18.6-bookworm",
  });
  const workerName = await assertScopedContainer(scope, "worker", {
    application: true,
    image: exactApplicationImage,
    user: "1000:2000",
  });
  const fake = await startFakeProvider(fakePort, sentinel);
  const apiOrigin = new URL(scope.apiUrl).origin;
  const observedResponses: string[] = [];
  const pendingResponses: Promise<void>[] = [];
  page.on("response", (response) => {
    const responseUrl = new URL(response.url());
    if (responseUrl.origin !== apiOrigin || !responseUrl.pathname.startsWith("/api/")) return;
    if (responseUrl.pathname === "/api/v1/events") return;
    const pending = response.text().then(
      (body) => { observedResponses.push(body); },
      () => undefined,
    );
    pendingResponses.push(pending);
  });

  try {
    await expect.poll(async () => {
      try {
        await execFileAsync("docker", [
          "exec",
          workerName,
          "wget",
          "--quiet",
          "--output-document=-",
          "--timeout=3",
          `${fake.baseUrl}/health`,
        ]);
        return 200;
      } catch {
        return 0;
      }
    }, { timeout: 15_000 }).toBe(200);

    await authenticate(
      page,
      scope.username,
      scope.password,
      scope.bootstrapToken,
    );

    const suffix = Date.now();
    const providerName = `Browser provider ${suffix}`;
    const credentialLabel = `Browser provider key ${suffix}`;
    const modelId = "acceptance-tool-model";
    const knowledgeBaseName = `Browser model routing ${suffix}`;
    const agentName = `Browser support Agent ${suffix}`;
    const agentKey = `browser-support-${suffix}`;

    await page.goto("/providers");
    await page.getByRole("link", { name: "Set up endpoint" }).click();
    await page.getByLabel("Display name").fill(providerName);
    await page.getByLabel("Base URL").fill(
      fake.baseUrl,
    );
    await page.getByLabel("Add a write-only API key").check();
    await page.getByLabel("Credential label").fill(credentialLabel);
    await page.getByRole("textbox", { name: "API key", exact: true }).fill(
      sentinel,
    );
    await page.getByLabel("Permit private-network addresses").check();
    await page.getByLabel(
      "Permit plain HTTP on an explicitly trusted private network",
    ).check();
    await page.getByRole("button", { name: "Create endpoint" }).click();

    await expect(page.getByRole("heading", { name: providerName })).toBeVisible();
    await expect(page.getByText("Endpoint saved")).toBeVisible();
    const endpointHref = await requiredHref(
      page.getByRole("link", { name: "Open endpoint detail" }),
    );
    await assertBrowserSecretAbsent(page, sentinel);

    await page.getByRole("button", { name: "Discover models" }).click();
    const discoveryJobHref = await requiredHref(
      page.getByRole("link", { name: /Open job/ }),
    );
    await pollSuccessfulJob(
      page,
      identifier(discoveryJobHref),
      observedResponses,
    );

    await page.goto(endpointHref);
    const modelLink = page.locator("a.record-card").filter({ hasText: modelId });
    await expect(modelLink).toBeVisible();
    const modelHref = await requiredHref(modelLink);
    await modelLink.click();
    await expect(page.getByRole("heading", { name: modelId })).toBeVisible();
    await page.getByLabel("Context window tokens").fill("8192");
    await page.getByLabel("Maximum output tokens").fill("256");
    // The themed dropdown covers its native select, so drive the value directly.
    await page.getByLabel("Reasoning transport").selectOption("reasoning_effort", { force: true });
    await page.getByLabel("Tool calling support").selectOption("yes", { force: true });
    await page.getByRole("button", { name: "Append settings version" }).click();
    await expect(page.getByText("settings v2", { exact: true })).toBeVisible();

    await page.goto(endpointHref);
    const probe = page.getByRole("region", { name: "Probe model capabilities" });
    await probe.getByLabel("Chat request").uncheck();
    await probe.getByLabel("Tool calling").check();
    await probe.getByLabel("Structured output").check();
    await probe.getByLabel(
      "I understand this probe sends a request to the endpoint and may incur provider cost.",
    ).check();
    await probe.getByRole("button", { name: "Enqueue probe" }).click();
    const probeJobHref = await requiredHref(
      probe.getByRole("link", { name: "Open job" }),
    );
    await pollSuccessfulJob(
      page,
      identifier(probeJobHref),
      observedResponses,
    );

    await page.goto("/knowledge-bases");
    await page.locator("summary").filter({ hasText: "Create knowledge base" }).click();
    await page.getByLabel("Name").fill(knowledgeBaseName);
    await page.getByRole("button", { name: "Create knowledge base" }).click();
    const knowledgeBaseLink = page.getByRole("link", {
      name: new RegExp(knowledgeBaseName),
    });
    await expect(knowledgeBaseLink).toBeVisible();
    const knowledgeBaseHref = await requiredHref(knowledgeBaseLink);
    await knowledgeBaseLink.click();
    await expect(page.getByRole("heading", { name: knowledgeBaseName })).toBeVisible();

    for (const role of [
      "Documentation planner",
      "Documentation writer",
    ]) {
      const card = page.locator("article.assignment-card").filter({ hasText: role });
      await expect(card.getByText("Eligible", { exact: true })).toBeVisible();
      await card.getByRole("button", { name: "Assign model" }).click();
      await expect(card.getByRole("button", { name: "Update assignment" })).toBeVisible();
    }

    const knowledgeBaseId = identifier(knowledgeBaseHref);
    await publishKnowledgeBaseFixture(scope, knowledgeBaseId);
    await page.goto("/agents/new");
    await page.getByLabel("Agent key").fill(agentKey);
    await page.getByLabel("Display name").fill(agentName);
    await page.getByLabel("Identity instructions").fill("Answer product questions using only the configured evidence.");
    await page.getByLabel("Model profile").selectOption({ label: `${providerName} · ${modelId} · available · settings v3` }, { force: true });
    await page.getByLabel("Knowledge base").selectOption({ label: `${knowledgeBaseName} · restricted` }, { force: true });
    await page.getByRole("button", { name: "Add knowledge base" }).click();
    await page.getByLabel("Answer mode").selectOption("single_pass", { force: true });
    await page.getByLabel("Maximum answer tokens").fill("200");
    await page.getByRole("button", { name: "Create Agent" }).click();
    await expect(page.getByRole("heading", { name: agentName })).toBeVisible();
    const agentHref = new URL(page.url()).pathname;
    await expect(page.getByText("Ready for delivery")).toBeVisible();
    await page.getByRole("button", { name: "Activate Agent" }).click();
    await expect(page.getByText("active", { exact: true })).toBeVisible();

    const tokenLabel = `Browser chat token ${suffix}`;
    await page.goto("/settings/chat-access-tokens");
    await page.getByLabel("Token label").fill(tokenLabel);
    await page.getByRole("checkbox", { name: new RegExp(agentName) }).check();
    await page.getByRole("button", { name: "Review token scope" }).click();
    await expect(page.getByRole("heading", { name: "Review chat access token" })).toBeVisible();
    await expect(page.getByText("Current effective scope")).toBeVisible();
    await page.getByRole("button", { name: "Issue token" }).click();
    const secretRegion = page.getByRole("region", { name: "Copy token now" });
    await expect(secretRegion).toBeVisible();
    await expect(secretRegion).toBeFocused();
    await expect(page.getByLabel("Token label")).toBeDisabled();
    await page.getByRole("button", { name: "Dismiss secret" }).click();
    await expect(secretRegion).toHaveCount(0);
    await expect(page.getByLabel("Token label")).toBeEnabled();
    await expect(page.getByText(tokenLabel, { exact: true })).toBeVisible();

    expect(fake.calls.map((call) => call.path)).toEqual([
      "/v1/models",
      "/v1/chat/completions",
      "/v1/chat/completions",
    ]);
    const probeBody = fake.calls[1]?.body;
    expect(isRecord(probeBody) && probeBody.max_completion_tokens).toBe(64);
    const structuredBody = fake.calls[2]?.body;
    expect(isRecord(structuredBody) && isRecord(structuredBody.response_format)).toBe(true);
    assertSecretAbsent("fake request capture", fake.calls, sentinel);

    await execFileAsync("docker", [
      "restart",
      `${scope.project}-api-1`,
      `${scope.project}-worker-1`,
    ]);
    await expect.poll(async () => {
      try {
        return (await page.request.get("/health/ready")).status();
      } catch {
        return 0;
      }
    }, { timeout: 45_000 }).toBe(200);
    await assertScopedContainer(scope, "api", {
      application: true,
      containerPort: 8_000,
      hostPort: scope.apiPort,
      image: exactApplicationImage,
      user: "ref0",
    });
    await assertScopedContainer(scope, "worker", {
      application: true,
      image: exactApplicationImage,
      user: "1000:2000",
    });

    await page.goto(endpointHref);
    await expect(page.getByRole("heading", { name: providerName })).toBeVisible();
    await expect(page.locator("a.record-card").filter({ hasText: modelId })).toBeVisible();
    await page.goto(modelHref);
    await expect(page.getByText("settings v3", { exact: true })).toBeVisible();
    await page.goto(knowledgeBaseHref);
    await expect(page.getByRole("heading", { name: knowledgeBaseName })).toBeVisible();
    await expect(page.getByRole("button", { name: "Update assignment" })).toHaveCount(2);
    await page.goto(agentHref);
    await expect(page.getByRole("heading", { name: agentName })).toBeVisible();

    const endpointId = identifier(endpointHref);
    const profileId = identifier(modelHref);
    for (const path of [
      "/api/v1/credentials",
      "/api/v1/provider-endpoints",
      `/api/v1/provider-endpoints/${endpointId}`,
      "/api/v1/model-profiles",
      `/api/v1/model-profiles/${profileId}`,
      "/api/v1/knowledge-bases",
      `/api/v1/knowledge-bases/${knowledgeBaseId}`,
      `/api/v1/knowledge-bases/${knowledgeBaseId}/model-assignments`,
      "/api/v1/agents?limit=50",
      agentHref.replace("/agents/", "/api/v1/agents/"),
      "/api/v1/chat-access-tokens?limit=50",
      "/api/v1/jobs?limit=100&offset=0",
    ]) {
      const response = await page.request.get(path);
      expect(response.ok()).toBe(true);
      observedResponses.push(await response.text());
    }

    await Promise.all(pendingResponses);
    assertSecretAbsent("API responses", observedResponses, sentinel);
    await assertBrowserSecretAbsent(page, sentinel);

    await runAcceptanceVerifier(sentinel, scope);

    const session = (await context.cookies()).find(
      (cookie) => cookie.name === "ref0_session",
    );
    expect(session?.httpOnly).toBe(true);
  } finally {
    await fake.close();
  }
});

async function authenticate(
  page: Page,
  username: string,
  password: string,
  bootstrapToken: string,
): Promise<void> {
  await page.goto("/login");
  await page.getByLabel("Username").fill(username);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  const overview = page.getByRole("heading", { name: "Today’s operations ledger" });
  await Promise.race([overview.waitFor(), page.getByRole("alert").waitFor()]);
  if (!(await overview.isVisible())) {
    await page.goto("/bootstrap");
    await page.getByLabel("Username").fill(username);
    await page.getByLabel("Password").fill(password);
    await page.getByLabel("Bootstrap token").fill(bootstrapToken);
    await page.getByRole("button", { name: "Create operator" }).click();
  }
  await expect(overview).toBeVisible();
}

async function requiredHref(locator: Locator): Promise<string> {
  await expect(locator).toBeVisible();
  const href = await locator.getAttribute("href");
  if (!href) throw new Error("acceptance link is missing its target");
  return href;
}

function identifier(path: string): string {
  const value = new URL(path, "http://acceptance.invalid").pathname.split("/").filter(Boolean).at(-1);
  if (!value) throw new Error("acceptance resource path is invalid");
  return value;
}

async function publishKnowledgeBaseFixture(scope: AcceptanceScope, knowledgeBaseId: string): Promise<void> {
  const jobId = randomUUID();
  const runId = randomUUID();
  const wikiId = randomUUID();
  const pageId = randomUUID();
  const sql = `
    BEGIN;
    SELECT
      set_config('ref0.acceptance_knowledge_base_id', :'knowledge_base_id', true),
      set_config('ref0.acceptance_wiki_id', :'wiki_id', true);
    DO $fixture$
    BEGIN
      PERFORM id FROM knowledge_bases
      WHERE id=current_setting('ref0.acceptance_knowledge_base_id')::uuid
        AND lifecycle='ACTIVE' AND published_wiki_id IS NULL
      FOR UPDATE;
      IF NOT FOUND THEN
        RAISE EXCEPTION 'acceptance knowledge base must be active and unpublished';
      END IF;
    END
    $fixture$;
    INSERT INTO jobs(
      id,job_type,target_type,target_id,payload,operation_key,concurrency_key,concurrency_limit,
      status,attempt_count,max_attempts,progress,lease_generation,result,
      created_at,updated_at,started_at,finished_at
    ) VALUES(
      :'job_id'::uuid,'PREPARE_RUN','knowledge_base',:'knowledge_base_id'::uuid,
      jsonb_build_object('run_id', :'run_id'::uuid),
      'acceptance-agent-wiki:' || :'knowledge_base_id','',0,
      'SUCCEEDED',1,3,100,1,'{}'::jsonb,
      transaction_timestamp(),transaction_timestamp(),transaction_timestamp(),transaction_timestamp()
    );
    INSERT INTO documentation_runs(
      id,knowledge_base_id,status,prepare_job_id,knowledge_base_version,instructions,language,
      created_at,updated_at,completed_at
    ) SELECT
      :'run_id'::uuid,id,'PUBLISHED',:'job_id'::uuid,version,instructions,language,
      transaction_timestamp(),transaction_timestamp(),transaction_timestamp()
    FROM knowledge_bases
    WHERE id=:'knowledge_base_id'::uuid AND lifecycle='ACTIVE' AND published_wiki_id IS NULL;
    INSERT INTO wiki_versions(
      id,knowledge_base_id,documentation_run_id,artifact_key,manifest_sha256,page_count,created_at,published_at
    ) VALUES(
      :'wiki_id'::uuid,:'knowledge_base_id'::uuid,:'run_id'::uuid,
      'knowledge-bases/' || :'knowledge_base_id' || '/wiki/' || :'wiki_id',
      sha256(convert_to('acceptance-agent-readiness-v1','UTF8')),1,
      transaction_timestamp(),transaction_timestamp()
    );
    INSERT INTO wiki_pages(
      id,wiki_version_id,slug,title,description,page_type,content_sha256,claims_sha256,body
    ) SELECT
      :'page_id'::uuid,:'wiki_id'::uuid,'overview','Acceptance overview',
      'Deterministic acceptance fixture.','overview',
      sha256(convert_to(content.body,'UTF8')),sha256(convert_to('[]','UTF8')),content.body
    FROM (VALUES ('# Acceptance overview\n\nPublished for the Agent delivery acceptance journey.')) AS content(body);
    UPDATE documentation_runs SET published_wiki_version_id=:'wiki_id'::uuid WHERE id=:'run_id'::uuid;
    UPDATE knowledge_bases
    SET published_wiki_id=:'wiki_id'::uuid,version=version+1,updated_at=transaction_timestamp()
    WHERE id=:'knowledge_base_id'::uuid AND lifecycle='ACTIVE' AND published_wiki_id IS NULL;
    DO $fixture$
    BEGIN
      PERFORM id FROM knowledge_bases
      WHERE id=current_setting('ref0.acceptance_knowledge_base_id')::uuid
        AND lifecycle='ACTIVE'
        AND published_wiki_id=current_setting('ref0.acceptance_wiki_id')::uuid;
      IF NOT FOUND THEN
        RAISE EXCEPTION 'acceptance knowledge base publication did not apply exactly once';
      END IF;
    END
    $fixture$;
    COMMIT;
  `;
  await execFileWithInput("docker", [
    "exec",
    "--interactive",
    `${scope.project}-postgres-1`,
    "psql",
    "--no-psqlrc",
    "--username",
    scope.databaseUser,
    "--dbname",
    scope.databaseName,
    "--set",
    "ON_ERROR_STOP=1",
    "--set",
    `knowledge_base_id=${knowledgeBaseId}`,
    "--set",
    `job_id=${jobId}`,
    "--set",
    `run_id=${runId}`,
    "--set",
    `wiki_id=${wikiId}`,
    "--set",
    `page_id=${pageId}`,
    "--file=-",
  ], sql);
}

async function execFileWithInput(file: string, args: string[], input: string): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const child = spawn(file, args, { stdio: ["pipe", "ignore", "pipe"] });
    let stderr = "";
    child.stderr.setEncoding("utf8");
    child.stderr.on("data", (chunk: string) => { stderr += chunk; });
    child.once("error", reject);
    child.once("close", (code, signal) => {
      if (code === 0) {
        resolve();
        return;
      }
      reject(new Error(`fixture command failed (${signal ?? code ?? "unknown"}): ${stderr.trim()}`));
    });
    child.stdin.end(input);
  });
}

async function pollSuccessfulJob(
  page: Page,
  jobId: string,
  observedResponses: string[],
): Promise<void> {
  await expect.poll(async () => {
    const response = await page.request.get(`/api/v1/jobs/${jobId}`);
    const text = await response.text();
    observedResponses.push(text);
    if (!response.ok()) return "http-error";
    const value: unknown = JSON.parse(text);
    if (!isRecord(value) || typeof value.status !== "string") return "invalid";
    if (["failed", "cancelled"].includes(value.status)) {
      throw new Error("provider acceptance job reached an unexpected terminal state");
    }
    return value.status;
  }, { timeout: 60_000 }).toBe("succeeded");
}

async function assertBrowserSecretAbsent(page: Page, sentinel: string): Promise<void> {
  const surface = await page.evaluate(async () => ({
    cacheNames: await caches.keys(),
    html: document.documentElement.outerHTML,
    indexedDatabaseNames: (await indexedDB.databases()).map((database) => database.name),
    inputValues: Array.from(document.querySelectorAll("input, textarea"), (field) => (
      field instanceof HTMLInputElement || field instanceof HTMLTextAreaElement
        ? field.value
        : ""
    )),
    localStorage: { ...localStorage },
    sessionStorage: { ...sessionStorage },
  }));
  assertSecretAbsent("browser state", surface, sentinel);
}

async function startFakeProvider(
  port: number,
  sentinel: string,
): Promise<FakeProvider> {
  const calls: FakeCall[] = [];
  const server = createServer({
    headersTimeout: 5_000,
    keepAliveTimeout: 1_000,
    maxHeaderSize: 16_384,
    requestTimeout: 10_000,
  }, async (request, response) => {
    if (request.method === "GET" && request.url === "/health") {
      sendJson(response, 200, { status: "ready" });
      return;
    }
    if (request.headers.authorization !== `Bearer ${sentinel}`) {
      sendJson(response, 401, { error: { message: "unauthorized" } });
      return;
    }
    if (calls.length >= 3) {
      sendJson(response, 429, { error: { message: "fake provider call limit reached" } });
      return;
    }
    if (request.method === "GET" && request.url === "/v1/models") {
      calls.push({ body: null, path: request.url });
      sendJson(response, 200, {
        data: [{ id: "acceptance-tool-model", object: "model", owned_by: "acceptance" }],
        object: "list",
        safe_extension: { deterministic: true },
      });
      return;
    }
    if (request.method === "POST" && request.url === "/v1/chat/completions") {
      const body = await readJson(request);
      calls.push({ body, path: request.url });
      if (!isRecord(body) || body.model !== "acceptance-tool-model") {
        sendJson(response, 400, { error: { message: "invalid probe request" } });
        return;
      }
      if (isRecord(body.response_format)) {
        sendJson(response, 200, {
          choices: [{
            finish_reason: "stop",
            index: 0,
            message: { content: "{\"ok\":true}", role: "assistant" },
          }],
          created: 0,
          id: "acceptance-structured-completion",
          model: "acceptance-tool-model",
          object: "chat.completion",
        });
        return;
      }
      if (!Array.isArray(body.tools)) {
        sendJson(response, 400, { error: { message: "invalid tool probe request" } });
        return;
      }
      sendJson(response, 200, {
        choices: [{
          finish_reason: "tool_calls",
          index: 0,
          message: {
            content: null,
            role: "assistant",
            tool_calls: [{
              function: { arguments: "{\"status\":\"ok\"}", name: "report_probe" },
              id: "acceptance-call",
              type: "function",
            }],
          },
        }],
        created: 0,
        id: "acceptance-completion",
        model: "acceptance-tool-model",
        object: "chat.completion",
      });
      return;
    }
    sendJson(response, 404, { error: { message: "not found" } });
  });
  server.maxConnections = 16;
  await listen(server, port);
  let proxyStarted = false;
  try {
    await execFileAsync("docker", [
      "run",
      "--detach",
      "--rm",
      "--name",
      providerProxyName,
      "--label",
      `io.ref0.acceptance=${exactProject}`,
      "--network",
      `${exactProject}_default`,
      "--add-host",
      "host.docker.internal:host-gateway",
      "--read-only",
      "--cap-drop",
      "ALL",
      "--security-opt",
      "no-new-privileges",
      "--pids-limit",
      "64",
      "--memory",
      "128m",
      "--user",
      "node",
      "node:24.20.0-bookworm-slim",
      "node",
      "--eval",
      proxyProgram(port),
    ]);
    proxyStarted = true;
    return {
      baseUrl: `http://${providerProxyName}:${port}`,
      calls,
      async close() {
        if (proxyStarted) {
          try {
            await execFileAsync("docker", ["stop", "--time", "3", providerProxyName]);
          } catch {
            // The exact disposable proxy may already have stopped.
          }
        }
        await closeServer(server);
      },
    };
  } catch (error: unknown) {
    await closeServer(server);
    throw error;
  }
}

function proxyProgram(port: number): string {
  return [
    'const net = require("node:net");',
    `const port = ${port};`,
    "const byteLimit = 131072;",
    "const server = net.createServer((client) => {",
    '  const upstream = net.connect({ host: "host.docker.internal", port });',
    "  let received = 0;",
    "  let sent = 0;",
    "  client.setTimeout(10000, () => client.destroy());",
    "  upstream.setTimeout(10000, () => upstream.destroy());",
    "  client.on(\"data\", (chunk) => { received += chunk.length; if (received > byteLimit) { client.destroy(); upstream.destroy(); } });",
    "  upstream.on(\"data\", (chunk) => { sent += chunk.length; if (sent > byteLimit) { client.destroy(); upstream.destroy(); } });",
    "  client.on(\"error\", () => upstream.destroy());",
    "  upstream.on(\"error\", () => client.destroy());",
    "  client.pipe(upstream);",
    "  upstream.pipe(client);",
    "});",
    "server.maxConnections = 16;",
    'server.listen(port, "0.0.0.0");',
  ].join("\n");
}

function closeServer(server: Server): Promise<void> {
  server.closeAllConnections();
  return new Promise<void>((resolve, reject) => {
    server.close((error) => error ? reject(error) : resolve());
  });
}

function listen(server: Server, port: number): Promise<void> {
  return new Promise((resolve, reject) => {
    const failed = (error: Error) => reject(error);
    server.once("error", failed);
    server.listen(port, "0.0.0.0", () => {
      server.off("error", failed);
      resolve();
    });
  });
}

function sendJson(
  response: ServerResponse,
  status: number,
  value: object,
): void {
  const encoded = Buffer.from(JSON.stringify(value));
  response.writeHead(status, {
    "Connection": "close",
    "Content-Length": String(encoded.length),
    "Content-Type": "application/json",
  });
  response.end(encoded);
}

async function readJson(request: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  let received = 0;
  for await (const chunk of request) {
    const value = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
    received += value.length;
    if (received > 65_536) throw new Error("fake provider request exceeded its bound");
    chunks.push(value);
  }
  return JSON.parse(Buffer.concat(chunks).toString("utf8"));
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

async function runAcceptanceVerifier(
  sentinel: string,
  scope: AcceptanceScope,
): Promise<void> {
  const hostDatabaseURL = `postgresql://${scope.databaseUser}:${encodeURIComponent(scope.databasePassword)}@127.0.0.1:${scope.databasePort}/${scope.databaseName}?sslmode=disable`;
  const verificationEnvironment = {
    ...process.env,
    REF0_SECRET_SCAN_SENTINEL: sentinel,
    TEST_DATABASE_URL: hostDatabaseURL,
  };
  const hostGo = process.env.REF0_ACCEPTANCE_GO_BINARY;
  if (hostGo) {
    await execFileAsync(hostGo, [
      "test",
      "./verification",
      "-run",
      "^TestDatabaseDoesNotContainPlaintextSentinel$",
      "-count=1",
    ], { cwd: repositoryRoot, env: verificationEnvironment });
  } else {
    verificationEnvironment.TEST_DATABASE_URL = `postgresql://${scope.databaseUser}:${encodeURIComponent(scope.databasePassword)}@postgres:5432/${scope.databaseName}?sslmode=disable`;
    await execFileAsync("docker", [
      "run",
      "--rm",
      "--name",
      secretVerifierName,
      "--label",
      `io.ref0.acceptance=${scope.project}`,
      "--network",
      `${scope.project}_default`,
      "--volume",
      `${repositoryRoot}:/src:ro`,
      "--workdir",
      "/src",
      "--env",
      "REF0_SECRET_SCAN_SENTINEL",
      "--env",
      "TEST_DATABASE_URL",
      "golang:1.27.0-bookworm",
      "go",
      "test",
      "./verification",
      "-run",
      "^TestDatabaseDoesNotContainPlaintextSentinel$",
      "-count=1",
    ], { env: verificationEnvironment });
  }
  const compose = [
    "compose",
    "--file",
    `${repositoryRoot}docker-compose.yml`,
    "--project-name",
    scope.project,
  ];
  const [{ stdout: logs }, { stdout: rendered }] = await Promise.all([
    execFileAsync("docker", [...compose, "logs", "--no-color"]),
    execFileAsync("docker", [...compose, "config", "--format", "json"]),
  ]);
  assertSecretAbsent("container logs", logs, sentinel);
  assertSecretAbsent("Compose configuration", rendered, sentinel);
}

function assertSecretAbsent(label: string, value: unknown, sentinel: string): void {
  const rendered = typeof value === "string" ? value : JSON.stringify(value) ?? "";
  if (rendered.includes(sentinel)) {
    throw new Error(`secret containment failed in ${label}`);
  }
}

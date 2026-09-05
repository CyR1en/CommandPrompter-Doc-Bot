import createClient from "openapi-fetch";

import type { components, paths } from "./schema";

export type AuthSession = components["schemas"]["AuthSessionResponse"];
export type Credential = components["schemas"]["CredentialResponse"];
export type CredentialKind = components["schemas"]["CreateCredentialRequest"]["kind"];
export type Job = components["schemas"]["JobResponse"];
export type JobStatus = components["schemas"]["JobResponse"]["status"];
export type KnowledgeBase = components["schemas"]["KnowledgeBaseResponse"];
export type KnowledgeBaseDeletion = components["schemas"]["DeleteKnowledgeBaseResponse"];
export type DocumentationRun = components["schemas"]["DocumentationRunResponse"];
export type WikiEvidence = components["schemas"]["WikiEvidenceResponse"];
export type WikiResponse = components["schemas"]["WikiResponse"];
export type WikiVersion = components["schemas"]["WikiVersionResponse"];
export type ProviderEndpoint = components["schemas"]["ProviderEndpointResponse"];
export type ProviderEndpointInput = components["schemas"]["CreateProviderEndpointRequest"];
export type ModelProfile = components["schemas"]["ModelProfileResponse"];
export type ModelSettingsInput = components["schemas"]["ModelSettingsRequest"];
export type DiscoveryRun = components["schemas"]["DiscoveryRunResponse"];
export type ProbeRun = components["schemas"]["ProbeRunResponse"];
export type ProbeCheck = components["schemas"]["ScheduleProbeRequest"]["selected_checks"][number];
export type ModelAssignment = components["schemas"]["ModelAssignmentResponse"];
export type ModelRole = ModelAssignment["role"];
export type ReasoningEffort = ModelAssignment["reasoning_effort"];
export type AnswerMode = ModelAssignment["answer_mode"];
export type ConfigurationExport = components["schemas"]["ConfigurationExport"];
export type OperationalOverview = components["schemas"]["OperationalOverview"];

export type Agent = components["schemas"]["AgentResponse"];
export type AgentPage = components["schemas"]["ListAgentsResponse"];
export type AgentVersion = components["schemas"]["AgentVersionResponse"];
export type AgentVersionPage = components["schemas"]["ListAgentVersionsResponse"];
export type AgentConfigurationSnapshot = components["schemas"]["AgentConfigurationResponse"];
export type AgentConfigurationInput = components["schemas"]["AgentConfigurationRequest"];
export type CreateAgentInput = components["schemas"]["CreateAgentRequest"];
export type ReplaceAgentConfigurationInput = components["schemas"]["ReplaceAgentConfigurationRequest"];
export type SetAgentLifecycleInput = components["schemas"]["SetAgentLifecycleRequest"];
export type AgentReadiness = components["schemas"]["AgentReadinessResponse"];
export type AgentReadinessIssue = components["schemas"]["ReadinessIssueResponse"];
export type AgentRunSummary = components["schemas"]["RunSummaryResponse"];
export type AgentRunPage = components["schemas"]["ListRunsResponse"];
export type AgentRunDetail = components["schemas"]["RunDetailResponse"];
export type AgentRunKnowledgeBase = components["schemas"]["RunKnowledgeBaseResponse"];
export type AgentReasoningEffort = AgentConfigurationInput["reasoning_effort"];
export type AgentAnswerMode = AgentConfigurationInput["answer_mode"];
export type AgentEvidenceAccess = AgentConfigurationInput["evidence_access"];
export type AgentLifecycle = Agent["lifecycle"];
export type AgentLifecycleTarget = SetAgentLifecycleInput["lifecycle"];
export type AgentCandidateNotReady = components["schemas"]["AgentCandidateNotReadyProblem"];
export type AgentMutationResult =
  | { agent: Agent; kind: "updated" }
  | { kind: "candidate_not_ready"; readiness: AgentReadiness };

export type ChatAccessToken = components["schemas"]["ChatTokenResponse"];
export type ChatAccessTokenSummary = components["schemas"]["ChatTokenSummaryResponse"];
export type IssuedChatAccessToken = components["schemas"]["IssuedChatTokenResponse"];
export type ChatAccessTokenPage = components["schemas"]["ListChatTokensResponse"];
export type ChatAccessTokenInput = components["schemas"]["CreateChatTokenRequest"];
export type ChatAccessTokenScope = components["schemas"]["ChatTokenAgentScopeResponse"];
export type ChatAccessTokenReplay = components["schemas"]["ChatTokenReplayProblem"];
export type ChatAccessTokenScopePreviewInput = components["schemas"]["PreviewChatTokenScopesRequest"];
export type ChatAccessTokenScopePreview = components["schemas"]["PreviewChatTokenScopesResponse"];

export type DiscordConnection = components["schemas"]["DiscordConnectionResponse"];
export type DiscordServer = components["schemas"]["DiscordServerResponse"];
export type DiscordChannel = components["schemas"]["DiscordChannelResponse"];
export type DiscordRole = components["schemas"]["DiscordRoleResponse"];
export type DiscordBinding = components["schemas"]["DiscordBindingResponse"];
export type DiscordBindingInput = components["schemas"]["CreateDiscordBindingRequest"];
export type DiscordBindingUpdateInput = components["schemas"]["UpdateDiscordBindingRequest"];
export type DiscordTrigger = DiscordBindingInput["triggers"][number];
export type DiscordInstallation = components["schemas"]["DiscordInstallationResponse"];

export type IssueChatAccessTokenResult =
  | { kind: "issued"; token: IssuedChatAccessToken }
  | { kind: "secret_already_issued"; token: ChatAccessToken };

export type RepositorySourceInput = Omit<components["schemas"]["CreateRepositorySourceRequest"], "knowledge_base_id">;
export type RepositorySourceUpdateInput = components["schemas"]["UpdateRepositorySourceRequest"];
export type RepositorySource = components["schemas"]["RepositorySourceResponse"];
export type WebsiteSourceInput = Omit<components["schemas"]["CreateWebsiteSourceRequest"], "knowledge_base_id">;
export type WebsiteSourceUpdateInput = components["schemas"]["UpdateWebsiteSourceRequest"];
export type WebsiteSource = components["schemas"]["WebsiteSourceResponse"];
export type Source = RepositorySource | WebsiteSource;
export type RepositoryRefKind = RepositorySourceInput["ref_kind"];
export type SourcePrivacy = RepositorySourceInput["privacy"];
export type SourceLifecycleInput = components["schemas"]["ChangeSourceLifecycleRequest"]["lifecycle"];
export type SourceRevision = components["schemas"]["SourceRevisionResponse"];
export type SourceSync = components["schemas"]["SourceSyncResponse"] | components["schemas"]["WebsiteSourceSyncResponse"];
export type SourceCreated = components["schemas"]["SourceCreatedResponse"];

export class ApiError extends Error {
  readonly status: number;

  constructor(status: number) {
    super(messageForStatus(status));
    this.name = "ApiError";
    this.status = status;
  }
}

type UnauthorizedHandler = () => void;

let unauthorizedHandler: UnauthorizedHandler | undefined;

const client = createClient<paths>({
  baseUrl: window.location.origin,
  credentials: "include",
  fetch: (request: Request) => globalThis.fetch(request),
});

function messageForStatus(status: number): string {
  if (status === 401) return "Your session has ended. Sign in again.";
  if (status === 403) return "That operation is not available.";
  if (status === 404) return "The requested record was not found.";
  if (status === 409) return "The record changed. Refresh and try again.";
  if (status === 422) return "Check the fields and try again.";
  return "The operation could not be completed.";
}

function fail(status: number): never {
  if (status === 401) unauthorizedHandler?.();
  throw new ApiError(status);
}

function requireData<T>(data: T | undefined, response: Response): T {
  if (data === undefined) fail(response.status);
  return data;
}

export function installUnauthorizedHandler(
  handler: UnauthorizedHandler,
): () => void {
  unauthorizedHandler = handler;
  return () => {
    if (unauthorizedHandler === handler) unauthorizedHandler = undefined;
  };
}

export function actionId(): string {
  return crypto.randomUUID();
}

export function safeErrorMessage(error: unknown): string {
  return error instanceof ApiError
    ? error.message
    : "The operation could not be completed.";
}

export async function currentSession(): Promise<AuthSession> {
  const { data, response } = await client.GET("/api/v1/auth/session");
  return requireData(data, response);
}

export async function login(username: string, password: string): Promise<AuthSession> {
  const { data, response } = await client.POST("/api/v1/auth/login", {
    body: { username, password },
  });
  return requireData(data, response);
}

export async function bootstrap(
  username: string,
  password: string,
  bootstrapToken: string,
): Promise<AuthSession> {
  const { data, response } = await client.POST("/api/v1/auth/bootstrap", {
    body: { username, password, bootstrap_token: bootstrapToken },
  });
  return requireData(data, response);
}

export async function logout(csrfToken: string): Promise<void> {
  const { response } = await client.POST("/api/v1/auth/logout", {
    params: { header: { "X-CSRF-Token": csrfToken } },
  });
  if (!response.ok) fail(response.status);
}

export async function listCredentials(): Promise<Credential[]> {
  const { data, response } = await client.GET("/api/v1/credentials");
  return requireData(data, response);
}

export async function createCredential(input: {
  csrfToken: string;
  idempotencyKey: string;
  kind: CredentialKind;
  label: string;
  secret: string;
}): Promise<Credential> {
  const { data, response } = await client.POST("/api/v1/credentials", {
    body: { kind: input.kind, label: input.label, secret: input.secret },
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
    },
  });
  return requireData(data, response);
}

export async function rotateCredential(input: {
  csrfToken: string;
  credentialId: string;
  idempotencyKey: string;
  secret: string;
}): Promise<Credential> {
  const { data, response } = await client.POST(
    "/api/v1/credentials/{credential_id}/rotate",
    {
      body: { secret: input.secret },
      params: {
        header: {
          "Idempotency-Key": input.idempotencyKey,
          "X-CSRF-Token": input.csrfToken,
        },
        path: { credential_id: input.credentialId },
      },
    },
  );
  return requireData(data, response);
}

export async function listKnowledgeBases(): Promise<KnowledgeBase[]> {
  const { data, response } = await client.GET("/api/v1/knowledge-bases");
  return requireData(data, response);
}

export async function listDocumentationRuns(knowledgeBaseId?: string): Promise<DocumentationRun[]> {
  const { data, response } = await client.GET("/api/v1/runs", {
    params: { query: { knowledge_base_id: knowledgeBaseId, limit: 50, offset: 0 } },
  });
  return requireData(data, response);
}

export async function getDocumentationRun(id: string): Promise<DocumentationRun> {
  const { data, response } = await client.GET("/api/v1/runs/{run_id}", {
    params: { path: { run_id: id } },
  });
  return requireData(data, response);
}

export async function generateKnowledgeBase(input: {
  csrfToken: string;
  expectedVersion: number;
  id: string;
  idempotencyKey: string;
}): Promise<Job> {
  const { data, response } = await client.POST("/api/v1/knowledge-bases/{knowledge_base_id}/generate", {
    body: { expected_version: input.expectedVersion },
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
      path: { knowledge_base_id: input.id },
    },
  });
  return requireData(data, response);
}

export async function getWiki(
  knowledgeBaseId: string,
  selection: { slug?: string; versionId?: string },
): Promise<WikiResponse> {
  const { data, response } = await client.GET("/api/v1/knowledge-bases/{knowledge_base_id}/wiki", {
    params: {
      path: { knowledge_base_id: knowledgeBaseId },
      query: { slug: selection.slug, version_id: selection.versionId },
    },
  });
  return requireData(data, response);
}

export async function listWikiVersions(knowledgeBaseId: string): Promise<WikiVersion[]> {
  const { data, response } = await client.GET("/api/v1/knowledge-bases/{knowledge_base_id}/wiki/versions", {
    params: { path: { knowledge_base_id: knowledgeBaseId } },
  });
  return requireData(data, response);
}

export function wikiExportUrl(knowledgeBaseId: string, versionId?: string): string {
  const base = `/api/v1/knowledge-bases/${encodeURIComponent(knowledgeBaseId)}/wiki/export`;
  return versionId ? `${base}?${new URLSearchParams({ version_id: versionId })}` : base;
}

export async function listSources(knowledgeBaseId?: string): Promise<Source[]> {
  const { data, response } = await client.GET("/api/v1/sources", {
    params: { query: { knowledge_base_id: knowledgeBaseId } },
  });
  return requireData(data, response);
}

export async function getSource(id: string): Promise<Source> {
  const { data, response } = await client.GET("/api/v1/sources/{source_id}", {
    params: { path: { source_id: id } },
  });
  return requireData(data, response);
}

export async function createRepositorySource(input: {
  body: RepositorySourceInput & { knowledge_base_id: string };
  csrfToken: string;
  idempotencyKey: string;
}): Promise<SourceCreated> {
  const { data, response } = await client.POST("/api/v1/sources/repositories", {
    body: input.body,
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
    },
  });
  return requireData(data, response);
}

export async function createWebsiteSource(input: {
  body: WebsiteSourceInput & { knowledge_base_id: string };
  csrfToken: string;
  idempotencyKey: string;
}): Promise<SourceCreated> {
  const { data, response } = await client.POST("/api/v1/sources/websites", {
    body: input.body,
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
    },
  });
  return requireData(data, response);
}

export async function updateRepositorySource(input: {
  body: RepositorySourceUpdateInput;
  csrfToken: string;
  id: string;
  idempotencyKey: string;
}): Promise<RepositorySource> {
  const { data, response } = await client.PATCH("/api/v1/sources/{source_id}", {
    body: input.body,
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
      path: { source_id: input.id },
    },
  });
  const value = requireData(data, response);
  if (value.kind !== "repository") fail(409);
  return value;
}

export async function updateWebsiteSource(input: {
  body: WebsiteSourceUpdateInput;
  csrfToken: string;
  id: string;
  idempotencyKey: string;
}): Promise<WebsiteSource> {
  const { data, response } = await client.PATCH("/api/v1/sources/{source_id}", {
    body: input.body,
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
      path: { source_id: input.id },
    },
  });
  const value = requireData(data, response);
  if (value.kind !== "website") fail(409);
  return value;
}

export async function changeSourceLifecycle(input: {
  csrfToken: string;
  expectedVersion: number;
  id: string;
  idempotencyKey: string;
  lifecycle: SourceLifecycleInput;
}): Promise<Source> {
  const { data, response } = await client.POST("/api/v1/sources/{source_id}/lifecycle", {
    body: {
      expected_version: input.expectedVersion,
      lifecycle: input.lifecycle,
    },
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
      path: { source_id: input.id },
    },
  });
  return requireData(data, response);
}

export async function validateSource(input: {
  csrfToken: string;
  expectedVersion: number;
  id: string;
  idempotencyKey: string;
}): Promise<SourceSync> {
  const { data, response } = await client.POST("/api/v1/sources/{source_id}/validate", {
    body: { expected_version: input.expectedVersion },
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
      path: { source_id: input.id },
    },
  });
  return requireData(data, response);
}

export async function syncSource(input: {
  csrfToken: string;
  expectedVersion: number;
  id: string;
  idempotencyKey: string;
}): Promise<SourceSync> {
  const { data, response } = await client.POST("/api/v1/sources/{source_id}/sync", {
    body: { expected_version: input.expectedVersion },
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
      path: { source_id: input.id },
    },
  });
  return requireData(data, response);
}

export async function listSourceRevisions(id: string): Promise<SourceRevision[]> {
  const { data, response } = await client.GET("/api/v1/sources/{source_id}/revisions", {
    params: { path: { source_id: id } },
  });
  return requireData(data, response);
}

export async function listSourceSyncs(id: string): Promise<SourceSync[]> {
  const { data, response } = await client.GET("/api/v1/sources/{source_id}/syncs", {
    params: { path: { source_id: id } },
  });
  return requireData(data, response);
}

export async function getKnowledgeBase(id: string): Promise<KnowledgeBase> {
  const { data, response } = await client.GET(
    "/api/v1/knowledge-bases/{knowledge_base_id}",
    { params: { path: { knowledge_base_id: id } } },
  );
  return requireData(data, response);
}

export async function createKnowledgeBase(input: {
  access: "public" | "restricted";
  csrfToken: string;
  idempotencyKey: string;
  instructions: string;
  language: string;
  name: string;
}): Promise<KnowledgeBase> {
  const { data, response } = await client.POST("/api/v1/knowledge-bases", {
    body: {
      access: input.access,
      instructions: input.instructions,
      language: input.language,
      name: input.name,
    },
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
    },
  });
  return requireData(data, response);
}

export async function updateKnowledgeBase(input: {
  body: components["schemas"]["UpdateKnowledgeBaseRequest"];
  csrfToken: string;
  id: string;
  idempotencyKey: string;
}): Promise<KnowledgeBase> {
  const { data, response } = await client.PATCH(
    "/api/v1/knowledge-bases/{knowledge_base_id}",
    {
      body: input.body,
      params: {
        header: {
          "Idempotency-Key": input.idempotencyKey,
          "X-CSRF-Token": input.csrfToken,
        },
        path: { knowledge_base_id: input.id },
      },
    },
  );
  return requireData(data, response);
}

export async function deleteKnowledgeBase(input: {
  confirmationName: string;
  csrfToken: string;
  expectedVersion: number;
  id: string;
  idempotencyKey: string;
}): Promise<KnowledgeBaseDeletion> {
  const { data, response } = await client.DELETE(
    "/api/v1/knowledge-bases/{knowledge_base_id}",
    {
      body: {
        confirmation_name: input.confirmationName,
        expected_version: input.expectedVersion,
      },
      params: {
        header: {
          "Idempotency-Key": input.idempotencyKey,
          "X-CSRF-Token": input.csrfToken,
        },
        path: { knowledge_base_id: input.id },
      },
    },
  );
  return requireData(data, response);
}

export async function restoreKnowledgeBase(input: {
  csrfToken: string;
  expectedVersion: number;
  id: string;
  idempotencyKey: string;
}): Promise<KnowledgeBase> {
  const { data, response } = await client.POST(
    "/api/v1/knowledge-bases/{knowledge_base_id}/restore",
    {
      body: { expected_version: input.expectedVersion },
      params: {
        header: {
          "Idempotency-Key": input.idempotencyKey,
          "X-CSRF-Token": input.csrfToken,
        },
        path: { knowledge_base_id: input.id },
      },
    },
  );
  return requireData(data, response);
}

export async function listJobs(status?: JobStatus): Promise<Job[]> {
  const { data, response } = await client.GET("/api/v1/jobs", {
    params: { query: { limit: 100, offset: 0, status } },
  });
  return requireData(data, response);
}

export async function getJob(id: string): Promise<Job> {
  const { data, response } = await client.GET("/api/v1/jobs/{job_id}", {
    params: { path: { job_id: id } },
  });
  return requireData(data, response);
}

export async function cancelJob(input: {
  csrfToken: string;
  id: string;
  idempotencyKey: string;
}): Promise<Job> {
  const { data, response } = await client.POST(
    "/api/v1/jobs/{job_id}/cancel",
    {
      params: {
        header: {
          "Idempotency-Key": input.idempotencyKey,
          "X-CSRF-Token": input.csrfToken,
        },
        path: { job_id: input.id },
      },
    },
  );
  return requireData(data, response);
}

export async function listProviderEndpoints(): Promise<ProviderEndpoint[]> {
  const { data, response } = await client.GET("/api/v1/provider-endpoints");
  return requireData(data, response);
}

export async function getProviderEndpoint(id: string): Promise<ProviderEndpoint> {
  const { data, response } = await client.GET(
    "/api/v1/provider-endpoints/{endpoint_id}",
    { params: { path: { endpoint_id: id } } },
  );
  return requireData(data, response);
}

export async function createProviderEndpoint(input: {
  body: ProviderEndpointInput;
  csrfToken: string;
  idempotencyKey: string;
}): Promise<ProviderEndpoint> {
  const { data, response } = await client.POST("/api/v1/provider-endpoints", {
    body: input.body,
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
    },
  });
  return requireData(data, response);
}

export async function updateProviderEndpoint(input: {
  body: components["schemas"]["UpdateProviderEndpointRequest"];
  csrfToken: string;
  id: string;
  idempotencyKey: string;
}): Promise<ProviderEndpoint> {
  const { data, response } = await client.PATCH(
    "/api/v1/provider-endpoints/{endpoint_id}",
    {
      body: input.body,
      params: {
        header: {
          "Idempotency-Key": input.idempotencyKey,
          "X-CSRF-Token": input.csrfToken,
        },
        path: { endpoint_id: input.id },
      },
    },
  );
  return requireData(data, response);
}

export async function scheduleDiscovery(input: {
  csrfToken: string;
  endpointId: string;
  expectedVersion: number;
  idempotencyKey: string;
}): Promise<DiscoveryRun> {
  const { data, response } = await client.POST(
    "/api/v1/provider-endpoints/{endpoint_id}/discover",
    {
      body: { expected_version: input.expectedVersion },
      params: {
        header: {
          "Idempotency-Key": input.idempotencyKey,
          "X-CSRF-Token": input.csrfToken,
        },
        path: { endpoint_id: input.endpointId },
      },
    },
  );
  return requireData(data, response);
}

export async function listModelProfiles(endpointId?: string): Promise<ModelProfile[]> {
  const { data, response } = await client.GET("/api/v1/model-profiles", {
    params: { query: { endpoint_id: endpointId } },
  });
  return requireData(data, response);
}

export async function getModelProfile(id: string): Promise<ModelProfile> {
  const { data, response } = await client.GET(
    "/api/v1/model-profiles/{profile_id}",
    { params: { path: { profile_id: id } } },
  );
  return requireData(data, response);
}

export async function createModelProfile(input: {
  body: components["schemas"]["CreateModelProfileRequest"];
  csrfToken: string;
  idempotencyKey: string;
}): Promise<ModelProfile> {
  const { data, response } = await client.POST("/api/v1/model-profiles", {
    body: input.body,
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
    },
  });
  return requireData(data, response);
}

export async function editModelProfile(input: {
  body: components["schemas"]["EditModelProfileRequest"];
  csrfToken: string;
  id: string;
  idempotencyKey: string;
}): Promise<ModelProfile> {
  const { data, response } = await client.PATCH(
    "/api/v1/model-profiles/{profile_id}",
    {
      body: input.body,
      params: {
        header: {
          "Idempotency-Key": input.idempotencyKey,
          "X-CSRF-Token": input.csrfToken,
        },
        path: { profile_id: input.id },
      },
    },
  );
  return requireData(data, response);
}

export async function scheduleProbe(input: {
  acknowledgeCost: true;
  checks: ProbeCheck[];
  csrfToken: string;
  endpointId: string;
  expectedVersion: number;
  idempotencyKey: string;
  profileId: string;
}): Promise<ProbeRun> {
  const { data, response } = await client.POST(
    "/api/v1/provider-endpoints/{endpoint_id}/probe",
    {
      body: {
        acknowledge_cost: input.acknowledgeCost,
        expected_version: input.expectedVersion,
        profile_id: input.profileId,
        selected_checks: input.checks,
      },
      params: {
        header: {
          "Idempotency-Key": input.idempotencyKey,
          "X-CSRF-Token": input.csrfToken,
        },
        path: { endpoint_id: input.endpointId },
      },
    },
  );
  return requireData(data, response);
}

export async function listModelAssignments(knowledgeBaseId: string): Promise<ModelAssignment[]> {
  const { data, response } = await client.GET(
    "/api/v1/knowledge-bases/{knowledge_base_id}/model-assignments",
    { params: { path: { knowledge_base_id: knowledgeBaseId } } },
  );
  return requireData(data, response);
}

export async function putModelAssignment(input: {
  body: components["schemas"]["PutModelAssignmentRequest"];
  csrfToken: string;
  idempotencyKey: string;
  knowledgeBaseId: string;
  role: ModelRole;
}): Promise<ModelAssignment> {
  const { data, response } = await client.PUT(
    "/api/v1/knowledge-bases/{knowledge_base_id}/model-assignments/{role}",
    {
      body: input.body,
      params: {
        header: {
          "Idempotency-Key": input.idempotencyKey,
          "X-CSRF-Token": input.csrfToken,
        },
        path: {
          knowledge_base_id: input.knowledgeBaseId,
          role: input.role,
        },
      },
    },
  );
  return requireData(data, response);
}

export async function listAgentsPage(input: {
  cursor?: string;
  limit: number;
}): Promise<AgentPage> {
  const { data, response } = await client.GET("/api/v1/agents", {
    params: { query: { cursor: input.cursor, limit: input.limit } },
  });
  return requireData(data, response);
}

export async function getAgent(id: string): Promise<Agent> {
  const { data, response } = await client.GET("/api/v1/agents/{agent_id}", {
    params: { path: { agent_id: id } },
  });
  return requireData(data, response);
}

export async function createAgent(input: {
  body: CreateAgentInput;
  csrfToken: string;
  idempotencyKey: string;
}): Promise<Agent> {
  const { data, response } = await client.POST("/api/v1/agents", {
    body: input.body,
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
    },
  });
  return requireData(data, response);
}

export async function replaceAgentConfiguration(input: {
  agentId: string;
  body: ReplaceAgentConfigurationInput;
  csrfToken: string;
  idempotencyKey: string;
}): Promise<AgentMutationResult> {
  const { data, error, response } = await client.PUT("/api/v1/agents/{agent_id}/configuration", {
    body: input.body,
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
      path: { agent_id: input.agentId },
    },
  });
  if (data !== undefined) return { agent: data, kind: "updated" };
  if (response.status === 409 && error !== undefined && "code" in error && error.code === "candidate_not_ready") {
    return { kind: "candidate_not_ready", readiness: error.readiness };
  }
  fail(response.status);
}

export async function setAgentLifecycle(input: {
  agentId: string;
  body: SetAgentLifecycleInput;
  csrfToken: string;
  idempotencyKey: string;
}): Promise<AgentMutationResult> {
  const { data, error, response } = await client.PATCH("/api/v1/agents/{agent_id}/lifecycle", {
    body: input.body,
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
      path: { agent_id: input.agentId },
    },
  });
  if (data !== undefined) return { agent: data, kind: "updated" };
  if (response.status === 409 && error !== undefined && "code" in error && error.code === "candidate_not_ready") {
    return { kind: "candidate_not_ready", readiness: error.readiness };
  }
  fail(response.status);
}

export async function getAgentReadiness(agentId: string): Promise<AgentReadiness> {
  const { data, response } = await client.GET("/api/v1/agents/{agent_id}/readiness", {
    params: { path: { agent_id: agentId } },
  });
  return requireData(data, response);
}

export async function listAgentVersionsPage(input: {
  agentId: string;
  cursor?: string;
  limit: number;
}): Promise<AgentVersionPage> {
  const { data, response } = await client.GET("/api/v1/agents/{agent_id}/versions", {
    params: {
      path: { agent_id: input.agentId },
      query: { cursor: input.cursor, limit: input.limit },
    },
  });
  return requireData(data, response);
}

export async function getAgentVersion(input: {
  agentId: string;
  versionId: string;
}): Promise<AgentVersion> {
  const { data, response } = await client.GET("/api/v1/agents/{agent_id}/versions/{version_id}", {
    params: { path: { agent_id: input.agentId, version_id: input.versionId } },
  });
  return requireData(data, response);
}

export async function listAgentRunsPage(input: {
  agentId: string;
  cursor?: string;
  limit: number;
}): Promise<AgentRunPage> {
  const { data, response } = await client.GET("/api/v1/agents/{agent_id}/runs", {
    params: {
      path: { agent_id: input.agentId },
      query: { cursor: input.cursor, limit: input.limit },
    },
  });
  return requireData(data, response);
}

export async function getAgentRun(input: {
  agentId: string;
  runId: string;
}): Promise<AgentRunDetail> {
  const { data, response } = await client.GET("/api/v1/agents/{agent_id}/runs/{run_id}", {
    params: { path: { agent_id: input.agentId, run_id: input.runId } },
  });
  return requireData(data, response);
}

export async function listChatAccessTokensPage(input: {
  cursor?: string;
  limit: number;
}): Promise<ChatAccessTokenPage> {
  const { data, response } = await client.GET("/api/v1/chat-access-tokens", {
    params: { query: { cursor: input.cursor, limit: input.limit } },
  });
  return requireData(data, response);
}

export async function previewChatAccessTokenScopes(
  input: ChatAccessTokenScopePreviewInput,
): Promise<ChatAccessTokenScopePreview> {
  const { data, response } = await client.POST("/api/v1/chat-access-tokens/preview", {
    body: input,
  });
  return requireData(data, response);
}

export async function issueChatAccessToken(input: {
  body: ChatAccessTokenInput;
  csrfToken: string;
  idempotencyKey: string;
}): Promise<IssueChatAccessTokenResult> {
  const { data, error, response } = await client.POST("/api/v1/chat-access-tokens", {
    body: input.body,
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
    },
  });
  if (data !== undefined) return { kind: "issued", token: data };
  if (response.status === 409 && error !== undefined && "code" in error && error.code === "secret_already_issued") {
    return { kind: "secret_already_issued", token: error.token };
  }
  fail(response.status);
}

export async function revokeChatAccessToken(input: {
  csrfToken: string;
  idempotencyKey: string;
  tokenId: string;
}): Promise<ChatAccessTokenSummary> {
  const { data, response } = await client.DELETE("/api/v1/chat-access-tokens/{token_id}", {
    params: {
      header: {
        "Idempotency-Key": input.idempotencyKey,
        "X-CSRF-Token": input.csrfToken,
      },
      path: { token_id: input.tokenId },
    },
  });
  return requireData(data, response);
}

export async function getOperationalOverview(): Promise<OperationalOverview> {
  const { data, response } = await client.GET("/api/v1/overview");
  return requireData(data, response);
}

export async function readiness(): Promise<boolean> {
  const response = await fetch("/health/ready", { credentials: "include" });
  return response.ok;
}

export async function getWikiEvidence(knowledgeBaseId: string, selection: {versionId: string; slug: string; claimId: string; evidenceId: string}): Promise<components["schemas"]["EvidenceExcerpt"]> {
  const { data, response } = await client.GET("/api/v1/knowledge-bases/{knowledge_base_id}/wiki/evidence", { params: { path: { knowledge_base_id: knowledgeBaseId }, query: { version_id: selection.versionId, slug: selection.slug, claim_id: selection.claimId, evidence_id: selection.evidenceId } } });
  return requireData(data, response);
}

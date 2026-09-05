import type { QueryClient } from "@tanstack/react-query";

import { currentSession } from "./client";
import { queryKeys } from "./queries";

export const resourceEvents = {
  agent: ["agent.created", "agent.version.created", "agent.activated", "agent.archived"],
  chatToken: ["chat_token.created", "chat_token.revoked"],
  credential: ["credential.created", "credential.rotated"],
  discordBinding: [
    "discord.binding.created",
    "discord.binding.deleted",
    "discord.binding.job_cancelled",
    "discord.binding.job_failed",
    "discord.binding.test_requested",
    "discord.binding.unhealthy",
    "discord.binding.updated",
    "discord.binding.validated",
  ],
  discordConnection: [
    "discord.connection.created",
    "discord.connection.job_cancelled",
    "discord.connection.job_failed",
    "discord.connection.job_requested",
    "discord.connection.state_changed",
    "discord.connection.token_rotated",
    "discord.connection.updated",
  ],
  discordDirectory: ["discord.directory.refreshed"],
  job: [
    "job.cancel_requested",
    "job.cancelled",
    "job.claimed",
    "job.enqueued",
    "job.failed",
    "job.heartbeat",
    "job.retry_wait",
    "job.succeeded",
  ],
  knowledgeBase: [
    "knowledge_base.created",
    "knowledge_base.deleted",
    "knowledge_base.pending_delete",
    "knowledge_base.restored",
    "knowledge_base.updated",
  ],
  model: [
    "model_profile.created",
    "model_profile.updated",
    "model_profile.version_appended",
  ],
  assignment: ["model_assignment.updated"],
  provider: ["provider_endpoint.created", "provider_endpoint.updated"],
  providerCapture: [
    "discovery.failed",
    "discovery.running",
    "discovery.scheduled",
    "discovery.succeeded",
    "discovery.superseded",
    "probe.failed",
    "probe.running",
    "probe.scheduled",
    "probe.succeeded",
    "probe.superseded",
    "provider_endpoint.health_checked",
  ],
  run: [
    "documentation_run.requested",
    "documentation_run.planning",
    "documentation_run.generating",
    "documentation_run.finalizing",
    "documentation_run.no_op",
    "documentation_run.published",
    "documentation_run.interrupted",
    "documentation_run.failed",
    "documentation_page.completed",
  ],
  source: [
    "repository_source.created",
    "repository_source.updated",
    "source.active",
    "source.disabled",
    "source.removed",
    "source.health_updated",
    "source_sync.scheduled",
    "source_sync.running",
    "source_sync.failed",
    "source_sync.succeeded",
    "source_sync.superseded",
    "source_revision.created",
    "website_source.created",
    "website_source.updated",
  ],
} as const;

let eventCursor: string | undefined;

export function clearEventStreamCursor(): void {
  eventCursor = undefined;
}

function storedCursor(): string | undefined {
  return eventCursor;
}

function rememberCursor(event: Event): void {
  if (!(event instanceof MessageEvent) || event.lastEventId === "") return;
  if (!/^(0|[1-9][0-9]*)$/.test(event.lastEventId) || BigInt(event.lastEventId) > 9_223_372_036_854_775_807n) return;
  eventCursor = event.lastEventId;
}

export function connectEventStream(queryClient: QueryClient): () => void {
  const cursor = storedCursor();
  const source = new EventSource(`/api/v1/events${cursor === undefined ? "" : `?after=${cursor}`}`);
  const subscriptions: Array<[string, EventListener]> = [];
  let checkingSession = false;

  function hasResourceSnapshot(event: Event): boolean {
    if (!(event instanceof MessageEvent) || typeof event.data !== "string") return false;
    try {
      const value: unknown = JSON.parse(event.data);
      return (
        typeof value === "object"
        && value !== null
        && "id" in value
        && typeof value.id === "string"
      );
    } catch {
      return false;
    }
  }

  function subscribe(eventNames: readonly string[], key: readonly string[]): void {
    for (const eventName of eventNames) {
      const listener: EventListener = (event) => {
        rememberCursor(event);
        if (!hasResourceSnapshot(event)) return;
        void queryClient.invalidateQueries({ queryKey: key });
      };
      source.addEventListener(eventName, listener);
      subscriptions.push([eventName, listener]);
    }
  }

  subscribe(resourceEvents.credential, queryKeys.credentials);
  subscribe(resourceEvents.credential, queryKeys.agentReadiness);
  subscribe(resourceEvents.credential, queryKeys.chatAccessTokens);
  subscribe(resourceEvents.agent, queryKeys.agents);
  subscribe(resourceEvents.agent, queryKeys.agentReadiness);
  subscribe(resourceEvents.agent, queryKeys.agentVersions);
  subscribe(resourceEvents.agent, queryKeys.chatAccessTokens);
  subscribe(resourceEvents.chatToken, queryKeys.chatAccessTokens);
  subscribe(resourceEvents.discordBinding, queryKeys.discordBindings);
  subscribe(resourceEvents.discordConnection, queryKeys.discordConnections);
  subscribe(resourceEvents.discordDirectory, queryKeys.discordServers);
  subscribe(resourceEvents.job, queryKeys.jobs);
  subscribe(resourceEvents.job, queryKeys.runs);
  subscribe(resourceEvents.job, queryKeys.wiki);
  subscribe(resourceEvents.knowledgeBase, queryKeys.knowledgeBases);
  subscribe(resourceEvents.knowledgeBase, queryKeys.agentReadiness);
  subscribe(resourceEvents.knowledgeBase, queryKeys.chatAccessTokens);
  subscribe(resourceEvents.model, queryKeys.models);
  subscribe(resourceEvents.model, queryKeys.agentReadiness);
  subscribe(resourceEvents.model, queryKeys.chatAccessTokens);
  subscribe(resourceEvents.assignment, queryKeys.modelAssignments);
  subscribe(resourceEvents.provider, queryKeys.providers);
  subscribe(resourceEvents.provider, queryKeys.agentReadiness);
  subscribe(resourceEvents.provider, queryKeys.chatAccessTokens);
  subscribe(resourceEvents.providerCapture, queryKeys.providers);
  subscribe(resourceEvents.providerCapture, queryKeys.models);
  subscribe(resourceEvents.providerCapture, queryKeys.agentReadiness);
  subscribe(resourceEvents.providerCapture, queryKeys.chatAccessTokens);
  subscribe(resourceEvents.run, queryKeys.runs);
  subscribe(["documentation_run.published"], queryKeys.wiki);
  subscribe(["documentation_run.published"], queryKeys.agentReadiness);
  subscribe(["documentation_run.published"], queryKeys.chatAccessTokens);
  subscribe(resourceEvents.source, queryKeys.sources);

  const resetListener: EventListener = (event) => {
    rememberCursor(event);
    if (!hasResourceSnapshot(event)) return;
    void queryClient.invalidateQueries();
  };
  source.addEventListener("stream.reset", resetListener);
  subscriptions.push(["stream.reset", resetListener]);

  const errorListener: EventListener = () => {
    if (checkingSession) return;
    checkingSession = true;
    void currentSession()
      .catch(() => undefined)
      .finally(() => { checkingSession = false; });
  };
  source.addEventListener("error", errorListener);
  subscriptions.push(["error", errorListener]);

  return () => {
    for (const [eventName, listener] of subscriptions) {
      source.removeEventListener(eventName, listener);
    }
    source.close();
  };
}

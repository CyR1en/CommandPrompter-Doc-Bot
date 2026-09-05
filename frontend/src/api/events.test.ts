import { QueryClient } from "@tanstack/react-query";
import { createBrowserHistory } from "@tanstack/react-router";
import { afterEach, expect, test, vi } from "vitest";

import { installUnauthorizedHandler } from "./client";
import { clearEventStreamCursor, connectEventStream, resourceEvents } from "./events";

class FakeEventSource {
  static latest: FakeEventSource | undefined;
  readonly listeners = new Map<string, Set<EventListener>>();
  readonly url: string;
  closed = false;

  constructor(url: string) {
    this.url = url;
    FakeEventSource.latest = this;
  }

  addEventListener(name: string, listener: EventListener): void {
    const listeners = this.listeners.get(name) ?? new Set<EventListener>();
    listeners.add(listener);
    this.listeners.set(name, listeners);
  }

  removeEventListener(name: string, listener: EventListener): void {
    this.listeners.get(name)?.delete(listener);
  }

  close(): void {
    this.closed = true;
  }

  emit(name: string, data: string, lastEventId = ""): void {
    const event = new MessageEvent(name, { data, lastEventId });
    for (const listener of this.listeners.get(name) ?? []) listener(event);
  }
}

afterEach(() => {
  vi.unstubAllGlobals();
  history.replaceState({}, "");
  clearEventStreamCursor();
  FakeEventSource.latest = undefined;
});

test("producer event names match their consumer query families", () => {
  expect(resourceEvents.discordBinding).toContain("discord.binding.unhealthy");
  expect(resourceEvents.discordBinding).toContain("discord.binding.job_failed");
  expect(resourceEvents.discordConnection).toContain("discord.connection.state_changed");
  expect(resourceEvents.discordConnection).toContain("discord.connection.job_cancelled");
  expect(resourceEvents.discordDirectory).toEqual(["discord.directory.refreshed"]);
  expect(resourceEvents.agent).toContain("agent.version.created");
  expect(resourceEvents.chatToken).toEqual(["chat_token.created", "chat_token.revoked"]);
  expect(resourceEvents.providerCapture).toContain("provider_endpoint.health_checked");
  expect(resourceEvents.source).toContain("source_revision.created");
  expect(resourceEvents.source).toContain("website_source.created");
  expect(resourceEvents.job).toContain("job.enqueued");
  expect(resourceEvents.job).toContain("job.retry_wait");
});

test("Discord, provider, and source producer events reach their dashboard consumers", () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const queryClient = new QueryClient();
  const invalidate = vi.spyOn(queryClient, "invalidateQueries");
  const disconnect = connectEventStream(queryClient);
  const source = FakeEventSource.latest;
  if (source === undefined) throw new Error("event stream was not opened");

  source.emit("discord.connection.state_changed", JSON.stringify({ id: "connection-id" }));
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["discord-connections"] });
  invalidate.mockClear();
  source.emit("discord.directory.refreshed", JSON.stringify({ id: "connection-id" }));
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["discord-servers"] });
  invalidate.mockClear();
  source.emit("provider_endpoint.health_checked", JSON.stringify({ id: "endpoint-id" }));
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["provider-endpoints"] });
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["model-profiles"] });
  invalidate.mockClear();
  source.emit("website_source.created", JSON.stringify({ id: "source-id" }));
  source.emit("source_revision.created", JSON.stringify({ id: "revision-id" }));
  expect(invalidate).toHaveBeenCalledTimes(2);
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["sources"] });
  disconnect();
});

test("a reconstructed stream resumes from the per-tab cursor and reset invalidates all queries", () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const queryClient = new QueryClient();
  const invalidate = vi.spyOn(queryClient, "invalidateQueries");
  const disconnectFirst = connectEventStream(queryClient);
  const first = FakeEventSource.latest;
  if (first === undefined) throw new Error("event stream was not opened");
  first.emit("credential.rotated", JSON.stringify({ id: "credential-id" }), "41");
  disconnectFirst();

  const disconnectSecond = connectEventStream(queryClient);
  const second = FakeEventSource.latest;
  if (second === undefined) throw new Error("event stream was not reconstructed");

  expect(second.url).toBe("/api/v1/events?after=41");
  second.emit("stream.reset", JSON.stringify({ id: "event_stream", reason: "cursor_pruned" }), "52");
  expect(invalidate).toHaveBeenCalledWith();
  disconnectSecond();

  const disconnectThird = connectEventStream(queryClient);
  expect(FakeEventSource.latest?.url).toBe("/api/v1/events?after=52");
  disconnectThird();
});

test("durable resource snapshots invalidate every dependent query family", () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const queryClient = new QueryClient();
  const invalidate = vi.spyOn(queryClient, "invalidateQueries");
  const disconnect = connectEventStream(queryClient);
  const source = FakeEventSource.latest;
  if (source === undefined) throw new Error("event stream was not opened");

  source.emit("credential.rotated", JSON.stringify({ id: "credential-id", secret_version: 2 }), "53");
  source.emit("knowledge_base.updated", JSON.stringify({ id: "knowledge-base-id", version: 3 }));
  source.emit("job.succeeded", "not-json");

  expect(invalidate).toHaveBeenCalledTimes(6);
  expect(invalidate).toHaveBeenNthCalledWith(1, { queryKey: ["credentials"] });
  expect(invalidate).toHaveBeenNthCalledWith(2, { queryKey: ["agent-readiness"] });
  expect(invalidate).toHaveBeenNthCalledWith(3, { queryKey: ["chat-access-tokens"] });
  expect(invalidate).toHaveBeenNthCalledWith(4, { queryKey: ["knowledge-bases"] });
  expect(invalidate).toHaveBeenNthCalledWith(5, { queryKey: ["agent-readiness"] });
  expect(invalidate).toHaveBeenNthCalledWith(6, { queryKey: ["chat-access-tokens"] });
  disconnect();
  expect(source.closed).toBe(true);

  const disconnectResumed = connectEventStream(queryClient);
  expect(FakeEventSource.latest?.url).toBe("/api/v1/events?after=53");
  disconnectResumed();
});

test("Agent and chat-token events refresh catalog, readiness, history, and effective scopes", () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const queryClient = new QueryClient();
  const invalidate = vi.spyOn(queryClient, "invalidateQueries");
  const disconnect = connectEventStream(queryClient);
  const source = FakeEventSource.latest;
  if (source === undefined) throw new Error("event stream was not opened");

  source.emit("agent.version.created", JSON.stringify({ id: "agent-id", version: 2 }));
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["agents"] });
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["agent-readiness"] });
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["agent-versions"] });
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["chat-access-tokens"] });
  invalidate.mockClear();

  source.emit("chat_token.revoked", JSON.stringify({ id: "token-id" }));
  expect(invalidate).toHaveBeenCalledOnce();
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["chat-access-tokens"] });
  disconnect();
});

test("persisting an event cursor does not emit router navigation", () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const routerHistory = createBrowserHistory();
  const navigated = vi.fn();
  const unsubscribe = routerHistory.subscribe(navigated);
  const disconnect = connectEventStream(new QueryClient());
  try {
    const source = FakeEventSource.latest;
    if (source === undefined) throw new Error("event stream was not opened");

    source.emit("credential.rotated", JSON.stringify({ id: "credential-id" }), "54");

    expect(navigated).not.toHaveBeenCalled();
  } finally {
    disconnect();
    unsubscribe();
    routerHistory.destroy();
  }
});

test("an SSE reconnect failure rechecks authentication so expiry can close the stream", async () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  vi.stubGlobal("fetch", vi.fn(async () => new Response(
    JSON.stringify({ title: "Authentication required", status: 401 }),
    { status: 401, headers: { "Content-Type": "application/json" } },
  )));
  const unauthorized = vi.fn();
  const uninstall = installUnauthorizedHandler(unauthorized);
  const disconnect = connectEventStream(new QueryClient());
  const source = FakeEventSource.latest;
  if (source === undefined) throw new Error("event stream was not opened");

  source.emit("error", "");
  await vi.waitFor(() => expect(unauthorized).toHaveBeenCalledOnce());
  disconnect();
  uninstall();
});

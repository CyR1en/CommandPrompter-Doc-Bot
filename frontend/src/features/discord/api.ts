import { ApiError, type Job } from "../../api/client";
import type { DiscordBinding, DiscordBindingInput, DiscordBindingUpdateInput, DiscordChannel, DiscordConnection, DiscordInstallation, DiscordRole, DiscordServer } from "./types";

type ProtectedInput = { csrfToken: string; idempotencyKey: string };

export function listDiscordConnections(): Promise<DiscordConnection[]> {
  return requestJson("/api/v1/discord/connections");
}

export function getDiscordConnection(id: string): Promise<DiscordConnection> {
  return requestJson(`/api/v1/discord/connections/${encodeURIComponent(id)}`);
}

export function createDiscordConnection(input: ProtectedInput & { credentialId: string; displayName: string }): Promise<DiscordConnection> {
  return requestJson("/api/v1/discord/connections", write(input, "POST", { credential_id: input.credentialId, display_name: input.displayName }));
}

export function updateDiscordConnection(input: ProtectedInput & { body: { display_name?: string; expected_version: number; lifecycle?: "enabled" | "disabled" }; id: string }): Promise<DiscordConnection> {
  return requestJson(`/api/v1/discord/connections/${encodeURIComponent(input.id)}`, write(input, "PATCH", input.body));
}

export function validateDiscordConnection(input: ProtectedInput & { expectedVersion: number; id: string }): Promise<Job> {
  return connectionJob(input, "validate");
}

export function refreshDiscordConnection(input: ProtectedInput & { expectedVersion: number; id: string }): Promise<Job> {
  return connectionJob(input, "refresh");
}

export function rotateDiscordToken(input: ProtectedInput & { credentialId: string; expectedVersion: number; id: string }): Promise<DiscordConnection> {
  return requestJson(`/api/v1/discord/connections/${encodeURIComponent(input.id)}/rotate-token`, write(input, "POST", { credential_id: input.credentialId, expected_version: input.expectedVersion }));
}

export function getDiscordInstallation(input: ProtectedInput & { id: string; threads: boolean }): Promise<DiscordInstallation> {
  return requestJson(`/api/v1/discord/connections/${encodeURIComponent(input.id)}/installation-url`, write(input, "POST", { threads: input.threads }));
}

export function listDiscordServers(connectionId: string): Promise<DiscordServer[]> {
  return requestJson(`/api/v1/discord/connections/${encodeURIComponent(connectionId)}/servers`);
}

export function listDiscordChannels(connectionId: string, serverId: string): Promise<DiscordChannel[]> {
  return requestJson(`/api/v1/discord/connections/${encodeURIComponent(connectionId)}/servers/${encodeURIComponent(serverId)}/channels`);
}

export function listDiscordRoles(connectionId: string, serverId: string): Promise<DiscordRole[]> {
  return requestJson(`/api/v1/discord/connections/${encodeURIComponent(connectionId)}/servers/${encodeURIComponent(serverId)}/roles`);
}

export function listDiscordBindings(): Promise<DiscordBinding[]> {
  return requestJson("/api/v1/discord/bindings");
}

export function getDiscordBinding(id: string): Promise<DiscordBinding> {
  return requestJson(`/api/v1/discord/bindings/${encodeURIComponent(id)}`);
}

export function createDiscordBinding(input: ProtectedInput & { body: DiscordBindingInput }): Promise<DiscordBinding> {
  return requestJson("/api/v1/discord/bindings", write(input, "POST", input.body));
}

export function updateDiscordBinding(input: ProtectedInput & { body: DiscordBindingUpdateInput; id: string }): Promise<DiscordBinding> {
  return requestJson(`/api/v1/discord/bindings/${encodeURIComponent(input.id)}`, write(input, "PATCH", input.body));
}

export function deleteDiscordBinding(input: ProtectedInput & { expectedVersion: number; id: string }): Promise<void> {
  return requestEmpty(`/api/v1/discord/bindings/${encodeURIComponent(input.id)}`, write(input, "DELETE", { expected_version: input.expectedVersion }));
}

export function validateDiscordBinding(input: ProtectedInput & { expectedVersion: number; id: string }): Promise<DiscordBinding> {
  return requestJson(`/api/v1/discord/bindings/${encodeURIComponent(input.id)}/validate`, write(input, "POST", { expected_version: input.expectedVersion }));
}

export function testDiscordBinding(input: ProtectedInput & { expectedVersion: number; id: string }): Promise<Job> {
  return requestJson(`/api/v1/discord/bindings/${encodeURIComponent(input.id)}/test-message`, write(input, "POST", { expected_version: input.expectedVersion }));
}

function connectionJob(input: ProtectedInput & { expectedVersion: number; id: string }, action: "refresh" | "validate"): Promise<Job> {
  return requestJson(`/api/v1/discord/connections/${encodeURIComponent(input.id)}/${action}`, write(input, "POST", { expected_version: input.expectedVersion }));
}

function write(input: ProtectedInput, method: string, body: unknown): RequestInit {
  return {
    body: JSON.stringify(body),
    headers: {
      "Content-Type": "application/json",
      "Idempotency-Key": input.idempotencyKey,
      "X-CSRF-Token": input.csrfToken,
    },
    method,
  };
}

async function requestJson<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await request(path, init);
  if (!response.ok) throw new ApiError(response.status);
  return response.json();
}

async function requestEmpty(path: string, init?: RequestInit): Promise<void> {
  const response = await request(path, init);
  if (!response.ok) throw new ApiError(response.status);
}

function request(path: string, init?: RequestInit): Promise<Response> {
  return globalThis.fetch(new Request(new URL(path, window.location.origin), { credentials: "include", ...init }));
}

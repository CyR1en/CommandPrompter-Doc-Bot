import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";

import { actionId, createCredential, getAgent, getAgentReadiness, safeErrorMessage, type Agent, type AgentReadiness, type Job } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { ConfirmationDialog } from "../../app/ConfirmationDialog";
import { Select } from "../../app/Select";
import { EmptyState, ErrorNotice, StatusBadge } from "../../app/StatusBadge";
import { useCsrfToken } from "../../app/auth";
import { useAgentPages } from "../agents/queries";
import { JobEnqueueNotice } from "../jobs/JobEnqueueNotice";
import {
  createDiscordBinding,
  createDiscordConnection,
  deleteDiscordBinding,
  getDiscordInstallation,
  listDiscordBindings,
  listDiscordChannels,
  listDiscordConnections,
  listDiscordRoles,
  listDiscordServers,
  refreshDiscordConnection,
  rotateDiscordToken,
  testDiscordBinding,
  updateDiscordBinding,
  updateDiscordConnection,
  validateDiscordBinding,
  validateDiscordConnection,
} from "./api";
import type { DiscordBinding, DiscordBindingInput, DiscordChannel, DiscordConnection, DiscordInstallation, DiscordRole, DiscordTrigger } from "./types";

type View = "connections" | "servers" | "bindings" | "health";

export interface DiscordSearch {
  agent_id?: string;
  connection_id?: string;
  server_id?: string;
  view?: View;
}

type DiscordSurfaceProps =
  | { kind: "standalone"; search: DiscordSearch }
  | { agent: Agent; kind: "agent"; readiness: AgentReadiness };

export function DiscordPage({ search = {} }: { search?: DiscordSearch } = {}): ReactNode {
  return <DiscordSurface kind="standalone" search={search} />;
}

export function AgentDiscordPanel({ agent, readiness }: { agent: Agent; readiness: AgentReadiness }): ReactNode {
  return <DiscordSurface agent={agent} kind="agent" readiness={readiness} />;
}

function DiscordSurface(props: DiscordSurfaceProps): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const initialSearch = props.kind === "standalone" ? props.search : {};
  const [view, setView] = useState<View>(initialSearch.view ?? (props.kind === "agent" ? "bindings" : "connections"));
  const [selectedAgentId, setSelectedAgentId] = useState(initialSearch.agent_id ?? "");
  const agentId = props.kind === "agent" ? props.agent.id : selectedAgentId;
  const [connectionId, setConnectionId] = useState(initialSearch.connection_id ?? "");
  const [serverId, setServerId] = useState(initialSearch.server_id ?? "");
  const workingSetKey = `${agentId}:${connectionId}:${serverId}:${view}`;
  const workingSetKeyRef = useRef(workingSetKey);
  workingSetKeyRef.current = workingSetKey;
  const [error, setError] = useState<{ key: string; message: string } | null>(null);
  const reportError = useCallback((message: string | null): void => {
    const ownedKey = workingSetKey;
    setError((current) => {
      if (message === null) return current?.key === ownedKey ? null : current;
      if (workingSetKeyRef.current !== ownedKey) return current;
      return { key: ownedKey, message };
    });
  }, [workingSetKey]);
  const agents = useAgentPages(props.kind === "standalone");
  const connections = useQuery({ queryKey: queryKeys.discordConnections, queryFn: listDiscordConnections });
  const bindings = useQuery({ queryKey: queryKeys.discordBindings, queryFn: listDiscordBindings });
  const agentItems = agents.data?.pages.flatMap((page) => page.items) ?? [];
  const loadedAgent = agentItems.find((agent) => agent.id === agentId);
  const directAgent = useQuery({
    enabled: props.kind === "standalone" && agentId !== "" && !agents.isPending && loadedAgent === undefined,
    queryKey: [...queryKeys.agents, "detail", agentId],
    queryFn: () => getAgent(agentId),
  });
  const readiness = useQuery({
    enabled: props.kind === "standalone" && agentId !== "",
    queryKey: [...queryKeys.agentReadiness, agentId],
    queryFn: () => getAgentReadiness(agentId),
  });
  const serverDirectories = useQueries({
    queries: (connections.data ?? []).map((connection) => ({
      queryKey: [...queryKeys.discordServers, connection.id],
      queryFn: () => listDiscordServers(connection.id),
    })),
  });
  const selectableAgents = directAgent.data && loadedAgent === undefined ? [directAgent.data, ...agentItems] : agentItems;
  const selectedAgent = props.kind === "agent" ? props.agent : loadedAgent ?? directAgent.data;
  const selectedReadiness = props.kind === "agent" ? props.readiness : readiness.data;
  const allServers = serverDirectories.flatMap((query) => query.data ?? []);
  const servers = allServers.filter((server) => server.connection_id === connectionId);
  const channels = useQuery({
    enabled: connectionId !== "" && serverId !== "",
    queryKey: [...queryKeys.discordServers, connectionId, serverId, "channels"],
    queryFn: () => listDiscordChannels(connectionId, serverId),
  });
  const roles = useQuery({
    enabled: connectionId !== "" && serverId !== "",
    queryKey: [...queryKeys.discordServers, connectionId, serverId, "roles"],
    queryFn: () => listDiscordRoles(connectionId, serverId),
  });

  useEffect(() => {
    if (!connectionId && connections.data?.[0]) setConnectionId(connections.data[0].id);
  }, [connectionId, connections.data]);
  useEffect(() => {
    if (!serverId && servers[0]) setServerId(servers[0].server_id);
  }, [serverId, servers]);
  useEffect(() => {
    setError((current) => current?.key === workingSetKey ? current : null);
  }, [workingSetKey]);

  const selectedConnection = connections.data?.find((item) => item.id === connectionId);
  const queryError = connections.error ?? bindings.error ?? agents.error ?? directAgent.error ?? readiness.error ?? serverDirectories.find((query) => query.error)?.error ?? channels.error ?? roles.error;

  async function settle(): Promise<void> {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: queryKeys.discordConnections }),
      queryClient.invalidateQueries({ queryKey: queryKeys.discordBindings }),
      queryClient.invalidateQueries({ queryKey: queryKeys.discordServers }),
    ]);
  }

  return (
    <section aria-label={props.kind === "agent" ? "Discord delivery" : undefined} className={`${props.kind === "agent" ? "agent-discord-panel" : "page"} discord-page`} id={props.kind === "agent" ? "discord" : undefined}>
      <header className="page-heading">
        <div><p className="eyebrow">Discord delivery</p>{props.kind === "agent" ? <h2>Bot and channel operations</h2> : <h1>Bot and channel operations</h1>}</div>
        <div className="discord-working-set">
          {props.kind === "standalone" ? (
            <label className="compact-field">Agent<Select onChange={setSelectedAgentId} options={[{ label: "Select an Agent", value: "" }, ...selectableAgents.map((agent) => ({ label: `${agent.current_version.configuration.display_name} · ${agent.selector}`, value: agent.id }))]} value={agentId} /></label>
          ) : <p className="agent-fixed-selector"><span>Fixed Agent</span><strong>{props.agent.selector}</strong></p>}
          <label className="compact-field">
            Working connection
            <Select
              onChange={(next) => { setConnectionId(next); setServerId(""); }}
              options={[{ label: "Select a connection", value: "" }, ...(connections.data ?? []).map((item) => ({ label: `${item.display_name} · ${item.state}`, value: item.id }))]}
              value={connectionId}
            />
          </label>
          {props.kind === "agent" ? <Link className="button secondary" search={{ agent_id: props.agent.id, connection_id: connectionId || undefined, server_id: serverId || undefined, view: "bindings" }} to="/discord">Open full Discord desk</Link> : null}
        </div>
      </header>
      {props.kind === "standalone" && agents.hasNextPage ? <button className="button secondary discord-agent-more" disabled={agents.isFetchingNextPage} onClick={() => void agents.fetchNextPage()} type="button">{agents.isFetchingNextPage ? "Loading Agents…" : "Load more Agents"}</button> : null}
      <nav aria-label="Discord views" className="discord-tabs">
        {(["connections", "servers", "bindings", "health"] satisfies View[]).map((item) => <button aria-current={view === item ? "page" : undefined} key={item} onClick={() => setView(item)} type="button">{(item[0] ?? "").toUpperCase() + item.slice(1)}</button>)}
      </nav>
      <ErrorNotice message={error?.key === workingSetKey ? error.message : queryError ? safeErrorMessage(queryError) : null} />
      {connections.isPending || bindings.isPending || (props.kind === "standalone" && (agents.isPending || directAgent.isFetching)) ? <p aria-live="polite" className="notice">Loading Discord configuration…</p> : null}
      {view === "connections" ? <ConnectionsView connections={connections.data ?? []} csrfToken={csrfToken} key={selectedConnection?.id ?? "unselected"} onError={reportError} onSelect={setConnectionId} onSettle={settle} selected={selectedConnection} /> : null}
      {view === "servers" ? <ServersView channels={channels.data ?? []} connection={selectedConnection} csrfToken={csrfToken} key={selectedConnection?.id ?? "unselected"} onError={reportError} onServer={setServerId} onSettle={settle} roles={roles.data ?? []} selectedServerId={serverId} servers={servers} /> : null}
      {view === "bindings" ? <BindingsView agent={selectedAgent} bindings={(bindings.data ?? []).filter((binding) => binding.connection_id === connectionId && binding.server_id === serverId && (agentId === "" || binding.agent_id === agentId))} channels={channels.data ?? []} connection={selectedConnection} csrfToken={csrfToken} onError={reportError} onSettle={settle} readiness={selectedReadiness} roles={roles.data ?? []} selectedServerId={serverId} servers={servers} /> : null}
      {view === "health" ? <HealthView bindings={bindings.data ?? []} connections={connections.data ?? []} servers={allServers} /> : null}
    </section>
  );
}

function ConnectionsView({ connections, csrfToken, onError, onSelect, onSettle, selected }: {
  connections: DiscordConnection[];
  csrfToken: string;
  onError(value: string | null): void;
  onSelect(value: string): void;
  onSettle(): Promise<void>;
  selected: DiscordConnection | undefined;
}): ReactNode {
  const [displayName, setDisplayName] = useState("");
  const [credentialLabel, setCredentialLabel] = useState("");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [job, setJob] = useState<Job | null>(null);
  const [installation, setInstallation] = useState<DiscordInstallation | null>(null);
  const [threads, setThreads] = useState(false);
  const [replacementLabel, setReplacementLabel] = useState("");
  const [replacementToken, setReplacementToken] = useState("");
  const [editedName, setEditedName] = useState("");

  useEffect(() => setEditedName(selected?.display_name ?? ""), [selected]);

  async function create(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    setBusy(true);
    onError(null);
    try {
      const credential = await createCredential({ csrfToken, idempotencyKey: actionId(), kind: "discord_bot_token", label: credentialLabel, secret: token });
      const connection = await createDiscordConnection({ credentialId: credential.id, csrfToken, displayName, idempotencyKey: actionId() });
      setDisplayName("");
      setCredentialLabel("");
      setToken("");
      onSelect(connection.id);
      await onSettle();
    } catch (caught: unknown) {
      onError(safeErrorMessage(caught));
    } finally {
      setBusy(false);
    }
  }

  async function connectionJob(action: "refresh" | "validate"): Promise<void> {
    if (!selected) return;
    onError(null);
    try {
      const queued = action === "validate"
        ? await validateDiscordConnection({ csrfToken, expectedVersion: selected.version, id: selected.id, idempotencyKey: actionId() })
        : await refreshDiscordConnection({ csrfToken, expectedVersion: selected.version, id: selected.id, idempotencyKey: actionId() });
      setJob(queued);
      await onSettle();
    } catch (caught: unknown) {
      onError(safeErrorMessage(caught));
    }
  }

  async function installationUrl(): Promise<void> {
    if (!selected) return;
    onError(null);
    try {
      setInstallation(await getDiscordInstallation({ csrfToken, id: selected.id, idempotencyKey: actionId(), threads }));
    } catch (caught: unknown) {
      onError(safeErrorMessage(caught));
    }
  }

  async function rotate(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (!selected) return;
    setBusy(true);
    onError(null);
    try {
      const credential = await createCredential({ csrfToken, idempotencyKey: actionId(), kind: "discord_bot_token", label: replacementLabel, secret: replacementToken });
      await rotateDiscordToken({ credentialId: credential.id, csrfToken, expectedVersion: selected.version, id: selected.id, idempotencyKey: actionId() });
      setReplacementLabel("");
      setReplacementToken("");
      await onSettle();
    } catch (caught: unknown) {
      onError(safeErrorMessage(caught));
    } finally {
      setBusy(false);
    }
  }

  async function toggle(): Promise<void> {
    if (!selected) return;
    onError(null);
    try {
      await updateDiscordConnection({ body: { expected_version: selected.version, lifecycle: selected.lifecycle === "enabled" ? "disabled" : "enabled" }, csrfToken, id: selected.id, idempotencyKey: actionId() });
      await onSettle();
    } catch (caught: unknown) {
      onError(safeErrorMessage(caught));
    }
  }

  async function rename(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (!selected || !editedName.trim()) return;
    onError(null);
    try {
      await updateDiscordConnection({ body: { display_name: editedName.trim(), expected_version: selected.version }, csrfToken, id: selected.id, idempotencyKey: actionId() });
      await onSettle();
    } catch (caught: unknown) {
      onError(safeErrorMessage(caught));
    }
  }

  return (
    <div className="discord-columns">
      <section aria-labelledby="new-connection-title" className="folio-panel">
        <p className="eyebrow">Step 1</p><h2 id="new-connection-title">Store token and create connection</h2>
        <form className="form-grid" onSubmit={(event) => void create(event)}>
          <label>Connection name<input maxLength={255} onChange={(event) => setDisplayName(event.currentTarget.value)} required value={displayName} /></label>
          <label>Credential label<input maxLength={255} onChange={(event) => setCredentialLabel(event.currentTarget.value)} required value={credentialLabel} /></label>
          <label className="full-field">Discord bot token<input autoComplete="off" onChange={(event) => setToken(event.currentTarget.value)} required type="password" value={token} /></label>
          <p className="field-note full-field">The token is written once to the encrypted credential vault. Connection records retain only its credential ID.</p>
          <button className="button primary" disabled={busy} type="submit">{busy ? "Encrypting token…" : "Create Discord connection"}</button>
        </form>
      </section>
      <section aria-labelledby="connection-identity-title" className="folio-panel">
        <p className="eyebrow">Steps 2–6</p><h2 id="connection-identity-title">Validate identity and install</h2>
        {selected ? (
          <>
            <div className="identity-card"><div><strong>{selected.bot_username ?? "Identity not validated"}</strong><span>{selected.application_id ? `application ${selected.application_id}` : "Validate the token to load bot identity"}</span><span>{selected.bot_user_id ? `bot user ${selected.bot_user_id}` : "Credential reference stored"}</span></div><StatusBadge value={selected.state} /></div>
            <form className="inline-edit-form" onSubmit={(event) => void rename(event)}><label>Display name<input maxLength={255} onChange={(event) => setEditedName(event.currentTarget.value)} required value={editedName} /></label><button className="button secondary" disabled={editedName.trim() === selected.display_name} type="submit">Save name</button></form>
            <div className="button-row"><button className="button primary" onClick={() => void connectionJob("validate")} type="button">Validate token and identity</button><button className="button secondary" onClick={() => void connectionJob("refresh")} type="button">Refresh joined servers</button></div>
            {job ? <JobEnqueueNotice jobId={job.id} label="Discord operation" /> : null}
            <label className="check-row"><input checked={threads} onChange={(event) => { setThreads(event.currentTarget.checked); setInstallation(null); }} type="checkbox" />Include public-thread permissions</label>
            <button className="button secondary" disabled={!selected.application_id} onClick={() => void installationUrl()} type="button">Generate installation URL</button>
            {installation ? <div className="notice"><p>Scopes: {installation.scopes.join(", ")} · permissions {installation.permissions}</p><a className="button primary" href={installation.url} rel="noreferrer" target="_blank">Open Discord authorization</a></div> : null}
            <form className="inline-secret-form" onSubmit={(event) => void rotate(event)}>
              <h3>Rotate bot token</h3>
              <label>New credential label<input onChange={(event) => setReplacementLabel(event.currentTarget.value)} required value={replacementLabel} /></label>
              <label>Replacement token<input autoComplete="off" onChange={(event) => setReplacementToken(event.currentTarget.value)} required type="password" value={replacementToken} /></label>
              <button className="button secondary" disabled={busy} type="submit">Store and reconnect</button>
            </form>
            <div className="danger-zone compact-danger"><button className="button secondary" onClick={() => void toggle()} type="button">{selected.lifecycle === "enabled" ? "Disable connection" : "Enable connection"}</button></div>
          </>
        ) : <EmptyState>Create or select a Discord connection.</EmptyState>}
      </section>
      {connections.length === 0 ? <EmptyState>No Discord connections are configured.</EmptyState> : null}
    </div>
  );
}

function ServersView({ channels, connection, csrfToken, onError, onServer, onSettle, roles, selectedServerId, servers }: {
  channels: DiscordChannel[];
  connection: DiscordConnection | undefined;
  csrfToken: string;
  onError(value: string | null): void;
  onServer(value: string): void;
  onSettle(): Promise<void>;
  roles: DiscordRole[];
  selectedServerId: string;
  servers: Array<{ name: string; owner: boolean; refreshed_at: string; server_id: string }>;
}): ReactNode {
  const [job, setJob] = useState<Job | null>(null);
  async function refresh(): Promise<void> {
    if (!connection) return;
    onError(null);
    try {
      const queued = await refreshDiscordConnection({ csrfToken, expectedVersion: connection.version, id: connection.id, idempotencyKey: actionId() });
      setJob(queued);
      await onSettle();
    } catch (caught: unknown) {
      onError(safeErrorMessage(caught));
    }
  }
  return (
    <section aria-labelledby="server-directory-title" className="ledger-section">
      <div className="section-heading"><div><p className="eyebrow">Installed membership</p><h2 id="server-directory-title">Servers and permission metadata</h2></div><button className="button secondary" disabled={!connection} onClick={() => void refresh()} type="button">Refresh from Discord</button></div>
      {job ? <JobEnqueueNotice jobId={job.id} label="Server refresh" /> : null}
      {connection ? <label className="compact-field">Server<Select onChange={onServer} options={[{ label: "Select a server", value: "" }, ...servers.map((server) => ({ label: `${server.name} · ${server.server_id}`, value: server.server_id }))]} value={selectedServerId} /></label> : <EmptyState>Select a connection first.</EmptyState>}
      {selectedServerId ? (
        <div className="discord-columns server-metadata">
          <div className="table-wrap"><table><caption>Supported channels</caption><thead><tr><th>Channel</th><th>Type</th><th>Permissions</th><th>Audience</th></tr></thead><tbody>{channels.map((channel) => <tr key={channel.channel_id}><th scope="row">#{channel.name}<small>{channel.channel_id}</small></th><td>{channelType(channel.channel_type)}</td><td><StatusBadge value={channel.permission_status} /></td><td>{audienceDescription(channel, roles)}</td></tr>)}</tbody></table></div>
          <div className="table-wrap"><table><caption>Invocation roles</caption><thead><tr><th>Role</th><th>Discord ID</th><th>Position</th></tr></thead><tbody>{roles.map((role) => <tr key={role.role_id}><th scope="row">{role.name}</th><td>{role.role_id}</td><td>{role.position}</td></tr>)}</tbody></table></div>
        </div>
      ) : null}
      {selectedServerId && channels.length === 0 ? <EmptyState>No supported text channels were discovered.</EmptyState> : null}
    </section>
  );
}

function BindingsView({ agent, bindings, channels, connection, csrfToken, onError, onSettle, readiness, roles, selectedServerId, servers }: {
  agent: Agent | undefined;
  bindings: DiscordBinding[];
  channels: DiscordChannel[];
  connection: DiscordConnection | undefined;
  csrfToken: string;
  onError(value: string | null): void;
  onSettle(): Promise<void>;
  readiness: AgentReadiness | undefined;
  roles: DiscordRole[];
  selectedServerId: string;
  servers: Array<{ name: string; server_id: string }>;
}): ReactNode {
  if (!agent) return <EmptyState>Select an Agent before creating or reviewing channel bindings.</EmptyState>;
  if (!connection || !selectedServerId) return <EmptyState>Select a connection and server in the Servers view before creating a channel binding.</EmptyState>;
  return (
    <div className="discord-binding-layout">
      <BindingForm agent={agent} channels={channels} connection={connection} csrfToken={csrfToken} key={`${agent.id}:${connection.id}:${selectedServerId}`} onError={onError} onSettle={onSettle} readiness={readiness} roles={roles} selectedServerId={selectedServerId} />
      <section aria-labelledby="binding-list-title" className="ledger-section"><div className="section-heading"><h2 id="binding-list-title">Channel bindings</h2><span>{bindings.length}</span></div>{bindings.map((binding) => <BindingCard agentLabel={agent.selector} agentLifecycle={agent.lifecycle} binding={binding} channels={channels} connectionName={connection.display_name} csrfToken={csrfToken} key={binding.id} onError={onError} onSettle={onSettle} ready={readiness?.ready === true} serverName={servers.find((item) => item.server_id === binding.server_id)?.name} />)}{bindings.length === 0 ? <EmptyState>No channel bindings are configured for this Agent and route.</EmptyState> : null}</section>
    </div>
  );
}

function BindingForm({ agent, channels, connection, csrfToken, onError, onSettle, readiness, roles, selectedServerId }: {
  agent: Agent;
  channels: DiscordChannel[];
  connection: DiscordConnection;
  csrfToken: string;
  onError(value: string | null): void;
  onSettle(): Promise<void>;
  readiness: AgentReadiness | undefined;
  roles: DiscordRole[];
  selectedServerId: string;
}): ReactNode {
  const [listenChannelId, setListenChannelId] = useState("");
  const [triggers, setTriggers] = useState<DiscordTrigger[]>(["mention"]);
  const [replyPolicy, setReplyPolicy] = useState<DiscordBindingInput["reply_policy"]>("same_channel");
  const [replyChannelId, setReplyChannelId] = useState("");
  const [allowedRoleIds, setAllowedRoleIds] = useState<string[]>([]);
  const [allowedUsers, setAllowedUsers] = useState("");
  const [rateRequests, setRateRequests] = useState(5);
  const [rateWindow, setRateWindow] = useState(60);
  const [busy, setBusy] = useState(false);
  const listen = channels.find((item) => item.channel_id === listenChannelId);
  const reply = replyPolicy === "selected_channel" ? channels.find((item) => item.channel_id === replyChannelId) : listen;
  const policyChecks = bindingChecks(listen, reply, replyPolicy, readiness?.effective_access, allowedRoleIds, parseIds(allowedUsers), triggers);

  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (!policyChecks.ok) return;
    setBusy(true);
    onError(null);
    try {
      await createDiscordBinding({
        body: { agent_id: agent.id, allowed_role_ids: allowedRoleIds, allowed_user_ids: parseIds(allowedUsers), connection_id: connection.id, enabled: false, listen_channel_id: listenChannelId, rate_requests: rateRequests, rate_window_seconds: rateWindow, reply_channel_id: replyPolicy === "selected_channel" ? replyChannelId : null, reply_policy: replyPolicy, server_id: selectedServerId, triggers },
        csrfToken,
        idempotencyKey: actionId(),
      });
      setListenChannelId("");
      setAllowedRoleIds([]);
      setAllowedUsers("");
      await onSettle();
    } catch (caught: unknown) {
      onError(safeErrorMessage(caught));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section aria-labelledby="new-binding-title" className="folio-panel binding-wizard"><p className="eyebrow">Channel setup</p><h2 id="new-binding-title">Create, test, then enable</h2>
      <form className="form-grid" onSubmit={(event) => void submit(event)}>
        <p className="agent-binding-target full-field"><span>Agent target</span><strong>{agent.selector}</strong><small>{readiness ? `${readiness.effective_access} · ${readiness.ready ? "ready" : "not ready"}` : "Resolving readiness…"}</small></p>
        <label>Listen channel<Select onChange={setListenChannelId} options={[{ label: "Select a channel", value: "" }, ...channels.map((channel) => ({ label: `#${channel.name} · ${channelType(channel.channel_type)}`, value: channel.channel_id }))]} required value={listenChannelId} /></label>
        <fieldset><legend>Triggers</legend>{(["mention", "slash_command"] satisfies DiscordTrigger[]).map((value) => <label className="check-row" key={value}><input checked={triggers.includes(value)} onChange={(event) => { const checked = event.currentTarget.checked; setTriggers((current) => checked ? [...current, value] : current.filter((trigger) => trigger !== value)); }} type="checkbox" />{value === "mention" ? "Mention" : "Slash command"}</label>)}</fieldset>
        <fieldset><legend>Reply destination</legend>{(["same_channel", "thread", "selected_channel"] as const).map((value) => <label className="check-row" key={value}><input checked={replyPolicy === value} name="reply" onChange={() => setReplyPolicy(value)} type="radio" />{value.replaceAll("_", " ")}</label>)}</fieldset>
        {replyPolicy === "selected_channel" ? <label className="full-field">Reply channel<Select onChange={setReplyChannelId} options={[{ label: "Select reply channel", value: "" }, ...channels.map((channel) => ({ label: `#${channel.name}`, value: channel.channel_id }))]} required value={replyChannelId} /></label> : null}
        <fieldset className="full-field role-grid"><legend>Allowed roles</legend>{roles.map((role) => <label className="check-row" key={role.role_id}><input checked={allowedRoleIds.includes(role.role_id)} onChange={(event) => { const checked = event.currentTarget.checked; setAllowedRoleIds((current) => checked ? [...current, role.role_id] : current.filter((id) => id !== role.role_id)); }} type="checkbox" />{role.name} <small>{role.role_id}</small></label>)}</fieldset>
        <label className="full-field">Allowed user IDs<textarea onChange={(event) => setAllowedUsers(event.currentTarget.value)} placeholder="One Discord user ID per line" rows={3} value={allowedUsers} /></label>
        <label>Requests per window<input max={100} min={1} onChange={(event) => setRateRequests(event.currentTarget.valueAsNumber)} type="number" value={rateRequests} /></label>
        <label>Window seconds<input max={86400} min={1} onChange={(event) => setRateWindow(event.currentTarget.valueAsNumber)} type="number" value={rateWindow} /></label>
        <section aria-labelledby="permission-review-title" className={`permission-review full-field ${policyChecks.ok ? "is-ready" : "is-blocked"}`}><h3 id="permission-review-title">Audience and permission review</h3><ul>{policyChecks.messages.map((message) => <li key={message}>{message}</li>)}</ul><p>Every user who can view the reply destination can read the response.</p></section>
        <button className="button primary" disabled={busy || !policyChecks.ok} type="submit">{busy ? "Creating draft…" : "Create draft binding"}</button>
      </form>
    </section>
  );
}

function BindingCard({ agentLabel, agentLifecycle, binding, channels, connectionName, csrfToken, onError, onSettle, ready, serverName }: {
  agentLabel: string;
  agentLifecycle: Agent["lifecycle"];
  binding: DiscordBinding;
  channels: DiscordChannel[];
  connectionName: string;
  csrfToken: string;
  onError(value: string | null): void;
  onSettle(): Promise<void>;
  ready: boolean;
  serverName?: string;
}): ReactNode {
  const [job, setJob] = useState<Job | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const listenName = channels.find((item) => item.channel_id === binding.listen_channel_id)?.name ?? binding.listen_channel_id;
  const executable = agentLifecycle === "active" && ready;
  async function act(action: "enable" | "test" | "validate"): Promise<void> {
    onError(null);
    try {
      if (action === "test") setJob(await testDiscordBinding({ csrfToken, expectedVersion: binding.version, id: binding.id, idempotencyKey: actionId() }));
      else if (action === "validate") await validateDiscordBinding({ csrfToken, expectedVersion: binding.version, id: binding.id, idempotencyKey: actionId() });
      else await updateDiscordBinding({ body: { enabled: !binding.enabled, expected_version: binding.version }, csrfToken, id: binding.id, idempotencyKey: actionId() });
      await onSettle();
    } catch (caught: unknown) {
      onError(safeErrorMessage(caught));
    }
  }
  async function remove(): Promise<void> {
    onError(null);
    setDeleteError(null);
    try {
      await deleteDiscordBinding({ csrfToken, expectedVersion: binding.version, id: binding.id, idempotencyKey: actionId() });
      await onSettle();
    } catch (caught: unknown) {
      const message = safeErrorMessage(caught);
      setDeleteError(message);
      onError(message);
      throw caught;
    }
  }
  return (
    <article className="binding-card"><header><div><h3 ref={headingRef}>#{listenName}</h3><p>{connectionName} · {serverName ?? binding.server_id} · {agentLabel}</p></div><StatusBadge value={binding.enabled ? binding.health : "draft"} /></header><dl><div><dt>Triggers</dt><dd>{binding.triggers.map((trigger) => trigger.replaceAll("_", " ")).join(", ")}</dd></div><div><dt>Reply</dt><dd>{binding.reply_policy.replaceAll("_", " ")}</dd></div><div><dt>Allowlist</dt><dd>{binding.allowed_role_ids.length} roles · {binding.allowed_user_ids.length} users</dd></div><div><dt>Rate limit</dt><dd>{binding.rate_requests} / {binding.rate_window_seconds}s</dd></div></dl>{binding.sanitized_error ? <p className="notice error" role="alert">{binding.sanitized_error}</p> : null}<div className="button-row"><button className="button secondary" onClick={() => void act("validate")} type="button">Validate permissions</button><button className="button secondary" onClick={() => void act("test")} type="button">Send test message</button><button className="button primary" disabled={!binding.enabled && (binding.health !== "healthy" || !executable)} onClick={() => void act("enable")} type="button">{binding.enabled ? "Disable" : "Enable"}</button><button className="button danger" onClick={() => { setDeleteError(null); setConfirmDelete(true); }} type="button">Delete</button></div>{!binding.enabled && !executable ? <p className="field-note">{bindingExecutionNote(agentLifecycle, ready)}</p> : null}{job ? <JobEnqueueNotice jobId={job.id} label="Discord test message" /> : null}<ConfirmationDialog confirmLabel="Delete binding" error={deleteError} fallbackFocusRef={headingRef} onClose={() => { setDeleteError(null); setConfirmDelete(false); }} onConfirm={remove} open={confirmDelete} title={`Delete #${listenName} binding?`}><p>This immediately stops replies through this channel binding and removes its configuration.</p></ConfirmationDialog></article>
  );
}

function bindingExecutionNote(lifecycle: Agent["lifecycle"], ready: boolean): string {
  if (lifecycle === "draft" && ready) return "Activate the Agent before enabling this binding.";
  if (lifecycle === "archived" && ready) return "Reactivate the Agent before enabling this binding.";
  return "Resolve Agent readiness issues before enabling this binding.";
}

function HealthView({ bindings, connections, servers }: { bindings: DiscordBinding[]; connections: DiscordConnection[]; servers: Array<{ connection_id: string }> }): ReactNode {
  return (
    <section aria-labelledby="discord-health-title" className="ledger-section"><div className="section-heading"><h2 id="discord-health-title">Gateway and binding health</h2><span>{connections.filter((item) => item.state === "ready").length} ready</span></div><div className="health-grid">{connections.map((connection) => { const ownedBindings = bindings.filter((item) => item.connection_id === connection.id); return <article className="folio-panel health-card" key={connection.id}><header><div><h3>{connection.display_name}</h3><p>{connection.bot_username ?? "Identity pending"}</p></div><StatusBadge value={connection.state} /></header><dl><div><dt>Gateway latency</dt><dd>{connection.gateway_latency_ms === null ? "Unavailable" : `${connection.gateway_latency_ms} ms`}</dd></div><div><dt>Last heartbeat</dt><dd>{connection.last_heartbeat_at ? formatDate(connection.last_heartbeat_at) : "None"}</dd></div><div><dt>Last event</dt><dd>{connection.last_event_at ? formatDate(connection.last_event_at) : "None"}</dd></div><div><dt>Servers</dt><dd>{servers.filter((item) => item.connection_id === connection.id).length}</dd></div><div><dt>Active bindings</dt><dd>{ownedBindings.filter((item) => item.enabled).length}</dd></div><div><dt>Unhealthy bindings</dt><dd>{ownedBindings.filter((item) => item.health === "unhealthy").length}</dd></div></dl>{connection.sanitized_error ? <p className="notice error" role="alert">{connection.sanitized_error}</p> : null}</article>; })}</div>{connections.length === 0 ? <EmptyState>No connections are available to monitor.</EmptyState> : null}</section>
  );
}

export function bindingChecks(listen: DiscordChannel | undefined, reply: DiscordChannel | undefined, replyPolicy: DiscordBindingInput["reply_policy"], access: "public" | "restricted" | undefined, roles: string[], users: string[], triggers: DiscordTrigger[]): { messages: string[]; ok: boolean } {
  const problems: string[] = [];
  const notes: string[] = [];
  if (triggers.length === 0) problems.push("Select at least one trigger.");
  if (!listen) problems.push("Select a listen channel.");
  else if (!hasPermissions(listen.effective_bot_permissions, [10, 16])) problems.push("The bot needs View Channel and Read Message History in the listen channel.");
  if (!reply) problems.push("Select a valid reply destination.");
  else {
    const required = [10, 11, 14, 16];
    if (reply.channel_type === 11) required.push(38);
    else if (replyPolicy === "thread") required.push(35, 38);
    if (!hasPermissions(reply.effective_bot_permissions, required)) {
      if (reply.channel_type === 11) problems.push("Replies to an existing thread also need Send Messages in Threads.");
      else if (replyPolicy === "thread") problems.push("Creating a thread reply also needs Create Public Threads and Send Messages in Threads.");
      else problems.push("The bot needs View Channel, Send Messages, Embed Links, and Read Message History in the reply destination.");
    }
    if (access === "restricted" && reply.everyone_can_view) problems.push("A restricted Agent cannot reply to a channel visible to @everyone.");
    if (!reply.everyone_can_view) {
      const extraRoles = reply.viewer_role_ids.filter((id) => !roles.includes(id));
      const extraUsers = reply.viewer_user_ids.filter((id) => !users.includes(id));
      notes.push(`Reply audience snapshot: ${reply.viewer_role_ids.length} viewer roles and ${reply.viewer_user_ids.length} explicit users.`);
      if (extraRoles.length > 0 || extraUsers.length > 0) notes.push(`${extraRoles.length} viewer roles and ${extraUsers.length} explicit viewers are outside the invocation allowlist; they can still read posted responses.`);
    }
  }
  if (access === "restricted" && roles.length === 0 && users.length === 0) problems.push("A restricted Agent requires an allowed role or user.");
  if (!access) problems.push("Agent readiness and effective access are still loading.");
  if (problems.length === 0) notes.unshift("Required bot permissions and audience policy are satisfied. Create the draft, test it, then enable it.");
  return { messages: [...problems, ...notes], ok: problems.length === 0 };
}

function audienceDescription(channel: DiscordChannel, roles: DiscordRole[]): string {
  if (channel.everyone_can_view) return "@everyone can view";
  const roleNames = channel.viewer_role_ids.map((id) => roles.find((role) => role.role_id === id)?.name ?? id);
  const principals = [
    roleNames.length > 0 ? `Roles: ${roleNames.join(", ")}` : "No viewer roles",
    channel.viewer_user_ids.length > 0 ? `Users: ${channel.viewer_user_ids.join(", ")}` : "no explicit users",
  ];
  return principals.join(" · ");
}

function hasPermissions(value: number, bits: number[]): boolean {
  const permissions = BigInt(value);
  return bits.every((bit) => (permissions & (1n << BigInt(bit))) !== 0n);
}

function parseIds(value: string): string[] {
  return [...new Set(value.split(/\s+/).map((item) => item.trim()).filter(Boolean))];
}

function channelType(value: number): string {
  return value === 11 ? "Public thread" : "Text channel";
}

function formatDate(value: string): string {
  return new Date(value).toLocaleString();
}

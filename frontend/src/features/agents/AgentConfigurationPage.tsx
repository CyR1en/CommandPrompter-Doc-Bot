import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";

import {
  actionId,
  createAgent,
  getAgent,
  getAgentReadiness,
  listKnowledgeBases,
  listModelProfiles,
  listProviderEndpoints,
  replaceAgentConfiguration,
  safeErrorMessage,
  setAgentLifecycle,
  type Agent,
  type AgentConfigurationInput,
  type AgentLifecycleTarget,
  type AgentReadiness,
  type CreateAgentInput,
} from "../../api/client";
import { queryKeys } from "../../api/queries";
import { ErrorNotice, StatusBadge } from "../../app/StatusBadge";
import { useCsrfToken } from "../../app/auth";
import { AgentDiscordPanel } from "../discord/DiscordPage";
import { AgentConfigurationForm } from "./AgentConfigurationForm";
import { AgentRunsPanel } from "./AgentRunsPanel";

type AgentConfigurationPageProps =
  | { kind: "create" }
  | { agentId: string; kind: "detail" };

export function AgentConfigurationPage(props: AgentConfigurationPageProps): ReactNode {
  const csrfToken = useCsrfToken();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [candidateReadiness, setCandidateReadiness] = useState<{ source: "configuration" | "lifecycle"; value: AgentReadiness } | null>(null);
  const agentId = props.kind === "detail" ? props.agentId : "";
  const knowledgeBases = useQuery({ queryKey: queryKeys.knowledgeBases, queryFn: listKnowledgeBases });
  const models = useQuery({ queryKey: queryKeys.models, queryFn: () => listModelProfiles() });
  const providers = useQuery({ queryKey: queryKeys.providers, queryFn: listProviderEndpoints });
  const agent = useQuery({
    enabled: props.kind === "detail",
    queryKey: [...queryKeys.agents, "detail", agentId],
    queryFn: () => getAgent(agentId),
  });
  const readiness = useQuery({
    enabled: props.kind === "detail",
    queryKey: [...queryKeys.agentReadiness, agentId],
    queryFn: () => getAgentReadiness(agentId),
  });
  const queryError = knowledgeBases.error ?? models.error ?? providers.error ?? agent.error ?? readiness.error;

  async function create(value: CreateAgentInput): Promise<void> {
    setBusy(true);
    setError(null);
    setCandidateReadiness(null);
    try {
      const created = await createAgent({ body: value, csrfToken, idempotencyKey: actionId() });
      await queryClient.invalidateQueries({ queryKey: queryKeys.agents });
      await navigate({ params: { agentId: created.id }, to: "/agents/$agentId" });
    } catch (caught: unknown) {
      setError(safeErrorMessage(caught));
    } finally {
      setBusy(false);
    }
  }

  async function replace(current: Agent, configuration: AgentConfigurationInput): Promise<void> {
    setBusy(true);
    setError(null);
    setCandidateReadiness(null);
    try {
      const result = await replaceAgentConfiguration({
        agentId: current.id,
        body: { configuration, expected_version: current.version },
        csrfToken,
        idempotencyKey: actionId(),
      });
      if (result.kind === "candidate_not_ready") {
        setCandidateReadiness({ source: "configuration", value: result.readiness });
        return;
      }
      const updated = result.agent;
      queryClient.setQueryData([...queryKeys.agents, "detail", current.id], updated);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.agents }),
        queryClient.invalidateQueries({ queryKey: queryKeys.agentReadiness }),
        queryClient.invalidateQueries({ queryKey: queryKeys.agentVersions }),
        queryClient.invalidateQueries({ queryKey: queryKeys.chatAccessTokens }),
      ]);
    } catch (caught: unknown) {
      setError(safeErrorMessage(caught));
    } finally {
      setBusy(false);
    }
  }

  async function changeLifecycle(current: Agent, lifecycle: AgentLifecycleTarget): Promise<void> {
    setBusy(true);
    setError(null);
    setCandidateReadiness(null);
    try {
      const result = await setAgentLifecycle({
        agentId: current.id,
        body: { expected_version: current.version, lifecycle },
        csrfToken,
        idempotencyKey: actionId(),
      });
      if (result.kind === "candidate_not_ready") {
        setCandidateReadiness({ source: "lifecycle", value: result.readiness });
        queryClient.setQueryData([...queryKeys.agentReadiness, current.id], result.readiness);
        return;
      }
      const updated = result.agent;
      queryClient.setQueryData([...queryKeys.agents, "detail", current.id], updated);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.agents }),
        queryClient.invalidateQueries({ queryKey: queryKeys.agentReadiness }),
        queryClient.invalidateQueries({ queryKey: queryKeys.chatAccessTokens }),
      ]);
    } catch (caught: unknown) {
      setError(safeErrorMessage(caught));
    } finally {
      setBusy(false);
    }
  }

  if (props.kind === "detail" && (agent.isPending || readiness.isPending)) {
    return <main className="page"><p aria-live="polite" className="notice">Loading Agent configuration…</p></main>;
  }

  if (props.kind === "detail" && (!agent.data || !readiness.data)) {
    return <main className="page"><ErrorNotice message={queryError ? safeErrorMessage(queryError) : "The Agent could not be loaded."} /></main>;
  }

  return (
    <section className="page agent-configuration-page">
      <header className="page-heading agent-detail-heading">
        <div>
          <p className="eyebrow">{props.kind === "create" ? "New delivery identity" : agent.data?.selector}</p>
          <h1>{props.kind === "create" ? "Create Agent" : agent.data?.current_version.configuration.display_name}</h1>
          <p className="lede">One complete configuration controls identity, model execution, ordered knowledge, and restrictive answer policy.</p>
        </div>
        {agent.data ? <StatusBadge value={agent.data.lifecycle} /> : <Link className="button secondary" to="/agents">Back to Agents</Link>}
      </header>
      <ErrorNotice message={error ?? (queryError ? safeErrorMessage(queryError) : null)} />
      {props.kind === "detail" && agent.data && readiness.data ? (
        <>
          <AgentControlStrip agent={agent.data} busy={busy} onLifecycle={changeLifecycle} readiness={readiness.data} />
          {candidateReadiness ? <CandidateReadinessNotice source={candidateReadiness.source} value={candidateReadiness.value} /> : null}
          <nav aria-label="Agent configuration sections" className="agent-section-nav">
            <a href="#identity">Identity</a><a href="#model">Model</a><a href="#knowledge">Knowledge</a><a href="#guardrails">Guardrails</a><a href="#discord">Discord</a><a href="#runs">Runs</a><a href="#versions">Versions</a>
          </nav>
          {knowledgeBases.data && models.data && providers.data ? (
            <AgentConfigurationForm
              agent={agent.data}
              busy={busy}
              key={agent.data.current_version_id}
              kind="replace"
              knowledgeBases={knowledgeBases.data}
              models={models.data}
              providers={providers.data}
              onSubmit={(configuration) => void replace(agent.data, configuration)}
            />
          ) : null}
          <AgentDiscordPanel agent={agent.data} readiness={readiness.data} />
          <AgentRunsPanel agentId={agent.data.id} />
        </>
      ) : null}
      {props.kind === "create" && knowledgeBases.data && models.data && providers.data ? (
        <AgentConfigurationForm busy={busy} kind="create" knowledgeBases={knowledgeBases.data} models={models.data} onSubmit={(value) => void create(value)} providers={providers.data} />
      ) : null}
    </section>
  );
}

function CandidateReadinessNotice({ source, value }: {
  source: "configuration" | "lifecycle";
  value: AgentReadiness;
}): ReactNode {
  return (
    <section aria-labelledby="candidate-readiness-title" className="notice error candidate-readiness" role="alert">
      <strong id="candidate-readiness-title">Candidate Agent is not ready.</strong>
      <p>{source === "configuration"
        ? "The active Agent was left unchanged. Resolve these candidate configuration issues and save again."
        : "Activation was rejected. Resolve the readiness issues and try again."}</p>
      <ul>{value.issues.map((issue, index) => <li key={`${issue.code}-${issue.knowledge_base_id ?? index}`}>{humanize(issue.code)}{issue.knowledge_base_id ? ` · ${issue.knowledge_base_id}` : ""}</li>)}</ul>
    </section>
  );
}

function AgentControlStrip({ agent, busy, onLifecycle, readiness }: {
  agent: Agent;
  busy: boolean;
  onLifecycle(agent: Agent, lifecycle: AgentLifecycleTarget): Promise<void>;
  readiness: AgentReadiness;
}): ReactNode {
  const activateLabel = agent.lifecycle === "archived" ? "Reactivate Agent" : "Activate Agent";
  return (
    <section aria-labelledby="agent-readiness-title" className="agent-control-strip">
      <div className="readiness-summary">
        <p className="eyebrow">Current readiness</p>
        <h2 id="agent-readiness-title">{readiness.ready ? "Ready for delivery" : "Configuration needs attention"}</h2>
        <p><StatusBadge value={readiness.ready ? "ready" : "not_ready"} /> <span>{readiness.effective_access} access</span></p>
      </div>
      <dl className="readiness-receipt">
        <div><dt>Agent version</dt><dd>{agent.current_version.version_number}</dd></div>
        <div><dt>Model profile version</dt><dd>{readiness.model_profile_version_number ? `v${readiness.model_profile_version_number}` : "unresolved"}</dd></div>
        <div><dt>Endpoint configuration</dt><dd>{readiness.endpoint_configuration_version ? `v${readiness.endpoint_configuration_version}` : "unresolved"}</dd></div>
      </dl>
      <div className="readiness-issues">
        {readiness.issues.length > 0 ? <ul>{readiness.issues.map((issue, index) => <li key={`${issue.code}-${issue.knowledge_base_id ?? index}`}>{humanize(issue.code)}{issue.knowledge_base_id ? ` · ${issue.knowledge_base_id}` : ""}</li>)}</ul> : <p>Model, provider, credential, and every ordered knowledge base are ready.</p>}
      </div>
      <div aria-label="Agent lifecycle" className="button-row lifecycle-actions">
        {agent.lifecycle === "active"
          ? <button className="button danger" disabled={busy} onClick={() => void onLifecycle(agent, "archived")} type="button">Archive Agent</button>
          : <button className="button primary" disabled={busy || !readiness.ready} onClick={() => void onLifecycle(agent, "active")} type="button">{activateLabel}</button>}
      </div>
      {agent.lifecycle !== "active" && !readiness.ready ? <p className="field-note lifecycle-note">Resolve every readiness issue before activation. Saving draft versions remains available.</p> : null}
    </section>
  );
}

function humanize(value: string): string {
  const phrase = value.replaceAll("_", " ");
  return (phrase[0] ?? "").toUpperCase() + phrase.slice(1);
}

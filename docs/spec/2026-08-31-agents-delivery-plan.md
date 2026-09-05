# Agents and delivery redesign plan

Status: implemented on 2026-08-31. This dated record explains the delivery
design; the live contract is documented in `README.md`, `CONTEXT.md`, and
`docs/architecture/openwiki-platform.md`.

## Outcome

Replace the knowledge-base-owned answer engine with first-class **Agents**.
Operators can create any number of Agents. Each Agent has one configuration page
for its identity, answer model, knowledge bases, guardrails, and Discord
delivery.

The existing dashboard chat and server-managed HTTP conversations are removed.
The external chat surface is an OpenAI/Open WebUI-shaped API where the `model`
field selects an Agent. Open WebUI or another client owns its saved chats and
resends the useful message history on each request.

Knowledge bases remain the evidence, publication, and access boundary. Agents
reference knowledge bases; they do not copy or merge their sources, wikis,
Claims, or indexes.

## Decisions made by this plan

1. **Agent is the answer-time aggregate.** An Agent owns answer identity, one
   model selection, an ordered set of one or more knowledge bases, and
   configurable guardrails.
2. **Documentation models stay on the knowledge base.** The
   `DOCUMENTATION_PLANNER` and `DOCUMENTATION_WRITER` assignments remain. The
   knowledge-base `ANSWER` assignment is deleted.
3. **Agent configuration is versioned.** A stable Agent points to one immutable
   current configuration version. Every run captures the exact Agent, model,
   provider endpoint, credential, and wiki versions it used.
4. **HTTP chat is stateless in the first release.** There is no HTTP
   `conversation_id`, session list, transcript CRUD, or locally managed chat
   history in ref0.
5. **Discord may keep bounded context.** Discord-only context remains separate
   from the Agent executor and uses its own retention policy. Editing an Agent
   starts a fresh context version.
6. **Discord bot identity stays separate from Agent identity.** A channel binding
   selects one Agent. A bot connection can serve several non-overlapping Agent
   bindings, and an Agent can use several bindings. A dedicated bot per Agent is
   supported but not required.
7. **The compatibility API exposes Agents as virtual models.** `model` means an
   immutable Agent key such as `agent:docs-support`; it never exposes or selects
   the upstream provider model.
8. **Guardrails have two levels.** Platform safeguards are fixed and cannot be
   disabled. Agent guardrails can add behavioral instructions and lower typed
   execution limits.
9. **Multi-knowledge-base access is all-or-nothing.** A caller must be authorized
   for the Agent's complete configured corpus. The runtime never silently drops
   an inaccessible or unavailable knowledge base.
10. **This is a hard cutover.** The application is unreleased, so the baseline,
    APIs, UI, and code are replaced without compatibility readers, aliases, or
    data backfills.

## Product experience

### Agents navigation

Replace the dashboard Chat entry with **Agents**. The Agent list is paginated
and has no product-level count limit. Runtime concurrency remains bounded by the
selected provider profiles and deployment settings.

Each Agent page contains:

- **Identity:** immutable Agent key, display name, description, response language,
  and trusted persona instructions.
- **Model:** model profile, reasoning effort, answer mode, capability/readiness
  state, and the exact current model-profile version.
- **Knowledge:** ordered knowledge-base selection, publication health, and the
  effective public/restricted access summary.
- **Guardrails:** behavioral policy, refusal text, evidence access, maximum tool
  calls, and maximum answer tokens. The page separately lists non-disableable
  platform protections so prompt guidance is not presented as enforcement.
- **Discord:** connections and channel bindings that expose this Agent, plus a
  create/connect flow.
- **Runs:** recent outcomes, usage, latency, captured versions, tools, and
  verified citations. This is operational history, not a saved-chat UI.

Saving the page replaces the complete configuration atomically and creates one
immutable Agent version. A failed validation changes nothing. Activation
requires a valid model and at least one active knowledge base with a published
wiki.

### Chat compatibility plane

Expose a separate bearer-authenticated compatibility surface:

```text
GET  /v1/models
POST /v1/chat/completions
```

`GET /v1/models` returns only active, ready Agents allowed by the token. A
typical selector is `agent:docs-support`. The key is immutable; changing the
display name does not break clients.

The first request subset is deliberately narrow:

- `model` is required and selects an Agent.
- `messages` is required, text-only, bounded, and must end in a user message.
- `user`, `assistant`, and string `system` messages are accepted. Client system
  text is demoted to untrusted conversation context below platform and Agent
  instructions.
- Client tools, tool messages, multimodal content, provider model IDs, sampling
  overrides, arbitrary response formats, and provider-specific fields are
  rejected rather than silently ignored.
- `max_tokens` may lower, but never raise, the Agent/model output limit.
- There is one completion. No caller-supplied knowledge-base subset is allowed.

The response uses the standard non-streaming chat-completion envelope, echoes
the Agent selector in `model`, and includes verified citation footnotes in the
assistant Markdown. An `x_ref0` extension contains the run ID, answer status,
and structured citations.

Support `stream: true` only as **buffered verified SSE**: finish the provider
call, apply guardrails and citation verification, then emit the already verified
text as OpenAI-shaped chunks followed by `[DONE]`. It provides client wire
compatibility, not first-token latency. Raw provider tokens must never leave
ref0 before verification.

Use OpenAI-shaped error envelopes on `/v1`. Unknown, archived, unavailable, and
token-unscoped Agent selectors return the same `model_not_found` response so
the endpoint does not disclose Agent existence.

### API credentials

Add chat access tokens distinct from operator sessions and provider keys. Token
plaintext is returned once; only a digest, prefix, label, Agent scopes, expiry,
revocation, and last-use metadata are stored.

The control plane remains operator-session and CSRF authenticated:

```text
GET    /api/v1/agents
POST   /api/v1/agents
GET    /api/v1/agents/{agent_id}
PUT    /api/v1/agents/{agent_id}/configuration
PATCH  /api/v1/agents/{agent_id}/lifecycle
GET    /api/v1/agents/{agent_id}/versions

GET    /api/v1/chat-access-tokens
POST   /api/v1/chat-access-tokens
DELETE /api/v1/chat-access-tokens/{token_id}
```

A token is scoped to explicit Agents. Adding an Agent later does not expand an
existing token. Issuance shows the complete transitive knowledge-base set and
its effective restriction before the operator confirms.

The first release authenticates the Open WebUI installation or API client, not
each downstream Open WebUI user. Per-reader authorization needs per-user tokens
or a later trusted identity integration.

## Domain shape

### Stable Agent and immutable configuration

```text
Agent
  id
  immutable API key
  lifecycle: draft | active | archived
  current configuration version
  optimistic resource version

Agent configuration version
  identity
  answer model selection
  ordered knowledge-base memberships
  configurable guardrails
  creator and creation time
```

The root is the stable selection and lifecycle identity. A complete
configuration update inserts one version and moves the current pointer in the
same transaction. Runs never depend on a mutable join set reconstructed after
the fact.

### Execution receipt

Each invocation captures:

- Agent ID, Agent resource version, and Agent configuration version;
- model profile and immutable model-profile version;
- provider endpoint configuration version, credential identity, and credential
  version;
- every knowledge-base ID, access policy, published wiki version,
  documentation run, and source snapshot used;
- invocation surface and authenticated subject;
- effective output/link policy.

The receipt is recorded with the run. The executor checks that security-relevant
configuration is current immediately before a provider request. Discord checks
the binding, Agent version, and access policy again before delivery.

A newly published wiki does not invalidate a run that already captured an
immutable prior wiki. Agent membership, Agent policy, knowledge-base lifecycle
or access changes, and Discord permission changes do invalidate delivery.
Endpoint versions plus credential identity and version are captured for audit;
routine credential rotation after a completed provider call does not by itself
make the verified answer unsafe to deliver.

### Deep execution interface

HTTP and Discord callers provide one authenticated Agent selection and a
bounded message window. One Agent executor owns:

1. Agent and dependency capture;
2. full-corpus authorization;
3. per-knowledge-base retrieval and fair merge;
4. prompt budgeting and model execution;
5. deterministic guardrails and citation verification;
6. run recording and idempotent settlement.

Capture and execution share a platform-owned 20-hour ceiling derived from the
24-hour reservation lease. Even the maximum bounded provider/tool loop has a
four-hour settlement margin while an abandoned process is eventually reclaimed.

Callers do not load knowledge bases, pick provider models, construct tools, or
validate citations themselves. HTTP and Discord retain only transport-specific
authentication, rate limiting, reauthorization, and presentation.

## Knowledge-base and evidence rules

Knowledge bases continue to own sources, revisions, published wikis, Claims,
evidence, access policy, and documentation planner/writer configuration. Agent
identity is the only answer persona; knowledge-base documentation instructions
are not concatenated into the answer system prompt.

An Agent is callable only when every configured knowledge base is active and
published and the principal is authorized for the complete set. If any member
is restricted, the Agent's effective delivery policy is restricted.

Retrieval preserves isolation:

1. Resolve every member and published wiki in one consistent database read and
   durably reserve those immutable wiki scopes for the bounded execution.
2. Search each captured wiki independently.
3. Take a bounded candidate quota per knowledge base.
4. Merge deterministically with a fair interleave and a global evidence budget.
5. Construct read capabilities only for the captured scopes.

Every search hit, tool handle, and citation is namespaced by knowledge base and
wiki version. Duplicate page slugs, Claim stable IDs, source IDs, or paths in
different knowledge bases cannot collide. The model cannot supply arbitrary
database IDs to widen its corpus.

The number of Agents is not capped. A measured deployment setting may cap the
number of knowledge bases and total evidence budget in one Agent so a single
request cannot multiply cost without bound.

## Guardrail model

### Fixed platform safeguards

These remain code-owned and do not appear as switches on an Agent:

- answer only from the captured authorized corpus;
- treat source, tool, and caller text as untrusted data, not instructions;
- expose only bounded read-only wiki, Claim, and source tools;
- expose no shell, write, network, process, Git, credential, or delegation tool;
- enforce context, output, tool-call, byte, line, and time ceilings outside the
  prompt;
- accept citations only from the current run's evidence ledger;
- remove unsupported material spans or return insufficient evidence;
- suppress restricted links at presentation time;
- never expose hidden reasoning or credentials.

### Agent-configurable restrictions

The first typed Agent policy supports:

- behavioral instructions;
- evidence access of `wiki_only` or `wiki_and_source`;
- a tool-call ceiling no higher than the platform ceiling;
- an answer-token ceiling no higher than the model/platform ceiling;
- deterministic refusal Markdown used when the model chooses `refused`.

The UI labels behavioral instructions as model guidance. Do not claim topic,
PII, moderation, or data-loss-prevention enforcement until a deterministic and
tested rule implementation exists.

Prompt precedence is fixed: platform policy, Agent identity, Agent restrictive
policy, authorized-scope manifest, untrusted caller transcript, then untrusted
tool results.

## Discord delivery

### Cardinality

- One Discord connection represents one bot token/application identity.
- One connection can have many channel bindings.
- One binding selects exactly one Agent.
- One Agent can have many bindings across connections, servers, and channels.
- One bot can serve different Agents on non-overlapping routes.
- A dedicated bot per Agent is a normal setup choice, not a database invariant.

This avoids consuming one gateway connection for every Agent. The current
supervisor limit of 20 active bot connections remains an operational limit to
make configurable and load-test; it is not an Agent count limit.

Normalize triggers so `mention` and `slash_command` are concrete child rows.
`both` creates two rows. A unique key on connection, server, listen channel, and
trigger kind makes one incoming event resolve to exactly one Agent even under
concurrent binding updates.

The existing `/ask` command remains per bot/guild. A bot/channel/trigger route
has one Agent, so the first release needs no public Agent picker in Discord. Two
Agents in the same channel require distinct bots or a later explicit routing
feature.

### Authorization flow

1. Resolve the enabled connection/channel/trigger binding.
2. Load the binding's Agent and complete knowledge-base policy.
3. Check connection health, caller roles/users, strictest access policy, reply
   audience, bot permissions, and durable rate limit.
4. Mint an execution grant and delivery permit; the event handler never passes
   knowledge-base IDs.
5. Execute the Agent against the captured corpus.
6. Reload authoritative connection, binding, Agent, access, roles, audience,
   destination, and bot permissions.
7. Deliver only if the permit and execution receipt are still valid.

The post-model check must suppress delivery when any of these change during the
run: binding target/version/lifecycle, Agent version/lifecycle/membership,
knowledge-base lifecycle/access, invoking roles, reply audience, channel
existence, connection state, or bot permissions.

## Sessions, audit, and retention

Delete the generic dashboard/HTTP conversation model in the first cut:

- no `conversation_id` on `/v1/chat/completions`;
- no conversation list/get/update/delete API;
- no dashboard Chat page or navigation;
- no browser local-storage transcript owned by ref0;
- no full caller-supplied HTTP transcript stored by default.

Keep durable Agent runs for audit, cost, evidence, and failure diagnosis. They
store version receipts, origin, request digest, outcome, usage, latency, bounded
tool audit, citation IDs, and sanitized errors. Retaining the current question
or verified answer body is a separate configurable audit-retention choice, not
a saved-chat feature.

Preserve bounded Discord-only context so follow-up questions work across events
and restarts. Key it by binding, Agent version, external user, and destination.
Give it an independent idle expiry and retention setting. It is not exposed
through the compatibility or operator APIs.

If saved ref0 chats are added later, introduce an explicit session resource
that stores bounded messages and calls the same stateless Agent executor. Do not
add session behavior to the execution contract.

## Storage cutover

Rewrite the unreleased baseline with these ownership changes:

| Record | Purpose and invariant |
| --- | --- |
| `agents` | Stable ID/key, lifecycle, current version pointer, optimistic version. |
| `agent_versions` | Immutable identity, model selection, guardrails, creator, and version number. |
| `agent_version_knowledge_bases` | Ordered non-empty unique memberships for one version. |
| `chat_access_tokens` | Digest-only bearer credentials with expiry/revocation. |
| `chat_access_token_agents` | Explicit Agent scopes; no implicit future expansion. |
| `agent_runs` | Stateless execution audit and captured Agent/model/provider versions. |
| `agent_run_knowledge_bases` | Exact access/wiki/documentation-run scope per run. |
| `agent_run_scope_reservations` | Bounded capture-to-settlement fence that protects each immutable wiki scope from retention. |
| `discord_conversations` | Optional bounded Discord-only context, versioned by Agent. |
| `discord_conversation_messages` | Discord context messages under independent retention. |
| `channel_bindings` | Replace `knowledge_base_id` with `agent_id`. |
| `channel_binding_triggers` | Concrete mention/slash routes with structural uniqueness. |
| `model_assignments` | Restrict to planner and writer roles; delete `ANSWER`. |

Delete the generic `conversations`, `conversation_messages`, and existing
knowledge-base-shaped query-run definitions. Do not add compatibility views or
dual foreign keys.

## Module and UI ownership

```text
internal/agents/
  types.go          Agent/version/run domain types
  catalog.go        Atomic configuration and lifecycle operations
  store.go          PostgreSQL representation and run audit
  scope.go          Agent/model/KB capture and freshness checks
  engine.go         One deep completion operation
  retrieval.go      Per-KB search and fair merge
  tools.go          Capability-scoped wiki/source tools
  guardrails.go     Fixed platform policy plus restrictive Agent policy
  validation.go     Boundary and citation verification

internal/chattokens/
  service.go        Issue, digest, scope, revoke, and authenticate tokens

internal/api/
  agents.go         Operator control-plane adapter
  chat_tokens.go    Token management adapter
  openai_chat.go    /v1 models/completions wire and buffered SSE adapter

internal/discord/
  bindings.go       Binding -> Agent and trigger uniqueness
  invocations.go    Initial authorization and final reauthorization
  context.go        Discord-only bounded context
  answer_handler.go Thin adapter around the Agent executor

frontend/src/features/agents/
  AgentsPage.tsx
  AgentConfigurationPage.tsx
  identity, model, knowledge, guardrail, Discord, and run panels
```

Move the reusable budget, retrieval, source-reader, model-loop, and citation
logic from `internal/answers` into the new ownership. Delete `internal/answers`
after all callers move; do not leave a forwarding compatibility service.

## Delivery milestones

### 1. Replace the baseline and domain ownership

- Add Agent, version, membership, token, run-scope, and Discord-context tables.
- Replace `channel_bindings.knowledge_base_id` with `agent_id` and normalize
  trigger rows.
- Restrict knowledge-base model assignments to planner and writer.
- Delete generic conversation storage and old query-run ownership.
- Add Agent domain validation and atomic catalog operations.

Exit: a fresh database enforces immutable Agent versions, complete ordered
membership, exact token scopes, non-overlapping Discord routes, and no `ANSWER`
knowledge-base role.

### 2. Add the Agent control plane

- Add Agent and chat-token APIs.
- Add Agents navigation, list, and one configuration page.
- Move answer settings out of the knowledge-base model-assignment UI.
- Show readiness, effective access, Discord bindings, and recent runs.

Exit: an operator can create, configure, activate, archive, and version several
Agents that reuse models and knowledge bases without partial configuration.

### 3. Build the verified multi-KB Agent executor

- Port current answer behavior behind the deep Agent execution interface.
- Capture Agent/model/provider/credential/wiki versions.
- Add isolated per-KB retrieval, fair merge, scope handles, and namespaced
  citations.
- Separate fixed platform safeguards from configurable restrictions.
- Record stateless Agent runs.

Exit: one Agent can answer from two knowledge bases with colliding page/Claim
names, and every delivered material span resolves to evidence from the exact
captured scope.

### 4. Ship the OpenAI/Open WebUI compatibility plane

- Add scoped bearer token issuance and authentication.
- Add `/v1/models` and non-streaming `/v1/chat/completions`.
- Add buffered verified SSE for `stream: true`.
- Add explicit supported-field validation and OpenAI-shaped errors.
- Do not add a replacement dashboard chat client.

Exit: a standard OpenAI client and the supported Open WebUI version can list
authorized Agents, select one through `model`, and receive a verified answer
without creating ref0 conversation rows.

### 5. Switch Discord bindings to Agents

- Update Discord domain, API, worker, gateway, frontend, and tests from
  knowledge-base selection to Agent selection.
- Compute effective access over the complete Agent knowledge set.
- Key Discord context by Agent version.
- Extend final delivery reauthorization to Agent and corpus state.
- Add Agent-page flows for new or existing bot connections and bindings.

Exit: one connection can expose several Agents on disjoint routes; a dedicated
bot can expose one Agent; and every security-relevant mid-run change suppresses
delivery.

### 6. Remove the superseded product surface

- Delete the dashboard Chat page/navigation, knowledge-base chat route,
  conversation API, generic conversation tables, and frontend query types.
- Delete the `ANSWER` role and old knowledge-base answer controls.
- Delete `internal/answers` after the Agent executor owns its behavior.
- Update OpenAPI, generated client types, metrics, retention, backup/restore,
  README, context vocabulary, architecture, and release verification.

Exit: repository and schema searches find no executable legacy selector,
conversation endpoint, compatibility alias, or knowledge-base `ANSWER` role.

### 7. Release proof

Run the complete Go, PostgreSQL, frontend, browser, deployment, backup/restore,
secret-containment, race, and generated-contract suites against a clean database
created only from the rewritten baseline.

## Verification strategy

### Domain and database

- Atomic configuration replacement creates one immutable version; stale
  optimistic versions and idempotency conflicts leave no partial membership.
- Agent keys are immutable and unique; display names may change.
- Knowledge-base membership is non-empty, ordered, unique, and protected from
  silent deletion.
- Activation rejects unpublished/archived knowledge bases, unavailable model
  profiles, unsupported answer modes, and limits above platform/model ceilings.
- Documentation planner/writer capture remains unchanged; `ANSWER` cannot be
  stored.
- Runs capture exact Agent, model, endpoint, credential, and every wiki version.

### Retrieval and guardrails

- Identical slugs, Claim IDs, paths, and labels in different knowledge bases do
  not collide.
- Forged, stale, cross-Agent, cross-KB, and cross-version handles fail closed.
- Search merging is deterministic, bounded, and gives each member a fair
  candidate opportunity.
- A principal unauthorized for one member cannot call the Agent or learn which
  member caused denial.
- Agent/client/source instructions cannot add tools or override platform policy.
- Unknown or unsupported citations are removed or force insufficient evidence
  before any adapter receives content.

### Chat compatibility and privacy

- `/v1/models` returns only token-scoped, active, ready Agents.
- `model` accepts the immutable Agent key but not a display name, database UUID,
  or provider model ID.
- Token secret-once behavior, digest storage, Agent scopes, expiry, revocation,
  and non-enumerating authorization are covered.
- Unsupported roles/fields and oversized transcripts fail explicitly.
- Non-streaming and buffered streaming responses contain only verified text.
- HTTP calls create no conversation/message rows and do not retain the supplied
  transcript by default.

### Discord and race safety

- One bot serves several Agents on disjoint routes; one Agent uses several
  bindings; overlapping trigger routes cannot both enable.
- Mixed public/restricted Agents require an allowlist and private reply audience.
- Pause after model completion and mutate each of: binding target/version,
  Agent version/lifecycle/membership, knowledge-base lifecycle/access, caller
  roles, reply audience, channel existence, connection state, and bot
  permissions. Every mutation suppresses delivery.
- A new wiki publication does not invalidate evidence captured from the prior
  immutable wiki.
- Existing rate limits, mention/slash handling, reply policies, permission
  checks, token rotation, and gateway ownership remain covered.

### UI and cutover

- Agents navigation replaces Chat.
- The Agent page round-trips identity, model, ordered knowledge bases,
  guardrails, readiness, and Discord bindings.
- Knowledge-base pages show only planner/writer assignments.
- Generated OpenAPI/client types contain the new contracts and no old chat or
  conversation shapes.
- Old paths return 404; no legacy branch or data-compatibility code exists.

## Risks and remaining product choices

- **Open WebUI compatibility:** the wire contract is pinned to Open WebUI
  `v0.11.3`. Buffered SSE satisfies the tested subset but not necessarily every
  feature or plugin.
- **Downstream reader identity:** an installation-level token does not identify
  individual Open WebUI readers. Add OIDC, signed proxy identity, or per-user
  tokens only when that requirement is explicit.
- **Guardrail promise:** prompt-only topical rules are advisory. Any claim of PII
  redaction, moderation, or DLP needs a deterministic enforcement design.
- **Discord memory:** bounded Discord-only context is retained; HTTP execution
  remains stateless and stores no transcript.
- **Retention:** Agent-run metadata defaults to 90 days and Discord context to
  seven days. Agent runs contain bounded execution receipts, not question or
  answer bodies.
- **Fan-out:** one Agent version admits at most 32 ordered knowledge bases, with
  a global bounded evidence budget. Benchmark representative deployments before
  raising either ceiling.
- **Bot scale:** if the product requires a dedicated bot for every Agent, raise
  and load-test the current 20-gateway supervisor limit rather than treating
  Agent count as gateway count.

## Architecture synthesis record

The selected base is the versioned Agent aggregate with an immutable execution
receipt, scoped bearer tokens, stateless OpenAI-shaped HTTP chat, Discord-only
bounded context, and binding-level Agent routing.

Two details were added during comparison:

- normalized Discord trigger rows encode overlap and concurrency safety more
  strongly than preflight checks alone;
- the execution receipt also captures endpoint configuration and credential
  versions, and the verification plan includes a full post-model delivery-race
  matrix.

Rejected shapes were: a thin Agent facade over knowledge-base answer engines,
mutable independently patched Agent subresources, request-selected KB subsets,
server-owned HTTP sessions, raw provider token streaming, and a mandatory
one-Agent/one-bot connection constraint.

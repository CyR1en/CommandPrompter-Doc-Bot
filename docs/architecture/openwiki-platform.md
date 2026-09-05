# ref0 platform architecture

This page describes the current ref0 runtime.

## One binary owns four process roles

The repository builds one Go executable from `cmd/ref0`. It accepts exactly four
top-level commands.

| Command | Ownership |
| --- | --- |
| `ref0 api` | Serves the Huma HTTP API, authenticated dashboard, server-sent events, health checks, and Prometheus metrics. |
| `ref0 worker` | Claims durable jobs, syncs sources, runs documentation generation, discovers providers, refreshes Discord state, and applies retention. |
| `ref0 discord` | Supervises enabled Discord connections and sends questions through the verified Agent executor. |
| `ref0 migrate` | Runs the embedded Goose schema command. Supported subcommands are `up`, `down`, `status`, and `version`. |

There are no command aliases and no second application runtime. PostgreSQL is
the shared authority for all four processes.

## State has three owners

PostgreSQL owns configuration and durable state. The schema stores operators,
sessions, credentials, knowledge bases, source revisions, model profile
versions, jobs and attempts, documentation runs, wiki metadata, Agent versions
and execution receipts, bounded Discord context, events, and audit records.

`APP_DATA_DIR`, `/app/data` in the container, owns application bytes. Source
snapshots, accepted page artifacts, run artifacts, and published wiki bundles
use ID-scoped relative keys. The artifact stores reject path traversal,
symlinked path components, and non-regular files. Publication writes immutable
files and moves the database pointer only after validation succeeds.

The Pi capsule owns one isolated agent attempt. It does not own provider
credentials, provider networking, source files, or durable state. See
[Pi capsule isolation](pi-capsule.md) for the trust boundary.

Back up PostgreSQL, the complete application-data volume, and the matching
master key set as one recovery point. See
[backup and restore](../operations/backup-and-restore.md).

## Packages keep policy separate from adapters

| Package | Responsibility |
| --- | --- |
| `internal/api` | HTTP input and output, authentication middleware, CSRF enforcement, OpenAPI, events, health, and metrics |
| `internal/auth` and `internal/security` | Bootstrap, sessions, scrypt password hashes, secret values, and versioned AES-256-GCM credential envelopes |
| `internal/knowledgebases` | Knowledge-base lifecycle and access policy |
| `internal/sources`, `internal/sourcegit`, and `internal/sourcefiles` | Source configuration, safe Git and website acquisition, immutable snapshots, and bounded source reads |
| `internal/providers` and `internal/safenet` | Model endpoint configuration, captured profile versions, outbound URL policy, DNS pinning, TLS, redirects, and bounded responses |
| `internal/documentation` and `internal/artifacts` | Run state, page validation, claims, evidence, deterministic wiki publication, and exports |
| `internal/capsule` and `internal/capsuledoc` | Trusted capsule host, protocol validation, provider relay, source tools, prompts, and documentation runtime adapter |
| `internal/agents` and `internal/chattokens` | Versioned Agent configuration, scoped access tokens, multi-knowledge-base execution, citation verification, and immutable run receipts |
| `internal/discord` | Stored connections, directory and permission checks, gateway supervision, trigger policy, rate limits, and answer presentation |
| `internal/jobs`, `internal/worker`, `internal/workerruntime`, and `internal/retention` | Fenced leases, handler composition, polling, and replay-safe cleanup |

Domain services accept parsed values rather than HTTP, Discord, or filesystem
framework objects. Adapters translate at the boundary.

## Durable work is fenced

The queue accepts only these job types:

- `VALIDATE_SOURCE`
- `SYNC_SOURCE`
- `PREPARE_RUN`
- `PLAN_RUN`
- `GENERATE_PAGE`
- `FINALIZE_RUN`
- `DISCOVER_ENDPOINT`
- `PROBE_MODEL`
- `REFRESH_DISCORD`
- `PURGE_KNOWLEDGE_BASE`
- `APPLY_RETENTION`

A claim returns a permit containing the job ID, worker ID, and lease generation.
Every state-changing handler operation checks that permit. The worker renews a
lease before it expires. A crashed worker cannot commit with a stale permit, and
another worker can reclaim the job after expiry. Retry delay is stored in
`not_before`, so a restart does not erase backoff.

Documentation runs move through this state machine:

```text
PREPARING -> NO_OP
          -> PLANNING -> GENERATING -> FINALIZING -> PUBLISHED
          -> FAILED        |               |
                           +-> INTERRUPTED <-+
```

`NO_OP`, `PUBLISHED`, `INTERRUPTED`, and `FAILED` are terminal. Page jobs move
through `PENDING`, `RUNNING`, `COMPLETE`, or `SKIPPED`. A run captures source
revision IDs, source fingerprints, provider profile versions, instructions,
language, and the prior wiki pointer before model work starts. Content-only
source updates leave the captured run executable. It publishes its original
snapshot, then atomically requests one follow-up using the latest revisions.
Source configuration, lifecycle, membership, knowledge-base configuration, and
model configuration changes still invalidate the run. Replaying publication
does not create another follow-up.
The same follow-up check applies when the captured run is a `NO_OP` because its
snapshot already matches the retained publication.

The planner submits one ordered page plan. Each writer submits one page and its
complete Claim set. Go code checks the plan, paths, evidence locations, hashes,
links, frontmatter, and manifest before publication. A failed or interrupted run
does not replace the published wiki.

## The compatibility API and Discord share one Agent executor

Operators create any number of Agents. Each stable Agent points to one immutable
current version containing identity, answer-model selection, ordered
knowledge-base memberships, and configurable guardrails. An activation check
requires the whole corpus to be active and published and the model route to be
ready. Knowledge bases retain only the documentation planner and writer model
assignments.

Bearer-authenticated `GET /v1/models` exposes the active, ready Agents in a chat
token's explicit scope. `POST /v1/chat/completions` accepts a bounded text
message window and selects an Agent through a virtual model key such as
`agent:docs-support`. The API buffers streaming responses until execution and
citation verification finish. It stores an immutable Agent-run receipt, not an
HTTP conversation or transcript; the client owns saved sessions and resends the
useful history. Immediately before delivering either JSON or buffered SSE, the
API authenticates the token again and checks its Agent scope. Expiry or
revocation during execution prevents delivery.

Every nonempty answer span must reference evidence gathered during that run;
the model cannot exempt spans from this requirement. Unknown or absent citation
IDs cause the span to be dropped. Citation coverage checks provenance, not
whether the evidence logically supports the text. See the separate
[answer-quality review](../operations/answer-quality.md).

Wiki search prefers exact full-text matches and also considers individual query
terms after removing common English question words. Single-pass execution reads
around the strongest matching line instead of always reading a page's opening.
This remains lexical retrieval; synonyms and non-English question wording need
evaluation against the deployed corpus.

Answer calls and durable model jobs share database-backed admission per provider
endpoint across processes. The endpoint uses the lowest current model-profile
concurrency limit. Answers wait at most five seconds for admission; saturation
returns HTTP 429. An answer lease expires after the call timeout plus five
seconds and is released after each attempt. Retries honor bounded provider
retry headers, release admission while waiting, and stop on cancellation.

Discord input selects an Agent through an enabled channel binding. The executor
captures the exact Agent version, model-profile version, endpoint configuration,
knowledge-base versions, and wiki scope. The Discord handler checks binding,
Agent, corpus, caller, and reply permissions again after model work and before
delivery. Discord alone keeps bounded, version-isolated context so editing an
Agent starts fresh context. Restricted evidence does not gain public links
through presentation.

The operator wiki reader renders Markdown and GFM with raw HTML disabled and
restricted link handling. Knowledge base, publication selection, and page slug
live in the URL, including internal page navigation and browser history.
Evidence excerpts resolve through the selected publication's claim and captured
source revision. Reads require an operator session, reject unsafe file paths,
and return at most 400 lines from a file no larger than 256,000 bytes. Purged
snapshots report that the evidence is unavailable.

## Security boundaries fail closed

The first operator consumes a database-backed bootstrap token. Passwords use
scrypt. Browser sessions use an HTTP-only cookie, and state-changing requests
require the matching CSRF token. `PUBLIC_ORIGIN=https://...` enables the secure
cookie flag. Non-loopback HTTP origins are rejected at startup; HTTP remains
available only for localhost development.

The API never returns a stored credential. `APP_MASTER_KEY` encrypts each
versioned provider, repository, or Discord secret with AES-256-GCM. Previous
keys are read-only inputs during key rotation. Logs, API errors, job payloads,
URLs, and browser storage must not contain plaintext credentials.

Outbound repository, website, and provider requests accept only normalized
addresses and paths. HTTPS is the default. The safe network client resolves and
pins an allowed address, rejects disallowed private or special-purpose
addresses unless the stored policy permits them, verifies TLS, rejects unsafe
redirects, limits response size, and returns sanitized failures. Repository
credentials use an ephemeral askpass helper and never enter `.git/config` or a
remote URL.

The application-data root and both capsule sockets reject symlinks and unexpected
ownership or modes. The worker refuses to start its capsule pool unless both
configured sockets pass topology checks.

## Runtime identity is ref0

ref0 is the only current product, module, executable, image, and configuration
identity. Cookies, cryptographic domains, advisory-lock keys, actor identifiers,
and artifact formats all use the `ref0` namespace. Because the application is
unreleased, the cutover intentionally provides no dual-name reader or stored
format compatibility path. `.openwikiignore` remains the documented source
ignore-file contract; it is not a runtime product identifier.

The unreleased application uses one embedded Goose baseline. `ref0 migrate up`
installs it in an empty database or advances a database already managed by that
baseline. It does not adopt an Alembic-managed development database, and no
runtime code uses Alembic or a Python interpreter.

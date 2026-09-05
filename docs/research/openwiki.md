# OpenWiki architecture research

Researched on 2026-08-28 against `langchain-ai/openwiki` commit
[`97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190`](https://github.com/langchain-ai/openwiki/tree/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190),
package version 0.4.3. This note uses only first-party source code, repository
documentation, and the upstream Open Knowledge Format specification.

## Short answer

OpenWiki is not an OpenCode-powered documentation bot. Its normal path is a
Node.js CLI that constructs LangChain chat models and DeepAgents workers itself.
OpenCode, Codex, Claude Code, and Cursor are optional host integrations that can
drive the same durable page-job lifecycle over MCP. The native path resolves a
provider and model, creates a planner, creates one fresh worker per wiki page,
validates repository-grounded claims, and writes a linked Markdown wiki to
`openwiki/`. [Source: native run dispatch](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/agent/index.ts#L154-L230),
[source: optional host integrations](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/README.md#L80-L113).

OpenWiki also is not an embedding-based RAG index. It produces an Open Knowledge
Format bundle made of Markdown, YAML frontmatter, links, provenance, and
verification metadata. Its browser graph is rebuilt by scanning Markdown files
and resolving Markdown links. The package manifest has no embedding or vector
store dependency. This is an inference from the current source tree and package
manifest, not a claim that vector retrieval could never be added.
[Source: package dependencies](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/package.json#L61-L99),
[source: graph construction](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/visualize/graph.ts#L240-L337),
[source: OKF v0.2 purpose and non-goals](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md#L197-L233).

## Architecture

### Native repository generation

For repository `init` and `update`, `runOpenWikiAgent` loads local configuration,
resolves provider credentials and the model, creates the LangChain model, then
calls `runNativeRepositoryGeneration`. Other operations, including personal wiki
updates and chat, use a general DeepAgent graph with connector tools, filesystem
backend, middleware, and a LangGraph checkpointer.
[Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/agent/index.ts#L154-L274).

The repository path follows this lifecycle:

1. `beginRepositoryRun` creates or resumes `openwiki/.run.json`, checks Claims,
   fingerprints model-visible source, and can prove a clean update is a no-op.
   [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/generation/repository-run.ts#L319-L529).
2. A read-only planner explores the repository and submits an ordered page plan.
   Its filesystem tools are `read_file`, `ls`, `glob`, and `grep`. It cannot
   delegate to subagents. [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/agent/repository-runner.ts#L277-L357).
3. OpenWiki persists the accepted plan and changes the run phase from `planning`
   to `generating`. Re-submitting the same semantic plan is idempotent; trying to
   replace it is rejected. [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/generation/repository-run.ts#L872-L914).
4. Each pending page gets a fresh, non-delegating DeepAgent worker. The worker can
   read repository files, write only its assigned wiki page, and call
   `submit_page` with the complete intended Claim set. A snapshot lets OpenWiki
   restore and mark a failed page as skipped without losing earlier progress.
   [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/agent/repository-runner.ts#L368-L505).
5. `submitRepositoryPage` checks ordering, page existence, frontmatter, Claims
   reconciliation, Claims persistence, and the per-page manifest before marking
   the job complete. [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/generation/repository-run.ts#L1139-L1240).
6. `finishRepositoryRun` applies deletions, finalizes the wiki, restores skipped
   pages, proves Claims durability, updates the page manifest and run metadata,
   and removes `.run.json` last. Source drift or skipped pages leave interrupted
   metadata so a later update can reconcile them.
   [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/generation/repository-run.ts#L1448-L1552).

This is the core reusable idea: make pages independently durable work units. A
run does not hold one enormous agent conversation across the whole repository.

### Agent research and writing boundaries

The planner prompt asks the agent to map entrypoints, public boundaries, state,
failure behavior, configuration, operations, integrations, and representative
tests before proposing the smallest complete information architecture. Seed paths
are starting points rather than research limits.
[Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/agent/repository-prompts.ts#L19-L82).

Each page worker receives its exact target, purpose, related pages, output
language, seed paths, relevant instructions, and existing Claims. It must write
one page and submit evidence as `repo://` URIs, optionally with line ranges.
[Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/agent/repository-prompts.ts#L85-L172).

The repository backend confines generation writes to `openwiki/`, hides Claims
sidecars from the model, applies `.openwikiignore` to reads and discovery, and
limits shell execution when ignore rules are active. These are explicit
prompt-injection and path-containment boundaries.
[Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/agent/docs-only-backend.ts#L147-L195),
[source: write and shell restrictions](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/agent/docs-only-backend.ts#L481-L532).

### Claims and finalization

Repository pages carry a separate JSON Claims sidecar under `openwiki/.claims/`.
Each Claim contains an ID, statement, one or more evidence resources, and the
evidence version observed during verification. The sidecar also records the hash
of the Markdown page and an optional verification event.
[Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/claims/brains/code/store.ts#L28-L117).

The page manifest records each page's exact Markdown hash, repository source
fingerprint, optional Git HEAD, completing actor, and run ID. Current coverage is
accepted only when the manifest, Markdown hash, and verified Claims sidecar still
agree. [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/generation/page-manifest.ts#L11-L87),
[source: current-coverage check](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/generation/page-manifest.ts#L287-L330).

Finalization is deterministic and model-free. It validates Mermaid, synchronizes
directory indexes, validates internal links, projects Claim sources into
frontmatter, and reconciles generated provenance.
[Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/agent/wiki-finalizer.ts#L242-L285).

## Ingestion and indexing

### Repository source flow

Code mode operates on an already available local Git checkout. OpenWiki hashes
Git HEAD, tracked files, untracked files, porcelain status, and file contents for
all model-visible source paths. It excludes generated OpenWiki state and paths
matched by `.openwikiignore`. This fingerprint is a run correctness gate, not a
search index. [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/agent/utils.ts#L289-L403).

There is no chunking and embedding pipeline in the current repository path. The
planner and page agents retrieve source by using repository filesystem tools,
then the durable lifecycle records the evidence they cite. The generated wiki is
the maintained knowledge artifact. This is an inference from the worker tools,
package dependencies, and absence of embedding or vector-store code in the
reviewed commit.

### Personal source flow

Personal mode has a connector registry for Custom MCP, local Git, Gmail,
Hacker News, LangSmith, Notion, Slack, Web Search, and X. A connector declares
its backend, supported mode, required environment keys, and whether discovery is
agentic. [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/connectors/types.ts#L1-L46),
[source: registry](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/connectors/registry.ts#L12-L55).

`runOpenWikiIngestion` resolves configured source instances and processes them
one at a time. Deterministic connectors first write raw data, then a source-
specific agent synthesizes it into the local wiki. Agentic-discovery connectors
skip the deterministic pull and let the agent inspect the source through tools.
One failed source does not stop the remaining sources.
[Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/ingestion/ingestion.ts#L65-L105),
[source: per-source run](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/ingestion/ingestion.ts#L124-L219).

Raw connector output and run history live under
`~/.openwiki/connectors/<connector>/`. Raw files and state use private file
permissions. Connector state keeps at most 20 run summaries.
[Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/config/openwiki-home.ts#L39-L95),
[source: connector persistence](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/connectors/io.ts#L33-L100).

The existing `web-search` connector is search-query ingestion through Tavily. It
supports configured queries, domain filters, search depth, time range, and an
option to include raw content. It is not a general website crawler with URL
ownership, crawl policy, sitemap traversal, incremental page snapshots, or page-
level freshness state. The second sentence is a design-gap inference from the
current connector implementation.
[Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/connectors/sources/web-search.ts#L20-L50),
[source: pull implementation](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/connectors/sources/web-search.ts#L59-L176).

## Data and storage choices

OpenWiki is file-first:

- Repository wiki pages, Claims, page manifest, last-update metadata, and active
  run checkpoint live under the repository's `openwiki/` directory. The run
  checkpoint stores phase, ordered jobs, source fingerprint, language, actor,
  planning context, previous metadata, and prepared finalization state.
  [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/generation/run-state.ts#L11-L218).
- Personal wiki pages, credentials, connector data, conversation history, and
  skills live under `~/.openwiki` by default. `OPENWIKI_CONFIG_DIR` changes that
  root. [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/config/openwiki-home.ts#L6-L50).
- Chat uses a persistent SQLite LangGraph checkpointer at
  `~/.openwiki/openwiki.sqlite`. Non-chat init and update graphs use an in-memory
  checkpointer. [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/agent/index.ts#L811-L915).
- Credentials and provider settings are persisted in `~/.openwiki/.env` using a
  private temporary file and atomic rename. Shell environment values override
  saved values. [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/config/env.ts#L265-L313),
  [source: atomic private write](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/config/env.ts#L316-L372).

## Configuration and model handling

OpenWiki has a declarative provider registry with 13 providers. It includes
OpenAI, an OpenAI ChatGPT OAuth path, Anthropic, Copilot, Gemini, Gemini
Enterprise, OpenRouter, a generic OpenAI-compatible endpoint, Bedrock, Fireworks,
Baseten, Nebius, and NVIDIA. The OpenAI-compatible provider requires a base URL
and API key but has no preset model list.
[Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/config/constants.ts#L103-L170),
[source: provider registry](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/config/constants.ts#L237-L410).

The model factory maps those providers onto LangChain model classes. Most
OpenAI-shaped providers use `ChatOpenAI`; custom base URLs are passed through its
configuration. OpenAI-compatible endpoints can opt into Responses API and
streaming transports. Output-token limit, retry count, and some provider timeouts
are configurable. [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/agent/index.ts#L1077-L1248).

The setup UI uses static model presets from the provider registry plus a custom
model-ID input. Providers with no presets start in custom input mode.
[Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/setup/credentials/steps.ts#L806-L858),
[source: Ink UI](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/setup/credentials/components.tsx#L304-L367).

Automatic model discovery is narrow. The runtime checks `/v1/models` only for
the official `openai` provider with the standard endpoint. Other providers return
`unknown`, and custom OpenAI endpoints are explicitly not validated. It validates
one selected model rather than returning a model catalogue for a configuration
dashboard. [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/model-availability.ts#L1-L84).

Reasoning controls are also allowlisted by provider and exact model ID. The
current source supports OpenAI GPT-5.6 model efforts and one NVIDIA Nemotron
mapping. Unknown provider/model combinations reject a configured effort.
[Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/config/reasoning.ts#L1-L101).

No setting in the reviewed source declares a model context-window size. The
implemented knobs cover output tokens, reasoning effort, retry attempts,
transport choices, and selected provider-specific timeouts. This is an inference
from the configuration registry and model factory. A future dashboard should
treat advertised input context as discovered model metadata or an operator
override, not as an OpenWiki feature that already exists.

## Frontend and backend boundary

OpenWiki has two user interfaces, neither of which is an application dashboard:

- The primary interface is an Ink React terminal UI. The same Node process owns
  setup, configuration persistence, ingestion, agent runs, and rendering. The CLI
  entrypoint dispatches auth, cron, ingestion, visualization, print mode, or the
  interactive TUI. [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/cli/cli.tsx#L1-L110).
- The visualizer is a read-only local browser app. Its HTTP server binds to
  `127.0.0.1` and exposes a fixed route set for static assets, `/api/graph`, and an
  SSE reload stream. It has no mutation or configuration routes.
  [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/visualize/server.ts#L14-L117),
  [source: fixed HTTP routes](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/visualize/server.ts#L156-L213).

The published package declares a CLI binary and does not declare a stable
`exports` API. Treating internal `dist` modules as a supported application
backend would be risky. This is an inference from the package manifest.
[Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/package.json#L1-L27).

## Repository support

Code mode documents the current local repository and writes `openwiki/` into the
checkout. Public versus private is not part of generation because the agent reads
local files. [Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/README.md#L134-L153).

The personal `git-repo` connector also accepts local checkout paths. It runs
local Git commands to capture branch, HEAD, recent commits, status, and changed
files. It does not clone, fetch, manage remotes, or store repository credentials.
[Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/connectors/sources/git-repo.ts#L20-L49),
[source: local manifest](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/src/connectors/sources/git-repo.ts#L58-L172).

For a dashboard whose first-class source is a public or private repository, the
missing layer is repository acquisition and lifecycle management: provider URL
validation, credentials, clone, fetch, branch or commit selection, isolated
checkout paths, webhook or polling triggers, and deletion or credential rotation.
That is a design inference. It should sit before the OpenWiki-style generation
lifecycle rather than inside a page worker.

## License and reuse

OpenWiki 0.4.3 is MIT licensed. The license permits use, copying, modification,
distribution, sublicensing, and sale, provided the copyright and permission
notice remain in copies or substantial portions. It disclaims warranty.
[Source](https://github.com/langchain-ai/openwiki/blob/97c6ef0ce72912cb3ba70a238a94b2dbc6b3b190/LICENSE#L1-L20).

Open Knowledge Format v0.2 is specified in the GoogleCloudPlatform
`knowledge-catalog` repository. The format deliberately specifies Markdown and
frontmatter structure without prescribing storage, serving, query infrastructure,
or a runtime. That makes OKF a good interoperability target for a separate
backend and dashboard. [Source](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md#L197-L233),
[source: bundle and concept structure](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md#L253-L318).

The Knowledge Catalog repository says its contents use Apache 2.0. If this
project copies text or implementation from that repository rather than merely
producing compatible OKF output, retain the applicable license and notices.
[Source](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/README.md#license).

OpenWiki's MIT license covers OpenWiki's code, not every dependency, hosted
provider API, model, generated third-party content, or product name. A release
that vendors code should run a dependency and notice audit. This last point is a
general reuse inference, not a special restriction found in OpenWiki's license.

## Design implications for this project

These are recommendations inferred from the facts above:

1. Reuse the lifecycle, not the CLI process. Define a backend-owned service API
   around `begin`, `plan`, `next page`, `submit page`, and `finish`. Keep run state
   durable and make every page independently resumable.
2. Keep OpenCode out of the core. Construct LangChain models directly from a
   provider profile, like OpenWiki's native path. Host coding-agent integration
   can remain an optional adapter later.
3. Split source acquisition from documentation generation. A repository service
   should own remote URL, auth reference, clone/fetch, revision, checkout, and
   source fingerprint. Generation should receive an immutable local snapshot.
4. Store operator configuration in an application database, and keep secrets in
   a secret store or encrypted credentials table. Generate process environment
   only at the worker boundary. OpenWiki's single-user `.env` file is sensible for
   a CLI but not for a multi-source dashboard service.
5. Adopt OKF Markdown as the canonical portable artifact. Store operational rows
   for runs, jobs, sources, provider profiles, and discovered models separately.
6. Treat model discovery as a capability probe. Query `/models` when supported,
   cache the raw result, allow manual model IDs, and keep operator overrides for
   context window, output tokens, reasoning transport and effort, streaming, and
   other provider-specific parameters. Do not infer unsupported capabilities from
   a model name alone.
7. Add retrieval only if the product needs question answering across generated
   docs. OpenWiki's linked Markdown and Claims solve documentation maintenance and
   freshness. They do not replace a chunk and embedding index for semantic search.
8. Extend the current visualizer into the dashboard only after the backend API
   exists. Its graph and reader are useful, but configuration, secret management,
   runs, logs, and source health need authenticated mutation endpoints and a job
   event stream.

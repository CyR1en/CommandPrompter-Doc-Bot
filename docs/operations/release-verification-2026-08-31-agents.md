# Agents hard-cutover verification for 2026-08-31

This record covers the unreleased Unit 6 cutover from the knowledge-base answer
engine and dashboard Chat to first-class Agents. It is a local implementation,
production-build browser, and isolated visual verification record, not a
vulnerability or live third-party sign-off.

## Verified result

- Agents are the only answer-time aggregate. Their immutable versions own
  identity, one answer model, ordered knowledge-base membership, and guardrails.
- Knowledge bases expose only documentation planner and writer model roles.
- The dashboard Chat page, generic HTTP conversations, and the old
  knowledge-base chat routes are absent. The two retired API routes return 404.
- `GET /v1/models` and `POST /v1/chat/completions` remain the scoped,
  OpenAI-shaped compatibility plane. HTTP callers own saved sessions and resend
  useful message history; ref0 stores no HTTP transcript.
- Discord bindings select Agents and retain only their separate bounded,
  Agent-version-isolated context.
- Completed Agent-run receipts and captured scope replace query-run metadata in
  retention, overview, metrics, and backup/restore proof.

## Schema and generated contracts

The embedded application baseline installs exactly:

| Catalog object | Application baseline | Live Goose-managed database |
| --- | ---: | ---: |
| Tables | 51 | 52 |
| Columns | 582 | 586 |
| Indexes | 133 | 134 |

The live totals add only Goose's `goose_db_version` table, four columns, and
primary-key index. `TestGooseBaselineCatalogInvariants` passed against a fresh
PostgreSQL 18.6 container. Its committed exact catalog records all 51
application tables, 582 columns, 133 indexes, 259 CHECK constraints, and 89
foreign keys. The gate also proved that `conversations`,
`conversation_messages`, and `query_runs` are absent and that both persisted
documentation-role constraints admit planner/writer but not `ANSWER`.

SQLC generation was byte-stable across two runs. The generated SQLC-tree digest
was `8e49dd63d49223d582afde0d146d14f0d2cac834e7d025e406930e7c0bdc74e2`.
OpenAPI and the TypeScript client were also stable across two runs, with digest
`db9ef2adc1679a23494ace6d0b2d9439e2daada4208dd0a82f9b36a4d8280b0e`.
The exact application-catalog manifest digest was
`9442a1f9ab8aa6038cd753b9f4e8c4909d28b7f5adb59d141bbaf40e9222f1e7`.
These values are reproducible from the repository root with:

```sh
find internal/database/sqlc -type f -name '*.go' ! -name 'generate.go' -print0 \
  | sort -z | xargs -0 shasum -a 256 | shasum -a 256
shasum -a 256 frontend/openapi.json frontend/src/api/schema.d.ts \
  | shasum -a 256
shasum -a 256 internal/migrate/testdata/application_catalog.json
```

The control-plane document contains 66 paths and 82 operations. It contains no
legacy conversation/chat path or `ChatRequest`, `ChatResponse`, or
`ConversationResponse` schema.

## Gates run

| Gate | Result |
| --- | --- |
| Formatting and generation | `gofmt` clean; `go generate ./...` and frontend API generation stable across two runs. |
| Build and static analysis | `go build ./...`, `go vet ./...`, and `git diff --check` passed. |
| Full Go/PostgreSQL suite | `TEST_DATABASE_URL=... go test -race -count=1 -p=1 ./...` passed against PostgreSQL 18.6. |
| Legacy absence | `go test ./verification` passed `TestLegacyAnswerAndDashboardChatSurfacesAreAbsent`; focused production-source and DDL searches returned no retired surface. |
| API and runtime focus | Provider, documentation, Agent, API, operations, retention, worker, workerruntime, and migration packages passed their focused suites. |
| Frontend | Lint and type-check passed; 22 test files and 100 tests passed; the production build transformed 192 modules. |
| Capsule regression | Format, type-check, build, and all 9 capsule tests passed. No capsule contract changed in this cutover. |
| Schema proof | Fresh PostgreSQL application catalog matched 51 tables, 582 columns, 133 indexes, 259 CHECK constraints, and 89 foreign keys. |
| Coordinated restore | `REF0_RUN_DOCKER_TESTS=1 go test -count=1 ./verification -run '^TestDatabaseAndArtifactBackupRestore$' -v` passed in 9.50 seconds. |
| Compose | Both Compose manifests passed `docker compose ... config --quiet` with their documented required values. |
| Production browser | The isolated production Compose build passed both Playwright journeys in 36.5 seconds: restart-persistent authentication/data and provider setup through Agent draft creation with secret containment. Cleanup removed its containers, volumes, and network without touching the existing local stack. |
| Visual and accessibility | Agent list, create, detail, Discord binding, token ledger, and token-scope preview were inspected at 1440×1000 and 390×844. The final audit reported no browser errors, horizontal overflow, or axe color-contrast violations. |

The restore proof preserved and decrypted the service-created credential with
its exact key ID, nonce, ciphertext, and version. It also preserved publication
pointers, complete Agent configuration/current version/membership, a complete
Agent-run receipt, both captured scope digests, and both wikis' exact
`.page-manifest.json` and `index.md` bytes.

## Absence and retained-term gates

The source gate uses exact ownership predicates instead of banning legitimate
protocol vocabulary:

```sh
test ! -e internal/answers
test ! -e internal/api/chat.go
test ! -e internal/api/chat_test.go
test ! -e frontend/src/features/chat
! rg 'CREATE TABLE public\.(conversation_messages|conversations|query_runs)' db/migrations/00001_baseline.sql
! rg -g '!**/*_test.go' '(/api/v1/conversations|knowledge-bases/.*/chat|path: "/chat"|CONVERSATION_IDLE_MINUTES|CONVERSATION_RETENTION_DAYS|ref0_query_results_total|query_failures)' internal frontend/src frontend/openapi.json .env.example docker-compose.yml docker-compose.portainer.yml README.md CONTEXT.md docs/architecture/openwiki-platform.md docs/operations/backup-and-restore.md docs/operations/metrics-and-retention.md
! rg "'ANSWER'::character varying" db/migrations/00001_baseline.sql
```

The gate intentionally retains `/v1/chat/completions`, chat access tokens,
provider Chat Completions transport, `answer_mode` on Agent versions, `ANSWERED`
run outcomes, and `discord_conversations`/`discord_conversation_messages`.

## Operational policy

`AGENT_RUN_RETENTION_DAYS` defaults to 90. A fenced retention pass removes
eligible Agent runs and their cascading captured scope before evaluating old
wiki versions. A wiki remains protected while any retained Agent-run scope
references it. `DISCORD_CONTEXT_RETENTION_DAYS` remains independent at seven
days. Prometheus exports current-retained-window Agent gauges under
`ref0_agent_retained_*`; these can decrease when retention removes receipts and
are not lifetime counters. The overview links recent failed Agent runs to the
corresponding Agent run history.

## Not exercised by this record

- live Open WebUI, provider, or Discord credentials;
- SBOM/license regeneration, vulnerability scans, or registry publication;
- GitHub-hosted Actions or multi-architecture release publication.

The dependency graph did not change in Unit 6. Run the current advisory and
image audit procedure in [dependency-audit.md](dependency-audit.md) before a
production release.

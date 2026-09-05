# ref0

ref0 turns authorized Git repositories and websites into a linked,
evidence-backed Markdown wiki. Operators manage sources, OpenAI-compatible model
endpoints, documentation runs, Agents, chat access tokens, and Discord bindings from one authenticated
dashboard. First-class Agents own answer identity, one model, an ordered
knowledge-base corpus, guardrails, and delivery configuration.

The runtime is four processes backed by PostgreSQL and one application-data volume:

- `api` serves `/api/v1`, server-sent events, wiki exports, and the React dashboard.
- `worker` syncs sources and runs resumable documentation jobs.
- `discord` supervises stored bot connections and calls the same verified Agent executor as the OpenAI-compatible API.
- `migrate` applies the database schema before the other processes start.

No model, repository, or Discord secret is required in an environment variable.
Operators enter those write-only credentials in the dashboard. The application
encrypts them with `APP_MASTER_KEY`.

## Start locally

Requirements: Docker Engine with Compose v2.

1. Copy `.env.example` to `.env`.
2. Replace `POSTGRES_PASSWORD`, `APP_MASTER_KEY`, `APP_BOOTSTRAP_TOKEN`, and
   `METRICS_BEARER_TOKEN` with independent random values.
3. Start the stack:

   ```sh
   docker compose up --build
   ```

4. Open `http://localhost:8000/bootstrap` and create the first operator.
5. Remove `APP_BOOTSTRAP_TOKEN` from `.env`, then restart the API service.

The bootstrap token is single-use in PostgreSQL. The browser session is
HTTP-only. State-changing API requests require the CSRF token returned by the
session endpoint.

## Configure a knowledge base

Use the setup flow in the dashboard:

1. Create a restricted or public knowledge base.
2. Add and validate a public or credential-backed HTTPS repository, or a bounded HTTPS website source.
3. Add an OpenAI-compatible endpoint, discover or enter a model, and record an explicit context window.
4. Assign model versions to the documentation planner and writer roles.
5. Generate the first wiki and inspect its Claims and evidence.
6. Create an Agent, choose its answer model, add one or more knowledge bases,
   set identity and guardrails, then activate it.
7. Issue a scoped chat access token and select the Agent through `GET /v1/models`
   and `POST /v1/chat/completions` from Open WebUI or another compatible client.
   ref0 does not store HTTP chat sessions or transcripts.
8. Optionally configure Discord. Add a bot token, open the generated
   installation URL, refresh its servers, and validate a channel binding. Send
   a test message before you enable the binding.

Long operations return durable jobs. Runs and events survive process restarts.
The queue reclaims an abandoned worker lease without replacing the current
published wiki.

## Operations

- Liveness: `GET /health/live`
- Readiness: `GET /health/ready`
- Prometheus metrics: authenticated `GET /metrics` using `METRICS_BEARER_TOKEN`
- API documentation: `GET /docs`
- OpenAI-compatible Agent discovery and execution: bearer-authenticated
  `GET /v1/models` and `POST /v1/chat/completions`
- Logs are structured and exclude prompt bodies and secrets by default.
- Keep PostgreSQL, `/app/data`, and the matching master key together when backing up the deployment.

Before changing a production deployment, read
[metrics and retention](docs/operations/metrics-and-retention.md),
[backup and restore](docs/operations/backup-and-restore.md), and
[upgrade](docs/operations/upgrade.md). Portainer users can deploy
`docker-compose.portainer.yml` after setting the required environment values in
the stack.

## Development

The tested toolchains are Go 1.27.0, Node 24 for the frontend, Node 22.19.0 for
the pinned Pi capsule, and PostgreSQL 18.

```sh
go build ./...
go vet ./...
go test -race ./...
npm --prefix frontend ci
npm --prefix frontend run lint
npm --prefix frontend run typecheck
npm --prefix frontend test
npm --prefix frontend run build
npm --prefix capsule ci
npm --prefix capsule run format:check
npm --prefix capsule run typecheck
npm --prefix capsule run build
npm --prefix capsule test
```

The executable exposes exactly four top-level commands. It has no compatibility
aliases for the retired application:

```sh
ref0 api
ref0 worker
ref0 discord
ref0 migrate up
```

`DATABASE_URL` may be supplied directly. Otherwise the commands construct it
from `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DB`, `POSTGRES_USER`, and
`POSTGRES_PASSWORD`. The single embedded Goose baseline is the complete schema
for this unreleased application. There is no Alembic or Python runtime.

CI runs formatting, build, vet, and race-enabled Go tests against PostgreSQL.
It then verifies the frontend, capsule, Compose manifests, and both production
images. On a Docker host, run the release backup and restore proof with:

```sh
REF0_RUN_DOCKER_TESTS=1 go test ./verification -run TestDatabaseAndArtifactBackupRestore
```

See [the current platform architecture](docs/architecture/openwiki-platform.md)
and [Pi capsule isolation](docs/architecture/pi-capsule.md). The dated files in
`docs/spec/` record earlier designs and do not describe the current runtime.
`CONTEXT.md` defines the project vocabulary and invariants.
The local proof for the Agent hard cutover is recorded in
[Agents hard-cutover verification](docs/operations/release-verification-2026-08-31-agents.md).

The project license is in [LICENSE](LICENSE). Both production images also ship
[third-party license notices](THIRD_PARTY_NOTICES.md) at
`/usr/share/licenses/ref0/`.

## Complete the ref0 rename

The source and runtime now use only the ref0 identity. Repository hosting,
container packages, and a local checkout name are external resources, so an
operator must rename those resources separately.

1. Create a coordinated backup before changing a deployment. Follow
   [backup and restore](docs/operations/backup-and-restore.md).
2. In the GitHub repository, open **Settings > General** and change the
   repository name to `ref0`.
3. Update the local remote URL and verify both fetch and push destinations:

   ```sh
   git remote set-url origin git@github.com:cyr1en/ref0.git
   git remote -v
   ```

   Use `https://github.com/cyr1en/ref0.git` instead when the checkout uses an
   HTTPS remote.
4. Publish both images under `ghcr.io/cyr1en/ref0`. A GitHub repository rename
   does not copy container tags to a new package path. Connect the new package
   to the renamed repository, grant its Actions workflow access, and publish the
   application release tag and the matching
   `pi-0.84.4-r9-<release-tag>` capsule tag. To copy an
   existing immutable image without rebuilding it, use its recorded digest:

   ```sh
   docker buildx imagetools create \
     --tag ghcr.io/cyr1en/ref0:<release-tag> \
     ghcr.io/cyr1en/<old-package>@sha256:<application-digest>
   docker buildx imagetools create \
     --tag ghcr.io/cyr1en/ref0:pi-0.84.4-r9-<release-tag> \
     ghcr.io/cyr1en/<old-package>@sha256:<capsule-digest>
   ```

   Set `REF0_IMAGE` and `REF0_CAPSULE_IMAGE` to the new immutable references.
   Do not keep retired image-variable names as aliases.
5. To rename the local folder, stop the stack first and move it from its parent
   directory:

   ```sh
   mv CommandPrompter-Doc-Bot ref0
   ```

   Compose derives its project name from the folder unless
   `COMPOSE_PROJECT_NAME` is set. Changing the project name makes Compose create
   new empty volumes. For an existing deployment, either keep the prior
   `COMPOSE_PROJECT_NAME` in `.env` or restore the coordinated backup into the
   new `ref0` volumes. New deployments use `COMPOSE_PROJECT_NAME=ref0` from
   `.env.example`.

The rename does not change the logical PostgreSQL schema or application-data
formats used by ref0. An existing Goose-managed ref0 deployment can keep its
database name and user by retaining the matching `POSTGRES_DB` and
`POSTGRES_USER` values. It must also keep the exact `APP_MASTER_KEY` and
`APP_PREVIOUS_MASTER_KEYS` needed by stored credentials.

This unreleased cutover does not adopt an Alembic-managed development database
or read Alembic revision metadata. `ref0 migrate up` expects an empty database
or a database already managed by the embedded Goose baseline. Recreate an old
development database from source data instead of treating it as a supported
upgrade input.

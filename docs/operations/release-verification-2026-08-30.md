# Release verification record: 2026-08-30

This record covers the uncommitted local working tree verified on 2026-08-30
in America/Denver. It records the Python-to-Go cutover and the complete `ref0`
runtime identity change. The dirty working tree was preserved. No reset, clean,
or compatibility layer was used.

All confirmed release blockers and P1/high findings were closed in this local
tree. This is not evidence of a GitHub-hosted Actions run, a GHCR publication,
or an external repository rename.

## Release changes

- Conversation serialization now uses a keyed process-local lock. The callback
  does not retain a primary-pool connection. Replica mode is not supported, so
  a distributed lock pool is not exposed as a partially supported option.
- One fail-closed terminal dispatcher handles failure, cancellation, and retry
  exhaustion for every job type. Documentation runs and pages, source syncs,
  provider and Discord operations, purges, and retention work all end in a
  terminal or retryable state.
- Artifact deletion uses committed intents followed by idempotent filesystem
  deletion and a fenced final transaction. Wiki versions, failed drafts,
  source snapshots, and knowledge bases recover after interruption. A committed
  knowledge-base deletion cannot be restored.
- Model budgets measure each serialized request, including system prompts, tool
  schemas, messages, options, JSON framing, output reserve, and later tool
  results. Oversized results are bounded before append and the truncation
  decision is persisted.
- The inclusive model-timeout range is 1 through 60 seconds in configuration,
  answer execution, safenet, the Go capsule client, and the TypeScript capsule
  wire contract. Accepted values are propagated without clamping.
- Documentation no-op detection includes planner and writer profile versions,
  model versions, endpoints and endpoint configuration, credential versions,
  and reasoning settings. A model configuration change forces work.
- Live cookies, CSRF and cryptographic domains, advisory-lock keys, artifact
  formats, actor IDs, fixtures, and generated files use only `ref0` names.
- Event streams start at the current tail, resume from a valid cursor, emit a
  reset after a pruned or ahead cursor, and stop or reauthenticate when the
  session expires. Frontend event names match backend producers, including
  provider, Discord, and source events.
- Event retention deletes only a contiguous expired prefix and advances an
  indexed prune watermark in the same transaction. Reads check that watermark
  atomically, preserving cursor and pruned-gap behavior.
- Metrics require a dedicated bearer token before collection. Aggregation is
  cached and concurrent misses are coalesced, so unauthenticated storms do not
  reach PostgreSQL. Public HTTPS deployments issue secure cookies, and unsafe
  public HTTP origins fail validation.
- PostgreSQL search uses the language-neutral `simple` text-search
  configuration for both indexed documents and queries.
- `sqlc` is pinned at v1.31.1. CI regenerates queries and rejects modified,
  deleted, or untracked generated output.
- Git startup fails closed when `http.curloptResolve` is unavailable. The
  accidental root `ref0` binary is removed and ignored. Docker build contexts
  exclude local and secret-bearing residue while retaining required capsule and
  verification inputs. Compose services have practical CPU, memory, PID,
  capability, and privilege limits. GitHub Actions references use commit SHAs.

## Toolchains and artifacts

| Item | Verified value |
| --- | --- |
| Go | `go1.27.0 darwin/arm64` |
| Host Node and npm | Node `24.8.0`, npm `11.16.0` |
| Frontend production image | Node `24.20.0` |
| Capsule build and runtime image | Node `22.19.0` |
| Database | PostgreSQL `18.6` on arm64 |
| Capsule revision | `pi-0.84.4-r9` |
| Local application image | `sha256:117956ddacec00217a12beb230ab739d0e4803b0aa0083e3eded7fad09c9463a` |
| Local capsule image | `sha256:19771d67f57cc25c69c1c5e08f480be69c1da4d64a1ab609bc7859fbb5d6413f` |
| OpenAPI document | `43777fa2dd5ebe03ade9c428997647cf814ff1e213e5fada8ebd8048fe291bc9` |
| Generated TypeScript schema | `5482d669b9c79800b10a367dfc5a8324fd63f623567a3c1ad9cada9b9b64d619` |
| Embedded `LICENSE` | `58bce87efcde33e07fd05c6c1e9f84ed89fbfca99327a58f064a4cb9165c6896` |
| Embedded notices | `92f89c7bc24b42d240589c88910e9ab427e36ba8881956dc99041524ea44bf1b` |

The image IDs identify local Linux arm64 BuildKit outputs. They are not remote
registry digests or multi-architecture manifest identifiers.

After recording the identifiers, the task-created verification containers,
networks, volumes, and image tags were removed. The root `ref0` executable is
absent and ignored. The final scan found no application-controlled Python files
or unexpected empty directories outside pre-existing ignored user state.

## Verification evidence

| Area | Command or check | Result |
| --- | --- | --- |
| Module integrity | `go mod verify` | Passed: all modules verified. |
| Formatting | `gofmt` over `capsule`, `cmd`, `db`, `internal`, and `verification` | Passed with an empty file list. |
| Generation | `go generate ./...`, run twice | Passed. The pinned sqlc output was byte-identical on the second run. |
| Build | `go build ./...` | Passed. The ignored root `ref0` executable was not created. |
| Static analysis | `go vet ./...` | Passed. |
| Shell syntax | `sh -n` over the browser and capsule verification scripts | Passed. |
| Diff hygiene | `git diff --check` | Passed. |
| Database baseline | `go run ./cmd/ref0 migrate up` against a new PostgreSQL 18.6 database | Passed. |
| Full Go and PostgreSQL suite | `go test -race -count=1 -p=1 ./...` | Passed for all 34 packages. |
| Queue and security regressions | `go test -count=1 -p=1 ./internal/jobs ./internal/jobterminal ./internal/security ./internal/artifacts ./internal/capsule ./internal/api ./internal/sourcegit` | Passed. |
| Event and metrics stress | `go test -race -count=20 ./internal/api` for the metrics-storm and pruned-cursor reset tests | Passed. |
| Backup and restore | `REF0_RUN_DOCKER_TESTS=1 go test -count=1 ./verification -run '^TestDatabaseAndArtifactBackupRestore$' -v` | Passed in 4.672 seconds. |
| Frontend | `npm ci`, API generation, lint, typecheck, tests, and production build | Passed: 19 files, 75 tests, and 187 production modules. |
| OpenAPI | Generate twice and compare both committed outputs | Passed: 56 paths and 71 operations; `/metrics` is not public API. |
| Capsule package | Format check, typecheck, build, and tests | Passed: 9 tests. The production Docker build repeated these checks under the required Node 22.19.0 runtime and pruned development tools. |
| Compose | `docker compose ... config --quiet` for both Compose files | Passed with required environment values. |
| Production images | Build the application and capsule production targets | Passed for Linux arm64. |
| Browser acceptance | `bash verification/run-browser-acceptance.sh` | Passed: both Chromium tests in 35.4 seconds. |
| Documentation end to end | `CAPSULE_VERIFY_TIMEOUT_SECONDS=600 sh verify/pi-capsule/isolation-run.sh` | Passed, including crash recovery and both isolation-negative checks. |
| Actions | `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7` plus a 40-character SHA scan | Passed. Every external Action reference is pinned; the verification workflow call remains a local path. |
| Go vulnerabilities | `govulncheck@v1.7.0 ./...` | Passed with no code or imported-package vulnerabilities. Two advisories exist in required modules but are not called. |
| npm vulnerabilities | Production-only and full audits for the frontend and capsule | Passed with zero reported vulnerabilities. |
| Container vulnerabilities | Digest-pinned Trivy 0.66.0 with vulnerability and secret scanners, HIGH/CRITICAL severity, and `--ignore-unfixed` | Passed for both production images with zero findings in that scope. |
| Image contents | Inspect users, entrypoints, binaries, runtime tools, and embedded notices | Passed. The application runs as UID 1000/GID 2000; both Go binaries are static; the capsule excludes npm, npx, corepack, TypeScript, test JavaScript, and development dependencies. |

Focused PostgreSQL tests also passed for the `MaxConns=2` distinct- and
same-conversation barriers; maximum iterative answer and capsule budgets;
all documentation terminal stages; all 11 terminal-dispatch job types;
provider, Discord, and source terminal outcomes; wiki, draft, snapshot, and
knowledge-base intent restart recovery; contiguous event retention;
documentation model-identity no-op detection; non-English retrieval; and the
1-second timeout boundary and propagated deadline.

## End-to-end documentation evidence

The final isolated run reached `PUBLISHED` through `PREPARE_RUN`, `PLAN_RUN`,
`GENERATE_PAGE`, and `FINALIZE_RUN`. The strict local provider received four
requests. Planner usage was 30 tokens, writer usage was 54 tokens, and total
usage was 84 tokens across four model calls. The run persisted one claim and
one evidence record.

The verifier crashed a worker after the page transaction but before queue
acknowledgement. The queue reclaimed the job at lease generations `[1,2]` and
published one final wiki. A 700,000-byte stalled result exhausted two bounded
attempts, drained safely, and released the slot for the next request.

| Output | SHA-256 |
| --- | --- |
| Page claims | `d0725ca3efd6dc1302e1683b59adf7851305459fa33734b7257d8524435fb4ef` |
| Page content | `131718496f2e4bfaae3fedecc678ec093dfc3ea148fd7e30c15a9fefca04a28b` |
| Wiki manifest | `0bb2b44979f465170b309b35d6c5df4715fe2f4e8735d9667138afb31b05193b` |

## Non-passing attempts

- The first full race run exposed two stale credential keyed-digest fixtures.
  The fixtures were updated for the new `ref0` cryptographic domain, their
  focused security tests passed, and the complete race suite then passed.
- Concurrent Docker verification briefly wedged the local OrbStack daemon.
  Those interrupted attempts are not counted above. After confirming that only
  disposable test resources were active, OrbStack was restarted and every
  Docker-dependent check listed above was rerun to completion.
- The host capsule checks emitted an engine warning because host Node 24.8.0 is
  newer than the package's exact Node 22.19.0 requirement. The production
  capsule image repeated and passed the suite with Node 22.19.0.

## Residual risks and external work

- `govulncheck` found two advisories in required modules that current code does
  not import or call. Dependency updates should continue to remove dormant
  exposure.
- The Trivy release gate ignores findings with no available fix. Such findings
  were outside the recorded scan scope.
- Provider and Discord behavior used strict local substitutes. No live external
  credentials or third-party services were exercised.
- GitHub-hosted Actions, GHCR pushes, and multi-architecture publication were
  not run locally.
- Browser acceptance reported the host's
  `DOCKER_INSECURE_NO_IPTABLES_RAW` warning. The isolated tests passed, but a
  production Docker host should retain normal firewall enforcement.
- Dated design records may retain historical `cmdp` wording. Live runtime,
  fixture, generated, and deployment paths do not.
- The pre-existing ignored `.venv` remains as user state. It is excluded from
  source scans, build contexts, tests, and runtime images.

The external rename still requires an operator to:

1. Rename the GitHub repository to `ref0` and update the Git remote.
2. Publish both images under `ghcr.io/cyr1en/ref0`, connect the package to the
   renamed repository, and use immutable values for `REF0_IMAGE` and
   `REF0_CAPSULE_IMAGE`.
3. Stop the deployment before renaming the checkout directory from
   `CommandPrompter-Doc-Bot` to `ref0`.
4. Keep the prior Compose project name for existing volumes, or restore the
   coordinated backup into new `ref0` volumes. Preserve the exact master-key
   material required by stored credentials.

The full procedure is in [Complete the ref0 rename](../../README.md#complete-the-ref0-rename).

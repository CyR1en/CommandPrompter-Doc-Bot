# Audit release dependencies

This page records the dependency graph at the ref0 source boundary, one dated
source audit, and the commands needed to audit a release. The source audit is
not a release sign-off. Advisory databases change after a commit is built, so
attach the raw output, tool versions, commit, UTC time, and image digests to each
release record.

## Observed source audit

These commands ran at `2026-08-30T10:21:26Z` against the working tree. npm was
`11.16.0`. `govulncheck` was `v1.7.0` on Go `1.27.0`, using the Go vulnerability
database updated at `2026-08-28T14:47:45Z`.

| Check | Observed result |
| --- | --- |
| `go mod verify` | Passed. |
| `govulncheck ./...` | Passed with 0 reachable vulnerabilities after pinning `github.com/gorilla/websocket v1.5.3`. Verbose output reported `GO-2026-6303` and `GO-2026-5932` in uncalled `golang.org/x/crypto` packages. |
| `npm --prefix frontend audit --omit=dev --audit-level=high` | Reported 0 vulnerabilities. |
| `npm --prefix frontend audit --audit-level=high` | Reported 0 vulnerabilities. |
| `npm --prefix capsule audit --omit=dev --audit-level=high` | Reported 0 vulnerabilities. |
| `npm --prefix capsule audit --audit-level=high` | Reported 0 vulnerabilities. |

The two module-only Go advisories affect `x/crypto/ssh` and `x/crypto/openpgp`.
ref0 imports neither package, and `govulncheck` found no package or symbol path
to either advisory. No container or license result was run for this record.

## Current toolchains

| Component | Pinned toolchain |
| --- | --- |
| Go application and capsule supervisor | Go 1.27.0 |
| Frontend build | Node 24.20.0 |
| Pi capsule build and runtime | Node 22.19.0 |
| Database | PostgreSQL 18.6 Bookworm image |

The Dockerfiles pin these versions by image tag. For a release record, resolve
and store each base-image digest because a mutable tag can point to newer bytes.

## Go modules

`go.mod` declares module `github.com/cyr1en/ref0` with these application-facing
dependencies:

| Module | Version | Use |
| --- | --- | --- |
| `github.com/danielgtaylor/huma/v2` | `v2.39.1` | HTTP and OpenAPI |
| `github.com/jackc/pgx/v5` | `v5.10.0` | PostgreSQL driver and pool |
| `github.com/pressly/goose/v3` | `v3.27.3` | Embedded schema command |
| `github.com/prometheus/client_golang` | `v1.24.1` | Prometheus exporter |
| `github.com/bwmarrin/discordgo` | `v0.29.0` | Discord REST and Gateway client |
| `golang.org/x/crypto` | `v0.54.0` | scrypt and cryptographic support |
| `golang.org/x/net` | `v0.57.0` | Network helpers |
| `golang.org/x/sync` | `v0.22.0` | Concurrent process coordination |
| `golang.org/x/sys` | `v0.47.0` | Unix socket and filesystem checks |
| `golang.org/x/text` | `v0.40.0` | Unicode normalization and case folding |

`go.mod` also pins these indirect modules:

| Module | Version |
| --- | --- |
| `github.com/beorn7/perks` | `v1.0.1` |
| `github.com/cespare/xxhash/v2` | `v2.3.0` |
| `github.com/gorilla/websocket` | `v1.5.3` |
| `github.com/jackc/pgpassfile` | `v1.0.0` |
| `github.com/jackc/pgservicefile` | `v0.0.0-20240606120523-5a60cdf6a761` |
| `github.com/jackc/puddle/v2` | `v2.2.2` |
| `github.com/mfridman/interpolate` | `v0.0.2` |
| `github.com/munnerz/goautoneg` | `v0.0.0-20191010083416-a7dc8b61c822` |
| `github.com/prometheus/client_model` | `v0.6.2` |
| `github.com/prometheus/common` | `v0.70.1` |
| `github.com/prometheus/procfs` | `v0.21.1` |
| `github.com/sethvargo/go-retry` | `v0.4.0` |
| `go.uber.org/multierr` | `v1.11.0` |
| `google.golang.org/protobuf` | `v1.36.11` |

The WebSocket version is an explicit security pin. Use `go list -m all` to
confirm the resolved transitive graph.

`go.sum` fixes the downloaded module content. Run the audit from the repository
root with the same Go version as the production build:

```sh
docker run --rm \
  -v "$PWD:/src" \
  -w /src \
  golang:1.27.0-bookworm \
  sh -c 'go mod verify && go install golang.org/x/vuln/cmd/govulncheck@v1.7.0 && "$(go env GOPATH)/bin/govulncheck" ./...'
```

The command pins `govulncheck` to `v1.7.0`. Save `govulncheck -version` and the
complete command output. A finding is reachable only when `govulncheck` reports
a call path, but review module-only findings too because another build tag or a
later code path can make them reachable.

## Pi capsule packages

`capsule/package-lock.json` fixes this Node dependency graph:

| Package | Version | Scope |
| --- | --- | --- |
| `@earendil-works/pi-agent-core` | `0.84.4` | Runtime |
| `@earendil-works/pi-ai` | `0.84.4` | Runtime |
| `typebox` | `1.3.7` | Runtime |
| `@types/node` | `22.19.19` | Development |
| `prettier` | `3.6.2` | Development |
| `typescript` | `5.9.3` | Development |

Audit the locked production graph and the build graph:

```sh
npm --prefix capsule ci --ignore-scripts
npm --prefix capsule audit --omit=dev --audit-level=high
npm --prefix capsule audit --audit-level=high
```

The production audit covers bytes copied into the capsule runtime. The full
audit also covers packages that execute in CI and the image build.

## Frontend packages

`frontend/package-lock.json` fixes these browser dependencies:

| Package | Version |
| --- | --- |
| `@fontsource-variable/jetbrains-mono` | `5.3.0` |
| `@fontsource-variable/outfit` | `5.3.0` |
| `@tanstack/react-query` | `5.102.8` |
| `@tanstack/react-router` | `1.170.32` |
| `openapi-fetch` | `0.17.0` |
| `react` | `19.2.8` |
| `react-dom` | `19.2.8` |

The frontend development dependencies are:

| Package | Version |
| --- | --- |
| `@playwright/test` | `1.62.1` |
| `@testing-library/dom` | `10.4.1` |
| `@testing-library/jest-dom` | `7.0.1` |
| `@testing-library/react` | `16.3.3` |
| `@testing-library/user-event` | `14.6.6` |
| `@types/node` | `26.4.0` |
| `@types/react` | `19.2.18` |
| `@types/react-dom` | `19.2.5` |
| `@vitejs/plugin-react` | `6.1.1` |
| `axe-core` | `4.13.0` |
| `jsdom` | `30.0.1` |
| `openapi-typescript` | `7.13.0` |
| `typescript` | `6.0.3` |
| `vite` | `8.2.2` |
| `vitest` | `4.1.11` |

Read the complete graph from the lockfile rather than treating these direct
dependencies as an allowlist.

Audit both scopes:

```sh
npm --prefix frontend ci --ignore-scripts
npm --prefix frontend audit --omit=dev --audit-level=high
npm --prefix frontend audit --audit-level=high
```

## Audit the production images

The module audits do not inspect Debian packages, copied runtime files, image
configuration, or accidentally embedded secrets. Build both release images,
record their digests, then scan those immutable references:

```sh
docker build --pull --target production --tag ref0:audit .
docker build --pull --file capsule/Dockerfile --target production --tag ref0-pi-capsule:audit .
trivy image --scanners vuln,secret --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 ref0:audit
trivy image --scanners vuln,secret --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 ref0-pi-capsule:audit
```

Record `trivy version`, its vulnerability database metadata, and both image
digests with the output. Inspect the application image to confirm that it runs
as `ref0`, contains `/usr/local/bin/ref0`, and contains no retired application
runtime. Inspect the capsule image separately because it contains Node and the
pinned Pi packages.

## Review licenses and provenance

A vulnerability pass is not a license or provenance pass. Generate a complete
transitive module and package list from `go list -m all` and both lockfiles.
Review every license against the intended distribution, retain required notices,
and investigate packages with missing or custom license metadata. Also review
the source and integrity entries in both npm lockfiles and the checksums in
`go.sum`.

Do not write "0 vulnerabilities" or "all licenses are permissive" in this file
unless the corresponding release record contains the command output. The audit
result applies only to that commit, those image digests, and the advisory data
available at that UTC time.

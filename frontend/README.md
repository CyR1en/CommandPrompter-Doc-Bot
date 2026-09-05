# ref0 control-plane frontend

The frontend uses the committed `package-lock.json`. TypeScript is pinned to
`6.0.3`, while `openapi-typescript 7.13.0` still declares a TypeScript 5 peer
range. `frontend/.npmrc` enables `legacy-peer-deps` for that known range
mismatch.

## Regenerate the API contract

Install dependencies once, then run the contract workflow from the repository
root after changing a Huma route, request, response, or error schema:

```sh
npm --prefix frontend ci
npm --prefix frontend run generate:api
git diff --exit-code -- frontend/openapi.json frontend/src/api/schema.d.ts
```

`generate:api` runs the Go snapshot writer against the complete control-plane
handler, writes `frontend/openapi.json`, derives
`frontend/src/api/schema.d.ts`, and proves the committed snapshot equals the
live Huma document. It does not contact a running API. Commit both files when
the API contract changes.

The underlying Go-only snapshot command is available for focused diagnosis:

```sh
REF0_OPENAPI_OUTPUT="$PWD/frontend/openapi.json" \
  go test -count=1 ./internal/api -run '^TestWriteControlPlaneOpenAPI$'
```

That focused command writes only `openapi.json`; the normal `generate:api`
workflow writes and verifies both generated artifacts.

## Verify the frontend

Run the same checks used by CI:

```sh
npm --prefix frontend ci
npm --prefix frontend run generate:api
npm --prefix frontend run lint
npm --prefix frontend run typecheck
npm --prefix frontend test
npm --prefix frontend run build
```

To prove that the committed bindings match the Go API, regenerate both files
before these checks and require an empty diff.

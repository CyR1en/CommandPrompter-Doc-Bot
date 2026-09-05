#!/usr/bin/env bash
set -Eeuo pipefail

accept_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly accept_root
readonly accept_project="ref0-accept-e2e"
readonly accept_application_image="ref0-accept-e2e:local"
readonly accept_capsule_image="ref0-pi-capsule-accept-e2e:local"
readonly accept_api_port="${REF0_ACCEPT_API_PORT:-18080}"
readonly accept_database_port="${REF0_ACCEPT_DATABASE_PORT:-15432}"
readonly accept_fake_port="${REF0_ACCEPT_FAKE_PORT:-19090}"
readonly accept_proxy="${accept_project}-provider-proxy"
readonly accept_secret_scan="${accept_project}-secret-scan"
readonly accept_lock="/tmp/${accept_project}.lock"

accept_lock_owned=0
accept_scope_owned=0

validate_host_port() {
  local label="$1"
  local value="$2"
  if [[ ! "${value}" =~ ^[0-9]+$ ]] || (( value < 1024 || value > 65535 )); then
    echo "browser acceptance ${label} must be an integer from 1024 through 65535: ${value}" >&2
    exit 1
  fi
}

validate_host_port "API port" "${accept_api_port}"
validate_host_port "database port" "${accept_database_port}"
validate_host_port "fake-provider port" "${accept_fake_port}"
if [[ "${accept_api_port}" == "${accept_database_port}" || "${accept_api_port}" == "${accept_fake_port}" || "${accept_database_port}" == "${accept_fake_port}" ]]; then
  echo "browser acceptance API, database, and fake-provider ports must be distinct" >&2
  exit 1
fi

accept_compose=(
  docker compose
  --file "${accept_root}/docker-compose.yml"
  --file "${accept_root}/docker-compose.build.yml"
  --project-name "${accept_project}"
)

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ "${accept_scope_owned}" == "1" ]]; then
    for container in "${accept_proxy}" "${accept_secret_scan}"; do
      local label=""
      label="$(docker container inspect --format '{{ index .Config.Labels "io.ref0.acceptance" }}' "${container}" 2>/dev/null || true)"
      if [[ "${label}" == "${accept_project}" ]]; then
        docker container rm --force "${container}" >/dev/null 2>&1 || true
      fi
    done
    "${accept_compose[@]}" down --volumes --remove-orphans --timeout 15 >/dev/null 2>&1 || true
  fi
  if [[ "${accept_lock_owned}" == "1" ]]; then
    rmdir "${accept_lock}" >/dev/null 2>&1 || true
  fi
  exit "${status}"
}
trap cleanup EXIT INT TERM

for command in docker node npm npx; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "browser acceptance requires ${command}" >&2
    exit 1
  fi
done
if [[ ! -x "${accept_root}/frontend/node_modules/.bin/playwright" ]]; then
  echo "browser acceptance requires frontend dependencies from npm ci" >&2
  exit 1
fi
docker info >/dev/null

if ! mkdir "${accept_lock}" 2>/dev/null; then
  echo "browser acceptance scope is already locked: ${accept_lock}" >&2
  exit 1
fi
accept_lock_owned=1

if [[ -n "$(docker ps --all --quiet --filter "label=com.docker.compose.project=${accept_project}")" ]]; then
  echo "browser acceptance refuses to reuse existing ${accept_project} containers" >&2
  exit 1
fi
if [[ -n "$(docker volume ls --quiet --filter "label=com.docker.compose.project=${accept_project}")" ]]; then
  echo "browser acceptance refuses to reuse existing ${accept_project} volumes" >&2
  exit 1
fi
if [[ -n "$(docker network ls --quiet --filter "label=com.docker.compose.project=${accept_project}")" ]]; then
  echo "browser acceptance refuses to reuse an existing ${accept_project} network" >&2
  exit 1
fi
for container in "${accept_proxy}" "${accept_secret_scan}"; do
  if docker container inspect "${container}" >/dev/null 2>&1; then
    echo "browser acceptance refuses to reuse existing container ${container}" >&2
    exit 1
  fi
done
accept_scope_owned=1

random_secret() {
  node --input-type=module --eval 'import { randomBytes } from "node:crypto"; process.stdout.write(randomBytes(32).toString("base64url"));'
}

export API_PORT="${accept_api_port}"
export APP_BOOTSTRAP_TOKEN="$(random_secret)"
export APP_MASTER_KEY="acceptance-v1:$(random_secret)"
export METRICS_BEARER_TOKEN="$(random_secret)"
export COMPOSE_PROJECT_NAME="${accept_project}"
export CONTROL_PLANE_API_URL="http://127.0.0.1:${accept_api_port}"
export CONTROL_PLANE_OPERATOR_PASSWORD="$(random_secret)"
export CONTROL_PLANE_OPERATOR_USERNAME="AcceptanceOperator"
export CONTROL_PLANE_PROVIDER_FAKE_PORT="${accept_fake_port}"
export CONTROL_PLANE_PROVIDER_SECRET_SENTINEL="provider-$(random_secret)"
export POSTGRES_DB="ref0_accept"
export POSTGRES_PASSWORD="$(random_secret)"
export POSTGRES_PORT="${accept_database_port}"
export POSTGRES_USER="ref0"
export PUBLIC_ORIGIN="${CONTROL_PLANE_API_URL}"
export REF0_BROWSER_ACCEPTANCE="1"
export REF0_CAPSULE_IMAGE="${accept_capsule_image}"
export REF0_IMAGE="${accept_application_image}"

if command -v go >/dev/null 2>&1; then
  REF0_ACCEPTANCE_GO_BINARY="$(command -v go)"
  export REF0_ACCEPTANCE_GO_BINARY
else
  unset REF0_ACCEPTANCE_GO_BINARY
fi

"${accept_compose[@]}" config --quiet
"${accept_compose[@]}" pull postgres
"${accept_compose[@]}" build migrate capsule-slot-0
"${accept_compose[@]}" up \
  --detach \
  --no-build \
  --pull never \
  --wait \
  --wait-timeout 240 \
  postgres capsule-slot-0 capsule-slot-1 api worker

accept_migrate_id="$("${accept_compose[@]}" ps --all --quiet migrate)"
if [[ -z "${accept_migrate_id}" ]]; then
  echo "browser acceptance migration container is missing" >&2
  exit 1
fi
if [[ "$(docker container inspect --format '{{.State.ExitCode}}' "${accept_migrate_id}")" != "0" ]]; then
  echo "browser acceptance migration did not complete successfully" >&2
  exit 1
fi

(
  cd "${accept_root}/frontend"
  npx --no-install playwright test \
    --workers=1 \
    e2e/auth-session.spec.ts \
    e2e/provider-setup.spec.ts
)

echo "ref0 browser acceptance passed"

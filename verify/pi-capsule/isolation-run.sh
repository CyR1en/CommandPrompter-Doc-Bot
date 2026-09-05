#!/bin/sh
set -eu

project="ref0-pi-capsule-isolation-negative"
compose="verify/pi-capsule/compose.yml"
assert_exit="verify/pi-capsule/assert-container-exit-zero.sh"
negative_name="ref0-pi-capsule-exit-assert-negative-$$"
malformed_log=$(mktemp "${TMPDIR:-/tmp}/pi-capsule-malformed.XXXXXX")

cleanup() {
  docker rm --force "$negative_name" >/dev/null 2>&1 || true
  docker compose --project-name "$project" --file "$compose" --profile negative down --volumes --remove-orphans
  rm -f "$malformed_log"
}
trap cleanup EXIT INT TERM

"$(dirname "$0")/run.sh"

negative_id=$(docker create --name "$negative_name" --entrypoint /bin/sh \
  ref0-pi-capsule:verify -c 'exit 7')
set +e
docker start --attach "$negative_id" >/dev/null 2>&1
container_status=$?
"$assert_exit" "negative-self-check=$negative_id" >/dev/null 2>&1
assertion_status=$?
set -e
test "$container_status" -ne 0
test "$assertion_status" -ne 0
printf '%s\n' '{"supervisor_exit_assertion_negative_self_check":"rejected"}'

set +e
docker compose --project-name "$project" --file "$compose" --profile negative up \
  --no-deps --exit-code-from capsule-malformed capsule-malformed >"$malformed_log" 2>&1
status=$?
set -e
test "$status" -ne 0
grep -q "capsule socket directory ownership or mode is invalid" "$malformed_log"
printf '%s\n' '{"malformed_volume_readiness":"rejected"}'

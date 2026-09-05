#!/bin/sh
set -eu

project="ref0-pi-capsule-verify"
compose="verify/pi-capsule/compose.yml"
assert_exit="verify/pi-capsule/assert-container-exit-zero.sh"
wall_clock_seconds=${CAPSULE_VERIFY_TIMEOUT_SECONDS:-600}
watchdog_pid=
compose_pid=

case "$wall_clock_seconds" in
  ''|*[!0-9]*|0)
    printf '%s\n' "CAPSULE_VERIFY_TIMEOUT_SECONDS must be a positive integer" >&2
    exit 2
    ;;
esac

cleanup() {
  if [ -n "$watchdog_pid" ]; then
    kill "$watchdog_pid" >/dev/null 2>&1 || true
    wait "$watchdog_pid" >/dev/null 2>&1 || true
    watchdog_pid=
  fi
  if [ -n "$compose_pid" ]; then
    kill -TERM "$compose_pid" >/dev/null 2>&1 || true
    compose_pid=
  fi
  docker compose --project-name "$project" --file "$compose" down --volumes --remove-orphans
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

set +e
docker compose --project-name "$project" --file "$compose" up \
  --build --abort-on-container-exit --exit-code-from control &
compose_pid=$!
sh -c '
  sleep "$1" &
  timer_pid=$!
  trap '\''kill "$timer_pid" >/dev/null 2>&1 || true; exit 0'\'' INT TERM
  wait "$timer_pid" || exit 0
  if kill -0 "$2" 2>/dev/null; then
    printf "%s\n" "Pi capsule verification exceeded ${1}s" >&2
    compose_parent=$(ps -o ppid= -p "$3" 2>/dev/null | tr -d " ")
    if [ "$compose_parent" = "$2" ]; then
      kill -TERM "$3" 2>/dev/null || true
    fi
    kill -TERM "$2" 2>/dev/null || exit 0
    sleep 10
    kill -KILL "$3" 2>/dev/null || true
    kill -KILL "$2" 2>/dev/null || true
  fi
' ref0-pi-capsule-watchdog "$wall_clock_seconds" "$$" "$compose_pid" &
watchdog_pid=$!
wait "$compose_pid"
compose_status=$?
compose_pid=
set -e

supervisor_status=0
for service in capsule capsule-production-b capsule-stalled capsule-a capsule-b isolation; do
  ids=$(docker compose --project-name "$project" --file "$compose" ps --all --quiet "$service") || {
    printf '%s\n' "$service container lookup failed" >&2
    supervisor_status=1
    continue
  }
  # Word splitting is intentional: exactly one container must exist per fixed slot.
  set -- $ids
  if [ "$#" -ne 1 ]; then
    printf '%s\n' "$service expected one container, found $#" >&2
    supervisor_status=1
    continue
  fi
  if ! "$assert_exit" "$service=$1"; then
    supervisor_status=1
  fi
done

if [ "$compose_status" -ne 0 ]; then
  printf '%s\n' "Compose control verification failed: exit $compose_status" >&2
  exit "$compose_status"
fi
test "$supervisor_status" -eq 0

#!/bin/sh
set -eu

if [ "$#" -eq 0 ]; then
  printf '%s\n' "usage: $0 name=container-id [...]" >&2
  exit 2
fi

for reference in "$@"; do
  name=${reference%%=*}
  container=${reference#*=}
  if [ -z "$name" ] || [ -z "$container" ] || [ "$name" = "$container" ]; then
    printf '%s\n' "invalid container reference: $reference" >&2
    exit 1
  fi
  state=$(docker inspect --format '{{.State.Status}} {{.State.ExitCode}} {{.State.OOMKilled}}' "$container")
  if [ "$state" != "exited 0 false" ]; then
    printf '%s\n' "$name failed supervisor exit assertion: $state" >&2
    exit 1
  fi
  printf '%s\n' "$name supervisor exit: $state"
done

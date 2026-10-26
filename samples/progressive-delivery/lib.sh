#!/usr/bin/env bash
# Copyright 2026 The OpenChoreo Authors
# SPDX-License-Identifier: Apache-2.0
#
# Shared helpers for the progressive delivery demos. Source it from a demo.sh.

set -euo pipefail

NS="${NS:-default}"
ENV_NAME="${ENV_NAME:-development}"
# AUTO=1 runs without pausing between steps.
AUTO="${AUTO:-0}"
# REQUESTS is how many requests each tally sends per path.
REQUESTS="${REQUESTS:-100}"

bold() { printf '\n\033[1m%s\033[0m\n' "$*"; }
say() { printf '  %s\n' "$*"; }

pause() {
  if [[ "$AUTO" != "1" ]]; then
    read -r -p "  [enter] " _
  fi
}

# run prints a command, then runs it.
run() {
  printf '  \033[2m$ %s\033[0m\n' "$*"
  "$@"
}

# binding prints the name of the Endpoint's binding in ENV_NAME.
binding() { echo "$1-$ENV_NAME"; }

# wait_ready waits until the Endpoint's binding is Ready on its latest release.
wait_ready() {
  local ep="$1" b latest
  b="$(binding "$ep")"
  for _ in $(seq 1 90); do
    latest="$(kubectl get endpoints.openchoreo.dev "$ep" -n "$NS" -o jsonpath='{.status.latestRelease.name}' 2>/dev/null || true)"
    if [[ -n "$latest" ]] &&
      [[ "$(kubectl get endpointreleasebinding "$b" -n "$NS" -o jsonpath='{.spec.endpointRelease}' 2>/dev/null)" == "$latest" ]] &&
      [[ "$(kubectl get endpointreleasebinding "$b" -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" == "True" ]]; then
      wait_backends "$ep"
      return 0
    fi
    sleep 2
  done
  echo "binding $b did not become Ready; kubectl describe endpointreleasebinding $b -n $NS" >&2
  return 1
}

# wait_backends waits until the Deployments of the binding's backend
# components are available, so the first requests don't hit pods that are
# still starting.
wait_backends() {
  local ep="$1" dpns c
  dpns="$(dp_namespace "$ep")"
  for c in $(kubectl get endpointreleasebinding "$(binding "$ep")" -n "$NS" -o jsonpath='{.status.backends[*].componentName}'); do
    kubectl wait deployment -n "$dpns" -l "openchoreo.dev/component=$c" --for=condition=Available --timeout=180s >/dev/null
  done
}

# wait_weights waits until the binding reports the given effective weights
# and the data plane has accepted the new routes, then lets Envoy settle.
wait_weights() {
  local ep="$1" want got names
  shift
  want="$(printf '%s\n' "$@" | sort | tr '\n' ' ')"
  names="$(printf '%s ' "${@%%=*}")"
  for _ in $(seq 1 60); do
    # status.backends omits a weight of 0.
    got="$(kubectl get endpointreleasebinding "$(binding "$ep")" -n "$NS" \
      -o jsonpath='{range .status.backends[*]}{.name}={.weight}{"\n"}{end}' |
      awk -F= -v names=" $names" 'index(names, " " $1 " ") {print $1 "=" ($2 == "" ? 0 : $2)}' | sort | tr '\n' ' ')"
    if [[ "$got" == "$want" ]] &&
      [[ "$(kubectl get endpointreleasebinding "$(binding "$ep")" -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="RoutesReady")].status}')" == "True" ]]; then
      sleep "${SETTLE_SECONDS:-5}"
      return 0
    fi
    sleep 1
  done
  echo "the binding of $ep did not reach weights: $want" >&2
}

# set_weights patches the binding's backendOverrides, e.g.
# set_weights trips-api trips-python=95 trips-go=5. No new release is cut.
set_weights() {
  local ep="$1" overrides="" sep=""
  shift
  for kv in "$@"; do
    overrides+="$sep{\"name\":\"${kv%%=*}\",\"weight\":${kv#*=}}"
    sep=","
  done
  run kubectl patch endpointreleasebinding "$(binding "$ep")" -n "$NS" --type merge \
    -p "{\"spec\":{\"backendOverrides\":[$overrides]}}"
  wait_weights "$ep" "$@"
}

# release_count prints how many EndpointReleases the Endpoint has.
release_count() {
  kubectl get endpointreleases -n "$NS" \
    -o jsonpath="{range .items[?(@.spec.owner.endpointName==\"$1\")]}{.metadata.name}{\"\\n\"}{end}" | grep -c . || true
}

# invoke_url prints the Endpoint's gateway URL in ENV_NAME.
invoke_url() {
  kubectl get endpointreleasebinding "$(binding "$1")" -n "$NS" -o jsonpath='{.status.invokeURL}'
}

# dp_namespace prints the data-plane namespace the Endpoint's Services are in.
dp_namespace() {
  kubectl get svc -A -l "openchoreo.dev/endpoint=$1" -o jsonpath='{.items[0].metadata.namespace}' 2>/dev/null ||
    kubectl get svc -A --field-selector "metadata.name=$1" -o jsonpath='{.items[0].metadata.namespace}'
}

# ensure_client starts a curl pod in the data-plane namespace, for the
# in-cluster (east-west) path. The namespace's mesh enrolment applies to it.
ensure_client() {
  local dpns="$1"
  if ! kubectl get pod pd-client -n "$dpns" >/dev/null 2>&1; then
    kubectl run pd-client -n "$dpns" --image=curlimages/curl:8.11.1 --restart=Never --command -- sleep infinity >/dev/null
  fi
  kubectl wait pod/pd-client -n "$dpns" --for=condition=Ready --timeout=120s >/dev/null
}

# tally sends REQUESTS requests to URL with optional curl arguments and counts
# who answered (X-Served-By) and the status codes.
tally() {
  local url="$1"
  shift
  for _ in $(seq 1 "$REQUESTS"); do
    curl -s -o /dev/null -D - "$@" "$url" | tr -d '\r' |
      awk 'NR==1{code=$2} tolower($1)=="x-served-by:"{who=$2} END{print (who==""?"-":who), code}'
  done | sort | uniq -c | awk '{printf "    %4d  %-18s HTTP %s\n", $1, $2, $3}'
}

# tally_mesh does the same from the in-cluster client, calling the Endpoint's
# stable Service.
tally_mesh() {
  local ep="$1" suffix="$2" dpns args=""
  shift 2
  if [[ $# -gt 0 ]]; then args="$(printf '%q ' "$@")"; fi
  dpns="$(dp_namespace "$ep")"
  ensure_client "$dpns"
  kubectl exec -n "$dpns" pd-client -- sh -c "
    for i in \$(seq 1 $REQUESTS); do
      curl -s -o /dev/null -D - $args 'http://$ep/${suffix#/}' | tr -d '\r' |
        awk 'NR==1{code=\$2} tolower(\$1)==\"x-served-by:\"{who=\$2} END{print (who==\"\"?\"-\":who), code}'
    done" | sort | uniq -c | awk '{printf "    %4d  %-18s HTTP %s\n", $1, $2, $3}'
}

# both tallies the gateway path and the in-cluster path side by side. SUFFIX
# is appended to both URLs (for example "/?country=DE"); curl arguments
# follow it. Set MESH=0 on a data plane without a mesh.
both() {
  local ep="$1" suffix="${2:-}" url
  shift 2 || shift $#
  url="$(invoke_url "$ep")$suffix"
  say "north-south, through the gateway ($url):"
  tally "$url" "$@"
  if [[ "${MESH:-1}" == "1" ]]; then
    say "east-west, from a pod in the cluster (http://$ep/${suffix#/}):"
    tally_mesh "$ep" "$suffix" "$@"
  fi
}

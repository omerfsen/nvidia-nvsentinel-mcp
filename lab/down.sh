#!/usr/bin/env bash
# Delete the lab cluster and its kubeconfigs, nothing else.
source "$(dirname "$0")/common.sh"
ensure_kind
# A failing `kind get clusters` (Docker stopped, no permission) must stop here,
# not read as "no cluster" and delete the kubeconfigs of a running one.
clusters=$("$KIND" get clusters)
if grep -qx "$CLUSTER" <<<"$clusters"; then
  "$KIND" delete cluster --name "$CLUSTER" --kubeconfig "$ADMIN_KUBECONFIG"
fi
# Only once the cluster is gone: its kubeconfig is the way back in.
if grep -qx "$CLUSTER" <<<"$("$KIND" get clusters)"; then
  echo "down.sh: cluster $CLUSTER is still there; kubeconfigs kept" >&2
  exit 1
fi
rm -f "$ADMIN_KUBECONFIG" "$MCP_KUBECONFIG" "$MCP_DOCKER_KUBECONFIG"

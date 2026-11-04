#!/usr/bin/env bash
# Delete the lab cluster and its kubeconfigs, nothing else.
source "$(dirname "$0")/common.sh"
if [[ -x $KIND ]] && "$KIND" get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  "$KIND" delete cluster --name "$CLUSTER" --kubeconfig "$ADMIN_KUBECONFIG"
fi
rm -f "$ADMIN_KUBECONFIG" "$MCP_KUBECONFIG" "$MCP_DOCKER_KUBECONFIG"

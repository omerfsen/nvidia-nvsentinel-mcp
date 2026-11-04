#!/usr/bin/env bash
# Short-lived kubeconfigs for the server's ServiceAccount (lab/rbac.yaml), never
# the admin one: one for the host, one for a container on kind's Docker network.
source "$(dirname "$0")/common.sh"
TOKEN_HOURS=${TOKEN_HOURS:-8}
token=$(admin create token nvsentinel-mcp -n nvsentinel-mcp --duration="${TOKEN_HOURS}h")
mint() {  # mint <file> [--internal]
  local file=$1; shift
  (umask 077 && "$KIND" get kubeconfig --name "$CLUSTER" "$@" > "$file")
  chmod 600 "$file"
  kubectl --kubeconfig "$file" config set-credentials "$CONTEXT" --token="$token" >/dev/null
  # Drop the admin certificate kind wrote: only the token remains.
  kubectl --kubeconfig "$file" config unset "users.$CONTEXT.client-certificate-data" >/dev/null
  kubectl --kubeconfig "$file" config unset "users.$CONTEXT.client-key-data" >/dev/null
}
mint "$MCP_KUBECONFIG"
mint "$MCP_DOCKER_KUBECONFIG" --internal
echo "Token valid for ${TOKEN_HOURS}h: $MCP_KUBECONFIG, $MCP_DOCKER_KUBECONFIG"

#!/usr/bin/env bash
# Short-lived kubeconfigs for the server's ServiceAccount (lab/rbac.yaml), never the
# admin one: one for the host, one for a container on kind's Docker network. The token
# stays out of argv, and each file is checked before it is trusted.
source "$(dirname "$0")/common.sh"
ensure_kind
TOKEN_HOURS=${TOKEN_HOURS:-8}
EXPECTED_USER=system:serviceaccount:nvsentinel-mcp:nvsentinel-mcp
TOKEN=$(admin create token nvsentinel-mcp -n nvsentinel-mcp --duration="${TOKEN_HOURS}h")
export TOKEN

mint() {  # mint <file> [--internal]
  local file=$1; shift
  (umask 077 && "$KIND" get kubeconfig --name "$CLUSTER" "$@" > "$file")
  chmod 600 "$file"
  # Every user entry is replaced, so kind's admin certificate cannot stay behind.
  yq -i '.users[].user = {"token": strenv(TOKEN)}' "$file"
  if yq '.users' "$file" | grep -q 'certificate-data\|client-key'; then
    : > "$file"
    echo "mint-kubeconfig.sh: $file kept a certificate, so it was emptied" >&2
    exit 1
  fi
}

mint "$MCP_KUBECONFIG"
mint "$MCP_DOCKER_KUBECONFIG" --internal

who=$(kubectl --kubeconfig "$MCP_KUBECONFIG" auth whoami -o jsonpath='{.status.userInfo.username}')
if [[ $who != "$EXPECTED_USER" ]]; then
  echo "mint-kubeconfig.sh: the kubeconfig is $who, not $EXPECTED_USER" >&2
  exit 1
fi
if [[ $(kubectl --kubeconfig "$MCP_KUBECONFIG" auth can-i get secrets -n nvsentinel) != no ]]; then
  echo "mint-kubeconfig.sh: the server's identity can read Secrets" >&2
  exit 1
fi
echo "Identity $who, valid for ${TOKEN_HOURS}h: $MCP_KUBECONFIG, $MCP_DOCKER_KUBECONFIG"

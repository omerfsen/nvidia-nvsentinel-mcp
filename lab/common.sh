# shellcheck shell=bash disable=SC2034
# Settings for the lab scripts. Source it, do not run it.
set -euo pipefail
CLUSTER=nvsentinel-lab
CONTEXT=kind-$CLUSTER
LAB_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
ROOT=$(dirname "$LAB_DIR")
TOOLS=$ROOT/.lab
KIND=$TOOLS/bin/kind
# The lab's own kubeconfigs. The scripts never read or write ~/.kube/config.
ADMIN_KUBECONFIG=$HOME/.kube/nvsentinel-lab-admin.yaml
MCP_KUBECONFIG=$HOME/.kube/nvsentinel-lab-mcp.yaml
MCP_DOCKER_KUBECONFIG=$HOME/.kube/nvsentinel-lab-mcp-docker.yaml
admin() { kubectl --kubeconfig "$ADMIN_KUBECONFIG" --context "$CONTEXT" "$@"; }

# The pinned kind, downloaded into the checkout on first use. Every script that
# talks to kind calls this first: a fresh clone has no $KIND yet.
KIND_VERSION=v0.33.0
KIND_SHA256=aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d
ensure_kind() {
  if [[ ! -x $KIND ]] || ! echo "$KIND_SHA256  $KIND" | sha256sum -c --status; then
    mkdir -p "$(dirname "$KIND")"
    curl -fsSL -o "$KIND.new" "https://github.com/kubernetes-sigs/kind/releases/download/$KIND_VERSION/kind-linux-amd64"
    echo "$KIND_SHA256  $KIND.new" | sha256sum -c --status
    chmod 755 "$KIND.new"
    mv "$KIND.new" "$KIND"
  fi
}

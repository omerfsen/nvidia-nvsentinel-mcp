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

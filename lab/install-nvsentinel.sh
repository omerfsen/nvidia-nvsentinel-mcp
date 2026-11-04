#!/usr/bin/env bash
# Install NVSentinel through omerfsen.aicr, as this repo installs nothing itself.
# AICR_DIR is a checkout of github.com/omerfsen/aicr with `make dev` done.
source "$(dirname "$0")/common.sh"
AICR_DIR=${AICR_DIR:-$HOME/projects/aicr}
cd "$AICR_DIR" || exit 1
exec .venv/bin/ansible-playbook playbooks/deploy.yml -i localhost, -c local \
  -e aicr_target=localhost -e ansible_become=false \
  -e @examples/kind-nvsentinel/vars.yml \
  -e "aicr_kubeconfig=$ADMIN_KUBECONFIG" \
  -e "aicr_bin_dir=$TOOLS/bin" -e "aicr_workdir=$TOOLS/aicr" -e "aicr_helm_plugins_dir=$TOOLS/helm-plugins" \
  -e "aicr_owner=$(id -un)" -e "aicr_group=$(id -gn)" \
  -e "aicr_helm=$(command -v helm)" -e "aicr_kubectl=$(command -v kubectl)"

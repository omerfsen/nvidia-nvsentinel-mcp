#!/usr/bin/env bash
# Create the kind cluster (pinned kind and node image), if it is not there.
source "$(dirname "$0")/common.sh"
NODE_IMAGE=kindest/node:v1.36.4@sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed
MIN_FREE_GIB=${MIN_FREE_GIB:-10}

free_gib=$(awk '/^MemAvailable:/ {print int($2 / 1048576)}' /proc/meminfo)
if ((free_gib < MIN_FREE_GIB)); then
  echo "up.sh: $free_gib GiB free, the lab wants $MIN_FREE_GIB. Nothing was created." >&2
  exit 1
fi

ensure_kind

if "$KIND" get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  echo "up.sh: cluster $CLUSTER is already there"
  # A kubeconfig removed by hand or by an older down.sh: write it again.
  if [[ ! -f $ADMIN_KUBECONFIG ]]; then
    mkdir -p "$(dirname "$ADMIN_KUBECONFIG")"
    (umask 077 && "$KIND" export kubeconfig --name "$CLUSTER" --kubeconfig "$ADMIN_KUBECONFIG")
  fi
else
  mkdir -p "$(dirname "$ADMIN_KUBECONFIG")"
  (umask 077 && "$KIND" create cluster --name "$CLUSTER" --image "$NODE_IMAGE" \
    --config "$LAB_DIR/kind.yaml" --kubeconfig "$ADMIN_KUBECONFIG")
fi
chmod 600 "$ADMIN_KUBECONFIG"
admin wait --for=condition=Ready nodes --all --timeout=180s

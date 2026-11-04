#!/usr/bin/env bash
# What the tests need on top of NVSentinel: the simulated DCGM, a labelled "GPU"
# node, a workload to evict, and the server's own identity.
source "$(dirname "$0")/common.sh"
admin create namespace gpu-operator --dry-run=client -o yaml | admin apply -f -
admin create configmap nvidia-dcgm-gpu-spec -n gpu-operator \
  --from-file=gpu-spec.yaml="$LAB_DIR/gpu-spec.yaml" --dry-run=client -o yaml | admin apply -f -
admin apply -f "$LAB_DIR/fake-dcgm.yaml"
admin apply -f "$LAB_DIR/rbac.yaml"
admin rollout status daemonset/nvidia-dcgm -n gpu-operator --timeout=900s

# In a real cluster the GPU Operator and NVSentinel's labeler write these.
for node in $(admin get nodes -o name -l '!node-role.kubernetes.io/control-plane'); do
  admin label "$node" --overwrite \
    nvidia.com/gpu.present=true \
    nvsentinel.dgxc.nvidia.com/driver.installed=true \
    nvsentinel.dgxc.nvidia.com/kata.enabled=false \
    nvsentinel.dgxc.nvidia.com/dcgm.version=4.x
done
admin rollout status daemonset/gpu-health-monitor-dcgm-4.x -n nvsentinel --timeout=900s
admin apply -f "$LAB_DIR/workload.yaml"
admin rollout status deployment/gpu-workload -n default --timeout=180s

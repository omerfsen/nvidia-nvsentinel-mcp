#!/usr/bin/env bash
# image.sh - image tags and registry login for the docker pipeline. Vendored to
# .github/cicd/docker/image.sh; tested by tests/pipelines/docker-test.sh.
#
#   image.sh tags <git-ref> <commit-sha>
#       The tags a push of that ref publishes, one per line; nothing when the
#       ref must not publish (anything but main and a vX.Y.Z tag).
#         refs/heads/main        sha-<7>, main
#         refs/tags/vX.Y.Z       X.Y.Z, X.Y, X (from 1.0.0 on), latest, sha-<7>
#         refs/tags/vX.Y.Z-pre   X.Y.Z-pre, sha-<7>
#
#   image.sh login <registry>
#       How to log in to the registry: "ghcr", "acr <name>" or "dockerhub".
#       Exits 1 for any other registry. Docker Hub is the one login with a
#       stored token, so it must be named in full: docker.io/<namespace>.
set -euo pipefail
die() { printf 'image.sh: %s\n' "$*" >&2; exit 1; }

tags() {
  local ref=${1:-} sha=${2:-}
  [[ $sha =~ ^[0-9a-f]{7,40}$ ]] || die "not a commit sha: '$sha'"
  local short=sha-${sha:0:7}
  if [[ $ref == refs/heads/main ]]; then
    printf '%s\n' "$short" main
  elif [[ $ref =~ ^refs/tags/v(([0-9]+)\.([0-9]+)\.([0-9]+))$ ]]; then
    local v=${BASH_REMATCH[1]} major=${BASH_REMATCH[2]} minor=${BASH_REMATCH[3]}
    printf '%s\n' "$v" "$major.$minor"
    ((major >= 1)) && printf '%s\n' "$major"
    printf '%s\n' latest "$short"
  elif [[ $ref =~ ^refs/tags/v([0-9]+\.[0-9]+\.[0-9]+-[0-9A-Za-z.-]+)$ ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}" "$short"
  fi
  return 0
}

login() {
  local reg=${1:-} host
  host=${reg%%/*}
  [[ -n $host ]] || die "empty registry"
  if [[ $host == ghcr.io ]]; then echo ghcr
  elif [[ $host =~ ^([a-z0-9]{5,50})\.azurecr\.io$ ]]; then echo "acr ${BASH_REMATCH[1]}"
  elif [[ $reg =~ ^docker\.io/[a-z0-9]+([._-][a-z0-9]+)*$ ]]; then echo dockerhub
  else die "no login for $reg (ghcr.io, <name>.azurecr.io and docker.io/<namespace> are supported)"
  fi
}

case ${1:-} in
  tags) shift; tags "$@" ;;
  login) shift; login "$@" ;;
  *) die "usage: image.sh tags <git-ref> <sha> | image.sh login <registry>" ;;
esac

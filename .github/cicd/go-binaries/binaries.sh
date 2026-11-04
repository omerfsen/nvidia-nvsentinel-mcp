#!/usr/bin/env bash
# binaries.sh - version, targets and file names for the go-binaries pipeline.
# Vendored to .github/cicd/go-binaries/binaries.sh; tested by
# tests/pipelines/go-binaries-test.sh.
#
#   binaries.sh version <git-ref>
#       The version a ref releases: refs/tags/v1.2.3 -> 1.2.3, and a pre-release
#       such as v1.2.3-rc.1 -> 1.2.3-rc.1. Nothing for any other ref.
#
#   binaries.sh targets <goos/goarch ...>
#       The targets, one per line. Exits 1 on anything but linux, darwin or windows
#       on amd64 or arm64, so a setting can never reach a command line unchecked.
#
#   binaries.sh asset <name> <goos/goarch>
#       The file name: <name>-<goos>-<goarch>, plus .exe on Windows. It holds no
#       version, so /releases/latest/download/<file> keeps working across releases.
set -euo pipefail
die() { printf 'binaries.sh: %s\n' "$*" >&2; exit 1; }

valid_target() { [[ ${1:-} =~ ^(linux|darwin|windows)/(amd64|arm64)$ ]]; }

version() {
  if [[ ${1:-} =~ ^refs/tags/v([0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)$ ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}"
  fi
}

targets() {
  (($# > 0)) || die "no targets"
  local target
  for target in "$@"; do
    valid_target "$target" || die "not a supported target: '$target'"
    printf '%s\n' "$target"
  done
}

asset() {
  local name=${1:-} target=${2:-}
  [[ $name =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "not a binary name: '$name'"
  valid_target "$target" || die "not a supported target: '$target'"
  local suffix=""
  [[ $target == windows/* ]] && suffix=.exe
  printf '%s-%s-%s%s\n' "$name" "${target%/*}" "${target#*/}" "$suffix"
}

case ${1:-} in
  version) shift; version "$@" ;;
  targets) shift; targets "$@" ;;
  asset) shift; asset "$@" ;;
  *) die "usage: binaries.sh version <ref> | targets <goos/goarch ...> | asset <name> <goos/goarch>" ;;
esac

#!/usr/bin/env bash
# check-release-pr.sh - decides whether the release workflow may merge a
# release PR on its own: it must be the release App's own PR, from
# release-please's branch in this repository into main, changing only
# CHANGELOG.md, .release-please-manifest.json and the repo's version files, the
# last ones only in their version line, all to the same version, below 1.0.0.
#
# Vendored from https://github.com/omerfsen/cicd (github/pipelines/release-please/);
# edit it there.
# Tested in that repo's tests/release-check.sh.
#
# Usage: REPO=<owner/name> SLUG=<app slug> VERSION_FILES="<files>" \
#          check-release-pr.sh <pr.json> <files.json>
#   pr.json        GET /repos/{repo}/pulls/{n}
#   files.json     GET /repos/{repo}/pulls/{n}/files (one array, or one per page)
#   VERSION_FILES  space-separated files release-please writes the version to
#                  (version.txt, package.json, pyproject.toml, ...)
# A version line is one of:  1.2.3   |   "version": "1.2.3",   |   version = "1.2.3"
#   |   version: 1.2.3 # x-release-please-version   (galaxy.yml, generic updater)
# Exit 0 = may merge; 1 = refused, with the reason on stderr.
set -euo pipefail

fail() { echo "$1" >&2; exit 1; }
[ -n "${REPO:-}" ] && [ -n "${SLUG:-}" ] || fail "REPO and SLUG must be set."
[ -n "${VERSION_FILES:-}" ] || fail "VERSION_FILES must be set."
[ $# -eq 2 ] || fail "usage: check-release-pr.sh <pr.json> <files.json>"

info=$(cat "$1")
files=$(jq -s 'add // []' "$2")
read -r -a vfiles <<<"$VERSION_FILES"

# The REST API names an App's PR author "<slug>[bot]" (type Bot); gh's own
# JSON says "app/<slug>". Either form, of this App only.
author=$(jq -r '.user.login // ""' <<<"$info")
case "$author" in
  "${SLUG}[bot]"|"app/${SLUG}") ;;
  *) fail "opened by ${author:-nobody}, not ${SLUG}[bot]." ;;
esac
[ "$(jq -r '.user.type // ""' <<<"$info")" = "Bot" ] || fail "the author $author is not a bot account."
[ "$(jq -r '.head.repo.full_name // ""' <<<"$info")" = "$REPO" ] || fail "it comes from another repository."
[ "$(jq -r '.base.ref // ""' <<<"$info")" = "main" ] || fail "it does not target main."
head=$(jq -r '.head.ref // ""' <<<"$info")
[[ "$head" =~ ^release-please--branches--main(--components--[a-z0-9-]+)?$ ]] \
  || fail "its branch ${head:-(none)} is not release-please's branch for main."
[ "$(jq 'length' <<<"$files")" -gt 0 ] || fail "it changes no files."

# Old names count too, so moving a file out of the way is not a loophole.
allowed=" CHANGELOG.md .release-please-manifest.json ${vfiles[*]} "
while read -r f; do
  [[ $allowed == *" $f "* ]] \
    || fail "it changes $f; a release PR changes only CHANGELOG.md, .release-please-manifest.json and ${vfiles[*]}."
done < <(jq -r '.[] | .filename, (.previous_filename // empty)' <<<"$files")

semver='[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?'
# A changed line is a version line in one of the four shapes, nothing else.
# The YAML one is top-level only, with release-please's marker or nothing after it.
marker=' # x-release-please-version'
version_line="^[+-]([[:space:]]*\"version\": \"$semver\",?|[[:space:]]*version = \"$semver\"|version: $semver($marker)?|$semver)$"
manifest_line="^[+-][[:space:]]*\"\\.\": \"$semver\"$"

patch_of() { jq -r --arg f "$1" '.[] | select(.filename == $f) | .patch' <<<"$files"; }
# $1 file, $2 pattern every changed line must match. A file GitHub shows no
# diff for (too large) is refused, not waved through. GitHub's patch has no
# ---/+++ file headers, so a line that looks like one is content: removing a
# YAML "---" is a change like any other.
only_version() {
  if jq -e --arg f "$1" 'any(.[]; .filename == $f and .patch == null)' <<<"$files" >/dev/null; then
    fail "no diff for $1 to check."
  fi
  local bad
  bad=$(patch_of "$1" | grep -E '^[+-]' | grep -Ev "$2" || true)
  [ -z "$bad" ] || fail "it changes more than the version in $1: $bad"
}
new_version() { # $1 file: the one version its '+' lines set
  local v
  v=$(patch_of "$1" | grep -E '^\+' \
    | sed -nE -e 's/^\+[[:space:]]*("version": "|version = "|"\.": ")?([0-9][^"]*)"?,?$/\2/p' \
        -e "s/^\\+version: ([0-9][^ ]*)($marker)?\$/\\1/p" || true)
  [ -n "$v" ] || fail "it changes no version in $1."
  [ "$(wc -l <<<"$v")" -eq 1 ] || fail "it sets more than one version in $1."
  echo "$v"
}

only_version .release-please-manifest.json "$manifest_line"
version=$(new_version .release-please-manifest.json)
for f in "${vfiles[@]}"; do
  only_version "$f" "$version_line"
  v=$(new_version "$f")
  [ "$v" = "$version" ] || fail "$f and the manifest disagree on the version: $v and $version."
done
# 1.0.0 and later are never merged here: that release is the owner's to cut, by hand.
[ "${version%%.*}" = "0" ] || fail "it would release v$version. v1.0.0 and later are cut only by the owner, by hand."

echo "ok"

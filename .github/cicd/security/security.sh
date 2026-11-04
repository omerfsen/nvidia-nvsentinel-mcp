#!/usr/bin/env bash
# security.sh - the decisions and reports of the security pipeline, kept out of
# the workflow so they are tested. Vendored to .github/cicd/security/security.sh;
# tested by tests/pipelines/security-test.sh. Every report is markdown on
# stdout. Exit 0: nothing to report; 1: findings; 2: bad input or usage.
#
#   security.sh range <base-sha> <head-sha>
#       "<base>..<head>" for gitleaks --log-opts when both are commits in this
#       checkout, else nothing: gitleaks given a range git cannot resolve scans
#       nothing and exits 0, so the caller scans the whole history instead.
#       Run inside the git checkout. Always exits 0.
#
#   security.sh gitleaks < gitleaks.json
#       Report of a gitleaks JSON report (run with --redact; secrets are never
#       printed). Exit 1 when it holds any finding.
#
#   security.sh osv <fixed|any|none> [root] < osv-scanner.json
#       Report of osv-scanner's JSON, one row per issue (aliases merged), with
#       [root] cut from the lockfile paths. Exit 1 when there are findings that
#       count: fixed = issues with a fix available, any = every issue, none =
#       never. A fix is available when the issue's OSV record names a fixed
#       version for that package, in any of its ranges, so it errs towards
#       failing.
#
#   security.sh semgrep < semgrep.json
#       Report of semgrep's JSON, plus the errors it logged. Exit 1 when it
#       holds any finding, 2 when an error is level "error" (the scan did not
#       complete, so no finding is not a pass).
#
#   security.sh severities <INFO|WARNING|ERROR>
#       The semgrep severities at or above that minimum, space-separated.
set -uo pipefail
die() { printf 'security.sh: %s\n' "$*" >&2; exit 2; }
MAX_ROWS=200
# jq helpers: a table cell (no newline, | escaped, cut to n chars) and the tail
# line for a table cut at MAX_ROWS.
JQ_DEFS='
def cell(n): tostring | gsub("[\r\n]+"; " ") | gsub("\\|"; "\\|") | if length > n then .[:n] + "..." else . end;
def more($n; $cap): if $n > $cap then "\n_... and \($n - $cap) more; the full report is in the artifact._" else empty end;
def more($n): more($n; $max);'
input() {  # input <jq-test> <what>: stdin into $IN, refused unless the test holds
  IN=$(cat)
  [[ -n $IN ]] || die "$2: empty input"
  jq -e "$1" >/dev/null 2>&1 <<<"$IN" || die "$2: not the JSON report expected"
}

range() {
  local base=${1:-} head=${2:-}
  if [[ -n $base && -n $head ]] && git rev-parse -q --verify "$base^{commit}" >/dev/null 2>&1 \
     && git rev-parse -q --verify "$head^{commit}" >/dev/null 2>&1; then
    echo "$base..$head"
  else
    echo "no usable PR range (base '${base}', head '${head}'); scan the whole history" >&2
  fi
}

gitleaks_report() {
  input 'type == "array"' gitleaks
  jq -r --argjson max "$MAX_ROWS" "$JQ_DEFS"'
    if length == 0 then "No secrets found."
    else
      "**\(length) possible secret(s).** Remove and rotate each one; for a false positive, add its fingerprint to `.gitleaksignore`.\n",
      "| Rule | File | Line | Commit | Fingerprint |", "|---|---|---|---|---|",
      (.[:$max][] | "| \(.RuleID | cell(60)) | \(.File | cell(120)) | \(.StartLine) | \(.Commit[:7]) | `\(.Fingerprint | cell(200))` |"),
      more(length)
    end' <<<"$IN"
  [[ $(jq length <<<"$IN") -eq 0 ]]
  return $?
}

osv_report() {
  local mode=${1:-} root=${2:-} n
  case $mode in fixed|any|none) ;; *) die "osv: mode must be fixed, any or none, not '$mode'" ;; esac
  input 'type == "object" and (.results | type) == "array"' osv
  # One row per group (an issue and its aliases) per package.
  local rows
  rows=$(jq -c --arg root "$root" '
    def low: tostring | ascii_downcase;
    def eco: low | split(":")[0];
    [ .results[] as $r | ($r.source.path // "" | ltrimstr($root)) as $src
      | $r.packages[]? as $p
      | $p.groups[]? as $g
      | [$p.vulnerabilities[]? | select(.id as $id | $g.ids | index($id))] as $vs
      | [$vs[].affected[]? | select((.package.name | low) == ($p.package.name | low)
                                    and (.package.ecosystem | eco) == ($p.package.ecosystem | eco))] as $mine
      | (if ($mine | length) > 0 then $mine else [$vs[].affected[]?] end) as $aff
      | { src: $src, name: $p.package.name, version: ($p.package.version // ""),
          ecosystem: ($p.package.ecosystem // ""), ids: $g.ids,
          severity: ($g.max_severity // ""),
          fixed: ([$aff[].ranges[]?.events[]? | .fixed // empty] | unique) }
    ] | sort_by((.fixed | length) == 0, .name, .ids[0])' <<<"$IN") || die "osv: could not read the report"
  jq -r --arg mode "$mode" --argjson max "$MAX_ROWS" "$JQ_DEFS"'
    (map(select(.fixed | length > 0)) | length) as $fixable
    | if length == 0 then "No known vulnerabilities."
      else
        "**\(length) known vulnerabilities, \($fixable) with a fix available.** Failing on: `\($mode)`.\n",
        "| Package | Version | Ecosystem | Vulnerability | Severity | Fixed in | Lockfile |", "|---|---|---|---|---|---|---|",
        (.[:$max][] | "| \(.name | cell(60)) | \(.version | cell(40)) | \(.ecosystem | cell(30)) | \(.ids | join(", ") | cell(80)) | \(.severity | cell(10)) | \(if (.fixed | length) > 0 then (.fixed | join(", ") | cell(60)) else "none" end) | \(.src | cell(120)) |"),
        more(length)
      end' <<<"$rows"
  case $mode in
    fixed) n=$(jq 'map(select(.fixed | length > 0)) | length' <<<"$rows") ;;
    any) n=$(jq length <<<"$rows") ;;
    none) n=0 ;;
  esac
  ((n == 0))
}

semgrep_report() {
  input 'type == "object" and (.results | type) == "array"' semgrep
  jq -r --argjson max "$MAX_ROWS" "$JQ_DEFS"'
    (if (.results | length) == 0 then "No findings."
     else
       "**\(.results | length) finding(s).** Fix them, or mark a false positive with a `nosemgrep` comment and a reason.\n",
       "| Severity | Rule | File | Line | Message |", "|---|---|---|---|---|",
       (.results[:$max][] | "| \(.extra.severity // "" | cell(10)) | \(.check_id | cell(100)) | \(.path | cell(120)) | \(.start.line) | \(.extra.message // "" | cell(160)) |"),
       more(.results | length)
     end),
    (if (.errors // [] | length) > 0 then
       ([.errors[] | select(.level == "error")] | length) as $fatal
       | "\nSemgrep logged \(.errors | length) problem(s), \($fatal) at level `error`: an `error` means the scan did not complete, a `warn` that a file was only partly parsed.",
         "<details><summary>Semgrep problems</summary>\n",
         (.errors[:50][] | (.path // .spans[0]?.file // "") as $p
          | "- `\(.level // "?")` \(.type | if type == "array" then .[0] else . end | cell(40))\(if $p != "" then " in " + ($p | cell(120)) else "" end): \(.message // "" | cell(160))"),
         more(.errors | length; 50),
         "\n</details>"
     else empty end)' <<<"$IN"
  # A scan that did not complete proves nothing, findings or not.
  jq -e 'any(.errors[]?; .level == "error")' >/dev/null <<<"$IN" && return 2
  [[ $(jq '.results | length' <<<"$IN") -eq 0 ]]
}

severities() {
  case ${1:-} in
    INFO) echo "INFO WARNING ERROR" ;;
    WARNING) echo "WARNING ERROR" ;;
    ERROR) echo "ERROR" ;;
    *) die "severities: the minimum must be INFO, WARNING or ERROR, not '${1:-}'" ;;
  esac
}

case ${1:-} in
  range) shift; range "$@" ;;
  gitleaks) gitleaks_report ;;
  osv) shift; osv_report "$@" ;;
  semgrep) semgrep_report ;;
  severities) shift; severities "$@" ;;
  *) die "usage: security.sh range <base> <head> | gitleaks | osv <fixed|any|none> [root] | semgrep | severities <min>" ;;
esac

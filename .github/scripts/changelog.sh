#!/usr/bin/env bash
# Print markdown release notes for <tag>: every commit since the previous release,
# grouped by conventional-commit type.
#
# Usage: changelog.sh <tag>
#
# Previous release = nearest earlier v* tag. For a final release (no "-" suffix)
# pre-release tags such as v1.0.0-rc.1 are skipped, so the notes cover everything
# since the last final release. Needs full history and tags (fetch-depth: 0).
set -euo pipefail

TAG="${1:?usage: changelog.sh <tag>}"
REPO="${GITHUB_REPOSITORY:-gjrud/steam-achievement-tracker}"

git rev-parse -q --verify "${TAG}^{commit}" >/dev/null || { echo "unknown tag: $TAG" >&2; exit 1; }

describe_args=(--tags --abbrev=0 --match 'v[0-9]*')
if [[ "$TAG" != *-* ]]; then
  describe_args+=(--exclude '*-*')
fi
PREV="$(git describe "${describe_args[@]}" "${TAG}^" 2>/dev/null || true)"

if [[ -n "$PREV" ]]; then
  RANGE="${PREV}..${TAG}"
else
  RANGE="$TAG"
fi

declare -A bucket=()
declare -A titles=(
  [breaking]="Breaking changes"
  [feat]="Features"
  [fix]="Bug fixes"
  [perf]="Performance"
  [refactor]="Refactoring"
  [docs]="Documentation"
  [test]="Tests"
  [build]="Build"
  [ci]="CI"
  [chore]="Chores"
  [other]="Other changes"
)
order=(breaking feat fix perf refactor docs test build ci chore other)

# --format (tformat) ends every record with a newline, so the last commit is read too.
# Hash first: a tab inside a subject cannot break the split.
while IFS=$'\t' read -r hash subject; do
  [[ -z "$subject" ]] && continue

  type="other"
  desc="$subject"
  breaking=""
  if [[ "$subject" =~ ^([a-z]+)(\(([^\)]+)\))?(!)?:[[:space:]]*(.+)$ ]]; then
    t="${BASH_REMATCH[1]}"
    scope="${BASH_REMATCH[3]}"
    breaking="${BASH_REMATCH[4]}"
    # Only known types are stripped; style:, revert: etc. keep their full subject.
    if [[ -n "${titles[$t]:-}" && "$t" != "breaking" && "$t" != "other" ]]; then
      type="$t"
      desc="${BASH_REMATCH[5]}"
      [[ -n "$scope" ]] && desc="**${scope}:** ${desc}"
    fi
  fi
  [[ -n "$breaking" ]] && type="breaking"

  bucket[$type]+="- ${desc} ([\`${hash:0:7}\`](https://github.com/${REPO}/commit/${hash}))"$'\n'
done < <(git log --no-merges --format='%H%x09%s' "$RANGE")

echo "## What's changed"
echo
found=0
for key in "${order[@]}"; do
  [[ -z "${bucket[$key]:-}" ]] && continue
  found=1
  echo "### ${titles[$key]}"
  echo
  printf '%s\n' "${bucket[$key]}"
done
[[ "$found" -eq 0 ]] && echo "No changes." && echo

if [[ -n "$PREV" ]]; then
  echo "**Full Changelog**: https://github.com/${REPO}/compare/${PREV}...${TAG}"
else
  echo "**Full Changelog**: https://github.com/${REPO}/commits/${TAG}"
fi

#!/usr/bin/env bash
# Compares UPSTREAM.md's last-ported SHA with upstream main; opens one issue per new head SHA.
set -euo pipefail
UPSTREAM=foivospro/athens-gtfs-realtime
last=$(sed -n 's/^last-ported: *\([0-9a-f]\{7,40\}\).*/\1/p' UPSTREAM.md)
[ -n "$last" ] || { echo "no last-ported SHA in UPSTREAM.md" >&2; exit 1; }
head=$(gh api "repos/$UPSTREAM/commits/main" --jq .sha)
if [ "${head:0:7}" = "${last:0:7}" ]; then
  echo "up to date at ${head:0:7}"; exit 0
fi
title="Upstream: new commits up to ${head:0:7}"
if gh issue list --state all --search "\"${head:0:7}\" in:title" --json title --jq '.[].title' | grep -qF "${head:0:7}"; then
  echo "issue for ${head:0:7} exists"; exit 0
fi
cmp=$(gh api "repos/$UPSTREAM/compare/${last}...${head}")
list=$(jq -r '.commits[] | "- [`\(.sha[0:7])`](\(.html_url)) \(.commit.message | split("\n")[0])"' <<<"$cmp")
files=$(jq -r '.files[]? | "- `\(.filename)` (+\(.additions) −\(.deletions))"' <<<"$cmp")
body=$(cat <<MD
Upstream [$UPSTREAM](https://github.com/$UPSTREAM) has $(jq '.total_commits' <<<"$cmp") new commit(s) since \`${last:0:7}\`, the last ported SHA in \`UPSTREAM.md\`.

**Diff:** https://github.com/$UPSTREAM/compare/${last}...${head}

### Commits
$list

### Files
$files

Port what applies (the HTML viewer is out of scope), then set \`last-ported: $head\` in \`UPSTREAM.md\`.
MD
)
gh label create upstream --color 0e8a16 --description "Upstream changes to port" >/dev/null 2>&1 || true
gh issue create --title "$title" --body "$body" --label upstream

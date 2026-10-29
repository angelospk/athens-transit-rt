#!/usr/bin/env bash
# Runs scripts/check-openapi-live.py; on a failure opens (or comments on) the live-check issue,
# on success closes it. Used by .github/workflows/live-check.yml.
set -uo pipefail
log=$(mktemp)
uv run scripts/check-openapi-live.py >"$log" 2>&1
status=$?
cat "$log"
open=$(gh issue list --label live-check --state open --json number --jq '.[0].number // empty')
if [ "$status" -eq 0 ]; then
  if [ -n "$open" ]; then
    gh issue close "$open" --comment "The live check passes again: ${RUN_URL:-local run}."
  fi
  exit 0
fi
problems=$(sed -n '/problem(s):$/,$p' "$log" | head -60)
body=$(cat <<MD
The daily check of \`https://transit.haroldpoi.dev\` against the OpenAPI spec and the /v2 data
rules failed: ${RUN_URL:-local run}

\`\`\`
$problems
\`\`\`

Run it yourself: \`uv run scripts/check-openapi-live.py\`. A metro Stop in capitals is missing
from \`internal/railgeo/stasy_stops.json\`: run \`scripts/stasy-stop-names.py\` and review the diff.
MD
)
gh label create live-check --color d93f0b --description "Daily check of the public instance failed" >/dev/null 2>&1 || true
if [ -n "$open" ]; then
  gh issue comment "$open" --body "$body"
else
  gh issue create --title "Live check failed on $(date -u +%F)" --body "$body" --label live-check
fi
exit "$status"

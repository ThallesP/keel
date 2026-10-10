#!/usr/bin/env bash
# Comments are banned in Go and TypeScript (CLAUDE.md, "Code rules"). Existing ones are counted in
# .comments-baseline and may only go down; files missing from it allow none.
#   scripts/check-comments.sh            fail if any file has more comments than its baseline
#   scripts/check-comments.sh --update   lower the baseline to today's counts (never raises it)
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
baseline=.comments-baseline

count() {
  git ls-files '*.go' '*.ts' '*.tsx' ':!:**/src/gen/**' ':!:*.d.ts' ':!:apps/fumadocs/**' |
    while read -r f; do
      [ -f "$f" ] || continue
      head -5 "$f" | grep -qE 'Code generated|@generated' && continue
      n=$(grep -E '^\s*(//|/\*|\*|\{/\*)|\S\s+//\s' "$f" |
        grep -cvE '//(go:|nolint|export |line )|// \+build|(eslint|oxlint|biome)-(disable|ignore)|@ts-(expect-error|ignore)|/// <reference|@vite-ignore|@jsx' || true)
      if [ "$n" -gt 0 ]; then echo "$n $f"; fi
    done
}

if [ "${1:-}" = "--init" ] && [ ! -f "$baseline" ]; then
  count | sort -k2 > "$baseline"
  exit 0
fi

if [ "${1:-}" = "--update" ]; then
  count | awk -v base="$baseline" '
    BEGIN { while ((getline l < base) > 0) { split(l, p, " "); old[p[2]] = p[1] } }
    ($2 in old) { print ($1 < old[$2] ? $1 : old[$2]), $2 }' | sort -k2 > "$baseline.tmp"
  mv "$baseline.tmp" "$baseline"
  echo "baseline: $(awk '{s += $1} END {print s + 0}' "$baseline") comment lines in $(wc -l < "$baseline") files"
  exit 0
fi

fail=0
while read -r n f; do
  allowed=$(awk -v f="$f" '$2 == f { print $1 }' "$baseline")
  if [ "$n" -gt "${allowed:-0}" ]; then
    echo "$f: $n comment lines, ${allowed:-0} allowed"
    fail=1
  fi
done < <(count)
if [ "$fail" = 1 ]; then
  echo
  echo "Comments are banned (CLAUDE.md, \"Code rules\"). Say it with a name, a type or a test, or put"
  echo "the design reason in docs/ or the commit message. After removing comments, run"
  echo "scripts/check-comments.sh --update to lock in the lower count."
  exit 1
fi

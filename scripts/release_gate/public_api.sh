#!/usr/bin/env bash
# Public-surface snapshot (ENG-18798). Lists the module's public API from its source, which is what
# a Go release ships (the tag is the release), and compares it with the committed `public-api.txt`.
#
#   scripts/release_gate/public_api.sh           check: fail on any difference (CI, `prepublish-surface`)
#   scripts/release_gate/public_api.sh --write   regenerate public-api.txt after a deliberate API change
#
# Any difference fails, additions included, so the snapshot moves in the same PR as the code. A
# removed or changed item then shows up as a `-` line in the diff a reviewer reads, instead of first
# surfacing in a consumer's build, after the proxy has cached the tag for good.
#
# The lister is scripts/release_gate/surface: go/parser and go/ast, stdlib only, in a module of its
# own so it stays out of the SDK's module zip. Its header says what a line holds and what it does
# not see. Its tests run first, so a broken lister fails as a broken lister, not as a diff.
#
# It reads the working tree, so `--write` works on uncommitted edits. In CI the tree is the PR's
# merge commit and clean.
set -euo pipefail

SNAPSHOT="public-api.txt"

mode="check"
case "${1:-}" in
  "") ;;
  --write) mode="write" ;;
  *) echo "usage: $0 [--write]" >&2; exit 64 ;;
esac

root="$(git rev-parse --show-toplevel)"
cd "$root"

# First, before the lister builds anything, so a forgotten snapshot fails in seconds.
if [ "$mode" = "check" ] && [ ! -f "$SNAPSHOT" ]; then
  echo "::error title=prepublish-surface::$SNAPSHOT is missing. Run scripts/release_gate/public_api.sh --write and commit it."
  exit 1
fi

lister="$root/scripts/release_gate/surface"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# GOWORK=off: a go.work above the checkout must not change what either module resolves.
export GOWORK=off
(cd "$lister" && go test -count=1 ./...) > "$work/lister-test.txt" 2>&1 || {
  cat "$work/lister-test.txt"
  echo "::error title=prepublish-surface::The public-surface lister's own tests fail (scripts/release_gate/surface), so no listing was compared."
  exit 1
}
(cd "$lister" && go run . "$root") > "$work/public-api.txt" || {
  echo "::error title=prepublish-surface::The public-surface lister passed its own tests but failed on this tree (scripts/release_gate/surface), so no listing was compared. The error is above."
  exit 1
}

if [ "$mode" = "write" ]; then
  cp "$work/public-api.txt" "$SNAPSHOT"
  echo "wrote $SNAPSHOT ($(wc -l < "$SNAPSHOT") items)"
  exit 0
fi

if diff -u --label "$SNAPSHOT (committed)" --label "$SNAPSHOT (this tree)" \
  "$SNAPSHOT" "$work/public-api.txt" > "$work/diff.txt"; then
  echo "public surface matches $SNAPSHOT ($(wc -l < "$SNAPSHOT") items)"
  exit 0
fi

removed="$(grep -c '^-[^-]' "$work/diff.txt" || true)"
added="$(grep -c '^+[^+]' "$work/diff.txt" || true)"
cat "$work/diff.txt"
echo "::error title=prepublish-surface::The module's public API differs from $SNAPSHOT: ${removed} item(s) gone or changed, ${added} new. If that is deliberate, run scripts/release_gate/public_api.sh --write and commit $SNAPSHOT in this PR, so the change is in the diff a reviewer reads. A \"-\" line is breaking and needs a \"!\" PR title. A \"+\" line can be too, if it adds a method to an interface callers implement."
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "### Public surface: ❌ differs from \`$SNAPSHOT\`"
    echo
    echo "${removed} item(s) gone or changed (\`-\`), ${added} new (\`+\`). Regenerate with \`scripts/release_gate/public_api.sh --write\` if deliberate."
    echo
    echo '```diff'
    cat "$work/diff.txt"
    echo '```'
  } >> "$GITHUB_STEP_SUMMARY"
fi
exit 1

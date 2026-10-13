#!/usr/bin/env bash
# Pre-publish smoke test (ENG-18798). Builds a fresh consumer of the SDK (scripts/release_gate/smoke)
# from a clean copy of the committed tree, and runs one unauthenticated read against the public
# testnet with it. No keys, no writes.
#
#   scripts/release_gate/smoke.sh               build, check the outcomes, read
#   scripts/release_gate/smoke.sh --build-only  build and check the outcomes, no testnet read (PRs that are not a release)
#
# A Go release has no package to unpack: the tag is the release, and the proxy serves the tagged
# tree. So the copy is `git archive HEAD`, the committed tree and nothing else: no untracked or
# modified file can leak into the build. In CI HEAD is the PR's merge commit; locally it is your
# last commit, so commit before running this. The consumer's go.work uses that copy as the SDK.
#
# Exit codes, kept apart on purpose: 0 passed, 1 failed, 2 testnet unreachable. The workflow fails
# on 1 and on 2, under different names. Unreachable is not a pass, and it is not the SDK's fault:
# re-run the job once testnet answers.
#
# The outcomes are exercised, not only asserted. In both modes, right after the build and before any
# testnet read, the built smoke runs twice with NEXUS_SMOKE_BASE_URL overridden:
#   http://127.0.0.1:1, where nothing listens, must exit 2 (unreachable, never a pass);
#   stub_server.py, which answers every request with an empty list, must exit 1 (failed).
# A wrong code fails this script as `prepublish-smoke (outcomes)`. Those runs call the binary
# directly, so they raise none of the annotations below, which their codes would otherwise trigger.
set -euo pipefail

mode="read"
case "${1:-}" in
  "") ;;
  --build-only) mode="build" ;;
  *) echo "usage: $0 [--build-only]" >&2; exit 64 ;;
esac

root="$(git rev-parse --show-toplevel)"
cd "$root"
commit="$(git rev-parse --short HEAD)"

work="$(mktemp -d)"
stub_pid=""
cleanup() {
  if [ -n "$stub_pid" ]; then
    kill "$stub_pid" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

summary() {
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf '### Testnet smoke: %s\n\n%s\n' "$1" "$2" >> "$GITHUB_STEP_SUMMARY"
  fi
}

mkdir "$work/sdk"
git archive --format=tar HEAD | tar -x -C "$work/sdk"
consumer="$work/sdk/scripts/release_gate/smoke"
# The archived go.work, so the SDK it uses is the archived tree, whatever go.work sits above $work.
export GOWORK="$consumer/go.work"
if ! (cd "$consumer" && go vet ./... && go build -o "$work/smoke" .); then
  echo "::error title=prepublish-smoke (build failed)::A clean consumer of the committed SDK did not vet or build, so nothing was read. The error is above."
  summary "❌ BUILD FAILED: nothing was read" "A clean consumer of \`${commit}\` did not vet or build."
  exit 1
fi
echo "built a consumer of the SDK at ${commit} (git archive HEAD)"

# The outcome checks. Each runs the built smoke directly and compares its exit code.
expect() {
  local name="$1" base="$2" want="$3" line got
  set +e
  line="$(NEXUS_SMOKE_BASE_URL="$base" "$work/smoke")"
  got=$?
  set -e
  if [ "$got" != "$want" ]; then
    echo "::error title=prepublish-smoke (outcomes)::${name}: the smoke exited ${got}, not ${want}, so its outcomes cannot be trusted. ${line}"
    summary "❌ OUTCOME CHECK FAILED" "${name}: exited ${got}, not ${want}. ${line}"
    exit 1
  fi
  echo "outcome check, ${name}: exited ${got} as it must (${line})"
}

expect "nothing listening" "http://127.0.0.1:1" 2

python3 -I "$work/sdk/scripts/release_gate/stub_server.py" > "$work/stub.port" &
stub_pid=$!
port=""
for _ in $(seq 50); do
  port="$(head -n 1 "$work/stub.port")"
  [ -n "$port" ] && break
  sleep 0.1
done
if [ -z "$port" ]; then
  echo "::error title=prepublish-smoke (outcomes)::stub_server.py printed no port, so the failed outcome was not checked."
  exit 1
fi
expect "a server with no markets" "http://127.0.0.1:${port}" 1
kill "$stub_pid" 2>/dev/null || true
stub_pid=""

if [ "$mode" = "build" ]; then
  echo "::notice title=prepublish-smoke (read not attempted)::Not a release PR: a clean consumer of the committed tree built, and its failed and unreachable outcomes were checked against local stand-ins. No testnet read was made. The read runs on the release PR."
  summary "build only" "Not a release PR: a clean consumer of \`${commit}\` built and its failed and unreachable outcomes were checked locally. No testnet read was attempted, so this is not a smoke pass."
  exit 0
fi

set +e
line="$("$work/smoke")"
code=$?
set -e
echo "$line"
case "$code" in
  0)
    summary "✅ passed" "$line"
    ;;
  2)
    echo "::error title=prepublish-smoke (testnet unreachable)::NOT a pass: the testnet read got no usable answer, so nothing about this release was verified. Re-run this job once testnet answers. ${line}"
    summary "⚠️ TESTNET UNREACHABLE: not a pass" "$line"
    ;;
  *)
    echo "::error title=prepublish-smoke (failed)::The committed SDK could not make an unauthenticated testnet read. ${line}"
    summary "❌ FAILED" "$line"
    code=1
    ;;
esac
exit "$code"

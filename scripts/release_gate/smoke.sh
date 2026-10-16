#!/usr/bin/env bash
# Pre-publish smoke test (ENG-18798). Builds a fresh consumer of the SDK (scripts/release_gate/smoke)
# from a clean copy of the committed tree, and runs one unauthenticated read against the public
# testnet with it. No keys, no writes.
#
#   scripts/release_gate/smoke.sh               build, check exits 2 and 1, read
#   scripts/release_gate/smoke.sh --build-only  build, check exits 2 and 1, no testnet read (PRs that are not a release)
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
# Exits 2 and 1 are exercised on every run, not only asserted. In both modes, right after the build
# and before any testnet read, the built smoke runs three times with NEXUS_SMOKE_BASE_URL overridden:
#   http://127.0.0.1:1, where nothing listens, must exit 2 (unreachable, never a pass);
#   `stub_server.py 503`, a server in an outage, must exit 2 (unreachable, never failed or passed);
#   stub_server.py, which answers every request with an empty list, must exit 1 (failed).
# Each must also print the line for that case (main.go has one per path: five end in 1, two in 2),
# so a probe can't pass on the right code for another reason. Exit 0 is exercised only by the real
# read, which runs on the release PR and on a change to this gate. Each probe runs under
# `timeout 180` (above the smoke's own 2-minute deadline, which bounds the SDK's retries); a wrong
# code, a wrong line, or a probe that hits the timeout, fails this script as
# `prepublish-smoke (outcomes)`, naming the case. Without GNU `timeout` or `gtimeout` (macOS has
# neither by default) the probes run with no time limit, and say so. The probes call the binary directly
# and keep both its stdout and stderr, so they raise none of the annotations below; each prints one
# plain log line instead: the case, the exit code and the first line of the smoke's output.
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

# The probes' time limit: GNU `timeout` (CI), else Homebrew's `gtimeout`, else none.
limit=()
if command -v timeout > /dev/null; then
  limit=(timeout 180)
elif command -v gtimeout > /dev/null; then
  limit=(gtimeout 180)
else
  echo "note: neither timeout nor gtimeout is installed, so the outcome checks run with no time limit"
fi

# The outcome checks. Each runs the built smoke directly, both output streams captured, under a
# hard timeout when there is one, and compares its exit code, then the start of its first line.
expect() {
  local name="$1" base="$2" want="$3" want_line="$4" out got first
  set +e
  out="$(NEXUS_SMOKE_BASE_URL="$base" ${limit[@]+"${limit[@]}"} "$work/smoke" 2>&1)"
  got=$?
  set -e
  first="${out%%$'\n'*}"
  if [ "$got" = 124 ] && [ "${#limit[@]}" -gt 0 ]; then
    echo "::error title=prepublish-smoke (outcomes)::${name}: the smoke gave no outcome within 180 s, so its outcomes cannot be trusted."
    summary "❌ OUTCOME CHECK FAILED" "${name}: no outcome within 180 s."
    exit 1
  fi
  if [ "$got" != "$want" ]; then
    echo "::error title=prepublish-smoke (outcomes)::${name}: the smoke exited ${got}, not ${want}, so its outcomes cannot be trusted. First line: ${first}"
    summary "❌ OUTCOME CHECK FAILED" "${name}: exited ${got}, not ${want}. ${first}"
    exit 1
  fi
  if [[ "$first" != "$want_line"* ]]; then
    echo "::error title=prepublish-smoke (outcomes)::${name}: the smoke exited ${got} as it must, but for another reason: its first line does not start with '${want_line}', so its outcomes cannot be trusted. First line: ${first}"
    summary "❌ OUTCOME CHECK FAILED" "${name}: exited ${got} for another reason. ${first}"
    exit 1
  fi
  echo "outcome check, ${name}: exit ${got} and its line, as it must; first line: ${first}"
}

# Starts stub_server.py with its arguments in the background and sets stub_pid and port.
start_stub() {
  # Created first, so the read below never meets a missing file while the stub is starting.
  : > "$work/stub.port"
  python3 -I "$work/sdk/scripts/release_gate/stub_server.py" "$@" > "$work/stub.port" &
  stub_pid=$!
  # Wait for the first line to end (a newline in the file), or for the stub to die, so the read
  # below never takes a port the stub is still writing.
  for _ in $(seq 50); do
    if [ "$(wc -l < "$work/stub.port")" -ge 1 ] || ! kill -0 "$stub_pid" 2>/dev/null; then break; fi
    sleep 0.1
  done
  port="$(head -n 1 "$work/stub.port")"
  if ! [[ "$port" =~ ^[0-9]+$ ]]; then
    echo "::error title=prepublish-smoke (outcomes)::stub_server.py $* printed no port (got '${port}'), so that outcome was not checked."
    exit 1
  fi
}

stop_stub() {
  kill "$stub_pid" 2>/dev/null || true
  wait "$stub_pid" 2>/dev/null || true
  stub_pid=""
}

expect "nothing listening" "http://127.0.0.1:1" 2 \
  "smoke: unreachable: http://127.0.0.1:1 gave no usable answer: "

start_stub 503
expect "a server answering 503" "http://127.0.0.1:${port}" 2 \
  "smoke: unreachable: http://127.0.0.1:${port} answered HTTP 503, "
stop_stub

start_stub
expect "a server with no markets" "http://127.0.0.1:${port}" 1 \
  "smoke: failed: FetchMarketsSummary decoded an EMPTY list from http://127.0.0.1:${port}"
stop_stub

if [ "$mode" = "build" ]; then
  echo "::notice title=prepublish-smoke (read not attempted)::Not a release PR: a clean consumer of the committed tree built, and its exits 2 (unreachable) and 1 (failed) were checked against local stand-ins. Exit 0 was not exercised: no testnet read was made. The read runs on the release PR."
  summary "build only" "Not a release PR: a clean consumer of \`${commit}\` built and its exits 2 and 1 were checked against local stand-ins. No testnet read was attempted, so exit 0 was not exercised and this is not a smoke pass."
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

#!/usr/bin/env bash
#
# install-agent-selection.sh — check which archive install-agent.sh unpacks.
#
# Sources the installer for its functions and feeds them stand-in downloads, so it needs no
# root, no systemd and no network. It pins the choice that decides whether the one-liner the
# Add agent window shows works: the latest release is used when it ships the agent, dev-latest
# takes over when it does not, an archive or tag chosen with --tarball or --version is never
# swapped, and an archive that cannot be read is an error, not a missing agent.
#
# Usage: bash deploy/test/install-agent-selection.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# shellcheck source=../../install-agent.sh
source "$ROOT/install-agent.sh"

# What the installer would learn from the machine and from GitHub.
arch() { echo 'amd64'; }
resolve_latest_tag() { echo 'v9.9.9'; }

latest_archive="" dev_archive=""
curl() {
    local out="" url=""
    while [[ $# -gt 0 ]]; do
        case "$1" in
            -o)
                out="$2"
                shift 2
                ;;
            -w)
                shift 2
                ;;
            http*)
                url="$1"
                shift
                ;;
            *) shift ;;
        esac
    done
    echo "$url" >> "$TMP/calls"
    case "$url" in
        *.sha256) printf '404' ;;
        */dev-latest/*) cp "$dev_archive" "$out" ;;
        *) cp "$latest_archive" "$out" ;;
    esac
}

mkdir -p "$TMP/with/x-ui/bin" "$TMP/without/x-ui/bin"
echo 'agent' > "$TMP/with/x-ui/x-ui-agent"
echo 'core' > "$TMP/with/x-ui/bin/xray-linux-amd64"
echo 'core' > "$TMP/without/x-ui/bin/xray-linux-amd64"
tar -czf "$TMP/with.tgz" -C "$TMP/with" x-ui
tar -czf "$TMP/without.tgz" -C "$TMP/without" x-ui
head -c 60 "$TMP/with.tgz" > "$TMP/cut.tgz"

failures=0
fail() {
    echo "FAIL: $*" >&2
    failures=$((failures + 1))
}

# attempt runs select_archive the way install_agent does, in a subshell because the installer
# exits on a refusal. It leaves the output in $out, the status in $rc and the downloads in $calls.
attempt() {
    : > "$TMP/calls"
    work=$(mktemp -d "$TMP/work.XXXXXX")
    rc=0
    out=$( (select_archive && echo "RESULT tag=${tag} archive=${archive##*/}") 2>&1) || rc=$?
    calls=$(grep -c . "$TMP/calls" || true)
}

want_ok() {
    [[ $rc -eq 0 ]] || fail "$1: exited $rc, want success: $out"
}
want_refused() {
    [[ $rc -ne 0 ]] || fail "$1: succeeded, want a refusal: $out"
}
want_text() {
    [[ "$out" == *"$2"* ]] || fail "$1: output lacks '$2': $out"
}
want_no_text() {
    [[ "$out" != *"$2"* ]] || fail "$1: output has '$2': $out"
}
want_downloads() {
    [[ "$calls" == "$2" ]] || fail "$1: $calls requests, want $2: $(cat "$TMP/calls")"
}
reset() {
    tag="" tag_given=0 tarball="" archive=""
}

name="the latest release ships the agent"
reset
latest_archive="$TMP/with.tgz" dev_archive="$TMP/without.tgz"
attempt
want_ok "$name"
want_text "$name" "RESULT tag=v9.9.9"
want_no_text "$name" "dev-latest"
want_downloads "$name" 2

name="the latest release predates the agent"
reset
latest_archive="$TMP/without.tgz" dev_archive="$TMP/with.tgz"
attempt
want_ok "$name"
want_text "$name" "does not ship x-ui-agent yet"
want_text "$name" "RESULT tag=dev-latest"
want_downloads "$name" 4

name="a version asked for lacks the agent"
reset
tag="v1.0.0" tag_given=1
latest_archive="$TMP/without.tgz" dev_archive="$TMP/with.tgz"
attempt
want_refused "$name"
want_text "$name" "does not ship x-ui-agent"
want_no_text "$name" "installed instead"
want_downloads "$name" 2

name="a version asked for ships the agent"
reset
tag="v1.0.0" tag_given=1
latest_archive="$TMP/with.tgz" dev_archive="$TMP/without.tgz"
attempt
want_ok "$name"
want_text "$name" "RESULT tag=v1.0.0"

name="an archive given lacks the agent"
reset
tarball="$TMP/without.tgz"
latest_archive="$TMP/without.tgz" dev_archive="$TMP/with.tgz"
attempt
want_refused "$name"
want_text "$name" "does not ship x-ui-agent"
want_downloads "$name" 0

name="an archive given ships the agent"
reset
tarball="$TMP/with.tgz"
attempt
want_ok "$name"
want_text "$name" "RESULT tag= archive=with.tgz"
want_downloads "$name" 0

name="dev-latest lacks the agent too"
reset
latest_archive="$TMP/without.tgz" dev_archive="$TMP/without.tgz"
attempt
want_refused "$name"
want_text "$name" "dev-latest archive does not ship x-ui-agent either"

name="an archive that is cut short"
reset
latest_archive="$TMP/cut.tgz" dev_archive="$TMP/with.tgz"
attempt
want_refused "$name"
want_text "$name" "Cannot read the release archive"
want_no_text "$name" "installed instead"
want_downloads "$name" 2

# Sourcing installed nothing, and running the file still runs it however it is started.
name="--help from a file"
out=$(bash "$ROOT/install-agent.sh" --help) || fail "$name: exited $?"
want_text "$name" "Usage: install-agent.sh"
name="--help piped into bash"
out=$(bash -s -- --help < "$ROOT/install-agent.sh") || fail "$name: exited $?"
want_text "$name" "Usage: install-agent.sh"
name="--help from a process substitution"
out=$(bash <(cat "$ROOT/install-agent.sh") --help) || fail "$name: exited $?"
want_text "$name" "Usage: install-agent.sh"

if [[ $failures -gt 0 ]]; then
    echo "$failures check(s) failed" >&2
    exit 1
fi
echo "install-agent.sh archive selection: all checks passed"

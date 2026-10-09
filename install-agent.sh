#!/usr/bin/env bash
# Installs, updates or removes the x-ui node agent (see docs/node-agent.md):
#
#   bash <(curl -fsSL https://raw.githubusercontent.com/kuzzrus/3x-ui-awg/main/install-agent.sh)
#
# The pairing bundle holds the agent's private key and secret, so it is read from a
# silent prompt, a file or stdin, and never from a command-line argument.
set -euo pipefail

REPO="kuzzrus/3x-ui-awg"
INSTALL_DIR="/usr/local/x-ui-agent"
STATE_DIR="/etc/x-ui-agent"
LOG_DIR="/var/log/x-ui-agent"
UNIT_FILE="/etc/systemd/system/x-ui-agent.service"
STAGING_PARENT="/usr/local"

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'

die() {
    echo -e "${red}$*${plain}" >&2
    exit 1
}
info() { echo -e "${green}$*${plain}"; }
warn() { echo -e "${yellow}$*${plain}" >&2; }

usage() {
    cat << 'EOF'
Usage: install-agent.sh [options]

  --bundle-file PATH  read the pairing bundle from PATH, or from stdin when PATH is -.
                      Without it the bundle is pasted at a silent prompt, and an
                      update keeps the bundle already installed.
  --version TAG       install this release tag (default: the latest stable release, or
                      dev-latest while that release does not ship the agent yet;
                      dev-latest follows the per-commit builds).
  --tarball PATH      install from an x-ui-linux-<arch>.tar.gz you downloaded yourself.
  --uninstall         stop the agent and remove everything it installed.
  --yes, -y           do not ask before uninstalling.
  --help, -h          show this text.
EOF
}

arch() {
    case "$(uname -m)" in
        x86_64 | x64 | amd64) echo 'amd64' ;;
        i*86 | x86) echo '386' ;;
        armv8* | arm64 | aarch64) echo 'arm64' ;;
        armv7* | arm) echo 'armv7' ;;
        armv6*) echo 'armv6' ;;
        armv5*) echo 'armv5' ;;
        s390x) echo 's390x' ;;
        *) die "Unsupported CPU architecture: $(uname -m)" ;;
    esac
}

# The panel binary maps GOARCH=arm to "arm32", and the agent looks for the core the same way.
core_name() {
    case "$(arch)" in
        armv5 | armv6 | armv7) echo 'xray-linux-arm32' ;;
        *) echo "xray-linux-$(arch)" ;;
    esac
}

require_system() {
    [[ $EUID -eq 0 ]] || die "Run this as root."
    [[ -d /run/systemd/system ]] || die "The agent installer needs systemd."
    local tool
    for tool in curl tar sha256sum install; do
        command -v "$tool" > /dev/null || die "Missing tool: $tool"
    done
}

resolve_latest_tag() {
    local url tag
    url=$(curl -sSLI -o /dev/null -w '%{url_effective}' --retry 5 --retry-delay 3 --connect-timeout 15 --max-time 60 "https://github.com/${REPO}/releases/latest" 2> /dev/null || true)
    tag=${url##*/tag/}
    if [[ "$tag" != "$url" && -n "$tag" && "$tag" != "latest" ]]; then
        echo "$tag"
        return 0
    fi
    curl -Ls --retry 5 --retry-delay 3 --connect-timeout 15 --max-time 60 "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/'
}

# A mismatch or a failed sidecar download aborts the install; only a 404 (releases
# that predate the sidecar) is tolerated with a warning.
verify_checksum() {
    local url="$1" file="$2" sums="$2.sha256" code expected actual
    code=$(curl -sL --retry 3 --retry-delay 3 --connect-timeout 15 --max-time 60 -o "$sums" -w '%{http_code}' "${url}.sha256")
    if [[ "$code" == "404" ]]; then
        warn "No checksum published for this release, skipping verification"
        return 0
    fi
    [[ "$code" == "200" ]] || die "Failed to download the checksum (HTTP ${code})"
    expected=$(awk 'NR == 1 {print $1}' "$sums")
    actual=$(sha256sum "$file" | awk '{print $1}')
    if [[ ! "$expected" =~ ^[0-9a-f]{64}$ || "$expected" != "$actual" ]]; then
        die "Checksum mismatch: expected ${expected:-<none>}, got ${actual}"
    fi
    info "Checksum verified: ${actual}"
}

# Sets $archive to the file to unpack: the one given with --tarball, or a download into
# the staging folder.
fetch_archive() {
    local url
    if [[ -n "$tarball" ]]; then
        [[ -r "$tarball" ]] || die "Cannot read ${tarball}"
        archive="$tarball"
        return 0
    fi
    [[ -n "$tag" ]] || tag=$(resolve_latest_tag || true)
    [[ -n "$tag" ]] || die "Could not work out the latest release"
    url="https://github.com/${REPO}/releases/download/${tag}/x-ui-linux-$(arch).tar.gz"
    info "Downloading ${tag} for $(arch)"
    archive="$work/x-ui.tar.gz"
    curl -fLR --retry 5 --retry-delay 3 --connect-timeout 15 --speed-limit 1 --speed-time 300 -o "$archive" "$url"
    verify_checksum "$url" "$archive"
}

# The release carries the panel, sidecar binaries and more, but the agent needs three
# kinds of entry: GNU tar unpacks only those, any other tar unpacks all.
unpack_archive() {
    if tar --version 2> /dev/null | grep -q 'GNU tar'; then
        tar -xzf "$archive" -C "$work" --wildcards 'x-ui/x-ui-agent' "x-ui/bin/xray-linux-$(arch)" 'x-ui/bin/*.dat'
    else
        tar -xzf "$archive" -C "$work"
    fi
}

# Unpacks the archive from scratch and succeeds only if it shipped the agent. With "quiet" tar's
# complaint about a missing entry is left out, for a caller that has another archive to try.
unpack_agent() {
    rm -rf "$work/x-ui"
    if [[ "${1:-}" == quiet ]]; then
        unpack_archive 2> /dev/null || true
    else
        unpack_archive || true
    fi
    [[ -s "$work/x-ui/x-ui-agent" ]]
}

# Puts the bundle in $1 with owner-only permissions: read it from the prompt, a file or stdin.
read_bundle() {
    local dest="$1" pasted
    (umask 077 && : > "$dest")
    case "$bundle_file" in
        "")
            [[ -t 0 ]] || die "No bundle given. Run this in a terminal to paste it, or pass --bundle-file."
            printf 'Paste the pairing bundle shown by the master and press Enter (nothing is echoed): ' > /dev/tty
            IFS= read -rs pasted < /dev/tty
            echo > /dev/tty
            printf '%s\n' "$pasted" > "$dest"
            ;;
        -) cat > "$dest" ;;
        *)
            [[ -r "$bundle_file" ]] || die "Cannot read ${bundle_file}"
            cat "$bundle_file" > "$dest"
            ;;
    esac
}

write_unit() {
    cat > "$UNIT_FILE" << EOF
[Unit]
Description=x-ui node agent
Documentation=https://github.com/${REPO}/blob/main/docs/node-agent.md
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/x-ui-agent -bundle-file ${STATE_DIR}/bundle -state-dir ${STATE_DIR} -bin-dir ${INSTALL_DIR}/bin -log-dir ${LOG_DIR}
Restart=always
RestartSec=3
LimitNOFILE=1048576
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true

[Install]
WantedBy=multi-user.target
EOF
}

# Replaces a file the running agent may have open without rewriting it in place.
put_binary() {
    install -m 0755 "$1" "$2.new"
    mv -f "$2.new" "$2"
}

cleanup() {
    if [[ -n "$work" ]]; then
        rm -rf "$work"
    fi
}

install_agent() {
    local agent_bin address
    # On the real filesystem, not /tmp: that is a RAM-backed tmpfs on many systemd distros,
    # too small on a low-memory node for the release archive plus what it unpacks.
    work=$(mktemp -d "${STAGING_PARENT}/.x-ui-agent-install.XXXXXX")
    trap cleanup EXIT

    fetch_archive
    agent_bin="$work/x-ui/x-ui-agent"
    if ! unpack_agent quiet; then
        # Only a release that came from "latest" is swapped: a tag or an archive the operator
        # chose is installed as given or not at all.
        if [[ -n "$tarball" || $tag_given -eq 1 ]]; then
            unpack_agent || true # again, aloud, so that tar says what is wrong
            die "Could not unpack the release archive. It may not ship x-ui-agent: try --version dev-latest, or a newer release."
        fi
        warn "The latest release (${tag}) does not ship x-ui-agent yet, so the rolling dev-latest build is installed instead."
        tag="dev-latest"
        fetch_archive
        unpack_agent || die "Could not unpack the dev-latest archive, or it does not ship x-ui-agent either."
    fi
    [[ -s "$work/x-ui/bin/xray-linux-$(arch)" ]] || die "The archive has no Xray core for $(arch)."
    chmod +x "$agent_bin"

    # Everything is checked before the running agent is touched, so a typo in the bundle
    # or a broken download leaves a working install alone.
    if [[ -n "$bundle_file" || ! -s "$STATE_DIR/bundle" ]]; then
        read_bundle "$work/bundle"
        address=$("$agent_bin" -check-bundle -bundle-file "$work/bundle") ||
            die "That is not a valid pairing bundle. Copy it again from the master."
    else
        address=$("$agent_bin" -check-bundle -bundle-file "$STATE_DIR/bundle") ||
            die "The installed bundle is unreadable. Pass a new one with --bundle-file."
    fi

    if systemctl is-active --quiet x-ui-agent; then
        info "Stopping the running agent"
        systemctl stop x-ui-agent
    fi

    install -d -m 0755 "$INSTALL_DIR" "$INSTALL_DIR/bin"
    install -d -m 0700 "$STATE_DIR"
    install -d -m 0750 "$LOG_DIR"
    put_binary "$agent_bin" "$INSTALL_DIR/x-ui-agent"
    put_binary "$work/x-ui/bin/xray-linux-$(arch)" "$INSTALL_DIR/bin/$(core_name)"
    local geo
    for geo in "$work"/x-ui/bin/*.dat; do
        if [[ -e "$geo" ]]; then
            install -m 0644 "$geo" "$INSTALL_DIR/bin/"
        fi
    done
    if [[ -s "$work/bundle" ]]; then
        install -m 0600 "$work/bundle" "$STATE_DIR/bundle"
    fi

    write_unit
    systemctl daemon-reload
    systemctl enable --now x-ui-agent > /dev/null

    info "The x-ui agent is running and listens on ${address##*:}/tcp."
    echo "Open that port in the firewall of this host, then add the node on the master."
    echo "Status: systemctl status x-ui-agent    Logs: journalctl -u x-ui-agent -f"
}

uninstall_agent() {
    local answer
    if [[ $assume_yes -eq 0 ]]; then
        [[ -t 0 ]] || die "Pass --yes to uninstall without a terminal."
        read -rp "Remove the x-ui agent, its pairing bundle and its logs from this host? [y/N] " answer
        [[ "$answer" == [yY]* ]] || die "Nothing was removed."
    fi
    systemctl disable --now x-ui-agent 2> /dev/null || true
    rm -f "$UNIT_FILE"
    systemctl daemon-reload
    rm -rf "$INSTALL_DIR" "$STATE_DIR" "$LOG_DIR"
    info "The x-ui agent is removed. Delete the node on the master as well."
}

bundle_file="" tag="" tag_given=0 tarball="" archive="" work="" do_uninstall=0 assume_yes=0
while [[ $# -gt 0 ]]; do
    case "$1" in
        --bundle-file)
            bundle_file="${2:?--bundle-file needs a path, or - for stdin}"
            shift 2
            ;;
        --version)
            tag="${2:?--version needs a release tag}"
            tag_given=1
            shift 2
            ;;
        --tarball)
            tarball="${2:?--tarball needs a path}"
            shift 2
            ;;
        --uninstall)
            do_uninstall=1
            shift
            ;;
        --yes | -y)
            assume_yes=1
            shift
            ;;
        -h | --help)
            usage
            exit 0
            ;;
        --bundle | --bundle=*)
            die "The bundle is a credential. Paste it at the prompt or use --bundle-file, not an argument."
            ;;
        *) die "Unknown option: $1 (see --help)" ;;
    esac
done

require_system
if [[ $do_uninstall -eq 1 ]]; then
    uninstall_agent
else
    install_agent
fi

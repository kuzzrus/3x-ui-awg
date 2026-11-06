#!/usr/bin/env bash
# warpwp.sh
# Единый менеджер WARP + wireproxy + 3x-ui helper.

set -Eeuo pipefail

VERSION="1.3.9"
REPO_SLUG="kuzzrus/WARP_WireProxy_Manager"
GITHUB_API="https://api.github.com/repos/$REPO_SLUG"
RELEASE_DOWNLOAD_BASE="https://github.com/$REPO_SLUG/releases/download"
RELEASE_SIGNING_ID="warpwp-release"
RELEASE_SIGNING_NAMESPACE="warpwp-release"
# Публичный ключ подписи release-артефактов. Закрытый ключ хранится только в
# GitHub Actions secret WARPWP_RELEASE_SIGNING_KEY и никогда не попадает в repo.
RELEASE_SIGNING_PUBLIC_KEY="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEa+mDJ1BJ6w2YdAogupkcdL8MJLo2XjMJlPT9WyQyA3"

MANAGER_BIN="/usr/local/bin/warpwp"
NATIVE_BIN="/usr/local/bin/warp-wireproxy-native.sh"
WG_DIR="${WG_DIR:-/etc/wireguard}"
CRON_FILE="/etc/cron.d/warp-wireproxy-check"
LOG_FILE="/var/log/warp-check.log"
LOCK_FILE="/var/lock/warpwp-check.lock"
NATIVE_LOCK_FILE="/var/lock/warpwp-native.lock"
ADMIN_LOCK_FILE="/var/lock/warpwp-admin.lock"
TIMER_SERVICE_FILE="/etc/systemd/system/warp-wireproxy-check.service"
TIMER_FILE="/etc/systemd/system/warp-wireproxy-check.timer"
TIMER_LOG_FILE="/var/log/warp-timer-check.log"
TIMER_ENV_FILE="/etc/default/warp-wireproxy-check"
LOGROTATE_FILE="/etc/logrotate.d/warp-wireproxy-manager"
DEFAULT_SCAN_COUNT="25"
QUICK_SCAN_COUNT="15"
DEEP_SCAN_COUNT="150"
DEFAULT_SCHEDULE="*/10 * * * *"
DEFAULT_TIMER_MINUTES="10"
SOCKS_HOST="127.0.0.1"
SOCKS_PORT="40000"
ZAPRET_PORTS="443,2408,1843,1010,500,1701,4500,4443,8443,8095"

# Диагностика идёт в stderr: иначе command substitution вокруг функций,
# которые логируют, затягивает сообщения в возвращаемое значение.
log() { printf '\033[1;36m[ИНФО]\033[0m %s\n' "$*" >&2; }
ok() { printf '\033[1;32m[ОК]\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33m[ВНИМАНИЕ]\033[0m %s\n' "$*" >&2; }
err() { printf '\033[1;31m[ОШИБКА]\033[0m %s\n' "$*" >&2; }
need_root() { [[ "${EUID}" -eq 0 ]] || { err "Запусти от root."; exit 1; }; }
pause() { echo; read -rp "Нажми Enter для продолжения... " _ || true; }
json_escape() { local s="${1:-}"; s="${s//\\/\\\\}"; s="${s//\"/\\\"}"; s="${s//$'\n'/\\n}"; s="${s//$'\r'/}"; s="${s//$'\t'/\\t}"; printf '%s' "$s"; }
json_bool() { [[ "${1:-}" == "1" || "${1:-}" == "true" ]] && printf 'true' || printf 'false'; }
trim() { local s="$*"; s="${s#"${s%%[![:space:]]*}"}"; s="${s%"${s##*[![:space:]]}"}"; printf '%s' "$s"; }

need_curl() {
  command -v curl >/dev/null 2>&1 && return 0
  log "curl не найден, пробую установить..."
  if command -v apt-get >/dev/null 2>&1; then apt-get update -y || true; apt-get install -y curl
  elif command -v dnf >/dev/null 2>&1; then dnf install -y curl
  elif command -v yum >/dev/null 2>&1; then yum install -y curl
  elif command -v apk >/dev/null 2>&1; then apk add --no-cache curl
  else err "curl не найден и пакетный менеджер неизвестен."; exit 1; fi
}
ensure_flock() {
  command -v flock >/dev/null 2>&1 && return 0
  warn "flock не найден. Пробую установить util-linux."
  if command -v apt-get >/dev/null 2>&1; then apt-get update -y || true; apt-get install -y util-linux || true
  elif command -v dnf >/dev/null 2>&1; then dnf install -y util-linux || true
  elif command -v yum >/dev/null 2>&1; then yum install -y util-linux || true
  elif command -v apk >/dev/null 2>&1; then apk add --no-cache util-linux || true; fi
}
acquire_admin_lock() {
  need_root
  ensure_flock
  if [[ -n "${WARPWP_ADMIN_LOCK_FD:-}" && -e "/proc/self/fd/${WARPWP_ADMIN_LOCK_FD}" ]]; then
    ADMIN_LOCK_FD="$WARPWP_ADMIN_LOCK_FD"
    return 0
  fi
  command -v flock >/dev/null 2>&1 || { err "flock is required for safe administration."; return 1; }
  mkdir -p "$(dirname "$ADMIN_LOCK_FILE")"
  exec {ADMIN_LOCK_FD}>"$ADMIN_LOCK_FILE"
  if ! flock -n "$ADMIN_LOCK_FD"; then
    err "Another install, update, or removal operation is already running."
    return 75
  fi
  export WARPWP_ADMIN_LOCK_FD="$ADMIN_LOCK_FD"
}
valid_release_tag() { [[ "${1:-}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; }
release_asset_url() { local tag="$1" asset="$2"; valid_release_tag "$tag" || return 1; printf '%s/%s/%s' "$RELEASE_DOWNLOAD_BASE" "$tag" "$asset"; }

install_release_verifier() {
  command -v sha256sum >/dev/null 2>&1 && command -v ssh-keygen >/dev/null 2>&1 && command -v python3 >/dev/null 2>&1 && return 0
  log "Устанавливаю инструменты проверки release-подписи: sha256sum, ssh-keygen, python3"
  if command -v apt-get >/dev/null 2>&1; then apt-get update -y || true; DEBIAN_FRONTEND=noninteractive apt-get install -y coreutils openssh-client python3
  elif command -v dnf >/dev/null 2>&1; then dnf install -y coreutils openssh-clients python3
  elif command -v yum >/dev/null 2>&1; then yum install -y coreutils openssh-clients python3
  elif command -v apk >/dev/null 2>&1; then apk add --no-cache coreutils openssh-client python3
  else err "Не найдены инструменты для проверки подписи: sha256sum, ssh-keygen, python3"; return 1; fi
  command -v sha256sum >/dev/null 2>&1 && command -v ssh-keygen >/dev/null 2>&1 && command -v python3 >/dev/null 2>&1 || { err "Не удалось установить инструменты проверки подписи release."; return 1; }
}

resolve_release_tag() {
  local requested="${1:-}" response tag
  if [[ -n "$requested" ]]; then
    valid_release_tag "$requested" || { err "Некорректный tag release: $requested"; return 1; }
    printf '%s' "$requested"
    return 0
  fi
  response="$(curl -fsSL --retry 2 --connect-timeout 15 -H 'Accept: application/vnd.github+json' "$GITHUB_API/releases/latest")" || { err "Не удалось получить latest release из GitHub API. Укажи tag явно: warpwp --update vX.Y.Z"; return 1; }
  tag="$(python3 -c 'import json, sys; print(json.load(sys.stdin).get("tag_name", ""))' <<< "$response" 2>/dev/null || true)"
  valid_release_tag "$tag" || { err "GitHub API вернул некорректный tag release: ${tag:-empty}"; return 1; }
  printf '%s' "$tag"
}

verify_release_manifest() {
  local manifest="$1" signature="$2" allowed_signers="$3"
  printf '%s %s\n' "$RELEASE_SIGNING_ID" "$RELEASE_SIGNING_PUBLIC_KEY" > "$allowed_signers"
  if ! ssh-keygen -Y verify -f "$allowed_signers" -I "$RELEASE_SIGNING_ID" -n "$RELEASE_SIGNING_NAMESPACE" -s "$signature" < "$manifest" >/dev/null 2>&1; then
    err "Подпись SHA256SUMS не прошла проверку. Обновление отменено."
    return 1
  fi
}

download_release_manifest() {
  local tag="$1" manifest="$2" signature="$3" allowed_signers="$4"
  curl -fsSL --retry 2 --connect-timeout 15 "$(release_asset_url "$tag" SHA256SUMS)" -o "$manifest" || { err "Не удалось скачать SHA256SUMS для $tag"; return 1; }
  curl -fsSL --retry 2 --connect-timeout 15 "$(release_asset_url "$tag" SHA256SUMS.sig)" -o "$signature" || { err "Не удалось скачать подпись SHA256SUMS для $tag"; return 1; }
  [[ -s "$manifest" && -s "$signature" ]] || { err "Release $tag содержит пустой manifest или подпись."; return 1; }
  verify_release_manifest "$manifest" "$signature" "$allowed_signers"
}

stage_release_asset() {
  local tag="$1" asset="$2" manifest="$3" tmp="$4" expected actual
  expected="$(awk -v asset="$asset" '$2 == asset || $2 == "*" asset {print $1; exit}' "$manifest")"
  [[ "$expected" =~ ^[a-fA-F0-9]{64}$ ]] || { err "В SHA256SUMS release $tag нет корректной суммы для $asset"; return 1; }
  if ! curl -fsSL --retry 2 --connect-timeout 15 "$(release_asset_url "$tag" "$asset")" -o "$tmp"; then err "Не удалось скачать $asset из release $tag"; return 1; fi
  actual="$(sha256sum "$tmp" | awk '{print $1}')"
  [[ "$actual" == "$expected" ]] || { err "SHA-256 $asset не совпадает с подписанным manifest. Обновление отменено."; return 1; }
  if [[ ! -s "$tmp" ]] || ! bash -n "$tmp"; then err "Release-артефакт $asset не прошёл bash -n."; return 1; fi
  chmod 0755 "$tmp"
}
configured_bind_address() { grep -i '^[[:space:]]*BindAddress[[:space:]]*=' "$WG_DIR/proxy.conf" 2>/dev/null | head -n1 | cut -d= -f2- | xargs || true; }
current_socks_host() {
  local bind host
  bind="$(configured_bind_address)"
  if [[ "$bind" =~ ^\[([^]]+)\]:([0-9]+)$ ]]; then host="${BASH_REMATCH[1]}"
  elif [[ "$bind" =~ ^([^:]+):([0-9]+)$ ]]; then host="${BASH_REMATCH[1]}"
  else host="$SOCKS_HOST"; fi
  case "$host" in 0.0.0.0) host="127.0.0.1" ;; ::) host="::1" ;; esac
  printf '%s' "$host"
}
current_socks_port() {
  local bind port
  bind="$(configured_bind_address)"
  if [[ "$bind" =~ ^\[[^]]+\]:([0-9]+)$ || "$bind" =~ ^[^:]+:([0-9]+)$ ]]; then port="${BASH_REMATCH[1]}"; else port="$SOCKS_PORT"; fi
  if ! [[ "$port" =~ ^[0-9]+$ ]] || (( 10#$port < 1 || 10#$port > 65535 )); then port="$SOCKS_PORT"; else port="$((10#$port))"; fi
  printf '%s' "$port"
}
socks_proxy_url() { local host; host="$(current_socks_host)"; [[ "$host" == *:* ]] && host="[$host]"; printf 'socks5h://%s:%s' "$host" "$(current_socks_port)"; }
socks_listening_bool() {
  local port
  port="$(current_socks_port)"
  ss -H -lnt 2>/dev/null | awk -v suffix=":$port" '
    { address=$4 }
    length(address) > length(suffix) && substr(address, length(address)-length(suffix)+1) == suffix { found=1 }
    END { exit !found }
  ' && echo 1 || echo 0
}
current_endpoint() { grep -i '^Endpoint' "$WG_DIR/proxy.conf" 2>/dev/null | head -n1 | awk -F= '{gsub(/^[ \t]+|[ \t]+$/, "", $2); print $2}' || true; }
current_endpoint_port() { local ep; ep="$(current_endpoint)"; [[ -n "$ep" && "$ep" == *:* ]] && echo "${ep##*:}" || echo "1843/2408/1010"; }
native_version() { [[ -x "$NATIVE_BIN" ]] && "$NATIVE_BIN" --version 2>/dev/null | awk '{print $2}' || true; }
cron_installed_bool() { [[ -f "$CRON_FILE" ]] && echo 1 || echo 0; }
cron_flock_bool() { grep -q "flock -n $LOCK_FILE" "$CRON_FILE" 2>/dev/null && echo 1 || echo 0; }
cron_schedule_from_file() { local file="$1"; awk 'NF && $0 !~ /^[[:space:]]*#/ && $0 !~ /^[[:space:]]*[[:alpha:]_][[:alnum:]_]*=/ {print $1 " " $2 " " $3 " " $4 " " $5; exit}' "$file" 2>/dev/null || true; }
cron_schedule() { cron_schedule_from_file "$CRON_FILE"; }
cron_daemon_name() {
  local svc
  command -v systemctl >/dev/null 2>&1 || return 1
  for svc in cron crond; do
    if systemctl cat "${svc}.service" >/dev/null 2>&1; then echo "$svc"; return 0; fi
  done
  return 1
}
cron_daemon_active_bool() { local svc; svc="$(cron_daemon_name 2>/dev/null || true)"; [[ -n "$svc" ]] && systemctl is-active --quiet "${svc}.service" 2>/dev/null && echo 1 || echo 0; }
cron_active_bool() { [[ "$(cron_installed_bool)" == "1" && "$(cron_daemon_active_bool)" == "1" ]] && echo 1 || echo 0; }
timer_installed_bool() { [[ -f "$TIMER_SERVICE_FILE" && -f "$TIMER_FILE" ]] && echo 1 || echo 0; }
timer_active_bool() { systemctl is-active --quiet warp-wireproxy-check.timer 2>/dev/null && echo 1 || echo 0; }
timer_enabled_bool() { systemctl is-enabled --quiet warp-wireproxy-check.timer 2>/dev/null && echo 1 || echo 0; }
normalize_positive_integer() { local value="${1:-}"; [[ "$value" =~ ^[0-9]+$ ]] || return 1; value="$((10#$value))"; [[ "$value" -ge 1 ]] || return 1; printf '%s' "$value"; }
get_timer_minutes() { local value=""; if [[ -f "$TIMER_ENV_FILE" ]]; then value="$(grep -E '^TIMER_MINUTES=' "$TIMER_ENV_FILE" 2>/dev/null | tail -n1 | cut -d= -f2- | tr -d '"' || true)"; fi; value="$(normalize_positive_integer "$value" 2>/dev/null || true)"; [[ -n "$value" ]] || value="$DEFAULT_TIMER_MINUTES"; echo "$value"; }
ask_timer_minutes() {
  local arg="${1:-}" current input
  current="$(get_timer_minutes)"
  # Явно переданный аргумент всегда важнее промпта: warpwp --install-timer 15
  if [[ -n "$arg" ]]; then input="$arg"
  elif [[ -t 0 ]]; then read -rp "Интервал проверки в минутах [${current}]: " input || true; input="${input:-$current}"
  else input="$current"; fi
  input="$(normalize_positive_integer "$input" 2>/dev/null || true)"
  if [[ -z "$input" ]]; then warn "Некорректный интервал, использую ${DEFAULT_TIMER_MINUTES} минут."; input="$DEFAULT_TIMER_MINUTES"; fi
  echo "$input"
}

install_manager() { acquire_admin_lock || return 1; update_local_scripts "${1:-}" || return 1; ok "Готово. Теперь меню запускается командой: warpwp"; }
update_local_scripts() (
  local requested_tag="${1:-}" release_tag native_stage manager_stage native_backup manager_backup manifest signature allowed_signers had_native=0 had_manager=0 preserve_backups=0 rollback_failed=0
  acquire_admin_lock || return 1
  need_curl || return 1
  install_release_verifier || return 1
  release_tag="$(resolve_release_tag "$requested_tag")" || return 1
  mkdir -p "$(dirname "$NATIVE_BIN")" "$(dirname "$MANAGER_BIN")" || return 1
  native_stage="$(mktemp "${NATIVE_BIN}.new.XXXXXX")" || return 1
  manager_stage="$(mktemp "${MANAGER_BIN}.new.XXXXXX")" || return 1
  native_backup="$(mktemp "${NATIVE_BIN}.bak.XXXXXX")" || return 1
  manager_backup="$(mktemp "${MANAGER_BIN}.bak.XXXXXX")" || return 1
  manifest="$(mktemp "${MANAGER_BIN}.manifest.XXXXXX")" || return 1
  signature="$(mktemp "${MANAGER_BIN}.manifest-signature.XXXXXX")" || return 1
  allowed_signers="$(mktemp "${MANAGER_BIN}.allowed-signers.XXXXXX")" || return 1
  trap 'rm -f -- "$native_stage" "$manager_stage" "$manifest" "$signature" "$allowed_signers"; [[ "$preserve_backups" == "1" ]] || rm -f -- "$native_backup" "$manager_backup"' EXIT
  log "Получаю подписанный manifest release $release_tag..."
  download_release_manifest "$release_tag" "$manifest" "$signature" "$allowed_signers" || return 1
  log "Загружаю и сверяю native-скрипт из $release_tag..."
  stage_release_asset "$release_tag" warp-wireproxy-native.sh "$manifest" "$native_stage" || return 1
  log "Загружаю и сверяю менеджер из $release_tag..."
  stage_release_asset "$release_tag" warpwp.sh "$manifest" "$manager_stage" || return 1
  chmod 0755 "$native_stage" "$manager_stage" || return 1
  if [[ -e "$NATIVE_BIN" || -L "$NATIVE_BIN" ]]; then cp -p -- "$NATIVE_BIN" "$native_backup" || return 1; had_native=1; fi
  if [[ -e "$MANAGER_BIN" || -L "$MANAGER_BIN" ]]; then cp -p -- "$MANAGER_BIN" "$manager_backup" || return 1; had_manager=1; fi
  if ! mv -f -- "$native_stage" "$NATIVE_BIN"; then err "Не удалось установить native-скрипт."; return 1; fi
  if ! mv -f -- "$manager_stage" "$MANAGER_BIN"; then
    err "Не удалось установить менеджер; откатываю обновление."
    if [[ "$had_native" == "1" ]]; then mv -f -- "$native_backup" "$NATIVE_BIN" || rollback_failed=1; else rm -f -- "$NATIVE_BIN" || rollback_failed=1; fi
    if [[ "$had_manager" == "1" ]]; then mv -f -- "$manager_backup" "$MANAGER_BIN" || rollback_failed=1; fi
    if [[ "$rollback_failed" == "1" ]]; then
      preserve_backups=1
      err "Откат обновления выполнен не полностью; резервные копии сохранены рядом с целевыми файлами (*.bak.*)."
    fi
    return 1
  fi
  ok "Обновлены из проверенного release $release_tag: $NATIVE_BIN и $MANAGER_BIN"
)
restart_updated_manager() { [[ -x "$MANAGER_BIN" ]] || { err "Не найден обновлённый менеджер: $MANAGER_BIN"; return 1; }; log "Перезапускаю менеджер из обновлённого файла..."; exec "$MANAGER_BIN" "$@"; }
remove_cron_check() { rm -f "$CRON_FILE"; systemctl restart cron 2>/dev/null || systemctl restart crond 2>/dev/null || true; }
remove_timer_check_quiet() { systemctl disable --now warp-wireproxy-check.timer 2>/dev/null || true; rm -f "$TIMER_SERVICE_FILE" "$TIMER_FILE"; systemctl daemon-reload 2>/dev/null || true; systemctl reset-failed 2>/dev/null || true; }

install_logrotate() {
  local tmp
  command -v logrotate >/dev/null 2>&1 || return 0
  mkdir -p "$(dirname "$LOGROTATE_FILE")"
  tmp="$(mktemp "${LOGROTATE_FILE}.tmp.XXXXXX")"
  cat > "$tmp" <<EOF_LOGROTATE
$LOG_FILE $TIMER_LOG_FILE {
    weekly
    rotate 12
    missingok
    notifempty
    compress
    delaycompress
    copytruncate
    su root root
}
EOF_LOGROTATE
  chmod 0644 "$tmp"
  mv -f -- "$tmp" "$LOGROTATE_FILE"
}

install_cron_package() {
  log "Cron daemon не найден, устанавливаю пакет..."
  if command -v apt-get >/dev/null 2>&1; then apt-get update; apt-get install -y cron
  elif command -v dnf >/dev/null 2>&1; then dnf install -y cronie
  elif command -v yum >/dev/null 2>&1; then yum install -y cronie
  elif command -v apk >/dev/null 2>&1; then apk add --no-cache dcron
  elif command -v pacman >/dev/null 2>&1; then pacman -Sy --noconfirm cronie
  else err "Не найден поддерживаемый пакетный менеджер для установки cron."; return 1; fi
}
ensure_cron_daemon() {
  local svc
  svc="$(cron_daemon_name 2>/dev/null || true)"
  if [[ -z "$svc" ]]; then install_cron_package; systemctl daemon-reload; svc="$(cron_daemon_name 2>/dev/null || true)"; fi
  [[ -n "$svc" ]] || { err "Cron установлен, но systemd unit cron/crond не найден."; return 1; }
  systemctl enable --now "${svc}.service"
  if ! systemctl is-active --quiet "${svc}.service"; then err "${svc}.service не запущен."; return 1; fi
  return 0
}

install_cron_check() {
  acquire_admin_lock
  local check_cmd cron_tmp cron_backup cron_svc had_cron=0
  if [[ ! -x "$NATIVE_BIN" ]]; then warn "Локальный native-скрипт не найден. Сначала обновляю скрипты."; update_local_scripts || return 1; fi
  ensure_cron_daemon || return 1
  cron_svc="$(cron_daemon_name)" || return 1
  if command -v flock >/dev/null 2>&1; then check_cmd="flock -n $LOCK_FILE $NATIVE_BIN --check --scan-count $DEFAULT_SCAN_COUNT --enough-good 1"; else warn "flock недоступен. Cron будет без lock-защиты."; check_cmd="$NATIVE_BIN --check --scan-count $DEFAULT_SCAN_COUNT --enough-good 1"; fi
  mkdir -p "$(dirname "$CRON_FILE")"
  cron_tmp="$(mktemp "${CRON_FILE}.tmp.XXXXXX")"
  cron_backup="$(mktemp "${CRON_FILE}.bak.XXXXXX")"
  if [[ -e "$CRON_FILE" || -L "$CRON_FILE" ]]; then cp -p -- "$CRON_FILE" "$cron_backup"; had_cron=1; fi
  cat > "$cron_tmp" <<EOF_CRON
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

$DEFAULT_SCHEDULE root $check_cmd >> $LOG_FILE 2>&1
EOF_CRON
  chmod 0644 "$cron_tmp"
  if [[ -z "$(cron_schedule_from_file "$cron_tmp")" ]]; then rm -f -- "$cron_tmp" "$cron_backup"; err "Создан некорректный cron-файл."; return 1; fi
  if ! mv -f -- "$cron_tmp" "$CRON_FILE"; then rm -f -- "$cron_tmp" "$cron_backup"; return 1; fi
  if ! systemctl restart "${cron_svc}.service" || ! systemctl is-active --quiet "${cron_svc}.service"; then
    warn "Новый cron-файл не удалось активировать; восстанавливаю предыдущее расписание."
    if [[ "$had_cron" == "1" ]]; then mv -f -- "$cron_backup" "$CRON_FILE"; else rm -f -- "$CRON_FILE"; fi
    systemctl restart "${cron_svc}.service" 2>/dev/null || true
    rm -f -- "$cron_tmp" "$cron_backup"
    return 1
  fi
  rm -f -- "$cron_backup"
  remove_timer_check_quiet
  install_logrotate
  ok "Cron включён: $CRON_FILE"; ok "Systemd timer отключён, чтобы не было двойного scheduler."
}
install_timer_check() {
  acquire_admin_lock
  if [[ ! -x "$NATIVE_BIN" ]]; then warn "Локальный native-скрипт не найден. Сначала обновляю скрипты."; update_local_scripts || return 1; fi
  local minutes exec_cmd service_tmp timer_tmp env_tmp service_backup timer_backup env_backup had_service=0 had_timer=0 had_env=0
  minutes="$(ask_timer_minutes "${1:-}")"
  if command -v flock >/dev/null 2>&1; then exec_cmd="/usr/bin/flock -n $LOCK_FILE $NATIVE_BIN --check --scan-count $DEFAULT_SCAN_COUNT --enough-good 1"; else warn "flock недоступен. Timer будет без lock-защиты."; exec_cmd="$NATIVE_BIN --check --scan-count $DEFAULT_SCAN_COUNT --enough-good 1"; fi
  mkdir -p "$(dirname "$TIMER_SERVICE_FILE")" "$(dirname "$TIMER_ENV_FILE")"
  service_tmp="$(mktemp "${TIMER_SERVICE_FILE}.tmp.XXXXXX")"
  timer_tmp="$(mktemp "${TIMER_FILE}.tmp.XXXXXX")"
  env_tmp="$(mktemp "${TIMER_ENV_FILE}.tmp.XXXXXX")"
  service_backup="$(mktemp "${TIMER_SERVICE_FILE}.bak.XXXXXX")"
  timer_backup="$(mktemp "${TIMER_FILE}.bak.XXXXXX")"
  env_backup="$(mktemp "${TIMER_ENV_FILE}.bak.XXXXXX")"
  if [[ -e "$TIMER_SERVICE_FILE" || -L "$TIMER_SERVICE_FILE" ]]; then cp -p -- "$TIMER_SERVICE_FILE" "$service_backup"; had_service=1; fi
  if [[ -e "$TIMER_FILE" || -L "$TIMER_FILE" ]]; then cp -p -- "$TIMER_FILE" "$timer_backup"; had_timer=1; fi
  if [[ -e "$TIMER_ENV_FILE" || -L "$TIMER_ENV_FILE" ]]; then cp -p -- "$TIMER_ENV_FILE" "$env_backup"; had_env=1; fi
  printf 'TIMER_MINUTES="%s"\n' "$minutes" > "$env_tmp"
  cat > "$service_tmp" <<EOF_SERVICE
[Unit]
Description=WARP WireProxy endpoint health check
Wants=network-online.target
After=network-online.target wireproxy.service

[Service]
Type=oneshot
EnvironmentFile=-$TIMER_ENV_FILE
ExecStart=/bin/bash -lc '$exec_cmd >> $TIMER_LOG_FILE 2>&1'
Nice=10
EOF_SERVICE
  cat > "$timer_tmp" <<EOF_TIMER
[Unit]
Description=Run WARP WireProxy endpoint health check every ${minutes}min

[Timer]
OnBootSec=2min
OnUnitActiveSec=${minutes}min
AccuracySec=30s
Persistent=true
Unit=warp-wireproxy-check.service

[Install]
WantedBy=timers.target
EOF_TIMER
  chmod 0644 "$service_tmp" "$timer_tmp" "$env_tmp"
  if ! mv -f -- "$service_tmp" "$TIMER_SERVICE_FILE"; then
    rm -f -- "$service_tmp" "$timer_tmp" "$env_tmp" "$service_backup" "$timer_backup" "$env_backup"
    return 1
  fi
  if ! mv -f -- "$timer_tmp" "$TIMER_FILE"; then
    if [[ "$had_service" == "1" ]]; then mv -f -- "$service_backup" "$TIMER_SERVICE_FILE"; else rm -f -- "$TIMER_SERVICE_FILE"; fi
    rm -f -- "$service_tmp" "$timer_tmp" "$env_tmp" "$timer_backup" "$env_backup"
    return 1
  fi
  if ! mv -f -- "$env_tmp" "$TIMER_ENV_FILE"; then
    if [[ "$had_service" == "1" ]]; then mv -f -- "$service_backup" "$TIMER_SERVICE_FILE"; else rm -f -- "$TIMER_SERVICE_FILE"; fi
    if [[ "$had_timer" == "1" ]]; then mv -f -- "$timer_backup" "$TIMER_FILE"; else rm -f -- "$TIMER_FILE"; fi
    rm -f -- "$service_tmp" "$timer_tmp" "$env_tmp" "$env_backup"
    return 1
  fi
  if ! systemctl daemon-reload || ! systemctl enable --now warp-wireproxy-check.timer || ! systemctl restart warp-wireproxy-check.timer || ! systemctl is-active --quiet warp-wireproxy-check.timer; then
    warn "Новый systemd timer не удалось активировать; восстанавливаю прежнюю конфигурацию."
    if [[ "$had_service" == "1" ]]; then mv -f -- "$service_backup" "$TIMER_SERVICE_FILE"; else rm -f -- "$TIMER_SERVICE_FILE"; fi
    if [[ "$had_timer" == "1" ]]; then mv -f -- "$timer_backup" "$TIMER_FILE"; else rm -f -- "$TIMER_FILE"; fi
    if [[ "$had_env" == "1" ]]; then mv -f -- "$env_backup" "$TIMER_ENV_FILE"; else rm -f -- "$TIMER_ENV_FILE"; fi
    systemctl daemon-reload 2>/dev/null || true
    if [[ "$had_timer" == "1" ]]; then systemctl restart warp-wireproxy-check.timer 2>/dev/null || true; else systemctl disable --now warp-wireproxy-check.timer 2>/dev/null || true; fi
    rm -f -- "$service_tmp" "$timer_tmp" "$env_tmp" "$service_backup" "$timer_backup" "$env_backup"
    return 1
  fi
  rm -f -- "$service_backup" "$timer_backup" "$env_backup"
  remove_cron_check
  install_logrotate
  ok "Timer включён: warp-wireproxy-check.timer, интервал: ${minutes} минут"; ok "Cron отключён, чтобы не было двойного scheduler."
}
remove_timer_check() { need_root; remove_timer_check_quiet; ok "Systemd timer удалён. Cron не тронут."; }
finish_install_or_update_all() { acquire_admin_lock || return 1; fix_routing --quiet || return 1; "$NATIVE_BIN" || return 1; install_cron_check || return 1; ok "Установка/обновление завершены."; print_memo_short; }
install_or_update_all() { local mode="${1:-}"; acquire_admin_lock || return 1; update_local_scripts || return 1; if [[ "$mode" == "--cli" ]]; then restart_updated_manager --install-current; else restart_updated_manager --install-current --menu; fi; }
scheduler_name() { local cron_active timer_active; cron_active="$(cron_active_bool)"; timer_active="$(timer_active_bool)"; if [[ "$cron_active" == "1" && "$timer_active" == "1" ]]; then echo "both"; elif [[ "$cron_active" == "1" ]]; then echo "cron"; elif [[ "$timer_active" == "1" ]]; then echo "systemd_timer"; else echo "none"; fi; }
scheduler_status() { echo "scheduler: $(scheduler_name)"; echo "cron installed: $(cron_installed_bool)"; echo "cron daemon: $(cron_daemon_name 2>/dev/null || echo absent)"; echo "cron active: $(cron_active_bool)"; echo "cron schedule: $(cron_schedule)"; echo "timer installed: $(timer_installed_bool)"; echo "timer active: $(timer_active_bool)"; echo "timer enabled: $(timer_enabled_bool)"; echo "timer interval minutes: $(get_timer_minutes)"; if [[ -f "$CRON_FILE" ]]; then echo; cat "$CRON_FILE"; fi; if [[ -f "$TIMER_FILE" ]]; then echo; cat "$TIMER_FILE"; fi; return 0; }
timer_status() { scheduler_status; echo; systemctl status warp-wireproxy-check.timer --no-pager -l 2>/dev/null || true; echo; systemctl list-timers --all 'warp-wireproxy-check.timer' 2>/dev/null || true; echo; tail -n 80 "$TIMER_LOG_FILE" 2>/dev/null || true; }

warp_rule_present() { ip "$1" rule show 2>/dev/null | grep -Eq 'lookup warp([[:space:]]|$)'; }
delete_warp_rules() {
  local family="$1" attempts=0
  while warp_rule_present "$family"; do
    ((attempts+=1))
    if (( attempts > 64 )); then return 1; fi
    ip "$family" rule del lookup warp 2>/dev/null || return 1
  done
}
routing_danger_bool() {
  ip link show warp >/dev/null 2>&1 && return 0
  warp_rule_present -4 && return 0
  warp_rule_present -6 && return 0
  ip -4 route show table warp 2>/dev/null | grep -q . && return 0
  ip -6 route show table warp 2>/dev/null | grep -q . && return 0
  systemctl is-active --quiet wg-quick@warp 2>/dev/null && return 0
  systemctl is-enabled --quiet wg-quick@warp 2>/dev/null && return 0
  systemctl is-active --quiet wg-quick@wgcf 2>/dev/null && return 0
  systemctl is-enabled --quiet wg-quick@wgcf 2>/dev/null && return 0
  systemctl is-active --quiet warp-svc 2>/dev/null && return 0
  systemctl is-enabled --quiet warp-svc 2>/dev/null && return 0
  return 1
}
routing_guard_status() {
  echo "--- Routing guard ---"
  if routing_danger_bool; then warn "Найдены признаки системного WARP full-tunnel. Он может ломать входящие SSH/443."; else ok "Опасная системная WARP-маршрутизация не найдена."; fi
  echo; echo "IPv4 ip rule:"; ip -4 rule show 2>/dev/null || true
  echo; echo "IPv6 ip rule:"; ip -6 rule show 2>/dev/null || true
  echo; echo "IPv4 table 51820:"; ip -4 route show table 51820 2>/dev/null || true
  echo; echo "IPv6 table 51820:"; ip -6 route show table 51820 2>/dev/null || true
  echo; echo "interface warp:"; ip -br link show warp 2>/dev/null || echo "warp: absent"
  echo; echo "conflicting services:"; for svc in wg-quick@warp wg-quick@wgcf warp-svc; do systemctl is-active --quiet "$svc" 2>/dev/null && echo "$svc: active" || true; systemctl is-enabled --quiet "$svc" 2>/dev/null && echo "$svc: enabled" || true; done
}
fix_routing() {
  acquire_admin_lock || return 1
  local quiet="${1:-}" force="${2:-}"
  if [[ "$quiet" == "--force" ]]; then force="--force"; quiet=""; fi
  if [[ "$force" != "--force" ]]; then
    warn "Автоматическая очистка маршрутов отключена: таблица 51820 может принадлежать чужому WireGuard-туннелю. Для явного удаления только системного WARP используй: warpwp --fix-routing --force"
    [[ "$quiet" == "--quiet" ]] || routing_guard_status
    return 0
  fi
  [[ "$quiet" == "--quiet" ]] || warn "Удаляю только явно подтверждённый системный WARP. wireproxy SOCKS5 не трогаю."
  systemctl disable --now wg-quick@warp wg-quick@wgcf warp-svc 2>/dev/null || true
  ip link del warp 2>/dev/null || true
  delete_warp_rules -4 || true
  delete_warp_rules -6 || true
  ip -4 route flush table warp 2>/dev/null || true
  ip -6 route flush table warp 2>/dev/null || true
  if routing_danger_bool; then err "Не удалось полностью убрать системную WARP-маршрутизацию."; [[ "$quiet" == "--quiet" ]] || routing_guard_status; return 1; fi
  [[ "$quiet" == "--quiet" ]] || { ok "Очистка завершена."; routing_guard_status; }
}

status() {
  local load_state socks_port
  socks_port="$(current_socks_port)"
  echo "warpwp v$VERSION"
  [[ -x "$NATIVE_BIN" ]] && "$NATIVE_BIN" --version 2>/dev/null || echo "native script: not installed"
  echo; echo "--- Endpoint ---"; grep -i '^Endpoint' "$WG_DIR/proxy.conf" 2>/dev/null || echo "proxy.conf не найден"
  echo; echo "--- Service ---"
  load_state="$(systemctl show -p LoadState --value wireproxy.service 2>/dev/null || true)"
  if [[ -z "$load_state" || "$load_state" == "not-found" ]]; then
    echo "wireproxy.service не найден"
  else
    systemctl status wireproxy --no-pager -l 2>/dev/null | head -35 || true
  fi
  echo; echo "--- Port $socks_port ---"
  if [[ "$(socks_listening_bool)" == "1" ]]; then ss -lntup 2>/dev/null | grep -E ":${socks_port}([[:space:]]|$)" || true; else echo "порт $socks_port не слушается"; fi
  echo; echo "--- Cloudflare trace через SOCKS5 ---"
  curl -m 10 -sS -x "$(socks_proxy_url)" https://www.cloudflare.com/cdn-cgi/trace 2>/dev/null | grep -E '^(ip|colo|loc|warp)=' || echo "нет ответа через SOCKS5"
  echo; scheduler_status; echo; routing_guard_status; echo; print_memo_short
}
status_json() {
  local ep ep_port native_ver service_state service_active socks_host socks_port socks_listening
  local cron_installed cron_active cron_flock cron_daemon cron_schedule_value log_exists manager_installed native_installed
  local trace ip colo loc warp installed healthy timer_installed timer_active timer_enabled timer_minutes timer_log_exists scheduler routing_danger
  local cache_line cache_time cache_colo cache_loc cache_checked_at cache_loss cache_stable cache_scanner _cache_ep
  ep="$(current_endpoint)"
  ep_port="$(current_endpoint_port)"
  native_ver="$(native_version)"
  service_state="$(systemctl is-active wireproxy 2>/dev/null || true)"
  [[ "$service_state" == "active" ]] && service_active="1" || service_active="0"
  socks_host="$(current_socks_host)"
  socks_port="$(current_socks_port)"
  socks_listening="$(socks_listening_bool)"
  cron_installed="$(cron_installed_bool)"
  cron_active="$(cron_active_bool)"
  cron_flock="$(cron_flock_bool)"
  cron_daemon="$(cron_daemon_name 2>/dev/null || true)"
  cron_schedule_value="$(cron_schedule)"
  [[ -f "$LOG_FILE" ]] && log_exists="1" || log_exists="0"
  [[ -x "$MANAGER_BIN" ]] && manager_installed="1" || manager_installed="0"
  [[ -x "$NATIVE_BIN" ]] && native_installed="1" || native_installed="0"
  [[ -f "$WG_DIR/proxy.conf" ]] && installed="1" || installed="0"
  timer_installed="$(timer_installed_bool)"
  timer_active="$(timer_active_bool)"
  timer_enabled="$(timer_enabled_bool)"
  timer_minutes="$(get_timer_minutes)"
  [[ -f "$TIMER_LOG_FILE" ]] && timer_log_exists="1" || timer_log_exists="0"
  scheduler="$(scheduler_name)"
  trace="$(curl -m 10 -sS -x "$(socks_proxy_url)" https://www.cloudflare.com/cdn-cgi/trace 2>/dev/null || true)"
  ip="$(echo "$trace" | awk -F= '$1=="ip"{print $2; exit}')"
  colo="$(echo "$trace" | awk -F= '$1=="colo"{print $2; exit}')"
  loc="$(echo "$trace" | awk -F= '$1=="loc"{print $2; exit}')"
  warp="$(echo "$trace" | awk -F= '$1=="warp"{print $2; exit}')"
  routing_danger="0"; routing_danger_bool && routing_danger="1" || true
  [[ "$installed" == "1" && "$service_active" == "1" && "$socks_listening" == "1" && "$warp" == "on" && "$scheduler" != "none" && "$routing_danger" == "0" ]] && healthy="1" || healthy="0"
  cache_line="$(awk -v ep="$ep" -F'\t' '$1==ep{line=$0} END{print line}' "$WG_DIR/warp-endpoints.good" 2>/dev/null || true)"
  IFS=$'\t' read -r _cache_ep cache_time cache_colo cache_loc cache_checked_at cache_loss cache_stable cache_scanner <<< "$cache_line"
  cache_time="${cache_time:-}"; cache_colo="${cache_colo:-}"; cache_loc="${cache_loc:-}"; cache_checked_at="${cache_checked_at:-0}"; cache_loss="${cache_loss:-}"; cache_stable="${cache_stable:-}"; cache_scanner="${cache_scanner:-legacy}"
  [[ "$cache_checked_at" =~ ^[0-9]+$ ]] || cache_checked_at="0"
  cat <<EOF_JSON
{
  "manager_version": "$(json_escape "$VERSION")",
  "native_version": "$(json_escape "$native_ver")",
  "healthy": $(json_bool "$healthy"),
  "scheduler": "$(json_escape "$scheduler")",
  "installed": $(json_bool "$installed"),
  "manager_installed": $(json_bool "$manager_installed"),
  "native_installed": $(json_bool "$native_installed"),
  "service": {"name": "wireproxy", "state": "$(json_escape "$service_state")", "active": $(json_bool "$service_active")},
  "socks5": {"host": "$(json_escape "$socks_host")", "port": $socks_port, "listening": $(json_bool "$socks_listening")},
  "warp": {"endpoint": "$(json_escape "$ep")", "endpoint_port": "$(json_escape "$ep_port")", "ip": "$(json_escape "$ip")", "colo": "$(json_escape "$colo")", "loc": "$(json_escape "$loc")", "status": "$(json_escape "$warp")", "on": $( [[ "$warp" == "on" ]] && printf 'true' || printf 'false' )},
  "selection": {"time_total": "$(json_escape "$cache_time")", "colo": "$(json_escape "$cache_colo")", "loc": "$(json_escape "$cache_loc")", "checked_at": $cache_checked_at, "probe_loss_percent": "$(json_escape "$cache_loss")", "stable": "$(json_escape "$cache_stable")", "scanner": "$(json_escape "$cache_scanner")"},
  "routing_guard": {"danger": $(json_bool "$routing_danger")},
  "cron": {"file": "$(json_escape "$CRON_FILE")", "installed": $(json_bool "$cron_installed"), "active": $(json_bool "$cron_active"), "daemon": "$(json_escape "$cron_daemon")", "uses_flock": $(json_bool "$cron_flock"), "lock_file": "$(json_escape "$LOCK_FILE")", "schedule": "$(json_escape "$cron_schedule_value")"},
  "timer": {"service_file": "$(json_escape "$TIMER_SERVICE_FILE")", "timer_file": "$(json_escape "$TIMER_FILE")", "installed": $(json_bool "$timer_installed"), "enabled": $(json_bool "$timer_enabled"), "active": $(json_bool "$timer_active"), "interval_minutes": $timer_minutes, "log_file": "$(json_escape "$TIMER_LOG_FILE")", "log_exists": $(json_bool "$timer_log_exists")},
  "logs": {"cron_file": "$(json_escape "$LOG_FILE")", "cron_exists": $(json_bool "$log_exists"), "timer_file": "$(json_escape "$TIMER_LOG_FILE")", "timer_exists": $(json_bool "$timer_log_exists")},
  "cache": {"good_file": "$(json_escape "$WG_DIR/warp-endpoints.good")", "bad_file": "$(json_escape "$WG_DIR/warp-endpoints.bad")"}
}
EOF_JSON
}

run_scan() {
  local count="$1" label="$2"; shift 2
  local -a extra=("$@")
  local scan_lock_fd rc
  acquire_admin_lock
  if [[ ! -x "$NATIVE_BIN" ]]; then update_local_scripts || return 1; fi
  ensure_flock
  if command -v flock >/dev/null 2>&1; then
    exec {scan_lock_fd}>"$LOCK_FILE"
    if flock -n -E 75 "$scan_lock_fd"; then
      :
    else
      rc=$?
      warn "Другая проверка уже выполняется (lock: $LOCK_FILE)."
      return "$rc"
    fi
  fi
  fix_routing --quiet
  log "$label: запускаю проверку/ремонт WARP с scan-count=$count ${extra[*]}"
  if "$NATIVE_BIN" --check --scan-count "$count" "${extra[@]}"; then rc=0; else rc=$?; err "Scan завершился с ошибкой (код $rc)."; fi
  [[ -n "${scan_lock_fd:-}" ]] && flock -u "$scan_lock_fd" || true
  return "$rc"
}
repair_endpoint() { run_scan "$DEFAULT_SCAN_COUNT" "Обычный scan" "$@"; }
quick_scan() { run_scan "$QUICK_SCAN_COUNT" "Quick scan" "$@"; }
deep_scan() { run_scan "$DEEP_SCAN_COUNT" "Deep scan" "$@"; }
warpscout_scan() { run_scan "$DEEP_SCAN_COUNT" "WARPSCOUT deep scan" --scanner warpscout "$@"; }
doctor() { status; }
show_logs() { echo "--- $LOG_FILE ---"; tail -n 120 "$LOG_FILE" 2>/dev/null || true; echo; echo "--- $TIMER_LOG_FILE ---"; tail -n 80 "$TIMER_LOG_FILE" 2>/dev/null || true; echo; journalctl -u wireproxy -n 80 --no-pager 2>/dev/null || true; }
# Файлы в /etc/wireguard, созданные этим проектом. Всё остальное в каталоге
# принадлежит пользователю: чужие wg0.conf и т.п. не трогаем никогда.
WG_OWNED_FILES=(warp.conf warp.wireproxy.conf proxy.conf warp-account.json warp-private.key warp-endpoints.good warp-endpoints.bad warp-endpoints.good.tmp warp-endpoints.bad.tmp)
remove_project_wg_files() {
  local f
  for f in "${WG_OWNED_FILES[@]}"; do rm -f "$WG_DIR/$f"; done
  rmdir "$WG_DIR" 2>/dev/null && return 0
  if [[ -d "$WG_DIR" ]]; then warn "$WG_DIR оставлен: там есть посторонние файлы."; ls -1A "$WG_DIR" >&2 2>/dev/null || true; fi
  return 0
}
acquire_maintenance_locks() {
  local rc
  ensure_flock
  command -v flock >/dev/null 2>&1 || { err "flock required for safe removal."; return 1; }
  exec {CHECK_LOCK_FD}>"$LOCK_FILE"
  if flock -w 15 -E 75 "$CHECK_LOCK_FD"; then :; else
    rc=$?; err "Проверка WARP ещё выполняется. Удаление отменено; повтори через несколько секунд."; return "$rc"
  fi
  exec {NATIVE_LOCK_FD}>"$NATIVE_LOCK_FILE"
  if flock -w 15 -E 75 "$NATIVE_LOCK_FD"; then :; else
    rc=$?; flock -u "$CHECK_LOCK_FD" || true; exec {CHECK_LOCK_FD}>&-; err "Native-скрипт ещё выполняется. Удаление отменено; повтори позже."; return "$rc"
  fi
}

release_maintenance_locks() {
  [[ -n "${NATIVE_LOCK_FD:-}" ]] && flock -u "$NATIVE_LOCK_FD" 2>/dev/null || true
  [[ -n "${CHECK_LOCK_FD:-}" ]] && flock -u "$CHECK_LOCK_FD" 2>/dev/null || true
}

package_owner() {
  local path="$1"
  if command -v dpkg-query >/dev/null 2>&1; then dpkg-query -S "$path" 2>/dev/null | head -n1 || true
  elif command -v rpm >/dev/null 2>&1; then rpm -qf "$path" 2>/dev/null || true
  else true; fi
}

remove_unmanaged_paths() {
  local path owner
  for path in "$@"; do
    [[ -e "$path" || -L "$path" ]] || continue
    owner="$(package_owner "$path")"
    if [[ -n "$owner" ]]; then
      warn "Оставляю пакетный файл $path ($owner). Удали пакет штатным менеджером при необходимости."
    else
      rm -f -- "$path"
    fi
  done
}

remove_safe() {
  local ans
  need_root
  echo "Это удалит компоненты WARP WireProxy Manager."
  read -rp "Продолжить? [y/N]: " ans
  case "$ans" in y|Y|yes|YES|да|Да) ;; *) echo "Отменено."; return 0 ;; esac
  acquire_admin_lock || return $?
  # Сначала выключаем источники новых запусков, потом ждём уже начатый scan.
  remove_timer_check_quiet
  systemctl stop warp-wireproxy-check.service 2>/dev/null || true
  remove_cron_check
  acquire_maintenance_locks || return $?
  systemctl stop wireproxy 2>/dev/null || true
  systemctl disable wireproxy 2>/dev/null || true
  rm -f -- /etc/systemd/system/wireproxy.service "$NATIVE_BIN" "$LOG_FILE" "$TIMER_LOG_FILE" "$TIMER_ENV_FILE" "$LOGROTATE_FILE"
  remove_project_wg_files
  systemctl daemon-reload
  systemctl reset-failed
  release_maintenance_locks
  ok "Удаление завершено. Команда warpwp оставлена."
}

purge_all() {
  local ans
  need_root
  echo "Это жёстко удалит WARP/wireproxy/cron/timer/wgcf/warp-cli/fscarmen-следы."
  read -rp "Продолжить PURGE? [y/N]: " ans
  case "$ans" in y|Y|yes|YES|да|Да) ;; *) echo "Отменено."; return 0 ;; esac
  acquire_admin_lock || return $?
  remove_timer_check_quiet
  systemctl stop warp-wireproxy-check.service 2>/dev/null || true
  remove_cron_check
  acquire_maintenance_locks || return $?
  fix_routing --quiet --force || { release_maintenance_locks; return 1; }
  systemctl stop wireproxy warp-svc wg-quick@warp wg-quick@wgcf 2>/dev/null || true
  systemctl disable wireproxy warp-svc wg-quick@warp wg-quick@wgcf 2>/dev/null || true
  pkill -x wireproxy 2>/dev/null || true
  pkill -x warp-svc 2>/dev/null || true
  pkill -x warp-cli 2>/dev/null || true
  pkill -x wgcf 2>/dev/null || true
  rm -f -- /etc/systemd/system/wireproxy.service "$NATIVE_BIN" "$LOG_FILE" "$TIMER_LOG_FILE" "$TIMER_ENV_FILE" "$LOGROTATE_FILE"
  remove_unmanaged_paths \
    /etc/systemd/system/warp-svc.service \
    /usr/lib/systemd/system/wireproxy.service /usr/lib/systemd/system/warp-svc.service \
    /lib/systemd/system/wireproxy.service /lib/systemd/system/warp-svc.service \
    /usr/bin/wireproxy /usr/local/bin/wireproxy /opt/bin/wireproxy \
    /usr/bin/warp-cli /usr/local/bin/warp-cli /usr/bin/warp-svc /usr/local/bin/warp-svc \
    /usr/bin/wgcf /usr/local/bin/wgcf
  remove_project_wg_files
  rm -rf -- /root/warp-wireproxy-backup /root/warp-wireproxy-native-backup
  # /root/menu.sh не принадлежит проекту: его принципиально не трогаем.
  rm -f -- /root/warp-wireproxy-auto.sh /root/warp-wireproxy-native.sh "$CRON_FILE"
  systemctl daemon-reload
  systemctl reset-failed
  release_maintenance_locks
  ok "PURGE завершён. Команда warpwp оставлена."
}

wg_emit_json() {
  local source_file="$1" line section key value private_key mtu public_key endpoint keepalive preshared_key workers no_kernel_tun item i total peer_count
  local -a addresses allowed_ips_arr addr_parts allowed_parts
  mtu="1420"; keepalive="0"; workers="2"; no_kernel_tun="false"; section=""; peer_count=0; addresses=(); allowed_ips_arr=()
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%%#*}"; line="$(trim "$line")"; [[ -z "$line" ]] && continue
    case "$line" in "[Interface]") section="interface"; continue ;; "[Peer]") section="peer"; ((peer_count+=1)); continue ;; esac
    [[ "$line" == *=* ]] || continue
    key="$(trim "${line%%=*}")"; value="$(trim "${line#*=}")"; key="${key,,}"
    case "$section:$key" in
      interface:privatekey) private_key="$value" ;;
      interface:address) IFS=',' read -r -a addr_parts <<< "$value"; for item in "${addr_parts[@]}"; do item="$(trim "$item")"; [[ -n "$item" ]] && addresses+=("$item"); done ;;
      interface:mtu) mtu="$value" ;;
      peer:publickey) public_key="$value" ;;
      peer:presharedkey) preshared_key="$value" ;;
      peer:endpoint) endpoint="$value" ;;
      peer:allowedips) IFS=',' read -r -a allowed_parts <<< "$value"; for item in "${allowed_parts[@]}"; do item="$(trim "$item")"; [[ -n "$item" ]] && allowed_ips_arr+=("$item"); done ;;
      peer:persistentkeepalive) keepalive="$value" ;;
    esac
  done < "$source_file"
  if [[ "$peer_count" -ne 1 ]]; then err "Поддерживается ровно один блок [Peer]; найдено: $peer_count."; return 1; fi
  if ! [[ "$mtu" =~ ^[0-9]+$ ]] || (( 10#$mtu < 1 || 10#$mtu > 65535 )); then err "MTU должен быть целым числом от 1 до 65535."; return 1; fi
  if ! [[ "$keepalive" =~ ^[0-9]+$ ]] || (( 10#$keepalive > 65535 )); then err "PersistentKeepalive должен быть целым числом от 0 до 65535."; return 1; fi
  mtu="$((10#$mtu))"; keepalive="$((10#$keepalive))"
  if [[ -z "${private_key:-}" || "${#addresses[@]}" -eq 0 || -z "${public_key:-}" || -z "${endpoint:-}" ]]; then err "Не хватает обязательных полей: PrivateKey, Address, Peer PublicKey, Endpoint."; return 1; fi
  [[ "${#allowed_ips_arr[@]}" -gt 0 ]] || allowed_ips_arr=("0.0.0.0/0" "::/0")
  printf '{\n'; printf '  "protocol": "wireguard",\n'; printf '  "settings": {\n'; printf '    "mtu": %s,\n' "$mtu"; printf '    "secretKey": "%s",\n' "$(json_escape "$private_key")"; printf '    "address": [\n'
  total="${#addresses[@]}"; for i in "${!addresses[@]}"; do item="${addresses[$i]}"; printf '      "%s"' "$(json_escape "$item")"; [[ "$i" -lt $((total-1)) ]] && printf ','; printf '\n'; done
  printf '    ],\n'; printf '    "workers": %s,\n' "$workers"; printf '    "peers": [\n'; printf '      {\n'; printf '        "publicKey": "%s",\n' "$(json_escape "$public_key")"; if [[ -n "${preshared_key:-}" ]]; then printf '        "preSharedKey": "%s",\n' "$(json_escape "$preshared_key")"; fi; printf '        "allowedIPs": [\n'
  total="${#allowed_ips_arr[@]}"; for i in "${!allowed_ips_arr[@]}"; do item="${allowed_ips_arr[$i]}"; printf '          "%s"' "$(json_escape "$item")"; [[ "$i" -lt $((total-1)) ]] && printf ','; printf '\n'; done
  printf '        ],\n'; printf '        "endpoint": "%s",\n' "$(json_escape "$endpoint")"; printf '        "keepAlive": %s\n' "$keepalive"; printf '      }\n'; printf '    ],\n'; printf '    "noKernelTun": %s\n' "$no_kernel_tun"; printf '  }\n'; printf '}\n'
}
wg_conf_to_json() { local file="${1:-}"; if [[ -z "$file" && -t 0 ]]; then read -rp "Путь к WireGuard .conf: " file; fi; if [[ -z "$file" || ! -r "$file" ]]; then err "Файл WireGuard .conf не найден/не читается: ${file:-empty}"; echo "Пример: warpwp --wg-json /root/wg0.conf"; return 1; fi; wg_emit_json "$file"; }
wg_paste_to_json() { local tmp line got=0; tmp="$(mktemp)"; trap 'rm -f "$tmp"' RETURN; echo "Вставь WireGuard config целиком. После вставки ничего не нажимай 2 секунды — JSON появится автоматически."; echo; while true; do if [[ "$got" -eq 0 ]]; then IFS= read -r line || break; got=1; else IFS= read -r -t 2 line || break; fi; printf '%s\n' "$line" >> "$tmp"; done; if [[ ! -s "$tmp" ]]; then err "Конфиг не получен."; return 1; fi; wg_emit_json "$tmp"; }

print_xray() {
  local socks_host socks_port
  socks_host="$(current_socks_host)"
  socks_port="$(current_socks_port)"
  cat <<EOF_XRAY
{
  "tag": "WARP-socks5",
  "protocol": "socks",
  "settings": {"servers": [{"address": "$socks_host", "port": $socks_port}]}
},
{
  "tag": "WARP",
  "protocol": "freedom",
  "settings": {"domainStrategy": "UseIPv4"},
  "proxySettings": {"tag": "WARP-socks5"}
}
EOF_XRAY
}
print_zapret() { local ep port; ep="$(current_endpoint)"; port="$(current_endpoint_port)"; [[ -z "$ep" ]] && ep="ещё не установлен"; echo "Текущий WARP endpoint: $ep"; echo "Минимальный UDP-порт endpoint: $port"; echo "NFQWS_PORTS_UDP=$ZAPRET_PORTS"; }
print_commands() { cat <<EOF_CMDS
warpwp --install          # установить / обновить всё + cron
warpwp --install-cron     # включить cron и отключить timer
warpwp --install-timer    # включить timer и отключить cron, спросит интервал
warpwp --timer-status     # статус systemd timer
warpwp --scheduler-status # какой scheduler активен
warpwp --status           # состояние
warpwp --status-json      # JSON-статус
warpwp --doctor           # диагностика + routing guard
warpwp --fix-routing      # показать безопасную диагностику системного WARP
warpwp --fix-routing --force # явно удалить legacy system-WARP, не трогая table 51820
warpwp --check            # scan-count=$DEFAULT_SCAN_COUNT
warpwp --quick-scan       # scan-count=$QUICK_SCAN_COUNT
warpwp --deep-scan        # scan-count=$DEEP_SCAN_COUNT
warpwp --warpscout-scan   # независимый deep scan через WARPSCOUT
warpwp --check --avoid-node DME --country DE,NL
warpwp --deep-scan --scanner auto --stability-probes 7
warpwp --xray             # блоки для 3x-ui/Xray
warpwp --zapret           # строки для zapret4rocket
warpwp --wg-paste         # вставить WireGuard .conf и получить JSON для 3x-ui
warpwp --wg-json FILE     # конвертировать WireGuard .conf из файла
warpwp --logs             # логи
warpwp --version          # версия
EOF_CMDS
}
print_memo_short() { local ep socks_host socks_port; ep="$(current_endpoint)"; socks_host="$(current_socks_host)"; socks_port="$(current_socks_port)"; [[ -z "$ep" ]] && ep="ещё не установлен"; echo "SOCKS5: socks5://$socks_host:$socks_port"; echo "Endpoint: $ep"; echo "Scheduler: warpwp --scheduler-status"; echo "Routing guard: warpwp --fix-routing"; }
print_memo_full() { print_xray; echo; print_zapret; echo; print_commands; }
menu() { while true; do clear || true; echo "WARP + wireproxy manager v$VERSION"; print_memo_short; cat <<EOF_MENU
1) Установить / обновить WARP + wireproxy + cron
2) Проверить состояние
3) Проверить и починить endpoint
4) Обновить локальные скрипты
5) Безопасно удалить WARP Manager
6) Показать логи
7) Показать команды
8) Показать полную памятку
9) Doctor / расширенная диагностика
10) PURGE / жёсткая очистка WARP-следов
11) Включить cron/check и отключить timer
12) Показать блоки для 3x-ui / Xray
13) Quick scan endpoint
14) Deep scan endpoint
15) Показать JSON-статус
16) Включить systemd timer и отключить cron
17) Статус systemd timer
18) Удалить systemd timer
19) Scheduler status
20) Fix routing / убрать системный WARP full-tunnel
0) Выход
EOF_MENU
read -rp "Выбери пункт: " choice; case "$choice" in 1) install_or_update_all; pause ;; 2) status; pause ;; 3) repair_endpoint; pause ;; 4) update_local_scripts; restart_updated_manager ;; 5) remove_safe; pause ;; 6) show_logs; pause ;; 7) print_commands; pause ;; 8) print_memo_full; pause ;; 9) doctor; pause ;; 10) purge_all; pause ;; 11) install_cron_check; pause ;; 12) print_xray; pause ;; 13) quick_scan; pause ;; 14) deep_scan; pause ;; 15) status_json; pause ;; 16) install_timer_check; pause ;; 17) timer_status; pause ;; 18) remove_timer_check; pause ;; 19) scheduler_status; pause ;; 20) fix_routing --force; pause ;; 0) exit 0 ;; *) echo "Неверный пункт"; sleep 1 ;; esac; done; }

main() {
  case "${1:-}" in
    --install-manager) install_manager "${2:-}" ;;
    --install) install_or_update_all --cli ;;
    --install-current) finish_install_or_update_all; if [[ "${2:-}" == "--menu" ]]; then pause; menu; fi ;;
    --install-cron|--cron) install_cron_check ;;
    --install-timer|--timer) install_timer_check "${2:-}" ;;
    --timer-status) timer_status ;;
    --scheduler-status|--scheduler) scheduler_status ;;
    --remove-timer) remove_timer_check ;;
    --update|--self-update) update_local_scripts "${2:-}" ;;
    --status) status ;;
    --status-json|--json) status_json ;;
    --doctor) doctor ;;
    --fix-routing|--routing-fix) fix_routing "${2:-}" "${3:-}" ;;
    --check|--repair) shift; repair_endpoint "$@" ;;
    --quick-scan|--quick) shift; quick_scan "$@" ;;
    --deep-scan|--deep) shift; deep_scan "$@" ;;
    --warpscout-scan) shift; warpscout_scan "$@" ;;
    --logs) show_logs ;;
    --xray) print_xray ;;
    --zapret) print_zapret ;;
    --wg-paste|--wg-stdin) wg_paste_to_json ;;
    --wg-json|--wg-convert) wg_conf_to_json "${2:-}" ;;
    --remove) remove_safe ;;
    --purge) purge_all ;;
    --memo) print_memo_full ;;
    --commands) print_commands ;;
    --version|-v) echo "warpwp v$VERSION" ;;
    -h|--help) print_commands ;;
    "") menu ;;
    *) err "Неизвестная опция: $1"; print_commands; return 1 ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi

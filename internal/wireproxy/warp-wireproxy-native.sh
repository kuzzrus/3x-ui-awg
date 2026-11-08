#!/usr/bin/env bash
# warp-wireproxy-native.sh
# Полностью неинтерактивный установщик Cloudflare WARP + wireproxy + SOCKS5 для 3x-ui/Xray.
# В режиме --check не запускает apt update/install, чтобы cron/timer были лёгкими.

set -Eeuo pipefail

VERSION="1.2.6"
SOCKS_HOST="127.0.0.1"
SOCKS_PORT="40000"
SOCKS_HOST_EXPLICIT="0"
SOCKS_PORT_EXPLICIT="0"
# Теневой (scan) инстанс wireproxy: перебор кандидатов гоняется через него,
# а не через боевой systemd-сервис, чтобы не рвать активные соединения.
SCAN_SOCKS_HOST="127.0.0.1"
SCAN_SOCKS_PORT=""
SCAN_COUNT="50"
USE_CUSTOM_ENDPOINTS="0"
FORCE_REGISTER="0"
CHECK_ONLY="0"
SCANNER="${WARPWP_SCANNER:-native}"
WARPSCOUT_BIN="${WARPWP_WARPSCOUT_BIN:-warpscout}"
WARPSCOUT_JOBS="${WARPWP_WARPSCOUT_JOBS:-6}"
STABILITY_PROBES="${WARPWP_STABILITY_PROBES:-5}"
NODE_ALLOW="${WARPWP_NODE:-}"
NODE_DENY="${WARPWP_AVOID_NODE:-}"
COUNTRY_ALLOW="${WARPWP_COUNTRY:-}"
COUNTRY_DENY="${WARPWP_AVOID_COUNTRY:-}"
POLICY_MODE="${WARPWP_POLICY_MODE:-prefer}"

# Сколько рабочих endpoint'ов достаточно, чтобы прекратить перебор и выбрать
# лучший из найденных. Без этого --deep-scan гонял все 150 полных проверок
# даже когда рабочий нашёлся на первой.
ENOUGH_GOOD="3"
PROBE_TIMEOUT="8"     # curl-таймаут при переборе кандидатов
FINAL_TIMEOUT="15"    # curl-таймаут финальной проверки
PORT_WAIT_TRIES="30"  # 30 x 0.1s = 3s ожидания, пока wireproxy займёт порт
# Единственный HTTP-запрос через SOCKS5 может провалиться из-за случайной
# заминки (потеря пакета, медленный DNS через WARP), а не потому что туннель
# правда мёртв. Без повторной попытки такая заминка запускала полный перебор
# endpoint'ов и несколько restart wireproxy на ровном месте.
QUICK_CHECK_RETRIES="2"      # попыток quick_warp_check перед тем как считать WARP мёртвым
QUICK_CHECK_RETRY_DELAY="2"  # секунд паузы между попытками quick_warp_check

WG_DIR="/etc/wireguard"
WARP_CONF="$WG_DIR/warp.wireproxy.conf"
LEGACY_WARP_CONF="$WG_DIR/warp.conf"
PROXY_CONF="$WG_DIR/proxy.conf"
ACCOUNT_JSON="$WG_DIR/warp-account.json"
PRIVATE_KEY_FILE="$WG_DIR/warp-private.key"
GOOD_ENDPOINTS_FILE="$WG_DIR/warp-endpoints.good"
BAD_ENDPOINTS_FILE="$WG_DIR/warp-endpoints.bad"
SERVICE_FILE="/etc/systemd/system/wireproxy.service"
TEST_URL="https://www.cloudflare.com/cdn-cgi/trace"
TMP_DIR=""
RESULT_FILE=""
CANDIDATES_FILE=""
WARPSCOUT_ACCOUNT_FILE=""
SCAN_PROXY_CONF=""
SCAN_PID_FILE=""
INTERNAL_LOCK_FILE="/var/lock/warpwp-native.lock"
INTERNAL_LOCK_HELD="0"
SCAN_TRANSACTION_ACTIVE="0"
SCAN_ORIGINAL_ENDPOINT=""
REGISTRATION_TRANSACTION_ACTIVE="0"
REGISTRATION_TRANSACTION_DIR=""
INSTALL_TRANSACTION_ACTIVE="0"
INSTALL_TRANSACTION_DIR=""
INSTALL_BACKUP_ROOT="${INSTALL_BACKUP_ROOT:-/root/warp-wireproxy-native-backup}"

BEST_ENDPOINT=""
BEST_TIME=""
BEST_TRACE=""
BEST_COLO=""
BEST_LOC=""
BEST_LOSS=""
BEST_STABLE=""
BEST_SCANNER=""
LAST_POLICY_MATCH="1"
LAST_STABILITY_LOSS="0"
LAST_STABILITY_TORN="0"
LAST_STABILITY_TIME=""
CURRENT_SCANNER="native"
ZAPRET_PORTS="443,2408,1843,1010,500,1701,4500,4443,8443,8095"

WARP_PREFIXES=("162.159.192" "162.159.193" "162.159.194" "162.159.195" "188.114.96" "188.114.97" "188.114.98" "188.114.99" "8.34.146" "8.39.214" "8.39.204" "8.6.112" "8.35.211" "8.39.125" "8.47.69")
WARP_PORTS=(500 854 859 864 878 880 890 891 894 903 908 928 934 939 942 943 945 946 955 968 987 988 1002 1010 1014 1018 1070 1074 1180 1387 1701 1843 2371 2408 2506 3138 3476 3581 3854 4177 4198 4233 4500 5279 5956 7103 7152 7156 7281 7559 8319 8742 8854 8886)
CUSTOM_ENDPOINTS=()

# Диагностика идёт в stderr: иначе command substitution вокруг функций,
# которые логируют, затягивает сообщения в возвращаемое значение.
log()  { printf '\033[1;36m[ИНФО]\033[0m %s\n' "$*" >&2; }
ok()   { printf '\033[1;32m[ОК]\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33m[ВНИМАНИЕ]\033[0m %s\n' "$*" >&2; }
err()  { printf '\033[1;31m[ОШИБКА]\033[0m %s\n' "$*" >&2; }

usage() {
  cat <<EOF_USAGE
Использование:
  bash $0 [опции]

Опции:
  --check                   Только проверить WARP. Если warp=on нет — пересканировать и подменить endpoint.
  --port <порт>             Локальный SOCKS5-порт. По умолчанию: 40000
  --host <хост>             Bind-адрес SOCKS5. По умолчанию: 127.0.0.1
  --scan-count <число>      Сколько случайных endpoint'ов проверить. По умолчанию: 50
  --enough-good <число>     Сколько рабочих endpoint'ов набрать, чтобы прекратить
                            перебор и выбрать лучший из них. По умолчанию: 3
  --scanner <режим>         native, warpscout или auto. По умолчанию: native
  --warpscout-bin <путь>    Путь к бинарнику WARPSCOUT. По умолчанию: warpscout
  --warpscout-jobs <число>  Параллельные туннели WARPSCOUT. По умолчанию: 6
  --stability-probes <N>    Проверок нового endpoint'а, минимум 3. По умолчанию: 5
  --node <коды>             Разрешённые Cloudflare colo, например HEL,ARN
  --avoid-node <коды>       Запрещённые Cloudflare colo, например DME
  --country <коды>          Разрешённые выходные страны, например DE,NL
  --avoid-country <коды>    Запрещённые выходные страны, например RU
  --policy-mode <режим>     prefer (fallback разрешён) или strict
  --ports "список"          Порты для сканирования, через пробел или запятую
  --endpoints "список"      Проверить конкретные endpoint'ы вместо автосканирования
  --force-register          Создать новый WARP-аккаунт, даже если старый уже есть
  --version                 Показать версию
  -h, --help                Показать справку
EOF_USAGE
}

require_option_value() {
  local option="$1" value="${2:-}"
  [[ -n "$value" && "$value" != --* ]] || {
    err "Для $option требуется непустое значение."
    exit 1
  }
}

valid_port() {
  [[ "$1" =~ ^[0-9]{1,5}$ ]] && ((10#$1 >= 1 && 10#$1 <= 65535))
}

decimal_number() {
  local value="$1"
  [[ "$value" =~ ^[0-9]{1,9}$ ]] || return 1
  printf '%s' "$((10#$value))"
}

valid_ipv4() {
  local value="$1" part
  local -a octets
  [[ "$value" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1
  IFS=. read -r -a octets <<< "$value"
  [[ "${#octets[@]}" -eq 4 ]] || return 1
  for part in "${octets[@]}"; do
    [[ "$part" =~ ^[0-9]{1,3}$ ]] && ((10#$part <= 255)) || return 1
  done
}

valid_hostname() {
  local value="$1" label
  local -a labels
  [[ -n "$value" && "${#value}" -le 253 && "$value" != .* && "$value" != *. ]] || return 1
  IFS=. read -r -a labels <<< "$value"
  for label in "${labels[@]}"; do
    [[ -n "$label" && "${#label}" -le 63 && "$label" =~ ^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$ ]] || return 1
  done
}

valid_host() {
  local value="$1" inner
  if [[ "$value" =~ ^\[([^][]+)\]$ ]]; then
    inner="${BASH_REMATCH[1]}"
    [[ "$inner" == *:* && "$inner" =~ ^[0-9A-Fa-f:.%A-Za-z_-]+$ ]]
  elif [[ "$value" == *:* ]]; then
    return 1
  elif [[ "$value" =~ ^[0-9.]+$ ]]; then
    valid_ipv4 "$value"
  else
    valid_hostname "$value"
  fi
}

valid_endpoint() {
  local endpoint="$1" host port
  if [[ "$endpoint" =~ ^\[([^][]+)\]:([0-9]+)$ ]]; then
    host="[${BASH_REMATCH[1]}]"
    port="${BASH_REMATCH[2]}"
  elif [[ "$endpoint" =~ ^([^:]+):([0-9]+)$ ]]; then
    host="${BASH_REMATCH[1]}"
    port="${BASH_REMATCH[2]}"
  else
    return 1
  fi
  valid_host "$host" && valid_port "$port"
}

validate_runtime_options() {
  local port endpoint value i
  value="$(decimal_number "$SCAN_COUNT" 2>/dev/null || true)"
  [[ -n "$value" && "$value" -ge 1 && "$value" -le 10000 ]] || { err "--scan-count должен быть числом от 1 до 10000."; return 1; }
  SCAN_COUNT="$value"
  value="$(decimal_number "$ENOUGH_GOOD" 2>/dev/null || true)"
  [[ -n "$value" && "$value" -ge 1 && "$value" -le 10000 ]] || { err "--enough-good должен быть числом от 1 до 10000."; return 1; }
  ENOUGH_GOOD="$value"
  value="$(decimal_number "$WARPSCOUT_JOBS" 2>/dev/null || true)"
  [[ -n "$value" && "$value" -ge 1 && "$value" -le 256 ]] || { err "--warpscout-jobs должен быть числом от 1 до 256."; return 1; }
  WARPSCOUT_JOBS="$value"
  value="$(decimal_number "$STABILITY_PROBES" 2>/dev/null || true)"
  [[ -n "$value" && "$value" -ge 3 && "$value" -le 100 ]] || { err "--stability-probes должен быть числом от 3 до 100."; return 1; }
  STABILITY_PROBES="$value"
  valid_host "$SOCKS_HOST" || { err "--host должен быть IPv4, hostname или IPv6 в квадратных скобках."; return 1; }
  valid_port "$SOCKS_PORT" || { err "--port должен быть числом от 1 до 65535."; return 1; }
  SOCKS_PORT="$((10#$SOCKS_PORT))"
  [[ "${#WARP_PORTS[@]}" -gt 0 ]] || { err "--ports не может быть пустым."; return 1; }
  for i in "${!WARP_PORTS[@]}"; do
    port="${WARP_PORTS[$i]}"
    valid_port "$port" || { err "Некорректный порт сканирования: $port"; return 1; }
    WARP_PORTS[$i]="$((10#$port))"
  done
  if [[ "$USE_CUSTOM_ENDPOINTS" == "1" ]]; then
    [[ "${#CUSTOM_ENDPOINTS[@]}" -gt 0 ]] || { err "--endpoints не может быть пустым."; return 1; }
    for endpoint in "${CUSTOM_ENDPOINTS[@]}"; do
      valid_endpoint "$endpoint" || { err "Некорректный endpoint: $endpoint"; return 1; }
    done
  fi
  case "$SCANNER" in native|warpscout|auto) ;; *) err "--scanner: используй native, warpscout или auto."; return 1 ;; esac
  case "$POLICY_MODE" in prefer|strict) ;; *) err "--policy-mode: используй prefer или strict."; return 1 ;; esac
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --check) CHECK_ONLY="1"; shift ;;
      --port) require_option_value "$1" "${2:-}"; SOCKS_PORT="$2"; SOCKS_PORT_EXPLICIT="1"; shift 2 ;;
      --host) require_option_value "$1" "${2:-}"; SOCKS_HOST="$2"; SOCKS_HOST_EXPLICIT="1"; shift 2 ;;
      --scan-count) require_option_value "$1" "${2:-}"; SCAN_COUNT="$2"; shift 2 ;;
      --ports)
        require_option_value "$1" "${2:-}"
        local raw_ports="$2"
        raw_ports="${raw_ports//,/ }"
        read -r -a WARP_PORTS <<< "$raw_ports"
        shift 2
        ;;
      --endpoints)
        require_option_value "$1" "${2:-}"
        local raw_eps="$2"
        raw_eps="${raw_eps//,/ }"
        read -r -a CUSTOM_ENDPOINTS <<< "$raw_eps"
        USE_CUSTOM_ENDPOINTS="1"
        shift 2
        ;;
      --enough-good) require_option_value "$1" "${2:-}"; ENOUGH_GOOD="$2"; shift 2 ;;
      --scanner) require_option_value "$1" "${2:-}"; SCANNER="$2"; shift 2 ;;
      --warpscout-bin) require_option_value "$1" "${2:-}"; WARPSCOUT_BIN="$2"; shift 2 ;;
      --warpscout-jobs) require_option_value "$1" "${2:-}"; WARPSCOUT_JOBS="$2"; shift 2 ;;
      --stability-probes) require_option_value "$1" "${2:-}"; STABILITY_PROBES="$2"; shift 2 ;;
      --node) require_option_value "$1" "${2:-}"; NODE_ALLOW="$2"; shift 2 ;;
      --avoid-node) require_option_value "$1" "${2:-}"; NODE_DENY="$2"; shift 2 ;;
      --country) require_option_value "$1" "${2:-}"; COUNTRY_ALLOW="$2"; shift 2 ;;
      --avoid-country) require_option_value "$1" "${2:-}"; COUNTRY_DENY="$2"; shift 2 ;;
      --policy-mode) require_option_value "$1" "${2:-}"; POLICY_MODE="$2"; shift 2 ;;
      --force-register) FORCE_REGISTER="1"; shift ;;
      --version|-v) echo "warp-wireproxy-native.sh v$VERSION"; exit 0 ;;
      -h|--help) usage; exit 0 ;;
      *) err "Неизвестная опция: $1"; usage; exit 1 ;;
    esac
  done

  validate_runtime_options || exit 1
  NODE_ALLOW="$(normalize_code_list "$NODE_ALLOW")"
  NODE_DENY="$(normalize_code_list "$NODE_DENY")"
  COUNTRY_ALLOW="$(normalize_code_list "$COUNTRY_ALLOW")"
  COUNTRY_DENY="$(normalize_code_list "$COUNTRY_DENY")"
}

normalize_code_list() {
  local raw="${1//,/ }" item out=""
  for item in $raw; do
    item="${item^^}"
    [[ "$item" =~ ^[A-Z0-9]{2,4}$ ]] || { err "Некорректный код в policy: $item"; return 1; }
    [[ ",$out," == *",$item,"* ]] && continue
    out="${out:+$out,}$item"
  done
  printf '%s' "$out"
}

list_has_code() {
  local needle="${1^^}" raw="${2//,/ }" item
  for item in $raw; do [[ "${item^^}" == "$needle" ]] && return 0; done
  return 1
}

policy_configured() {
  [[ -n "$NODE_ALLOW$NODE_DENY$COUNTRY_ALLOW$COUNTRY_DENY" ]]
}

endpoint_matches_policy() {
  local colo="${1^^}" country="${2^^}"
  [[ -z "$NODE_ALLOW$NODE_DENY" || -n "$colo" ]] || return 1
  [[ -z "$COUNTRY_ALLOW$COUNTRY_DENY" || -n "$country" ]] || return 1
  [[ -z "$NODE_ALLOW" ]] || list_has_code "$colo" "$NODE_ALLOW" || return 1
  [[ -z "$COUNTRY_ALLOW" ]] || list_has_code "$country" "$COUNTRY_ALLOW" || return 1
  [[ -z "$NODE_DENY" ]] || ! list_has_code "$colo" "$NODE_DENY" || return 1
  [[ -z "$COUNTRY_DENY" ]] || ! list_has_code "$country" "$COUNTRY_DENY" || return 1
  return 0
}

policy_summary() {
  local out=""
  [[ -n "$NODE_ALLOW" ]] && out+=" node=$NODE_ALLOW"
  [[ -n "$NODE_DENY" ]] && out+=" avoid-node=$NODE_DENY"
  [[ -n "$COUNTRY_ALLOW" ]] && out+=" country=$COUNTRY_ALLOW"
  [[ -n "$COUNTRY_DENY" ]] && out+=" avoid-country=$COUNTRY_DENY"
  [[ -n "$out" ]] && printf '%s mode=%s' "${out# }" "$POLICY_MODE" || printf 'none'
}

require_root() { [[ "${EUID}" -eq 0 ]] || { err "Запусти от root."; exit 1; }; }

init_runtime() {
  umask 077
  TMP_DIR="$(mktemp -d /tmp/warp-wireproxy-native.XXXXXX)" || {
    err "Не удалось создать защищённый временный каталог."
    exit 1
  }
  RESULT_FILE="$TMP_DIR/results.tsv"
  CANDIDATES_FILE="$TMP_DIR/candidates.txt"
  WARPSCOUT_ACCOUNT_FILE="$TMP_DIR/warpscout-account.json"
  SCAN_PROXY_CONF="$TMP_DIR/scan-proxy.conf"
  SCAN_PID_FILE="$TMP_DIR/scan-wireproxy.pid"
  : > "$RESULT_FILE"
  : > "$CANDIDATES_FILE"
}

acquire_internal_lock() {
  command -v flock >/dev/null 2>&1 || {
    err "Не найден flock: установи util-linux."
    return 1
  }
  mkdir -p "$(dirname "$INTERNAL_LOCK_FILE")"
  exec 9>"$INTERNAL_LOCK_FILE"
  if ! flock -n 9; then
    err "Другая установка или проверка WARP уже выполняется."
    return 75
  fi
  INTERNAL_LOCK_HELD="1"
}

check_deps_light() {
  local missing=()
  for cmd in curl grep sed awk python3 mktemp flock systemctl ss sort head cut date ip; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done
  if [[ "${#missing[@]}" -gt 0 ]]; then
    err "Не найдены команды для --check: ${missing[*]}"
    err "Запусти обычную установку без --check, чтобы поставить зависимости."
    exit 1
  fi
}

install_deps_apt() {
  apt-get update -y || warn "apt update завершился с ошибкой. Продолжаю: часто причина в сломанном стороннем репозитории."
  DEBIAN_FRONTEND=noninteractive apt-get install -y curl wget ca-certificates grep sed gawk coreutils util-linux iproute2 systemd wireguard-tools python3 tar unzip git || { err "Не удалось установить зависимости через apt."; exit 1; }
  if ! command -v awk >/dev/null 2>&1 && command -v gawk >/dev/null 2>&1; then ln -sf "$(command -v gawk)" /usr/local/bin/awk; fi
}
install_deps_dnf() { dnf install -y curl wget ca-certificates grep sed gawk coreutils util-linux iproute systemd wireguard-tools python3 tar unzip git; }
install_deps_yum() { yum install -y curl wget ca-certificates grep sed gawk coreutils util-linux iproute systemd wireguard-tools python3 tar unzip git; }
install_deps_apk() { apk add --no-cache curl wget ca-certificates grep sed gawk coreutils util-linux iproute2 wireguard-tools python3 tar unzip git; }
install_deps() {
  log "Проверяю зависимости..."
  if command -v apt-get >/dev/null 2>&1; then install_deps_apt
  elif command -v dnf >/dev/null 2>&1; then install_deps_dnf
  elif command -v yum >/dev/null 2>&1; then install_deps_yum
  elif command -v apk >/dev/null 2>&1; then install_deps_apk
  else warn "Неизвестный пакетный менеджер. Убедись, что curl, python3, wg и systemctl установлены."; fi
  local missing=()
  for cmd in curl wget grep sed awk python3 mktemp flock wg ip ss systemctl sort head cut uniq tar; do command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd"); done
  [[ "${#missing[@]}" -eq 0 ]] || { err "Не найдены команды: ${missing[*]}"; exit 1; }
  ok "Зависимости готовы."
}

find_wireproxy_bin() {
  if command -v wireproxy >/dev/null 2>&1; then command -v wireproxy; return 0; fi
  for p in /usr/local/bin/wireproxy /usr/bin/wireproxy /opt/bin/wireproxy; do [[ -x "$p" ]] && echo "$p" && return 0; done
  return 1
}

select_wireproxy_release_url() {
  local arch="$1" metadata_file="$2"
  "${PYTHON_BIN:-python3}" - "$arch" "$metadata_file" <<'PY'
import json, pathlib, re, sys

machine = sys.argv[1].lower()
asset_arches = {
    "x86_64": ("amd64",),
    "amd64": ("amd64",),
    "aarch64": ("arm64",),
    "arm64": ("arm64",),
    "armv7l": ("arm", "armv7", "armv7l", "armhf"),
    "armv7": ("arm", "armv7", "armv7l", "armhf"),
    "armhf": ("arm", "armv7", "armv7l", "armhf"),
}.get(machine, (machine,))

try:
    data = json.loads(pathlib.Path(sys.argv[2]).read_text(encoding="utf-8"))
except (OSError, ValueError):
    sys.exit(1)

for asset in data.get("assets", []):
    name = str(asset.get("name", "")).lower()
    url = str(asset.get("browser_download_url", ""))
    # Match the release filename's architecture component exactly.  In
    # particular, linux_arm must never be selected by matching linux_arm64.
    match = re.search(r"(?:^|[_-])linux[_-]([a-z0-9]+)(?:[_.-]|$)", name)
    if match and match.group(1) in asset_arches and url:
        print(url)
        sys.exit(0)
sys.exit(1)
PY
}

install_wireproxy_from_release() {
  local arch url metadata tmp tmpdir bin
  arch="$(uname -m)"
  log "Пытаюсь скачать wireproxy из GitHub Releases для архитектуры: $arch"
  metadata="$TMP_DIR/wireproxy-release.json"
  curl -fsSL --max-time 20 'https://api.github.com/repos/pufferffish/wireproxy/releases/latest' -o "$metadata" || return 1
  url="$(select_wireproxy_release_url "$arch" "$metadata")" || true
  [[ -z "$url" ]] && return 1
  tmp="$TMP_DIR/wireproxy-download"; tmpdir="$TMP_DIR/wireproxy-extract"; mkdir -p "$tmpdir"
  curl -fL "$url" -o "$tmp"
  if [[ "$url" == *.zip ]]; then unzip -q "$tmp" -d "$tmpdir"; elif [[ "$url" == *.tar.gz || "$url" == *.tgz ]]; then tar -xzf "$tmp" -C "$tmpdir"; else cp "$tmp" "$tmpdir/wireproxy"; fi
  bin="$(find "$tmpdir" -type f \( -name 'wireproxy' -o -name 'wireproxy-*' \) | head -n1 || true)"
  [[ -n "$bin" ]] || return 1
  install -m 0755 "$bin" /usr/local/bin/wireproxy || return 1
  rm -rf "$tmp" "$tmpdir"
  ok "wireproxy установлен: /usr/local/bin/wireproxy"
}
install_wireproxy_from_go() {
  warn "Готового релиза wireproxy не нашёл. Пробую собрать через Go."
  if command -v apt-get >/dev/null 2>&1; then apt-get update -y || true; DEBIAN_FRONTEND=noninteractive apt-get install -y golang-go
  elif command -v dnf >/dev/null 2>&1; then dnf install -y golang
  elif command -v yum >/dev/null 2>&1; then yum install -y golang
  elif command -v apk >/dev/null 2>&1; then apk add --no-cache go; fi
  command -v go >/dev/null 2>&1 || { err "Go не установлен, собрать wireproxy не удалось."; exit 1; }
  GOBIN=/usr/local/bin go install github.com/pufferffish/wireproxy/cmd/wireproxy@latest
  find_wireproxy_bin >/dev/null 2>&1 || { err "wireproxy не появился после go install."; exit 1; }
}
ensure_wireproxy_installed() { find_wireproxy_bin >/dev/null 2>&1 && { ok "wireproxy уже установлен: $(find_wireproxy_bin)"; return 0; }; install_wireproxy_from_release || install_wireproxy_from_go; }

snapshot_install_file() {
  local target="$1" name="$2"
  [[ -n "$INSTALL_TRANSACTION_DIR" ]] || return 1
  if [[ -e "$target" || -L "$target" ]]; then
    : > "$INSTALL_TRANSACTION_DIR/$name.existed" || return 1
    cp -a -- "$target" "$INSTALL_TRANSACTION_DIR/$name.backup" || return 1
  fi
}

restore_install_file() {
  local target="$1" name="$2"
  if [[ -f "$INSTALL_TRANSACTION_DIR/$name.existed" ]]; then
    [[ -e "$INSTALL_TRANSACTION_DIR/$name.backup" || -L "$INSTALL_TRANSACTION_DIR/$name.backup" ]] || return 1
    cp -a -- "$INSTALL_TRANSACTION_DIR/$name.backup" "$target"
  else
    rm -f -- "$target"
  fi
}

begin_install_transaction() {
  local target name
  mkdir -p "$INSTALL_BACKUP_ROOT" || return 1
  INSTALL_TRANSACTION_DIR="$(mktemp -d "$INSTALL_BACKUP_ROOT/transaction.XXXXXX")" || return 1
  chmod 700 "$INSTALL_TRANSACTION_DIR" || { rm -rf -- "$INSTALL_TRANSACTION_DIR"; INSTALL_TRANSACTION_DIR=""; return 1; }
  INSTALL_TRANSACTION_ACTIVE="0"
  while IFS='|' read -r target name; do
    snapshot_install_file "$target" "$name" || {
      err "Не удалось создать резервную копию $target; установка не начата."
      rm -rf -- "$INSTALL_TRANSACTION_DIR"
      INSTALL_TRANSACTION_DIR=""
      return 1
    }
  done <<EOF_SNAPSHOTS
$WARP_CONF|warp-conf
$LEGACY_WARP_CONF|legacy-warp-conf
$PROXY_CONF|proxy-conf
$ACCOUNT_JSON|account
$PRIVATE_KEY_FILE|private-key
$SERVICE_FILE|service
$GOOD_ENDPOINTS_FILE|good-endpoints
$BAD_ENDPOINTS_FILE|bad-endpoints
EOF_SNAPSHOTS
  systemctl is-active --quiet wireproxy 2>/dev/null && : > "$INSTALL_TRANSACTION_DIR/service.was-active" || true
  systemctl is-enabled --quiet wireproxy 2>/dev/null && : > "$INSTALL_TRANSACTION_DIR/service.was-enabled" || true
  INSTALL_TRANSACTION_ACTIVE="1"
}

rollback_install_transaction() {
  local failed=0 target name
  [[ "$INSTALL_TRANSACTION_ACTIVE" == "1" && -n "$INSTALL_TRANSACTION_DIR" ]] || return 0
  warn "Откатываю незавершённую установку WARP/wireproxy к предыдущей рабочей конфигурации."
  systemctl stop wireproxy 2>/dev/null || true
  while IFS='|' read -r target name; do
    restore_install_file "$target" "$name" || { err "Не удалось восстановить $target."; failed=1; }
  done <<EOF_RESTORE
$WARP_CONF|warp-conf
$LEGACY_WARP_CONF|legacy-warp-conf
$PROXY_CONF|proxy-conf
$ACCOUNT_JSON|account
$PRIVATE_KEY_FILE|private-key
$GOOD_ENDPOINTS_FILE|good-endpoints
$BAD_ENDPOINTS_FILE|bad-endpoints
$SERVICE_FILE|service
EOF_RESTORE
  systemctl daemon-reload 2>/dev/null || failed=1
  if [[ -f "$INSTALL_TRANSACTION_DIR/service.was-enabled" ]]; then systemctl enable wireproxy >/dev/null 2>&1 || failed=1; else systemctl disable wireproxy >/dev/null 2>&1 || true; fi
  if [[ -f "$INSTALL_TRANSACTION_DIR/service.was-active" ]]; then systemctl start wireproxy >/dev/null 2>&1 || failed=1; else systemctl stop wireproxy >/dev/null 2>&1 || true; fi
  if [[ "$failed" == "1" ]]; then
    err "Откат завершился с ошибкой; резервная копия сохранена: $INSTALL_TRANSACTION_DIR"
    return 1
  fi
  INSTALL_TRANSACTION_ACTIVE="0"
  rm -rf -- "$INSTALL_TRANSACTION_DIR"
  INSTALL_TRANSACTION_DIR=""
}

commit_install_transaction() {
  [[ "$INSTALL_TRANSACTION_ACTIVE" == "1" ]] || return 0
  INSTALL_TRANSACTION_ACTIVE="0"
  rm -rf -- "$INSTALL_TRANSACTION_DIR"
  INSTALL_TRANSACTION_DIR=""
}

backup_existing() { begin_install_transaction; }

routing_guard_report() {
  local family label table
  echo "--- WARP routing guard ---"
  if ip link show warp >/dev/null 2>&1; then warn "Найден системный интерфейс warp. Он может ломать входящие SSH/443."; else ok "interface warp отсутствует."; fi
  for family in -4 -6; do
    [[ "$family" == "-4" ]] && label="IPv4" || label="IPv6"
    if ip "$family" rule show 2>/dev/null | grep -Eq 'lookup (51820|warp)([[:space:]]|$)'; then
      warn "Найдены policy rules WARP ($label):"
      ip "$family" rule show 2>/dev/null | grep -E 'lookup (51820|warp)([[:space:]]|$)' || true
    else
      ok "policy rules WARP ($label) не найдены."
    fi
    for table in 51820 warp; do
      if ip "$family" route show table "$table" 2>/dev/null | grep -q .; then
        warn "Таблица $table ($label) не пустая:"
        ip "$family" route show table "$table" 2>/dev/null || true
      fi
    done
  done
  for svc in wg-quick@warp wg-quick@wgcf warp-svc; do
    if systemctl is-active --quiet "$svc" 2>/dev/null || systemctl is-enabled --quiet "$svc" 2>/dev/null; then warn "Найден конфликтующий service: $svc"; fi
  done
}

cleanup_system_warp_routes() {
  warn "Автоматическая очистка system WARP отключена: таблица 51820 может принадлежать чужому WireGuard-туннелю. Установка использует только wireproxy SOCKS5 и не меняет ip rule/route."
  routing_guard_report
}

validate_account_pair() {
  local account_file="$1" private_file="$2"
  [[ -s "$account_file" && -s "$private_file" ]] || return 1
  wg pubkey < "$private_file" >/dev/null 2>&1 || return 1
  python3 - "$account_file" <<'PY'
import json, sys
with open(sys.argv[1], encoding='utf-8') as fh:
    data = json.load(fh)
if not isinstance(data, dict) or not all(data.get(k) for k in ('id', 'token')):
    raise SystemExit(1)
config = data.get('config')
if not isinstance(config, dict):
    raise SystemExit(1)
interface = config.get('interface')
addresses = interface.get('addresses') if isinstance(interface, dict) else None
if not isinstance(addresses, dict) or not all(isinstance(addresses.get(k), str) and addresses[k] for k in ('v4', 'v6')):
    raise SystemExit(1)
peers = config.get('peers')
if not isinstance(peers, list) or not peers or not isinstance(peers[0], dict):
    raise SystemExit(1)
endpoint = peers[0].get('endpoint')
if not isinstance(peers[0].get('public_key'), str) or not peers[0]['public_key']:
    raise SystemExit(1)
if not isinstance(endpoint, dict) or not isinstance(endpoint.get('host'), str) or not endpoint['host']:
    raise SystemExit(1)
PY
}

rollback_registration_transaction() {
  local failed=0
  [[ "$REGISTRATION_TRANSACTION_ACTIVE" == "1" && -n "$REGISTRATION_TRANSACTION_DIR" ]] || return 0
  warn "Откатываю незавершённую замену WARP account/private key."
  if [[ -f "$REGISTRATION_TRANSACTION_DIR/private.replaced" ]]; then
    if [[ -f "$REGISTRATION_TRANSACTION_DIR/private.original-existed" ]]; then
      [[ -f "$REGISTRATION_TRANSACTION_DIR/private.backup-ready" ]] && cp -a "$REGISTRATION_TRANSACTION_DIR/private.backup" "$PRIVATE_KEY_FILE" 2>/dev/null || failed=1
    else
      rm -f "$PRIVATE_KEY_FILE" 2>/dev/null || failed=1
    fi
  fi
  if [[ -f "$REGISTRATION_TRANSACTION_DIR/account.replaced" ]]; then
    if [[ -f "$REGISTRATION_TRANSACTION_DIR/account.original-existed" ]]; then
      [[ -f "$REGISTRATION_TRANSACTION_DIR/account.backup-ready" ]] && cp -a "$REGISTRATION_TRANSACTION_DIR/account.backup" "$ACCOUNT_JSON" 2>/dev/null || failed=1
    else
      rm -f "$ACCOUNT_JSON" 2>/dev/null || failed=1
    fi
  fi
  if [[ "$failed" == "1" ]]; then
    err "Не удалось полностью восстановить WARP account/private key; backup сохранён: $REGISTRATION_TRANSACTION_DIR"
    return 1
  fi
  REGISTRATION_TRANSACTION_ACTIVE="0"
  rm -rf -- "$REGISTRATION_TRANSACTION_DIR" 2>/dev/null || true
  REGISTRATION_TRANSACTION_DIR=""
}

register_warp_account() {
  mkdir -p "$WG_DIR" || return 1
  chmod 700 "$WG_DIR" || return 1
  if [[ "$FORCE_REGISTER" != "1" && -f "$ACCOUNT_JSON" && -f "$PRIVATE_KEY_FILE" ]]; then
    if validate_account_pair "$ACCOUNT_JSON" "$PRIVATE_KEY_FILE"; then
      ok "WARP-аккаунт уже есть. Использую существующий $ACCOUNT_JSON"
      return 0
    fi
    warn "Существующая пара WARP account/private key повреждена; регистрирую новую."
  fi
  log "Генерирую WireGuard ключи и регистрирую WARP-устройство через API Cloudflare..."
  local private_key public_key tos body staged_key staged_account txn_dir
  staged_key="$TMP_DIR/warp-private.key.new"
  staged_account="$TMP_DIR/warp-account.json.new"
  private_key="$(wg genkey)" || { err "Не удалось создать WireGuard private key."; return 1; }
  public_key="$(printf '%s' "$private_key" | wg pubkey)" || { err "Не удалось получить WireGuard public key."; return 1; }
  tos="$(date -u +%Y-%m-%dT%H:%M:%S.000Z)"
  printf '%s\n' "$private_key" > "$staged_key" || return 1
  chmod 600 "$staged_key" || return 1
  body="$(python3 - "$public_key" "$tos" <<'PY'
import json, sys
pub, tos = sys.argv[1], sys.argv[2]
print(json.dumps({'key': pub, 'install_id': '', 'fcm_token': '', 'tos': tos, 'type': 'Android', 'model': 'PC', 'locale': 'en_US'}))
PY
  )" || return 1
  if ! curl --connect-timeout 10 --max-time 30 -fsSL -X POST 'https://api.cloudflareclient.com/v0a2158/reg' -H 'Content-Type: application/json; charset=UTF-8' -H 'User-Agent: okhttp/3.12.1' -H 'CF-Client-Version: a-6.11-2223' --data "$body" > "$staged_account"; then
    err "Cloudflare API не зарегистрировал WARP-устройство; рабочие ключи не изменены."
    return 1
  fi
  chmod 600 "$staged_account" || return 1
  validate_account_pair "$staged_account" "$staged_key" || {
    err "Cloudflare API вернул неполный account; рабочие ключи не изменены."
    return 1
  }

  txn_dir="$(mktemp -d "$WG_DIR/.warp-registration.XXXXXX")" || return 1
  chmod 700 "$txn_dir" || { rm -rf -- "$txn_dir"; return 1; }
  REGISTRATION_TRANSACTION_DIR="$txn_dir"
  REGISTRATION_TRANSACTION_ACTIVE="0"
  if [[ -f "$PRIVATE_KEY_FILE" ]]; then
    : > "$txn_dir/private.original-existed" || { rm -rf -- "$txn_dir"; REGISTRATION_TRANSACTION_DIR=""; return 1; }
    if ! cp -a "$PRIVATE_KEY_FILE" "$txn_dir/private.backup"; then rm -rf -- "$txn_dir"; REGISTRATION_TRANSACTION_DIR=""; return 1; fi
    : > "$txn_dir/private.backup-ready" || { rm -rf -- "$txn_dir"; REGISTRATION_TRANSACTION_DIR=""; return 1; }
  fi
  if [[ -f "$ACCOUNT_JSON" ]]; then
    : > "$txn_dir/account.original-existed" || { rm -rf -- "$txn_dir"; REGISTRATION_TRANSACTION_DIR=""; return 1; }
    if ! cp -a "$ACCOUNT_JSON" "$txn_dir/account.backup"; then rm -rf -- "$txn_dir"; REGISTRATION_TRANSACTION_DIR=""; return 1; fi
    : > "$txn_dir/account.backup-ready" || { rm -rf -- "$txn_dir"; REGISTRATION_TRANSACTION_DIR=""; return 1; }
  fi
  install -m 0600 "$staged_key" "$txn_dir/private.new" || { rm -rf -- "$txn_dir"; REGISTRATION_TRANSACTION_DIR=""; return 1; }
  install -m 0600 "$staged_account" "$txn_dir/account.new" || { rm -rf -- "$txn_dir"; REGISTRATION_TRANSACTION_DIR=""; return 1; }
  : > "$txn_dir/private.replaced" || { rm -rf -- "$txn_dir"; REGISTRATION_TRANSACTION_DIR=""; return 1; }
  : > "$txn_dir/account.replaced" || { rm -rf -- "$txn_dir"; REGISTRATION_TRANSACTION_DIR=""; return 1; }
  REGISTRATION_TRANSACTION_ACTIVE="1"
  mv -f "$txn_dir/private.new" "$PRIVATE_KEY_FILE" || { rollback_registration_transaction; return 1; }
  mv -f "$txn_dir/account.new" "$ACCOUNT_JSON" || { rollback_registration_transaction; return 1; }
  validate_account_pair "$ACCOUNT_JSON" "$PRIVATE_KEY_FILE" || { rollback_registration_transaction; return 1; }
  REGISTRATION_TRANSACTION_ACTIVE="0"
  rm -rf -- "$txn_dir"
  REGISTRATION_TRANSACTION_DIR=""
  ok "WARP-аккаунт зарегистрирован."
}

json_get_config() {
  python3 - "$ACCOUNT_JSON" "$PRIVATE_KEY_FILE" <<'PY'
import json, sys
a=json.load(open(sys.argv[1])); private=open(sys.argv[2]).read().strip(); c=a.get('config',{}); iface=c.get('interface',{}); addresses=iface.get('addresses',{}); peers=c.get('peers',[{}]); peer=peers[0] if peers else {}; endpoint=peer.get('endpoint',{}).get('host') or 'engage.cloudflareclient.com:2408'
print('PRIVATE_KEY='+private)
print('ADDRESS_V4='+addresses.get('v4','172.16.0.2'))
print('ADDRESS_V6='+addresses.get('v6','2606:4700:110:0000:0000:0000:0000:0002'))
print('PEER_PUBLIC_KEY='+peer.get('public_key','bmXOC+F1QSPGQ2ObwTOu6NWKSLW89kykyGw4RrHkGOU='))
print('ENDPOINT='+endpoint)
PY
}

write_configs() {
  local cfg private address_v4 address_v6 peer_public endpoint
  cfg="$(json_get_config)" || return 1
  private="$(echo "$cfg" | awk -F= '/^PRIVATE_KEY=/{print substr($0,index($0,"=")+1)}')"
  address_v4="$(echo "$cfg" | awk -F= '/^ADDRESS_V4=/{print substr($0,index($0,"=")+1)}')"
  address_v6="$(echo "$cfg" | awk -F= '/^ADDRESS_V6=/{print substr($0,index($0,"=")+1)}')"
  peer_public="$(echo "$cfg" | awk -F= '/^PEER_PUBLIC_KEY=/{print substr($0,index($0,"=")+1)}')"
  endpoint="$(echo "$cfg" | awk -F= '/^ENDPOINT=/{print substr($0,index($0,"=")+1)}')"
  cat > "$WARP_CONF" <<EOF_WARP || return 1
# Internal WARP config mirror for WARP WireProxy Manager.
# Do NOT run this file with wg-quick. Use wireproxy.service and $PROXY_CONF.
[Interface]
PrivateKey = $private
Address = $address_v4/32
Address = $address_v6/128
DNS = 1.1.1.1
MTU = 1280
Table = off
PreUp = /bin/sh -c 'echo "ERROR: WARP WireProxy Manager uses wireproxy SOCKS5 only; do not run wg-quick with this config." >&2; exit 1'

[Peer]
PublicKey = $peer_public
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = $endpoint
PersistentKeepalive = 25
EOF_WARP
  chmod 600 "$WARP_CONF" || return 1
  cat > "$PROXY_CONF" <<EOF_PROXY || return 1
[Interface]
PrivateKey = $private
Address = $address_v4/32
Address = $address_v6/128
DNS = 1.1.1.1
MTU = 1280

[Peer]
PublicKey = $peer_public
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = $endpoint
PersistentKeepalive = 25

[Socks5]
BindAddress = $SOCKS_HOST:$SOCKS_PORT
EOF_PROXY
  chmod 600 "$PROXY_CONF" || return 1
  cat > "$LEGACY_WARP_CONF" <<EOF_LEGACY || return 1
# Guard file created by WARP WireProxy Manager.
# This project uses Cloudflare WARP only through wireproxy SOCKS5: $SOCKS_HOST:$SOCKS_PORT
# Do NOT run: wg-quick up warp / systemctl enable --now wg-quick@warp
[Interface]
PrivateKey = $private
Address = $address_v4/32
Address = $address_v6/128
DNS = 1.1.1.1
MTU = 1280
Table = off
PreUp = /bin/sh -c 'echo "ERROR: do not run /etc/wireguard/warp.conf with wg-quick; use wireproxy.service only." >&2; exit 1'

[Peer]
PublicKey = $peer_public
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = $endpoint
PersistentKeepalive = 25
EOF_LEGACY
  chmod 600 "$LEGACY_WARP_CONF" || return 1
  ok "Конфиги созданы. Первичный endpoint: $endpoint"
  ok "Защита от случайного wg-quick@warp включена: $LEGACY_WARP_CONF"
}

set_endpoint() {
  local ep="$1" file
  valid_endpoint "$ep" || { err "Отказ менять конфиг на некорректный endpoint: $ep"; return 1; }
  [[ -f "$PROXY_CONF" ]] || { err "Не найден $PROXY_CONF."; return 1; }
  for file in "$WARP_CONF" "$LEGACY_WARP_CONF" "$PROXY_CONF"; do
    [[ -f "$file" ]] || continue
    grep -qi '^Endpoint[[:space:]]*=' "$file" || { err "В $file отсутствует Endpoint."; return 1; }
    sed -i "s#^Endpoint[[:space:]]*=.*#Endpoint = $ep#I" "$file" || { err "Не удалось обновить Endpoint в $file."; return 1; }
  done
}
create_service() {
  local bin; bin="$(find_wireproxy_bin)"
  cat > "$SERVICE_FILE" <<EOF_SERVICE || return 1
[Unit]
Description=WireProxy for WARP
Documentation=https://github.com/pufferffish/wireproxy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$bin -c $PROXY_CONF
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF_SERVICE
  systemctl daemon-reload || return 1
  systemctl enable wireproxy >/dev/null 2>&1 || return 1
  ok "wireproxy.service создан."
}
ensure_service_exists() { [[ -f "$SERVICE_FILE" ]] || systemctl list-unit-files 2>/dev/null | grep -q '^wireproxy\.service' && return 0; find_wireproxy_bin >/dev/null 2>&1 && [[ -f "$PROXY_CONF" ]] && { create_service; return 0; }; return 1; }
# wireproxy занимает порт за доли секунды. Опрос вместо фиксированного
# sleep 2 экономит почти всё время перебора кандидатов.
socks_address() { printf '%s:%s' "${1:-$SOCKS_HOST}" "${2:-$SOCKS_PORT}"; }
socks_proxy_url() {
  local host="${1:-$SOCKS_HOST}" port="${2:-$SOCKS_PORT}"
  host="${host#[}"
  host="${host%]}"
  case "$host" in 0.0.0.0) host="127.0.0.1" ;; ::) host="::1" ;; esac
  [[ "$host" == *:* ]] && host="[$host]"
  printf 'socks5h://%s:%s' "$host" "$port"
}
socks_port_listening() {
  local host="${1:-$SOCKS_HOST}" port="${2:-$SOCKS_PORT}" address resolved
  host="${host#[}"
  host="${host%]}"
  case "$host" in
    0.0.0.0) host="127.0.0.1" ;;
    ::) host="::1" ;;
  esac
  if [[ "$host" != *:* && "$host" =~ [[:alpha:]] ]]; then
    while IFS= read -r resolved; do
      [[ -n "$resolved" ]] || continue
      if ss -H -lnt 2>/dev/null | awk -v address="$resolved:$port" '$4 == address { found=1 } END { exit !found }'; then return 0; fi
    done < <(getent ahostsv4 "$host" 2>/dev/null | awk '{print $1}' | sort -u)
    while IFS= read -r resolved; do
      [[ -n "$resolved" ]] || continue
      if ss -H -lnt 2>/dev/null | awk -v address="[$resolved]:$port" '$4 == address { found=1 } END { exit !found }'; then return 0; fi
    done < <(getent ahostsv6 "$host" 2>/dev/null | awk '{print $1}' | sort -u)
    return 1
  fi
  [[ "$host" == *:* ]] && address="[$host]:$port" || address="$host:$port"
  ss -H -lnt 2>/dev/null | awk -v address="$address" '$4 == address { found=1 } END { exit !found }'
}
wait_for_socks_port() {
  local host="${1:-}" port="${2:-}" i
  for ((i = 0; i < PORT_WAIT_TRIES; i++)); do
    if socks_port_listening "$host" "$port"; then return 0; fi
    sleep 0.1
  done
  return 1
}
restart_wireproxy() {
  ensure_service_exists || { err "wireproxy.service отсутствует, а $PROXY_CONF или бинарник wireproxy не найден."; return 1; }
  systemctl restart wireproxy || { err "Не удалось перезапустить wireproxy.service."; return 1; }
  wait_for_socks_port || {
    err "wireproxy.service не открыл SOCKS5 $(socks_address) за отведённое время."
    systemctl status wireproxy --no-pager -l | head -80 || true
    return 1
  }
}
check_port() {
  if socks_port_listening; then
    ok "SOCKS5 слушает $(socks_address)"
    return 0
  fi
  warn "SOCKS5 порт пока не виден. Статус wireproxy:"
  systemctl status wireproxy --no-pager -l | head -80 || true
  return 1
}
ensure_wireproxy_ready_for_check() {
  if systemctl is-active --quiet wireproxy && socks_port_listening; then
    log "wireproxy уже активен, перезапуск для проверки не требуется."
    return 0
  fi
  warn "wireproxy неактивен или SOCKS5-порт не слушает; перезапускаю сервис."
  restart_wireproxy || return 1
}
get_current_endpoint() { grep -i '^Endpoint' "$PROXY_CONF" 2>/dev/null | head -n1 | awk -F= '{gsub(/^[ \t]+|[ \t]+$/, "", $2); print $2}'; }

begin_scan_transaction() {
  [[ "$SCAN_TRANSACTION_ACTIVE" == "0" ]] || return 0
  SCAN_ORIGINAL_ENDPOINT="$(get_current_endpoint || true)"
  valid_endpoint "$SCAN_ORIGINAL_ENDPOINT" || {
    err "Невозможно безопасно начать scan: текущий Endpoint отсутствует или некорректен."
    return 1
  }
  SCAN_TRANSACTION_ACTIVE="1"
}

restore_scan_original() {
  [[ "$SCAN_TRANSACTION_ACTIVE" == "1" ]] || return 0
  # Перебор кандидатов больше не трогает боевой конфиг (см. test_endpoint),
  # поэтому в подавляющем большинстве случаев восстанавливать нечего — и не
  # нужно платить лишним restart wireproxy за откат несуществующего изменения.
  [[ "$(get_current_endpoint || true)" != "$SCAN_ORIGINAL_ENDPOINT" ]] || return 0
  warn "Возвращаю исходный endpoint: $SCAN_ORIGINAL_ENDPOINT"
  set_endpoint "$SCAN_ORIGINAL_ENDPOINT" || return 1
  systemctl restart wireproxy >/dev/null 2>&1 || return 1
  wait_for_socks_port || return 1
}

rollback_scan_transaction() {
  [[ "$SCAN_TRANSACTION_ACTIVE" == "1" ]] || return 0
  if ! restore_scan_original; then
    err "Не удалось полностью откатить исходный endpoint $SCAN_ORIGINAL_ENDPOINT."
  fi
  SCAN_TRANSACTION_ACTIVE="0"
}

commit_scan_transaction() {
  [[ "$SCAN_TRANSACTION_ACTIVE" == "1" ]] || return 0
  SCAN_TRANSACTION_ACTIVE="0"
  SCAN_ORIGINAL_ENDPOINT=""
}

quick_warp_check() {
  local raw trace endpoint time_total http_code colo loc cache_line _ep _time _colo _loc _ts cached_loss cached_stable cached_scanner attempt ok=0
  for ((attempt = 1; attempt <= QUICK_CHECK_RETRIES; attempt++)); do
    raw="$(LC_ALL=C curl -m "$PROBE_TIMEOUT" -sS -x "$(socks_proxy_url)" -w '\n__TIME_TOTAL__=%{time_total}\n__HTTP_CODE__=%{http_code}\n' "$TEST_URL" 2>/dev/null)" || raw=""
    trace="$(printf '%s\n' "$raw" | grep -E '^(ip|colo|loc|warp)=' || true)"
    http_code="$(printf '%s\n' "$raw" | awk -F= '$1=="__HTTP_CODE__"{print $2; exit}')"
    if [[ "$http_code" == "200" ]] && printf '%s\n' "$trace" | grep -q '^warp=on$'; then
      ok=1
      break
    fi
    if [[ "$attempt" -lt "$QUICK_CHECK_RETRIES" ]]; then
      warn "quick-check не прошёл с попытки $attempt/$QUICK_CHECK_RETRIES, повторяю через ${QUICK_CHECK_RETRY_DELAY}с..."
      sleep "$QUICK_CHECK_RETRY_DELAY"
    fi
  done
  BEST_TRACE="$trace"
  [[ "$ok" -eq 1 ]] || return 1
  endpoint="$(get_current_endpoint || true)"
  colo="$(printf '%s\n' "$trace" | awk -F= '$1=="colo"{print $2; exit}')"
  loc="$(printf '%s\n' "$trace" | awk -F= '$1=="loc"{print $2; exit}')"
  if [[ -n "$endpoint" ]]; then
    # Пишем настоящее время ответа: раньше сюда шёл ноль, из-за чего любой
    # успешный quick-check намертво прибивал текущий endpoint к вершине
    # good-кэша и рейтинг переставал что-либо значить.
    time_total="$(printf '%s\n' "$raw" | grep -m1 '^__TIME_TOTAL__=' | cut -d= -f2- || true)"
    cache_line="$(awk -v ep="$endpoint" -F'\t' '$1==ep{line=$0} END{print line}' "$GOOD_ENDPOINTS_FILE" 2>/dev/null || true)"
    IFS=$'\t' read -r _ep _time _colo _loc _ts cached_loss cached_stable cached_scanner <<< "$cache_line"
    remember_good_endpoint "$endpoint" "${time_total:-9}" "${colo:-unknown}" "${loc:-unknown}" "${cached_loss:-0}" "${cached_stable:-1}" "${cached_scanner:-healthcheck}"
  fi
  if policy_configured && ! endpoint_matches_policy "$colo" "$loc"; then
    LAST_POLICY_MATCH="0"
    warn "Текущий endpoint работает, но не соответствует policy: $(policy_summary)"
    return 1
  fi
  LAST_POLICY_MATCH="1"
  return 0
}
remember_good_endpoint() { local ep="${1:-}" time="${2:-0}" colo="${3:-unknown}" loc="${4:-unknown}" loss="${5:-0}" stable="${6:-1}" scanner="${7:-native}" ts; [[ -z "$ep" ]] && return 0; mkdir -p "$WG_DIR"; ts="$(date +%s)"; touch "$GOOD_ENDPOINTS_FILE" "$BAD_ENDPOINTS_FILE"; awk -v ep="$ep" -F'\t' '$1 != ep {print}' "$GOOD_ENDPOINTS_FILE" > "${GOOD_ENDPOINTS_FILE}.tmp" 2>/dev/null || true; mv "${GOOD_ENDPOINTS_FILE}.tmp" "$GOOD_ENDPOINTS_FILE" 2>/dev/null || true; printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$ep" "$time" "$colo" "$loc" "$ts" "$loss" "$stable" "$scanner" >> "$GOOD_ENDPOINTS_FILE"; LC_ALL=C sort -t $'\t' -k2,2n "$GOOD_ENDPOINTS_FILE" | head -n 30 > "${GOOD_ENDPOINTS_FILE}.tmp" || true; mv "${GOOD_ENDPOINTS_FILE}.tmp" "$GOOD_ENDPOINTS_FILE" 2>/dev/null || true; awk -v ep="$ep" -F'\t' '$1 != ep {print}' "$BAD_ENDPOINTS_FILE" > "${BAD_ENDPOINTS_FILE}.tmp" 2>/dev/null || true; mv "${BAD_ENDPOINTS_FILE}.tmp" "$BAD_ENDPOINTS_FILE" 2>/dev/null || true; }
remember_bad_endpoint() { local ep="${1:-}" ts count; [[ -z "$ep" ]] && return 0; mkdir -p "$WG_DIR"; touch "$BAD_ENDPOINTS_FILE"; ts="$(date +%s)"; count="$(awk -v ep="$ep" -F'\t' '$1 == ep {print $2}' "$BAD_ENDPOINTS_FILE" 2>/dev/null | tail -n1)"; count="${count:-0}"; count=$((count + 1)); awk -v ep="$ep" -F'\t' '$1 != ep {print}' "$BAD_ENDPOINTS_FILE" > "${BAD_ENDPOINTS_FILE}.tmp" 2>/dev/null || true; mv "${BAD_ENDPOINTS_FILE}.tmp" "$BAD_ENDPOINTS_FILE" 2>/dev/null || true; printf '%s\t%s\t%s\n' "$ep" "$count" "$ts" >> "$BAD_ENDPOINTS_FILE"; tail -n 200 "$BAD_ENDPOINTS_FILE" > "${BAD_ENDPOINTS_FILE}.tmp" || true; mv "${BAD_ENDPOINTS_FILE}.tmp" "$BAD_ENDPOINTS_FILE" 2>/dev/null || true; }
is_bad_endpoint() { local ep="${1:-}" now ts count age; [[ -z "$ep" || ! -f "$BAD_ENDPOINTS_FILE" ]] && return 1; now="$(date +%s)"; count="$(awk -v ep="$ep" -F'\t' '$1 == ep {print $2}' "$BAD_ENDPOINTS_FILE" | tail -n1)"; ts="$(awk -v ep="$ep" -F'\t' '$1 == ep {print $3}' "$BAD_ENDPOINTS_FILE" | tail -n1)"; count="${count:-0}"; ts="${ts:-0}"; age=$((now - ts)); [[ "$count" -ge 3 && "$age" -lt 86400 ]]; }
append_candidate() { local ep="${1:-}" forced="${2:-0}"; [[ -z "$ep" ]] && return 0; [[ "$forced" == "1" ]] || ! is_bad_endpoint "$ep" || return 0; grep -qxF "$ep" "$CANDIDATES_FILE" 2>/dev/null || echo "$ep" >> "$CANDIDATES_FILE"; }
generate_endpoint_candidates() { : > "$CANDIDATES_FILE"; local current_endpoint ep made attempts prefix last_octet port; if [[ "$USE_CUSTOM_ENDPOINTS" == "1" ]]; then for ep in "${CUSTOM_ENDPOINTS[@]}"; do append_candidate "$ep" "1"; done; ok "Пользовательских endpoint: $(wc -l < "$CANDIDATES_FILE" | tr -d ' ')"; return 0; fi; current_endpoint="$(get_current_endpoint || true)"; [[ -n "$current_endpoint" ]] && append_candidate "$current_endpoint"; if [[ -f "$GOOD_ENDPOINTS_FILE" ]]; then while IFS=$'\t' read -r ep _rest; do append_candidate "$ep"; done < <(LC_ALL=C sort -t $'\t' -k2,2n "$GOOD_ENDPOINTS_FILE" | head -n 20); fi; for ep in engage.cloudflareclient.com:2408 162.159.192.244:1843 162.159.195.100:1010 162.159.193.10:2408 188.114.96.10:2408 188.114.97.10:2408; do append_candidate "$ep"; done; made=0; attempts=0; while [[ "$made" -lt "$SCAN_COUNT" && "$attempts" -lt $((SCAN_COUNT * 10 + 200)) ]]; do attempts=$((attempts + 1)); prefix="${WARP_PREFIXES[$((RANDOM % ${#WARP_PREFIXES[@]}))]}"; last_octet="$((RANDOM % 256))"; port="${WARP_PORTS[$((RANDOM % ${#WARP_PORTS[@]}))]}"; ep="${prefix}.${last_octet}:${port}"; if ! grep -qxF "$ep" "$CANDIDATES_FILE" 2>/dev/null && ! is_bad_endpoint "$ep"; then echo "$ep" >> "$CANDIDATES_FILE"; made=$((made + 1)); fi; done; ok "Кандидатов endpoint: $(wc -l < "$CANDIDATES_FILE" | tr -d ' ')"; }

stability_check() {
  local first_time="$1" raw http_code warp time_total i successes=1 tail_failures=0 times
  times="$first_time"
  for ((i = 2; i <= STABILITY_PROBES; i++)); do
    if ! raw="$(LC_ALL=C curl -m "$PROBE_TIMEOUT" -sS -x "$(socks_proxy_url "$SCAN_SOCKS_HOST" "$SCAN_SOCKS_PORT")" -w '\n__TIME_TOTAL__=%{time_total}\n__HTTP_CODE__=%{http_code}\n' "$TEST_URL" 2>/dev/null)"; then
      raw=""
    fi
    http_code="$(printf '%s\n' "$raw" | awk -F= '$1=="__HTTP_CODE__"{print $2; exit}')"
    warp="$(printf '%s\n' "$raw" | awk -F= '$1=="warp"{print $2; exit}')"
    time_total="$(printf '%s\n' "$raw" | awk -F= '$1=="__TIME_TOTAL__"{print $2; exit}')"
    if [[ "$http_code" == "200" && "$warp" == "on" && -n "$time_total" ]]; then
      successes=$((successes + 1))
      tail_failures=0
      times+=$'\n'"$time_total"
    else
      tail_failures=$((tail_failures + 1))
    fi
  done
  LAST_STABILITY_LOSS="$(awk -v ok="$successes" -v total="$STABILITY_PROBES" 'BEGIN { printf "%.0f", 100 * (total-ok) / total }')"
  LAST_STABILITY_TIME="$(printf '%s\n' "$times" | awk 'NF {sum+=$1; n++} END {if (n) printf "%.6f", sum/n}')"
  [[ "$tail_failures" -ge 2 ]] && LAST_STABILITY_TORN="1" || LAST_STABILITY_TORN="0"
  [[ "$successes" -ge $((STABILITY_PROBES - 1)) && "$LAST_STABILITY_TORN" == "0" ]]
}

scan_port_free() {
  local port="$1"
  ! ss -H -lnt 2>/dev/null | awk '{print $4}' | awk -F: '{print $NF}' | grep -qx "$port"
}

pick_scan_socks_port() {
  local port=$((SOCKS_PORT + 1)) tries=0
  [[ "$port" -le 65535 ]] || port=20000
  while [[ "$tries" -lt 200 ]]; do
    if scan_port_free "$port"; then printf '%s' "$port"; return 0; fi
    port=$((port + 1))
    [[ "$port" -le 65535 ]] || port=20000
    tries=$((tries + 1))
  done
  return 1
}

init_scan_proxy_conf() {
  [[ -f "$PROXY_CONF" ]] || { err "Не найден $PROXY_CONF для scan-конфига."; return 1; }
  cp "$PROXY_CONF" "$SCAN_PROXY_CONF" || return 1
  chmod 600 "$SCAN_PROXY_CONF"
  grep -qi '^BindAddress[[:space:]]*=' "$SCAN_PROXY_CONF" || { err "В $SCAN_PROXY_CONF отсутствует BindAddress."; return 1; }
  sed -i "s#^BindAddress[[:space:]]*=.*#BindAddress = $SCAN_SOCKS_HOST:$SCAN_SOCKS_PORT#I" "$SCAN_PROXY_CONF" || return 1
}

set_scan_endpoint() {
  local ep="$1"
  valid_endpoint "$ep" || { err "Отказ менять scan-конфиг на некорректный endpoint: $ep"; return 1; }
  grep -qi '^Endpoint[[:space:]]*=' "$SCAN_PROXY_CONF" || { err "В $SCAN_PROXY_CONF отсутствует Endpoint."; return 1; }
  sed -i "s#^Endpoint[[:space:]]*=.*#Endpoint = $ep#I" "$SCAN_PROXY_CONF" || { err "Не удалось обновить Endpoint в $SCAN_PROXY_CONF."; return 1; }
}

# Кандидатов гоняем через одноразовый background-процесс wireproxy на отдельном
# порту, а не через systemctl restart wireproxy — так перебор ни разу не
# трогает боевой SOCKS5, которым в это время пользуется Xray.
stop_scan_wireproxy() {
  [[ -f "$SCAN_PID_FILE" ]] || return 0
  local pid="" i
  pid="$(cat "$SCAN_PID_FILE" 2>/dev/null || true)"
  rm -f "$SCAN_PID_FILE"
  [[ -n "$pid" ]] || return 0
  kill -0 "$pid" 2>/dev/null || return 0
  kill "$pid" 2>/dev/null || true
  for ((i = 0; i < 30; i++)); do
    kill -0 "$pid" 2>/dev/null || return 0
    sleep 0.1
  done
  kill -9 "$pid" 2>/dev/null || true
}

restart_scan_wireproxy() {
  local bin
  bin="$(find_wireproxy_bin)" || { err "wireproxy бинарник не найден для scan-инстанса."; return 1; }
  stop_scan_wireproxy
  "$bin" -c "$SCAN_PROXY_CONF" >"$TMP_DIR/scan-wireproxy.log" 2>&1 &
  echo $! > "$SCAN_PID_FILE"
  wait_for_socks_port "$SCAN_SOCKS_HOST" "$SCAN_SOCKS_PORT" || {
    err "Теневой wireproxy не поднял SOCKS5 $SCAN_SOCKS_HOST:$SCAN_SOCKS_PORT."
    return 1
  }
}

test_endpoint() {
  local ep="$1" trace_file="$TMP_DIR/trace" rc=0 warp ip colo loc time_total http_code policy
  if ! set_scan_endpoint "$ep"; then
    printf '%s\tFAIL\tconfig_update\t-\t-\t-\t-\t100\t0\t-\t%s\n' "$ep" "$CURRENT_SCANNER" >> "$RESULT_FILE"
    return 1
  fi
  if ! restart_scan_wireproxy; then
    printf '%s\tFAIL\tservice_restart\t-\t-\t-\t-\t100\t0\t-\t%s\n' "$ep" "$CURRENT_SCANNER" >> "$RESULT_FILE"
    return 1
  fi
  LC_ALL=C curl -m "$PROBE_TIMEOUT" -sS -x "$(socks_proxy_url "$SCAN_SOCKS_HOST" "$SCAN_SOCKS_PORT")" -w '\n__TIME_TOTAL__=%{time_total}\n__HTTP_CODE__=%{http_code}\n' "$TEST_URL" > "$trace_file" 2>/dev/null || rc=$?
  if [[ "$rc" -ne 0 ]]; then
    printf '%s\tFAIL\tcurl_rc=%s\t-\t-\t-\t-\t100\t0\t-\t%s\n' "$ep" "$rc" "$CURRENT_SCANNER" >> "$RESULT_FILE"
    remember_bad_endpoint "$ep"
    rm -f "$trace_file"
    return 1
  fi
  warp="$(grep -m1 '^warp=' "$trace_file" | cut -d= -f2- || true)"
  ip="$(grep -m1 '^ip=' "$trace_file" | cut -d= -f2- || true)"
  colo="$(grep -m1 '^colo=' "$trace_file" | cut -d= -f2- || true)"
  loc="$(grep -m1 '^loc=' "$trace_file" | cut -d= -f2- || true)"
  time_total="$(grep -m1 '^__TIME_TOTAL__=' "$trace_file" | cut -d= -f2- || true)"
  http_code="$(grep -m1 '^__HTTP_CODE__=' "$trace_file" | cut -d= -f2- || true)"
  rm -f "$trace_file"
  if [[ "$http_code" != "200" || "$warp" != "on" || -z "$time_total" ]]; then
    printf '%s\tFAIL\t%s\t%s\t%s\t%s\t%s\t100\t0\t-\t%s\n' "$ep" "${time_total:-9}" "${ip:--}" "${colo:--}" "${loc:--}" "${warp:--}" "$CURRENT_SCANNER" >> "$RESULT_FILE"
    remember_bad_endpoint "$ep"
    return 1
  fi
  if ! stability_check "$time_total"; then
    printf '%s\tUNSTABLE\t%s\t%s\t%s\t%s\t%s\t%s\t0\t-\t%s\n' "$ep" "${LAST_STABILITY_TIME:-$time_total}" "$ip" "$colo" "$loc" "$warp" "$LAST_STABILITY_LOSS" "$CURRENT_SCANNER" >> "$RESULT_FILE"
    remember_bad_endpoint "$ep"
    return 1
  fi
  time_total="${LAST_STABILITY_TIME:-$time_total}"
  LAST_POLICY_MATCH="1"
  policy="MATCH"
  if ! endpoint_matches_policy "$colo" "$loc"; then LAST_POLICY_MATCH="0"; policy="MISMATCH"; fi
  printf '%s\tOK\t%s\t%s\t%s\t%s\t%s\t%s\t1\t%s\t%s\n' "$ep" "$time_total" "$ip" "$colo" "$loc" "$warp" "$LAST_STABILITY_LOSS" "$policy" "$CURRENT_SCANNER" >> "$RESULT_FILE"
  remember_good_endpoint "$ep" "$time_total" "$colo" "$loc" "$LAST_STABILITY_LOSS" "1" "$CURRENT_SCANNER"
  return 0
}

pick_best_line() {
  local result_file="$1" best_line
  best_line="$(awk -F'\t' '$2=="OK" && $10=="MATCH"{print $0}' "$result_file" | LC_ALL=C sort -t $'\t' -k8,8n -k3,3n | head -n1 || true)"
  if [[ -z "$best_line" && "$POLICY_MODE" == "prefer" ]]; then
    best_line="$(awk -F'\t' '$2=="OK"{print $0}' "$result_file" | LC_ALL=C sort -t $'\t' -k8,8n -k3,3n | head -n1 || true)"
  fi
  printf '%s' "$best_line"
}

apply_best_line() {
  local best_line="$1"
  BEST_ENDPOINT="$(printf '%s' "$best_line" | awk -F'\t' '{print $1}')"
  BEST_TIME="$(printf '%s' "$best_line" | awk -F'\t' '{print $3}')"
  BEST_COLO="$(printf '%s' "$best_line" | awk -F'\t' '{print $5}')"
  BEST_LOC="$(printf '%s' "$best_line" | awk -F'\t' '{print $6}')"
  BEST_LOSS="$(printf '%s' "$best_line" | awk -F'\t' '{print $8}')"
  BEST_STABLE="$(printf '%s' "$best_line" | awk -F'\t' '{print $9}')"
  BEST_SCANNER="$(printf '%s' "$best_line" | awk -F'\t' '{print $11}')"
  # Победитель был проверен только на теневом инстансе — боевой сервис ещё
  # ни разу не переключался на него, поэтому restart здесь всегда нужен.
  set_endpoint "$BEST_ENDPOINT" || return 1
  restart_wireproxy || return 1
  remember_good_endpoint "$BEST_ENDPOINT" "$BEST_TIME" "$BEST_COLO" "$BEST_LOC" "$BEST_LOSS" "$BEST_STABLE" "$BEST_SCANNER"
  ok "Выбран endpoint: $BEST_ENDPOINT time_total=$BEST_TIME probe_loss=${BEST_LOSS}% colo=$BEST_COLO loc=$BEST_LOC scanner=$BEST_SCANNER"
}

select_best_endpoint_native() {
  CURRENT_SCANNER="native"
  generate_endpoint_candidates
  : > "$RESULT_FILE"
  local ep best_line total good_count=0 tested=0
  # Каждый test_endpoint здесь перезапускает только теневой инстанс на
  # SCAN_SOCKS_PORT (см. select_best_endpoint), боевой $PROXY_CONF во время
  # перебора не трогается вообще — его меняет только apply_best_line в конце,
  # ровно один раз, на уже выбранного победителя.
  total="$(wc -l < "$CANDIDATES_FILE" | tr -d ' ')"
  while IFS= read -r ep; do
    [[ -z "$ep" ]] && continue
    tested=$((tested + 1))
    log "Проверяю $ep"
    if test_endpoint "$ep"; then
      if [[ "$LAST_POLICY_MATCH" == "1" ]]; then
        ok "$ep работает и соответствует policy"
        good_count=$((good_count + 1))
      else
        warn "$ep работает, но не соответствует policy"
      fi
      if [[ "$good_count" -ge "$ENOUGH_GOOD" ]]; then
        log "Набрано рабочих endpoint'ов: $good_count. Останавливаю перебор."
        break
      fi
    else
      warn "$ep не подошёл"
    fi
  done < "$CANDIDATES_FILE"
  stop_scan_wireproxy
  if [[ "$tested" -lt "$total" ]]; then log "Проверено кандидатов: $tested из $total (ранняя остановка)."; fi
  echo; echo "=== Результаты проверки ==="
  command -v column >/dev/null 2>&1 && column -t -s $'\t' "$RESULT_FILE" || cat "$RESULT_FILE"
  best_line="$(pick_best_line "$RESULT_FILE")"
  if [[ -n "$best_line" && "$(printf '%s' "$best_line" | awk -F'\t' '{print $10}')" != "MATCH" ]]; then
    warn "Endpoint по policy не найден; использую рабочий fallback (mode=prefer)."
  fi
  if [[ -z "$best_line" ]]; then
    err "Не найден стабильный endpoint с warp=on, подходящий под policy: $(policy_summary)"
    exit 1
  fi
  apply_best_line "$best_line" || return 1
}

find_warpscout_bin() {
  if [[ "$WARPSCOUT_BIN" == */* ]]; then [[ -x "$WARPSCOUT_BIN" ]] && printf '%s' "$WARPSCOUT_BIN"; return; fi
  command -v "$WARPSCOUT_BIN" 2>/dev/null || return 1
}

prepare_warpscout_account() {
  [[ -r "$ACCOUNT_JSON" && -r "$PRIVATE_KEY_FILE" ]] || return 1
  [[ -n "$WARPSCOUT_ACCOUNT_FILE" ]] || WARPSCOUT_ACCOUNT_FILE="$(mktemp /tmp/warpwp-warpscout-account.XXXXXX)"
  if (umask 077; python3 - "$ACCOUNT_JSON" "$PRIVATE_KEY_FILE" > "$WARPSCOUT_ACCOUNT_FILE") <<'PY'
import json, sys
account_path, private_path = sys.argv[1:]
with open(account_path, encoding='utf-8') as fh:
    account = json.load(fh)
with open(private_path, encoding='utf-8') as fh:
    private_key = fh.read().strip()
peers = account.get('config', {}).get('peers', [])
peer_public_key = peers[0].get('public_key', '') if peers else ''
if not private_key or not peer_public_key:
    raise SystemExit('WARP account has no private or peer public key')
json.dump({
    'private_key': private_key,
    'peer_public_key': peer_public_key,
}, sys.stdout)
PY
  then
    chmod 600 "$WARPSCOUT_ACCOUNT_FILE"
    return 0
  fi
  rm -f "$WARPSCOUT_ACCOUNT_FILE"
  WARPSCOUT_ACCOUNT_FILE=""
  return 1
}

valid_scanned_endpoint() {
  local ep="$1" host port
  if [[ "$ep" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}:[0-9]{1,5}$ ]]; then
    host="${ep%:*}"
  elif [[ "$ep" =~ ^\[[0-9A-Fa-f:]+\]:[0-9]{1,5}$ ]]; then
    host="${ep%%]:*}"; host="${host:1}"
  else
    return 1
  fi
  port="${ep##*:}"; port="${port%]}"
  valid_port "$port" || return 1
  python3 -c 'import ipaddress, sys; ipaddress.ip_address(sys.argv[1])' "$host" >/dev/null 2>&1
}

select_best_endpoint_warpscout() {
  local bin sample ping_count output endpoint best_line
  local -a args
  [[ "$USE_CUSTOM_ENDPOINTS" == "0" ]] || { warn "WARPSCOUT mode не поддерживает список endpoint:port; использую native scanner."; return 1; }
  bin="$(find_warpscout_bin)" || { warn "WARPSCOUT не найден: $WARPSCOUT_BIN"; return 1; }
  prepare_warpscout_account || { warn "Не удалось подготовить временный WARPSCOUT account из текущего WARP account."; return 1; }
  sample=$(((SCAN_COUNT + 14) / 15)); [[ "$sample" -gt 256 ]] && sample=256
  ping_count="$STABILITY_PROBES"; [[ "$ping_count" -lt 5 ]] && ping_count=5
  args=(scan -p wg -a "$WARPSCOUT_ACCOUNT_FILE" -n "$sample" -jt "$WARPSCOUT_JOBS" -t "$PROBE_TIMEOUT" -tun-ping-count "$ping_count" -plain -best -no-report)
  [[ -n "$NODE_ALLOW" ]] && args+=(-node "$NODE_ALLOW")
  log "WARPSCOUT discovery: sample=$sample jobs=$WARPSCOUT_JOBS policy=$(policy_summary)"
  if ! output="$("$bin" "${args[@]}")"; then
    warn "WARPSCOUT не нашёл подходящий endpoint."
    return 1
  fi
  endpoint="$(printf '%s\n' "$output" | awk 'NF {line=$0} END {print line}')"
  valid_scanned_endpoint "$endpoint" || { warn "WARPSCOUT вернул некорректный endpoint: $endpoint"; return 1; }
  : > "$RESULT_FILE"
  CURRENT_SCANNER="warpscout"
  log "Финально проверяю найденный WARPSCOUT endpoint через wireproxy: $endpoint"
  if ! test_endpoint "$endpoint"; then
    stop_scan_wireproxy
    warn "WARPSCOUT endpoint не прошёл финальную проверку Manager."
    return 1
  fi
  stop_scan_wireproxy
  if [[ "$LAST_POLICY_MATCH" != "1" ]]; then
    if [[ "$POLICY_MODE" == "prefer" && "$SCANNER" == "warpscout" ]]; then
      warn "WARPSCOUT endpoint не соответствует policy; принимаю fallback в mode=prefer."
    else
      warn "WARPSCOUT endpoint не соответствует policy; ищу другой через native scanner."
      return 1
    fi
  fi
  best_line="$(awk -F'\t' '$2=="OK"{print $0; exit}' "$RESULT_FILE")"
  [[ -n "$best_line" ]] || return 1
  apply_best_line "$best_line" || return 1
  return 0
}

select_best_endpoint() {
  begin_scan_transaction || exit 1
  SCAN_SOCKS_PORT="$(pick_scan_socks_port)" || { err "Не нашёл свободный локальный порт для теневого инстанса wireproxy."; exit 1; }
  init_scan_proxy_conf || exit 1
  case "$SCANNER" in
    native) select_best_endpoint_native ;;
    warpscout)
      select_best_endpoint_warpscout || { err "WARPSCOUT scanner не смог выбрать endpoint."; exit 1; }
      ;;
    auto)
      if select_best_endpoint_warpscout; then return 0; fi
      warn "Перехожу к встроенному native scanner."
      select_best_endpoint_native
      ;;
  esac
}
final_check() {
  local raw http_code
  if ! raw="$(LC_ALL=C curl -m "$FINAL_TIMEOUT" -sS -x "$(socks_proxy_url)" -w '\n__HTTP_CODE__=%{http_code}\n' "$TEST_URL" 2>/dev/null)"; then
    err "Финальная проверка Cloudflare завершилась ошибкой curl."
    return 1
  fi
  http_code="$(printf '%s\n' "$raw" | awk -F= '$1=="__HTTP_CODE__"{print $2; exit}')"
  BEST_TRACE="$(printf '%s\n' "$raw" | grep -E '^(ip|colo|loc|warp)=' || true)"
  echo "$BEST_TRACE"
  if [[ "$http_code" != "200" ]] || ! printf '%s\n' "$BEST_TRACE" | grep -q '^warp=on$'; then
    err "Финальная проверка не получила HTTP 200 с warp=on."
    systemctl status wireproxy --no-pager -l | head -80 || true
    return 1
  fi
  if [[ "$POLICY_MODE" == "strict" ]] && ! endpoint_matches_policy "$(printf '%s\n' "$BEST_TRACE" | awk -F= '$1=="colo"{print $2; exit}')" "$(printf '%s\n' "$BEST_TRACE" | awk -F= '$1=="loc"{print $2; exit}')"; then
    err "Финальная проверка прошла через WARP, но не соответствует strict policy: $(policy_summary)."
    return 1
  fi
  ok "WARP работает: warp=on"
}

load_socks_bind_from_proxy() {
  local bind host port
  bind="$(awk -F= 'tolower($1) ~ /^[[:space:]]*bindaddress[[:space:]]*$/ {v=$2; sub(/^[[:space:]]*/,"",v); sub(/[[:space:]]*$/,"",v); print v; exit}' "$PROXY_CONF")"
  [[ -n "$bind" ]] || { err "В $PROXY_CONF отсутствует BindAddress."; return 1; }
  valid_endpoint "$bind" || { err "Некорректный BindAddress в $PROXY_CONF: $bind"; return 1; }
  if [[ "$bind" == \[* ]]; then host="${bind%]:*}]"; port="${bind##*:}"; else host="${bind%:*}"; port="${bind##*:}"; fi
  [[ "$SOCKS_HOST_EXPLICIT" == "1" ]] || SOCKS_HOST="$host"
  [[ "$SOCKS_PORT_EXPLICIT" == "1" ]] || SOCKS_PORT="$port"
  validate_runtime_options
}

run_check_and_repair() {
  [[ -f "$PROXY_CONF" ]] || { err "Не найден $PROXY_CONF. Сначала запусти обычную установку без --check."; exit 1; }
  find_wireproxy_bin >/dev/null 2>&1 || { err "wireproxy не найден. Сначала запусти обычную установку без --check."; exit 1; }
  load_socks_bind_from_proxy || exit 1
  cleanup_system_warp_routes || exit 1
  log "Режим проверки: проверяю текущий WARP без переустановки и без apt update."
  ensure_service_exists || create_service
  ensure_wireproxy_ready_for_check || exit 1
  check_port || exit 1
  if quick_warp_check; then
    ok "WARP живой, endpoint менять не нужно."
    echo "$BEST_TRACE"; echo; echo "Текущий endpoint: $(get_current_endpoint)"; echo "Кэш good endpoint'ов: $GOOD_ENDPOINTS_FILE"
    exit 0
  fi
  warn "WARP не отвечает или нет warp=on. Запускаю быстрый перескан endpoint'ов..."
  backup_existing
  select_best_endpoint
  final_check || exit 1
  commit_scan_transaction
  echo; ok "Endpoint был автоматически заменён на рабочий: $BEST_ENDPOINT"
  print_result
}

print_result() { local endpoint_port; endpoint_port="${BEST_ENDPOINT##*:}"; cat <<EOF_RESULT

============================================================
ГОТОВО
============================================================

Выбранный WARP endpoint:
  $BEST_ENDPOINT
  scanner=$BEST_SCANNER time_total=$BEST_TIME probe_loss=${BEST_LOSS}% stable=$BEST_STABLE
  colo=$BEST_COLO loc=$BEST_LOC
  policy=$(policy_summary)

Локальный SOCKS5:
  socks5://$SOCKS_HOST:$SOCKS_PORT

Cloudflare trace:
$(echo "$BEST_TRACE" | sed 's/^/  /')

------------------------------------------------------------
3x-ui / Xray outbounds
------------------------------------------------------------

{
  "tag": "WARP-socks5",
  "protocol": "socks",
  "settings": {"servers": [{"address": "$SOCKS_HOST", "port": $SOCKS_PORT}]}
},
{
  "tag": "WARP",
  "protocol": "freedom",
  "settings": {"domainStrategy": "UseIPv4"},
  "proxySettings": {"tag": "WARP-socks5"}
}

Важно: в routing указывай "WARP", не "WARP-socks5".
Не запускай wg-quick@warp: этот проект использует WARP только через wireproxy SOCKS5.

zapret4rocket:
  NFQWS_PORTS_UDP=$ZAPRET_PORTS
  endpoint UDP port: $endpoint_port

Проверить вручную:
  curl -m 10 -s -x "$(socks_proxy_url)" https://www.cloudflare.com/cdn-cgi/trace | grep -E 'ip=|colo=|loc=|warp='

EOF_RESULT
}
cleanup() {
  local rc=$?
  trap - EXIT INT TERM
  stop_scan_wireproxy
  rollback_registration_transaction
  rollback_install_transaction
  rollback_scan_transaction
  if [[ -n "$TMP_DIR" && -d "$TMP_DIR" && "$TMP_DIR" == /tmp/warp-wireproxy-native.* ]]; then rm -rf -- "$TMP_DIR"; fi
  if [[ "$INTERNAL_LOCK_HELD" == "1" ]]; then flock -u 9 2>/dev/null || true; fi
  return "$rc"
}
main() {
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  parse_args "$@"
  require_root
  init_runtime
  if [[ "$CHECK_ONLY" == "1" ]]; then
    check_deps_light
    acquire_internal_lock
    run_check_and_repair
    exit 0
  fi
  if command -v flock >/dev/null 2>&1; then
    acquire_internal_lock
    install_deps
  else
    # На совсем минимальной системе сначала ставим util-linux вместе с
    # остальными зависимостями, после чего уже можем сериализовать мутации.
    install_deps
    acquire_internal_lock
  fi
  cleanup_system_warp_routes || exit 1
  ensure_wireproxy_installed || exit 1
  if [[ -f "$PROXY_CONF" ]]; then load_socks_bind_from_proxy || exit 1; fi
  backup_existing || exit 1
  register_warp_account || exit 1
  write_configs || exit 1
  create_service || exit 1
  restart_wireproxy || exit 1
  check_port || exit 1
  select_best_endpoint
  final_check || exit 1
  commit_scan_transaction
  commit_install_transaction
  routing_guard_report
  print_result
}
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi

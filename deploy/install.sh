#!/usr/bin/env bash
# Installs Sideyard on a Linux server with systemd, or upgrades it.
#
#   curl -fsSL https://github.com/OWNER/REPO/releases/latest/download/install.sh | sudo bash
#
# A fresh install asks for your domain, an email for Let's Encrypt and an
# admin password, then starts Sideyard as a systemd service. Running it again
# upgrades to the newest release; settings, apps and data stay as they are.
#
# Everything runs from main at the bottom, so a download cut off midway runs
# nothing.

set -euo pipefail

# Where releases are published; release builds fill this in.
SOURCE="${SIDEYARD_SOURCE:-__SOURCE__}"

BIN_DIR=/opt/sideyard/bin
BIN=$BIN_DIR/sideyard
DATA_DIR=/var/lib/sideyard
CONF_DIR=/etc/sideyard
ENV_FILE=$CONF_DIR/sideyard.env
UNIT_FILE=/etc/systemd/system/sideyard.service

usage() {
  cat <<'EOF'
Installs Sideyard on a Linux server with systemd, or upgrades it.

  curl -fsSL <release>/install.sh | sudo bash
  curl -fsSL <release>/install.sh | sudo bash -s -- [options]

Options:
  --version vX.Y.Z  install that release instead of the newest (also downgrades)
  --yes             ask nothing; read answers from the environment instead:
                    SIDEYARD_DOMAIN (required), SIDEYARD_ACME_EMAIL,
                    SIDEYARD_ADMIN_PASSWORD (generated if empty)
  --help            show this

Without a terminal it behaves as with --yes. sudo drops your environment,
so pass the variables after it:
  curl -fsSL <release>/install.sh | sudo SIDEYARD_DOMAIN=example.com bash -s -- --yes
EOF
}

# --- output ---------------------------------------------------------------

if [ -t 1 ]; then
  BOLD=$'\e[1m' DIM=$'\e[2m' GREEN=$'\e[32m' YELLOW=$'\e[33m' RED=$'\e[31m' RESET=$'\e[0m'
else
  BOLD='' DIM='' GREEN='' YELLOW='' RED='' RESET=''
fi
say() { printf '%s\n' "$*"; }
step() { printf '%s→%s %s\n' "$GREEN" "$RESET" "$*"; }
warn() { printf '%s!%s %s\n' "$YELLOW" "$RESET" "$*" >&2; }
die() {
  printf '%s✗ %s%s\n' "$RED" "$*" "$RESET" >&2
  exit 1
}

# --- questions ------------------------------------------------------------

# Questions go to the terminal, since stdin is this script when piped. ask
# and ask_secret set the variable named by their first argument, so their
# own locals have names no caller uses.
ask() { # ask VAR "Question" [default]
  local _ask_reply=""
  if [ -n "${3:-}" ]; then
    printf '%s %s[%s]%s ' "$2" "$DIM" "$3" "$RESET" >/dev/tty
  else
    printf '%s ' "$2" >/dev/tty
  fi
  IFS= read -r _ask_reply </dev/tty || true
  printf -v "$1" '%s' "${_ask_reply:-${3:-}}"
}

ask_secret() { # ask_secret VAR "Question"
  local _ask_reply=""
  printf '%s ' "$2" >/dev/tty
  IFS= read -rs _ask_reply </dev/tty || true
  printf '\n' >/dev/tty
  printf -v "$1" '%s' "$_ask_reply"
}

confirm() { # confirm "Question" → yes unless answered no
  [ "$INTERACTIVE" = 1 ] || return 0
  local reply=""
  ask reply "$1 [Y/n]"
  case "$reply" in [nN]*) return 1 ;; esac
}

domain_ok() { [[ "$1" =~ ^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z][a-z0-9-]*[a-z0-9]$ ]]; }
email_ok() { [[ "$1" =~ ^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$ ]]; }

# password_problem prints what's wrong with a new admin password, if
# anything. bcrypt reads at most 72 bytes, so that's the limit, in bytes.
password_problem() {
  local bytes
  bytes=$(printf '%s' "$1" | wc -c | tr -d ' ')
  if [ "${#1}" -lt 12 ]; then
    echo "Use at least 12 characters."
  elif [ "$bytes" -gt 72 ]; then
    echo "Use at most 72 bytes; this one is $bytes."
  fi
}

# --- the release ----------------------------------------------------------

# version_older A B: is release A older than release B?
version_older() {
  [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n 1)" = "$1" ]
}

fetch_release() {
  local url
  if [ -n "$WANT_VERSION" ]; then
    case "$WANT_VERSION" in v*) ;; *) WANT_VERSION="v$WANT_VERSION" ;; esac
    url="$SOURCE/releases/download/$WANT_VERSION"
  else
    url="$SOURCE/releases/latest/download"
  fi
  curl -fsSL --retry 3 -o "$TMP/release.json" "$url/release.json" ||
    die "Couldn't fetch $url/release.json. Is the release published (and the repository public)?"
  VERSION=$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' "$TMP/release.json" | head -n 1)
  [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "$url/release.json doesn't name a release version."
  # The asset URLs name the version, so they can't change under us mid-install.
  RELEASE_URL="$SOURCE/releases/download/$VERSION"
}

download() {
  step "Downloading Sideyard $VERSION for $ARCH"
  curl -fSL --retry 3 --progress-bar -o "$TMP/$ASSET" "$RELEASE_URL/$ASSET" || die "Download failed: $RELEASE_URL/$ASSET"
  curl -fsSL --retry 3 -o "$TMP/SHA256SUMS" "$RELEASE_URL/SHA256SUMS" || die "Couldn't fetch the checksums."
  (cd "$TMP" && grep " $ASSET\$" SHA256SUMS | sha256sum -c --quiet -) ||
    die "The download doesn't match the release's checksum. Nothing was changed."
  chmod 755 "$TMP/$ASSET"
  local said
  said=$("$TMP/$ASSET" version 2>/dev/null) || die "The downloaded binary doesn't run on this machine."
  [ "$said" = "$VERSION" ] || die "The downloaded binary says it's $said, not $VERSION."
}

# install_binary puts the download in place, keeping the old one as .prev.
install_binary() {
  install -d -m 755 "$BIN_DIR"
  if [ -f "$BIN" ]; then
    ln -f "$BIN" "$BIN.prev"
  fi
  install -m 755 "$TMP/$ASSET" "$BIN.new"
  mv -f "$BIN.new" "$BIN"
  # The command for your shell (reset-password, version).
  if [ -e /usr/local/bin/sideyard ] && [ ! -L /usr/local/bin/sideyard ]; then
    mv -f /usr/local/bin/sideyard "$BIN_DIR/sideyard.manual"
  fi
  ln -sfn "$BIN" /usr/local/bin/sideyard
}

# wait_until_up waits for the dashboard's API to answer, then makes sure
# Sideyard stays up rather than answering once and crashing.
wait_until_up() {
  local addr restarts i
  addr=$(sed -n 's/^SIDEYARD_API_ADDR=//p' "$ENV_FILE" 2>/dev/null | tail -n 1)
  addr=${addr:-127.0.0.1:7070}
  restarts=$(systemctl show sideyard -p NRestarts --value 2>/dev/null || echo 0)
  for i in $(seq 1 60); do
    systemctl is-failed --quiet sideyard && return 1 # systemd gave up on it
    # Crashing over and over: it isn't coming up.
    [ "$(systemctl show sideyard -p NRestarts --value 2>/dev/null || echo 0)" -ge $((restarts + 3)) ] && return 1
    if curl -fsS -o /dev/null --max-time 2 "http://$addr/api/session" 2>/dev/null; then
      sleep 3
      systemctl is-active --quiet sideyard &&
        [ "$(systemctl show sideyard -p NRestarts --value 2>/dev/null || echo 0)" = "$restarts" ]
      return
    fi
    sleep 1
  done
  return 1
}

start_service() {
  systemctl daemon-reload
  systemctl enable --quiet sideyard
  # A crash loop trips systemd's start limit, after which it refuses starts.
  systemctl reset-failed sideyard 2>/dev/null || true
  systemctl restart sideyard
}

# --- upgrade --------------------------------------------------------------

upgrade() {
  local current unit
  current=$("$BIN" version 2>/dev/null || /usr/local/bin/sideyard version 2>/dev/null || echo unknown)
  if [ "$current" = "$VERSION" ]; then
    # A previous run may have stopped short of starting it.
    if ! systemctl is-active --quiet sideyard || ! systemctl is-enabled --quiet sideyard; then
      step "Sideyard $VERSION is installed but not running; starting it"
      start_service
      wait_until_up || die "Sideyard didn't start. See: journalctl -u sideyard -n 50"
    fi
    say "Sideyard $VERSION is installed and running. ${DIM}(--version vX.Y.Z installs another release.)${RESET}"
    return
  fi
  if [ -z "$WANT_VERSION" ] && [ "$current" != unknown ] && version_older "$VERSION" "$current"; then
    say "Sideyard $current is newer than the latest release ($VERSION). To go back to it anyway: --version $VERSION"
    return
  fi

  say "${BOLD}Sideyard is installed ($current).${RESET} This switches it to $VERSION; apps and settings stay."
  confirm "Continue?" || exit 0
  download
  install_binary

  # Installs from before this script ran the binary from /usr/local/bin,
  # which systemd keeps read-only for Sideyard, so it couldn't update itself.
  unit=$(systemctl show sideyard -p FragmentPath --value 2>/dev/null || true)
  if [ -n "$unit" ] && [ -f "$unit" ] && ! grep -q "^ExecStart=$BIN\$" "$unit"; then
    step "Pointing $unit at $BIN"
    sed -i "s#^ExecStart=.*#ExecStart=$BIN#" "$unit"
  fi
  if [ "$(systemctl show sideyard -p ProtectSystem --value 2>/dev/null || true)" = strict ]; then
    install -d -m 755 /etc/systemd/system/sideyard.service.d
    printf '[Service]\n# Updates replace the binary here.\nReadWritePaths=%s\n' "$BIN_DIR" >/etc/systemd/system/sideyard.service.d/updates.conf
  fi

  step "Restarting Sideyard (apps keep running)"
  start_service
  if ! wait_until_up; then
    if [ -f "$BIN.prev" ]; then
      warn "Sideyard $VERSION didn't stay up; going back to $current."
      mv -f "$BIN.prev" "$BIN"
      start_service
      wait_until_up || true
    fi
    die "See why: journalctl -u sideyard -n 50"
  fi
  say "${GREEN}✓${RESET} Sideyard $VERSION is running."
}

# --- fresh install --------------------------------------------------------

fresh_install() {
  say ""
  say "${BOLD}Sideyard $VERSION${RESET}"
  say "Every app gets <name>.<your domain>; the dashboard is sideyard.<your domain>."
  say ""

  # Ports 80 and 443 must be free: Sideyard is the web server.
  if command -v ss >/dev/null; then
    local busy
    busy=$(ss -Hltnp '( sport = :80 or sport = :443 )' 2>/dev/null || true)
    if [ -n "$busy" ]; then
      say "$busy" >&2
      die "Something is already listening on port 80 or 443 (above). Stop it first: Sideyard serves HTTP and HTTPS itself."
    fi
  fi

  local domain="${SIDEYARD_DOMAIN:-}" email="${SIDEYARD_ACME_EMAIL:-}" password="${SIDEYARD_ADMIN_PASSWORD:-}"
  local again="" problem="" generated=0
  if [ "$INTERACTIVE" = 1 ]; then
    while :; do
      ask domain "Domain (like example.com):" "$domain"
      domain=$(printf '%s' "$domain" | tr '[:upper:]' '[:lower:]' | sed 's#^https\?://##; s#/.*##; s/^sideyard\.//')
      domain_ok "$domain" && break
      warn "That doesn't look like a domain name."
    done
    while :; do
      ask email "Email for Let's Encrypt certificate notices (optional):" "$email"
      if [ -z "$email" ] || email_ok "$email"; then break; fi
      warn "That isn't an email address, and Let's Encrypt would refuse it. Leave it empty to skip."
      email=""
    done
    while :; do
      ask_secret password "Admin password (12+ characters; empty to generate one):"
      [ -z "$password" ] && break
      problem=$(password_problem "$password")
      if [ -n "$problem" ]; then
        warn "$problem"
        continue
      fi
      ask_secret again "Again:"
      [ "$password" = "$again" ] && break
      warn "Those didn't match."
    done
  else
    domain=$(printf '%s' "$domain" | tr '[:upper:]' '[:lower:]')
    domain_ok "$domain" || die "Set SIDEYARD_DOMAIN to your domain, like example.com."
    [ -z "$email" ] || email_ok "$email" || die "SIDEYARD_ACME_EMAIL isn't an email address."
    if [ -n "$password" ]; then
      problem=$(password_problem "$password")
      [ -z "$problem" ] || die "SIDEYARD_ADMIN_PASSWORD: $problem"
    fi
  fi
  if [ -z "$password" ]; then
    password=$(head -c 32 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-20)
    generated=1
  fi

  # The server's public address: for the DNS instructions, and so the
  # dashboard can tell whether a domain points here when the VM is behind NAT.
  local public_ip resolved
  public_ip=$(curl -fsS --max-time 4 https://checkip.amazonaws.com 2>/dev/null | tr -d '[:space:]' || true)
  [[ "$public_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || public_ip=""
  resolved=$(getent ahostsv4 "sideyard.$domain" 2>/dev/null | awk '{ print $1; exit }' || true)

  say ""
  if [ -n "$public_ip" ] && [ "$resolved" = "$public_ip" ]; then
    step "sideyard.$domain already points here ($public_ip)."
  else
    say "${BOLD}DNS${RESET}: add this record at your DNS provider (DNS only, no proxy), if you haven't:"
    say ""
    say "    *.$domain    A    ${public_ip:-<the public IPv4 address of this server>}"
    say ""
    if [ -n "$resolved" ]; then
      warn "sideyard.$domain resolves to $resolved${public_ip:+, not $public_ip}. Certificates are issued once it points here."
    else
      warn "sideyard.$domain doesn't resolve yet. Certificates are issued once it does."
    fi
  fi

  local open_firewall=0
  if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
    if confirm "ufw is on. Allow HTTP and HTTPS (ports 80 and 443) through it?"; then
      open_firewall=1
    else
      warn "Leaving ufw as it is; open ports 80 and 443 yourself."
    fi
  fi

  say ""
  confirm "Install Sideyard $VERSION in $BIN_DIR, with its data in $DATA_DIR?" || exit 0
  say ""

  download
  install_binary
  step "Writing $ENV_FILE"
  umask 077 # the env file, Sideyard's database and its keys are root's alone
  install -d -m 711 "$DATA_DIR"
  install -d -m 755 "$CONF_DIR"
  cat >"$ENV_FILE.new" <<EOF
# Sideyard's settings. Change them, then: systemctl restart sideyard
# Every option: SIDEYARD_* in docs/install.md.

# Projects get <name>.<domain>; the dashboard is sideyard.<domain>.
SIDEYARD_DOMAIN=$domain
# Let's Encrypt account email.
SIDEYARD_ACME_EMAIL=$email
# This server's public address, for the dashboard's DNS checks.
SIDEYARD_PUBLIC_IP=$public_ip
EOF
  chmod 600 "$ENV_FILE.new"
  mv -f "$ENV_FILE.new" "$ENV_FILE"

  step "Setting the admin password"
  printf '%s\n' "$password" | SIDEYARD_DATA_DIR=$DATA_DIR "$BIN" set-password >/dev/null

  # The service goes in last: it's what marks Sideyard installed, so a run
  # that stops before here starts over next time.
  step "Writing the sideyard service"
  cat >"$UNIT_FILE.new" <<EOF
# Sideyard runs as root so it can give each project its own Unix user and run
# every release as a sandboxed systemd unit (sy-<instance>-<project>-r<n>-….service
# in sideyard.slice). Those units aren't part of this service, so restarting or
# upgrading Sideyard leaves the apps running.
[Unit]
Description=Sideyard
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=$ENV_FILE
Environment=SIDEYARD_DATA_DIR=$DATA_DIR
ExecStart=$BIN
Restart=always
RestartSec=2
TimeoutStopSec=30
# Files Sideyard creates (its database, keys, env files) are private by default.
UMask=0077
NoNewPrivileges=true
# /usr and /boot read-only. /etc stays writable for useradd, /opt for updates.
ProtectSystem=true
ProtectHome=true
PrivateTmp=true
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
  chmod 644 "$UNIT_FILE.new"
  mv -f "$UNIT_FILE.new" "$UNIT_FILE"

  if [ "$open_firewall" = 1 ]; then
    step "Allowing HTTP and HTTPS through ufw"
    ufw allow 80/tcp >/dev/null
    ufw allow 443/tcp >/dev/null
  fi

  step "Starting Sideyard"
  start_service
  wait_until_up || die "Sideyard didn't start. See: journalctl -u sideyard -n 50. Running this again retries."

  say ""
  say "${GREEN}✓${RESET} ${BOLD}Sideyard $VERSION is running.${RESET}"
  say ""
  say "  Dashboard   https://sideyard.$domain"
  if [ "$generated" = 1 ]; then
    say "  Password    $password   ${DIM}(generated; shown only now)${RESET}"
  else
    say "  Password    the one you chose"
  fi
  say ""
  say "  ${DIM}Logs        journalctl -u sideyard -f${RESET}"
  say "  ${DIM}Updates     the dashboard offers them; or run this installer again${RESET}"
  say "  ${DIM}Password    sudo sideyard reset-password (prints a new one)${RESET}"
  say ""
}

# --- main -----------------------------------------------------------------

main() {
  WANT_VERSION=""
  local assume_yes=0
  while [ $# -gt 0 ]; do
    case "$1" in
    --version)
      [ $# -ge 2 ] || die "--version needs a value, like v1.2.3"
      WANT_VERSION="$2"
      shift 2
      ;;
    --version=*)
      WANT_VERSION="${1#*=}"
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
    *) die "unknown option $1 (try --help)" ;;
    esac
  done

  INTERACTIVE=0
  if [ "$assume_yes" = 0 ] && (exec </dev/tty) 2>/dev/null; then
    INTERACTIVE=1
  fi

  [ "$(uname -s)" = Linux ] || die "Sideyard runs on Linux."
  [ "$(id -u)" = 0 ] || die "Run this as root, e.g.: curl -fsSL <install.sh link> | sudo bash"
  [ -d /run/systemd/system ] || die "Sideyard needs systemd, and this system isn't running it."
  command -v curl >/dev/null || die "curl is needed."
  command -v sha256sum >/dev/null || die "sha256sum (coreutils) is needed."
  SOURCE="${SOURCE%/}"
  case "$SOURCE" in
  https://*) ;;
  http://127.* | http://localhost | http://localhost:* | http://\[::1\]*) ;; # a release being tried on this machine
  http://*) die "Releases must come over https, since their checksums travel with them. ($SOURCE)" ;;
  *) die "This copy of install.sh doesn't say where releases are. Use the one attached to a release, or set SIDEYARD_SOURCE=https://github.com/OWNER/REPO." ;;
  esac
  case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) die "No Sideyard build for $(uname -m); there are amd64 and arm64 builds." ;;
  esac
  ASSET=sideyard-linux-$ARCH

  # Downloads go next to where the binary lives, not /tmp: /tmp is often
  # mounted noexec, and the download has to run to prove it works.
  install -d -m 755 "$(dirname "$BIN_DIR")"
  TMP=$(mktemp -d "$(dirname "$BIN_DIR")/.download.XXXXXX")
  trap 'rm -rf "$TMP"' EXIT

  fetch_release
  # Installed means a sideyard service exists, written by this script or by
  # hand. Otherwise only our own unfinished install may be redone.
  if [ -f "$UNIT_FILE" ] || systemctl cat sideyard >/dev/null 2>&1; then
    upgrade
  elif [ -f "$DATA_DIR/sideyard.db" ] && [ ! -f "$ENV_FILE" ]; then
    die "$DATA_DIR holds a Sideyard database, but there's no sideyard service. Move it aside for a fresh install, or write the service (docs/install.md)."
  else
    fresh_install
  fi
}

main "$@"

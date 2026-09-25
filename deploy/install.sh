#!/usr/bin/env bash
#
# WGPanel - Main panel installer (control-plane API + Postgres/Timescale + Redis + frontend + Caddy)
# Run this on the server that will host the admin panel and API (NOT on the WireGuard nodes
# themselves - those use install-node.sh).
#
# Usage:
#   sudo bash install.sh
#
set -euo pipefail

INSTALL_DIR="/opt/wgpanel"
COMPOSE_FILE="$INSTALL_DIR/docker-compose.yml"
ENV_FILE="$INSTALL_DIR/.env"
SCRIPT_SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# The README's documented install method is `curl .../install.sh -o install.sh &&
# sudo bash install.sh` - a single file, not a git clone - but setup_files() below
# needs docker-compose.yml/Caddyfile/.env.example/wgpanel sitting right next to this
# script. ensure_deploy_files (called from main, before anything reads
# SCRIPT_SOURCE_DIR) detects that mismatch and fetches those companion files from
# the same repo/branch into a temp dir, repointing SCRIPT_SOURCE_DIR there - a real
# bug hit verifying the README's own instructions on a fresh server, not a
# hypothetical.
REPO_RAW_BASE="https://raw.githubusercontent.com/iransinfo/WGPanel/main/deploy"
DEPLOY_COMPANION_FILES=(docker-compose.yml Caddyfile .env.example wgpanel)

# The panel's own server can optionally run as this WGPanel's first WireGuard node,
# the same way install-node.sh sets up a remote one: a production Docker container
# (NODE_IMAGE) managed by compose under NODE_DIR (docs/STORY-09-multi-node-accounts.md).
NODE_DIR="/opt/wgpanel-node"
NODE_COMPOSE_FILE="$NODE_DIR/docker-compose.yml"
NODE_ENV_FILE="$NODE_DIR/.env"
WGPANEL_REPO_URL="${WGPANEL_REPO_URL:-https://github.com/iransinfo/WGPanel.git}"

log()  { echo -e "\033[1;32m[wgpanel]\033[0m $*"; }
warn() { echo -e "\033[1;33m[wgpanel]\033[0m $*"; }
err()  { echo -e "\033[1;31m[wgpanel]\033[0m $*" >&2; }

# read_required re-prompts until the answer is non-empty (a stray blank line in a
# paste would otherwise answer the NEXT prompt); read_default applies a default on
# Enter. Both strip pasted CRs (CRLF clipboards). Same helpers as install-node.sh.
read_required() {
  local prompt="$1" var="$2" val=""
  while [[ -z "$val" ]]; do
    read -rp "$prompt" val
    val="${val//$'\r'/}"
  done
  printf -v "$var" '%s' "$val"
}

read_default() {
  local prompt="$1" var="$2" def="$3" val
  read -rp "$prompt" val
  val="${val//$'\r'/}"
  printf -v "$var" '%s' "${val:-$def}"
}

valid_port() { [[ "$1" =~ ^[0-9]+$ ]] && (( 10#$1 >= 1 && 10#$1 <= 65535 )); }

# valid_cidr4 checks the whole value, not just its shape: 999.1.1.1/40 matches a
# naive digits regex but only fails much later (wg-quick, or a 400 from the API).
valid_cidr4() {
  local addr="$1" prefix o
  [[ "$addr" =~ ^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})/([0-9]{1,2})$ ]] || return 1
  # 10# forces base-10: a leading zero ("08") would otherwise be parsed as
  # (invalid) octal and abort the script under set -e.
  prefix="${BASH_REMATCH[5]}"
  (( 10#$prefix >= 1 && 10#$prefix <= 32 )) || return 1
  for o in "${BASH_REMATCH[@]:1:4}"; do
    (( 10#$o <= 255 )) || return 1
  done
}

# env_get reads KEY's value from .env without tripping set -e/pipefail: grep exits
# non-zero when the key is missing - entirely possible with a legacy .env predating
# newer keys (the PANEL_HTTP_PORT backfill in setup_files exists for exactly that) -
# and a bare VAR="$(grep ... | cut ...)" assignment then kills the whole script at
# that line with no message at all.
env_get() {
  grep -m1 "^$1=" "$ENV_FILE" 2>/dev/null | cut -d= -f2- || true
}

# --fresh / --reset wipes any prior install (containers, volumes, secrets) for a
# genuinely clean setup. Off by default so a normal re-run never destroys data.
FRESH=0
parse_args() {
  for a in "$@"; do
    case "$a" in
      --fresh|--reset) FRESH=1 ;;
      -h|--help)
        echo "Usage: sudo bash install.sh [--fresh]"
        echo "  --fresh   Remove any existing WGPanel stack, self-node, secrets, and data"
        echo "            volumes first, then install cleanly. DESTROYS the database."
        exit 0
        ;;
      *) err "Unknown argument: $a (try --help)"; exit 1 ;;
    esac
  done
}

# fresh_reset tears down a prior install so the run below starts from nothing. Runs
# only under --fresh, and only after Docker is available (it uses docker compose to
# remove the named volumes, which a plain `rm` can't touch).
fresh_reset() {
  [[ "$FRESH" -eq 1 ]] || return 0
  warn "--fresh: removing any existing WGPanel install (containers, volumes, secrets, self-node)."
  if [[ -f "$INSTALL_DIR/docker-compose.yml" ]]; then
    (cd "$INSTALL_DIR" && docker compose down -v --remove-orphans 2>/dev/null) || true
  fi
  if [[ -f "$NODE_COMPOSE_FILE" ]]; then
    (cd "$NODE_DIR" && docker compose down -v --remove-orphans 2>/dev/null) || true
  fi
  # Belt-and-suspenders for a stack whose compose file is already gone but whose
  # volumes/containers linger from an older layout.
  docker ps -aq --filter "name=^/wgpanel" | xargs -r docker rm -f >/dev/null 2>&1 || true
  rm -f "$ENV_FILE"
  rm -rf "$NODE_DIR" "$INSTALL_DIR/state"
  log "Previous install removed - continuing with a clean setup."
}

ensure_deploy_files() {
  local f
  for f in "${DEPLOY_COMPANION_FILES[@]}"; do
    if [[ ! -f "$SCRIPT_SOURCE_DIR/$f" ]]; then
      log "Running as a standalone script - downloading companion files (docker-compose.yml, Caddyfile, .env.example, wgpanel) from GitHub..."
      local tmpdir; tmpdir="$(mktemp -d)"
      for f in "${DEPLOY_COMPANION_FILES[@]}"; do
        curl -fsSL "$REPO_RAW_BASE/$f" -o "$tmpdir/$f"
      done
      SCRIPT_SOURCE_DIR="$tmpdir"
      return
    fi
  done
}

require_root() {
  if [[ "$EUID" -ne 0 ]]; then
    err "Please run as root (sudo bash install.sh)"
    exit 1
  fi
}

detect_os() {
  if ! command -v apt-get >/dev/null 2>&1; then
    err "This installer currently supports Debian/Ubuntu (apt-based) systems only."
    exit 1
  fi
}

install_prereqs() {
  log "Installing base packages..."
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -y
  # git is only used by the self-node's build-from-source fallback (ensure_self_node_image).
  apt-get install -y curl ca-certificates gnupg lsb-release ufw openssl git
}

install_docker() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    log "Docker + Compose plugin already installed, skipping."
    # Still make sure it starts on boot - a preinstalled-but-disabled daemon would
    # leave the whole panel dead after the first reboot.
    systemctl enable --now docker >/dev/null 2>&1 || true
    return
  fi
  log "Installing Docker Engine + Compose plugin..."
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
}

random_secret() { openssl rand -hex 24; }

setup_files() {
  mkdir -p "$INSTALL_DIR"
  cp "$SCRIPT_SOURCE_DIR/docker-compose.yml" "$INSTALL_DIR/docker-compose.yml"
  cp "$SCRIPT_SOURCE_DIR/Caddyfile" "$INSTALL_DIR/Caddyfile"

  if [[ -f "$ENV_FILE" ]]; then
    warn ".env already exists at $ENV_FILE - leaving existing values untouched. Re-run with --fresh (or delete it) for a clean setup."
    # An existing .env predates whatever keys this script has added since it was
    # first created (e.g. PANEL_HTTP_PORT/PANEL_HTTPS_PORT) - the fresh-install
    # branch below prompts for these once, but "leave it untouched" then means the
    # prompt is skipped forever on a pre-existing .env, silently falling back to
    # docker-compose's :-80/:-443 defaults with no way to know that happened until
    # a port conflict shows up. Found live: a second server's .env predated these
    # keys, install.sh never asked, and Caddy failed to bind because 443 was
    # already taken by something else on that host.
    if ! grep -q '^PANEL_HTTP_PORT=' "$ENV_FILE"; then
      while true; do
        read_default "Public HTTP port [80]: " PANEL_HTTP_PORT 80
        valid_port "$PANEL_HTTP_PORT" && break
        warn "The port must be a number between 1 and 65535."
      done
      echo "PANEL_HTTP_PORT=${PANEL_HTTP_PORT}" >> "$ENV_FILE"
    fi
    if ! grep -q '^PANEL_HTTPS_PORT=' "$ENV_FILE"; then
      while true; do
        read_default "Public HTTPS port [443]: " PANEL_HTTPS_PORT 443
        valid_port "$PANEL_HTTPS_PORT" && break
        warn "The port must be a number between 1 and 65535."
      done
      echo "PANEL_HTTPS_PORT=${PANEL_HTTPS_PORT}" >> "$ENV_FILE"
    fi
    return
  fi

  cp "$SCRIPT_SOURCE_DIR/.env.example" "$ENV_FILE"

  # The domain lands in the Caddy site address and the self-node's public_endpoint -
  # an empty answer (stray Enter), a pasted URL scheme, or a trailing path would all
  # produce a panel that installs "fine" and then fails ACME/routing much later.
  while true; do
    read_required "Panel domain (must already point to this server's IP, e.g. panel.example.com): " PANEL_DOMAIN
    PANEL_DOMAIN="${PANEL_DOMAIN#http://}"
    PANEL_DOMAIN="${PANEL_DOMAIN#https://}"
    PANEL_DOMAIN="${PANEL_DOMAIN%%/*}"
    [[ "$PANEL_DOMAIN" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]] && break
    warn "That doesn't look like a hostname - enter just the domain, e.g. panel.example.com."
  done
  while true; do
    read_required "Admin e-mail (used for Let's Encrypt notices): " ADMIN_EMAIL
    [[ "$ADMIN_EMAIL" =~ ^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]] && break
    warn "That doesn't look like an e-mail address."
  done
  while true; do
    read_default "Desired super-admin username [admin]: " ADMIN_USER admin
    [[ "$ADMIN_USER" =~ ^[A-Za-z0-9._-]+$ ]] && break
    warn "Usernames can only contain letters, digits, . _ -"
  done
  # Only relevant if 80/443 are already taken by another service on this host -
  # Caddy itself still binds :80/:443 inside its own container either way (its
  # automatic-HTTPS/ACME logic assumes that internally); this only changes which
  # host-side port maps to it.
  while true; do
    read_default "Public HTTP port [80]: " PANEL_HTTP_PORT 80
    valid_port "$PANEL_HTTP_PORT" && break
    warn "The port must be a number between 1 and 65535."
  done
  while true; do
    read_default "Public HTTPS port [443]: " PANEL_HTTPS_PORT 443
    valid_port "$PANEL_HTTPS_PORT" && break
    warn "The port must be a number between 1 and 65535."
  done

  PG_PASS="$(random_secret)"
  REDIS_PASS="$(random_secret)"
  JWT_SECRET="$(random_secret)"
  HMAC_KEY="$(openssl rand -hex 32)" # exactly 32 bytes for AES-256-GCM - encrypts api_keys.secret_encrypted (STORY-05)
  INTERNAL_TOKEN="$(random_secret)"
  ACCOUNT_KEY_ENC="$(openssl rand -hex 32)" # exactly 32 bytes for AES-256-GCM

  sed -i \
    -e "s#^PANEL_DOMAIN=.*#PANEL_DOMAIN=${PANEL_DOMAIN}#" \
    -e "s#^ADMIN_ACL_EMAIL=.*#ADMIN_ACL_EMAIL=${ADMIN_EMAIL}#" \
    -e "s#^PANEL_HTTP_PORT=.*#PANEL_HTTP_PORT=${PANEL_HTTP_PORT}#" \
    -e "s#^PANEL_HTTPS_PORT=.*#PANEL_HTTPS_PORT=${PANEL_HTTPS_PORT}#" \
    -e "s#^POSTGRES_PASSWORD=.*#POSTGRES_PASSWORD=${PG_PASS}#" \
    -e "s#^REDIS_PASSWORD=.*#REDIS_PASSWORD=${REDIS_PASS}#" \
    -e "s#^JWT_SECRET=.*#JWT_SECRET=${JWT_SECRET}#" \
    -e "s#^API_HMAC_MASTER_KEY=.*#API_HMAC_MASTER_KEY=${HMAC_KEY}#" \
    -e "s#^INTERNAL_API_TOKEN=.*#INTERNAL_API_TOKEN=${INTERNAL_TOKEN}#" \
    -e "s#^ACCOUNT_KEY_ENCRYPTION_KEY=.*#ACCOUNT_KEY_ENCRYPTION_KEY=${ACCOUNT_KEY_ENC}#" \
    -e "s#^ADMIN_BOOTSTRAP_USERNAME=.*#ADMIN_BOOTSTRAP_USERNAME=${ADMIN_USER}#" \
    "$ENV_FILE"

  chmod 600 "$ENV_FILE"
  log "Secrets generated and saved to $ENV_FILE (permissions 600)."
}

setup_firewall() {
  local rule ssh_port node_agent_port http_port https_port
  log "Configuring firewall (ufw)..."
  # Never lock out SSH: the OpenSSH app profile only exists when openssh-server is
  # installed from apt, and sshd may listen on a custom port - "ufw allow OpenSSH"
  # silently failing followed by "ufw enable" (default deny incoming) would cut this
  # session off. Allow the ports sshd is actually listening on as well.
  ufw allow OpenSSH >/dev/null 2>&1 || true
  while read -r ssh_port; do
    [[ -n "$ssh_port" ]] && ufw allow "${ssh_port}/tcp" >/dev/null 2>&1 || true
  done < <(ss -tlnpH 2>/dev/null | awk '/sshd/ {n = split($4, a, ":"); print a[n]}' | sort -u)
  # Node agents connect back to the control plane on NODE_AGENT_PORT over the public
  # internet. A broken ufw/iptables ("ERROR: problem running iptables/ufw-init" -
  # classically a kernel upgraded without a reboot) must not abort the install this
  # late; warn with the exact rule to add by hand once ufw is healthy again.
  # The web ports come from .env, NOT hardcoded 80/443: a panel installed on custom
  # PANEL_HTTP_PORT/PANEL_HTTPS_PORT would otherwise get the wrong ports opened.
  node_agent_port="$(env_get NODE_AGENT_PORT)"
  http_port="$(env_get PANEL_HTTP_PORT)"
  https_port="$(env_get PANEL_HTTPS_PORT)"
  for rule in "${http_port:-80}/tcp" "${https_port:-443}/tcp" "${node_agent_port:-48443}/tcp"; do
    if ! ufw allow "$rule"; then
      warn "ufw could not add the ${rule} rule (see the error above) - continuing anyway."
      warn "If the error mentions iptables, a reboot usually fixes it (pending kernel"
      warn "upgrade); then run: ufw allow ${rule}"
    fi
  done
  # ufw's default FORWARD policy is DROP, which also drops the traffic Docker forwards
  # for the self-node container's clients. Setting it to ACCEPT lets Docker's own
  # per-bridge FORWARD rules govern forwarding (they're specific, not blanket), which
  # is what the containerized node relies on for full-tunnel client internet.
  if [[ -f /etc/default/ufw ]]; then
    sed -i 's/^DEFAULT_FORWARD_POLICY=.*/DEFAULT_FORWARD_POLICY="ACCEPT"/' /etc/default/ufw
  fi
  yes | ufw enable >/dev/null 2>&1 || true
  ufw reload >/dev/null 2>&1 || true
}

# preflight_ports fails fast with a clear message if a host port the stack must
# publish is already taken - far friendlier than the mid-`docker compose up`
# "failed to start userland proxy / docker-proxy" error you get otherwise. Checks
# the publicly-bound ports (NODE_AGENT_PORT and Caddy's web ports - a taken 443 is
# the setup_files comment's own live incident); the loopback API/frontend ports
# rarely clash and Docker's own message for those is at least specific.
preflight_ports() {
  local port pid
  command -v ss >/dev/null 2>&1 || return 0
  for port in "$(env_get NODE_AGENT_PORT)" "$(env_get PANEL_HTTP_PORT)" "$(env_get PANEL_HTTPS_PORT)"; do
    [[ -n "$port" ]] || continue
    ss -tlnH "sport = :${port}" 2>/dev/null | grep -q . || continue
    pid="$(ss -tlnpH "sport = :${port}" 2>/dev/null | grep -oE 'users:\(\("[^"]+"' | head -n1 | sed -E 's/.*"([^"]+)"/\1/')"
    # docker-proxy means OUR already-running stack holds it (this is a re-run) -
    # compose re-publishes the same port fine on --force-recreate; aborting here
    # would make every re-run over a live panel fail its own preflight.
    [[ "$pid" == "docker-proxy" ]] && continue
    err "Port ${port} is already in use${pid:+ by \"${pid}\"} - the stack can't publish it."
    err "  Common culprit: cockpit or prometheus (both default to 9090), or another web server on 80/443."
    err "  Fix: free the port (e.g. 'systemctl disable --now cockpit.socket'), or change the"
    err "       corresponding port in ${ENV_FILE} and open it in the firewall, then re-run."
    exit 1
  done
}

# Total RAM+swap (MB) this stack needs to boot reliably. TimescaleDB's first-boot plus
# the api's migration, and optionally the self-node container, all allocate at once; on
# a box below this the Go runtimes fail with "out of memory allocating heap arena map"
# or "pthread_create: Resource temporarily unavailable" and crash mid-start.
MIN_TOTAL_MEM_MB=3072

# preflight_resources guards against the single most common small-VPS failure: too
# little memory. It looks at RAM + swap together and, if the total is under the target,
# offers to create a swap file sized to reach it (not a flat amount) so the install
# actually completes instead of OOM-crashing the api / node / docker itself.
# Disk headroom (MB) to always leave free after a swap file - for image layers, the
# database volume, and growth. A swap file must never eat into this.
MIN_DISK_HEADROOM_MB=5120

preflight_resources() {
  local mem_kb swap_kb total_mb need_mb swap_gb avail_mb max_swap_mb
  mem_kb="$(awk '/^MemTotal:/{print $2}' /proc/meminfo 2>/dev/null || echo 0)"
  swap_kb="$(awk '/^SwapTotal:/{print $2}' /proc/meminfo 2>/dev/null || echo 0)"
  total_mb=$(( (mem_kb + swap_kb) / 1024 ))
  (( total_mb > 0 )) || return 0

  # Free disk where /swapfile lives (the root fs). Warn loudly if it's already tight -
  # a full disk shows up as "no space left on device" from docker-proxy/runc mid-start.
  avail_mb="$(df -Pm / 2>/dev/null | awk 'NR==2{print $4}')"
  avail_mb="${avail_mb:-0}"
  if (( avail_mb > 0 && avail_mb < MIN_DISK_HEADROOM_MB )); then
    warn "Only ~${avail_mb} MB free disk. The images + database need room; a nearly-full"
    warn "disk fails mid-start with 'no space left on device'. Free space (e.g. 'docker"
    warn "system prune -af') or use a larger disk before continuing."
  fi

  (( total_mb >= MIN_TOTAL_MEM_MB )) && return 0

  need_mb=$(( MIN_TOTAL_MEM_MB - total_mb ))
  swap_gb=$(( (need_mb + 1023) / 1024 ))   # round up to whole GB
  (( swap_gb < 2 )) && swap_gb=2

  # Never let the swap file fill the disk: cap it so MIN_DISK_HEADROOM_MB stays free.
  max_swap_mb=$(( avail_mb - MIN_DISK_HEADROOM_MB ))
  if (( max_swap_mb < 1024 )); then
    warn "Low memory (~${total_mb} MB RAM+swap) AND little free disk (~${avail_mb} MB) -"
    warn "not enough room to safely add swap. This server is undersized for the stack;"
    warn "it will likely crash with out-of-memory or out-of-disk errors. Use a server"
    warn "with more RAM and disk (2 GB RAM / 20 GB disk+), or free space and re-run."
    read -rp "Continue anyway? [y/N]: " CONT
    [[ "${CONT:-N}" =~ ^[Yy] ]] || exit 1
    return 0
  fi
  if (( swap_gb * 1024 > max_swap_mb )); then
    swap_gb=$(( max_swap_mb / 1024 ))
    warn "Capping swap to ${swap_gb} GB to keep ${MIN_DISK_HEADROOM_MB} MB disk free."
  fi

  warn "Low memory: this server has only ~${total_mb} MB RAM+swap. This stack needs"
  warn "~${MIN_TOTAL_MEM_MB} MB to boot reliably - below it, TimescaleDB/the API/the node"
  warn "crash mid-start with 'out of memory' / 'pthread_create: Resource temporarily"
  warn "unavailable' (not a clean error). Adding swap fixes it on small VPSes."
  read -rp "Create a ${swap_gb} GB swap file now (recommended)? [Y/n]: " MKSWAP
  if [[ ! "${MKSWAP:-Y}" =~ ^[Nn] ]]; then
    create_swap "$swap_gb"
  else
    warn "Continuing without swap - expect out-of-memory crashes if RAM is tight."
  fi
}

# create_swap makes (or resizes) /swapfile to <gb> GB and enables it persistently.
create_swap() {
  local gb="${1:-2}"
  log "Creating a ${gb} GB swap file at /swapfile..."
  swapoff /swapfile 2>/dev/null || true
  rm -f /swapfile
  fallocate -l "${gb}G" /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count=$(( gb * 1024 )) status=none
  chmod 600 /swapfile
  mkswap /swapfile >/dev/null 2>&1
  swapon /swapfile
  grep -q '^/swapfile ' /etc/fstab 2>/dev/null || echo '/swapfile none swap sw 0 0' >> /etc/fstab
  # On a low-RAM box the kernel needs a nudge to actually lean on swap under pressure.
  sysctl -w vm.swappiness=60 >/dev/null 2>&1 || true
  grep -q '^vm.swappiness' /etc/sysctl.d/99-wgpanel.conf 2>/dev/null || echo 'vm.swappiness=60' > /etc/sysctl.d/99-wgpanel.conf
  log "Swap enabled - total memory now $(awk '/^MemTotal:|^SwapTotal:/{s+=$2} END{printf "%d MB", s/1024}' /proc/meminfo)."
}

start_stack() {
  preflight_ports
  log "Pulling images and starting the stack..."
  # --force-recreate: plain `up -d` only recreates a container when compose detects
  # its own service definition changed - it does NOT notice that a bind-mounted
  # config file (Caddyfile, docker-compose.yml itself) changed on disk, so a
  # container can keep running against a stale file indefinitely. Found live: fixing
  # a real Caddyfile routing bug and updating the file on disk did nothing until the
  # container was explicitly force-recreated.
  (cd "$INSTALL_DIR" && docker compose pull)
  # Bring the stack up, but never let a slow first boot abort the whole install: the
  # api's first start migrates a fresh DB before it reports healthy, and compose can
  # return non-zero while waiting. show_first_admin_credentials waits for real health
  # next, so treat a non-zero here as "keep going" rather than a fatal error (which,
  # under `set -e`, would otherwise kill the script right at the finish line).
  if ! (cd "$INSTALL_DIR" && docker compose up -d --force-recreate); then
    warn "compose returned non-zero while starting (often just the api still migrating on first boot) - continuing and waiting for health below."
  fi
}

install_cli() {
  cp "$SCRIPT_SOURCE_DIR/wgpanel" /usr/local/bin/wgpanel
  chmod +x /usr/local/bin/wgpanel
  log "Installed 'wgpanel' management command (try: wgpanel status / wgpanel doctor)"
}

# Populated by show_first_admin_credentials, printed as part of the final summary at
# the very end of main() instead of immediately - previously this printed right
# before setup_self_node's own long, scrolling output (WireGuard setup, docker pulls,
# more prompts), so by the time the script finished the one-time password had
# already scrolled off screen. Same information, just held until the end so it's the
# last thing on screen, not the first.
ADMIN_CREDS_BLOCK=""

show_first_admin_credentials() {
  # The API auto-creates the super admin itself on its very first successful boot
  # against a fresh database (gated on the admins table being empty - see
  # cmd/api/main.go's bootstrapFirstAdmin), using ADMIN_BOOTSTRAP_USERNAME from .env.
  # This just waits for that boot to finish, then re-displays the one-time password it
  # printed to its own logs - it is never recoverable after this, so surface it clearly
  # rather than leaving the admin to go find it in `docker compose logs api` themselves.
  log "Waiting for the API to finish booting/migrating and creating the super admin..."
  if ! wgpanel_wait_for_health; then
    err "API did not become healthy within the timeout. Check: wgpanel logs api"
    err "If it recovers on its own, find the generated credentials with: wgpanel logs api | grep WGPANEL_INITIAL_ADMIN"
    return 1
  fi

  local creds
  creds="$(cd "$INSTALL_DIR" && docker compose logs api 2>/dev/null | grep 'WGPANEL_INITIAL_ADMIN_' || true)"
  if [[ -z "$creds" ]]; then
    warn "API is healthy but no bootstrap-admin log line was found (an admin may already exist from a prior install)."
    warn "If you need a new one, run: wgpanel create-admin --username <name>"
    return 0
  fi

  ADMIN_CREDS_BLOCK="$(echo "$creds" | sed -E 's/^.*(WGPANEL_INITIAL_ADMIN_[A-Z]+=.*)$/  \1/')"
}

wgpanel_wait_for_health() {
  # 180s, not 90: a fresh DB's first-boot migration + admin bootstrap can legitimately
  # run past 90s on a small/loaded VPS, and this wait is what surfaces the one-time
  # admin password - timing out early makes install.sh claim failure on a panel that's
  # actually still coming up.
  local api_port token timeout=180 waited=0
  api_port="$(env_get API_PORT)"
  token="$(env_get INTERNAL_API_TOKEN)"
  if [[ -z "$api_port" || -z "$token" ]]; then
    err "API_PORT or INTERNAL_API_TOKEN is missing from ${ENV_FILE} - cannot check API health."
    return 1
  fi
  while (( waited < timeout )); do
    curl -fsS -H "X-Internal-Token: ${token}" "http://127.0.0.1:${api_port}/internal/healthz" >/dev/null 2>&1 && return 0
    sleep 3
    waited=$((waited + 3))
  done
  return 1
}

# ---- core server as a node (docs/STORY-09-multi-node-accounts.md) ----
#
# Sets this same server up as this WGPanel's first WireGuard node - as a production
# Docker container (the same NODE_IMAGE install-node.sh uses on remote nodes), so a
# single-server install can create accounts immediately with no separate "add a node"
# step. The node container joins the panel's own compose network so its agent dials
# the API directly at api:NODE_AGENT_PORT. Idempotent: the presence of NODE_COMPOSE_FILE
# means this box is already set up, so re-running install.sh never creates a second node.
setup_self_node() {
  if [[ -f "$NODE_COMPOSE_FILE" ]]; then
    warn "This server is already set up as a node (found ${NODE_COMPOSE_FILE}) - skipping."
    return 0
  fi

  read -rp "Also set up WireGuard on this same server as your first node? [Y/n]: " DO_SELF_NODE
  if [[ "${DO_SELF_NODE:-Y}" =~ ^[Nn] ]]; then
    log "Skipping - add a node later with: Nodes -> Add Node in the panel, then install-node.sh on that server."
    return 0
  fi

  log "Configuring this server as a WireGuard node (Docker)..."
  # NODE_NAME/NODE_GROUP/NODE_CAPACITY are interpolated RAW into the bootstrap JSON
  # body - a quote in a name or a non-numeric capacity produces invalid JSON and a
  # cryptic API error, so every answer is validated here first. Every prompt has a
  # default so hitting Enter through the whole sequence produces a valid config
  # (an empty wg_subnet used to reach the API and 400).
  while true; do
    read_default "A name for this node [core]: " NODE_NAME core
    [[ "$NODE_NAME" =~ ^[A-Za-z0-9._-]+$ ]] && break
    warn "Node names can only contain letters, digits, . _ -"
  done
  while true; do
    read_default "Node group [default]: " NODE_GROUP default
    [[ "$NODE_GROUP" =~ ^[A-Za-z0-9._-]+$ ]] && break
    warn "Group names can only contain letters, digits, . _ -"
  done
  while true; do
    read_default "Max peers on this node [250]: " NODE_CAPACITY 250
    [[ "$NODE_CAPACITY" =~ ^[0-9]+$ ]] && (( 10#$NODE_CAPACITY >= 1 )) && break
    warn "Capacity must be a positive number."
  done
  while true; do
    read_default "WireGuard listen port [51820]: " WG_PORT 51820
    valid_port "$WG_PORT" && break
    warn "The port must be a number between 1 and 65535."
  done
  # Linux caps interface names at 15 chars (IFNAMSIZ); slashes/spaces would also
  # break the wg-quick config path inside the container.
  while true; do
    read_default "WireGuard interface name [wg0]: " WG_IFACE wg0
    [[ "$WG_IFACE" =~ ^[A-Za-z0-9_=+.-]{1,15}$ ]] && break
    warn "Interface names must be 1-15 characters (letters, digits, . _ = + -)."
  done
  while true; do
    read_default "WireGuard subnet for peer IPs [10.66.0.0/24]: " WG_SUBNET 10.66.0.0/24
    valid_cidr4 "$WG_SUBNET" && break
    warn "Expected a valid IPv4 subnet in CIDR form, e.g. 10.66.0.0/24."
  done
  # Auto-derived as the subnet's .1 (the convention used everywhere else here), still
  # overridable. The sed only rewrites a well-formed A.B.C.D/nn - guaranteed by the
  # valid_cidr4 gate above; on a non-match it would pass the input through unchanged.
  default_iface_addr="$(echo "$WG_SUBNET" | sed -E 's#^([0-9]+\.[0-9]+\.[0-9]+)\.[0-9]+/([0-9]+)$#\1.1/\2#')"
  while true; do
    read_default "This node's own interface address, with prefix [${default_iface_addr}]: " WG_IFACE_ADDR "$default_iface_addr"
    valid_cidr4 "$WG_IFACE_ADDR" && break
    warn "Expected a valid IPv4 address with a prefix length, e.g. 10.66.0.1/24."
  done

  local panel_domain node_agent_port node_image
  panel_domain="$(env_get PANEL_DOMAIN)"
  node_agent_port="$(env_get NODE_AGENT_PORT)"
  node_agent_port="${node_agent_port:-48443}"
  node_image="$(env_get NODE_IMAGE)"
  node_image="${node_image:-ghcr.io/iransinfo/wgpanel-node:latest}"

  setup_self_node_host || return 1
  bootstrap_self_node "$panel_domain" || return 1
  write_self_node_files "$node_image" "$node_agent_port" || return 1
  ensure_self_node_image "$node_image" || return 1
  (cd "$NODE_DIR" && docker compose up -d) || return 1
  ufw allow "${WG_PORT}/udp" >/dev/null 2>&1 || true

  # "Container running" says nothing about registration (a crash-looping agent stays
  # "running" under restart: unless-stopped) - watch the agent's own JSON log markers
  # instead, same as install-node.sh's verify().
  local logs deadline=$((SECONDS + 45))
  log "Waiting for the node to register with the control plane..."
  while (( SECONDS < deadline )); do
    sleep 3
    logs="$(cd "$NODE_DIR" && docker compose logs --no-color 2>/dev/null || true)"
    if grep -q '"msg":"registered"' <<<"$logs"; then
      log "This server is now registered as node '${NODE_NAME}' and should show 'online' in the panel shortly."
      return 0
    fi
    grep -q '"msg":"fatal"' <<<"$logs" && break
  done
  err "Node did not confirm registration - last log lines:"
  (cd "$NODE_DIR" && docker compose logs --no-color --tail 5) >&2 || true
  err "Check with: cd ${NODE_DIR} && docker compose logs -f"
  return 1
}

# Host prep for running WireGuard in a container: the kernel module (the container's
# wg-quick drives the host kernel), loaded now and on every boot. IP forwarding for
# the container's internet egress is handled by the Docker daemon itself.
setup_self_node_host() {
  apt-get install -y wireguard >/dev/null 2>&1 || true
  modprobe wireguard 2>/dev/null || warn "Could not load the wireguard kernel module - continuing (may be built in)."
  echo wireguard > /etc/modules-load.d/wireguard.conf
}

# Creates the node record and returns a join token via the internal bootstrap-self
# endpoint (CreateNode + join-token in one call - nodes_bootstrap.go). No public_key
# is sent: the container generates its keypair on first boot and submits the public
# key when its agent registers (agentserver.go's SetNodePublicKey), same as a remote
# node installed with install-node.sh.
bootstrap_self_node() {
  local panel_domain="$1"
  local api_port token resp
  api_port="$(env_get API_PORT)"
  token="$(env_get INTERNAL_API_TOKEN)"
  if [[ -z "$api_port" || -z "$token" ]]; then
    err "API_PORT or INTERNAL_API_TOKEN is missing from ${ENV_FILE} - cannot bootstrap the self-node."
    return 1
  fi

  # No -f (deliberately): it would swallow a non-2xx body, turning a real error (e.g.
  # "wg_subnet is required") into an empty string. An empty JOIN_TOKEN detects failure.
  resp=$(curl -sS -X POST "http://127.0.0.1:${api_port}/internal/nodes/bootstrap-self" \
    -H "X-Internal-Token: ${token}" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"${NODE_NAME}\",\"node_group\":\"${NODE_GROUP}\",\"public_endpoint\":\"${panel_domain}:${WG_PORT}\",\"wg_subnet\":\"${WG_SUBNET}\",\"capacity_max_peers\":${NODE_CAPACITY}}")

  JOIN_TOKEN="$(echo "$resp" | grep -o '"token":"[^"]*"' | cut -d'"' -f4)"
  if [[ -z "$JOIN_TOKEN" ]]; then
    err "Could not bootstrap this node - response: ${resp}"
    return 1
  fi
}

# Writes the node's compose + env under NODE_DIR. Bridge networking (the container
# NATs client traffic in its own netns, so no host DOCKER-USER handling is needed),
# but attached to the panel's existing compose network so the agent reaches the API at
# api:NODE_AGENT_PORT with no public hairpin. Only the WireGuard UDP port is published.
write_self_node_files() {
  local node_image="$1" node_agent_port="$2"
  mkdir -p "$NODE_DIR"

  cat > "$NODE_ENV_FILE" <<EOF
NODE_IMAGE=${node_image}
WGPANEL_PANEL_ADDR=api:${node_agent_port}
WGPANEL_JOIN_TOKEN=${JOIN_TOKEN}
WGPANEL_NODE_NAME=${NODE_NAME}
WGPANEL_WG_INTERFACE=${WG_IFACE}
WGPANEL_WG_PORT=${WG_PORT}
WG_IFACE_ADDR=${WG_IFACE_ADDR}
EOF
  chmod 600 "$NODE_ENV_FILE"

  cat > "$NODE_COMPOSE_FILE" <<'EOF'
name: wgpanel-node

services:
  node:
    image: ${NODE_IMAGE}
    restart: unless-stopped
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "5"
    cap_add:
      - NET_ADMIN
    security_opt:
      - no-new-privileges:true
    sysctls:
      - net.ipv4.ip_forward=1
    env_file:
      - .env
    networks:
      - panel
    ports:
      - "${WGPANEL_WG_PORT:-51820}:${WGPANEL_WG_PORT:-51820}/udp"
    volumes:
      - node_wg_state:/etc/wireguard
      - node_agent_state:/etc/wgpanel/state
    deploy:
      resources:
        limits:
          memory: ${NODE_MEM_LIMIT:-256m}

networks:
  panel:
    # The panel stack (name: wgpanel) creates this; the node joins it to reach api:PORT.
    external: true
    name: wgpanel_default

volumes:
  node_wg_state:
  node_agent_state:
EOF
}

# Pulls the node image; falls back to building it from source if the pull fails
# (private GHCR package, or before CI has published it) - mirrors install-node.sh.
ensure_self_node_image() {
  local node_image="$1"
  if (cd "$NODE_DIR" && docker compose pull); then
    return 0
  fi
  warn "Could not pull ${node_image} - building the node image from source instead."
  local src="/tmp/wgpanel-src.$$"
  rm -rf "$src"
  git clone --depth 1 "$WGPANEL_REPO_URL" "$src" || return 1
  docker build -f "$src/deploy/node.Dockerfile" -t wgpanel-node:local "$src" || { rm -rf "$src"; return 1; }
  rm -rf "$src"
  sed -i 's#^NODE_IMAGE=.*#NODE_IMAGE=wgpanel-node:local#' "$NODE_ENV_FILE"
}

main() {
  parse_args "$@"
  require_root
  detect_os
  ensure_deploy_files
  install_prereqs
  install_docker
  preflight_resources
  fresh_reset
  setup_files
  setup_firewall
  start_stack
  install_cli
  show_first_admin_credentials || true

  # Run in a subshell with errexit disabled: setup_self_node is a nice-to-have on
  # top of an already-fully-working panel, and every step inside it is explicitly
  # `|| return 1`-guarded, so a failure partway through (docker cp, wg genkey,
  # the bootstrap API call, whatever) should stop just that flow, not abort this
  # whole script this late after the panel and its first admin already exist.
  # The rc MUST be captured via `|| ...`: a bare `( ... ); rc=$?` still trips the
  # parent's set -e on the subshell's non-zero exit, killing the script right here -
  # before the final summary (and the one-time admin password) ever prints.
  self_node_rc=0
  ( set +e; setup_self_node ) || self_node_rc=$?
  if [[ $self_node_rc -ne 0 ]]; then
    warn "Self-node setup did not complete - the panel itself is still fully usable; add a node manually with install-node.sh whenever you're ready."
  fi

  # Read back from the env file rather than reusing setup_files()'s PANEL_DOMAIN/
  # PANEL_HTTPS_PORT variables directly - those are only ever set on a fresh
  # install; setup_files() returns early without touching them at all when .env
  # already existed, which under `set -u` would otherwise crash here exactly like
  # cmd_backup's leaked RETURN trap did.
  PANEL_DOMAIN="$(env_get PANEL_DOMAIN)"
  PANEL_HTTPS_PORT="$(env_get PANEL_HTTPS_PORT)"
  panel_display_url="https://${PANEL_DOMAIN}"
  if [[ -n "$PANEL_HTTPS_PORT" && "$PANEL_HTTPS_PORT" != "443" ]]; then
    panel_display_url="${panel_display_url}:${PANEL_HTTPS_PORT}"
  fi
  echo
  echo "=================================================================="
  echo " WGPanel install complete"
  echo "=================================================================="
  echo "  Panel address:  ${panel_display_url}"
  if [[ -n "$ADMIN_CREDS_BLOCK" ]]; then
    echo
    echo "  Login (save these now - shown only this once):"
    echo "$ADMIN_CREDS_BLOCK"
  else
    echo
    echo "  An admin already existed - recover credentials with: wgpanel show-bootstrap-admin"
  fi
  echo "=================================================================="
  log "(Caddy needs a minute to obtain the TLS certificate on first boot - check with: wgpanel logs caddy)"
  log "Manage the stack with: wgpanel {start|stop|restart|status|logs|update|rollback|backup|restore|doctor|create-admin}"
  log "To add more WireGuard nodes later: Nodes -> Add Node in the panel, then run install-node.sh on that server."
}

main "$@"

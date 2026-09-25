#!/usr/bin/env bash
#
# WGPanel - Node agent installer (production Docker)
# Run this on EACH server that will actually run WireGuard and terminate customer
# connections. It installs Docker, then runs the WireGuard node as a production
# container (deploy/node.Dockerfile image) managed by docker compose - the same
# proven model the in-repo dev node uses, just pulled from the registry and set to
# restart on boot. The container connects OUTBOUND to the control plane (no inbound
# access to the panel needed), applies peer changes via wgctrl, terminates client
# tunnels, and NATs their traffic to the internet.
#
# Usage:
#   sudo bash install-node.sh
#
# You will be asked for:
#   - the control-plane address (e.g. panel.example.com:48443)
#   - a one-time join token, generated from the admin panel: Nodes -> Add Node -> Generate Token
#   - this node's WireGuard subnet .1 address (must match the subnet you set in the panel)
#
# The node image is pulled from $NODE_IMAGE (GHCR by default). If it can't be pulled
# (private package, or before CI has published it), the script falls back to building
# it from source - override the source with WGPANEL_REPO_URL.
set -euo pipefail

NODE_DIR="/opt/wgpanel-node"
COMPOSE_FILE="$NODE_DIR/docker-compose.yml"
ENV_FILE="$NODE_DIR/.env"
NODE_IMAGE="${NODE_IMAGE:-ghcr.io/iransinfo/wgpanel-node:latest}"
WGPANEL_REPO_URL="${WGPANEL_REPO_URL:-https://github.com/iransinfo/WGPanel.git}"

log()  { echo -e "\033[1;32m[wgpanel-node]\033[0m $*"; }
warn() { echo -e "\033[1;33m[wgpanel-node]\033[0m $*"; }
err()  { echo -e "\033[1;31m[wgpanel-node]\033[0m $*" >&2; }

require_root() {
  if [[ "$EUID" -ne 0 ]]; then
    err "Please run as root (sudo bash install-node.sh)"
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
  log "Installing base packages and the WireGuard kernel module..."
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -y
  # wireguard-tools is not needed on the host (it lives in the container), but the
  # kernel MODULE is: the container's wg-quick drives the host kernel's WireGuard.
  # git is only used by the build-from-source fallback.
  apt-get install -y curl ca-certificates ufw wireguard git

  # Load the module now and on every boot - the container relies on it being present.
  if ! modprobe wireguard 2>/dev/null; then
    warn "Could not load the wireguard kernel module (kernel may have it built in, or needs wireguard-dkms). Continuing."
  fi
  echo wireguard > /etc/modules-load.d/wireguard.conf
}

install_docker() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    log "Docker + Compose plugin already installed, skipping."
    # Still make sure it starts on boot - a preinstalled-but-disabled daemon would
    # leave the node dead after the first reboot, despite restart: unless-stopped.
    systemctl enable --now docker >/dev/null 2>&1 || true
    return
  fi
  log "Installing Docker Engine + Compose plugin..."
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
}

# read_required re-prompts until the answer is non-empty. Without this, a stray
# blank line in a paste (very easy to hit when copy-pasting the join token with a
# trailing newline) silently answers the NEXT prompt, and required settings land
# empty in .env - the node then fails in ways that only surface much later.
# Pasted CRs (CRLF clipboards) are stripped too.
read_required() {
  local prompt="$1" var="$2" val=""
  while [[ -z "$val" ]]; do
    read -rp "$prompt" val
    val="${val//$'\r'/}"
  done
  printf -v "$var" '%s' "$val"
}

# valid_cidr4 checks the whole value, not just its shape: 999.1.1.1/40 matches a
# naive digits regex but only fails much later, inside the container's wg-quick,
# where the error is far harder to trace back to this prompt.
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

prompt_config() {
  local probe
  mkdir -p "$NODE_DIR"

  # The agent dials https://<addr>/agent/* - that's the panel's NODE_AGENT_PORT
  # (48443 by default), NOT the panel web UI port and NOT WireGuard's UDP port.
  # Getting this wrong is the most common install mistake, so probe it over TCP
  # before accepting the answer.
  while true; do
    read_required "Control plane address (host:port - the panel's NODE_AGENT_PORT, e.g. panel.example.com:48443): " PANEL_ADDR
    # The agent prepends https:// itself, so a pasted URL scheme or path would
    # produce https://https://... and break registration - strip both.
    PANEL_ADDR="${PANEL_ADDR#http://}"
    PANEL_ADDR="${PANEL_ADDR#https://}"
    PANEL_ADDR="${PANEL_ADDR%%/*}"
    if [[ "$PANEL_ADDR" != *:* ]] || [[ -z "${PANEL_ADDR%:*}" ]] ||
       [[ ! "${PANEL_ADDR##*:}" =~ ^[0-9]+$ ]] ||
       (( 10#${PANEL_ADDR##*:} < 1 || 10#${PANEL_ADDR##*:} > 65535 )); then
      warn "Expected host:port with a numeric port, e.g. panel.example.com:48443."
      continue
    fi
    if ! timeout 5 bash -c ": </dev/tcp/${PANEL_ADDR%:*}/${PANEL_ADDR##*:}" 2>/dev/null; then
      warn "Cannot reach ${PANEL_ADDR} over TCP. Check the address: the port must be the"
      warn "panel's node-agent port (NODE_AGENT_PORT in the panel's .env, 48443 by default),"
      warn "not the WireGuard port, and it must be open in the panel server's firewall."
      read -rp "Use ${PANEL_ADDR} anyway? [y/N]: " CONFIRM
      [[ "${CONFIRM,,}" == y* ]] && break
      continue
    fi
    # Reachable is not enough: the panel's WEB port (443) also answers TCP. The real
    # agent endpoint replies with plain text/JSON, never HTML - an HTML answer means
    # this is the web UI and registration would die with an nginx "405 Not Allowed".
    # Buffered in a variable rather than piped to grep -q: under pipefail, grep's
    # early exit can SIGPIPE curl and turn a positive match into a skipped warning.
    probe="$(curl -skm 5 "https://${PANEL_ADDR}/agent/register" 2>/dev/null || true)"
    if grep -qiE '<html|<!doctype' <<<"$probe"; then
      warn "${PANEL_ADDR} answers like the panel's WEB UI, not the node-agent API."
      warn "Enter the panel's node-agent port instead: NODE_AGENT_PORT in the panel"
      warn "server's /opt/wgpanel/.env (48443 by default) - e.g. ${PANEL_ADDR%:*}:48443."
      read -rp "Use ${PANEL_ADDR} anyway? [y/N]: " CONFIRM
      [[ "${CONFIRM,,}" == y* ]] && break
      continue
    fi
    break
  done

  read_required "Join token (from admin panel -> Nodes -> Add Node): " JOIN_TOKEN
  read_required "A name for this node (e.g. de-frankfurt-1): " NODE_NAME

  while true; do
    read -rp "WireGuard listen port [51820]: " WG_PORT
    WG_PORT="${WG_PORT//$'\r'/}"
    WG_PORT=${WG_PORT:-51820}
    [[ "$WG_PORT" =~ ^[0-9]+$ ]] && (( 10#$WG_PORT >= 1 && 10#$WG_PORT <= 65535 )) && break
    warn "The port must be a number between 1 and 65535."
  done

  # Linux caps interface names at 15 chars (IFNAMSIZ); slashes/spaces would also
  # break the wg-quick config path inside the container.
  while true; do
    read -rp "WireGuard interface name [wg0]: " WG_IFACE
    WG_IFACE="${WG_IFACE//$'\r'/}"
    WG_IFACE=${WG_IFACE:-wg0}
    [[ "$WG_IFACE" =~ ^[A-Za-z0-9_=+.-]{1,15}$ ]] && break
    warn "Interface names must be 1-15 characters (letters, digits, . _ = + -)."
  done

  while true; do
    read_required "This node's own WireGuard interface address, with prefix (the .1 of the subnet you set in the panel, e.g. 10.66.0.1/24): " WG_IFACE_ADDR
    valid_cidr4 "$WG_IFACE_ADDR" && break
    warn "Expected a valid IPv4 address with a prefix length, e.g. 10.66.0.1/24."
  done

  cat > "$ENV_FILE" <<EOF
NODE_IMAGE=${NODE_IMAGE}
WGPANEL_PANEL_ADDR=${PANEL_ADDR}
WGPANEL_JOIN_TOKEN=${JOIN_TOKEN}
WGPANEL_NODE_NAME=${NODE_NAME}
WGPANEL_WG_INTERFACE=${WG_IFACE}
WGPANEL_WG_PORT=${WG_PORT}
WG_IFACE_ADDR=${WG_IFACE_ADDR}
EOF
  chmod 600 "$ENV_FILE"
}

write_compose() {
  # Bridge networking (not host): the container terminates WireGuard and NATs client
  # traffic to the internet inside its OWN network namespace, so it never has to touch
  # the host's Docker-managed FORWARD/DOCKER-USER chains - the exact model the in-repo
  # dev node is verified against. Only the WireGuard UDP port is published to the host.
  cat > "$COMPOSE_FILE" <<'EOF'
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
    ports:
      - "${WGPANEL_WG_PORT:-51820}:${WGPANEL_WG_PORT:-51820}/udp"
    volumes:
      - node_wg_state:/etc/wireguard
      - node_agent_state:/etc/wgpanel/state
    deploy:
      resources:
        limits:
          memory: ${NODE_MEM_LIMIT:-256m}

volumes:
  node_wg_state:
  node_agent_state:
EOF
}

# Pull the published node image; if that fails (private package, or CI hasn't
# published it yet), build it from source so the install still completes.
ensure_image() {
  if [[ -n "${WGPANEL_NODE_BUILD:-}" ]]; then
    build_image_from_source
    return
  fi
  log "Pulling node image ${NODE_IMAGE} ..."
  if (cd "$NODE_DIR" && docker compose pull); then
    return
  fi
  warn "Could not pull ${NODE_IMAGE} - falling back to building the image from source."
  build_image_from_source
}

build_image_from_source() {
  local src="/tmp/wgpanel-src.$$"
  log "Cloning ${WGPANEL_REPO_URL} and building the node image locally..."
  rm -rf "$src"
  git clone --depth 1 "$WGPANEL_REPO_URL" "$src"
  docker build -f "$src/deploy/node.Dockerfile" -t wgpanel-node:local "$src"
  rm -rf "$src"
  # Repoint the compose at the locally-built image.
  sed -i 's#^NODE_IMAGE=.*#NODE_IMAGE=wgpanel-node:local#' "$ENV_FILE"
  NODE_IMAGE="wgpanel-node:local"
}

setup_firewall() {
  log "Configuring firewall (ufw)..."
  # Never lock out SSH: the OpenSSH app profile only exists when openssh-server is
  # installed from apt, and sshd may listen on a custom port - "ufw allow OpenSSH"
  # silently failing followed by "ufw enable" (default deny incoming) would cut this
  # session off. Allow the ports sshd is actually listening on as well.
  ufw allow OpenSSH >/dev/null 2>&1 || true
  local ssh_port
  while read -r ssh_port; do
    [[ -n "$ssh_port" ]] && ufw allow "${ssh_port}/tcp" >/dev/null 2>&1 || true
  done < <(ss -tlnpH 2>/dev/null | awk '/sshd/ {n = split($4, a, ":"); print a[n]}' | sort -u)
  # A broken ufw/iptables ("ERROR: problem running iptables/ufw-init" - classically a
  # kernel upgraded without a reboot, leaving the running kernel unable to load
  # iptables modules) must not abort the install this late. The node works without
  # the rule; the port just has to be opened once ufw is healthy again.
  if ! ufw allow "${WG_PORT}/udp"; then
    warn "ufw could not add the ${WG_PORT}/udp rule (see the error above) - continuing anyway."
    warn "Clients can't connect until UDP ${WG_PORT} is open. If the error mentions iptables,"
    warn "a reboot usually fixes it (pending kernel upgrade); then run: ufw allow ${WG_PORT}/udp"
  fi
  # ufw's default FORWARD policy is DROP, which also drops the traffic Docker forwards
  # for the node container's clients (container bridge -> host egress). Set it to ACCEPT
  # so Docker's own specific per-bridge FORWARD rules govern forwarding - without this,
  # clients handshake but get no internet. (The node's client-subnet -> egress
  # MASQUERADE itself lives inside the container; see node-entrypoint.sh.)
  if [[ -f /etc/default/ufw ]]; then
    sed -i 's/^DEFAULT_FORWARD_POLICY=.*/DEFAULT_FORWARD_POLICY="ACCEPT"/' /etc/default/ufw
  fi
  yes | ufw enable >/dev/null 2>&1 || true
  ufw reload >/dev/null 2>&1 || true
}

start_node() {
  log "Starting the node container..."
  # --force-recreate: on a re-install with an unchanged .env, `up -d` would leave the
  # old container (and its old logs) running untouched - verify() would then read
  # stale failure lines from a previous run instead of this one.
  (cd "$NODE_DIR" && docker compose up -d --force-recreate)
}

# The container being "running" says nothing about registration: a bad join token or
# wrong panel address makes the agent crash-loop while restart: unless-stopped keeps
# the container alive - the old 3-second check reported success anyway. Watch the
# agent's own log markers instead ("registered" / "fatal", cmd/agent/main.go).
verify() {
  log "Waiting for the node to register with the control plane..."
  local logs deadline=$((SECONDS + 45))
  while (( SECONDS < deadline )); do
    sleep 3
    logs="$(cd "$NODE_DIR" && docker compose logs --no-color 2>/dev/null || true)"
    if grep -q '"msg":"registered"' <<<"$logs"; then
      log "Node registered with the control plane. Follow heartbeats with:"
      log "    cd ${NODE_DIR} && docker compose logs -f"
      return
    fi
    if grep -q '"msg":"fatal"' <<<"$logs"; then
      err "The node agent failed to start - last log lines:"
      (cd "$NODE_DIR" && docker compose logs --no-color --tail 5) >&2 || true
      err "The usual causes are a wrong/expired join token or a wrong control-plane address."
      err "Fix .env in ${NODE_DIR}, then run: cd ${NODE_DIR} && docker compose up -d --force-recreate"
      exit 1
    fi
  done
  if (cd "$NODE_DIR" && docker compose ps --status running --format '{{.Name}}' | grep -q .); then
    warn "Node container is running but registration is not confirmed yet. Follow it with:"
    warn "    cd ${NODE_DIR} && docker compose logs -f"
  else
    err "Node container failed to start. Check logs with: cd ${NODE_DIR} && docker compose logs"
    exit 1
  fi
}

main() {
  require_root
  detect_os
  install_prereqs
  install_docker
  prompt_config
  write_compose
  ensure_image
  setup_firewall
  start_node
  verify
  log "Done. This node should appear as 'online' in the admin panel's Nodes list shortly, with its WireGuard public key already set."
  log "Manage it with: cd ${NODE_DIR} && docker compose {ps|logs|restart|pull|up -d|down}"
}

main "$@"

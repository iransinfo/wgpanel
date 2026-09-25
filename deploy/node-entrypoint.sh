#!/usr/bin/env bash
#
# Containerized equivalent of install-node.sh's setup_wireguard() - generates a
# wg0.conf on first boot (persisted via the wg_node_state volume so keys survive
# container recreation), brings the interface up, then execs the agent. No
# DOCKER-USER chain handling here (unlike install-node.sh): that chain only exists
# on a host that also runs the Docker daemon itself, not inside this container's
# own network namespace.
set -euo pipefail

WG_IFACE="${WGPANEL_WG_INTERFACE:-wg0}"
WG_PORT="${WGPANEL_WG_PORT:-51820}"
WG_CONF="/etc/wireguard/${WG_IFACE}.conf"
WG_IFACE_ADDR="${WG_IFACE_ADDR:-10.66.0.1/24}"

mkdir -p /etc/wireguard
chmod 700 /etc/wireguard

sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || \
  echo "node-entrypoint: could not set ip_forward in-container - make sure the 'sysctls: [net.ipv4.ip_forward=1]' compose setting is applied" >&2

if [[ ! -f "$WG_CONF" ]]; then
  umask 077
  wg genkey > "/etc/wireguard/${WG_IFACE}-private.key"
  wg pubkey < "/etc/wireguard/${WG_IFACE}-private.key" > "/etc/wireguard/${WG_IFACE}-public.key"

  egress_iface="$(ip route show default | awk '{print $5; exit}')"
  egress_iface="${egress_iface:-eth0}"

  cat > "$WG_CONF" <<EOF
[Interface]
PrivateKey = $(cat "/etc/wireguard/${WG_IFACE}-private.key")
Address = ${WG_IFACE_ADDR}
ListenPort = ${WG_PORT}
PostUp = iptables -A FORWARD -i %i -j ACCEPT; iptables -t nat -A POSTROUTING -o ${egress_iface} -j MASQUERADE
PostDown = iptables -D FORWARD -i %i -j ACCEPT; iptables -t nat -D POSTROUTING -o ${egress_iface} -j MASQUERADE
EOF
  chmod 600 "$WG_CONF"
else
  # The conf outlives the container (state volume), so env changes would otherwise
  # never apply: a re-install with a new WGPANEL_WG_PORT publishes the new port on
  # the host while the interface keeps listening on the old one. Sync the mutable
  # fields on every boot; the generated keys are left untouched. '#' as the sed
  # delimiter because the address value contains '/'.
  sed -i \
    -e "s#^Address = .*#Address = ${WG_IFACE_ADDR}#" \
    -e "s#^ListenPort = .*#ListenPort = ${WG_PORT}#" \
    "$WG_CONF"
fi

# Idempotent across container restarts and crashes: if a stale interface or leftover
# routes survived (e.g. an unclean stop), tear it down first so `up` starts clean
# instead of failing with "Device or resource busy".
wg-quick down "$WG_IFACE" 2>/dev/null || true
wg-quick up "$WG_IFACE"

export WGPANEL_WG_PUBLIC_KEY="$(cat "/etc/wireguard/${WG_IFACE}-public.key")"

exec /usr/local/bin/wgpanel-agent

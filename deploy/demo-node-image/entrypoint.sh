#!/bin/sh
# Real-node entrypoint for local demo/testing (docs/STORY-10 follow-up) - mirrors
# deploy/install-node.sh's setup_wireguard() but containerized: generates a
# WireGuard keypair once (persisted in the /etc/wireguard volume so it's stable
# across container restarts, matching install-node.sh's "leave existing config
# untouched" behavior), brings up a real wg0 kernel interface via wg-quick, then
# execs the compiled agent so it submits WGPANEL_WG_PUBLIC_KEY at registration.
set -e

WG_CONF=/etc/wireguard/wg0.conf

if [ ! -f "$WG_CONF" ]; then
  umask 077
  wg genkey > /etc/wireguard/wg0-private.key
  wg pubkey < /etc/wireguard/wg0-private.key > /etc/wireguard/wg0-public.key
  cat > "$WG_CONF" <<CONF
[Interface]
PrivateKey = $(cat /etc/wireguard/wg0-private.key)
Address = ${WG_IFACE_ADDR}
ListenPort = ${WG_PORT}
PostUp = iptables -A FORWARD -i %i -j ACCEPT; iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE
PostDown = iptables -D FORWARD -i %i -j ACCEPT; iptables -t nat -D POSTROUTING -o eth0 -j MASQUERADE
CONF
fi

# Same real gap found and fixed in deploy/install-node.sh: AllowedIPs = 0.0.0.0/0 in
# generated client configs is a no-op for actual internet access without this. Docker
# masks /proc/sys/net/ipv4/ip_forward read-only by default even with NET_ADMIN - it
# must be set via `docker run --sysctl net.ipv4.ip_forward=1` instead, which is what
# actually enables this; the write here is just a fallback for --privileged runs.
echo 1 > /proc/sys/net/ipv4/ip_forward 2>/dev/null || true

wg-quick up wg0
export WGPANEL_WG_PUBLIC_KEY="$(cat /etc/wireguard/wg0-public.key)"

exec /wgpanel-agent

# Local demo/testing "real node" image - NOT part of the production install path
# (that's deploy/install-node.sh on a bare-metal/VM host). This exists only to give
# a locally-run demo panel one actual, connectable WireGuard peer inside Docker
# Desktop's Linux VM (confirmed to support the kernel wireguard module).
FROM alpine:3.20
RUN apk add --no-cache wireguard-tools iproute2 iptables
COPY --from=wgpanel-api:local /wgpanel-agent /wgpanel-agent
COPY entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]

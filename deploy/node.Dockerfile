FROM golang:1.25-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/. .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wgpanel-agent ./cmd/agent

# Debian (not distroless, unlike backend/Dockerfile's api image) - this container
# needs a real userland: wg-quick is a bash script, plus iptables/iproute2 for the
# NAT/forwarding setup install-node.sh normally does on a bare Linux host.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      wireguard-tools iptables iproute2 ca-certificates && \
    rm -rf /var/lib/apt/lists/*
COPY --from=build /out/wgpanel-agent /usr/local/bin/wgpanel-agent
COPY deploy/node-entrypoint.sh /usr/local/bin/node-entrypoint.sh
RUN chmod +x /usr/local/bin/node-entrypoint.sh
ENTRYPOINT ["/usr/local/bin/node-entrypoint.sh"]

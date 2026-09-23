FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wgpanel-api ./cmd/api
# Also build the node agent binary into this same image (docs/STORY-09-multi-node-
# accounts.md) - install.sh extracts it via `docker create`+`docker cp` to
# self-register the panel's own server as a node, without depending on
# install-node.sh's separate (currently unpublished) release-download URL. It never
# runs inside this container - only ever copied out onto the bare-metal host.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wgpanel-agent ./cmd/agent
# distroless:nonroot has no shell to chown a volume mountpoint in, and a Docker named
# volume inherits ownership from whatever's baked into the image at that path on
# first creation - so the /data dir has to arrive already owned by the nonroot user
# (65532:65532, distroless's fixed UID/GID) rather than being created/chowned later.
RUN mkdir -p /out/data && chown 65532:65532 /out/data

# Runtime base pinned to a digest (resolved from :nonroot) so the shipped image is
# reproducible and can't drift under an unattended CI rebuild - bump deliberately.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:aef9602f8710ec12bde19d593fed1f76c708531bb7aba205110f1029786ead7b
COPY --from=build /out/wgpanel-api /wgpanel-api
COPY --from=build /out/wgpanel-agent /wgpanel-agent
COPY --from=build --chown=65532:65532 /out/data /data
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/wgpanel-api"]

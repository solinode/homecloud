# The HomeCloud server image. It runs `homecloud serve` and manages the Docker
# host whose socket is mounted into it:
#
#   docker run -d --name homecloud -p 127.0.0.1:8080:8080 \
#     -v /var/run/docker.sock:/var/run/docker.sock -v homecloud-data:/data \
#     ghcr.io/solinode/homecloud
#
# Build: docker buildx build --platform linux/amd64,linux/arm64 -t homecloud .
# Base images are pinned by digest; bump them together with their tags.

# ---- web console (platform independent: built once, on the build machine) ----
FROM --platform=$BUILDPLATFORM node:22-alpine@sha256:0a7108bf6c7bf5de370ffb1a3ed6be93d405b43ff159f681a8d18c0e2bc2e402 AS console
WORKDIR /src/console
ENV NEXT_TELEMETRY_DISABLED=1
COPY console/package.json console/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY console/ ./
RUN npm run build

# ---- the homecloud binary, cross-compiled for the target platform ----
FROM --platform=$BUILDPLATFORM golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59 AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src/cli
COPY cli/go.mod cli/go.sum ./
RUN go mod download
COPY cli/ ./
COPY --from=console /src/console/out/ ./internal/web/dist/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/homecloudhq/homecloud/cli/cmd.Version=${VERSION} -X github.com/homecloudhq/homecloud/cli/cmd.Commit=${COMMIT}" \
      -o /out/homecloud .

# ---- runtime ----
FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8
ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.title="HomeCloud" \
      org.opencontainers.image.description="A self-hosted AWS: S3, EC2, Lambda, DynamoDB, SQS and more on your own Docker host" \
      org.opencontainers.image.source="https://github.com/solinode/homecloud" \
      org.opencontainers.image.url="https://github.com/solinode/homecloud" \
      org.opencontainers.image.documentation="https://github.com/solinode/homecloud/blob/main/docs/install-server.md" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"
# su-exec drops root once the entrypoint has granted the server user access
# to the Docker socket.
RUN apk add --no-cache ca-certificates su-exec tzdata \
 && addgroup -S -g 10001 homecloud \
 && adduser -S -D -H -u 10001 -G homecloud -h /data homecloud \
 && mkdir -p /data && chown homecloud:homecloud /data && chmod 700 /data
COPY --from=build /out/homecloud /usr/local/bin/homecloud
COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
# HOMECLOUD_ADDR: listen on every address of the container (publish the port
# to choose who reaches it). HOME=/data lets `docker exec ... homecloud` find
# the credentials.
ENV HOMECLOUD_DATA_DIR=/data \
    HOMECLOUD_ADDR=0.0.0.0:8080 \
    HOMECLOUD_IN_CONTAINER=1 \
    HOME=/data
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/api/v1/health || wget -q --no-check-certificate -O /dev/null https://127.0.0.1:8080/api/v1/health || exit 1
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["serve"]

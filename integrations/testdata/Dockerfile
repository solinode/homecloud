# Test-only HomeCloud image for the testcontainers modules, used until the
# official ghcr.io/solinode/homecloud image is published. Build context: cli/
#
#   docker build -f integrations/testdata/Dockerfile -t homecloud:test cli
#
# The image's entrypoint is the homecloud binary, so `docker run IMAGE serve ...`
# and `docker exec CONTAINER homecloud aws-env` both work. HomeCloud drives the
# host's Docker through the mounted socket and must run with host networking
# (see docs/integrations.md).
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/homecloudhq/homecloud/cli/cmd.Version=test" -o /out/homecloud .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/homecloud /usr/local/bin/homecloud
ENV HOMECLOUD_DATA_DIR=/data
VOLUME /data
ENTRYPOINT ["homecloud"]
CMD ["serve", "--data-dir", "/data", "--addr", "0.0.0.0:8080"]

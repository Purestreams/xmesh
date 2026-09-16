FROM ghcr.io/xtls/xray-core:26.3.27 AS xray

FROM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/xmesh ./cmd/xmesh
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/deploycheck ./tests/reality/deploycheck

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /etc/xmesh /var/lib/xmesh
COPY --from=xray /usr/local/bin/xray /usr/local/lib/xmesh/xray
COPY --from=build /out/xmesh /usr/local/bin/xmesh
COPY --from=build /out/deploycheck /usr/local/bin/deploycheck
ENTRYPOINT ["/usr/local/bin/xmesh"]

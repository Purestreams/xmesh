FROM ghcr.io/xtls/xray-core:26.9.9 AS xray

FROM golang:1.27.2-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/xmesh ./cmd/xmesh
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/deploycheck ./tests/reality/deploycheck
RUN curl --fail --location --retry 3 https://github.com/HyNetworks/hysteria/releases/download/app/v2.13.0/hysteria-linux-amd64 -o /out/hysteria \
    && echo '907ba8c9693edb104b20582681fb7dc15639d5b64a9cbb616a7b539190a86691  /out/hysteria' | sha256sum -c - \
    && chmod 755 /out/hysteria

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /etc/xmesh /var/lib/xmesh
COPY --from=xray /usr/local/bin/xray /usr/local/lib/xmesh/xray
COPY --from=build /out/xmesh /usr/local/bin/xmesh
COPY --from=build /out/deploycheck /usr/local/bin/deploycheck
COPY --from=build /out/hysteria /usr/local/bin/hysteria
ENTRYPOINT ["/usr/local/bin/xmesh"]

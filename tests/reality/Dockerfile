FROM ghcr.io/xtls/xray-core:26.9.9 AS xray
FROM golang:1.27.2-bookworm
COPY --from=xray /usr/local/bin/xray /usr/local/bin/xray
RUN curl --fail --location --retry 3 https://github.com/HyNetworks/hysteria/releases/download/app/v2.13.0/hysteria-linux-amd64 -o /usr/local/bin/hysteria \
    && echo '907ba8c9693edb104b20582681fb7dc15639d5b64a9cbb616a7b539190a86691  /usr/local/bin/hysteria' | sha256sum -c - \
    && chmod 755 /usr/local/bin/hysteria
WORKDIR /src
ENV XMESH_TEST_XRAY=/usr/local/bin/xray
ENV XMESH_TEST_HYSTERIA=/usr/local/bin/hysteria
CMD ["go", "test", "./internal/gateway", "-v", "-count=1", "-timeout=180s"]

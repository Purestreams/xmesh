FROM ghcr.io/xtls/xray-core:26.3.27 AS xray
FROM golang:1.27.1-bookworm
COPY --from=xray /usr/local/bin/xray /usr/local/bin/xray
WORKDIR /src
ENV XMESH_TEST_XRAY=/usr/local/bin/xray
CMD ["go", "test", "./internal/gateway", "-run", "TestRealityTunnelTCPAndUDP", "-v", "-count=1", "-timeout=60s"]

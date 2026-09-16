# xmesh

xmesh is a small reverse-proxy control plane and data plane made of three roles:

- `controller`: single-admin HTTP panel, desired state, subscriptions, enrollment, and status.
- `gateway`: local authenticated SOCKS5 handoff plus inbound Agent tunnels.
- `agent`: outbound tunnel client and the actual TCP/UDP network exit.

The public VMess/WebSocket listener remains an independent Xray process. Every user-facing
VMess node on a Gateway shares the same cleartext WS listener (default `:8080`). Xray hands
authenticated traffic to the Gateway's loopback-only SOCKS5 listener. Agent tunnels use
WebSocket Secure plus smux protocol version 2.

## Development

The repository pins Go 1.27.1 with mise:

```sh
mise install
mise exec -- go test ./...
mise exec -- go build ./cmd/xmesh
```

Never commit generated node credentials, enrollment scripts, runtime state, TLS private keys,
or local configuration. See `.gitignore`.

See [deployment](docs/deployment.md) and [architecture](docs/architecture.md) for the operational
model and trust boundaries.

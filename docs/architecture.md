# Architecture

```text
VMess client -- cleartext WS :8080 --> Xray
                                      |
                                      | authenticated loopback SOCKS5
                                      v
Controller HTTP <--- config/status --- Gateway <--- WSS + smux v2 --- Agent ---> target
   (no business traffic)               TCP/UDP                         system DNS/routes
```

The stable authorization model is `Node = Gateway × Agent` and `Grant = User × Node`. Each Grant
has a stable VMess UUID and separate loopback SOCKS credential. A Link is an internal path for one
Gateway–Agent attachment; adding direct/CDN Links never creates extra subscription nodes.

For a new TCP connection or UDP association, the Gateway chooses the lowest-numbered healthy
priority group and minimizes `(active streams + 1) / weight` within that group. The resulting
business session remains pinned to the chosen smux session. Existing sessions are not migrated
when policy or health changes.

Control messages are size-bounded, length-prefixed JSON. TCP payload is streamed without JSON or
Base64 wrapping. UDP uses a length-bounded frame that preserves one datagram and its source/target
address. The tunnel accepts complete UDP payloads up to the IPv4 UDP limit; larger frames are
rejected instead of truncated. Every SOCKS UDP association has a distinct loopback socket tied to
its authenticated TCP control connection and one pinned smux stream. The Agent keeps one UDP
socket for that association so multiple targets and their reply source addresses remain distinct.

Both tunnel peers use smux v2 with 32 KiB frames, an 8 MiB per-stream receive window, and a
32 MiB shared receive budget per WSS session. The half-window exceeds the approximately 2.5 MB
bandwidth-delay product of a 50 Mbps flow at 400 ms RTT. Keepalive runs every 10 seconds with a
60-second timeout; these values apply to new sessions and do not change TCP or TLS verification.

The Controller persists desired state with atomic replacement. Node credentials, enrollment
tokens, subscription tokens, VMess UUIDs, and tunnel credentials have separate purposes. A Grant
is not published in a subscription until its Gateway reports that the matching desired revision
and Xray configuration are both applied.

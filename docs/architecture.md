# Architecture

```text
VMess client -- cleartext WS :8080 --> Xray
                                      |
                                      | authenticated loopback SOCKS5
                                      v
Controller HTTP <--- config/status --- Gateway <--- WSS + smux v1 --- Agent ---> target
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
address. The tunnel accepts payloads up to 4 KiB; larger SOCKS datagrams are rejected and logged
instead of being truncated. Every SOCKS UDP association has a distinct loopback socket tied to its
authenticated TCP control connection. Each accepted datagram uses a separate smux substream so a
large or stalled datagram cannot block later traffic in the association.

The Controller persists desired state with atomic replacement. Node credentials, enrollment
tokens, subscription tokens, VMess UUIDs, and tunnel credentials have separate purposes. A Grant
is not published in a subscription until its Gateway reports that the matching desired revision
and Xray configuration are both applied.

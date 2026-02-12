# depot / depotd

A minimal Linux-only remote interactive console with selectable transport.

- `depotd`: server daemon
- `depot`: client

## Notes

- `-proto tcp` uses raw TCP with no encryption and no secure handshake.
- `-proto httpws` uses HTTP + WebSocket framing (also plaintext unless you run behind TLS/WSS).
- Simple password authentication is required.
- Single active client connection at a time.
- Shell is `/bin/bash` on the server.

## Build

```bash
go build ./cmd/depot
go build ./cmd/depotd
```

## Run (raw TCP)

Start server:

```bash
./depotd -proto tcp -listen 0.0.0.0:2222 -password yourpass
```

Connect client:

```bash
./depot -proto tcp -addr 127.0.0.1:2222 -password yourpass
```

## Run (HTTP + WebSocket)

Start server:

```bash
./depotd -proto httpws -listen 0.0.0.0:8080 -ws-path /ws -password yourpass
```

Connect client:

```bash
./depot -proto httpws -addr 127.0.0.1:8080 -ws-path /ws -password yourpass
```

## Behavior

- If password is wrong, connection is rejected.
- If a client is already connected, new connections are rejected.
- After successful auth, the connection is bridged to a bash PTY.

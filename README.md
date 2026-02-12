# depot / depotd

A minimal Linux-only remote interactive console over raw TCP.

- `depotd`: server daemon
- `depot`: client

## Notes

- No encryption and no secure handshake are implemented.
- Simple password authentication is required.
- Single active client connection at a time.
- Shell is `/bin/bash` on the server.

## Build

```bash
go build ./cmd/depot
go build ./cmd/depotd
```

## Run

Start server:

```bash
./depotd -listen 0.0.0.0:2222 -password yourpass
```

Connect client:

```bash
./depot -addr 127.0.0.1:2222 -password yourpass
```

## Behavior

- If password is wrong, connection is rejected.
- If a client is already connected, new connections are rejected.
- After successful auth, the connection is bridged to a bash PTY.

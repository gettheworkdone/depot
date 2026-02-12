# depot / depotd

A minimal Linux-only remote interactive console with selectable transport.

- `depotd`: server daemon
- `depot`: client

## Notes

- `-proto tcp` uses raw TCP with no encryption and no secure handshake.
- `-proto httpws` uses HTTP + WebSocket framing (plaintext unless TLS is terminated externally).
- `-proto httpswss` uses HTTPS + secure WebSocket (WSS) with in-process TLS cert/key.
- Simple password authentication is required.
- Single active client connection at a time.
- Shell is `/bin/bash` on the server.

## Build (Linux)

```bash
go build ./cmd/depot
go build ./cmd/depotd
```

## Build for macOS binaries (cross-compile from Linux)

```bash
GOOS=darwin GOARCH=amd64 go build -o depot-darwin-amd64 ./cmd/depot
GOOS=darwin GOARCH=amd64 go build -o depotd-darwin-amd64 ./cmd/depotd

GOOS=darwin GOARCH=arm64 go build -o depot-darwin-arm64 ./cmd/depot
GOOS=darwin GOARCH=arm64 go build -o depotd-darwin-arm64 ./cmd/depotd
```

> Note: this project is implemented/tested for Linux shell behavior (`/bin/bash` + PTY). The commands above show how to compile artifacts for macOS.

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

## Run (HTTPS + WSS)

### Generate a self-signed TLS certificate

```bash
openssl req -x509 -newkey rsa:2048 -sha256 -days 365 -nodes \
  -keyout server.key -out server.crt \
  -subj "/CN=localhost"
```

### Start secure server

```bash
./depotd -proto httpswss -listen 0.0.0.0:8443 -ws-path /ws \
  -tls-cert server.crt -tls-key server.key -password yourpass
```

### Connect secure client

For self-signed certs in testing:

```bash
./depot -proto httpswss -addr 127.0.0.1:8443 -ws-path /ws \
  -insecure-tls -password yourpass
```

For trusted certificates, omit `-insecure-tls`.

## Behavior

- If password is wrong, connection is rejected.
- If a client is already connected, new connections are rejected.
- After successful auth, the connection is bridged to a bash PTY.

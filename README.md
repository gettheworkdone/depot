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

### Generate a self-signed TLS certificate with VPS IP in SAN

Set your public VPS IP first:

```bash
VPS_IP=203.0.113.10
```

Create an OpenSSL config to include the IP as SAN:

```bash
cat > san.cnf <<EOF2
[req]
default_bits = 2048
prompt = no
default_md = sha256
distinguished_name = dn
x509_extensions = v3_req

[dn]
CN = ${VPS_IP}

[v3_req]
subjectAltName = @alt_names

[alt_names]
IP.1 = ${VPS_IP}
EOF2
```

Generate cert + key:

```bash
openssl req -x509 -nodes -days 365 -newkey rsa:2048 \
  -keyout server.key -out server.crt -config san.cnf
```

### Start secure server

```bash
./depotd -proto httpswss -listen 0.0.0.0:443 -ws-path /ws \
  -tls-cert server.crt -tls-key server.key -password yourpass
```

### Connect secure client

```bash
./depot -proto httpswss -addr ${VPS_IP}:443 -ws-path /ws -password yourpass
```

If your cert is self-signed, add `server.crt` to your client trust store (for example, in macOS Keychain as a trusted certificate) before connecting.

On macOS, you can trust it from terminal with:

```bash
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ./server.crt
```

## Run `depotd` permanently on VPS with auto-restart (systemd)

1. Copy `depotd`, `server.crt`, and `server.key` to the VPS.
2. Put files in place under `/root/depot`:

```bash
sudo mkdir -p /root/depot
sudo cp ./depotd /root/depot/depotd
sudo cp ./server.crt /root/depot/server.crt
sudo cp ./server.key /root/depot/server.key
sudo chmod 700 /root/depot
sudo chmod 600 /root/depot/server.key
```

3. Create `/etc/systemd/system/depotd.service`:

```ini
[Unit]
Description=depotd remote console daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Group=root
WorkingDirectory=/root/depot
ExecStart=/root/depot/depotd -proto httpswss -listen 0.0.0.0:443 -ws-path /ws -tls-cert /root/depot/server.crt -tls-key /root/depot/server.key -password yourpass
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
```

4. Enable + start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now depotd
```

5. Verify:

```bash
sudo systemctl status depotd
sudo journalctl -u depotd -f
```

Now `depotd` will start automatically after VPS reboot and restart on failures.

## Behavior

- If password is wrong, connection is rejected.
- If a client is already connected, new connections are rejected.
- After successful auth, the connection is bridged to a bash PTY.

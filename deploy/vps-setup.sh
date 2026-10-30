#!/usr/bin/env bash
# rea-relay VPS bring-up (Debian/Ubuntu). Run as root (or with sudo) on the VPS.
#
# Prerequisites:
#   - DNS A record  relay.mt5tradebot.com -> this VPS public IP (already done).
#   - Inbound TCP 80 AND 443 open in the VPS firewall / security group.
#   - The relay binary copied to this host first, e.g. from your PC:
#       scp rea-work\dist\relay-linux-amd64  root@198.44.116.178:/opt/rea-relay/relay
#
# Then run this script on the VPS:
#       ssh root@198.44.116.178 'mkdir -p /opt/rea-relay'      # if needed
#       scp deploy/vps-setup.sh root@198.44.116.178:/root/
#       ssh root@198.44.116.178 'bash /root/vps-setup.sh'
#
# Idempotent: safe to re-run after replacing the binary.
set -euo pipefail

RELAY_HOST="${RELAY_HOST:-relay.mt5tradebot.com}"
APP_DIR=/opt/rea-relay
BIN="$APP_DIR/relay"

mkdir -p "$APP_DIR"

# 1. Install Caddy from its official repo (skips if already present).
if ! command -v caddy >/dev/null 2>&1; then
  echo "[*] installing Caddy ..."
  apt-get update -y
  apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl gnupg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
    | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
    > /etc/apt/sources.list.d/caddy-stable.list
  apt-get update -y
  apt-get install -y caddy
fi

# 2. Require the relay binary to be present.
if [ ! -s "$BIN" ]; then
  echo "ERROR: relay binary not found at $BIN" >&2
  echo "       scp rea-work\\dist\\relay-linux-amd64  root@<vps>:$BIN" >&2
  exit 1
fi
chmod +x "$BIN"

# 3. systemd service for the relay (binds loopback only; Caddy fronts it with TLS).
cat > /etc/systemd/system/rea-relay.service <<UNIT
[Unit]
Description=rea-relay ingress
After=network.target

[Service]
ExecStart=$BIN -addr 127.0.0.1:8080
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
UNIT

# 4. Caddyfile — automatic Let's Encrypt TLS for the relay host.
cat > /etc/caddy/Caddyfile <<CADDY
$RELAY_HOST {
    encode gzip
    reverse_proxy 127.0.0.1:8080 {
        flush_interval -1
    }
}
CADDY

# 5. Start / enable.
systemctl daemon-reload
systemctl enable --now rea-relay
systemctl reload caddy 2>/dev/null || systemctl restart caddy

sleep 2
echo "----- rea-relay -----"; systemctl --no-pager --lines=5 status rea-relay || true
echo "----- local health -----"; curl -sS http://127.0.0.1:8080/healthz && echo
echo
echo "[*] Done. From anywhere, verify TLS termination:"
echo "    curl https://$RELAY_HOST/healthz    # expect: ok"
echo "[*] If the TLS check fails, Caddy may still be fetching the cert — check: journalctl -u caddy -n 50"

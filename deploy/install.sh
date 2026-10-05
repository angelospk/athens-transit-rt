#!/usr/bin/env bash
# Installs or upgrades atrt on the VPS. Run from the repo root on a dev machine:
#   deploy/install.sh oracle-vm
set -euo pipefail
host=${1:-oracle-vm}
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tmp/atrt-linux ./cmd/atrt
scp -q /tmp/atrt-linux deploy/atrt.service "$host":/tmp/
ssh "$host" 'set -e
  id atrt >/dev/null 2>&1 || sudo useradd --system --home /var/lib/atrt --shell /usr/sbin/nologin atrt
  sudo install -m 0755 /tmp/atrt-linux /usr/local/bin/atrt
  sudo install -m 0644 /tmp/atrt.service /etc/systemd/system/atrt.service
  sudo systemctl daemon-reload
  sudo systemctl enable atrt >/dev/null
  sudo systemctl restart atrt
  rm -f /tmp/atrt-linux /tmp/atrt.service
  sleep 3; systemctl is-active atrt; curl -fsS 127.0.0.1:8095/healthz'

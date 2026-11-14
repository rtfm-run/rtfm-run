#!/bin/bash
# Prepares a fresh Ubuntu 24.04 VM (with nested virtualization) as an rtfm
# worker. Run as root from the directory holding the uploaded files:
#   rtfm, agy, deploy.env, rtfm-worker.service, cloud-sql-proxy.service,
#   rtfm-fetch-secrets, proxy.env
set -euo pipefail
cd "$(dirname "$0")"

test -e /dev/kvm || { echo "no /dev/kvm: create the VM with --enable-nested-virtualization" >&2; exit 1; }

export DEBIAN_FRONTEND=noninteractive
apt-get update -q
apt-get install -y -q docker.io docker-buildx git ca-certificates curl
systemctl enable --now docker

# Cloud SQL Auth Proxy (pinned).
curl -fsSL -o /usr/local/bin/cloud-sql-proxy \
  https://storage.googleapis.com/cloud-sql-connectors/cloud-sql-proxy/v2.14.3/cloud-sql-proxy.linux.amd64
chmod 0755 /usr/local/bin/cloud-sql-proxy

id rtfm >/dev/null 2>&1 || useradd --system --create-home --home-dir /var/lib/rtfm --shell /usr/sbin/nologin rtfm
usermod -aG docker,kvm rtfm
install -d -o rtfm -g rtfm /var/lib/rtfm/runs /var/lib/rtfm/work
install -d -m 0750 -o root -g rtfm /etc/rtfm

install -m 0755 rtfm agy rtfm-fetch-secrets /usr/local/bin/
install -m 0644 deploy.env proxy.env /etc/rtfm/
install -m 0644 rtfm-worker.service cloud-sql-proxy.service /etc/systemd/system/

# Devbox images: docker (fallback) and Firecracker microVMs.
sudo -u rtfm -H /usr/local/bin/rtfm devbox build --backend docker
sudo -u rtfm -H /usr/local/bin/rtfm devbox build --backend firecracker

/usr/local/bin/rtfm-fetch-secrets
systemctl daemon-reload
systemctl enable --now cloud-sql-proxy.service
systemctl enable --now rtfm-worker.service
systemctl --no-pager status rtfm-worker.service | head -5

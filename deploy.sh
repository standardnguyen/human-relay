#!/bin/bash
set -euo pipefail
cd /opt/human-relay
git fetch origin main
git reset --hard origin/main
docker compose build --no-cache
/usr/local/bin/bao-env services/human-relay -o /opt/human-relay/.env
/usr/local/bin/bao-env services/openproject >> /opt/human-relay/.env
/usr/local/bin/bao-env services/kronos >> /opt/human-relay/.env
chmod 600 /opt/human-relay/.env
docker compose up -d
docker image prune -f
install -m 0644 /opt/human-relay/cron/openproject-caldav-bridge /etc/cron.d/openproject-caldav-bridge

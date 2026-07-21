#!/bin/bash
set -euo pipefail
cd /opt/human-relay
git fetch origin main
git reset --hard origin/main
docker compose build --no-cache
/usr/local/bin/bao-env services/human-relay -o /opt/human-relay/.env
for svc in openproject speakr kronos bsky-signet baserow; do
  /usr/local/bin/bao-env "services/$svc" >> /opt/human-relay/.env
done
chmod 600 /opt/human-relay/.env
docker compose up -d
docker image prune -f
install -m 0644 /opt/human-relay/cron/openproject-caldav-bridge /etc/cron.d/openproject-caldav-bridge
install -m 0644 /opt/human-relay/cron/transcribe-sweep /etc/cron.d/transcribe-sweep

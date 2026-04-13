#!/bin/bash
set -euo pipefail
# cs2-status.sh — List running CS2 server instances

echo "Running CS2 servers:"
echo ""
docker ps --filter "name=cs2-" --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"

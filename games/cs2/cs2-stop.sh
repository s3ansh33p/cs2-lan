#!/bin/bash
set -euo pipefail

# cs2-stop.sh — Stop and remove a CS2 server instance
# Usage: ./cs2-stop.sh <name>
# Example: ./cs2-stop.sh dm

if [[ $# -lt 1 ]]; then
    echo "Usage: $0 <name>"
    echo "Running servers:"
    docker ps --filter "name=cs2-" --format "  {{.Names}}  ({{.Ports}})"
    exit 1
fi

NAME="$1"
echo "Stopping cs2-${NAME}..."
docker stop "cs2-${NAME}" && docker rm "cs2-${NAME}"
echo "Done."

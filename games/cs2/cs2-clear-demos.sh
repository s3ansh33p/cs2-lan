#!/bin/bash
set -euo pipefail

# cs2-clear-demos.sh — Delete all .dem files from the CS2 base volume
# Usage: ./cs2-clear-demos.sh [-y] [volume]
#   -y       Skip confirmation prompt
#   volume   Docker volume name (default: cs2-lan_cs2-base)

VOLUME="cs2-lan_cs2-base"
ASSUME_YES=0

for arg in "$@"; do
    case "$arg" in
        -y|--yes) ASSUME_YES=1 ;;
        -*)       echo "Unknown flag: $arg" >&2; exit 1 ;;
        *)        VOLUME="$arg" ;;
    esac
done

if ! docker volume inspect "$VOLUME" >/dev/null 2>&1; then
    echo "Error: volume '$VOLUME' does not exist." >&2
    exit 1
fi

echo "Scanning volume '$VOLUME' for .dem files..."
MATCHES=$(docker run --rm -v "${VOLUME}:/data" alpine \
    sh -c 'find /data -type f -name "*.dem" 2>/dev/null | sort')

if [[ -z "$MATCHES" ]]; then
    echo "No demo files found."
    exit 0
fi

COUNT=$(printf '%s\n' "$MATCHES" | wc -l)
echo "Found $COUNT demo file(s):"
printf '%s\n' "$MATCHES" | sed 's/^/  /'

if [[ $ASSUME_YES -ne 1 ]]; then
    read -r -p "Delete all $COUNT file(s)? [y/N] " reply
    case "$reply" in
        y|Y|yes|YES) ;;
        *) echo "Aborted."; exit 0 ;;
    esac
fi

docker run --rm -v "${VOLUME}:/data" alpine \
    sh -c 'find /data -type f -name "*.dem" -delete'

echo "Deleted $COUNT demo file(s) from '$VOLUME'."

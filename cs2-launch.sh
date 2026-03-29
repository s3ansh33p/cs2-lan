#!/bin/bash
set -euo pipefail

# cs2-launch.sh — Spin up a CS2 LAN server instance
#
# Usage: ./cs2-launch.sh <name> <port> [options]
#
# Examples:
#   ./cs2-launch.sh comp 27015
#   ./cs2-launch.sh comp 27015 --map de_dust2 --mode competitive --players 10
#   ./cs2-launch.sh dm 27016 --mode deathmatch --players 16
#   ./cs2-launch.sh casual 27017 --mode casual
#
# Players connect to: <your-lan-ip>:<port>

usage() {
    echo "Usage: $0 <name> <port> [options]"
    echo ""
    echo "Options:"
    echo "  --mode <alias>     Game mode alias: competitive, casual, deathmatch, etc."
    echo "  --map <map>        Starting map (default: de_inferno)"
    echo "  --players <n>      Max players (default: 10)"
    echo "  --password <pw>    Server password"
    echo "  --rcon <pw>        RCON password (default: changeme)"
    echo "  --tv               Enable CSTV"
    exit 1
}

if [[ $# -lt 2 ]]; then
    usage
fi

NAME="$1"
PORT="$2"
shift 2

# Defaults
MAP="de_inferno"
MODE="competitive"
PLAYERS=10
PASSWORD=""
RCON="changeme"
TV=0
EXTRA_ARGS=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        --mode)    MODE="$2";     shift 2 ;;
        --map)     MAP="$2";      shift 2 ;;
        --players) PLAYERS="$2";  shift 2 ;;
        --password) PASSWORD="$2"; shift 2 ;;
        --rcon)    RCON="$2";     shift 2 ;;
        --tv)      TV=1;          shift ;;
        *)         EXTRA_ARGS+=(-e "$1"); shift ;;
    esac
done

TV_PORT=$((PORT + 5))

echo "Starting CS2 LAN server: ${NAME}"
echo "  Connect: localhost:${PORT}"
echo "  Mode: ${MODE} | Map: ${MAP} | Players: ${PLAYERS}"

docker compose run -d \
    --name "cs2-${NAME}" \
    -e CS2_SERVERNAME="${NAME}" \
    -e CS2_PORT="${PORT}" \
    -e CS2_GAMEALIAS="${MODE}" \
    -e CS2_STARTMAP="${MAP}" \
    -e CS2_MAXPLAYERS="${PLAYERS}" \
    -e CS2_RCONPW="${RCON}" \
    -e CS2_PW="${PASSWORD}" \
    -e TV_ENABLE="${TV}" \
    -e TV_PORT="${TV_PORT}" \
    -e CS2_LOG=on \
    -e CS2_LOG_DETAIL=3 \
    -e CS2_LOG_ECHO=1 \
    "${EXTRA_ARGS[@]+"${EXTRA_ARGS[@]}"}" \
    cs2

echo ""
echo "Server cs2-${NAME} started on port ${PORT}"

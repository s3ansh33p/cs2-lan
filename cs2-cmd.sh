#!/bin/bash
set -euo pipefail

# cs2-cmd.sh — Send RCON commands to a running CS2 server
#
# Usage:
#   ./cs2-cmd.sh <name> <command>          Send a single command
#   ./cs2-cmd.sh <name>                    Open interactive RCON shell
#
# Examples:
#   ./cs2-cmd.sh comp mp_restartgame 1
#   ./cs2-cmd.sh dm changelevel de_dust2
#   ./cs2-cmd.sh comp                      (interactive mode)

if [[ $# -lt 1 ]]; then
    echo "Usage: $0 <name> [command...]"
    echo ""
    echo "Common commands:"
    echo "  mp_restartgame 1          Restart current match"
    echo "  changelevel <map>         Change map"
    echo "  maps *                    List available maps"
    echo "  status                    Show server status"
    echo "  kick <player>             Kick a player"
    echo "  bot_add                   Add a bot"
    echo "  bot_kick                  Kick all bots"
    echo "  quit                      Shut down server"
    echo ""
    echo "Running servers:"
    docker ps --filter "name=cs2-" --format "  {{.Names}}"
    exit 1
fi

NAME="$1"
shift

# Get the port and rcon password from the container's env
PORT=$(docker inspect "cs2-${NAME}" --format '{{range .Config.Env}}{{println .}}{{end}}' | grep ^CS2_PORT= | cut -d= -f2)
RCON_PW=$(docker inspect "cs2-${NAME}" --format '{{range .Config.Env}}{{println .}}{{end}}' | grep ^CS2_RCONPW= | cut -d= -f2)
PORT="${PORT:-27015}"
RCON_PW="${RCON_PW:-changeme}"

if [[ $# -eq 0 ]]; then
    # Interactive mode
    echo "RCON shell for cs2-${NAME} (localhost:${PORT})"
    echo "Type commands, Ctrl+C to exit"
    echo ""
    docker run -it --rm --network=host outdead/rcon ./rcon -a "localhost:${PORT}" -p "${RCON_PW}"
else
    # Single command
    docker run --rm --network=host outdead/rcon ./rcon -a "localhost:${PORT}" -p "${RCON_PW}" "$*"
fi

# CS2 LAN Servers

Run multiple CS2 dedicated servers from one machine, sharing a single copy of the game files. Built on [joedwards32/CS2](https://github.com/joedwards32/CS2).

## Setup

Download game files (once, ~35GB):

```bash
docker compose --profile update run --rm cs2-updater
```

## Launch servers

```bash
./cs2-launch.sh <name> <port> [options]
```

Options:

| Flag | Default | Description |
|------|---------|-------------|
| `--mode` | `competitive` | Game mode: `competitive`, `casual`, `deathmatch`, etc. |
| `--map` | `de_inferno` | Starting map |
| `--players` | `10` | Max players |
| `--password` | | Server password |
| `--rcon` | `changeme` | RCON password |
| `--tv` | off | Enable CSTV |

Examples:

```bash
./cs2-launch.sh comp   27015 --mode competitive --map de_dust2 --players 10
./cs2-launch.sh dm     27016 --mode deathmatch --players 16
./cs2-launch.sh casual 27017 --mode casual
```

Players connect to `localhost:<port>` or `<your-lan-ip>:<port>`.

## Server console

Attach to a server's interactive console using tmux:

```bash
tmux new -s comp 'docker attach cs2-comp'
```

- Type server commands directly and see all output
- `Ctrl+B` then `D` to detach (server keeps running)
- `tmux attach -t comp` to reattach later

### Useful server commands

| Command | Description |
|---------|-------------|
| `mp_restartgame 1` | Restart current match |
| `changelevel <map>` | Change map (e.g. `changelevel de_dust2`) |
| `maps *` | List available maps |
| `status` | Show server info and connected players |
| `kick <player>` | Kick a player |
| `bot_add` | Add a bot |
| `bot_kick` | Kick all bots |
| `mp_warmup_end` | End warmup |
| `mp_maxrounds 30` | Set max rounds |
| `quit` | Shut down server |

## Manage servers

```bash
./cs2-status.sh       # list running servers
./cs2-stop.sh <name>  # stop a server
```

## WSL2 network setup

If running Docker inside WSL2, enable mirrored networking so ports are accessible from Windows and LAN clients. Add to `C:\Users\<you>\.wslconfig`:

```ini
[wsl2]
networkingMode=mirrored
```

Then restart WSL: `wsl --shutdown` in PowerShell.

Open the firewall for LAN players (run as Administrator in PowerShell):

```powershell
.\cs2-firewall.ps1 enable                # allow game ports (27015-27030)
.\cs2-firewall.ps1 enable -WebPort 8080  # also open the web panel port
.\cs2-firewall.ps1 disable               # remove all rules when done
```

## Web Panel

An optional web interface for managing servers from any device on the LAN.

```bash
cd panel
make build
./cs2-panel --password <secret> --compose-file ../docker-compose.yml
```

Visit `http://<your-lan-ip>:8080` from any device on the network. Features:

- Dashboard with all running servers
- Launch and stop servers
- Live container logs (WebSocket)
- RCON console with autocomplete
- Real-time scoreboard, killfeed with weapon icons, and player equipment
- Team scores and round tracking
- Per-player money, weapons, grenades, armor, and bomb status

Options:

| Flag | Default | Description |
|------|---------|-------------|
| `--password` | (required) | Panel access password |
| `--port` | `8080` | HTTP listen port |
| `--compose-file` | `./docker-compose.yml` | Path to compose file |
| `--rcon-default` | `changeme` | Default RCON password for new servers |

If using WSL2, open the web port in the firewall: `.\cs2-firewall.ps1 enable -WebPort 8080`

## Update game files

Stop all running servers first, then update:

```bash
docker stop $(docker ps -q --filter "name=cs2-")
docker compose --profile update run --rm cs2-updater
```

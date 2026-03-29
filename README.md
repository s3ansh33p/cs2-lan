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

Players connect to `<your-lan-ip>:<port>` (e.g. `192.168.1.50:27015`).

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

## Update game files

Stop all running servers first, then update:

```bash
docker stop $(docker ps -q --filter "name=cs2-")
docker compose --profile update run --rm cs2-updater
```

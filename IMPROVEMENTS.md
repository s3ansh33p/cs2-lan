# Post-Event Improvements Checklist

Compiled after UniLAN 8 event (April 2026).

---

## Project Context

UniLAN Panel is a Go web application for running LAN events. It manages CS2 dedicated servers via Docker, and provides tournament brackets, event scheduling, live scoreboards, and a public-facing site — all over WebSockets for real-time updates.

### Tech Stack
- **Backend:** Go 1.25+, standard library HTTP server + `net/http` mux
- **Database:** SQLite (WAL mode, single writer), schema auto-migrates on startup
- **Frontend:** Server-rendered HTML templates (Go `html/template`), HTMX, vanilla JS, Tailwind CSS
- **Real-time:** WebSockets for live scoreboards, brackets, schedule, killfeed, logs
- **CS2 servers:** Docker containers via Docker Compose, host networking, shared `cs2-base` volume for game files
- **Server communication:** RCON (connection-pooled) for commands, Docker API log streaming for game event parsing
- **Build:** `make build` in `panel/` (compiles Tailwind CSS then Go binary)

### Key Directories & Files
```
panel/
  cmd/panel/main.go          — entrypoint, CLI flags
  internal/
    web/
      routes.go              — all HTTP routes (public + admin)
      handler.go             — Handler struct, shared state
      admin_tournament.go    — tournament admin endpoints
      bracket.go             — public bracket views
      match.go               — match/game management
      ws.go                  — WebSocket handlers
      schedule.go            — schedule endpoints
    db/
      db.go                  — SQLite setup, schema, migrations
      tournament.go          — Tournament model + queries
      bracket.go             — Match model, GenerateBracket() (single-elim only currently)
      games.go               — Game model (individual maps within a match), GameRound, PlayerStat
      teams.go               — Team + TeamMember models
      schedule.go            — Schedule items
    docker/
      docker.go              — Docker client, container listing, logs
      launch.go              — LaunchRequest struct, container creation via docker compose
    gametracker/
      tracker.go             — Real-time CS2 log parsing: kills, rounds, player stats, score tracking
    rcon/
      rcon.go                — RCON client (connection pool, command execution)
    auth/                    — Simple password auth with cookie sessions
  web/
    templates/               — HTML templates (layout.html, dashboard.html, bracket.html, etc.)
      partials/              — HTMX partials (scoreboard, killfeed, etc.)
    static/                  — JS files (bracket.js, admin.js, app.js, schedule*.js), CSS, HTMX
    embed.go                 — go:embed for static assets
  Makefile                   — `make build` = tailwind compile + go build

lan-default.cfg              — CS2 server config loaded by all instances (overtime, CSTV, autorecord)
docker-compose.yml           — CS2 server service definition + updater profile
```

### Current Database Schema (key tables)
- **tournament** — id, name, team_size, game_mode (currently CS2 modes only: competitive/wingman/etc.), status (draft→registration→locked→active→completed), registration window, server_ip, server_password
- **teams** — id, tournament_id, name, seed
- **team_members** — id, team_id, steam_name (this is how players are identified)
- **matches** — id, tournament_id, round, position, best_of, team1_id, team2_id, winner_id, next_match_id, is_bye (single-elimination bracket structure)
- **games** — id, match_id, game_number, map_name, scores (team1/team2 + per-half CT/T breakdowns), server_name (links to running container), status, team1_starts_ct
- **game_rounds** — id, game_id, round number, winner (CT/T), reason (elimination/bomb/defuse/time)
- **player_stats** — id, game_id, team_id, player_name, kills, deaths, assists, hs%, kdr, adr, mvps, ef, ud
- **settings** — key/value store (site_name, announcement, event_start/end, active_tournament_id)
- **schedule_items** — event schedule entries (title, start/end time, color, description)

### How Game Tracking Works
The `gametracker` package streams Docker container logs in real-time and parses CS2 server output to extract:
- Kill events (weapon, modifiers like headshot/wallbang/noscope)
- Round results from CS2's native `round_stats` JSON
- Player stats (K/D/A, ADR, HS%, money, equipment, weapons)
- Half-time detection, side swaps, overtime
- Match completion → writes final scores + stats to DB, advances bracket winner

Player matching currently works by comparing the Steam display name from the server against `team_members.steam_name` in the DB. If they don't match exactly, the player's stats are lost.

### How Server Launch Works
`docker/launch.go` builds a `docker compose run` command with environment variables (port, game mode, map, passwords, etc.). Each server is a container named `cs2-<name>` using host networking. The launch page (`/admin/launch`) pre-fills the next available port by checking existing containers, but this check happens at page load time — not at submission time.

### CS2 Server Commands (via RCON)
Commands are sent through the RCON client in `rcon/rcon.go`. The game tracker also uses RCON to poll player status. Any CS2 console command can be sent — relevant ones for new features include match restart commands, pause/unpause, and `say` for chat messages.

### Demo Files
`tv_autorecord 1` is set in `lan-default.cfg`. Demos are written to `/home/steam/cs2-dedicated/game/csgo/replays/` inside the container, on the shared `cs2-base` Docker volume. Currently there's no web access to these files — they must be manually copied out via `docker cp` or direct volume access.

---

## 1. Match Restart & Ready-Up System (CS2)

### Match Restart with Countdown
- [ ] Add a "Restart Match" button in the admin UI for active servers/tournament games
- [ ] On restart, execute the appropriate CS2 commands to reset the match (research best approach — `mp_restartgame`, `mp_warmup_start`, etc.)
- [ ] Display an in-game countdown timer via CS2 chat/center messages so players clearly know the match is (re)starting
- [ ] Investigate CS2 config/commands for reliable countdown behavior

### Pause & Ready-Up After Restart
- [ ] After restart, automatically pause the match
- [ ] Implement a ready-up system using CS2 chat — players type a command (e.g. `.ready`) to mark themselves as ready
- [ ] Match player connections against the team roster entered on the website (by Steam name)
- [ ] Only require real roster players to ready up — ignore bots and spectators
- [ ] Verify correct team sides (CT/T) based on what was configured on the website for starting sides
- [ ] If all rostered players have joined but not all have readied up, send a periodic console/chat message (every ~60s) showing ready status (e.g. "4/5 players ready, waiting on: PlayerX")
- [ ] Auto-unpause and start the match once all rostered players are ready
- [ ] Add a "Force Start" button in the admin panel to override and start the match regardless of ready status

---

## 2. Multi-Game Tournament Support

### Game Selection
- [ ] Add a game selection field when creating a tournament (dropdown with presets + custom/other entry)
- [ ] Presets to include at minimum: CS2, Valorant (expand as needed)
- [ ] For CS2 tournaments, enable CS2-specific features (server linking, live tracking, demo files, map veto, etc.)
- [ ] For non-CS2 tournaments, the bracket is manual — admins enter team names and scores by hand, no live server integration

### Rebrand Scope
- [ ] The application is UniLAN as a whole — schedule + multiple tournaments + dedicated server hosting
- [ ] Ensure the UI reflects this (UniLAN branding with CS2 server management as one section, not the primary identity)

---

## 3. Bracket System Expansion

### Double Elimination
- [ ] Implement double elimination bracket format (winners bracket, losers bracket, grand final)
- [ ] Handle losers bracket seeding from winners bracket losses
- [ ] Support grand final advantage (if applicable — configurable)

### Round Robin
- [ ] Implement round robin bracket format
- [ ] Generate all group stage matchups automatically
- [ ] Track standings (wins, losses, map differential, round differential — configurable tiebreakers)
- [ ] Display a group standings table on the public view

### Hybrid / Flexible Formats
- [ ] Support round robin as a first stage feeding into a single or double elimination playoff
- [ ] Allow configuring which teams advance from group stage to playoffs (e.g. top 2 from each group)
- [ ] Research common bracket systems and formats to ensure flexibility

### Map Veto System
- [ ] Add a map veto/pick UI for Bo3+ matches
- [ ] Make the veto format configurable per tournament (e.g. ban-ban-pick-pick-ban-ban-last for Bo3, or custom sequences)
- [ ] Account for different map pools per game mode (e.g. competitive has 7 maps, wingman has 6)
- [ ] Admin-operated: admins enter bans/picks on behalf of teams (teams come to the admin in person)
- [ ] Display veto results on the public bracket page for the relevant match

---

## 4. Port Reservation on Server Launch

- [ ] When submitting the server launch form, if the selected port is already in use (by a server that was launched moments before), automatically find and use the next available port instead of crashing
- [ ] The issue: opening multiple launch tabs quickly pre-fills the same "next available" port since it was correct at the time of page load — the fix should be server-side validation on submit
- [ ] Return clear feedback to the admin if the port was changed (e.g. "Port 27015 was in use, started on 27016 instead")

---

## 5. Demo File Access (CS2)

- [ ] After a CS2 game completes, copy the demo file from the Docker volume to a location the web server can serve
- [ ] Use the still-running server container to access the demo before cleanup
- [ ] Serve demo files as direct downloads from the public bracket/match view
- [ ] One download link per game (not zipped — each game in a Bo3 is its own `.dem` file)
- [ ] Only applicable when the tournament game is CS2

---

## 6. Player Name Matching & Stats

- [ ] When a player's Steam name doesn't exactly match the roster entry, their stats row is currently missing from the scoreboard/post-game stats
- [ ] In the admin UI, show all players the server detected (both matched and unmatched)
- [ ] Public view continues to show only matched players (current behavior)
- [ ] Allow admins to manually map an unmatched server player to a roster slot (e.g. dropdown: "This player is actually → [roster member]")
- [ ] After remapping, include that player's stats in the match results

---

## 7. Server Launch from Tournament View

- [ ] When launching a server for a tournament match, show a modal/inline form on the tournament page instead of navigating to a new tab
- [ ] Pre-fill the modal with tournament config (game mode, map from veto if applicable, team settings, etc.)
- [ ] Keep the admin on the tournament page after launch so they can continue managing the bracket

---

## 8. Unified WebSocket Architecture

Currently there are 9 separate WebSocket endpoints (4 public, 5 admin), meaning clients open multiple connections per page. This should be consolidated into two multiplexed endpoints with topic-based subscriptions.

### Two Endpoints
- [ ] `GET /ws` — single public WebSocket endpoint (unauthenticated)
- [ ] `GET /admin/ws` — single admin WebSocket endpoint (auth-protected)
- [ ] Keeping two endpoints avoids auth complexity on the public side and prevents accidental exposure of admin-only topics

### Topic Subscription Protocol
- [ ] Client sends subscribe/unsubscribe messages (e.g. `{"subscribe": "bracket:5"}`, `{"unsubscribe": "schedule"}`)
- [ ] Server tracks which topics each connection cares about and only sends relevant updates
- [ ] Public topics: `bracket:<tournamentID>`, `schedule`, `announce`
- [ ] Admin topics: `dashboard`, `server:logs:<name>`, `server:game:<name>`, `tournaments`, `tournament:<tid>`
- [ ] When navigating between pages (or HTMX swaps), client updates subscriptions on the existing connection rather than opening/closing WebSockets

### HTMX-Based Navigation (Maintain Persistent Connections)
- [ ] Use HTMX (`hx-get`, `hx-target`, `hx-push-url`) for page navigation instead of full page refreshes
- [ ] Page transitions swap the content area only — the layout shell (nav, WS connection, scripts) stays alive
- [ ] This means the WebSocket connection persists across page navigations, reducing reconnection overhead
- [ ] On content swap, JS updates topic subscriptions (unsubscribe old page topics, subscribe new ones)
- [ ] Eliminates the current pattern of establishing fresh WebSocket connections on every page load

### Migration
- [ ] Implement a central WebSocket hub (per endpoint) that manages connections and topic routing
- [ ] Refactor existing broadcasters (`dashBcast`, `bracketSubs`, `scheduleBcast`, `announceBcast`, etc.) to publish through the hub
- [ ] Update all JS to use a single shared connection per endpoint with topic subscribe/unsubscribe
- [ ] Remove the 9 individual WebSocket route handlers and endpoints once migration is complete

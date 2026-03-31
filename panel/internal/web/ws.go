package web

import (
	"context"
	"encoding/json"
	"html"
	"log"
	"net/http"
	"strings"
	"time"

	"cs2-panel/internal/docker"
	"cs2-panel/internal/gametracker"

	"github.com/gorilla/websocket"
)

const (
	pingInterval = 15 * time.Second
	pongWait     = 20 * time.Second
	writeWait    = 10 * time.Second
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// setupWSConn upgrades and configures a WebSocket connection.
func setupWSConn(w http.ResponseWriter, r *http.Request) (*websocket.Conn, chan struct{}, error) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, nil, err
	}

	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	return conn, done, nil
}

func (h *Handler) LogsWebSocket(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	conn, done, err := setupWSConn(w, r)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reader, isTTY, err := h.docker.StreamLogs(ctx, name)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	defer reader.Close()

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	lines := make(chan string, 64)
	go func() {
		docker.ReadLogLines(reader, isTTY, lines, done)
		close(lines)
	}()

	for {
		select {
		case <-done:
			return
		case <-pingTicker.C:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case line, ok := <-lines:
			if !ok {
				return
			}
			// Tag game event lines so the client can filter them
			prefix := ""
			if isGameEventLine(line) {
				prefix = "E:"
			}
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.TextMessage, []byte(prefix+line)); err != nil {
				return
			}
		}
	}
}

// GameStateWebSocket pushes game state as JSON.
// Players table: debounced (200ms) to batch rapid changes like buy phase.
// Killfeed: pushed immediately as individual events for instant display.
func (h *Handler) GameStateWebSocket(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	state := h.tracker.GetState(name)
	if state == nil {
		info, err := h.docker.InspectServer(r.Context(), "cs2-"+name)
		if err != nil {
			http.Error(w, "Server not found", http.StatusNotFound)
			return
		}
		state = h.tracker.TrackServer(name, info.Port, info.RCONPassword, info.GameMode, info.Map)
	}

	conn, done, err := setupWSConn(w, r)
	if err != nil {
		log.Printf("ws game upgrade: %v", err)
		return
	}
	defer conn.Close()

	changes, unsub := state.Subscribe()
	defer unsub()

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	var debounceTimer *time.Timer
	playersCh := make(chan struct{}, 1)

	lastKillIdx := state.KillCount()

	// Send initial full state
	h.sendPlayers(conn, name)
	h.sendKillfeedFull(conn, name)

	for {
		select {
		case <-done:
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return
		case <-pingTicker.C:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-changes:
			// Push new killfeed entries immediately
			newKills := state.GetKillsSince(lastKillIdx)
			if len(newKills) > 0 {
				lastKillIdx = state.KillCount()
				if err := h.sendKillfeedNew(conn, newKills); err != nil {
					return
				}
			}

			// Debounce players update
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(200*time.Millisecond, func() {
				select {
				case playersCh <- struct{}{}:
				default:
				}
			})
		case <-playersCh:
			if err := h.sendPlayers(conn, name); err != nil {
				return
			}
		}
	}
}

type gamePlayerJSON struct {
	Name       string   `json:"name"`
	Team       string   `json:"team"`
	IsBot      bool     `json:"bot,omitempty"`
	Online     bool     `json:"online"`
	Kills      int      `json:"k"`
	Deaths     int      `json:"d"`
	Assists    int      `json:"a"`
	Ping       int      `json:"ping"`
	Duration   string   `json:"dur,omitempty"`
	Money      int      `json:"money"`
	Weapons    []string `json:"weapons,omitempty"`
	Grenades   []string `json:"grenades,omitempty"`
	HasArmor   bool     `json:"armor,omitempty"`
	HasHelmet  bool     `json:"helmet,omitempty"`
	HasDefuser bool     `json:"defuser,omitempty"`
	HasBomb    bool     `json:"bomb,omitempty"`
	Alive      bool     `json:"alive"`
	HSPercent  float64  `json:"hsp"`
	KDR        float64  `json:"kdr"`
	ADR        float64  `json:"adr"`
	MVPs       int      `json:"mvp"`
	EF         int      `json:"ef"`
	UD         float64  `json:"ud"`
	KnifeKills int      `json:"knifek,omitempty"`
	ZeusKills  int      `json:"zeusk,omitempty"`
	Level      int      `json:"level,omitempty"`
}

type killJSON struct {
	Killer       string `json:"killer,omitempty"`
	KillerTeam   string `json:"kt,omitempty"`
	Victim       string `json:"victim,omitempty"`
	VictimTeam   string `json:"vt,omitempty"`
	Weapon       string `json:"weapon,omitempty"`
	Headshot     bool   `json:"hs,omitempty"`
	Wallbang     bool   `json:"wb,omitempty"`
	Noscope      bool   `json:"ns,omitempty"`
	BlindKill    bool   `json:"bk,omitempty"`
	InAir        bool   `json:"ia,omitempty"`
	ThroughSmoke bool   `json:"ts,omitempty"`
	Assister     string `json:"assist,omitempty"`
	AssisterTeam string `json:"at,omitempty"`
	FlashAssist  bool   `json:"fa,omitempty"`
	System       bool   `json:"sys,omitempty"`
	Message      string `json:"msg,omitempty"`
	Time         string `json:"time"`
}

func shortTeam(t string) string {
	switch t {
	case "TERRORIST":
		return "T"
	case "SPECTATOR":
		return "S"
	default:
		return t
	}
}

func killToJSON(k gametracker.Kill) killJSON {
	return killJSON{
		Killer: html.EscapeString(k.Killer), KillerTeam: shortTeam(k.KillerTeam),
		Victim: html.EscapeString(k.Victim), VictimTeam: shortTeam(k.VictimTeam),
		Weapon:   k.Weapon,
		Headshot: k.Headshot, Wallbang: k.Wallbang, Noscope: k.Noscope,
		BlindKill: k.BlindKill, InAir: k.InAir, ThroughSmoke: k.ThroughSmoke,
		Assister: html.EscapeString(k.Assister), AssisterTeam: shortTeam(k.AssisterTeam), FlashAssist: k.FlashAssist,
		System: k.IsSystem, Message: html.EscapeString(k.Message),
		Time: k.Time.Format("15:04:05"),
	}
}

type scoreJSON struct {
	Round     int         `json:"round"`
	CT        int         `json:"ct"`
	T         int         `json:"t"`
	GameMode  string      `json:"mode,omitempty"`
	Map       string      `json:"map,omitempty"`
	Rounds    []roundJSON `json:"rounds,omitempty"`
	HalfRound int         `json:"half,omitempty"`
	Warmup    bool        `json:"warmup,omitempty"`
}

type roundJSON struct {
	Round  int    `json:"r"`
	Winner string `json:"w"`  // "CT" or "T"
	Reason string `json:"rs"` // "elimination", "bomb", "defuse", "time"
}

// sendPlayers sends a "players" message from tracker state (no RCON calls).
func (h *Handler) sendPlayers(conn *websocket.Conn, name string) error {
	players := buildPlayerList(name, h.tracker)

	var score *scoreJSON
	if state := h.tracker.GetState(name); state != nil {
		s := state.GetScore()
		var rounds []roundJSON
		for _, r := range s.Rounds {
			rounds = append(rounds, roundJSON{Round: r.Round, Winner: r.Winner, Reason: r.Reason})
		}
		score = &scoreJSON{Round: s.Round, CT: s.CT, T: s.T, GameMode: s.GameMode, Map: s.CurrentMap, Rounds: rounds, HalfRound: s.HalfRound, Warmup: s.InWarmup}
	}

	msg := struct {
		Type    string           `json:"type"`
		Players []gamePlayerJSON `json:"players"`
		Score   *scoreJSON       `json:"score,omitempty"`
	}{Type: "players", Players: players, Score: score}

	conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteJSON(msg)
}

// buildPlayerList reads player data entirely from the tracker.
func buildPlayerList(serverName string, tracker *gametracker.Manager) []gamePlayerJSON {
	state := tracker.GetState(serverName)
	if state == nil {
		return nil
	}

	scoreboard := state.GetScoreboard()
	players := make([]gamePlayerJSON, 0, len(scoreboard))

	for _, ps := range scoreboard {
		team := shortTeam(ps.Team)

		// Send raw weapon names — client renders as SVG icons
		weapons := ps.WeaponList()
		grenades := ps.GrenadeList()

		// Compute HS% and KDR from kill counts if round_stats hasn't provided them
		hsp := ps.HSPercent
		kdr := ps.KDR
		if hsp == 0 && ps.Kills > 0 && ps.HeadshotKills > 0 {
			hsp = float64(ps.HeadshotKills) / float64(ps.Kills) * 100
		}
		if kdr == 0 && ps.Kills > 0 {
			if ps.Deaths > 0 {
				kdr = float64(ps.Kills) / float64(ps.Deaths)
			} else {
				kdr = float64(ps.Kills)
			}
		}

		players = append(players, gamePlayerJSON{
			Name: html.EscapeString(ps.Name), Team: team, IsBot: ps.IsBot, Online: ps.Online,
			Kills: ps.Kills, Deaths: ps.Deaths, Assists: ps.Assists,
			Ping: ps.Ping, Duration: ps.Duration, Money: ps.Money,
			Weapons: weapons, Grenades: grenades,
			HasArmor: ps.HasArmor, HasHelmet: ps.HasHelmet, HasDefuser: ps.HasDefuser, HasBomb: ps.HasBomb, Alive: ps.Alive,
			Damage: ps.Damage, HSPercent: hsp, KDR: kdr, ADR: ps.ADR, MVPs: ps.MVPs, EF: ps.EF, UD: ps.UD,
			KnifeKills: ps.KnifeKills, ZeusKills: ps.ZeusKills, Level: ps.Level,
		})
	}

	return players
}

// sendKillfeedFull sends the full killfeed (initial load).
func (h *Handler) sendKillfeedFull(conn *websocket.Conn, name string) error {
	state := h.tracker.GetState(name)
	var kills []killJSON
	if state != nil {
		for _, k := range state.GetKillfeed(20) {
			kills = append(kills, killToJSON(k))
		}
	}
	msg := struct {
		Type     string     `json:"type"`
		Killfeed []killJSON `json:"killfeed"`
	}{Type: "killfeed", Killfeed: kills}

	conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteJSON(msg)
}

// sendKillfeedNew sends only new kill events (incremental).
func (h *Handler) sendKillfeedNew(conn *websocket.Conn, newKills []gametracker.Kill) error {
	kills := make([]killJSON, len(newKills))
	for i, k := range newKills {
		kills[i] = killToJSON(k)
	}
	msg := struct {
		Type  string     `json:"type"`
		Kills []killJSON `json:"kills"`
	}{Type: "kill", Kills: kills}

	conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteJSON(msg)
}

// DashboardWebSocket subscribes to the shared dashboard broadcaster.
func (h *Handler) DashboardWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, done, err := setupWSConn(w, r)
	if err != nil {
		log.Printf("ws dashboard upgrade: %v", err)
		return
	}
	defer conn.Close()

	updates, unsub := h.subscribeDashboard()
	defer unsub()

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	// Send initial state immediately
	data := h.getDashboardData()
	if data == nil {
		data = h.buildDashboardJSON()
	}
	if data != nil {
		conn.SetWriteDeadline(time.Now().Add(writeWait))
		conn.WriteMessage(websocket.TextMessage, data)
	}

	for {
		select {
		case <-done:
			return
		case <-pingTicker.C:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-updates:
			data := h.getDashboardData()
			if data == nil {
				continue
			}
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}
}

type dashServerJSON struct {
	Name        string     `json:"name"`
	Alias       string     `json:"alias"`
	Status      string     `json:"status"`
	Port        int        `json:"port"`
	GameMode    string     `json:"mode"`
	Map         string     `json:"map"`
	MaxPlayers  int        `json:"maxPlayers"`
	PlayerCount int        `json:"playerCount"`
	Score       *scoreJSON `json:"score,omitempty"`
}

// buildDashboardJSON computes dashboard state and returns the JSON bytes.
func (h *Handler) buildDashboardJSON() []byte {
	servers, err := h.docker.ListServers(context.Background())
	if err != nil {
		servers = nil
	}

	// Stop tracking servers that are no longer running
	runningNames := make(map[string]bool)
	for _, s := range servers {
		if s.Status == "running" {
			runningNames[s.Name] = true
		}
	}
	h.tracker.StopNotIn(runningNames)

	var result []dashServerJSON
	for _, s := range servers {
		ds := dashServerJSON{
			Name:       s.Name,
			Alias:      html.EscapeString(h.aliases.Get(s.Name)),
			Status:     s.Status,
			Port:       s.Port,
			GameMode:   s.GameMode,
			Map:        s.Map,
			MaxPlayers: s.MaxPlayers,
		}

		// Start tracking if not already (so dashboard shows live data)
		if s.Status == "running" && s.Port > 0 && s.RCONPassword != "" {
			h.tracker.TrackServer(s.Name, s.Port, s.RCONPassword, s.GameMode, s.Map)
		}

		// Get player count and score from tracker
		state := h.tracker.GetState(s.Name)
		if state != nil {
			sc := state.GetScore()
			ds.Score = &scoreJSON{Round: sc.Round, CT: sc.CT, T: sc.T, GameMode: sc.GameMode}
			if sc.CurrentMap != "" {
				ds.Map = sc.CurrentMap
			}
			for _, ps := range state.GetScoreboard() {
				if ps.Online {
					ds.PlayerCount++
				}
			}
		}

		result = append(result, ds)
	}

	msg := struct {
		Type    string           `json:"type"`
		Servers []dashServerJSON `json:"servers"`
	}{Type: "dashboard", Servers: result}

	data, _ := json.Marshal(msg)
	return data
}

// isGameEventLine returns true for log lines that are game events
// already displayed in the killfeed/players panel.
// All CS2 game event lines start with "L MM/DD/YYYY".
func isGameEventLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) > 2 && trimmed[0] == 'L' && trimmed[1] == ' ' {
		return true
	}
	// Also filter JSON round_stats blocks and MatchStatus
	if strings.Contains(line, "JSON_BEGIN{") || strings.Contains(line, "}}JSON_END") ||
		strings.HasPrefix(trimmed, "MatchStatus:") || strings.HasPrefix(trimmed, "Started map") ||
		strings.HasPrefix(trimmed, "GMR_") {
		return true
	}
	return false
}

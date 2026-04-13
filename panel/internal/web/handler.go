package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"unilan/internal/db"
	"unilan/internal/docker"
	"unilan/internal/games"
	"unilan/internal/games/cs2/tracker"
	"unilan/internal/games/cs2/rcon"
	webfs "unilan/web"
)

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// sanitize strips HTML tags and trims whitespace from user input.
func sanitize(s string) string {
	return strings.TrimSpace(htmlTagRe.ReplaceAllString(s, ""))
}

var (
	descAllowedRe = regexp.MustCompile(`(?i)<(/?)(a|b|i|em|strong|br)\b`)
	descHrefRe    = regexp.MustCompile(`(?i)href\s*=\s*"([^"]*)"`)
)

// sanitizeDesc allows a small set of safe HTML tags, strips everything else.
// For <a> tags, only href is kept; javascript: URLs are removed.
func sanitizeDesc(s string) string {
	s = strings.TrimSpace(s)
	return htmlTagRe.ReplaceAllStringFunc(s, func(tag string) string {
		if !descAllowedRe.MatchString(tag) {
			return ""
		}
		lower := strings.ToLower(tag)
		if strings.HasPrefix(lower, "<a ") || lower == "<a>" {
			m := descHrefRe.FindStringSubmatch(tag)
			if len(m) < 2 || m[1] == "" {
				return ""
			}
			href := m[1]
			if strings.HasPrefix(strings.ToLower(href), "javascript:") {
				return ""
			}
			if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
				return `<a href="` + href + `" target="_blank" rel="noopener noreferrer">`
			}
			return `<a href="` + href + `">`
		}
		return tag
	})
}

// broadcaster is a simple pub/sub signal hub. Subscribers receive a struct{}
// on their channel whenever notify is called. Used for dashboard and tournament list.
type broadcaster struct {
	mu   sync.RWMutex
	subs []chan struct{}
}

func (b *broadcaster) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, c := range b.subs {
			if c == ch {
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
				break
			}
		}
	}
}

func (b *broadcaster) notify() {
	b.mu.RLock()
	subs := make([]chan struct{}, len(b.subs))
	copy(subs, b.subs)
	b.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

type Handler struct {
	docker      *docker.Client
	rcon        *rcon.Manager
	tracker     *tracker.Manager
	db          *db.DB
	aliases     *AliasStore
	composeFile string
	defaultRCON string
	pages       map[string]*template.Template

	// Dashboard broadcast: compute once, send to all WS clients
	dashMu   sync.RWMutex
	dashData []byte // cached JSON message
	dashBcast broadcaster

	// Servers currently being restarted or stopped (name -> last known info for dashboard)
	restartMu      sync.RWMutex
	restartServers map[string]docker.ServerInfo
	stoppingMu     sync.RWMutex
	stoppingServers map[string]bool

	// System stats sampler
	sys sysSampler

	// Bracket broadcast: push updates to public bracket viewers (per tournament)
	bracketMu   sync.RWMutex
	bracketSubs map[int64][]chan struct{}

	// Tournament list broadcast: sync admin tournament list page
	tournamentListBcast broadcaster

	// Announcements: ephemeral broadcast to all connected clients
	announceMu    sync.RWMutex
	announcement  string
	announceLink  string
	announceBcast broadcaster

	// Schedule broadcast: compute once, send to all WS clients
	schedMu    sync.RWMutex
	schedData  []byte // cached JSON message
	scheduleBcast broadcaster

	// Unified WebSocket hub (topic-based multiplexed connections)
	hub *WSHub
}

func NewHandler(dc *docker.Client, rm *rcon.Manager, tm *tracker.Manager, database *db.DB, composeFile, defaultRCON string) (*Handler, error) {
	tmplFS, err := fs.Sub(webfs.Assets, "templates")
	if err != nil {
		return nil, fmt.Errorf("template fs: %w", err)
	}

	funcMap := template.FuncMap{
		"upper": strings.ToUpper,
		"siteName": func() string {
			return database.GetSetting("site_name")
		},
		"isExternal": func(link string) bool {
			return strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://")
		},
		"divf": func(a, b int) float64 {
			if b == 0 {
				return 0
			}
			return float64(a) / float64(b)
		},
		"seq": func(n int) []int {
			s := make([]int, n)
			for i := range s {
				s[i] = i + 1
			}
			return s
		},
		"derefInt64": func(p *int64) int64 {
			if p == nil {
				return 0
			}
			return *p
		},
		"mapPoolsJSON": func() template.JS {
			data, _ := json.Marshal(games.Default().MapPools())
			return template.JS(data)
		},
		"allMapsJSON": func() template.JS {
			data, _ := json.Marshal(games.Default().AllMaps())
			return template.JS(data)
		},
		"gameModesJSON": func() template.JS {
			data, _ := json.Marshal(games.Default().GameModes())
			return template.JS(data)
		},
	}

	// Parse base layout + partials as a clonable base
	base, err := template.New("base").Funcs(funcMap).ParseFS(tmplFS,
		"layout.html",
		"partials/*.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parse base templates: %w", err)
	}

	// Each page gets its own clone of base so {{define "content"}} doesn't collide
	pages := make(map[string]*template.Template)
	for _, page := range []string{"dashboard.html", "server.html", "launch.html", "bracket.html", "admin_tournament.html", "tournaments.html", "settings.html", "home.html", "admin_schedule.html"} {
		t, err := template.Must(base.Clone()).ParseFS(tmplFS, page)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", page, err)
		}
		pages[page] = t
	}

	// Login is standalone (no layout)
	login, err := template.New("login").Funcs(funcMap).ParseFS(tmplFS, "login.html")
	if err != nil {
		return nil, fmt.Errorf("parse login: %w", err)
	}
	pages["login.html"] = login

	// Partials can be rendered directly from the base
	pages["server_rows.html"] = base
	pages["player_list.html"] = base
	pages["rcon_output.html"] = base
	pages["scoreboard.html"] = base
	pages["killfeed.html"] = base

	h := &Handler{
		docker:         dc,
		rcon:           rm,
		tracker:        tm,
		db:             database,
		aliases:        NewAliasStore(database),
		composeFile:    composeFile,
		defaultRCON:    defaultRCON,
		pages:          pages,
		restartServers:  make(map[string]docker.ServerInfo),
		stoppingServers: make(map[string]bool),
		bracketSubs:     make(map[int64][]chan struct{}),
		announcement:    database.GetSetting("announcement"),
		announceLink:    database.GetSetting("announcement_link"),
		hub:             NewWSHub(),
	}
	go h.dashboardPoller()
	h.setupGameOverHook()
	h.setupReadyHook()
	return h, nil
}

func generateRCONPassword() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// isHTMX returns true if the request was made by HTMX (boosted navigation).
func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// renderPage renders a page template. For HTMX requests, it renders just the
// "content" block (no layout wrapper). For normal requests, it renders the full
// page with layout. This enables HTMX boosted link navigation where only the
// content area is swapped.
func (h *Handler) renderPage(w http.ResponseWriter, r *http.Request, name string, data any) {
	t, ok := h.pages[name]
	if !ok {
		slog.Error("template not found", "name", name)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	execName := "layout"
	if isHTMX(r) {
		execName = "content"
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, execName, data); err != nil {
		slog.Error("render failed", "template", name, "exec", execName, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	t, ok := h.pages[name]
	if !ok {
		slog.Error("template not found", "name", name)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// For page templates that use layout, execute "layout"; for partials/login, execute the named define
	execName := name
	switch name {
	case "dashboard.html", "server.html", "launch.html", "bracket.html", "admin_tournament.html", "tournaments.html", "settings.html", "home.html", "admin_schedule.html":
		execName = "layout"
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, execName, data); err != nil {
		slog.Error("render failed", "template", name, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Error": r.URL.Query().Get("error") == "1",
	}
	h.render(w, "login.html", data)
}

type serverWithStatus struct {
	docker.ServerInfo
	PlayerCount int
	CurrentMap  string
	RCONOk      bool
}

func (h *Handler) enrichServers(ctx context.Context) []serverWithStatus {
	servers, err := h.docker.ListServers(ctx)
	if err != nil {
		slog.Error("list servers", "err", err)
		return nil
	}
	result := make([]serverWithStatus, len(servers))
	var wg sync.WaitGroup
	for i, s := range servers {
		result[i] = serverWithStatus{ServerInfo: s}
		if s.Status == "running" && s.Port > 0 && s.RCONPassword != "" {
			wg.Add(1)
			go func(idx int, s docker.ServerInfo) {
				defer wg.Done()
				addr := fmt.Sprintf("localhost:%d", s.Port)
				resp, err := h.rcon.Execute(addr, s.RCONPassword, "status")
				if err == nil {
					status := rcon.ParseStatus(resp)
					result[idx].PlayerCount = status.Humans + status.Bots
					result[idx].CurrentMap = status.Map
					result[idx].RCONOk = true
				}
			}(i, s)
		}
	}
	wg.Wait()
	return result
}

// dashboardPoller refreshes dashboard state periodically as a fallback
// for changes not covered by explicit notifyDashboard calls (e.g. player count changes).
func (h *Handler) dashboardPoller() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		h.notifyDashboard()
	}
}

// trackServer wraps tracker.TrackServer with DB-backed metadata restoration.
func (h *Handler) trackServer(name string, port int, rconPw, mode, mapName string) *tracker.ServerState {
	state, isNew := h.tracker.TrackServer(name, port, rconPw, mode, mapName)
	if isNew {
		if saved, err := h.db.LoadTrackerState(name); err == nil && saved != nil {
			state.RestoreMetadata(tracker.TrackerMetadata{
				GameMode:   saved.GameMode,
				CurrentMap: saved.CurrentMap,
				HalfRound:  saved.HalfRound,
				MaxRounds:  saved.MaxRounds,
				CTScore:    saved.CTScore,
				TScore:     saved.TScore,
				Round:      saved.Round,
				InWarmup:   saved.InWarmup,
				IsPaused:   saved.IsPaused,
			})
		}
	}
	return state
}


func (h *Handler) getDashboardData() []byte {
	h.dashMu.RLock()
	defer h.dashMu.RUnlock()
	return h.dashData
}

// notifyDashboard triggers an immediate dashboard rebuild + broadcast.
func (h *Handler) notifyDashboard() {
	data := h.buildDashboardJSON()
	h.dashMu.Lock()
	h.dashData = data
	h.dashMu.Unlock()
	h.dashBcast.notify()

	if h.hub != nil && data != nil {
		h.hub.Publish("dashboard", "dashboard", json.RawMessage(data))
	}
}

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	// Just check if servers exist — WS fills in live data immediately
	servers, _ := h.docker.ListServers(r.Context())
	h.renderPage(w, r, "dashboard.html", map[string]any{
		"Servers": servers,
		"Title":   "Dashboard",
	})
}

func (h *Handler) SettingsPage(w http.ResponseWriter, r *http.Request) {
	h.announceMu.RLock()
	ann := h.announcement
	annLink := h.announceLink
	h.announceMu.RUnlock()
	h.renderPage(w, r, "settings.html", map[string]any{
		"Title":            "Settings",
		"SiteName":         h.db.GetSetting("site_name"),
		"Announcement":     ann,
		"AnnouncementLink": annLink,
		"EventStart":       h.db.GetSetting("event_start"),
		"EventEnd":         h.db.GetSetting("event_end"),
	})
}

func (h *Handler) SetSiteName(w http.ResponseWriter, r *http.Request) {
	name := sanitize(r.FormValue("site_name"))
	if name == "" {
		name = "UniLAN"
	}
	h.db.SetSetting("site_name", name)
	slog.Info("settings: site_name", "name", name)
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

func (h *Handler) ServersPartial(w http.ResponseWriter, r *http.Request) {
	h.render(w, "server_rows.html", map[string]any{
		"Servers": h.enrichServers(r.Context()),
	})
}

func (h *Handler) LaunchPage(w http.ResponseWriter, r *http.Request) {
	// Find next available port starting from 27015
	nextPort := 27015
	servers, _ := h.docker.ListServers(r.Context())
	usedPorts := make(map[int]bool)
	for _, s := range servers {
		usedPorts[s.Port] = true
		usedPorts[s.TVPort] = true
	}
	for usedPorts[nextPort] || usedPorts[nextPort+1000] {
		nextPort++
	}

	h.renderPage(w, r, "launch.html", map[string]any{
		"Title":       "Launch Server",
		"DefaultRCON": h.defaultRCON,
		"NextPort":    nextPort,
	})
}

// wantsJSON returns true if the request prefers a JSON response (AJAX modal).
func wantsJSON(r *http.Request) bool {
	return r.Header.Get("Accept") == "application/json" ||
		r.Header.Get("X-Requested-With") == "fetch"
}

func (h *Handler) LaunchServer(w http.ResponseWriter, r *http.Request) {
	port, _ := strconv.Atoi(r.FormValue("port"))
	players, _ := strconv.Atoi(r.FormValue("players"))
	if players == 0 {
		players = 10
	}

	req := docker.LaunchRequest{
		Name:     r.FormValue("name"),
		Port:     port,
		Mode:     r.FormValue("mode"),
		Map:      r.FormValue("map"),
		Players:  players,
		Password: r.FormValue("password"),
		RCON:     r.FormValue("rcon"),
		TV:       r.FormValue("tv") == "on",
		ExtraCfg: strings.TrimSpace(r.FormValue("extra_cfg")),
	}

	if req.Mode == "" {
		req.Mode = "competitive"
	}
	if req.Map == "" {
		req.Map = "de_inferno"
	}
	if req.RCON == "" {
		req.RCON = generateRCONPassword()
	}

	submittedPort := req.Port
	req, err := h.docker.Launch(r.Context(), req, h.composeFile)
	if err != nil {
		slog.Error("server: launch failed", "name", req.Name, "err", err)
		if wantsJSON(r) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		h.renderPage(w, r, "launch.html", map[string]any{
			"Title":       "Launch Server",
			"Error":       err.Error(),
			"DefaultRCON": h.defaultRCON,
			"Form":        req,
		})
		return
	}

	if req.Port != submittedPort {
		slog.Info("server: port conflict resolved", "name", req.Name, "requested", submittedPort, "assigned", req.Port)
	}
	slog.Info("server: launched", "name", req.Name, "port", req.Port, "mode", req.Mode, "map", req.Map, "players", req.Players)
	go h.notifyDashboard()

	// If launched from a bracket match, link the server to the game
	if matchID := r.FormValue("match_id"); matchID != "" {
		gameNum := r.FormValue("game_number")
		if gameNum == "" {
			gameNum = "1"
		}
		mid, _ := strconv.ParseInt(matchID, 10, 64)
		gn, _ := strconv.Atoi(gameNum)
		if mid > 0 {
			// Create game if it doesn't exist, then link
			games, _ := h.db.GetMatchGames(mid)
			var gameID int64
			for _, g := range games {
				if g.GameNumber == gn {
					gameID = g.ID
					break
				}
			}
			if gameID == 0 {
				gameID, _ = h.db.CreateGame(mid, gn, req.Map, true)
			}
			if gameID > 0 {
				h.db.LinkGameToServer(gameID, req.Name)
				slog.Info("server: linked to game", "server", req.Name, "game", gameID, "match", mid, "game_number", gn)
				h.notifyBracket()
			}
		}
	}

	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":   true,
			"name": req.Name,
			"port": req.Port,
		})
		return
	}

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (h *Handler) ServerDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	info, err := h.docker.InspectServer(r.Context(), games.Default().ContainerPrefix()+name)
	if err != nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	var status *rcon.StatusInfo
	if info.Status == "running" && info.Port > 0 && info.RCONPassword != "" {
		addr := fmt.Sprintf("localhost:%d", info.Port)
		resp, err := h.rcon.Execute(addr, info.RCONPassword, "status")
		if err == nil {
			s := rcon.ParseStatus(resp)
			status = &s
		}
	}

	// Start tracking this server for killfeed/scoreboard via UDP log
	state := h.trackServer(name, info.Port, info.RCONPassword, info.GameMode, info.Map)

	// Build initial JSON for immediate client-side render
	initialPlayers := buildPlayerList(name, h.tracker)
	var initialScore *scoreJSON
	sc := state.GetScore()
	var rounds []roundJSON
	for _, r := range sc.Rounds {
		rounds = append(rounds, roundJSON{Round: r.Round, Winner: r.Winner, Reason: r.Reason})
	}
	initialScore = &scoreJSON{Round: sc.Round, CT: sc.CT, T: sc.T, GameMode: sc.GameMode, Map: html.EscapeString(sc.CurrentMap), Rounds: rounds, HalfRound: sc.HalfRound, Warmup: sc.InWarmup}

	playersJSON, _ := json.Marshal(initialPlayers)
	scoreJSON, _ := json.Marshal(initialScore)

	// Check if server is linked to a tournament game for team name display
	var ctTeamName, tTeamName, team1Name, team2Name string
	var team1StartsCT bool
	if game, err := h.db.GetGameByServerAny(name); err == nil {
		if match, err := h.db.GetMatchByID(game.MatchID); err == nil {
			team1Name = match.Team1Name
			team2Name = match.Team2Name
			team1StartsCT = game.Team1StartsCT
			if team1StartsCT {
				ctTeamName = team1Name
				tTeamName = team2Name
			} else {
				ctTeamName = team2Name
				tTeamName = team1Name
			}
		}
	}

	// Check if there's a live game linked
	hasLinkedGame := false
	if _, err := h.db.GetGameByServer(name); err == nil {
		hasLinkedGame = true
	}

	h.renderPage(w, r, "server.html", map[string]any{
		"Title":          h.aliases.Get(info.Name),
		"Alias":          h.aliases.Get(info.Name),
		"Server":         info,
		"Status":         status,
		"Scoreboard":     state.GetScoreboard(),
		"Killfeed":       state.GetKillfeed(20),
		"InitPlayers":    template.JS(playersJSON),
		"InitScore":      template.JS(scoreJSON),
		"CTTeamName":     ctTeamName,
		"TTeamName":      tTeamName,
		"Team1Name":      team1Name,
		"Team2Name":      team2Name,
		"Team1StartsCT":  team1StartsCT,
		"HasLinkedGame":  hasLinkedGame,
	})
}

func (h *Handler) PlayersPartial(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	state := h.tracker.GetState(name)
	var players []CombinedPlayer
	if state != nil {
		for _, ps := range state.GetScoreboard() {
			cp := CombinedPlayer{
				Name: ps.Name, IsBot: ps.IsBot, Online: ps.Online,
				Kills: ps.Kills, Deaths: ps.Deaths, Assists: ps.Assists,
				Score: ps.Score(), Ping: ps.Ping, Duration: ps.Duration,
			}
			cp.Team = shortTeam(ps.Team)
			for _, w := range ps.WeaponList() {
				cp.Weapons = append(cp.Weapons, tracker.DisplayName(w))
			}
			for _, g := range ps.GrenadeList() {
				if short, ok := tracker.GrenadeShort[g]; ok {
					cp.Grenades = append(cp.Grenades, short)
				}
			}
			players = append(players, cp)
		}
	}

	h.render(w, "player_list.html", map[string]any{
		"Players": players,
	})
}

func (h *Handler) RCONCommand(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	command := r.FormValue("command")
	if command == "" {
		return
	}

	info, err := h.docker.InspectServer(r.Context(), games.Default().ContainerPrefix()+name)
	if err != nil {
		h.render(w, "rcon_output.html", map[string]any{
			"Command":  command,
			"Response": "Error: server not found",
			"IsError":  true,
		})
		return
	}

	addr := fmt.Sprintf("localhost:%d", info.Port)
	resp, err := h.rcon.Execute(addr, info.RCONPassword, command)
	if err != nil {
		slog.Warn("rcon: command failed", "server", name, "cmd", command, "err", err)
		h.render(w, "rcon_output.html", map[string]any{
			"Command":  command,
			"Response": fmt.Sprintf("Error: %v", err),
			"IsError":  true,
		})
		return
	}

	slog.Info("rcon: command", "server", name, "cmd", command, "ip", r.RemoteAddr)

	// Filter out game event log lines from RCON response
	var filtered []string
	for _, line := range strings.Split(resp, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 2 && trimmed[0] == 'L' && trimmed[1] == ' ' {
			continue
		}
		if trimmed != "" {
			filtered = append(filtered, line)
		}
	}
	resp = strings.Join(filtered, "\n")

	h.render(w, "rcon_output.html", map[string]any{
		"Command":  command,
		"Response": resp,
		"IsError":  false,
	})
}

func (h *Handler) ScoreboardPartial(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	state := h.tracker.GetState(name)
	var scoreboard []tracker.PlayerStats
	if state != nil {
		scoreboard = state.GetScoreboard()
	}
	h.render(w, "scoreboard.html", map[string]any{
		"Scoreboard": scoreboard,
	})
}

func (h *Handler) KillfeedPartial(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	state := h.tracker.GetState(name)
	var killfeed []tracker.Kill
	if state != nil {
		killfeed = state.GetKillfeed(20)
	}
	h.render(w, "killfeed.html", map[string]any{
		"Killfeed": killfeed,
	})
}

func (h *Handler) RestartServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	slog.Info("server: restarting", "name", name, "ip", r.RemoteAddr)

	info, err := h.docker.InspectServer(r.Context(), games.Default().ContainerPrefix()+name)
	if err != nil {
		slog.Error("server: restart inspect failed", "name", name, "err", err)
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// Redirect immediately, restart in background
	redirect := "/admin/server/" + name
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusOK)
	} else {
		http.Redirect(w, r, redirect, http.StatusSeeOther)
	}

	// Notify game WS clients that server is restarting
	if state := h.tracker.GetState(name); state != nil {
		state.MarkRestarting()
	}

	// Track restarting state for dashboard
	h.restartMu.Lock()
	h.restartServers[name] = info
	h.restartMu.Unlock()

	go func() {
		h.tracker.StopTracking(name)
		h.db.DeleteTrackerState(name) // clear so fresh server gets clean state
		h.notifyDashboard()
		if err := h.docker.StopServer(context.Background(), name); err != nil {
			slog.Error("server: restart stop failed", "name", name, "err", err)
		}

		tvEnabled := "0"
		if info.TVEnabled {
			tvEnabled = "1"
		}
		req := docker.LaunchRequest{
			Name:     info.Name,
			Port:     info.Port,
			Mode:     info.GameMode,
			Map:      info.Map,
			Players:  info.MaxPlayers,
			Password: info.Password,
			RCON:     info.RCONPassword,
			TV:       tvEnabled == "1",
			ExtraCfg: info.ExtraCfg,
		}
		if _, err := h.docker.Launch(context.Background(), req, h.composeFile); err != nil {
			slog.Error("server: restart launch failed", "name", name, "err", err)
		}

		h.restartMu.Lock()
		delete(h.restartServers, name)
		h.restartMu.Unlock()

		h.notifyDashboard()
	}()
}

func (h *Handler) StopServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	slog.Info("server: stopping", "name", name, "ip", r.RemoteAddr)

	// Redirect immediately, stop in background
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/admin")
		w.WriteHeader(http.StatusOK)
	} else {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	}

	// Track stopping state for dashboard
	h.stoppingMu.Lock()
	h.stoppingServers[name] = true
	h.stoppingMu.Unlock()

	go func() {
		h.tracker.StopTracking(name)
		h.db.DeleteTrackerState(name)
		h.notifyDashboard()
		if err := h.docker.StopServer(context.Background(), name); err != nil {
			slog.Error("server: stop failed", "name", name, "err", err)
		}

		h.stoppingMu.Lock()
		delete(h.stoppingServers, name)
		h.stoppingMu.Unlock()

		h.notifyDashboard()
	}()
}

func (h *Handler) RenameServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	alias := r.FormValue("alias")
	h.aliases.Set(name, alias)
	slog.Info("server: renamed", "name", name, "alias", alias)
	w.WriteHeader(http.StatusOK)
}

// CombinedPlayer merges RCON status info with tracker K/D/A.
type CombinedPlayer struct {
	Name     string
	IsBot    bool
	Ping     int
	Duration string
	Address  string
	Team     string   // "CT", "T", or ""
	Kills    int
	Deaths   int
	Assists  int
	Score    int
	Weapons  []string // display names of non-grenade weapons
	Grenades []string // short grenade abbreviations
	Online   bool
}

// ServeDemo serves a demo file for download.
func (h *Handler) ServeDemo(w http.ResponseWriter, r *http.Request) {
	gameID, err := strconv.ParseInt(r.PathValue("gameID"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}

	game, err := h.db.GetGameByID(gameID)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	if game.DemoPath == "" {
		http.Error(w, "No demo available", http.StatusNotFound)
		return
	}

	// Check file exists
	info, err := os.Stat(game.DemoPath)
	if err != nil || info.IsDir() {
		http.Error(w, "Demo file not found", http.StatusNotFound)
		return
	}

	filename := filepath.Base(game.DemoPath)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	http.ServeFile(w, r, game.DemoPath)
}


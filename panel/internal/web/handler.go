package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cs2-panel/internal/db"
	"cs2-panel/internal/docker"
	"cs2-panel/internal/gametracker"
	"cs2-panel/internal/rcon"
	webfs "cs2-panel/web"
)

type Handler struct {
	docker      *docker.Client
	rcon        *rcon.Manager
	tracker     *gametracker.Manager
	db          *db.DB
	aliases     *AliasStore
	composeFile string
	defaultRCON string
	pages       map[string]*template.Template

	// Dashboard broadcast: compute once, send to all WS clients
	dashMu   sync.RWMutex
	dashData []byte // cached JSON message
	dashSubs []chan struct{}

	// Servers currently being restarted (name -> last known info for dashboard)
	restartMu      sync.RWMutex
	restartServers map[string]docker.ServerInfo

	// System stats sampler
	sys sysSampler

	// Bracket broadcast: push updates to public bracket viewers
	bracketMu   sync.RWMutex
	bracketSubs []chan struct{}
}

func NewHandler(dc *docker.Client, rm *rcon.Manager, tm *gametracker.Manager, database *db.DB, composeFile, defaultRCON string) (*Handler, error) {
	tmplFS, err := fs.Sub(webfs.Assets, "templates")
	if err != nil {
		return nil, fmt.Errorf("template fs: %w", err)
	}

	funcMap := template.FuncMap{
		"upper": strings.ToUpper,
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
	for _, page := range []string{"dashboard.html", "server.html", "launch.html", "bracket.html", "admin_tournament.html"} {
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
		aliases:        NewAliasStore("server-aliases.json"),
		composeFile:    composeFile,
		defaultRCON:    defaultRCON,
		pages:          pages,
		restartServers: make(map[string]docker.ServerInfo),
	}
	go h.dashboardPoller()
	h.setupGameOverHook()
	return h, nil
}

func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	t, ok := h.pages[name]
	if !ok {
		log.Printf("template %s not found", name)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// For page templates that use layout, execute "layout"; for partials/login, execute the named define
	execName := name
	switch name {
	case "dashboard.html", "server.html", "launch.html", "bracket.html", "admin_tournament.html":
		execName = "layout"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, execName, data); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
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
		log.Printf("list servers: %v", err)
		return nil
	}
	var result []serverWithStatus
	for _, s := range servers {
		ss := serverWithStatus{ServerInfo: s}
		if s.Status == "running" && s.Port > 0 && s.RCONPassword != "" {
			addr := fmt.Sprintf("localhost:%d", s.Port)
			resp, err := h.rcon.Execute(addr, s.RCONPassword, "status")
			if err == nil {
				status := rcon.ParseStatus(resp)
				ss.PlayerCount = status.Humans + status.Bots
				ss.CurrentMap = status.Map
				ss.RCONOk = true
			}
		}
		result = append(result, ss)
	}
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

func (h *Handler) subscribeDashboard() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.dashMu.Lock()
	h.dashSubs = append(h.dashSubs, ch)
	h.dashMu.Unlock()
	return ch, func() {
		h.dashMu.Lock()
		defer h.dashMu.Unlock()
		for i, c := range h.dashSubs {
			if c == ch {
				h.dashSubs = append(h.dashSubs[:i], h.dashSubs[i+1:]...)
				break
			}
		}
	}
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
	subs := make([]chan struct{}, len(h.dashSubs))
	copy(subs, h.dashSubs)
	h.dashMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	// Just check if servers exist — WS fills in live data immediately
	servers, _ := h.docker.ListServers(r.Context())
	h.render(w, "dashboard.html", map[string]any{
		"Servers": servers,
		"Title":   "Dashboard",
	})
}

func (h *Handler) ServersPartial(w http.ResponseWriter, r *http.Request) {
	h.render(w, "server_rows.html", map[string]any{
		"Servers": h.enrichServers(r.Context()),
	})
}

func (h *Handler) LaunchPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, "launch.html", map[string]any{
		"Title":       "Launch Server",
		"DefaultRCON": h.defaultRCON,
	})
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
		req.RCON = h.defaultRCON
	}

	err := h.docker.Launch(r.Context(), req, h.composeFile)
	if err != nil {
		h.render(w, "launch.html", map[string]any{
			"Title":       "Launch Server",
			"Error":       err.Error(),
			"DefaultRCON": h.defaultRCON,
			"Form":        req,
		})
		return
	}

	h.notifyDashboard()

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
				log.Printf("linked server %s to game %d (match %d, game %d)", req.Name, gameID, mid, gn)
			}
		}
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) ServerDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	info, err := h.docker.InspectServer(r.Context(), "cs2-"+name)
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
	state := h.tracker.TrackServer(name, info.Port, info.RCONPassword, info.GameMode, info.Map)

	// Build initial JSON for immediate client-side render
	initialPlayers := buildPlayerList(name, h.tracker)
	var initialScore *scoreJSON
	sc := state.GetScore()
	var rounds []roundJSON
	for _, r := range sc.Rounds {
		rounds = append(rounds, roundJSON{Round: r.Round, Winner: r.Winner, Reason: r.Reason})
	}
	initialScore = &scoreJSON{Round: sc.Round, CT: sc.CT, T: sc.T, GameMode: sc.GameMode, Map: sc.CurrentMap, Rounds: rounds, HalfRound: sc.HalfRound, Warmup: sc.InWarmup}

	playersJSON, _ := json.Marshal(initialPlayers)
	scoreJSON, _ := json.Marshal(initialScore)

	h.render(w, "server.html", map[string]any{
		"Title":        h.aliases.Get(info.Name),
		"Alias":        h.aliases.Get(info.Name),
		"Server":       info,
		"Status":       status,
		"Scoreboard":   state.GetScoreboard(),
		"Killfeed":     state.GetKillfeed(20),
		"InitPlayers":  template.JS(playersJSON),
		"InitScore":    template.JS(scoreJSON),
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
				cp.Weapons = append(cp.Weapons, gametracker.DisplayName(w))
			}
			for _, g := range ps.GrenadeList() {
				if short, ok := gametracker.GrenadeShort[g]; ok {
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

	info, err := h.docker.InspectServer(r.Context(), "cs2-"+name)
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
		h.render(w, "rcon_output.html", map[string]any{
			"Command":  command,
			"Response": fmt.Sprintf("Error: %v", err),
			"IsError":  true,
		})
		return
	}

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
	var scoreboard []gametracker.PlayerStats
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
	var killfeed []gametracker.Kill
	if state != nil {
		killfeed = state.GetKillfeed(20)
	}
	h.render(w, "killfeed.html", map[string]any{
		"Killfeed": killfeed,
	})
}

func (h *Handler) RestartServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	info, err := h.docker.InspectServer(r.Context(), "cs2-"+name)
	if err != nil {
		log.Printf("restart %s: inspect failed: %v", name, err)
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// Redirect immediately, restart in background
	redirect := "/server/" + name
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
		h.notifyDashboard()
		if err := h.docker.StopServer(context.Background(), name); err != nil {
			log.Printf("restart %s: stop failed: %v", name, err)
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
		if err := h.docker.Launch(context.Background(), req, h.composeFile); err != nil {
			log.Printf("restart %s: launch failed: %v", name, err)
		}

		h.restartMu.Lock()
		delete(h.restartServers, name)
		h.restartMu.Unlock()

		h.notifyDashboard()
	}()
}

func (h *Handler) StopServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	// Redirect immediately, stop in background
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusOK)
	} else {
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}

	go func() {
		h.tracker.StopTracking(name)
		h.notifyDashboard()
		if err := h.docker.StopServer(context.Background(), name); err != nil {
			log.Printf("stop server %s: %v", name, err)
		}
		h.notifyDashboard()
	}()
}

func (h *Handler) RenameServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	alias := r.FormValue("alias")
	h.aliases.Set(name, alias)
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


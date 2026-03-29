package web

import (
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"cs2-panel/internal/docker"
	"cs2-panel/internal/gametracker"
	"cs2-panel/internal/rcon"
	webfs "cs2-panel/web"
)

type Handler struct {
	docker      *docker.Client
	rcon        *rcon.Manager
	tracker     *gametracker.Manager
	composeFile string
	defaultRCON string
	pages       map[string]*template.Template
}

func NewHandler(dc *docker.Client, rm *rcon.Manager, tm *gametracker.Manager, composeFile, defaultRCON string) (*Handler, error) {
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
	for _, page := range []string{"dashboard.html", "server.html", "launch.html"} {
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

	return &Handler{
		docker:      dc,
		rcon:        rm,
		tracker:     tm,
		composeFile: composeFile,
		defaultRCON: defaultRCON,
		pages:       pages,
	}, nil
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
	case "dashboard.html", "server.html", "launch.html":
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

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	servers, err := h.docker.ListServers(r.Context())
	if err != nil {
		log.Printf("list servers: %v", err)
		servers = nil
	}

	// Best-effort RCON status for player counts
	type serverWithStatus struct {
		docker.ServerInfo
		PlayerCount int
		CurrentMap  string
		RCONOk      bool
	}

	var enriched []serverWithStatus
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
		enriched = append(enriched, ss)
	}

	h.render(w, "dashboard.html", map[string]any{
		"Servers": enriched,
		"Title":   "Dashboard",
	})
}

func (h *Handler) ServersPartial(w http.ResponseWriter, r *http.Request) {
	servers, err := h.docker.ListServers(r.Context())
	if err != nil {
		log.Printf("list servers: %v", err)
		servers = nil
	}

	type serverWithStatus struct {
		docker.ServerInfo
		PlayerCount int
		CurrentMap  string
		RCONOk      bool
	}

	var enriched []serverWithStatus
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
		enriched = append(enriched, ss)
	}

	h.render(w, "server_rows.html", map[string]any{
		"Servers": enriched,
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
	state := h.tracker.TrackServer(name, info.Port, info.RCONPassword)

	h.render(w, "server.html", map[string]any{
		"Title":      info.Name,
		"Server":     info,
		"Status":     status,
		"Scoreboard": state.GetScoreboard(),
		"Killfeed":   state.GetKillfeed(20),
	})
}

func (h *Handler) PlayersPartial(w http.ResponseWriter, r *http.Request) {
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
			log.Printf("RCON status for %s:\n%s", name, resp)
			s := rcon.ParseStatus(resp)
			status = &s
		} else {
			log.Printf("RCON status error for %s: %v", name, err)
		}
	}

	// Merge RCON player list with tracker K/D/A stats
	players := mergePlayerData(name, status, h.tracker)

	h.render(w, "player_list.html", map[string]any{
		"Server":  info,
		"Status":  status,
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

func (h *Handler) StopServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	h.tracker.StopTracking(name)

	err := h.docker.StopServer(r.Context(), name)
	if err != nil {
		log.Printf("stop server %s: %v", name, err)
	}

	// Always redirect to dashboard
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// CombinedPlayer merges RCON status info with tracker K/D/A.
type CombinedPlayer struct {
	Name     string
	IsBot    bool
	Ping     int
	Duration string
	Address  string
	Kills    int
	Deaths   int
	Assists  int
	Score    int
	Online   bool // true if currently connected
}

func mergePlayerData(serverName string, status *rcon.StatusInfo, tracker *gametracker.Manager) []CombinedPlayer {
	// Build stats map from tracker
	statsMap := make(map[string]*gametracker.PlayerStats)
	state := tracker.GetState(serverName)
	if state != nil {
		for _, ps := range state.GetScoreboard() {
			ps := ps
			statsMap[ps.Name] = &ps
		}
	}

	seen := make(map[string]bool)
	var players []CombinedPlayer

	// First: online players from RCON status
	if status != nil {
		for _, p := range status.Players {
			cp := CombinedPlayer{
				Name:     p.Name,
				IsBot:    p.IsBot,
				Ping:     p.Ping,
				Duration: p.Duration,
				Address:  p.Address,
				Online:   true,
			}
			if s, ok := statsMap[p.Name]; ok {
				cp.Kills = s.Kills
				cp.Deaths = s.Deaths
				cp.Assists = s.Assists
				cp.Score = s.Score()
			}
			players = append(players, cp)
			seen[p.Name] = true
		}
	}

	// Second: offline players who have stats (disconnected but played)
	if state != nil {
		for _, ps := range state.GetScoreboard() {
			if !seen[ps.Name] {
				players = append(players, CombinedPlayer{
					Name:    ps.Name,
					Kills:   ps.Kills,
					Deaths:  ps.Deaths,
					Assists: ps.Assists,
					Score:   ps.Score(),
					Online:  false,
				})
			}
		}
	}

	// Sort: online first, then by score desc, then alphabetical
	sort.Slice(players, func(i, j int) bool {
		if players[i].Online != players[j].Online {
			return players[i].Online
		}
		if players[i].Score != players[j].Score {
			return players[i].Score > players[j].Score
		}
		return players[i].Name < players[j].Name
	})

	return players
}

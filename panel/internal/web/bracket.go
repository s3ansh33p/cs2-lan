package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"strconv"
	"time"

	"cs2-panel/internal/db"

	"github.com/gorilla/websocket"
)

// bracketPageData holds the template data for rendering a bracket page.
type bracketPageData struct {
	Title        string
	Tournament   *db.Tournament
	Teams        []db.Team
	Bracket      []db.Match
	CanRegister  bool
	ConnectInfo  string
	GamePorts    map[string]int
	TournamentID int64
}

// buildBracketPage loads teams, bracket, connect info, and live game ports for a tournament.
func (h *Handler) buildBracketPage(ctx context.Context, tournament *db.Tournament, title string) bracketPageData {
	data := bracketPageData{
		Title:      title,
		Tournament: tournament,
		GamePorts:  make(map[string]int),
	}
	if tournament == nil {
		return data
	}

	data.TournamentID = tournament.ID
	data.Teams, _ = h.db.ListTeams(tournament.ID)
	data.Bracket, _ = h.db.GetBracket(tournament.ID)
	data.CanRegister = tournament.CanRegister()

	if tournament.ServerIP != "" {
		data.ConnectInfo = fmt.Sprintf("connect %s", tournament.ServerIP)
		if tournament.ServerPassword != "" {
			data.ConnectInfo += fmt.Sprintf("; password %s", tournament.ServerPassword)
		}
	}

	if tournament.Status != db.TournamentCompleted {
		servers, _ := h.docker.ListServers(ctx)
		serverPorts := make(map[string]int)
		for _, s := range servers {
			serverPorts[s.Name] = s.Port
		}
		for _, m := range data.Bracket {
			for _, g := range m.Games {
				if g.Status == db.GameLive && g.ServerName != "" {
					if port, ok := serverPorts[g.ServerName]; ok {
						data.GamePorts[g.ServerName] = port
					}
				}
			}
		}
	}

	return data
}

func (h *Handler) PublicBracket(w http.ResponseWriter, r *http.Request) {
	tournament, err := h.db.GetActiveTournament()
	if err != nil {
		log.Printf("get tournament: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	title := ""
	if tournament != nil {
		title = tournament.Name
	}
	data := h.buildBracketPage(r.Context(), tournament, title)
	h.render(w, "bracket.html", map[string]any{
		"Title":        data.Title,
		"Tournament":   data.Tournament,
		"Teams":        data.Teams,
		"Bracket":      data.Bracket,
		"CanRegister":  data.CanRegister,
		"ConnectInfo":  data.ConnectInfo,
		"GamePorts":    data.GamePorts,
		"TournamentID": data.TournamentID,
	})
}

// PublicTournamentList shows all non-deleted tournaments.
func (h *Handler) PublicTournamentList(w http.ResponseWriter, r *http.Request) {
	tournaments, _ := h.db.ListTournaments()
	activeID, _ := h.db.GetActiveTournamentID()

	h.render(w, "tournaments.html", map[string]any{
		"Title":       "All Tournaments",
		"Tournaments": tournaments,
		"ActiveID":    activeID,
	})
}

// PublicTournamentBracket shows a specific tournament's bracket by ID.
func (h *Handler) PublicTournamentBracket(w http.ResponseWriter, r *http.Request) {
	tid, err := strconv.ParseInt(r.PathValue("tid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid tournament ID", http.StatusBadRequest)
		return
	}

	tournament, err := h.db.GetTournamentByID(tid)
	if err != nil || tournament == nil || tournament.DeletedAt != nil {
		http.Error(w, "Tournament not found", http.StatusNotFound)
		return
	}

	data := h.buildBracketPage(r.Context(), tournament, tournament.Name)
	h.render(w, "bracket.html", map[string]any{
		"Title":        data.Title,
		"Tournament":   data.Tournament,
		"Teams":        data.Teams,
		"Bracket":      data.Bracket,
		"CanRegister":  data.CanRegister,
		"ConnectInfo":  data.ConnectInfo,
		"GamePorts":    data.GamePorts,
		"TournamentID": data.TournamentID,
	})
}

// publicTournament resolves the tournament from a form's tournament_id field,
// falling back to the active tournament. Returns the tournament and the redirect URL.
func (h *Handler) publicTournament(r *http.Request) (*db.Tournament, string) {
	redirect := "/"
	if tidStr := r.FormValue("tournament_id"); tidStr != "" {
		tid, err := strconv.ParseInt(tidStr, 10, 64)
		if err == nil && tid > 0 {
			t, err := h.db.GetTournamentByID(tid)
			if err == nil && t != nil {
				redirect = fmt.Sprintf("/tournament/%d", tid)
				return t, redirect
			}
		}
	}
	t, _ := h.db.GetActiveTournament()
	return t, redirect
}

// Public team creation (during registration window)
func (h *Handler) PublicCreateTeam(w http.ResponseWriter, r *http.Request) {
	tournament, redirect := h.publicTournament(r)
	if tournament == nil || !tournament.CanRegister() {
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}

	name := sanitize(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}

	teamID, err := h.db.CreateTeam(tournament.ID, name)
	if err != nil {
		log.Printf("public create team: %v", err)
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": teamID, "name": name})
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// Public add member (during registration window)
func (h *Handler) PublicAddMember(w http.ResponseWriter, r *http.Request) {
	tournament, redirect := h.publicTournament(r)
	if tournament == nil || !tournament.CanRegister() {
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}

	teamID, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	steamName := sanitize(r.FormValue("steam_name"))
	if steamName == "" {
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}

	members, _ := h.db.ListMembers(teamID)
	if len(members) >= tournament.TeamSize {
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}

	mid, addErr := h.db.AddMember(teamID, steamName)
	if addErr != nil {
		log.Printf("public add member: %v", addErr)
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": mid, "team_id": teamID, "name": steamName})
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// Public remove member (during registration window)
func (h *Handler) PublicRemoveMember(w http.ResponseWriter, r *http.Request) {
	tournament, redirect := h.publicTournament(r)
	if tournament == nil || !tournament.CanRegister() {
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}

	mid, _ := strconv.ParseInt(r.PathValue("mid"), 10, 64)
	if err := h.db.RemoveMember(mid); err != nil {
		log.Printf("public remove member: %v", err)
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// Public rename team (during registration window)
func (h *Handler) PublicRenameTeam(w http.ResponseWriter, r *http.Request) {
	tournament, redirect := h.publicTournament(r)
	if tournament == nil || !tournament.CanRegister() {
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}

	teamID, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	name := sanitize(r.FormValue("name"))
	if name == "" {
		if isAJAX(r) {
			http.Error(w, "Name required", http.StatusBadRequest)
		} else {
			http.Redirect(w, r, redirect, http.StatusSeeOther)
		}
		return
	}

	if err := h.db.UpdateTeam(teamID, name); err != nil {
		log.Printf("public rename team: %v", err)
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// notifyBracket pushes an update signal to subscribers watching a specific tournament.
func (h *Handler) notifyBracket(tournamentID ...int64) {
	h.bracketMu.RLock()
	var allSubs []chan struct{}
	if len(tournamentID) > 0 {
		for _, tid := range tournamentID {
			allSubs = append(allSubs, h.bracketSubs[tid]...)
		}
	} else {
		// No ID specified — notify all (backward compat for match/game handlers)
		for _, subs := range h.bracketSubs {
			allSubs = append(allSubs, subs...)
		}
	}
	h.bracketMu.RUnlock()
	for _, ch := range allSubs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *Handler) subscribeBracket(tournamentID int64) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.bracketMu.Lock()
	h.bracketSubs[tournamentID] = append(h.bracketSubs[tournamentID], ch)
	h.bracketMu.Unlock()
	return ch, func() {
		h.bracketMu.Lock()
		defer h.bracketMu.Unlock()
		subs := h.bracketSubs[tournamentID]
		for i, c := range subs {
			if c == ch {
				h.bracketSubs[tournamentID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
	}
}

// BracketWebSocket pushes bracket updates to public viewers.
func (h *Handler) BracketWebSocket(w http.ResponseWriter, r *http.Request) {
	// Resolve tournament ID: from URL path or active tournament
	var tournamentID int64
	if tidStr := r.PathValue("tid"); tidStr != "" {
		var err error
		tournamentID, err = strconv.ParseInt(tidStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid tournament ID", http.StatusBadRequest)
			return
		}
	} else {
		id, _ := h.db.GetActiveTournamentID()
		tournamentID = id
	}

	if tournamentID == 0 {
		http.Error(w, "No tournament", http.StatusNotFound)
		return
	}

	// Refuse WS for completed tournaments
	tournament, err := h.db.GetTournamentByID(tournamentID)
	if err != nil || tournament == nil {
		http.Error(w, "Tournament not found", http.StatusNotFound)
		return
	}
	if tournament.Status == db.TournamentCompleted {
		http.Error(w, "Tournament completed, no live updates", http.StatusGone)
		return
	}

	conn, done, err := setupWSConn(w, r)
	if err != nil {
		log.Printf("ws bracket upgrade: %v", err)
		return
	}
	defer conn.Close()

	updates, unsub := h.subscribeBracket(tournamentID)
	defer unsub()

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	// Send initial bracket state
	h.sendBracketState(conn, tournamentID)

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
			if err := h.sendBracketState(conn, tournamentID); err != nil {
				return
			}
		}
	}
}

func (h *Handler) sendBracketState(conn *websocket.Conn, tournamentID int64) error {
	tournament, err := h.db.GetTournamentByID(tournamentID)
	if err != nil || tournament == nil {
		return nil
	}
	bracket, err := h.db.GetBracket(tournament.ID)
	if err != nil {
		return nil
	}

	type gameJSON struct {
		ID     int64  `json:"id"`
		Num    int    `json:"num"`
		Map    string `json:"map"`
		T1     int    `json:"t1"`
		T2     int    `json:"t2"`
		Status string `json:"status"`
		Server string `json:"server,omitempty"`
		Port   int    `json:"port,omitempty"`
		T1CT   bool   `json:"t1ct"`
		H1CT   int    `json:"h1ct"`
		H1T    int    `json:"h1t"`
		H2CT   int    `json:"h2ct"`
		H2T    int    `json:"h2t"`
	}
	type teamRef struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	type matchJSON struct {
		ID     int64      `json:"id"`
		Round  int        `json:"round"`
		Pos    int        `json:"pos"`
		BestOf int        `json:"bestOf"`
		Team1  teamRef    `json:"team1"`
		Team2  teamRef    `json:"team2"`
		Winner int64      `json:"winner"`
		IsBye  bool       `json:"isBye"`
		Games  []gameJSON `json:"games"`
	}

	// Build server port lookup
	servers, _ := h.docker.ListServers(context.Background())
	serverPorts := make(map[string]int)
	for _, s := range servers {
		serverPorts[s.Name] = s.Port
	}

	var matches []matchJSON
	for _, m := range bracket {
		mj := matchJSON{
			ID: m.ID, Round: m.Round, Pos: m.Position, BestOf: m.BestOf, IsBye: m.IsBye,
			Team1: teamRef{Name: html.EscapeString(m.Team1Name)}, Team2: teamRef{Name: html.EscapeString(m.Team2Name)},
		}
		if m.Team1ID != nil {
			mj.Team1.ID = *m.Team1ID
		}
		if m.Team2ID != nil {
			mj.Team2.ID = *m.Team2ID
		}
		if m.WinnerID != nil {
			mj.Winner = *m.WinnerID
		}
		for _, g := range m.Games {
			gj := gameJSON{ID: g.ID, Num: g.GameNumber, Map: g.MapName, T1: g.Team1Score, T2: g.Team2Score,
				Status: g.Status, Server: g.ServerName, T1CT: g.Team1StartsCT,
				H1CT: g.H1CT, H1T: g.H1T, H2CT: g.H2CT, H2T: g.H2T}
			// Look up port for live games
			if g.Status == db.GameLive && g.ServerName != "" {
				if port, ok := serverPorts[g.ServerName]; ok {
					gj.Port = port
				}
			}
			mj.Games = append(mj.Games, gj)
		}
		matches = append(matches, mj)
	}

	// Build teams list
	type memberJSON struct {
		ID        int64  `json:"id"`
		TeamID    int64  `json:"teamId"`
		SteamName string `json:"steamName"`
	}
	type teamJSON struct {
		ID      int64        `json:"id"`
		Name    string       `json:"name"`
		Members []memberJSON `json:"members"`
	}
	var teamsOut []teamJSON
	if teams, err := h.db.ListTeams(tournament.ID); err == nil {
		for _, t := range teams {
			tj := teamJSON{ID: t.ID, Name: html.EscapeString(t.Name)}
			for _, m := range t.Members {
				tj.Members = append(tj.Members, memberJSON{ID: m.ID, TeamID: m.TeamID, SteamName: html.EscapeString(m.SteamName)})
			}
			teamsOut = append(teamsOut, tj)
		}
	}

	// Build tournament metadata for status-aware clients
	var connectInfo string
	if tournament.ServerIP != "" {
		connectInfo = fmt.Sprintf("connect %s", tournament.ServerIP)
		if tournament.ServerPassword != "" {
			connectInfo += fmt.Sprintf("; password %s", tournament.ServerPassword)
		}
	}

	msg := struct {
		Type        string      `json:"type"`
		Bracket     []matchJSON `json:"bracket"`
		Teams       []teamJSON  `json:"teams"`
		TeamSize    int         `json:"teamSize"`
		Status      string      `json:"status"`
		Name        string      `json:"name"`
		CanRegister bool        `json:"canRegister"`
		ConnectInfo string      `json:"connectInfo,omitempty"`
	}{
		Type:        "bracket",
		Bracket:     matches,
		Teams:       teamsOut,
		TeamSize:    tournament.TeamSize,
		Status:      tournament.Status,
		Name:        html.EscapeString(tournament.Name),
		CanRegister: tournament.CanRegister(),
		ConnectInfo: html.EscapeString(connectInfo),
	}

	conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteJSON(msg)
}

// PublicGameStats returns HTML with round history bar + player stats table.
func (h *Handler) PublicGameStats(w http.ResponseWriter, r *http.Request) {
	gameID, _ := strconv.ParseInt(r.PathValue("gid"), 10, 64)

	rounds, _ := h.db.GetGameRounds(gameID)
	stats, _ := h.db.GetGameStats(gameID)

	if len(rounds) == 0 && len(stats) == 0 {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<p class="text-sm text-slate-500">No data available for this game.</p>`)
		return
	}

	w.Header().Set("Content-Type", "text/html")

	// Round history bar
	if len(rounds) > 0 {
		var halfRound int
		h.db.QueryRow(`SELECT half_round FROM games WHERE id=?`, gameID).Scan(&halfRound)
		maxRounds := halfRound * 2
		renderRoundHistoryHTML(w, rounds, halfRound, maxRounds)
	}

	// Player stats table — split by team
	if len(stats) > 0 {
		// Build team ID -> name lookup from the stats (at most 2 teams)
		teamIDs := make(map[int64]bool)
		for _, s := range stats {
			teamIDs[s.TeamID] = true
		}
		teamNames := make(map[int64]string)
		for id := range teamIDs {
			var name string
			if err := h.db.QueryRow(`SELECT name FROM teams WHERE id=?`, id).Scan(&name); err == nil {
				teamNames[id] = name
			}
		}

		// Split stats into two groups by team
		var group1, group2 []db.PlayerStat
		var firstTeamID int64
		for _, s := range stats {
			if firstTeamID == 0 {
				firstTeamID = s.TeamID
			}
			if s.TeamID == firstTeamID {
				group1 = append(group1, s)
			} else {
				group2 = append(group2, s)
			}
		}

		name1 := teamNames[firstTeamID]
		name2 := ""
		if len(group2) > 0 {
			name2 = teamNames[group2[0].TeamID]
		}

		writeStatsRows := func(players []db.PlayerStat, teamName string) {
			if len(players) == 0 {
				return
			}
			label := html.EscapeString(teamName)
			if label == "" {
				label = "Player"
			}
			fmt.Fprintf(w, `<tr class="text-slate-500 border-b border-slate-700"><th class="text-left px-3 py-2">%s</th>`, label)
			fmt.Fprint(w, `<th class="px-2 py-2">K</th><th class="px-2 py-2">D</th><th class="px-2 py-2">A</th>`)
			fmt.Fprint(w, `<th class="px-2 py-2">MVPs</th><th class="px-2 py-2">HS%</th>`)
			fmt.Fprint(w, `<th class="px-2 py-2">KDR</th><th class="px-2 py-2">ADR</th>`)
			fmt.Fprint(w, `<th class="px-2 py-2">EF</th><th class="px-2 py-2">UD</th></tr>`)
			for _, s := range players {
				fmt.Fprintf(w, `<tr class="text-slate-300 border-b border-slate-700/50">`)
				fmt.Fprintf(w, `<td class="px-3 py-1.5 font-medium">%s</td>`, html.EscapeString(s.PlayerName))
				fmt.Fprintf(w, `<td class="px-2 py-1.5 text-center">%d</td>`, s.Kills)
				fmt.Fprintf(w, `<td class="px-2 py-1.5 text-center">%d</td>`, s.Deaths)
				fmt.Fprintf(w, `<td class="px-2 py-1.5 text-center">%d</td>`, s.Assists)
				fmt.Fprintf(w, `<td class="px-2 py-1.5 text-center">%d</td>`, s.MVPs)
				fmt.Fprintf(w, `<td class="px-2 py-1.5 text-center">%.0f%%</td>`, s.HSPercent)
				fmt.Fprintf(w, `<td class="px-2 py-1.5 text-center">%.2f</td>`, s.KDR)
				fmt.Fprintf(w, `<td class="px-2 py-1.5 text-center">%.0f</td>`, s.ADR)
				fmt.Fprintf(w, `<td class="px-2 py-1.5 text-center">%d</td>`, s.EF)
				fmt.Fprintf(w, `<td class="px-2 py-1.5 text-center">%.0f</td>`, s.UD)
				fmt.Fprint(w, `</tr>`)
			}
		}

		fmt.Fprint(w, `<table class="w-full text-sm mt-4 mb-3">`)
		writeStatsRows(group1, name1)
		writeStatsRows(group2, name2)
		fmt.Fprint(w, `</table>`)
	}
}

// renderRoundHistoryHTML writes the 2-row round history bar as HTML.
// Same visual logic as the live server view JS (admin.js renderScore).
func renderRoundHistoryHTML(w http.ResponseWriter, rounds []db.GameRound, halfRound, maxRounds int) {
	type section struct {
		label  string
		top    string // CT-on-top icons
		bottom string // T-on-top icons
	}

	iconHTML := func(r db.GameRound) string {
		icon := ""
		switch r.Reason {
		case "elimination":
			icon = "/static/icons/ui/kill.svg"
		case "bomb":
			icon = "/static/icons/equipment/planted_c4.svg"
		case "defuse":
			icon = "/static/icons/equipment/defuser.svg"
		case "time":
			icon = "/static/icons/ui/timer.svg"
		}
		filter := `filter:brightness(0) saturate(100%) invert(55%) sepia(90%) saturate(500%) hue-rotate(190deg)` // CT blue
		if r.Winner == "T" {
			filter = `filter:brightness(0) saturate(100%) invert(70%) sepia(90%) saturate(400%) hue-rotate(5deg)` // T yellow
		}
		return fmt.Sprintf(`<img src="%s" class="h-4 w-4" style="%s" title="Round %d: %s (%s)">`,
			icon, filter, r.Round, r.Winner, r.Reason)
	}

	blank := `<span class="inline-block w-4 h-4"></span>`

	// getPeriod returns section index and whether CT is on top
	type period struct {
		key     string
		label   string
		ctOnTop bool
	}
	getPeriod := func(roundNum int) period {
		if halfRound == 0 {
			return period{"reg_0", "First Half", true}
		}
		if roundNum <= halfRound {
			return period{"reg_0", "First Half", true}
		}
		if maxRounds > 0 && roundNum <= maxRounds {
			return period{"reg_1", "Second Half", false}
		}
		// Overtime
		otRound := roundNum - maxRounds
		otNum := (otRound-1)/6 + 1
		withinOT := otRound - (otNum-1)*6
		otHalf := 0
		if withinOT > 3 {
			otHalf = 1
		}
		ctTop := otHalf == 0
		key := fmt.Sprintf("ot%d_%d", otNum, otHalf)
		label := ""
		if otHalf == 0 {
			label = fmt.Sprintf("OT%d", otNum)
		}
		return period{key, label, ctTop}
	}

	// Build sections in order
	var sections []section
	sectionMap := map[string]int{} // key -> index in sections

	ensureSection := func(p period) int {
		if idx, ok := sectionMap[p.key]; ok {
			return idx
		}
		idx := len(sections)
		sections = append(sections, section{label: p.label})
		sectionMap[p.key] = idx
		return idx
	}

	// Pre-create regulation sections
	ensureSection(period{"reg_0", "First Half", true})
	if halfRound > 0 {
		ensureSection(period{"reg_1", "Second Half", false})
	}

	for _, r := range rounds {
		p := getPeriod(r.Round)
		idx := ensureSection(p)
		icon := iconHTML(r)
		isTop := (r.Winner == "CT") == p.ctOnTop
		if isTop {
			sections[idx].top += icon
			sections[idx].bottom += blank
		} else {
			sections[idx].top += blank
			sections[idx].bottom += icon
		}
	}

	// Render grid
	var cols []string
	for i := range sections {
		if i > 0 {
			cols = append(cols, "5px")
		}
		cols = append(cols, "auto")
	}

	colStr := ""
	for i, c := range cols {
		if i > 0 {
			colStr += " "
		}
		colStr += c
	}

	fmt.Fprintf(w, `<div class="mb-4 overflow-x-auto"><div class="grid grid-rows-[auto_1fr_1fr] gap-0.5" style="grid-template-columns:%s;min-width:max-content">`, colStr)

	// Row 0: labels
	for i, s := range sections {
		if i > 0 {
			fmt.Fprint(w, `<div class="row-span-3 w-px bg-slate-500 mx-0.5"></div>`)
		}
		fmt.Fprintf(w, `<div class="text-center text-xs text-slate-500 font-medium px-1">%s</div>`, s.label)
	}
	// Row 1: top
	for i, s := range sections {
		rounding := ""
		if i == 0 {
			rounding = " rounded-tl"
		} else if i == len(sections)-1 {
			rounding = " rounded-tr"
		}
		fmt.Fprintf(w, `<div class="flex items-center gap-0.5 px-1.5 py-1 bg-slate-700/30 min-h-6%s">%s</div>`, rounding, s.top)
	}
	// Row 2: bottom
	for i, s := range sections {
		rounding := ""
		if i == 0 {
			rounding = " rounded-bl"
		} else if i == len(sections)-1 {
			rounding = " rounded-br"
		}
		fmt.Fprintf(w, `<div class="flex items-center gap-0.5 px-1.5 py-1 bg-slate-700/30 min-h-6%s">%s</div>`, rounding, s.bottom)
	}
	fmt.Fprint(w, `</div></div>`)
}

package web

import (
	"encoding/json"
	"unilan/internal/auth"
	"io/fs"
	"net/http"

	webfs "unilan/web"
)

func SetupRoutes(a *auth.Auth, h *Handler) http.Handler {
	// Wire auth into the WebSocket hub for admin topic gating
	h.hub.authFn = func(r *http.Request) bool {
		c, err := r.Cookie("cs2panel_session")
		if err != nil {
			return false
		}
		return a.ValidateSession(c.Value)
	}

	// Send the current dashboard state to admins as soon as they subscribe,
	// so they don't have to wait up to a poll interval for the first frame.
	h.hub.RegisterSnapshot("dashboard", func() (string, any) {
		data := h.getDashboardData()
		if data == nil {
			data = h.buildDashboardJSON()
		}
		if data == nil {
			return "dashboard", nil
		}
		return "dashboard", json.RawMessage(data)
	})

	mux := http.NewServeMux()

	// Static files (embedded) — public assets
	staticFS, _ := fs.Sub(webfs.Assets, "static")
	staticHandler := http.FileServer(http.FS(staticFS))
	mux.HandleFunc("GET /static/admin.js", func(w http.ResponseWriter, r *http.Request) {
		// Guard admin.js behind auth
		a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.StripPrefix("/static/", staticHandler).ServeHTTP(w, r)
		})).ServeHTTP(w, r)
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler))

	// Auth routes
	mux.HandleFunc("GET /login", h.LoginPage)
	mux.HandleFunc("POST /login", a.HandleLogin)
	mux.HandleFunc("POST /logout", a.HandleLogout)

	// Public routes — homepage with schedule
	mux.HandleFunc("GET /{$}", h.HomePage)
	mux.HandleFunc("POST /teams", h.PublicCreateTeam)
	mux.HandleFunc("POST /teams/{id}/members", h.PublicAddMember)
	mux.HandleFunc("POST /teams/{id}/members/{mid}/delete", h.PublicRemoveMember)
	mux.HandleFunc("POST /teams/{id}/rename", h.PublicRenameTeam)
	mux.HandleFunc("GET /game/{gid}/stats", h.PublicGameStats)

	// CSTV+ broadcast relay: CS2 game servers (running on the host via
	// host-mode networking) POST demo fragments here. Mounted without auth
	// because the game client doesn't authenticate; the relay itself is
	// write-once-read-local-only, so unless someone is already on the LAN
	// with the panel open there's nothing interesting to scrape.
	mux.Handle("/cstv/", http.StripPrefix("/cstv", h.CSTVRelay.Handler()))

	// Public routes — specific tournament by ID
	mux.HandleFunc("GET /tournaments", h.PublicTournamentList)
	mux.HandleFunc("GET /tournament/{tid}", h.PublicTournamentBracket)
	mux.HandleFunc("GET /tournament/{tid}/game/{gid}/stats", h.PublicGameStats)
	mux.HandleFunc("GET /ws", h.ServeWS)
	mux.HandleFunc("GET /demo/{gameID}", h.cs2.ServeDemo)

	// Protected admin routes
	protected := http.NewServeMux()
	protected.HandleFunc("GET /admin/{$}", h.Dashboard)
	protected.HandleFunc("GET /admin/api/servers", h.ServersPartial)
	protected.HandleFunc("GET /admin/settings", h.SettingsPage)
	protected.HandleFunc("POST /admin/settings/site-name", h.SetSiteName)
	protected.HandleFunc("POST /admin/settings/event-bounds", h.SetEventBounds)
	protected.HandleFunc("GET /admin/schedule", h.AdminSchedule)
	protected.HandleFunc("POST /admin/schedule/create", h.AdminCreateScheduleItem)
	protected.HandleFunc("POST /admin/schedule/{id}/update", h.AdminUpdateScheduleItem)
	protected.HandleFunc("POST /admin/schedule/{id}/delete", h.AdminDeleteScheduleItem)
	protected.HandleFunc("POST /admin/announcement", h.SetAnnouncement)
	protected.HandleFunc("GET /admin/launch", h.LaunchPage)
	protected.HandleFunc("POST /admin/launch", h.LaunchServer)
	protected.HandleFunc("GET /admin/server/{name}", h.ServerDetail)
	protected.HandleFunc("GET /admin/server/{name}/players", h.PlayersPartial)
	protected.HandleFunc("POST /admin/server/{name}/rcon", h.RCONCommand)
	protected.HandleFunc("GET /admin/server/{name}/logs/ws", h.LogsWebSocket)
	protected.HandleFunc("GET /admin/server/{name}/game/ws", h.GameStateWebSocket)
	protected.HandleFunc("GET /admin/server/{name}/killfeed", h.cs2.KillfeedPartial)
	protected.HandleFunc("POST /admin/server/{name}/rename", h.RenameServer)
	protected.HandleFunc("POST /admin/server/{name}/restart", h.RestartServer)
	protected.HandleFunc("POST /admin/server/{name}/stop", h.StopServer)
	protected.HandleFunc("POST /admin/server/{name}/restart-match", h.cs2.RestartMatch)
	protected.HandleFunc("POST /admin/server/{name}/force-start", h.cs2.ForceStart)
	protected.HandleFunc("GET /admin/server/{name}/ready-state", h.cs2.ReadyState)

	// Admin tournament routes — list/selector
	protected.HandleFunc("GET /admin/tournament", h.AdminTournament)
	protected.HandleFunc("GET /admin/tournament/{tid}", h.AdminTournamentDetail)
	protected.HandleFunc("POST /admin/tournament/create", h.CreateTournament)

	// Admin tournament routes — scoped by tournament ID
	protected.HandleFunc("POST /admin/tournament/{tid}/update", h.UpdateTournament)
	protected.HandleFunc("POST /admin/tournament/{tid}/delete", h.SoftDeleteTournament)
	protected.HandleFunc("POST /admin/tournament/{tid}/restore", h.RestoreTournament)
	protected.HandleFunc("POST /admin/tournament/{tid}/purge", h.PurgeTournament)
	protected.HandleFunc("POST /admin/tournament/{tid}/hide", h.HideTournament)
	protected.HandleFunc("POST /admin/tournament/{tid}/unhide", h.UnhideTournament)
	protected.HandleFunc("POST /admin/tournament/{tid}/status", h.SetTournamentStatus)
	protected.HandleFunc("POST /admin/tournament/{tid}/active", h.SetActiveTournament)
	protected.HandleFunc("POST /admin/tournament/{tid}/teams", h.AdminCreateTeam)
	protected.HandleFunc("POST /admin/tournament/{tid}/teams/{id}/delete", h.AdminDeleteTeam)
	protected.HandleFunc("POST /admin/tournament/{tid}/teams/{id}/members", h.AdminAddMember)
	protected.HandleFunc("POST /admin/tournament/{tid}/teams/{id}/members/{mid}/delete", h.AdminRemoveMember)
	protected.HandleFunc("POST /admin/tournament/{tid}/teams/{id}/rename", h.AdminRenameTeam)
	protected.HandleFunc("POST /admin/tournament/{tid}/bracket/seed", h.AdminSeedBracket)
	protected.HandleFunc("POST /admin/tournament/{tid}/bracket/delete", h.AdminDeleteBracket)
	protected.HandleFunc("POST /admin/tournament/{tid}/generate-playoffs", h.AdminGeneratePlayoffs)

	// Admin match/game routes — work by match/game ID (tournament-scoped via foreign keys)
	protected.HandleFunc("POST /admin/bracket/bestof", h.AdminSetBestOf)
	protected.HandleFunc("POST /admin/bracket/winner", h.AdminSetWinner)
	protected.HandleFunc("POST /admin/bracket/clearwinner", h.AdminClearWinner)
	protected.HandleFunc("POST /admin/bracket/swap", h.AdminSwapTeams)
	protected.HandleFunc("POST /admin/match/{id}/game", h.AdminCreateGame)
	protected.HandleFunc("POST /admin/match/{id}/game/{gid}", h.AdminUpdateGame)
	protected.HandleFunc("POST /admin/match/{id}/game/{gid}/side", h.AdminSetGameSide)
	protected.HandleFunc("POST /admin/match/{id}/game/{gid}/reset", h.AdminResetGame)
	protected.HandleFunc("POST /admin/match/{id}/game/{gid}/delete", h.AdminDeleteGame)
	protected.HandleFunc("POST /admin/match/{id}/game/{gid}/remap", h.AdminRemapPlayer)
	protected.HandleFunc("GET /admin/match/{id}/game/{gid}/unmatched", h.AdminGameStatsAdmin)
	protected.HandleFunc("GET /admin/match/{id}/launch", h.AdminLaunchMatch)

	// Veto routes
	protected.HandleFunc("GET /admin/match/{id}/veto", h.AdminGetVetoState)
	protected.HandleFunc("POST /admin/match/{id}/veto", h.AdminSubmitVetoStep)
	protected.HandleFunc("DELETE /admin/match/{id}/veto", h.AdminClearVeto)

	mux.Handle("/admin/", a.Middleware(protected))

	return mux
}

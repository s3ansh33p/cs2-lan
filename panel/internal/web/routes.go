package web

import (
	"cs2-panel/internal/auth"
	"io/fs"
	"net/http"

	webfs "cs2-panel/web"
)

func SetupRoutes(a *auth.Auth, h *Handler) http.Handler {
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

	// Public routes
	mux.HandleFunc("GET /login", h.LoginPage)
	mux.HandleFunc("POST /login", a.HandleLogin)
	mux.HandleFunc("POST /logout", a.HandleLogout)

	// Public bracket routes (no auth)
	mux.HandleFunc("GET /bracket", h.PublicBracket)
	mux.HandleFunc("POST /bracket/teams", h.PublicCreateTeam)
	mux.HandleFunc("POST /bracket/teams/{id}/members", h.PublicAddMember)
	mux.HandleFunc("POST /bracket/teams/{id}/members/{mid}/delete", h.PublicRemoveMember)
	mux.HandleFunc("GET /bracket/game/{gid}/stats", h.PublicGameStats)
	mux.HandleFunc("GET /bracket/ws", h.BracketWebSocket)

	// Protected routes
	protected := http.NewServeMux()
	protected.HandleFunc("GET /{$}", h.Dashboard)
	protected.HandleFunc("GET /api/servers", h.ServersPartial)
	protected.HandleFunc("GET /api/dashboard/ws", h.DashboardWebSocket)
	protected.HandleFunc("GET /launch", h.LaunchPage)
	protected.HandleFunc("POST /launch", h.LaunchServer)
	protected.HandleFunc("GET /server/{name}", h.ServerDetail)
	protected.HandleFunc("GET /server/{name}/players", h.PlayersPartial)
	protected.HandleFunc("POST /server/{name}/rcon", h.RCONCommand)
	protected.HandleFunc("GET /server/{name}/logs/ws", h.LogsWebSocket)
	protected.HandleFunc("GET /server/{name}/game/ws", h.GameStateWebSocket)
	protected.HandleFunc("GET /server/{name}/killfeed", h.KillfeedPartial)
	protected.HandleFunc("POST /server/{name}/rename", h.RenameServer)
	protected.HandleFunc("POST /server/{name}/restart", h.RestartServer)
	protected.HandleFunc("POST /server/{name}/stop", h.StopServer)

	// Admin tournament routes
	protected.HandleFunc("GET /admin/tournament", h.AdminTournament)
	protected.HandleFunc("POST /admin/tournament/create", h.CreateTournament)
	protected.HandleFunc("POST /admin/tournament/update", h.UpdateTournament)
	protected.HandleFunc("POST /admin/tournament/delete", h.DeleteTournament)
	protected.HandleFunc("POST /admin/tournament/status", h.SetTournamentStatus)
	protected.HandleFunc("POST /admin/tournament/teams", h.AdminCreateTeam)
	protected.HandleFunc("POST /admin/tournament/teams/{id}/delete", h.AdminDeleteTeam)
	protected.HandleFunc("POST /admin/tournament/teams/{id}/members", h.AdminAddMember)
	protected.HandleFunc("POST /admin/tournament/teams/{id}/members/{mid}/delete", h.AdminRemoveMember)
	protected.HandleFunc("POST /admin/bracket/seed", h.AdminSeedBracket)
	protected.HandleFunc("POST /admin/bracket/bestof", h.AdminSetBestOf)
	protected.HandleFunc("POST /admin/bracket/winner", h.AdminSetWinner)
	protected.HandleFunc("POST /admin/bracket/swap", h.AdminSwapTeams)
	protected.HandleFunc("POST /admin/match/{id}/game", h.AdminCreateGame)
	protected.HandleFunc("POST /admin/match/{id}/game/{gid}", h.AdminUpdateGame)
	protected.HandleFunc("GET /admin/match/{id}/launch", h.AdminLaunchMatch)

	mux.Handle("/", a.Middleware(protected))

	return mux
}

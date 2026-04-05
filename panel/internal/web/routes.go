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

	// Auth routes
	mux.HandleFunc("GET /login", h.LoginPage)
	mux.HandleFunc("POST /login", a.HandleLogin)
	mux.HandleFunc("POST /logout", a.HandleLogout)

	// Public routes — active tournament at root
	mux.HandleFunc("GET /{$}", h.PublicBracket)
	mux.HandleFunc("POST /teams", h.PublicCreateTeam)
	mux.HandleFunc("POST /teams/{id}/members", h.PublicAddMember)
	mux.HandleFunc("POST /teams/{id}/members/{mid}/delete", h.PublicRemoveMember)
	mux.HandleFunc("POST /teams/{id}/rename", h.PublicRenameTeam)
	mux.HandleFunc("GET /game/{gid}/stats", h.PublicGameStats)
	mux.HandleFunc("GET /ws", h.BracketWebSocket)

	// Public routes — specific tournament by ID
	mux.HandleFunc("GET /tournaments", h.PublicTournamentList)
	mux.HandleFunc("GET /tournament/{tid}", h.PublicTournamentBracket)
	mux.HandleFunc("GET /tournament/{tid}/game/{gid}/stats", h.PublicGameStats)
	mux.HandleFunc("GET /tournament/{tid}/ws", h.BracketWebSocket)

	// Protected admin routes
	protected := http.NewServeMux()
	protected.HandleFunc("GET /admin/{$}", h.Dashboard)
	protected.HandleFunc("GET /admin/api/servers", h.ServersPartial)
	protected.HandleFunc("GET /admin/api/dashboard/ws", h.DashboardWebSocket)
	protected.HandleFunc("GET /admin/launch", h.LaunchPage)
	protected.HandleFunc("POST /admin/launch", h.LaunchServer)
	protected.HandleFunc("GET /admin/server/{name}", h.ServerDetail)
	protected.HandleFunc("GET /admin/server/{name}/players", h.PlayersPartial)
	protected.HandleFunc("POST /admin/server/{name}/rcon", h.RCONCommand)
	protected.HandleFunc("GET /admin/server/{name}/logs/ws", h.LogsWebSocket)
	protected.HandleFunc("GET /admin/server/{name}/game/ws", h.GameStateWebSocket)
	protected.HandleFunc("GET /admin/server/{name}/killfeed", h.KillfeedPartial)
	protected.HandleFunc("POST /admin/server/{name}/rename", h.RenameServer)
	protected.HandleFunc("POST /admin/server/{name}/restart", h.RestartServer)
	protected.HandleFunc("POST /admin/server/{name}/stop", h.StopServer)

	// Admin tournament routes — list/selector
	protected.HandleFunc("GET /admin/tournament", h.AdminTournament)
	protected.HandleFunc("GET /admin/tournament/{tid}", h.AdminTournamentDetail)
	protected.HandleFunc("POST /admin/tournament/create", h.CreateTournament)

	// Admin tournament routes — scoped by tournament ID
	protected.HandleFunc("POST /admin/tournament/{tid}/update", h.UpdateTournament)
	protected.HandleFunc("POST /admin/tournament/{tid}/delete", h.SoftDeleteTournament)
	protected.HandleFunc("POST /admin/tournament/{tid}/restore", h.RestoreTournament)
	protected.HandleFunc("POST /admin/tournament/{tid}/purge", h.PurgeTournament)
	protected.HandleFunc("POST /admin/tournament/{tid}/status", h.SetTournamentStatus)
	protected.HandleFunc("POST /admin/tournament/{tid}/active", h.SetActiveTournament)
	protected.HandleFunc("POST /admin/tournament/{tid}/teams", h.AdminCreateTeam)
	protected.HandleFunc("POST /admin/tournament/{tid}/teams/{id}/delete", h.AdminDeleteTeam)
	protected.HandleFunc("POST /admin/tournament/{tid}/teams/{id}/members", h.AdminAddMember)
	protected.HandleFunc("POST /admin/tournament/{tid}/teams/{id}/members/{mid}/delete", h.AdminRemoveMember)
	protected.HandleFunc("POST /admin/tournament/{tid}/teams/{id}/rename", h.AdminRenameTeam)
	protected.HandleFunc("POST /admin/tournament/{tid}/bracket/seed", h.AdminSeedBracket)
	protected.HandleFunc("POST /admin/tournament/{tid}/bracket/delete", h.AdminDeleteBracket)

	// Admin match/game routes — work by match/game ID (tournament-scoped via foreign keys)
	protected.HandleFunc("POST /admin/bracket/bestof", h.AdminSetBestOf)
	protected.HandleFunc("POST /admin/bracket/winner", h.AdminSetWinner)
	protected.HandleFunc("POST /admin/bracket/swap", h.AdminSwapTeams)
	protected.HandleFunc("POST /admin/match/{id}/game", h.AdminCreateGame)
	protected.HandleFunc("POST /admin/match/{id}/game/{gid}", h.AdminUpdateGame)
	protected.HandleFunc("POST /admin/match/{id}/game/{gid}/side", h.AdminSetGameSide)
	protected.HandleFunc("POST /admin/match/{id}/game/{gid}/reset", h.AdminResetGame)
	protected.HandleFunc("POST /admin/match/{id}/game/{gid}/delete", h.AdminDeleteGame)
	protected.HandleFunc("GET /admin/match/{id}/launch", h.AdminLaunchMatch)

	mux.Handle("/admin/", a.Middleware(protected))

	return mux
}

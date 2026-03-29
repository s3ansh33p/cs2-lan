package web

import (
	"cs2-panel/internal/auth"
	"io/fs"
	"net/http"

	webfs "cs2-panel/web"
)

func SetupRoutes(a *auth.Auth, h *Handler) http.Handler {
	mux := http.NewServeMux()

	// Static files (embedded)
	staticFS, _ := fs.Sub(webfs.Assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// Public routes
	mux.HandleFunc("GET /login", h.LoginPage)
	mux.HandleFunc("POST /login", a.HandleLogin)
	mux.HandleFunc("POST /logout", a.HandleLogout)

	// Protected routes
	protected := http.NewServeMux()
	protected.HandleFunc("GET /{$}", h.Dashboard)
	protected.HandleFunc("GET /api/servers", h.ServersPartial)
	protected.HandleFunc("GET /launch", h.LaunchPage)
	protected.HandleFunc("POST /launch", h.LaunchServer)
	protected.HandleFunc("GET /server/{name}", h.ServerDetail)
	protected.HandleFunc("GET /server/{name}/players", h.PlayersPartial)
	protected.HandleFunc("POST /server/{name}/rcon", h.RCONCommand)
	protected.HandleFunc("GET /server/{name}/logs/ws", h.LogsWebSocket)
	protected.HandleFunc("POST /server/{name}/stop", h.StopServer)

	mux.Handle("/", a.Middleware(protected))

	return mux
}

// Package cs2web holds HTTP handlers whose behavior is CS2-specific
// (CT/T team assignment, .ready chat protocol, mp_* RCON flow, CS2 demo
// extraction, half-time score model, etc.).
//
// Generic container lifecycle handlers (start/stop/restart), RCON dispatch,
// and log streaming stay in the top-level web package because they work
// the same way regardless of which game runs inside the container.
package cs2web

import (
	"net/http"

	"unilan/internal/db"
	"unilan/internal/docker"
	"unilan/internal/games/cs2/rcon"
	"unilan/internal/games/cs2/tracker"
)

// RenderFunc renders a named template with the given data into w.
// Provided by the top-level web.Handler to avoid exposing its template map.
type RenderFunc func(w http.ResponseWriter, name string, data any)

// Handler holds the dependencies CS2-specific HTTP handlers need. Wired at
// startup from cmd/panel/main.go and passed to web.SetupRoutes for route
// registration.
type Handler struct {
	DB      *db.DB
	Docker  *docker.Client
	Tracker *tracker.Manager
	RCON    *rcon.Manager
	Render  RenderFunc
}

// NewHandler builds the handler. Call SetupReadyHook after construction to
// register the .ready chat callback on the tracker.
func NewHandler(database *db.DB, dc *docker.Client, tm *tracker.Manager, rm *rcon.Manager, render RenderFunc) *Handler {
	return &Handler{DB: database, Docker: dc, Tracker: tm, RCON: rm, Render: render}
}

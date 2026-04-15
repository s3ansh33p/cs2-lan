package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.status = 200
		r.wrote = true
	}
	return r.ResponseWriter.Write(b)
}

// LoggingMiddleware logs every HTTP request with method, path, status, duration, and client IP.
// WebSocket upgrades are skipped — WS handlers log their own connect/disconnect events.
// CSTV broadcast traffic is skipped — CS2 clients fetch dozens of fragment URLs per
// second (start/full/delta plus parallel prefetch), which drowns the log. The relay
// logs signup events of interest (see cstv/relay.go: "cstv start POST").
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip WebSocket upgrades — logged by WS handlers
		if r.Header.Get("Upgrade") == "websocket" {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/cstv/") {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		duration := time.Since(start).Round(time.Millisecond)

		path := r.URL.Path
		if r.URL.RawQuery != "" {
			path = path + "?" + r.URL.RawQuery
		}

		htmx := ""
		if r.Header.Get("HX-Request") == "true" {
			htmx = " [htmx]"
		}

		slog.Info(fmt.Sprintf("http: %s %s %d %s%s", r.Method, path, rec.status, duration, htmx),
			"ip", r.RemoteAddr)
	})
}

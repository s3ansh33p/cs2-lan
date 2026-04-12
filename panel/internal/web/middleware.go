package web

import (
	"fmt"
	"log/slog"
	"net/http"
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
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip WebSocket upgrades — logged by WS handlers
		if r.Header.Get("Upgrade") == "websocket" {
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

		slog.Info(fmt.Sprintf("http: %s %s %d %s", r.Method, path, rec.status, duration),
			"ip", r.RemoteAddr)
	})
}

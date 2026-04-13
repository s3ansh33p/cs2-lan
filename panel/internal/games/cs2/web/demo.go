package cs2web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"unilan/internal/games"
)

// ServeDemo streams a CS2 .dem file to the client for download.
func (h *Handler) ServeDemo(w http.ResponseWriter, r *http.Request) {
	gameID, err := strconv.ParseInt(r.PathValue("gameID"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}

	game, err := h.DB.GetGameByID(gameID)
	if err != nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	if game.DemoPath == "" {
		http.Error(w, "No demo available", http.StatusNotFound)
		return
	}

	info, err := os.Stat(game.DemoPath)
	if err != nil || info.IsDir() {
		http.Error(w, "Demo file not found", http.StatusNotFound)
		return
	}

	filename := filepath.Base(game.DemoPath)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	http.ServeFile(w, r, game.DemoPath)
}

// CopyDemo copies the newest .dem file from a CS2 server container to the local
// demos/ directory, then records its path on the game row. Called in a
// goroutine from the game-over hook — safe to block on network/disk.
func (h *Handler) CopyDemo(gameID int64, serverName, mapName string) {
	ctx := context.Background()
	game := games.Default()
	containerName := game.ContainerPrefix() + serverName
	replayDir := game.DemoPath()

	files, err := h.Docker.ListContainerDir(ctx, containerName, replayDir)
	if err != nil {
		slog.Warn("demo: list replay dir", "server", serverName, "err", err)
		return
	}

	// Filter for .dem files and pick the newest by name (filenames include timestamps).
	var dems []string
	for _, f := range files {
		if strings.HasSuffix(f, ".dem") {
			dems = append(dems, f)
		}
	}
	if len(dems) == 0 {
		slog.Info("demo: no .dem files found", "server", serverName)
		return
	}
	sort.Strings(dems)
	newest := dems[len(dems)-1]

	if err := os.MkdirAll("demos", 0755); err != nil {
		slog.Error("demo: create dir", "err", err)
		return
	}

	srcPath := replayDir + filepath.Base(newest)
	localPath, err := h.Docker.CopyFileFromContainer(ctx, containerName, srcPath, "demos")
	if err != nil {
		slog.Error("demo: copy from container", "server", serverName, "file", newest, "err", err)
		return
	}

	// Rename to a descriptive filename tied to the game ID.
	safeName := strings.ReplaceAll(mapName, "/", "_")
	dstName := fmt.Sprintf("game_%d_%s.dem", gameID, safeName)
	dstPath := filepath.Join("demos", dstName)
	if localPath != dstPath {
		if err := os.Rename(localPath, dstPath); err != nil {
			slog.Error("demo: rename", "from", localPath, "to", dstPath, "err", err)
			dstPath = localPath
		}
	}

	if err := h.DB.UpdateGameDemo(gameID, dstPath); err != nil {
		slog.Error("demo: save path", "game", gameID, "err", err)
		return
	}
	slog.Info("demo: saved", "game", gameID, "path", dstPath)
}

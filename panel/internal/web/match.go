package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"unilan/internal/db"
	"unilan/internal/gametracker"
)

// setupGameOverHook registers callbacks on the tracker for auto-recording
// match results when a game ends on a server linked to a tournament game.
func (h *Handler) setupGameOverHook() {
	h.tracker.OnGameOver(func(info gametracker.GameOverInfo) {
		h.handleGameOver(info)
	})
	h.tracker.OnRoundEnd(func(info gametracker.RoundEndInfo) {
		h.handleRoundEnd(info)
	})
	h.tracker.OnMetadataChange(func(serverName string, m gametracker.TrackerMetadata) {
		h.db.SaveTrackerState(serverName, db.TrackerState{
			GameMode:   m.GameMode,
			CurrentMap: m.CurrentMap,
			HalfRound:  m.HalfRound,
			MaxRounds:  m.MaxRounds,
			CTScore:    m.CTScore,
			TScore:     m.TScore,
			Round:      m.Round,
			InWarmup:   m.InWarmup,
			IsPaused:   m.IsPaused,
		})
	})
}

// mapScores maps CT/T scores to team1/team2 using the stored CT assignment.
// team1StartsCT means team1 was CT in the first half (and T in the second).
// CT and T are the total scores per side (not per team).
func mapScores(ct, t int, team1StartsCT bool) (team1, team2 int) {
	// In competitive CS2, the total score is cumulative across halves.
	// The tracker gives us total CT wins and total T wins.
	// team1's total = their CT-side rounds + their T-side rounds.
	// If team1 starts CT: team1_total = CT score (but CT score includes both halves for CT side)
	// This is actually simpler than it seems: the tracker's CT/T totals ARE per-side totals
	// across the whole match. So if team1 started CT, after halftime they're T.
	// CT total = team1's first-half rounds + team2's second-half rounds
	// T total = team2's first-half rounds + team1's second-half rounds
	// So team1_total = CT + T... no, that's both teams.
	//
	// Actually: CT score = rounds won by whichever team is currently CT.
	// After half, sides swap. So CT score = team1_first_half_rounds + team2_second_half_rounds.
	// We can't decompose from just CT/T totals without half-time info.
	// But for LIVE updates we just need a reasonable mapping.
	// Use: if team1 starts CT, and it's first half, team1=CT. After half, team1=T.
	// For live: we don't know exactly, so just use the total — team1=CT if they started CT.
	// This is approximate for live but Game Over uses round history for accuracy.
	if team1StartsCT {
		return ct, t
	}
	return t, ct
}

func (h *Handler) handleGameOver(info gametracker.GameOverInfo) {
	game, err := h.db.GetGameByServer(info.ServerName)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("game-over: lookup server", "server", info.ServerName, "err", err)
		}
		return
	}

	slog.Info("game-over: recording", "game", game.ID, "server", info.ServerName)

	// Immediately mark as completed so handleRoundEnd stops updating this game
	h.db.Exec(`UPDATE games SET status='completed' WHERE id=?`, game.ID)

	match, err := h.db.GetMatchByID(game.MatchID)
	if err != nil {
		slog.Error("game-over: get match", "match", game.MatchID, "err", err)
		return
	}
	if match.Team1ID == nil || match.Team2ID == nil {
		slog.Warn("game-over: match missing teams", "match", game.MatchID)
		return
	}

	slog.Info("game-over: raw scores", "ct", info.Score.CT, "t", info.Score.T,
		"half_round", info.Score.HalfRound, "rounds", len(info.Score.Rounds), "players", len(info.Players))

	// Calculate half-time splits from round history
	var h1ct, h1t, h2ct, h2t int
	halfRound := info.Score.HalfRound
	for _, r := range info.Score.Rounds {
		if halfRound > 0 && r.Round <= halfRound {
			if r.Winner == "CT" {
				h1ct++
			} else {
				h1t++
			}
		} else {
			if r.Winner == "CT" {
				h2ct++
			} else {
				h2t++
			}
		}
	}

	// Map to team1/team2 using CT assignment
	var team1Score, team2Score int
	if len(info.Score.Rounds) > 0 {
		// Use round history for accurate per-half mapping
		if game.Team1StartsCT {
			team1Score = h1ct + h2t  // team1: CT first half + T second half
			team2Score = h1t + h2ct  // team2: T first half + CT second half
		} else {
			team1Score = h1t + h2ct
			team2Score = h1ct + h2t
		}
	} else {
		// Fallback: use raw CT/T totals (no round history available)
		team1Score, team2Score = mapScores(info.Score.CT, info.Score.T, game.Team1StartsCT)
	}

	// Store half scores + half round
	h.db.UpdateGameHalfScores(game.ID, h1ct, h1t, h2ct, h2t)
	h.db.UpdateGameHalfRound(game.ID, halfRound)

	// Store round-by-round results
	if len(info.Score.Rounds) > 0 {
		var gameRounds []db.GameRound
		for _, r := range info.Score.Rounds {
			gameRounds = append(gameRounds, db.GameRound{
				GameID: game.ID, Round: r.Round, Winner: r.Winner, Reason: r.Reason,
			})
		}
		h.db.SaveGameRounds(game.ID, gameRounds)
	}

	// Determine winner
	var winnerID *int64
	if team1Score > team2Score {
		winnerID = match.Team1ID
	} else if team2Score > team1Score {
		winnerID = match.Team2ID
	}

	slog.Info("game-over: scores mapped", "team1", team1Score, "team2", team2Score, "team1_starts_ct", game.Team1StartsCT)

	if err := h.db.UpdateGameScore(game.ID, team1Score, team2Score, winnerID); err != nil {
		slog.Error("game-over: update score", "game", game.ID, "err", err)
		return
	}

	// Save player stats — map players to teams via steam names, keep unmatched too
	team1Names, team2Names := h.loadTeamNames(match)
	var stats []db.PlayerStat
	for _, p := range info.Players {
		if p.IsBot || !p.Online {
			continue
		}
		nameLower := strings.ToLower(p.Name)
		var teamID int64
		var matched bool
		var originalName string
		if team1Names[nameLower] {
			teamID = *match.Team1ID
			matched = true
		} else if team2Names[nameLower] {
			teamID = *match.Team2ID
			matched = true
		} else {
			// Unmatched: determine team from CS2 side (CT/T) + game.Team1StartsCT
			originalName = p.Name
			isCT := p.Team == "CT"
			if game.Team1StartsCT {
				if isCT {
					teamID = *match.Team1ID
				} else {
					teamID = *match.Team2ID
				}
			} else {
				if isCT {
					teamID = *match.Team2ID
				} else {
					teamID = *match.Team1ID
				}
			}
		}

		hsp, kdr := computeHSPKDR(p.Kills, p.Deaths, p.HeadshotKills, p.HSPercent, p.KDR)

		stats = append(stats, db.PlayerStat{
			GameID: game.ID, TeamID: teamID, PlayerName: p.Name,
			Kills: p.Kills, Deaths: p.Deaths, Assists: p.Assists,
			HSPercent: hsp, KDR: kdr, ADR: p.ADR, MVPs: p.MVPs, EF: p.EF, UD: p.UD,
			OriginalName: originalName, Matched: matched,
		})
	}

	if len(stats) > 0 {
		if err := h.db.SavePlayerStats(game.ID, stats); err != nil {
			slog.Error("game-over: save stats", "game", game.ID, "err", err)
		}
	}

	// Set final map from tracker
	if info.Score.CurrentMap != "" {
		h.db.Exec(`UPDATE games SET map_name=? WHERE id=?`, info.Score.CurrentMap, game.ID)
	}

	if winnerID != nil {
		h.checkMatchDecided(match, *winnerID)
	}

	slog.Info(fmt.Sprintf("game-over: %s %d:%d %s on %s (halves: %d:%d / %d:%d)",
		match.Team1Name, team1Score, team2Score, match.Team2Name,
		info.Score.CurrentMap, h1ct, h1t, h2ct, h2t), "game", game.ID)

	// For Bo3+: if match not decided, auto-create next game on same server
	if match.BestOf > 1 {
		// Refresh match to check if winner was set by checkMatchDecided
		match, _ = h.db.GetMatchByID(match.ID)
		if match != nil && match.WinnerID == nil {
			nextNum := game.GameNumber + 1
			if nextNum <= match.BestOf {
				nextID, err := h.db.CreateGame(match.ID, nextNum, "", game.Team1StartsCT)
				if err == nil {
					h.db.LinkGameToServer(nextID, game.ServerName)
					slog.Info("game-over: auto-created next game", "game", nextID, "number", nextNum, "bestof", match.BestOf, "server", game.ServerName)
				}
			}
		}
	}

	h.notifyBracket()

	// Copy demo file from container in background (can be large)
	if game.ServerName != "" {
		gameID := game.ID
		serverName := game.ServerName
		mapName := info.Score.CurrentMap
		if mapName == "" {
			mapName = game.MapName
		}
		go h.copyDemo(gameID, serverName, mapName)
	}
}

// copyDemo copies the newest .dem file from a CS2 server container to the local demos/ directory.
func (h *Handler) copyDemo(gameID int64, serverName, mapName string) {
	ctx := context.Background()
	containerName := "cs2-" + serverName
	replayDir := "/home/steam/cs2-dedicated/game/csgo/replays/"

	files, err := h.docker.ListContainerDir(ctx, containerName, replayDir)
	if err != nil {
		slog.Warn("demo: list replay dir", "server", serverName, "err", err)
		return
	}

	// Filter for .dem files and pick the newest by name (they include timestamps)
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

	// Ensure demos/ directory exists
	if err := os.MkdirAll("demos", 0755); err != nil {
		slog.Error("demo: create dir", "err", err)
		return
	}

	// Copy from container
	srcPath := replayDir + filepath.Base(newest)
	localPath, err := h.docker.CopyFileFromContainer(ctx, containerName, srcPath, "demos")
	if err != nil {
		slog.Error("demo: copy from container", "server", serverName, "file", newest, "err", err)
		return
	}

	// Rename to descriptive filename
	safeName := strings.ReplaceAll(mapName, "/", "_")
	dstName := fmt.Sprintf("game_%d_%s.dem", gameID, safeName)
	dstPath := filepath.Join("demos", dstName)
	if localPath != dstPath {
		if err := os.Rename(localPath, dstPath); err != nil {
			slog.Error("demo: rename", "from", localPath, "to", dstPath, "err", err)
			dstPath = localPath // fall back to original path
		}
	}

	if err := h.db.UpdateGameDemo(gameID, dstPath); err != nil {
		slog.Error("demo: save path", "game", gameID, "err", err)
		return
	}
	slog.Info("demo: saved", "game", gameID, "path", dstPath)
}

// handleRoundEnd updates live game scores after each round.
func (h *Handler) handleRoundEnd(info gametracker.RoundEndInfo) {
	game, err := h.db.GetGameByServer(info.ServerName)
	if err != nil {
		return
	}

	// Use stored CT assignment for score mapping
	team1Score, team2Score := mapScores(info.CT, info.T, game.Team1StartsCT)

	if err := h.db.UpdateLiveScore(game.ID, team1Score, team2Score); err != nil {
		return
	}

	// Keep map in sync with what the server is currently playing
	if state := h.tracker.GetState(info.ServerName); state != nil {
		sc := state.GetScore()
		if sc.CurrentMap != "" && sc.CurrentMap != game.MapName {
			h.db.Exec(`UPDATE games SET map_name=? WHERE id=?`, sc.CurrentMap, game.ID)
		}
	}

	h.notifyBracket()
}

// loadTeamNames returns lowercase steam name sets for both teams.
func (h *Handler) loadTeamNames(match *db.Match) (team1, team2 map[string]bool) {
	team1 = make(map[string]bool)
	team2 = make(map[string]bool)
	if match.Team1ID != nil {
		members, _ := h.db.ListMembers(*match.Team1ID)
		for _, m := range members {
			team1[strings.ToLower(m.SteamName)] = true
		}
	}
	if match.Team2ID != nil {
		members, _ := h.db.ListMembers(*match.Team2ID)
		for _, m := range members {
			team2[strings.ToLower(m.SteamName)] = true
		}
	}
	return
}

// checkMatchDecided checks if a Bo1/Bo3/Bo5 match is decided and advances the winner.
func (h *Handler) checkMatchDecided(match *db.Match, lastGameWinner int64) {
	games, err := h.db.GetMatchGames(match.ID)
	if err != nil {
		return
	}

	setWinner := func(winnerID int64) {
		if err := h.db.SetMatchWinner(match.ID, winnerID); err != nil {
			slog.Error("game-over: set winner", "match", match.ID, "err", err)
		} else {
			slog.Info("match: decided", "match", match.ID, "winner", winnerID)
		}
		h.updateGroupStandingsIfNeeded(match.ID)
	}

	if match.BestOf == 1 {
		setWinner(lastGameWinner)
		return
	}

	// Bo3/Bo5: need majority wins
	winsNeeded := match.BestOf/2 + 1
	wins := make(map[int64]int)
	for _, g := range games {
		if g.WinnerID != nil {
			wins[*g.WinnerID]++
		}
	}
	for teamID, w := range wins {
		if w >= winsNeeded {
			setWinner(teamID)
			return
		}
	}
}

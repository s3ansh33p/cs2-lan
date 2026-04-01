package web

import (
	"database/sql"
	"errors"
	"log"
	"strings"

	"cs2-panel/internal/db"
	"cs2-panel/internal/gametracker"
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
			log.Printf("game-over: lookup server %s: %v", info.ServerName, err)
		}
		return
	}

	log.Printf("game-over: recording results for game %d (server %s)", game.ID, info.ServerName)

	// Immediately mark as completed so handleRoundEnd stops updating this game
	h.db.Exec(`UPDATE games SET status='completed' WHERE id=?`, game.ID)

	match, err := h.db.GetMatchByID(game.MatchID)
	if err != nil {
		log.Printf("game-over: get match %d: %v", game.MatchID, err)
		return
	}
	if match.Team1ID == nil || match.Team2ID == nil {
		log.Printf("game-over: match %d missing teams", game.MatchID)
		return
	}

	log.Printf("game-over: score CT=%d T=%d, halfRound=%d, rounds=%d, players=%d",
		info.Score.CT, info.Score.T, info.Score.HalfRound, len(info.Score.Rounds), len(info.Players))

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

	// Store half scores
	h.db.UpdateGameHalfScores(game.ID, h1ct, h1t, h2ct, h2t)

	// Determine winner
	var winnerID *int64
	if team1Score > team2Score {
		winnerID = match.Team1ID
	} else if team2Score > team1Score {
		winnerID = match.Team2ID
	}

	log.Printf("game-over: team1=%d team2=%d winner=%v team1StartsCT=%v",
		team1Score, team2Score, winnerID, game.Team1StartsCT)

	if err := h.db.UpdateGameScore(game.ID, team1Score, team2Score, winnerID); err != nil {
		log.Printf("game-over: update score: %v", err)
		return
	}

	// Save player stats — map players to teams via steam names
	team1Names, team2Names := h.loadTeamNames(match)
	var stats []db.PlayerStat
	for _, p := range info.Players {
		if p.IsBot || !p.Online {
			continue
		}
		nameLower := strings.ToLower(p.Name)
		var teamID int64
		if team1Names[nameLower] {
			teamID = *match.Team1ID
		} else if team2Names[nameLower] {
			teamID = *match.Team2ID
		} else {
			continue
		}

		hsp := p.HSPercent
		kdr := p.KDR
		if hsp == 0 && p.Kills > 0 && p.HeadshotKills > 0 {
			hsp = float64(p.HeadshotKills) / float64(p.Kills) * 100
		}
		if kdr == 0 && p.Kills > 0 {
			if p.Deaths > 0 {
				kdr = float64(p.Kills) / float64(p.Deaths)
			} else {
				kdr = float64(p.Kills)
			}
		}

		stats = append(stats, db.PlayerStat{
			GameID: game.ID, TeamID: teamID, PlayerName: p.Name,
			Kills: p.Kills, Deaths: p.Deaths, Assists: p.Assists,
			HSPercent: hsp, KDR: kdr, ADR: p.ADR, MVPs: p.MVPs, EF: p.EF, UD: p.UD,
		})
	}

	if len(stats) > 0 {
		if err := h.db.SavePlayerStats(game.ID, stats); err != nil {
			log.Printf("game-over: save stats: %v", err)
		}
	}

	if winnerID != nil {
		h.checkMatchDecided(match, *winnerID)
	}

	log.Printf("game-over: recorded game %d — %s %d:%d %s (halves: %d:%d / %d:%d)",
		game.ID, match.Team1Name, team1Score, team2Score, match.Team2Name,
		h1ct, h1t, h2ct, h2t)

	h.notifyBracket()
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

// checkMatchDecided checks if a Bo1/Bo3 match is decided and advances the winner.
func (h *Handler) checkMatchDecided(match *db.Match, lastGameWinner int64) {
	games, err := h.db.GetMatchGames(match.ID)
	if err != nil {
		return
	}

	if match.BestOf == 1 {
		if err := h.db.SetMatchWinner(match.ID, lastGameWinner); err != nil {
			log.Printf("game-over: set winner: %v", err)
		}
		return
	}

	// Bo3: need 2 wins
	wins := make(map[int64]int)
	for _, g := range games {
		if g.WinnerID != nil {
			wins[*g.WinnerID]++
		}
	}
	for teamID, w := range wins {
		if w >= 2 {
			if err := h.db.SetMatchWinner(match.ID, teamID); err != nil {
				log.Printf("game-over: set bo3 winner: %v", err)
			}
			return
		}
	}
}

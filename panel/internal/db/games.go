package db

import (
	"fmt"
	"time"
)

// Game status constants.
const (
	GamePending   = "pending"
	GameLive      = "live"
	GameCompleted = "completed"
)

// CS2GameDetail holds CS2-specific per-game fields, stored in the cs2_games table.
// Embedded in Game so callers can access `game.Team1StartsCT` directly.
type CS2GameDetail struct {
	Team1StartsCT bool
	H1CT          int // first half CT round wins
	H1T           int // first half T round wins
	H2CT          int // second half CT round wins
	H2T           int // second half T round wins
	HalfRound     int // round number where half-time occurred
	DemoPath      string
}

type Game struct {
	ID          int64
	MatchID     int64
	GameNumber  int
	MapName     string
	Team1Score  int
	Team2Score  int
	WinnerID    *int64
	ServerName  string
	Status      string // pending, live, completed
	StartedAt   *time.Time
	CompletedAt *time.Time
	CS2GameDetail       // embedded; populated via LEFT JOIN on cs2_games
}

type GameRound struct {
	ID     int64
	GameID int64
	Round  int
	Winner string // "CT" or "T"
	Reason string // "elimination", "bomb", "defuse", "time"
}

type PlayerStat struct {
	ID           int64
	GameID       int64
	TeamID       int64
	PlayerName   string
	Kills        int
	Deaths       int
	Assists      int
	HSPercent    float64
	KDR          float64
	ADR          float64
	MVPs         int
	EF           int
	UD           float64
	OriginalName string
	Matched      bool
}

// gameColumns selects all generic game fields plus CS2 detail via LEFT JOIN.
// COALESCE handles rows without a cs2_games entry (future non-CS2 games).
const gameColumns = `g.id, g.match_id, g.game_number, g.map_name, g.team1_score, g.team2_score,
	g.winner_id, g.server_name, g.status, g.started_at, g.completed_at,
	COALESCE(c.team1_starts_ct, 1), COALESCE(c.h1_ct, 0), COALESCE(c.h1_t, 0),
	COALESCE(c.h2_ct, 0), COALESCE(c.h2_t, 0), COALESCE(c.half_round, 0),
	COALESCE(c.demo_path, '')`

const gameFrom = `FROM games g LEFT JOIN cs2_games c ON c.game_id = g.id`

func scanGame(s interface{ Scan(...any) error }) (Game, error) {
	var g Game
	var startsCT int
	err := s.Scan(&g.ID, &g.MatchID, &g.GameNumber, &g.MapName, &g.Team1Score,
		&g.Team2Score, &g.WinnerID, &g.ServerName, &g.Status, &g.StartedAt, &g.CompletedAt,
		&startsCT, &g.H1CT, &g.H1T, &g.H2CT, &g.H2T, &g.HalfRound, &g.DemoPath)
	g.Team1StartsCT = startsCT != 0
	return g, err
}

func (db *DB) GetMatchGames(matchID int64) ([]Game, error) {
	rows, err := db.Query(`SELECT `+gameColumns+` `+gameFrom+`
		WHERE g.match_id=? ORDER BY g.game_number`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var games []Game
	for rows.Next() {
		g, err := scanGame(rows)
		if err != nil {
			return nil, err
		}
		games = append(games, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return games, nil
}

// UpdateLiveScore updates scores for a live game without completing it.
func (db *DB) UpdateLiveScore(id int64, team1Score, team2Score int) error {
	_, err := db.Exec(`UPDATE games SET team1_score=?, team2_score=? WHERE id=? AND status='live'`,
		team1Score, team2Score, id)
	return err
}

func (db *DB) CreateGame(matchID int64, gameNumber int, mapName string, team1StartsCT bool) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO games (match_id, game_number, map_name) VALUES (?, ?, ?)`,
		matchID, gameNumber, mapName)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	ct := 0
	if team1StartsCT {
		ct = 1
	}
	if _, err := tx.Exec(`INSERT INTO cs2_games (game_id, team1_starts_ct) VALUES (?, ?)`, id, ct); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (db *DB) UpdateGameScore(id int64, team1Score, team2Score int, winnerID *int64) error {
	_, err := db.Exec(`UPDATE games SET team1_score=?, team2_score=?, winner_id=?, status='completed',
		completed_at=CURRENT_TIMESTAMP WHERE id=?`, team1Score, team2Score, winnerID, id)
	return err
}

func (db *DB) UpdateGameHalfScores(id int64, h1ct, h1t, h2ct, h2t int) error {
	_, err := db.Exec(`INSERT INTO cs2_games (game_id, h1_ct, h1_t, h2_ct, h2_t) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(game_id) DO UPDATE SET h1_ct=excluded.h1_ct, h1_t=excluded.h1_t,
		h2_ct=excluded.h2_ct, h2_t=excluded.h2_t`,
		id, h1ct, h1t, h2ct, h2t)
	return err
}

func (db *DB) LinkGameToServer(id int64, serverName string) error {
	_, err := db.Exec(`UPDATE games SET server_name=?, status='live', started_at=CURRENT_TIMESTAMP WHERE id=?`,
		serverName, id)
	return err
}

func (db *DB) GetGameByServer(serverName string) (*Game, error) {
	g, err := scanGame(db.QueryRow(`SELECT `+gameColumns+` `+gameFrom+`
		WHERE g.server_name=? AND g.status='live'`, serverName))
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// GetGameByServerAny finds any game linked to this server (any status), most recent first.
func (db *DB) GetGameByServerAny(serverName string) (*Game, error) {
	g, err := scanGame(db.QueryRow(`SELECT `+gameColumns+` `+gameFrom+`
		WHERE g.server_name=? ORDER BY g.id DESC LIMIT 1`, serverName))
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (db *DB) SavePlayerStats(gameID int64, stats []PlayerStat) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO cs2_game_player_stats
		(game_id, team_id, player_name, kills, deaths, assists, hs_percent, kdr, adr, mvps, ef, ud, original_name, matched)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(game_id, player_name) DO UPDATE SET
		kills=excluded.kills, deaths=excluded.deaths, assists=excluded.assists,
		hs_percent=excluded.hs_percent, kdr=excluded.kdr, adr=excluded.adr,
		mvps=excluded.mvps, ef=excluded.ef, ud=excluded.ud,
		original_name=excluded.original_name, matched=excluded.matched`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, s := range stats {
		matched := 0
		if s.Matched {
			matched = 1
		}
		if _, err := stmt.Exec(gameID, s.TeamID, s.PlayerName, s.Kills, s.Deaths, s.Assists,
			s.HSPercent, s.KDR, s.ADR, s.MVPs, s.EF, s.UD, s.OriginalName, matched); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) GetGameStats(gameID int64) ([]PlayerStat, error) {
	rows, err := db.Query(`SELECT id, game_id, team_id, player_name, kills, deaths, assists,
		hs_percent, kdr, adr, mvps, ef, ud, original_name, matched
		FROM cs2_game_player_stats WHERE game_id=? AND matched=1 ORDER BY kills DESC`, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []PlayerStat
	for rows.Next() {
		var s PlayerStat
		var matched int
		if err := rows.Scan(&s.ID, &s.GameID, &s.TeamID, &s.PlayerName, &s.Kills, &s.Deaths,
			&s.Assists, &s.HSPercent, &s.KDR, &s.ADR, &s.MVPs, &s.EF, &s.UD,
			&s.OriginalName, &matched); err != nil {
			return nil, err
		}
		s.Matched = matched != 0
		stats = append(stats, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}

// GetGameStatsAdmin returns ALL players (matched + unmatched), sorted by matched desc, kills desc.
func (db *DB) GetGameStatsAdmin(gameID int64) ([]PlayerStat, error) {
	rows, err := db.Query(`SELECT id, game_id, team_id, player_name, kills, deaths, assists,
		hs_percent, kdr, adr, mvps, ef, ud, original_name, matched
		FROM cs2_game_player_stats WHERE game_id=? ORDER BY matched DESC, kills DESC`, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []PlayerStat
	for rows.Next() {
		var s PlayerStat
		var matched int
		if err := rows.Scan(&s.ID, &s.GameID, &s.TeamID, &s.PlayerName, &s.Kills, &s.Deaths,
			&s.Assists, &s.HSPercent, &s.KDR, &s.ADR, &s.MVPs, &s.EF, &s.UD,
			&s.OriginalName, &matched); err != nil {
			return nil, err
		}
		s.Matched = matched != 0
		stats = append(stats, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}

// RemapPlayerStat reassigns an unmatched player to a roster name.
// It deletes any existing row for the target name (to avoid UNIQUE conflict),
// then updates the unmatched row's player_name, team_id, and sets matched=1.
func (db *DB) RemapPlayerStat(gameID int64, originalName string, targetName string, teamID int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Remove any existing row for the target name in this game (conflict prevention)
	if _, err := tx.Exec(`DELETE FROM cs2_game_player_stats WHERE game_id=? AND player_name=? AND player_name!=?`,
		gameID, targetName, originalName); err != nil {
		return fmt.Errorf("delete existing: %w", err)
	}

	// Update the unmatched row
	res, err := tx.Exec(`UPDATE cs2_game_player_stats SET player_name=?, team_id=?, matched=1
		WHERE game_id=? AND original_name=? AND matched=0`,
		targetName, teamID, gameID, originalName)
	if err != nil {
		return fmt.Errorf("remap: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("no unmatched player found with original_name=%q", originalName)
	}

	return tx.Commit()
}

func (db *DB) SaveGameRounds(gameID int64, rounds []GameRound) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO cs2_game_rounds (game_id, round, winner, reason) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range rounds {
		if _, err := stmt.Exec(gameID, r.Round, r.Winner, r.Reason); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) GetGameRounds(gameID int64) ([]GameRound, error) {
	rows, err := db.Query(`SELECT id, game_id, round, winner, reason FROM cs2_game_rounds WHERE game_id=? ORDER BY round`, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rounds []GameRound
	for rows.Next() {
		var r GameRound
		if err := rows.Scan(&r.ID, &r.GameID, &r.Round, &r.Winner, &r.Reason); err != nil {
			return nil, err
		}
		rounds = append(rounds, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rounds, nil
}

func (db *DB) UpdateGameHalfRound(id int64, halfRound int) error {
	_, err := db.Exec(`INSERT INTO cs2_games (game_id, half_round) VALUES (?, ?)
		ON CONFLICT(game_id) DO UPDATE SET half_round=excluded.half_round`,
		id, halfRound)
	return err
}

// DeleteGame removes a game and its associated rounds/stats.
func (db *DB) DeleteGame(gameID int64) error {
	if _, err := db.Exec(`DELETE FROM cs2_game_rounds WHERE game_id=?`, gameID); err != nil {
		return fmt.Errorf("delete rounds: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM cs2_game_player_stats WHERE game_id=?`, gameID); err != nil {
		return fmt.Errorf("delete stats: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM games WHERE id=?`, gameID); err != nil {
		return fmt.Errorf("delete game: %w", err)
	}
	return nil
}

// UpdateGameDemo sets the demo file path for a game.
func (db *DB) UpdateGameDemo(gameID int64, demoPath string) error {
	_, err := db.Exec(`INSERT INTO cs2_games (game_id, demo_path) VALUES (?, ?)
		ON CONFLICT(game_id) DO UPDATE SET demo_path=excluded.demo_path`,
		gameID, demoPath)
	return err
}

// GetGameByID returns a single game by ID.
func (db *DB) GetGameByID(gameID int64) (*Game, error) {
	g, err := scanGame(db.QueryRow(`SELECT `+gameColumns+` `+gameFrom+` WHERE g.id=?`, gameID))
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// ResetGame clears a game's results and cascades: deletes rounds/stats,
// undoes match winner if set, and removes pending Bo3 follow-up games.
func (db *DB) ResetGame(gameID int64) error {
	// Get game info
	game, err := scanGame(db.QueryRow(`SELECT `+gameColumns+` `+gameFrom+` WHERE g.id=?`, gameID))
	if err != nil {
		return err
	}

	// Reset the game record
	_, err = db.Exec(`UPDATE games SET team1_score=0, team2_score=0, winner_id=NULL,
		status='pending', server_name='', started_at=NULL, completed_at=NULL WHERE id=?`, gameID)
	if err != nil {
		return err
	}
	// Reset CS2-specific half scores and half-round marker
	db.Exec(`UPDATE cs2_games SET h1_ct=0, h1_t=0, h2_ct=0, h2_t=0, half_round=0 WHERE game_id=?`, gameID)

	// Delete associated rounds and stats
	if _, err := db.Exec(`DELETE FROM cs2_game_rounds WHERE game_id=?`, gameID); err != nil {
		return fmt.Errorf("delete rounds: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM cs2_game_player_stats WHERE game_id=?`, gameID); err != nil {
		return fmt.Errorf("delete stats: %w", err)
	}

	// Check if match winner needs to be undone
	var matchWinnerID *int64
	if err := db.QueryRow(`SELECT winner_id FROM matches WHERE id=?`, game.MatchID).Scan(&matchWinnerID); err == nil && matchWinnerID != nil {
		if err := db.ClearMatchWinner(game.MatchID); err != nil {
			return fmt.Errorf("clear match winner: %w", err)
		}
	}

	// Delete pending follow-up games in Bo3 (auto-created games after this one)
	if _, err := db.Exec(`DELETE FROM games WHERE match_id=? AND game_number>? AND status='pending'`,
		game.MatchID, game.GameNumber); err != nil {
		return fmt.Errorf("delete follow-up games: %w", err)
	}

	return nil
}

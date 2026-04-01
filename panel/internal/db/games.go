package db

import "time"

type Game struct {
	ID            int64
	MatchID       int64
	GameNumber    int
	MapName       string
	Team1Score    int
	Team2Score    int
	WinnerID      *int64
	ServerName    string
	Status        string // pending, live, completed
	Team1StartsCT bool
	H1CT          int // first half CT round wins
	H1T           int // first half T round wins
	H2CT          int // second half CT round wins
	H2T           int // second half T round wins
	HalfRound     int // round number where half-time occurred
	StartedAt     *time.Time
	CompletedAt   *time.Time
}

type GameRound struct {
	ID     int64
	GameID int64
	Round  int
	Winner string // "CT" or "T"
	Reason string // "elimination", "bomb", "defuse", "time"
}

type PlayerStat struct {
	ID         int64
	GameID     int64
	TeamID     int64
	PlayerName string
	Kills      int
	Deaths     int
	Assists    int
	HSPercent  float64
	KDR        float64
	ADR        float64
	MVPs       int
	EF         int
	UD         float64
}

const gameColumns = `id, match_id, game_number, map_name, team1_score, team2_score,
	winner_id, server_name, status, team1_starts_ct, h1_ct, h1_t, h2_ct, h2_t, half_round, started_at, completed_at`

func scanGame(s interface{ Scan(...any) error }) (Game, error) {
	var g Game
	var startsCT int
	err := s.Scan(&g.ID, &g.MatchID, &g.GameNumber, &g.MapName, &g.Team1Score,
		&g.Team2Score, &g.WinnerID, &g.ServerName, &g.Status,
		&startsCT, &g.H1CT, &g.H1T, &g.H2CT, &g.H2T, &g.HalfRound, &g.StartedAt, &g.CompletedAt)
	g.Team1StartsCT = startsCT != 0
	return g, err
}

func (db *DB) GetMatchGames(matchID int64) ([]Game, error) {
	rows, err := db.Query(`SELECT `+gameColumns+`
		FROM games WHERE match_id=? ORDER BY game_number`, matchID)
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
	return games, nil
}

// UpdateLiveScore updates scores for a live game without completing it.
func (db *DB) UpdateLiveScore(id int64, team1Score, team2Score int) error {
	_, err := db.Exec(`UPDATE games SET team1_score=?, team2_score=? WHERE id=? AND status='live'`,
		team1Score, team2Score, id)
	return err
}

func (db *DB) CreateGame(matchID int64, gameNumber int, mapName string, team1StartsCT bool) (int64, error) {
	ct := 0
	if team1StartsCT {
		ct = 1
	}
	res, err := db.Exec(`INSERT INTO games (match_id, game_number, map_name, team1_starts_ct) VALUES (?, ?, ?, ?)`,
		matchID, gameNumber, mapName, ct)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) UpdateGameScore(id int64, team1Score, team2Score int, winnerID *int64) error {
	_, err := db.Exec(`UPDATE games SET team1_score=?, team2_score=?, winner_id=?, status='completed',
		completed_at=CURRENT_TIMESTAMP WHERE id=?`, team1Score, team2Score, winnerID, id)
	return err
}

func (db *DB) UpdateGameHalfScores(id int64, h1ct, h1t, h2ct, h2t int) error {
	_, err := db.Exec(`UPDATE games SET h1_ct=?, h1_t=?, h2_ct=?, h2_t=? WHERE id=?`,
		h1ct, h1t, h2ct, h2t, id)
	return err
}

func (db *DB) LinkGameToServer(id int64, serverName string) error {
	_, err := db.Exec(`UPDATE games SET server_name=?, status='live', started_at=CURRENT_TIMESTAMP WHERE id=?`,
		serverName, id)
	return err
}

func (db *DB) GetGameByServer(serverName string) (*Game, error) {
	g, err := scanGame(db.QueryRow(`SELECT `+gameColumns+`
		FROM games WHERE server_name=? AND status='live'`, serverName))
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (db *DB) SavePlayerStats(gameID int64, stats []PlayerStat) error {
	for _, s := range stats {
		_, err := db.Exec(`INSERT INTO game_player_stats
			(game_id, team_id, player_name, kills, deaths, assists, hs_percent, kdr, adr, mvps, ef, ud)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(game_id, player_name) DO UPDATE SET
			kills=?, deaths=?, assists=?, hs_percent=?, kdr=?, adr=?, mvps=?, ef=?, ud=?`,
			gameID, s.TeamID, s.PlayerName, s.Kills, s.Deaths, s.Assists, s.HSPercent, s.KDR, s.ADR, s.MVPs, s.EF, s.UD,
			s.Kills, s.Deaths, s.Assists, s.HSPercent, s.KDR, s.ADR, s.MVPs, s.EF, s.UD)
		if err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) GetGameStats(gameID int64) ([]PlayerStat, error) {
	rows, err := db.Query(`SELECT id, game_id, team_id, player_name, kills, deaths, assists,
		hs_percent, kdr, adr, mvps, ef, ud
		FROM game_player_stats WHERE game_id=? ORDER BY kills DESC`, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []PlayerStat
	for rows.Next() {
		var s PlayerStat
		if err := rows.Scan(&s.ID, &s.GameID, &s.TeamID, &s.PlayerName, &s.Kills, &s.Deaths,
			&s.Assists, &s.HSPercent, &s.KDR, &s.ADR, &s.MVPs, &s.EF, &s.UD); err != nil {
			return nil, err
		}
		stats = append(stats, s)
	}
	return stats, nil
}

func (db *DB) SaveGameRounds(gameID int64, rounds []GameRound) error {
	for _, r := range rounds {
		_, err := db.Exec(`INSERT INTO game_rounds (game_id, round, winner, reason) VALUES (?, ?, ?, ?)`,
			gameID, r.Round, r.Winner, r.Reason)
		if err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) GetGameRounds(gameID int64) ([]GameRound, error) {
	rows, err := db.Query(`SELECT id, game_id, round, winner, reason FROM game_rounds WHERE game_id=? ORDER BY round`, gameID)
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
	return rounds, nil
}

func (db *DB) UpdateGameHalfRound(id int64, halfRound int) error {
	_, err := db.Exec(`UPDATE games SET half_round=? WHERE id=?`, halfRound, id)
	return err
}

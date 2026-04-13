package db

import "time"

type ReadyState struct {
	ID         int64
	GameID     int64
	ServerName string
	Status     string // waiting, countdown, ready, started, force_started
	CreatedAt  time.Time
}

type PlayerReady struct {
	ID           int64
	ReadyStateID int64
	PlayerName   string
	Team         string // CT or T
	IsReady      bool
}

func (db *DB) CreateReadyState(gameID int64, serverName string) (*ReadyState, error) {
	// Delete any existing ready state for this game first
	db.Exec(`DELETE FROM cs2_ready_state WHERE game_id=?`, gameID)

	res, err := db.Exec(`INSERT INTO cs2_ready_state (game_id, server_name, status) VALUES (?, ?, 'waiting')`,
		gameID, serverName)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &ReadyState{
		ID:         id,
		GameID:     gameID,
		ServerName: serverName,
		Status:     "waiting",
		CreatedAt:  time.Now(),
	}, nil
}

func (db *DB) GetReadyState(gameID int64) (*ReadyState, error) {
	var rs ReadyState
	err := db.QueryRow(`SELECT id, game_id, server_name, status, created_at
		FROM cs2_ready_state WHERE game_id=?`, gameID).
		Scan(&rs.ID, &rs.GameID, &rs.ServerName, &rs.Status, &rs.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &rs, nil
}

func (db *DB) GetReadyStateByServer(serverName string) (*ReadyState, error) {
	var rs ReadyState
	err := db.QueryRow(`SELECT id, game_id, server_name, status, created_at
		FROM cs2_ready_state WHERE server_name=? AND status IN ('waiting','countdown','ready')
		ORDER BY id DESC LIMIT 1`, serverName).
		Scan(&rs.ID, &rs.GameID, &rs.ServerName, &rs.Status, &rs.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &rs, nil
}

func (db *DB) UpdateReadyStatus(readyStateID int64, status string) error {
	_, err := db.Exec(`UPDATE cs2_ready_state SET status=? WHERE id=?`, status, readyStateID)
	return err
}

func (db *DB) SetPlayerReady(readyStateID int64, playerName, team string) error {
	_, err := db.Exec(`INSERT INTO cs2_player_ready (ready_state_id, player_name, team, is_ready)
		VALUES (?, ?, ?, 1)
		ON CONFLICT(ready_state_id, player_name) DO UPDATE SET is_ready=1, team=excluded.team`,
		readyStateID, playerName, team)
	return err
}

func (db *DB) GetReadyPlayers(readyStateID int64) ([]PlayerReady, error) {
	rows, err := db.Query(`SELECT id, ready_state_id, player_name, team, is_ready
		FROM cs2_player_ready WHERE ready_state_id=?`, readyStateID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var players []PlayerReady
	for rows.Next() {
		var p PlayerReady
		var ready int
		if err := rows.Scan(&p.ID, &p.ReadyStateID, &p.PlayerName, &p.Team, &ready); err != nil {
			return nil, err
		}
		p.IsReady = ready != 0
		players = append(players, p)
	}
	return players, rows.Err()
}

func (db *DB) DeleteReadyState(gameID int64) error {
	// Cascade deletes cs2_player_ready rows via foreign key
	_, err := db.Exec(`DELETE FROM cs2_ready_state WHERE game_id=?`, gameID)
	return err
}

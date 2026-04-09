package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

type Tournament struct {
	ID                int64
	Name              string
	TeamSize          int
	GameMode          string // competitive, wingman, casual, etc.
	RegistrationOpen  *time.Time
	RegistrationClose *time.Time
	ServerIP          string
	ServerPassword    string
	Status            string // draft, registration, locked, active, completed
	CreatedAt         time.Time
	DeletedAt         *time.Time
	HiddenAt          *time.Time
}

// CanRegister returns true if the tournament is accepting team registrations.
func (t *Tournament) CanRegister() bool {
	if t.Status != "registration" {
		return false
	}
	now := time.Now()
	if t.RegistrationOpen != nil && now.Before(*t.RegistrationOpen) {
		return false
	}
	if t.RegistrationClose != nil && now.After(*t.RegistrationClose) {
		return false
	}
	return true
}

const tournamentColumns = `id, name, team_size, game_mode, registration_open, registration_close,
	server_ip, server_password, status, created_at, deleted_at, hidden_at`

func scanTournament(row interface{ Scan(...any) error }) (*Tournament, error) {
	t := &Tournament{}
	err := row.Scan(&t.ID, &t.Name, &t.TeamSize, &t.GameMode, &t.RegistrationOpen, &t.RegistrationClose,
		&t.ServerIP, &t.ServerPassword, &t.Status, &t.CreatedAt, &t.DeletedAt, &t.HiddenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (db *DB) GetTournamentByID(id int64) (*Tournament, error) {
	return scanTournament(db.QueryRow(`SELECT `+tournamentColumns+` FROM tournament WHERE id=?`, id))
}

func (db *DB) GetActiveTournament() (*Tournament, error) {
	id, err := db.GetActiveTournamentID()
	if err != nil || id == 0 {
		// Fallback: if no active tournament set, return the most recent non-deleted, non-hidden
		return scanTournament(db.QueryRow(`SELECT ` + tournamentColumns + ` FROM tournament WHERE deleted_at IS NULL AND hidden_at IS NULL ORDER BY id DESC LIMIT 1`))
	}
	return db.GetTournamentByID(id)
}

func (db *DB) ListTournaments() ([]Tournament, error) {
	rows, err := db.Query(`SELECT `+tournamentColumns+` FROM tournament WHERE deleted_at IS NULL AND hidden_at IS NULL ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Tournament
	for rows.Next() {
		t, err := scanTournament(rows)
		if err != nil {
			return nil, err
		}
		if t != nil {
			result = append(result, *t)
		}
	}
	return result, rows.Err()
}

func (db *DB) ListDeletedTournaments() ([]Tournament, error) {
	rows, err := db.Query(`SELECT `+tournamentColumns+` FROM tournament WHERE deleted_at IS NOT NULL ORDER BY deleted_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Tournament
	for rows.Next() {
		t, err := scanTournament(rows)
		if err != nil {
			return nil, err
		}
		if t != nil {
			result = append(result, *t)
		}
	}
	return result, rows.Err()
}

func (db *DB) ListHiddenTournaments() ([]Tournament, error) {
	rows, err := db.Query(`SELECT `+tournamentColumns+` FROM tournament WHERE deleted_at IS NULL AND hidden_at IS NOT NULL ORDER BY hidden_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Tournament
	for rows.Next() {
		t, err := scanTournament(rows)
		if err != nil {
			return nil, err
		}
		if t != nil {
			result = append(result, *t)
		}
	}
	return result, rows.Err()
}

func (db *DB) HideTournament(id int64) error {
	_, err := db.Exec(`UPDATE tournament SET hidden_at=CURRENT_TIMESTAMP WHERE id=?`, id)
	if err != nil {
		return err
	}
	activeID, _ := db.GetActiveTournamentID()
	if activeID == id {
		db.SetActiveTournament(0)
	}
	return nil
}

func (db *DB) UnhideTournament(id int64) error {
	_, err := db.Exec(`UPDATE tournament SET hidden_at=NULL WHERE id=?`, id)
	return err
}

func (db *DB) CreateTournament(name string, teamSize int, gameMode, serverIP, serverPassword string) (*Tournament, error) {
	if gameMode == "" {
		gameMode = "competitive"
	}

	res, err := db.Exec(`INSERT INTO tournament (name, team_size, game_mode, server_ip, server_password)
		VALUES (?, ?, ?, ?, ?)`, name, teamSize, gameMode, serverIP, serverPassword)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last insert id: %w", err)
	}
	return &Tournament{ID: id, Name: name, TeamSize: teamSize, GameMode: gameMode, ServerIP: serverIP, ServerPassword: serverPassword, Status: "draft"}, nil
}

func (db *DB) UpdateTournament(id int64, name string, teamSize int, gameMode string, regOpen, regClose *time.Time, serverIP, serverPassword string) error {
	_, err := db.Exec(`UPDATE tournament SET name=?, team_size=?, game_mode=?, registration_open=?,
		registration_close=?, server_ip=?, server_password=? WHERE id=?`,
		name, teamSize, gameMode, regOpen, regClose, serverIP, serverPassword, id)
	return err
}

func (db *DB) SetTournamentStatus(id int64, status string) error {
	_, err := db.Exec(`UPDATE tournament SET status=? WHERE id=?`, status, id)
	return err
}

func (db *DB) DeleteTournament(id int64) error {
	_, err := db.Exec(`DELETE FROM tournament WHERE id=?`, id)
	return err
}

func (db *DB) SoftDeleteTournament(id int64) error {
	_, err := db.Exec(`UPDATE tournament SET deleted_at=CURRENT_TIMESTAMP WHERE id=?`, id)
	if err != nil {
		return err
	}
	// If this was the active tournament, clear active
	activeID, _ := db.GetActiveTournamentID()
	if activeID == id {
		db.SetActiveTournament(0)
	}
	return nil
}

func (db *DB) RestoreTournament(id int64) error {
	_, err := db.Exec(`UPDATE tournament SET deleted_at=NULL WHERE id=?`, id)
	return err
}

func (db *DB) PurgeTournament(id int64) error {
	_, err := db.Exec(`DELETE FROM tournament WHERE id=? AND deleted_at IS NOT NULL`, id)
	return err
}

// --- Settings ---

func (db *DB) GetSetting(key string) string {
	var val string
	db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&val)
	return val
}

func (db *DB) SetSetting(key, value string) error {
	_, err := db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (db *DB) GetActiveTournamentID() (int64, error) {
	var val string
	err := db.QueryRow(`SELECT value FROM settings WHERE key='active_tournament_id'`).Scan(&val)
	if err != nil || val == "" {
		return 0, err
	}
	id, err := strconv.ParseInt(val, 10, 64)
	return id, err
}

func (db *DB) SetActiveTournament(id int64) error {
	val := ""
	if id > 0 {
		val = strconv.FormatInt(id, 10)
	}
	_, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('active_tournament_id', ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, val)
	return err
}

// GetTournamentByMatchID resolves the tournament for a given match ID.
func (db *DB) GetTournamentByMatchID(matchID int64) (*Tournament, error) {
	var tid int64
	err := db.QueryRow(`SELECT tournament_id FROM matches WHERE id=?`, matchID).Scan(&tid)
	if err != nil {
		return nil, err
	}
	return db.GetTournamentByID(tid)
}

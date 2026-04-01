package db

import (
	"database/sql"
	"errors"
	"fmt"
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

func (db *DB) GetTournament() (*Tournament, error) {
	row := db.QueryRow(`SELECT id, name, team_size, game_mode, registration_open, registration_close,
		server_ip, server_password, status, created_at FROM tournament LIMIT 1`)

	t := &Tournament{}
	err := row.Scan(&t.ID, &t.Name, &t.TeamSize, &t.GameMode, &t.RegistrationOpen, &t.RegistrationClose,
		&t.ServerIP, &t.ServerPassword, &t.Status, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (db *DB) CreateTournament(name string, teamSize int, gameMode, serverIP, serverPassword string) (*Tournament, error) {
	// Only one tournament at a time — delete any existing
	if _, err := db.Exec(`DELETE FROM tournament`); err != nil {
		return nil, err
	}

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

package db

import (
	"strings"
	"time"
)

type Team struct {
	ID           int64
	TournamentID int64
	Name         string
	Seed         *int
	Members      []TeamMember
	CreatedAt    time.Time
}

type TeamMember struct {
	ID        int64
	TeamID    int64
	SteamName string
}

func (db *DB) ListTeams(tournamentID int64) ([]Team, error) {
	rows, err := db.Query(`SELECT id, tournament_id, name, seed, created_at
		FROM teams WHERE tournament_id=? ORDER BY COALESCE(seed, 999999), name`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var teams []Team
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.TournamentID, &t.Name, &t.Seed, &t.CreatedAt); err != nil {
			return nil, err
		}
		teams = append(teams, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Batch-load members for all teams
	if len(teams) > 0 {
		teamIDs := make([]any, len(teams))
		placeholders := make([]string, len(teams))
		teamIdx := make(map[int64]int)
		for i, t := range teams {
			teamIDs[i] = t.ID
			placeholders[i] = "?"
			teamIdx[t.ID] = i
		}
		mRows, err := db.Query(`SELECT id, team_id, steam_name FROM team_members WHERE team_id IN (`+strings.Join(placeholders, ",")+`)`, teamIDs...)
		if err != nil {
			return nil, err
		}
		defer mRows.Close()
		for mRows.Next() {
			var m TeamMember
			if err := mRows.Scan(&m.ID, &m.TeamID, &m.SteamName); err != nil {
				return nil, err
			}
			if idx, ok := teamIdx[m.TeamID]; ok {
				teams[idx].Members = append(teams[idx].Members, m)
			}
		}
		if err := mRows.Err(); err != nil {
			return nil, err
		}
	}
	return teams, nil
}

func (db *DB) GetTeam(id int64) (*Team, error) {
	t := &Team{}
	err := db.QueryRow(`SELECT id, tournament_id, name, seed, created_at FROM teams WHERE id=?`, id).
		Scan(&t.ID, &t.TournamentID, &t.Name, &t.Seed, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	t.Members, err = db.ListMembers(t.ID)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (db *DB) CreateTeam(tournamentID int64, name string) (int64, error) {
	res, err := db.Exec(`INSERT INTO teams (tournament_id, name) VALUES (?, ?)`, tournamentID, name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) UpdateTeam(id int64, name string) error {
	_, err := db.Exec(`UPDATE teams SET name=? WHERE id=?`, name, id)
	return err
}

func (db *DB) DeleteTeam(id int64) error {
	_, err := db.Exec(`DELETE FROM teams WHERE id=?`, id)
	return err
}

func (db *DB) SetTeamSeed(id int64, seed int) error {
	_, err := db.Exec(`UPDATE teams SET seed=? WHERE id=?`, seed, id)
	return err
}

func (db *DB) ListMembers(teamID int64) ([]TeamMember, error) {
	rows, err := db.Query(`SELECT id, team_id, steam_name FROM team_members WHERE team_id=?`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []TeamMember
	for rows.Next() {
		var m TeamMember
		if err := rows.Scan(&m.ID, &m.TeamID, &m.SteamName); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return members, nil
}

func (db *DB) AddMember(teamID int64, steamName string) (int64, error) {
	res, err := db.Exec(`INSERT INTO team_members (team_id, steam_name) VALUES (?, ?)`, teamID, steamName)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) RemoveMember(id int64) error {
	_, err := db.Exec(`DELETE FROM team_members WHERE id=?`, id)
	return err
}

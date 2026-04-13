package db

import "strings"

// MapVeto represents a single step in a map veto process.
type MapVeto struct {
	ID       int64
	MatchID  int64
	Step     int
	Action   string // "ban", "pick", "last"
	TeamID   *int64
	TeamName string
	MapName  string
}

// GetMatchVetoes returns all veto steps for a match, ordered by step.
func (db *DB) GetMatchVetoes(matchID int64) ([]MapVeto, error) {
	rows, err := db.Query(`SELECT v.id, v.match_id, v.step, v.action, v.team_id, COALESCE(t.name, ''), v.map_name
		FROM map_vetoes v LEFT JOIN teams t ON v.team_id = t.id
		WHERE v.match_id=? ORDER BY v.step`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vetoes []MapVeto
	for rows.Next() {
		var v MapVeto
		if err := rows.Scan(&v.ID, &v.MatchID, &v.Step, &v.Action, &v.TeamID, &v.TeamName, &v.MapName); err != nil {
			return nil, err
		}
		vetoes = append(vetoes, v)
	}
	return vetoes, rows.Err()
}

// AddVetoStep inserts a single veto step.
func (db *DB) AddVetoStep(matchID int64, step int, action, mapName string, teamID *int64) error {
	_, err := db.Exec(`INSERT INTO map_vetoes (match_id, step, action, team_id, map_name)
		VALUES (?, ?, ?, ?, ?)`, matchID, step, action, teamID, mapName)
	return err
}

// ClearVetoes deletes all veto steps for a match.
func (db *DB) ClearVetoes(matchID int64) error {
	_, err := db.Exec(`DELETE FROM map_vetoes WHERE match_id=?`, matchID)
	return err
}

// ParseVetoFormat parses a comma-separated veto format string into a slice of
// actions. If the input is empty or yields no valid actions, returns
// defaultFormat. Game-specific defaults live in the games package
// (e.g. games.Get("cs2").DefaultVetoFormat()).
func ParseVetoFormat(format string, defaultFormat []string) []string {
	format = strings.TrimSpace(format)
	if format == "" {
		return defaultFormat
	}
	parts := strings.Split(format, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "ban" || p == "pick" || p == "last" {
			result = append(result, p)
		}
	}
	if len(result) == 0 {
		return defaultFormat
	}
	return result
}

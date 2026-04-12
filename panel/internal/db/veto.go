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

// MapPool returns the map pool for a given game mode.
func MapPool(gameMode string) []string {
	switch gameMode {
	case "wingman":
		return []string{"de_inferno", "de_nuke", "de_overpass", "de_vertigo", "de_shortdust", "de_lake"}
	default: // competitive
		return []string{"de_ancient", "de_anubis", "de_dust2", "de_inferno", "de_mirage", "de_nuke", "de_vertigo"}
	}
}

// ParseVetoFormat parses a comma-separated veto format string into a slice of actions.
// Returns a default Bo3 format if the input is empty.
func ParseVetoFormat(format string) []string {
	format = strings.TrimSpace(format)
	if format == "" {
		return []string{"ban", "ban", "pick", "pick", "ban", "ban", "last"}
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
		return []string{"ban", "ban", "pick", "pick", "ban", "ban", "last"}
	}
	return result
}

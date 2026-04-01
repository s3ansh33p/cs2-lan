package db

import (
	"fmt"
	"math"
)

type Match struct {
	ID           int64
	TournamentID int64
	Round        int
	Position     int
	BestOf       int
	Team1ID      *int64
	Team2ID      *int64
	WinnerID     *int64
	NextMatchID  *int64
	IsBye        bool

	// Populated by GetBracket
	Team1Name string
	Team2Name string
	WinnerName string
	Games     []Game
}

// GenerateBracket creates a single-elimination bracket from an ordered list of team IDs.
// The order determines seeding (index 0 = seed 1).
func (db *DB) GenerateBracket(tournamentID int64, teamIDs []int64) error {
	// Clear existing bracket
	if _, err := db.Exec(`DELETE FROM matches WHERE tournament_id=?`, tournamentID); err != nil {
		return err
	}

	n := len(teamIDs)
	if n < 2 {
		return fmt.Errorf("need at least 2 teams, got %d", n)
	}

	// Next power of 2
	p := 1
	for p < n {
		p *= 2
	}
	numRounds := int(math.Log2(float64(p)))
	numByes := p - n

	// Build seeding order: 1vP, 2v(P-1), etc.
	// For a bracket of size P, standard seeding places teams so that
	// seed 1 plays seed P, seed 2 plays seed P-1, etc.
	type slot struct {
		teamID *int64
	}
	slots := make([]slot, p)
	for i, id := range teamIDs {
		id := id
		slots[i] = slot{teamID: &id}
	}
	// Remaining slots are nil (byes)

	// Standard bracket ordering for seed positions
	order := bracketOrder(p)

	// Create all matches bottom-up
	// Round 1 has p/2 matches, round 2 has p/4, etc.
	matchIDs := make(map[string]int64) // "round-position" -> match ID

	// Create matches from final round down to first (so we have next_match_id)
	for round := numRounds; round >= 1; round-- {
		matchesInRound := p / int(math.Pow(2, float64(round)))
		for pos := 0; pos < matchesInRound; pos++ {
			var nextMatchID *int64
			if round < numRounds {
				nid := matchIDs[fmt.Sprintf("%d-%d", round+1, pos/2)]
				nextMatchID = &nid
			}

			res, err := db.Exec(`INSERT INTO matches (tournament_id, round, position, next_match_id)
				VALUES (?, ?, ?, ?)`, tournamentID, round, pos, nextMatchID)
			if err != nil {
				return fmt.Errorf("create match r%d p%d: %w", round, pos, err)
			}
			id, _ := res.LastInsertId()
			matchIDs[fmt.Sprintf("%d-%d", round, pos)] = id
		}
	}

	// Place teams into first-round matches using seeding order
	firstRoundMatches := p / 2
	for i := 0; i < firstRoundMatches; i++ {
		s1 := order[i*2]
		s2 := order[i*2+1]

		matchID := matchIDs[fmt.Sprintf("1-%d", i)]
		var team1ID, team2ID *int64
		if s1 < len(slots) && slots[s1].teamID != nil {
			team1ID = slots[s1].teamID
		}
		if s2 < len(slots) && slots[s2].teamID != nil {
			team2ID = slots[s2].teamID
		}

		isBye := team1ID == nil || team2ID == nil
		var winnerID *int64
		if isBye {
			if team1ID != nil {
				winnerID = team1ID
			} else if team2ID != nil {
				winnerID = team2ID
			}
		}

		_, err := db.Exec(`UPDATE matches SET team1_id=?, team2_id=?, is_bye=?, winner_id=? WHERE id=?`,
			team1ID, team2ID, isBye, winnerID, matchID)
		if err != nil {
			return fmt.Errorf("place teams in match %d: %w", matchID, err)
		}

		// Advance bye winners to next round
		if isBye && winnerID != nil {
			if err := db.advanceToNext(matchID, *winnerID); err != nil {
				return err
			}
		}
	}

	// Update team seeds
	for i, id := range teamIDs {
		if _, err := db.Exec(`UPDATE teams SET seed=? WHERE id=?`, i+1, id); err != nil {
			return err
		}
	}

	_ = numByes // used implicitly via nil slots
	return nil
}

// advanceToNext places the winner into the appropriate slot of the next match.
func (db *DB) advanceToNext(matchID int64, winnerID int64) error {
	var nextMatchID *int64
	var position int
	err := db.QueryRow(`SELECT next_match_id, position FROM matches WHERE id=?`, matchID).
		Scan(&nextMatchID, &position)
	if err != nil || nextMatchID == nil {
		return err
	}

	// Even positions go to team1, odd to team2
	col := "team1_id"
	if position%2 == 1 {
		col = "team2_id"
	}
	_, err = db.Exec(fmt.Sprintf(`UPDATE matches SET %s=? WHERE id=?`, col), winnerID, *nextMatchID)
	return err
}

// GetBracket returns all matches for a tournament with team names populated.
func (db *DB) GetBracket(tournamentID int64) ([]Match, error) {
	rows, err := db.Query(`SELECT m.id, m.tournament_id, m.round, m.position, m.best_of,
		m.team1_id, m.team2_id, m.winner_id, m.next_match_id, m.is_bye,
		COALESCE(t1.name, ''), COALESCE(t2.name, ''), COALESCE(tw.name, '')
		FROM matches m
		LEFT JOIN teams t1 ON m.team1_id = t1.id
		LEFT JOIN teams t2 ON m.team2_id = t2.id
		LEFT JOIN teams tw ON m.winner_id = tw.id
		WHERE m.tournament_id=?
		ORDER BY m.round, m.position`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []Match
	for rows.Next() {
		var m Match
		if err := rows.Scan(&m.ID, &m.TournamentID, &m.Round, &m.Position, &m.BestOf,
			&m.Team1ID, &m.Team2ID, &m.WinnerID, &m.NextMatchID, &m.IsBye,
			&m.Team1Name, &m.Team2Name, &m.WinnerName); err != nil {
			return nil, err
		}
		matches = append(matches, m)
	}

	// Load games for each match
	for i := range matches {
		matches[i].Games, err = db.GetMatchGames(matches[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return matches, nil
}

func (db *DB) GetMatchByID(matchID int64) (*Match, error) {
	var m Match
	err := db.QueryRow(`SELECT m.id, m.tournament_id, m.round, m.position, m.best_of,
		m.team1_id, m.team2_id, m.winner_id, m.next_match_id, m.is_bye,
		COALESCE(t1.name, ''), COALESCE(t2.name, ''), COALESCE(tw.name, '')
		FROM matches m
		LEFT JOIN teams t1 ON m.team1_id = t1.id
		LEFT JOIN teams t2 ON m.team2_id = t2.id
		LEFT JOIN teams tw ON m.winner_id = tw.id
		WHERE m.id=?`, matchID).
		Scan(&m.ID, &m.TournamentID, &m.Round, &m.Position, &m.BestOf,
			&m.Team1ID, &m.Team2ID, &m.WinnerID, &m.NextMatchID, &m.IsBye,
			&m.Team1Name, &m.Team2Name, &m.WinnerName)
	if err != nil {
		return nil, err
	}
	m.Games, _ = db.GetMatchGames(m.ID)
	return &m, nil
}

func (db *DB) SetMatchBestOf(matchID int64, bestOf int) error {
	if bestOf != 1 && bestOf != 3 {
		return fmt.Errorf("bestOf must be 1 or 3, got %d", bestOf)
	}
	_, err := db.Exec(`UPDATE matches SET best_of=? WHERE id=?`, bestOf, matchID)
	return err
}

func (db *DB) SetMatchWinner(matchID int64, winnerID int64) error {
	_, err := db.Exec(`UPDATE matches SET winner_id=? WHERE id=?`, winnerID, matchID)
	if err != nil {
		return err
	}
	return db.advanceToNext(matchID, winnerID)
}

// SwapTeams swaps two teams between bracket positions.
// slot is "team1" or "team2" for each match.
func (db *DB) SwapTeams(match1ID int64, slot1 string, match2ID int64, slot2 string) error {
	var team1ID, team2ID *int64
	err := db.QueryRow(fmt.Sprintf(`SELECT %s FROM matches WHERE id=?`, slot1), match1ID).Scan(&team1ID)
	if err != nil {
		return err
	}
	err = db.QueryRow(fmt.Sprintf(`SELECT %s FROM matches WHERE id=?`, slot2), match2ID).Scan(&team2ID)
	if err != nil {
		return err
	}

	if _, err := db.Exec(fmt.Sprintf(`UPDATE matches SET %s=? WHERE id=?`, slot1), team2ID, match1ID); err != nil {
		return err
	}
	_, err = db.Exec(fmt.Sprintf(`UPDATE matches SET %s=? WHERE id=?`, slot2), team1ID, match2ID)
	return err
}

// bracketOrder returns the standard single-elimination seeding positions
// for a bracket of size n (must be power of 2).
// e.g. for n=8: [0,7, 3,4, 1,6, 2,5] (seed 1v8, 4v5, 2v7, 3v6)
func bracketOrder(n int) []int {
	if n == 1 {
		return []int{0}
	}
	half := bracketOrder(n / 2)
	result := make([]int, n)
	for i, v := range half {
		result[i*2] = v
		result[i*2+1] = n - 1 - v
	}
	return result
}

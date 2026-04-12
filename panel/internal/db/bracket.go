package db

import (
	"fmt"
	"math"
	"strings"
)

type Match struct {
	ID               int64
	TournamentID     int64
	Round            int
	Position         int
	BestOf           int
	Team1ID          *int64
	Team2ID          *int64
	WinnerID         *int64
	NextMatchID      *int64
	IsBye            bool
	BracketSection   string // "winners", "losers", "grand_final"
	GroupID          int
	LoserNextMatchID *int64

	// Populated by GetBracket
	Team1Name  string
	Team2Name  string
	WinnerName string
	Games      []Game
}

// GenerateBracket creates a single-elimination bracket from an ordered list of team IDs.
// The order determines seeding (index 0 = seed 1).
func (db *DB) GenerateBracket(tournamentID int64, teamIDs []int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Clear existing bracket
	if _, err := tx.Exec(`DELETE FROM matches WHERE tournament_id=?`, tournamentID); err != nil {
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

			res, err := tx.Exec(`INSERT INTO matches (tournament_id, round, position, next_match_id)
				VALUES (?, ?, ?, ?)`, tournamentID, round, pos, nextMatchID)
			if err != nil {
				return fmt.Errorf("create match r%d p%d: %w", round, pos, err)
			}
			id, err := res.LastInsertId()
			if err != nil {
				return fmt.Errorf("last insert id: %w", err)
			}
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

		_, err := tx.Exec(`UPDATE matches SET team1_id=?, team2_id=?, is_bye=?, winner_id=? WHERE id=?`,
			team1ID, team2ID, isBye, winnerID, matchID)
		if err != nil {
			return fmt.Errorf("place teams in match %d: %w", matchID, err)
		}

		// Advance bye winners to next round
		if isBye && winnerID != nil {
			var nextMatchID *int64
			var pos int
			tx.QueryRow(`SELECT next_match_id, position FROM matches WHERE id=?`, matchID).Scan(&nextMatchID, &pos)
			if nextMatchID != nil {
				col := "team1_id"
				if pos%2 == 1 {
					col = "team2_id"
				}
				tx.Exec(fmt.Sprintf(`UPDATE matches SET %s=? WHERE id=?`, col), *winnerID, *nextMatchID)
			}
		}
	}

	// Update team seeds
	for i, id := range teamIDs {
		if _, err := tx.Exec(`UPDATE teams SET seed=? WHERE id=?`, i+1, id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GenerateDoubleElimBracket creates a double-elimination bracket from an ordered list of team IDs.
// The order determines seeding (index 0 = seed 1).
//
// Structure for N teams (rounded up to power of 2, P):
//   - Winners bracket: standard single-elimination (log2(P) rounds)
//   - Losers bracket: 2*(numWBRounds-1) rounds with alternating structure
//   - Grand final: WB winner vs LB winner
//
// Losers bracket round structure:
//   - Odd LB rounds (minor): WB losers drop down, play previous LB round winners
//   - Even LB rounds (major): LB winners play each other (no new dropdowns)
//   - Exception: LR1 is WB round 1 losers paired against each other
func (db *DB) GenerateDoubleElimBracket(tournamentID int64, teamIDs []int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Clear existing bracket
	if _, err := tx.Exec(`UPDATE matches SET next_match_id=NULL, loser_next_match_id=NULL WHERE tournament_id=?`, tournamentID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM matches WHERE tournament_id=?`, tournamentID); err != nil {
		return err
	}

	n := len(teamIDs)
	if n < 3 {
		return fmt.Errorf("double elimination needs at least 3 teams, got %d", n)
	}

	// Next power of 2
	p := 1
	for p < n {
		p *= 2
	}
	numWBRounds := int(math.Log2(float64(p)))

	// Build seeding slots
	type slot struct {
		teamID *int64
	}
	slots := make([]slot, p)
	for i, id := range teamIDs {
		id := id
		slots[i] = slot{teamID: &id}
	}

	order := bracketOrder(p)

	// -------------------------------------------------------------------
	// 1. Create Winners Bracket matches (top-down, same as single elim)
	// -------------------------------------------------------------------
	wbMatchIDs := make(map[string]int64) // "round-position" -> match ID

	for round := numWBRounds; round >= 1; round-- {
		matchesInRound := p / int(math.Pow(2, float64(round)))
		for pos := 0; pos < matchesInRound; pos++ {
			var nextMatchID *int64
			if round < numWBRounds {
				nid := wbMatchIDs[fmt.Sprintf("%d-%d", round+1, pos/2)]
				nextMatchID = &nid
			}

			res, err := tx.Exec(`INSERT INTO matches (tournament_id, round, position, next_match_id, bracket_section)
				VALUES (?, ?, ?, ?, 'winners')`, tournamentID, round, pos, nextMatchID)
			if err != nil {
				return fmt.Errorf("create WB match r%d p%d: %w", round, pos, err)
			}
			id, err := res.LastInsertId()
			if err != nil {
				return fmt.Errorf("last insert id: %w", err)
			}
			wbMatchIDs[fmt.Sprintf("%d-%d", round, pos)] = id
		}
	}

	// -------------------------------------------------------------------
	// 2. Create Losers Bracket matches
	// -------------------------------------------------------------------
	// LB has 2*(numWBRounds-1) rounds for standard double elim.
	// LR1: p/4 matches (WR1 losers play each other, cross-seeded)
	// LR2: p/4 matches (LR1 winners vs WR2 losers)
	// LR3: p/8 matches (LR2 winners play each other)
	// LR4: p/8 matches (LR3 winners vs WR3 losers)
	// ...pattern continues...
	// Final two LB rounds: LR(2k-1) has 1 match, LR(2k) has 1 match

	numLBRounds := 2 * (numWBRounds - 1)
	lbMatchIDs := make(map[string]int64) // "lbround-position" -> match ID

	// Calculate how many matches in each LB round
	lbRoundSizes := make([]int, numLBRounds+1) // 1-indexed
	if numLBRounds > 0 {
		lbRoundSizes[1] = p / 4 // LR1: half of WR1 matches (WR1 losers paired up)
		for lbr := 2; lbr <= numLBRounds; lbr++ {
			if lbr%2 == 0 {
				// Even (minor/dropdown) round: same count as previous round
				lbRoundSizes[lbr] = lbRoundSizes[lbr-1]
			} else {
				// Odd (major) round: halves the count
				lbRoundSizes[lbr] = lbRoundSizes[lbr-1] / 2
			}
		}
	}

	// Create LB matches from last round down to first (so we have next_match_id)
	for lbr := numLBRounds; lbr >= 1; lbr-- {
		matchesInRound := lbRoundSizes[lbr]
		for pos := 0; pos < matchesInRound; pos++ {
			var nextMatchID *int64
			if lbr < numLBRounds {
				var nid int64
				if (lbr+1)%2 == 0 {
					// Next round is even (dropdown): same count, position maps 1:1
					nid = lbMatchIDs[fmt.Sprintf("%d-%d", lbr+1, pos)]
				} else {
					// Next round is odd (major): halves the count, pos/2
					nid = lbMatchIDs[fmt.Sprintf("%d-%d", lbr+1, pos/2)]
				}
				nextMatchID = &nid
			}

			// Negative rounds for LB: round -1 = LR1, round -2 = LR2, etc.
			res, err := tx.Exec(`INSERT INTO matches (tournament_id, round, position, next_match_id, bracket_section)
				VALUES (?, ?, ?, ?, 'losers')`, tournamentID, -lbr, pos, nextMatchID)
			if err != nil {
				return fmt.Errorf("create LB match lr%d p%d: %w", lbr, pos, err)
			}
			id, err := res.LastInsertId()
			if err != nil {
				return fmt.Errorf("last insert id: %w", err)
			}
			lbMatchIDs[fmt.Sprintf("%d-%d", lbr, pos)] = id
		}
	}

	// -------------------------------------------------------------------
	// 3. Create Grand Final match
	// -------------------------------------------------------------------
	var gfNextMatchID *int64 // grand final has no next match
	// WB final winner goes to GF as team1
	// LB final winner goes to GF as team2
	res, err := tx.Exec(`INSERT INTO matches (tournament_id, round, position, next_match_id, bracket_section)
		VALUES (?, ?, 0, ?, 'grand_final')`, tournamentID, numWBRounds+1, gfNextMatchID)
	if err != nil {
		return fmt.Errorf("create grand final: %w", err)
	}
	gfID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("last insert id: %w", err)
	}

	// -------------------------------------------------------------------
	// 4. Link WB final → Grand Final (winner advances)
	// -------------------------------------------------------------------
	wbFinalID := wbMatchIDs[fmt.Sprintf("%d-0", numWBRounds)]
	if _, err := tx.Exec(`UPDATE matches SET next_match_id=? WHERE id=?`, gfID, wbFinalID); err != nil {
		return fmt.Errorf("link WB final to GF: %w", err)
	}

	// -------------------------------------------------------------------
	// 5. Link LB final → Grand Final (winner goes to team2)
	// -------------------------------------------------------------------
	if numLBRounds > 0 {
		lbFinalID := lbMatchIDs[fmt.Sprintf("%d-0", numLBRounds)]
		// LB final winner goes to GF team2. The LB final is position 0,
		// and we need it to go to team2. We set next_match_id and use position 1.
		if _, err := tx.Exec(`UPDATE matches SET next_match_id=?, position=1 WHERE id=?`, gfID, lbFinalID); err != nil {
			return fmt.Errorf("link LB final to GF: %w", err)
		}
	}

	// -------------------------------------------------------------------
	// 6. Set up loser_next_match_id for WB matches dropping into LB
	// -------------------------------------------------------------------

	// WR1 losers → LR1 (cross-seeded to avoid rematches)
	// WR1 has p/2 matches. LR1 has p/4 matches.
	// Cross-seed: WR1 match i's loser goes to LR1 match mapping.
	// For p/2 WB matches producing p/2 losers going into p/4 LB matches (2 per match):
	// Cross-pair: top half losers vs bottom half losers
	// WR1 match 0 loser vs WR1 match (p/4) loser → LR1 match 0
	// WR1 match 1 loser vs WR1 match (p/4+1) loser → LR1 match 1
	// etc.
	wr1Matches := p / 2
	lr1Matches := p / 4
	for i := 0; i < lr1Matches; i++ {
		// Top half WB match loser → LR1 match i (fills first available slot)
		topWBID := wbMatchIDs[fmt.Sprintf("1-%d", i)]
		lr1ID := lbMatchIDs[fmt.Sprintf("1-%d", i)]
		if _, err := tx.Exec(`UPDATE matches SET loser_next_match_id=? WHERE id=?`, lr1ID, topWBID); err != nil {
			return fmt.Errorf("link WR1[%d] loser to LR1: %w", i, err)
		}
		// Bottom half WB match loser → same LR1 match (cross-seeded for rematch avoidance)
		bottomIdx := wr1Matches - 1 - i
		bottomWBID := wbMatchIDs[fmt.Sprintf("1-%d", bottomIdx)]
		if _, err := tx.Exec(`UPDATE matches SET loser_next_match_id=? WHERE id=?`, lr1ID, bottomWBID); err != nil {
			return fmt.Errorf("link WR1[%d] loser to LR1: %w", bottomIdx, err)
		}
	}

	// WR2+ losers → LR even rounds (dropdown rounds)
	// WR(k) losers → LR(2*(k-1)) for k >= 2
	// Cross-pair: WR(k) match i loser vs LR(2*(k-1)-1) match j winner
	// The dropdown round has the same number of matches as the WB round it feeds from.
	// WR(k) loser at position i → LR(2*(k-1)) match at a cross-paired position
	for wbr := 2; wbr <= numWBRounds; wbr++ {
		lbDropdownRound := 2 * (wbr - 1)
		wbMatchesInRound := p / int(math.Pow(2, float64(wbr)))
		for i := 0; i < wbMatchesInRound; i++ {
			wbID := wbMatchIDs[fmt.Sprintf("%d-%d", wbr, i)]
			// Cross-pair: reverse the order to avoid rematches
			lbPos := wbMatchesInRound - 1 - i
			lbID := lbMatchIDs[fmt.Sprintf("%d-%d", lbDropdownRound, lbPos)]
			if _, err := tx.Exec(`UPDATE matches SET loser_next_match_id=? WHERE id=?`, lbID, wbID); err != nil {
				return fmt.Errorf("link WR%d[%d] loser to LR%d: %w", wbr, i, lbDropdownRound, err)
			}
		}
	}

	// WB Final loser → LB Final (last LB round)
	// This is already handled above: WB Final is the last WB round, and its loser
	// goes to LR(2*(numWBRounds-1)) = LR(numLBRounds) which is the LB final.
	// Already covered by the wbr loop above.

	// -------------------------------------------------------------------
	// 7. Place teams into first-round WB matches using seeding order
	// -------------------------------------------------------------------
	// Note: advanceLoser fills the first available slot (team1 then team2) in the
	// target LB match, so we don't need explicit slot routing for loser advancement.
	firstRoundMatches := p / 2
	for i := 0; i < firstRoundMatches; i++ {
		s1 := order[i*2]
		s2 := order[i*2+1]

		matchID := wbMatchIDs[fmt.Sprintf("1-%d", i)]
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

		_, err := tx.Exec(`UPDATE matches SET team1_id=?, team2_id=?, is_bye=?, winner_id=? WHERE id=?`,
			team1ID, team2ID, isBye, winnerID, matchID)
		if err != nil {
			return fmt.Errorf("place teams in WB match %d: %w", matchID, err)
		}

		// Advance bye winners to WB next round
		if isBye && winnerID != nil {
			var nextMatchID *int64
			var pos int
			tx.QueryRow(`SELECT next_match_id, position FROM matches WHERE id=?`, matchID).Scan(&nextMatchID, &pos)
			if nextMatchID != nil {
				col := "team1_id"
				if pos%2 == 1 {
					col = "team2_id"
				}
				tx.Exec(fmt.Sprintf(`UPDATE matches SET %s=? WHERE id=?`, col), *winnerID, *nextMatchID)
			}

			// For byes, the "loser" doesn't exist, so also mark the corresponding
			// LR1 slot. A bye in WR1 means there's no loser to send to LR1.
			// The LR1 match that was expecting this loser needs to handle it as a bye too.
			// We'll handle LR1 byes after all WR1 matches are placed.
		}
	}

	// -------------------------------------------------------------------
	// 8. Handle LR1 byes from WR1 byes
	// -------------------------------------------------------------------
	// For each LR1 match, check if both feeding WB matches had byes.
	// If one WB match was a bye, the LR1 match has a bye slot (no loser sent).
	// If both were byes, the LR1 match itself is a bye with no teams.
	for i := 0; i < lr1Matches; i++ {
		lr1ID := lbMatchIDs[fmt.Sprintf("1-%d", i)]

		// Check which WB matches feed this LR1 match
		var team1, team2 *int64
		tx.QueryRow(`SELECT team1_id, team2_id FROM matches WHERE id=?`, lr1ID).Scan(&team1, &team2)

		// A LR1 match is a bye if it has only one team (or zero)
		hasTeam1 := team1 != nil
		hasTeam2 := team2 != nil
		if (hasTeam1 && !hasTeam2) || (!hasTeam1 && hasTeam2) {
			// Single team bye - advance to next LB round
			byeWinner := team1
			if byeWinner == nil {
				byeWinner = team2
			}
			tx.Exec(`UPDATE matches SET is_bye=1, winner_id=? WHERE id=?`, byeWinner, lr1ID)

			// Advance bye winner to next LB round
			var nextMatchID *int64
			var pos int
			tx.QueryRow(`SELECT next_match_id, position FROM matches WHERE id=?`, lr1ID).Scan(&nextMatchID, &pos)
			if nextMatchID != nil {
				col := "team1_id"
				if pos%2 == 1 {
					col = "team2_id"
				}
				tx.Exec(fmt.Sprintf(`UPDATE matches SET %s=? WHERE id=?`, col), *byeWinner, *nextMatchID)
			}
		} else if !hasTeam1 && !hasTeam2 {
			// No teams at all - mark as bye
			tx.Exec(`UPDATE matches SET is_bye=1 WHERE id=?`, lr1ID)
		}
	}

	// -------------------------------------------------------------------
	// 9. Update team seeds
	// -------------------------------------------------------------------
	for i, id := range teamIDs {
		if _, err := tx.Exec(`UPDATE teams SET seed=? WHERE id=?`, i+1, id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// DeleteBracket removes all matches and their associated games, rounds, and stats.
// Relies on ON DELETE CASCADE from matches → games → game_rounds/game_player_stats.
func (db *DB) DeleteBracket(tournamentID int64) error {
	// Clear self-referential FKs before deleting
	if _, err := db.Exec(`UPDATE matches SET next_match_id=NULL, loser_next_match_id=NULL WHERE tournament_id=?`, tournamentID); err != nil {
		return fmt.Errorf("clear match refs: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM matches WHERE tournament_id=?`, tournamentID); err != nil {
		return fmt.Errorf("delete matches: %w", err)
	}
	// Also clear round-robin standings
	db.Exec(`DELETE FROM group_standings WHERE tournament_id=?`, tournamentID)
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

// advanceLoser places the loser into the first available slot of the loser's next match.
// Unlike advanceToNext (which uses position%2), losers bracket routing fills team1 first,
// then team2, since the feeding pattern for losers doesn't follow consecutive-pair logic.
func (db *DB) advanceLoser(matchID int64, loserID int64) error {
	var loserNextMatchID *int64
	err := db.QueryRow(`SELECT loser_next_match_id FROM matches WHERE id=?`, matchID).
		Scan(&loserNextMatchID)
	if err != nil || loserNextMatchID == nil {
		return err
	}

	// Check which slot is available in the target match
	var team1ID, team2ID *int64
	err = db.QueryRow(`SELECT team1_id, team2_id FROM matches WHERE id=?`, *loserNextMatchID).
		Scan(&team1ID, &team2ID)
	if err != nil {
		return err
	}

	col := "team1_id"
	if team1ID != nil {
		col = "team2_id"
	}
	_, err = db.Exec(fmt.Sprintf(`UPDATE matches SET %s=? WHERE id=?`, col), loserID, *loserNextMatchID)
	return err
}

const matchColumns = `m.id, m.tournament_id, m.round, m.position, m.best_of,
	m.team1_id, m.team2_id, m.winner_id, m.next_match_id, m.is_bye,
	m.bracket_section, m.group_id, m.loser_next_match_id,
	COALESCE(t1.name, ''), COALESCE(t2.name, ''), COALESCE(tw.name, '')`

const matchJoins = `FROM matches m
	LEFT JOIN teams t1 ON m.team1_id = t1.id
	LEFT JOIN teams t2 ON m.team2_id = t2.id
	LEFT JOIN teams tw ON m.winner_id = tw.id`

func scanMatch(s interface{ Scan(...any) error }) (Match, error) {
	var m Match
	err := s.Scan(&m.ID, &m.TournamentID, &m.Round, &m.Position, &m.BestOf,
		&m.Team1ID, &m.Team2ID, &m.WinnerID, &m.NextMatchID, &m.IsBye,
		&m.BracketSection, &m.GroupID, &m.LoserNextMatchID,
		&m.Team1Name, &m.Team2Name, &m.WinnerName)
	return m, err
}

// GetBracket returns all matches for a tournament with team names populated.
func (db *DB) GetBracket(tournamentID int64) ([]Match, error) {
	rows, err := db.Query(`SELECT `+matchColumns+` `+matchJoins+`
		WHERE m.tournament_id=?
		ORDER BY m.round, m.position`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []Match
	for rows.Next() {
		m, err := scanMatch(rows)
		if err != nil {
			return nil, err
		}
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Batch-load all games for this tournament's matches
	if len(matches) > 0 {
		matchIDs := make([]any, len(matches))
		placeholders := make([]string, len(matches))
		matchIdx := make(map[int64]int) // matchID -> index in matches
		for i, m := range matches {
			matchIDs[i] = m.ID
			placeholders[i] = "?"
			matchIdx[m.ID] = i
		}
		gRows, err := db.Query(`SELECT `+gameColumns+` FROM games WHERE match_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY game_number`, matchIDs...)
		if err != nil {
			return nil, err
		}
		defer gRows.Close()
		for gRows.Next() {
			g, err := scanGame(gRows)
			if err != nil {
				return nil, err
			}
			if idx, ok := matchIdx[g.MatchID]; ok {
				matches[idx].Games = append(matches[idx].Games, g)
			}
		}
		if err := gRows.Err(); err != nil {
			return nil, err
		}
	}
	return matches, nil
}

func (db *DB) GetMatchByID(matchID int64) (*Match, error) {
	m, err := scanMatch(db.QueryRow(`SELECT `+matchColumns+` `+matchJoins+` WHERE m.id=?`, matchID))
	if err != nil {
		return nil, err
	}
	m.Games, _ = db.GetMatchGames(m.ID)
	return &m, nil
}

func (db *DB) SetMatchBestOf(matchID int64, bestOf int) error {
	if bestOf != 1 && bestOf != 3 && bestOf != 5 {
		return fmt.Errorf("bestOf must be 1, 3, or 5, got %d", bestOf)
	}
	_, err := db.Exec(`UPDATE matches SET best_of=? WHERE id=?`, bestOf, matchID)
	return err
}

func (db *DB) SetMatchWinner(matchID int64, winnerID int64) error {
	_, err := db.Exec(`UPDATE matches SET winner_id=? WHERE id=?`, winnerID, matchID)
	if err != nil {
		return err
	}
	if err := db.advanceToNext(matchID, winnerID); err != nil {
		return err
	}

	// In double elimination, also advance the loser to the losers bracket
	var loserNextMatchID *int64
	var team1ID, team2ID *int64
	db.QueryRow(`SELECT loser_next_match_id, team1_id, team2_id FROM matches WHERE id=?`, matchID).
		Scan(&loserNextMatchID, &team1ID, &team2ID)
	if loserNextMatchID != nil {
		var loserID int64
		if team1ID != nil && *team1ID == winnerID && team2ID != nil {
			loserID = *team2ID
		} else if team2ID != nil && *team2ID == winnerID && team1ID != nil {
			loserID = *team1ID
		}
		if loserID != 0 {
			return db.advanceLoser(matchID, loserID)
		}
	}
	return nil
}

// ClearMatchWinner removes the winner from a match and undoes the advancement
// to the next round (one level only). Also reverts loser advancement in double elimination.
func (db *DB) ClearMatchWinner(matchID int64) error {
	var nextMatchID *int64
	var loserNextMatchID *int64
	var position int
	var winnerID *int64
	err := db.QueryRow(`SELECT next_match_id, loser_next_match_id, position, winner_id FROM matches WHERE id=?`, matchID).
		Scan(&nextMatchID, &loserNextMatchID, &position, &winnerID)
	if err != nil {
		return err
	}

	// Clear winner on this match
	if _, err := db.Exec(`UPDATE matches SET winner_id=NULL WHERE id=?`, matchID); err != nil {
		return err
	}

	// Remove team from next round slot
	if nextMatchID != nil {
		col := "team1_id"
		if position%2 == 1 {
			col = "team2_id"
		}
		if _, err := db.Exec(fmt.Sprintf(`UPDATE matches SET %s=NULL WHERE id=?`, col), *nextMatchID); err != nil {
			return err
		}
	}

	// Remove loser from losers bracket slot (double elimination)
	if loserNextMatchID != nil && winnerID != nil {
		// Determine who the loser was
		var team1ID, team2ID *int64
		db.QueryRow(`SELECT team1_id, team2_id FROM matches WHERE id=?`, matchID).
			Scan(&team1ID, &team2ID)
		var loserID *int64
		if team1ID != nil && *team1ID != *winnerID {
			loserID = team1ID
		} else if team2ID != nil && *team2ID != *winnerID {
			loserID = team2ID
		}
		if loserID != nil {
			// Find which slot in the LB match holds this loser and clear it
			var lbTeam1, lbTeam2 *int64
			db.QueryRow(`SELECT team1_id, team2_id FROM matches WHERE id=?`, *loserNextMatchID).
				Scan(&lbTeam1, &lbTeam2)
			if lbTeam1 != nil && *lbTeam1 == *loserID {
				db.Exec(`UPDATE matches SET team1_id=NULL WHERE id=?`, *loserNextMatchID)
			} else if lbTeam2 != nil && *lbTeam2 == *loserID {
				db.Exec(`UPDATE matches SET team2_id=NULL WHERE id=?`, *loserNextMatchID)
			}
		}
	}

	return nil
}

// SwapTeams swaps two teams between bracket positions.
// slot is "team1_id" or "team2_id" for each match.
func (db *DB) SwapTeams(match1ID int64, slot1 string, match2ID int64, slot2 string) error {
	// Whitelist valid column names
	if slot1 != "team1_id" && slot1 != "team2_id" {
		return fmt.Errorf("invalid slot: %s", slot1)
	}
	if slot2 != "team1_id" && slot2 != "team2_id" {
		return fmt.Errorf("invalid slot: %s", slot2)
	}

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

// GenerateRoundRobin creates a round-robin bracket from an ordered list of team IDs.
// Teams are distributed into groups round-robin style, and within each group every
// team plays every other team. The circle method is used to generate pairings.
func (db *DB) GenerateRoundRobin(tournamentID int64, teamIDs []int64, groupCount int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Clear existing bracket and standings
	if _, err := tx.Exec(`UPDATE matches SET next_match_id=NULL, loser_next_match_id=NULL WHERE tournament_id=?`, tournamentID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM matches WHERE tournament_id=?`, tournamentID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM group_standings WHERE tournament_id=?`, tournamentID); err != nil {
		return err
	}

	n := len(teamIDs)
	if n < 2 {
		return fmt.Errorf("need at least 2 teams, got %d", n)
	}
	if groupCount <= 0 {
		groupCount = 1
	}

	// Distribute teams into groups round-robin style
	groups := make([][]int64, groupCount)
	for i, id := range teamIDs {
		g := i % groupCount
		groups[g] = append(groups[g], id)
	}

	// For each group, generate pairings using the circle method and create matches
	for g := 0; g < groupCount; g++ {
		gTeams := groups[g]
		gn := len(gTeams)

		if gn < 2 {
			// Single team in a group -- just create standings row, no matches
			if _, err := tx.Exec(`INSERT INTO group_standings (tournament_id, group_id, team_id) VALUES (?, ?, ?)`,
				tournamentID, g, gTeams[0]); err != nil {
				return fmt.Errorf("init standing g%d: %w", g, err)
			}
			continue
		}

		// Circle method: if odd number of teams, add a "bye" sentinel (-1)
		circle := make([]int64, len(gTeams))
		copy(circle, gTeams)
		hasBye := gn%2 != 0
		if hasBye {
			circle = append(circle, -1) // sentinel for bye
		}
		cn := len(circle) // always even now
		numRounds := cn - 1

		for round := 0; round < numRounds; round++ {
			posInRound := 0
			for i := 0; i < cn/2; i++ {
				home := circle[i]
				away := circle[cn-1-i]
				// Skip bye pairings
				if home == -1 || away == -1 {
					continue
				}
				_, err := tx.Exec(`INSERT INTO matches (tournament_id, round, position, bracket_section, group_id, team1_id, team2_id)
					VALUES (?, ?, ?, 'group', ?, ?, ?)`,
					tournamentID, round+1, posInRound, g, home, away)
				if err != nil {
					return fmt.Errorf("create match g%d r%d p%d: %w", g, round+1, posInRound, err)
				}
				posInRound++
			}
			// Rotate: fix circle[0], rotate circle[1..cn-1]
			last := circle[cn-1]
			copy(circle[2:], circle[1:cn-1])
			circle[1] = last
		}

		// Initialize standings for all teams in this group
		for _, tid := range gTeams {
			if _, err := tx.Exec(`INSERT INTO group_standings (tournament_id, group_id, team_id) VALUES (?, ?, ?)`,
				tournamentID, g, tid); err != nil {
				return fmt.Errorf("init standing g%d t%d: %w", g, tid, err)
			}
		}
	}

	// Update team seeds
	for i, id := range teamIDs {
		if _, err := tx.Exec(`UPDATE teams SET seed=? WHERE id=?`, i+1, id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GroupStanding holds a team's round-robin group standing.
type GroupStanding struct {
	TeamID    int64
	TeamName  string
	GroupID   int
	Wins      int
	Losses    int
	MapDiff   int
	RoundDiff int
	Points    int
}

// UpdateGroupStandings recalculates standings for a specific group in a tournament.
func (db *DB) UpdateGroupStandings(tournamentID int64, groupID int) error {
	// Get all completed matches in this group
	rows, err := db.Query(`SELECT m.team1_id, m.team2_id, m.winner_id
		FROM matches m
		WHERE m.tournament_id=? AND m.bracket_section='group' AND m.group_id=? AND m.winner_id IS NOT NULL`,
		tournamentID, groupID)
	if err != nil {
		return err
	}
	defer rows.Close()

	type teamStats struct {
		wins      int
		losses    int
		mapDiff   int
		roundDiff int
	}
	stats := make(map[int64]*teamStats)

	ensureStats := func(tid int64) *teamStats {
		if s, ok := stats[tid]; ok {
			return s
		}
		s := &teamStats{}
		stats[tid] = s
		return s
	}

	type matchResult struct {
		team1ID  int64
		team2ID  int64
		winnerID int64
	}
	var results []matchResult

	for rows.Next() {
		var t1, t2, w int64
		if err := rows.Scan(&t1, &t2, &w); err != nil {
			return err
		}
		results = append(results, matchResult{t1, t2, w})
		ensureStats(t1)
		ensureStats(t2)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Process each match
	for _, mr := range results {
		s1 := ensureStats(mr.team1ID)
		s2 := ensureStats(mr.team2ID)

		if mr.winnerID == mr.team1ID {
			s1.wins++
			s2.losses++
		} else {
			s2.wins++
			s1.losses++
		}

		// Calculate map diff and round diff from games
		gRows, err := db.Query(`SELECT team1_score, team2_score, winner_id
			FROM games WHERE match_id IN (
				SELECT id FROM matches WHERE tournament_id=? AND bracket_section='group' AND group_id=?
				AND team1_id=? AND team2_id=? AND winner_id IS NOT NULL
			) AND status='completed'`,
			tournamentID, groupID, mr.team1ID, mr.team2ID)
		if err != nil {
			continue
		}
		var t1GamesWon, t2GamesWon int
		for gRows.Next() {
			var gs1, gs2 int
			var gw *int64
			if err := gRows.Scan(&gs1, &gs2, &gw); err != nil {
				continue
			}
			s1.roundDiff += gs1 - gs2
			s2.roundDiff += gs2 - gs1
			if gw != nil && *gw == mr.team1ID {
				t1GamesWon++
			} else if gw != nil {
				t2GamesWon++
			}
		}
		gRows.Close()
		s1.mapDiff += t1GamesWon - t2GamesWon
		s2.mapDiff += t2GamesWon - t1GamesWon
	}

	// Upsert standings
	for tid, s := range stats {
		points := s.wins * 3
		_, err := db.Exec(`INSERT INTO group_standings (tournament_id, group_id, team_id, wins, losses, map_diff, round_diff, points)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(tournament_id, group_id, team_id) DO UPDATE SET
				wins=excluded.wins, losses=excluded.losses, map_diff=excluded.map_diff,
				round_diff=excluded.round_diff, points=excluded.points`,
			tournamentID, groupID, tid, s.wins, s.losses, s.mapDiff, s.roundDiff, points)
		if err != nil {
			return fmt.Errorf("upsert standing t%d: %w", tid, err)
		}
	}
	return nil
}

// GetGroupStandings returns all standings for a tournament, sorted by group, then points DESC, etc.
func (db *DB) GetGroupStandings(tournamentID int64) ([]GroupStanding, error) {
	rows, err := db.Query(`SELECT gs.team_id, COALESCE(t.name, ''), gs.group_id, gs.wins, gs.losses, gs.map_diff, gs.round_diff, gs.points
		FROM group_standings gs
		LEFT JOIN teams t ON gs.team_id = t.id
		WHERE gs.tournament_id=?
		ORDER BY gs.group_id, gs.points DESC, gs.map_diff DESC, gs.round_diff DESC`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var standings []GroupStanding
	for rows.Next() {
		var s GroupStanding
		if err := rows.Scan(&s.TeamID, &s.TeamName, &s.GroupID, &s.Wins, &s.Losses, &s.MapDiff, &s.RoundDiff, &s.Points); err != nil {
			return nil, err
		}
		standings = append(standings, s)
	}
	return standings, rows.Err()
}

// GenerateHybridBracket creates the group stage for a hybrid tournament.
// The playoff bracket is generated separately via GeneratePlayoffBracket.
func (db *DB) GenerateHybridBracket(tournamentID int64, teamIDs []int64, groupCount, advanceCount int) error {
	if advanceCount <= 0 {
		advanceCount = 2
	}
	// Store group/advance counts on the tournament
	if _, err := db.Exec(`UPDATE tournament SET bracket_group_count=?, bracket_advance_count=? WHERE id=?`,
		groupCount, advanceCount, tournamentID); err != nil {
		return fmt.Errorf("update hybrid settings: %w", err)
	}
	return db.GenerateRoundRobin(tournamentID, teamIDs, groupCount)
}

// DeletePlayoffMatches removes only playoff matches (non-group) for a tournament,
// preserving the group stage matches and standings.
func (db *DB) DeletePlayoffMatches(tournamentID int64) error {
	// Clear self-referential FKs on playoff matches before deleting
	if _, err := db.Exec(`UPDATE matches SET next_match_id=NULL, loser_next_match_id=NULL
		WHERE tournament_id=? AND bracket_section != 'group'`, tournamentID); err != nil {
		return fmt.Errorf("clear playoff match refs: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM matches WHERE tournament_id=? AND bracket_section != 'group'`, tournamentID); err != nil {
		return fmt.Errorf("delete playoff matches: %w", err)
	}
	return nil
}

// GeneratePlayoffBracket reads group standings, takes the top N teams from each group,
// seeds them for the playoff bracket, and generates a single or double elimination bracket.
// playoffFormat is "single_elim" or "double_elim".
func (db *DB) GeneratePlayoffBracket(tournamentID int64, playoffFormat string) error {
	// Read tournament settings
	var groupCount, advanceCount int
	err := db.QueryRow(`SELECT bracket_group_count, bracket_advance_count FROM tournament WHERE id=?`, tournamentID).
		Scan(&groupCount, &advanceCount)
	if err != nil {
		return fmt.Errorf("read tournament settings: %w", err)
	}
	if groupCount <= 0 {
		return fmt.Errorf("no groups configured for hybrid tournament")
	}
	if advanceCount <= 0 {
		advanceCount = 2
	}

	// Get group standings
	standings, err := db.GetGroupStandings(tournamentID)
	if err != nil {
		return fmt.Errorf("get group standings: %w", err)
	}

	// Group standings by groupID
	groupStandings := make(map[int][]GroupStanding)
	for _, s := range standings {
		groupStandings[s.GroupID] = append(groupStandings[s.GroupID], s)
	}

	// Seed teams: Group A #1, Group B #1, ..., Group A #2, Group B #2, ...
	// This ensures teams from the same group don't meet in early rounds.
	var seededTeamIDs []int64
	for rank := 0; rank < advanceCount; rank++ {
		for g := 0; g < groupCount; g++ {
			gs := groupStandings[g]
			if rank < len(gs) {
				seededTeamIDs = append(seededTeamIDs, gs[rank].TeamID)
			}
		}
	}

	if len(seededTeamIDs) < 2 {
		return fmt.Errorf("need at least 2 teams for playoffs, got %d", len(seededTeamIDs))
	}

	// Delete existing playoff matches (keep group matches)
	if err := db.DeletePlayoffMatches(tournamentID); err != nil {
		return fmt.Errorf("delete existing playoffs: %w", err)
	}

	// Generate the playoff bracket using a transaction-safe approach.
	// The existing GenerateBracket/GenerateDoubleElimBracket functions delete ALL matches,
	// so we need custom versions that only create playoff matches.
	if playoffFormat == "double_elim" {
		return db.generatePlayoffDoubleElim(tournamentID, seededTeamIDs)
	}
	return db.generatePlayoffSingleElim(tournamentID, seededTeamIDs)
}

// generatePlayoffSingleElim creates a single-elimination playoff bracket
// without deleting existing group matches.
func (db *DB) generatePlayoffSingleElim(tournamentID int64, teamIDs []int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	n := len(teamIDs)
	if n < 2 {
		return fmt.Errorf("need at least 2 teams, got %d", n)
	}

	p := 1
	for p < n {
		p *= 2
	}
	numRounds := int(math.Log2(float64(p)))

	type slot struct {
		teamID *int64
	}
	slots := make([]slot, p)
	for i, id := range teamIDs {
		id := id
		slots[i] = slot{teamID: &id}
	}

	order := bracketOrder(p)
	matchIDs := make(map[string]int64)

	for round := numRounds; round >= 1; round-- {
		matchesInRound := p / int(math.Pow(2, float64(round)))
		for pos := 0; pos < matchesInRound; pos++ {
			var nextMatchID *int64
			if round < numRounds {
				nid := matchIDs[fmt.Sprintf("%d-%d", round+1, pos/2)]
				nextMatchID = &nid
			}

			res, err := tx.Exec(`INSERT INTO matches (tournament_id, round, position, next_match_id, bracket_section)
				VALUES (?, ?, ?, ?, 'winners')`, tournamentID, round, pos, nextMatchID)
			if err != nil {
				return fmt.Errorf("create playoff match r%d p%d: %w", round, pos, err)
			}
			id, err := res.LastInsertId()
			if err != nil {
				return fmt.Errorf("last insert id: %w", err)
			}
			matchIDs[fmt.Sprintf("%d-%d", round, pos)] = id
		}
	}

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

		_, err := tx.Exec(`UPDATE matches SET team1_id=?, team2_id=?, is_bye=?, winner_id=? WHERE id=?`,
			team1ID, team2ID, isBye, winnerID, matchID)
		if err != nil {
			return fmt.Errorf("place teams in playoff match %d: %w", matchID, err)
		}

		if isBye && winnerID != nil {
			var nextMatchID *int64
			var pos int
			tx.QueryRow(`SELECT next_match_id, position FROM matches WHERE id=?`, matchID).Scan(&nextMatchID, &pos)
			if nextMatchID != nil {
				col := "team1_id"
				if pos%2 == 1 {
					col = "team2_id"
				}
				tx.Exec(fmt.Sprintf(`UPDATE matches SET %s=? WHERE id=?`, col), *winnerID, *nextMatchID)
			}
		}
	}

	return tx.Commit()
}

// generatePlayoffDoubleElim creates a double-elimination playoff bracket
// without deleting existing group matches.
func (db *DB) generatePlayoffDoubleElim(tournamentID int64, teamIDs []int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	n := len(teamIDs)
	if n < 3 {
		return fmt.Errorf("double elimination needs at least 3 teams, got %d", n)
	}

	p := 1
	for p < n {
		p *= 2
	}
	numWBRounds := int(math.Log2(float64(p)))

	type slot struct {
		teamID *int64
	}
	slots := make([]slot, p)
	for i, id := range teamIDs {
		id := id
		slots[i] = slot{teamID: &id}
	}

	order := bracketOrder(p)

	// 1. Create Winners Bracket matches
	wbMatchIDs := make(map[string]int64)
	for round := numWBRounds; round >= 1; round-- {
		matchesInRound := p / int(math.Pow(2, float64(round)))
		for pos := 0; pos < matchesInRound; pos++ {
			var nextMatchID *int64
			if round < numWBRounds {
				nid := wbMatchIDs[fmt.Sprintf("%d-%d", round+1, pos/2)]
				nextMatchID = &nid
			}
			res, err := tx.Exec(`INSERT INTO matches (tournament_id, round, position, next_match_id, bracket_section)
				VALUES (?, ?, ?, ?, 'winners')`, tournamentID, round, pos, nextMatchID)
			if err != nil {
				return fmt.Errorf("create playoff WB match r%d p%d: %w", round, pos, err)
			}
			id, _ := res.LastInsertId()
			wbMatchIDs[fmt.Sprintf("%d-%d", round, pos)] = id
		}
	}

	// 2. Create Losers Bracket matches
	numLBRounds := 2 * (numWBRounds - 1)
	lbMatchIDs := make(map[string]int64)
	lbRoundSizes := make([]int, numLBRounds+1)
	if numLBRounds > 0 {
		lbRoundSizes[1] = p / 4
		for lbr := 2; lbr <= numLBRounds; lbr++ {
			if lbr%2 == 0 {
				lbRoundSizes[lbr] = lbRoundSizes[lbr-1]
			} else {
				lbRoundSizes[lbr] = lbRoundSizes[lbr-1] / 2
			}
		}
	}

	for lbr := numLBRounds; lbr >= 1; lbr-- {
		matchesInRound := lbRoundSizes[lbr]
		for pos := 0; pos < matchesInRound; pos++ {
			var nextMatchID *int64
			if lbr < numLBRounds {
				var nid int64
				if (lbr+1)%2 == 0 {
					nid = lbMatchIDs[fmt.Sprintf("%d-%d", lbr+1, pos)]
				} else {
					nid = lbMatchIDs[fmt.Sprintf("%d-%d", lbr+1, pos/2)]
				}
				nextMatchID = &nid
			}
			res, err := tx.Exec(`INSERT INTO matches (tournament_id, round, position, next_match_id, bracket_section)
				VALUES (?, ?, ?, ?, 'losers')`, tournamentID, -lbr, pos, nextMatchID)
			if err != nil {
				return fmt.Errorf("create playoff LB match lr%d p%d: %w", lbr, pos, err)
			}
			id, _ := res.LastInsertId()
			lbMatchIDs[fmt.Sprintf("%d-%d", lbr, pos)] = id
		}
	}

	// 3. Create Grand Final
	res, err := tx.Exec(`INSERT INTO matches (tournament_id, round, position, next_match_id, bracket_section)
		VALUES (?, ?, 0, NULL, 'grand_final')`, tournamentID, numWBRounds+1)
	if err != nil {
		return fmt.Errorf("create playoff grand final: %w", err)
	}
	gfID, _ := res.LastInsertId()

	// 4. Link WB final -> Grand Final
	wbFinalID := wbMatchIDs[fmt.Sprintf("%d-0", numWBRounds)]
	tx.Exec(`UPDATE matches SET next_match_id=? WHERE id=?`, gfID, wbFinalID)

	// 5. Link LB final -> Grand Final
	if numLBRounds > 0 {
		lbFinalID := lbMatchIDs[fmt.Sprintf("%d-0", numLBRounds)]
		tx.Exec(`UPDATE matches SET next_match_id=?, position=1 WHERE id=?`, gfID, lbFinalID)
	}

	// 6. Set up loser_next_match_id for WB matches dropping into LB
	wr1Matches := p / 2
	lr1Matches := p / 4
	for i := 0; i < lr1Matches; i++ {
		topWBID := wbMatchIDs[fmt.Sprintf("1-%d", i)]
		lr1ID := lbMatchIDs[fmt.Sprintf("1-%d", i)]
		tx.Exec(`UPDATE matches SET loser_next_match_id=? WHERE id=?`, lr1ID, topWBID)
		bottomIdx := wr1Matches - 1 - i
		bottomWBID := wbMatchIDs[fmt.Sprintf("1-%d", bottomIdx)]
		tx.Exec(`UPDATE matches SET loser_next_match_id=? WHERE id=?`, lr1ID, bottomWBID)
	}

	for wbr := 2; wbr <= numWBRounds; wbr++ {
		lbDropdownRound := 2 * (wbr - 1)
		wbMatchesInRound := p / int(math.Pow(2, float64(wbr)))
		for i := 0; i < wbMatchesInRound; i++ {
			wbID := wbMatchIDs[fmt.Sprintf("%d-%d", wbr, i)]
			lbPos := wbMatchesInRound - 1 - i
			lbID := lbMatchIDs[fmt.Sprintf("%d-%d", lbDropdownRound, lbPos)]
			tx.Exec(`UPDATE matches SET loser_next_match_id=? WHERE id=?`, lbID, wbID)
		}
	}

	// 7. Place teams into first-round WB matches
	firstRoundMatches := p / 2
	for i := 0; i < firstRoundMatches; i++ {
		s1 := order[i*2]
		s2 := order[i*2+1]

		matchID := wbMatchIDs[fmt.Sprintf("1-%d", i)]
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

		tx.Exec(`UPDATE matches SET team1_id=?, team2_id=?, is_bye=?, winner_id=? WHERE id=?`,
			team1ID, team2ID, isBye, winnerID, matchID)

		if isBye && winnerID != nil {
			var nextMatchID *int64
			var pos int
			tx.QueryRow(`SELECT next_match_id, position FROM matches WHERE id=?`, matchID).Scan(&nextMatchID, &pos)
			if nextMatchID != nil {
				col := "team1_id"
				if pos%2 == 1 {
					col = "team2_id"
				}
				tx.Exec(fmt.Sprintf(`UPDATE matches SET %s=? WHERE id=?`, col), *winnerID, *nextMatchID)
			}
		}
	}

	// 8. Handle LR1 byes
	for i := 0; i < lr1Matches; i++ {
		lr1ID := lbMatchIDs[fmt.Sprintf("1-%d", i)]
		var team1, team2 *int64
		tx.QueryRow(`SELECT team1_id, team2_id FROM matches WHERE id=?`, lr1ID).Scan(&team1, &team2)

		hasTeam1 := team1 != nil
		hasTeam2 := team2 != nil
		if (hasTeam1 && !hasTeam2) || (!hasTeam1 && hasTeam2) {
			byeWinner := team1
			if byeWinner == nil {
				byeWinner = team2
			}
			tx.Exec(`UPDATE matches SET is_bye=1, winner_id=? WHERE id=?`, byeWinner, lr1ID)
			var nextMatchID *int64
			var pos int
			tx.QueryRow(`SELECT next_match_id, position FROM matches WHERE id=?`, lr1ID).Scan(&nextMatchID, &pos)
			if nextMatchID != nil {
				col := "team1_id"
				if pos%2 == 1 {
					col = "team2_id"
				}
				tx.Exec(fmt.Sprintf(`UPDATE matches SET %s=? WHERE id=?`, col), *byeWinner, *nextMatchID)
			}
		} else if !hasTeam1 && !hasTeam2 {
			tx.Exec(`UPDATE matches SET is_bye=1 WHERE id=?`, lr1ID)
		}
	}

	return tx.Commit()
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

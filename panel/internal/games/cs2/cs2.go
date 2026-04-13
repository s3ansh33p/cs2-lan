// Package cs2 implements the games.Game interface for Counter-Strike 2.
//
// Source: CS2 official map pools as of January 2026.
//   - Active Duty (competitive): 7 maps, Anubis added / Train moved to reserve in Jan 2026.
//   - Wingman: Sanctum and Poseidon added Jan 29, 2026.
package cs2

import (
	"sort"

	"unilan/internal/games"
)

// Game is the CS2 implementation of games.Game. It is stateless; a zero value
// is fully functional.
type Game struct{}

func (Game) Slug() string { return "cs2" }
func (Game) Name() string { return "Counter-Strike 2" }

// Map families. Kept as package vars (not constants) because they're slices.
var (
	activeDuty = []string{"de_ancient", "de_anubis", "de_dust2", "de_inferno", "de_mirage", "de_nuke", "de_overpass"}
	reserve    = []string{"de_train", "de_vertigo"}
	hostage    = []string{"cs_office", "cs_italy", "cs_alpine"}
	armsRace   = []string{"ar_baggage", "ar_shoots", "ar_pool_day"}
	wingman    = []string{"de_inferno", "de_nuke", "de_overpass", "de_vertigo", "de_sanctum", "de_poseidon"}
)

// pools maps each game mode to its map list. Built from the families above.
var pools = map[string][]string{
	"competitive": activeDuty,
	"wingman":     wingman,
	"casual":      concat(activeDuty, reserve, hostage),
	"deathmatch":  concat(activeDuty, reserve),
	"armsrace":    armsRace,
	"demolition":  armsRace,
}

func concat(slices ...[]string) []string {
	var out []string
	for _, s := range slices {
		out = append(out, s...)
	}
	return out
}

func (Game) MapPools() map[string][]string { return pools }

// AllMaps returns the sorted, deduplicated superset of every pool.
func (Game) AllMaps() []string {
	seen := map[string]bool{}
	var out []string
	for _, pool := range pools {
		for _, m := range pool {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return out
}

// gameModes is the ordered list shown in mode dropdowns. Order matters
// (matches user expectation: competitive first).
var gameModes = []games.GameMode{
	{Slug: "competitive", Name: "Competitive"},
	{Slug: "wingman", Name: "Wingman"},
	{Slug: "casual", Name: "Casual"},
	{Slug: "deathmatch", Name: "Deathmatch"},
	{Slug: "armsrace", Name: "Arms Race"},
	{Slug: "demolition", Name: "Demolition"},
}

func (Game) GameModes() []games.GameMode { return gameModes }

// DefaultVetoFormat is the standard Bo3 veto sequence: ban-ban-pick-pick-ban-ban-last.
func (Game) DefaultVetoFormat() []string {
	return []string{"ban", "ban", "pick", "pick", "ban", "ban", "last"}
}

func init() {
	games.Register(Game{})
}

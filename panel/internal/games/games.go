// Package games defines the Game interface and registry used to dispatch
// game-specific data and configuration. To add a new game, create a sibling
// package (e.g., internal/games/valorant), implement Game, register the
// implementation in init(), and add a blank import to cmd/panel/main.go.
//
// This package owns only data and constants — log parsing, docker env-var
// generation, schema, and frontend UI are still CS2-specific elsewhere
// in the codebase. They will be lifted into per-game packages as a real
// second game forces the design.
package games

import "sync"

// Game describes a supported game. Implementations are stateless value types.
type Game interface {
	// Identity
	Slug() string // canonical identifier matching tournament.game_type, e.g. "cs2"
	Name() string // human-readable display name, e.g. "Counter-Strike 2"

	// Map data
	MapPools() map[string][]string // game mode (e.g. "wingman") -> ordered map list
	AllMaps() []string             // sorted, deduplicated superset of all pools

	// UI metadata
	GameModes() []GameMode // ordered list for game-mode dropdowns

	// Match flow
	DefaultVetoFormat() []string // fallback when tournament.veto_format is empty
	RCON() RCONCommands          // server control command strings

	// Server / container
	ContainerPrefix() string // docker container name prefix (e.g. "cs2-")
	DemoPath() string        // absolute in-container path containing demo files
}

// GameMode is a single entry in a game's mode dropdown.
type GameMode struct {
	Slug string // value submitted in forms (e.g. "competitive")
	Name string // display label (e.g. "Competitive")
}

// RCONCommands holds the literal command strings each game uses to control match flow.
// SetupLogging is the slice of commands issued once on tracker start.
type RCONCommands struct {
	RestartMatch string
	StartWarmup  string
	EndWarmup    string
	PauseWarmup  string
	PauseMatch   string
	UnpauseMatch string
	SetupLogging []string
}

// registry is the global game lookup table. Populated by package init() in each
// game's package via Register.
var (
	mu       sync.RWMutex
	registry = map[string]Game{}
)

// defaultSlug is the slug returned by Default() and used as the fallback when
// Get is called with an unknown slug. Currently CS2 is the only game.
const defaultSlug = "cs2"

// Register adds a game to the registry. Panics on duplicate slug — game
// registration is a startup invariant, not a runtime decision.
func Register(g Game) {
	mu.Lock()
	defer mu.Unlock()
	slug := g.Slug()
	if _, exists := registry[slug]; exists {
		panic("games: duplicate registration for slug " + slug)
	}
	registry[slug] = g
}

// Get returns the game for the given slug. Falls back to Default() if the
// slug is unknown (e.g. tournament.game_type is "valorant" but no Valorant
// implementation is registered yet).
func Get(slug string) Game {
	mu.RLock()
	defer mu.RUnlock()
	if g, ok := registry[slug]; ok {
		return g
	}
	return registry[defaultSlug]
}

// Default returns the fallback game (CS2). Panics if CS2 is not registered —
// this is a startup-time wiring error.
func Default() Game {
	mu.RLock()
	defer mu.RUnlock()
	g, ok := registry[defaultSlug]
	if !ok {
		panic("games: default game " + defaultSlug + " not registered (missing blank import in cmd/panel/main.go?)")
	}
	return g
}

// All returns every registered game. Order is not guaranteed.
func All() []Game {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Game, 0, len(registry))
	for _, g := range registry {
		out = append(out, g)
	}
	return out
}

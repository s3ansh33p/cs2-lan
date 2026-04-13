package cs2

import "unilan/internal/games"

// rconCommands holds the CS2 (Source-engine) RCON commands used to control
// match flow. Returned by Game.RCON().
var rconCommands = games.RCONCommands{
	RestartMatch: "mp_restartgame 1",
	StartWarmup:  "mp_warmup_start",
	EndWarmup:    "mp_warmup_end",
	PauseWarmup:  "mp_warmup_pausetimer 1",
	PauseMatch:   "mp_pause_match",
	UnpauseMatch: "mp_unpause_match",
	// Tracker state now comes from the CSTV+ broadcast parser, so the old
	// srcds text-log hooks (sv_logecho/log on/mp_logdetail) aren't needed.
	// Any additional commands added here will still run after the tracker
	// enables broadcast — handy for per-game setup that isn't broadcast-related.
	SetupLogging: nil,
}

func (Game) RCON() games.RCONCommands { return rconCommands }

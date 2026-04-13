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
	SetupLogging: []string{"sv_logecho 1", "log on", "mp_logdetail 3"},
}

func (Game) RCON() games.RCONCommands { return rconCommands }

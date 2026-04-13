package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	sqlDB.SetMaxOpenConns(1) // SQLite doesn't handle concurrent writes well

	db := &DB{sqlDB}
	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func (db *DB) migrate() error {
	// Pre-schema renames: migrate CS2-specific tables to cs2_ prefix.
	// These run before schema CREATEs so the CREATE TABLE IF NOT EXISTS below
	// becomes a no-op (not a second empty table). Errors are ignored — most
	// commonly the old table doesn't exist (fresh DB or already migrated).
	for _, q := range []string{
		`ALTER TABLE game_rounds RENAME TO cs2_game_rounds`,
		`ALTER TABLE game_player_stats RENAME TO cs2_game_player_stats`,
		`ALTER TABLE server_tracker_state RENAME TO cs2_server_tracker_state`,
		`ALTER TABLE ready_state RENAME TO cs2_ready_state`,
		`ALTER TABLE player_ready RENAME TO cs2_player_ready`,
	} {
		db.Exec(q)
	}

	if _, err := db.Exec(schema); err != nil {
		return err
	}
	// Add columns that may not exist on older DBs (ALTER TABLE IF NOT EXISTS not supported).
	// For DBs created pre-split, the games table still has the old CS2 columns; the
	// ADD COLUMN statements below are idempotent (errors ignored) and needed only for
	// DBs at intermediate schema versions.
	for _, q := range []string{
		`ALTER TABLE tournament ADD COLUMN game_mode TEXT NOT NULL DEFAULT 'competitive'`,
		`ALTER TABLE games ADD COLUMN team1_starts_ct INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE games ADD COLUMN h1_ct INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE games ADD COLUMN h1_t INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE games ADD COLUMN h2_ct INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE games ADD COLUMN h2_t INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE games ADD COLUMN half_round INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tournament ADD COLUMN deleted_at DATETIME`,
		`ALTER TABLE tournament ADD COLUMN hidden_at DATETIME`,

		// Item 2: Multi-game tournament support
		`ALTER TABLE tournament ADD COLUMN game_type TEXT NOT NULL DEFAULT 'cs2'`,

		// Item 3: Bracket expansion
		`ALTER TABLE tournament ADD COLUMN bracket_format TEXT NOT NULL DEFAULT 'single_elim'`,
		`ALTER TABLE tournament ADD COLUMN bracket_group_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tournament ADD COLUMN bracket_advance_count INTEGER NOT NULL DEFAULT 2`,
		`ALTER TABLE tournament ADD COLUMN veto_format TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE matches ADD COLUMN bracket_section TEXT NOT NULL DEFAULT 'winners'`,
		`ALTER TABLE matches ADD COLUMN group_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE matches ADD COLUMN loser_next_match_id INTEGER REFERENCES matches(id)`,

		// Item 5: Demo file access
		`ALTER TABLE games ADD COLUMN demo_path TEXT NOT NULL DEFAULT ''`,

		// Item 6: Player name matching
		`ALTER TABLE cs2_game_player_stats ADD COLUMN original_name TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE cs2_game_player_stats ADD COLUMN matched INTEGER NOT NULL DEFAULT 1`,
	} {
		db.Exec(q) // ignore errors (column already exists)
	}

	// Schema split: move CS2-specific columns out of `games` into `cs2_games`.
	// Idempotent — only runs if games still has the team1_starts_ct column.
	var hasOldCols int
	db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('games') WHERE name='team1_starts_ct'`).Scan(&hasOldCols)
	if hasOldCols > 0 {
		// Copy existing rows. INSERT OR IGNORE in case (game_id) already exists.
		db.Exec(`INSERT OR IGNORE INTO cs2_games
			(game_id, team1_starts_ct, h1_ct, h1_t, h2_ct, h2_t, half_round, demo_path)
			SELECT id, team1_starts_ct, h1_ct, h1_t, h2_ct, h2_t, half_round, demo_path FROM games`)
		// Drop the old columns. DROP COLUMN is supported in SQLite 3.35+.
		for _, col := range []string{"team1_starts_ct", "h1_ct", "h1_t", "h2_ct", "h2_t", "half_round", "demo_path"} {
			db.Exec(`ALTER TABLE games DROP COLUMN ` + col)
		}
	}

	// Seed settings table with defaults if empty
	db.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES ('active_tournament_id', '')`)
	db.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES ('site_name', 'UniLAN')`)
	db.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES ('announcement', '')`)
	db.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES ('announcement_link', '')`)
	db.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES ('event_start', '')`)
	db.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES ('event_end', '')`)

	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS tournament (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	team_size INTEGER NOT NULL DEFAULT 5,
	game_mode TEXT NOT NULL DEFAULT 'competitive',
	registration_open DATETIME,
	registration_close DATETIME,
	server_ip TEXT NOT NULL DEFAULT '',
	server_password TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'draft',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS teams (
	id INTEGER PRIMARY KEY,
	tournament_id INTEGER NOT NULL REFERENCES tournament(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	seed INTEGER,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS team_members (
	id INTEGER PRIMARY KEY,
	team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
	steam_name TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS matches (
	id INTEGER PRIMARY KEY,
	tournament_id INTEGER NOT NULL REFERENCES tournament(id) ON DELETE CASCADE,
	round INTEGER NOT NULL,
	position INTEGER NOT NULL,
	best_of INTEGER NOT NULL DEFAULT 1,
	team1_id INTEGER REFERENCES teams(id),
	team2_id INTEGER REFERENCES teams(id),
	winner_id INTEGER REFERENCES teams(id),
	next_match_id INTEGER REFERENCES matches(id),
	is_bye INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS games (
	id INTEGER PRIMARY KEY,
	match_id INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
	game_number INTEGER NOT NULL DEFAULT 1,
	map_name TEXT NOT NULL DEFAULT '',
	team1_score INTEGER NOT NULL DEFAULT 0,
	team2_score INTEGER NOT NULL DEFAULT 0,
	winner_id INTEGER REFERENCES teams(id),
	server_name TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'pending',
	started_at DATETIME,
	completed_at DATETIME
);

-- CS2-specific per-game detail. One row per CS2 game; joined via game_id.
CREATE TABLE IF NOT EXISTS cs2_games (
	game_id INTEGER PRIMARY KEY REFERENCES games(id) ON DELETE CASCADE,
	team1_starts_ct INTEGER NOT NULL DEFAULT 1,
	h1_ct INTEGER NOT NULL DEFAULT 0,
	h1_t INTEGER NOT NULL DEFAULT 0,
	h2_ct INTEGER NOT NULL DEFAULT 0,
	h2_t INTEGER NOT NULL DEFAULT 0,
	half_round INTEGER NOT NULL DEFAULT 0,
	demo_path TEXT NOT NULL DEFAULT ''
);

-- CS2-specific: per-round winner + reason (CT/T + elimination/bomb/defuse/time).
CREATE TABLE IF NOT EXISTS cs2_game_rounds (
	id INTEGER PRIMARY KEY,
	game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
	round INTEGER NOT NULL,
	winner TEXT NOT NULL,
	reason TEXT NOT NULL
);

-- CS2-specific: per-player stat columns (HS%, MVPs, EF, UD are CS2 concepts).
CREATE TABLE IF NOT EXISTS cs2_game_player_stats (
	id INTEGER PRIMARY KEY,
	game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
	team_id INTEGER NOT NULL REFERENCES teams(id),
	player_name TEXT NOT NULL,
	kills INTEGER NOT NULL DEFAULT 0,
	deaths INTEGER NOT NULL DEFAULT 0,
	assists INTEGER NOT NULL DEFAULT 0,
	hs_percent REAL NOT NULL DEFAULT 0,
	kdr REAL NOT NULL DEFAULT 0,
	adr REAL NOT NULL DEFAULT 0,
	mvps INTEGER NOT NULL DEFAULT 0,
	ef INTEGER NOT NULL DEFAULT 0,
	ud REAL NOT NULL DEFAULT 0,
	UNIQUE(game_id, player_name)
);

CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS server_aliases (
	server_name TEXT PRIMARY KEY,
	alias TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
	token TEXT PRIMARY KEY,
	created_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_teams_tournament ON teams(tournament_id);
CREATE INDEX IF NOT EXISTS idx_matches_tournament ON matches(tournament_id);
CREATE INDEX IF NOT EXISTS idx_games_match ON games(match_id);
CREATE INDEX IF NOT EXISTS idx_cs2_game_rounds_game ON cs2_game_rounds(game_id);
CREATE INDEX IF NOT EXISTS idx_cs2_game_player_stats_game ON cs2_game_player_stats(game_id);

CREATE TABLE IF NOT EXISTS schedule_items (
	id INTEGER PRIMARY KEY,
	start_at TEXT NOT NULL DEFAULT '',
	end_at TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	color TEXT NOT NULL DEFAULT 'blue'
);

-- CS2-specific: live server state (half_round, CT/T score are CS2 concepts).
CREATE TABLE IF NOT EXISTS cs2_server_tracker_state (
	server_name TEXT PRIMARY KEY,
	game_mode TEXT NOT NULL DEFAULT '',
	current_map TEXT NOT NULL DEFAULT '',
	half_round INTEGER NOT NULL DEFAULT 0,
	max_rounds INTEGER NOT NULL DEFAULT 0,
	ct_score INTEGER NOT NULL DEFAULT 0,
	t_score INTEGER NOT NULL DEFAULT 0,
	round INTEGER NOT NULL DEFAULT 0,
	in_warmup INTEGER NOT NULL DEFAULT 0,
	is_paused INTEGER NOT NULL DEFAULT 0,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS map_vetoes (
	id INTEGER PRIMARY KEY,
	match_id INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
	step INTEGER NOT NULL,
	action TEXT NOT NULL,
	team_id INTEGER REFERENCES teams(id),
	map_name TEXT NOT NULL,
	UNIQUE(match_id, step)
);
CREATE INDEX IF NOT EXISTS idx_map_vetoes_match ON map_vetoes(match_id);

CREATE TABLE IF NOT EXISTS group_standings (
	id INTEGER PRIMARY KEY,
	tournament_id INTEGER NOT NULL REFERENCES tournament(id) ON DELETE CASCADE,
	group_id INTEGER NOT NULL,
	team_id INTEGER NOT NULL REFERENCES teams(id),
	wins INTEGER NOT NULL DEFAULT 0,
	losses INTEGER NOT NULL DEFAULT 0,
	map_diff INTEGER NOT NULL DEFAULT 0,
	round_diff INTEGER NOT NULL DEFAULT 0,
	points INTEGER NOT NULL DEFAULT 0,
	UNIQUE(tournament_id, group_id, team_id)
);
CREATE INDEX IF NOT EXISTS idx_group_standings_tournament ON group_standings(tournament_id);

-- CS2-specific: .ready chat-command flow with CT/T team field on cs2_player_ready.
CREATE TABLE IF NOT EXISTS cs2_ready_state (
	id INTEGER PRIMARY KEY,
	game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
	server_name TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'waiting',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(game_id)
);

CREATE TABLE IF NOT EXISTS cs2_player_ready (
	id INTEGER PRIMARY KEY,
	ready_state_id INTEGER NOT NULL REFERENCES cs2_ready_state(id) ON DELETE CASCADE,
	player_name TEXT NOT NULL,
	team TEXT NOT NULL,
	is_ready INTEGER NOT NULL DEFAULT 0,
	UNIQUE(ready_state_id, player_name)
);
CREATE INDEX IF NOT EXISTS idx_cs2_player_ready_state ON cs2_player_ready(ready_state_id);
`

func (db *DB) LoadAliases() (map[string]string, error) {
	rows, err := db.Query("SELECT server_name, alias FROM server_aliases")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var name, alias string
		if err := rows.Scan(&name, &alias); err != nil {
			return nil, err
		}
		m[name] = alias
	}
	return m, rows.Err()
}

func (db *DB) SetAlias(name, alias string) error {
	if alias == "" {
		_, err := db.Exec("DELETE FROM server_aliases WHERE server_name = ?", name)
		return err
	}
	_, err := db.Exec(
		"INSERT INTO server_aliases (server_name, alias) VALUES (?, ?) ON CONFLICT(server_name) DO UPDATE SET alias = excluded.alias",
		name, alias,
	)
	return err
}

// --- Server tracker state persistence ---

type TrackerState struct {
	GameMode   string
	CurrentMap string
	HalfRound  int
	MaxRounds  int
	CTScore    int
	TScore     int
	Round      int
	InWarmup   bool
	IsPaused   bool
}

func (db *DB) SaveTrackerState(name string, s TrackerState) error {
	warmup, paused := 0, 0
	if s.InWarmup {
		warmup = 1
	}
	if s.IsPaused {
		paused = 1
	}
	_, err := db.Exec(`INSERT INTO cs2_server_tracker_state
		(server_name, game_mode, current_map, half_round, max_rounds, ct_score, t_score, round, in_warmup, is_paused, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(server_name) DO UPDATE SET
		game_mode=excluded.game_mode, current_map=excluded.current_map,
		half_round=excluded.half_round, max_rounds=excluded.max_rounds,
		ct_score=excluded.ct_score, t_score=excluded.t_score, round=excluded.round,
		in_warmup=excluded.in_warmup, is_paused=excluded.is_paused,
		updated_at=excluded.updated_at`,
		name, s.GameMode, s.CurrentMap, s.HalfRound, s.MaxRounds, s.CTScore, s.TScore, s.Round, warmup, paused)
	return err
}

func (db *DB) LoadTrackerState(name string) (*TrackerState, error) {
	var s TrackerState
	var warmup, paused int
	err := db.QueryRow(`SELECT game_mode, current_map, half_round, max_rounds, ct_score, t_score, round, in_warmup, is_paused
		FROM cs2_server_tracker_state WHERE server_name=?`, name).
		Scan(&s.GameMode, &s.CurrentMap, &s.HalfRound, &s.MaxRounds, &s.CTScore, &s.TScore, &s.Round, &warmup, &paused)
	if err != nil {
		return nil, err
	}
	s.InWarmup = warmup != 0
	s.IsPaused = paused != 0
	return &s, nil
}

func (db *DB) DeleteTrackerState(name string) error {
	_, err := db.Exec("DELETE FROM cs2_server_tracker_state WHERE server_name=?", name)
	return err
}

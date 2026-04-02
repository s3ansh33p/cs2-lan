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
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	// Add columns that may not exist on older DBs (ALTER TABLE IF NOT EXISTS not supported)
	for _, q := range []string{
		`ALTER TABLE tournament ADD COLUMN game_mode TEXT NOT NULL DEFAULT 'competitive'`,
		`ALTER TABLE games ADD COLUMN team1_starts_ct INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE games ADD COLUMN h1_ct INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE games ADD COLUMN h1_t INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE games ADD COLUMN h2_ct INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE games ADD COLUMN h2_t INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE games ADD COLUMN half_round INTEGER NOT NULL DEFAULT 0`,
	} {
		db.Exec(q) // ignore errors (column already exists)
	}
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
	team1_starts_ct INTEGER NOT NULL DEFAULT 1,
	h1_ct INTEGER NOT NULL DEFAULT 0,
	h1_t INTEGER NOT NULL DEFAULT 0,
	h2_ct INTEGER NOT NULL DEFAULT 0,
	h2_t INTEGER NOT NULL DEFAULT 0,
	half_round INTEGER NOT NULL DEFAULT 0,
	started_at DATETIME,
	completed_at DATETIME
);

CREATE TABLE IF NOT EXISTS game_rounds (
	id INTEGER PRIMARY KEY,
	game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
	round INTEGER NOT NULL,
	winner TEXT NOT NULL,
	reason TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS game_player_stats (
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

CREATE TABLE IF NOT EXISTS server_aliases (
	server_name TEXT PRIMARY KEY,
	alias TEXT NOT NULL
);
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

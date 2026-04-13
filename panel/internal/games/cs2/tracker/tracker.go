// Package tracker maintains live game state for CS2 servers.
//
// The tracker consumes Valve's CSTV+ broadcast protocol via an in-process
// relay (internal/games/cs2/cstv) and markus-wa/demoinfocs-golang's
// NewCSTVBroadcastParser. One goroutine per tracked server runs a parser
// that turns structured net-message events into updates on ServerState.
//
// The public surface (Kill, PlayerStats, ServerState, Manager, callbacks,
// TrackerMetadata) is what the rest of the panel depends on — HTTP partials,
// the game-state WebSocket, and persistence. Keep it stable; internals are
// free to change.
package tracker

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"unilan/internal/games"
	"unilan/internal/games/cs2/cstv"

	demoinfocs "github.com/markus-wa/demoinfocs-golang/v5/pkg/demoinfocs"
	"github.com/markus-wa/demoinfocs-golang/v5/pkg/demoinfocs/common"
	"github.com/markus-wa/demoinfocs-golang/v5/pkg/demoinfocs/events"
	"github.com/markus-wa/demoinfocs-golang/v5/pkg/demoinfocs/msg"
)

// Kill represents a kill event or a system message in the killfeed.
type Kill struct {
	Time         time.Time
	Killer       string
	KillerTeam   string // "CT", "TERRORIST"
	Victim       string
	VictimTeam   string // "CT", "TERRORIST"
	Weapon       string
	Headshot     bool
	Wallbang     bool
	Noscope      bool
	BlindKill    bool
	InAir        bool
	ThroughSmoke bool
	Assister     string
	AssisterTeam string
	FlashAssist  bool
	IsSystem     bool
	Message      string
}

// PlayerStats tracks per-player game state.
type PlayerStats struct {
	Name          string
	Team          string // "CT", "TERRORIST", or ""
	Kills         int
	Deaths        int
	Assists       int
	Weapons       map[string]bool // current loadout (normalized weapon names)
	Online        bool            // connected to server
	IsBot         bool
	Ping          int     // from RCON polling
	Duration      string  // from RCON polling
	Address       string  // from RCON polling
	Money         int     // from GameState
	AccountID     string  // Steam account ID
	Damage        float64 // total health damage dealt this match
	HSPercent     float64 // headshot percentage (derived)
	KDR           float64 // kill/death ratio (derived)
	ADR           float64 // average damage per round (derived)
	MVPs          int
	EF            int     // enemies flashed (over threshold)
	UD            float64 // grenade damage dealt
	KnifeKills    int
	ZeusKills     int
	HeadshotKills int
	Level         int // arms race level — not tracked via broadcast
	LevelKills    int // arms race per-level kills — not tracked via broadcast
	HasArmor      bool
	HasHelmet     bool
	HasDefuser    bool
	HasBomb       bool
	Alive         bool
	Health        int // 0-100, from common.Player.Health() (m_iHealth)
	Armor         int // 0-100, from common.Player.Armor() (m_ArmorValue)
}

func (p PlayerStats) Score() int {
	return p.Kills*2 + p.Assists
}

// WeaponList returns sorted non-grenade weapons.
func (p PlayerStats) WeaponList() []string {
	var out []string
	for w := range p.Weapons {
		if !IsGrenade(w) && !IsEquipment(w) && w != "c4" && w != "weapon_c4" {
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}

// GrenadeList returns sorted grenades.
func (p PlayerStats) GrenadeList() []string {
	var out []string
	for w := range p.Weapons {
		if IsGrenade(w) {
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}

// Grenade and equipment identification
var grenadeSet = map[string]bool{
	"hegrenade": true, "flashbang": true, "smokegrenade": true,
	"molotov": true, "incgrenade": true, "decoy": true,
}

var equipmentSet = map[string]bool{
	"kevlar": true, "assaultsuit": true, "defuser": true,
}

func IsGrenade(w string) bool   { return grenadeSet[w] }
func IsEquipment(w string) bool { return equipmentSet[w] }

// Display names for weapons
var WeaponDisplayName = map[string]string{
	"ak47": "AK-47", "m4a1": "M4A4", "m4a1_silencer": "M4A1-S",
	"awp": "AWP", "deagle": "Deagle", "usp_silencer": "USP-S",
	"glock": "Glock", "hkp2000": "P2000", "fiveseven": "Five-SeveN",
	"tec9": "Tec-9", "p250": "P250", "cz75a": "CZ75",
	"elite": "Dualies", "revolver": "R8",
	"famas": "FAMAS", "galilar": "Galil", "aug": "AUG", "sg556": "SG 553",
	"ssg08": "Scout", "scar20": "SCAR-20", "g3sg1": "G3SG1",
	"mac10": "MAC-10", "mp9": "MP9", "mp7": "MP7", "mp5sd": "MP5-SD",
	"ump45": "UMP-45", "p90": "P90", "bizon": "PP-Bizon",
	"nova": "Nova", "xm1014": "XM1014", "mag7": "MAG-7", "sawedoff": "Sawed-Off",
	"m249": "M249", "negev": "Negev",
	"knife": "Knife", "knife_t": "Knife",
	"hegrenade": "HE", "flashbang": "Flash", "smokegrenade": "Smoke",
	"molotov": "Molotov", "incgrenade": "Incendiary", "decoy": "Decoy",
	"taser": "Zeus",
}

func DisplayName(w string) string {
	if n, ok := WeaponDisplayName[w]; ok {
		return n
	}
	return w
}

// GrenadeShort returns a short abbreviation for grenades.
var GrenadeShort = map[string]string{
	"hegrenade": "HE", "flashbang": "FB", "smokegrenade": "SK",
	"molotov": "ML", "incgrenade": "IN", "decoy": "DC",
}

// RoundResult records the outcome of a single round.
type RoundResult struct {
	Round  int    // round number
	Winner string // "CT" or "T"
	Reason string // "elimination", "bomb", "defuse", "time"
}

// ServerState holds the parsed game state for one server.
type ServerState struct {
	mu         sync.RWMutex
	kills      []Kill
	killSeq    int // monotonically increasing kill counter
	stats      map[string]*PlayerStats
	maxKills   int
	round      int
	ctScore    int
	tScore     int
	rounds     []RoundResult
	gameMode   string
	currentMap string
	halfRound  int // round number where half-time occurred
	maxRounds  int // halfRound * 2 — for overtime detection
	inWarmup   bool
	isPaused   bool

	// roundsPlayed is used for ADR denominator. Counts non-warmup rounds since match start.
	roundsPlayed int

	// Server lifecycle: "", "restarting", "stopped"
	status string

	// Callbacks (set by Manager)
	serverName string
	gameOverFn GameOverFunc
	roundEndFn RoundEndFunc
	persistFn  PersistFunc
	readyFn    ReadyFunc

	// Metadata dirty flag — set under mu, read in notify()
	metaDirty bool

	// Change notification
	subMu sync.Mutex
	subs  []chan struct{}
}

// ScoreInfo returns the current round scores.
type ScoreInfo struct {
	Round      int
	CT         int
	T          int
	GameMode   string
	CurrentMap string
	Rounds     []RoundResult
	HalfRound  int
	MaxRounds  int
	InWarmup   bool
	IsPaused   bool
}

func (s *ServerState) GetScore() ScoreInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rounds := make([]RoundResult, len(s.rounds))
	copy(rounds, s.rounds)
	return ScoreInfo{Round: s.round, CT: s.ctScore, T: s.tScore, GameMode: s.gameMode, CurrentMap: s.currentMap, Rounds: rounds, HalfRound: s.halfRound, MaxRounds: s.maxRounds, InWarmup: s.inWarmup, IsPaused: s.isPaused}
}

// TrackerMetadata holds the subset of server state persisted across panel restarts.
type TrackerMetadata struct {
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

func (s *ServerState) GetMetadata() TrackerMetadata {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return TrackerMetadata{
		GameMode:   s.gameMode,
		CurrentMap: s.currentMap,
		HalfRound:  s.halfRound,
		MaxRounds:  s.maxRounds,
		CTScore:    s.ctScore,
		TScore:     s.tScore,
		Round:      s.round,
		InWarmup:   s.inWarmup,
		IsPaused:   s.isPaused,
	}
}

// markMetaDirty flags that metadata has changed. Caller must hold s.mu.
func (s *ServerState) markMetaDirty() { s.metaDirty = true }

func (s *ServerState) RestoreMetadata(m TrackerMetadata) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gameMode = m.GameMode
	s.currentMap = m.CurrentMap
	s.halfRound = m.HalfRound
	s.maxRounds = m.MaxRounds
	s.ctScore = m.CTScore
	s.tScore = m.TScore
	s.round = m.Round
	s.inWarmup = m.InWarmup
	s.isPaused = m.IsPaused
}

func newServerState() *ServerState {
	return &ServerState{
		stats:    make(map[string]*PlayerStats),
		inWarmup: false,
		maxKills: 200,
	}
}

// Subscribe returns a channel that receives a signal whenever state changes.
// Call the returned function to unsubscribe.
func (s *ServerState) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.subMu.Lock()
	s.subs = append(s.subs, ch)
	s.subMu.Unlock()
	return ch, func() {
		s.subMu.Lock()
		defer s.subMu.Unlock()
		for i, c := range s.subs {
			if c == ch {
				s.subs = append(s.subs[:i], s.subs[i+1:]...)
				break
			}
		}
	}
}

func (s *ServerState) notify() {
	// Check and clear metadata dirty flag (set under s.mu by callers)
	s.mu.Lock()
	dirty := s.metaDirty
	if dirty {
		s.metaDirty = false
	}
	var meta TrackerMetadata
	var persistFn PersistFunc
	if dirty && s.persistFn != nil {
		meta = TrackerMetadata{
			GameMode: s.gameMode, CurrentMap: s.currentMap,
			HalfRound: s.halfRound, MaxRounds: s.maxRounds,
			CTScore: s.ctScore, TScore: s.tScore, Round: s.round,
			InWarmup: s.inWarmup, IsPaused: s.isPaused,
		}
		persistFn = s.persistFn
	}
	name := s.serverName
	s.mu.Unlock()

	if persistFn != nil {
		go persistFn(name, meta)
	}

	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default: // don't block if subscriber is slow
		}
	}
}

// MarkStopped marks this server as stopped and notifies all subscribers.
func (s *ServerState) MarkStopped() {
	s.mu.Lock()
	s.status = "stopped"
	s.mu.Unlock()
	s.notify()
}

// MarkRestarting marks this server as restarting and notifies all subscribers.
func (s *ServerState) MarkRestarting() {
	s.mu.Lock()
	s.status = "restarting"
	s.mu.Unlock()
	s.notify()
}

// MarkReady clears the lifecycle status and notifies all subscribers.
func (s *ServerState) MarkReady() {
	s.mu.Lock()
	s.status = "ready"
	s.mu.Unlock()
	s.notify()
}

// Status returns the current lifecycle status ("", "restarting", "stopped", "ready").
func (s *ServerState) Status() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *ServerState) GetKillfeed(n int) []Kill {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n > len(s.kills) {
		n = len(s.kills)
	}
	result := make([]Kill, n)
	for i := 0; i < n; i++ {
		result[i] = s.kills[len(s.kills)-1-i]
	}
	return result
}

// KillCount returns the monotonic kill sequence number.
func (s *ServerState) KillCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.killSeq
}

// GetKillsSince returns kills added after sequence number `since` (oldest first).
func (s *ServerState) GetKillsSince(since int) []Kill {
	s.mu.RLock()
	defer s.mu.RUnlock()
	newCount := s.killSeq - since
	if newCount <= 0 {
		return nil
	}
	if newCount > len(s.kills) {
		newCount = len(s.kills)
	}
	start := len(s.kills) - newCount
	result := make([]Kill, newCount)
	copy(result, s.kills[start:])
	return result
}

func (s *ServerState) GetScoreboard() []PlayerStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]PlayerStats, 0, len(s.stats))
	for _, ps := range s.stats {
		cp := *ps
		if ps.Weapons != nil {
			cp.Weapons = make(map[string]bool, len(ps.Weapons))
			for k, v := range ps.Weapons {
				cp.Weapons[k] = v
			}
		}
		// Derive HSPercent, KDR, ADR from counters.
		if cp.Kills > 0 {
			cp.HSPercent = float64(cp.HeadshotKills) / float64(cp.Kills) * 100
		}
		if cp.Deaths > 0 {
			cp.KDR = float64(cp.Kills) / float64(cp.Deaths)
		} else if cp.Kills > 0 {
			cp.KDR = float64(cp.Kills)
		}
		if s.roundsPlayed > 0 {
			cp.ADR = cp.Damage / float64(s.roundsPlayed)
		}
		result = append(result, cp)
	}
	sort.Slice(result, func(i, j int) bool {
		si := result[i].Score()
		sj := result[j].Score()
		if si != sj {
			return si > sj
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// appendKill adds a kill to the ring buffer. Caller must hold s.mu.
func (s *ServerState) appendKill(k Kill) {
	s.kills = append(s.kills, k)
	s.killSeq++
	if len(s.kills) > s.maxKills {
		s.kills = s.kills[len(s.kills)-s.maxKills:]
	}
}

func (s *ServerState) ensurePlayer(name string) *PlayerStats {
	if _, ok := s.stats[name]; !ok {
		s.stats[name] = &PlayerStats{Name: name, Weapons: make(map[string]bool), Online: true, Alive: true, Team: "SPECTATOR"}
	}
	return s.stats[name]
}

// snapshotParticipants mirrors authoritative entity-graph fields from the
// demoinfocs parser (money, inventory, armor, helmet, defuser, bomb, alive,
// team) into PlayerStats. Returns true if any field moved so the caller can
// decide whether to notify subscribers. The parser provides live-updated
// entity properties for all of these, which is strictly more reliable than
// the Source 1 ItemPickup/Drop/Equip events that don't fire on CS2 CSTV+.
func (s *ServerState) snapshotParticipants(players []*common.Player) bool {
	changed := false
	s.mu.Lock()
	for _, pl := range players {
		if pl == nil {
			continue
		}
		name := pl.Name
		if name == "" {
			continue
		}
		p := s.ensurePlayer(name)

		// Team
		if t := teamString(pl.Team); t != "" && p.Team != t {
			p.Team = t
			changed = true
		}
		// IsBot / Online
		if p.IsBot != pl.IsBot {
			p.IsBot = pl.IsBot
			changed = true
		}
		if pl.IsConnected && !p.Online {
			p.Online = true
			changed = true
		}
		// Alive
		alive := pl.IsAlive()
		if p.Alive != alive {
			p.Alive = alive
			changed = true
		}
		// Money
		money := pl.Money()
		if p.Money != money {
			p.Money = money
			changed = true
		}
		// Health / Armor / Helmet / Defuser — authoritative entity values.
		health := pl.Health()
		if p.Health != health {
			p.Health = health
			changed = true
		}
		armor := pl.Armor()
		if p.Armor != armor {
			p.Armor = armor
			changed = true
		}
		hasArmor := armor > 0
		if p.HasArmor != hasArmor {
			p.HasArmor = hasArmor
			changed = true
		}
		helmet := pl.HasHelmet()
		if p.HasHelmet != helmet {
			p.HasHelmet = helmet
			changed = true
		}
		defuser := pl.HasDefuseKit()
		if p.HasDefuser != defuser {
			p.HasDefuser = defuser
			changed = true
		}

		// Weapons + bomb flag — rebuild from live inventory each snapshot.
		newWeapons := make(map[string]bool)
		hasBomb := false
		for _, eq := range pl.Weapons() {
			if eq == nil {
				continue
			}
			if eq.Type == common.EqBomb {
				hasBomb = true
				continue
			}
			if eq.Type == common.EqKnife {
				continue
			}
			wn := weaponName(eq)
			if wn == "" || wn == "world" {
				continue
			}
			newWeapons[wn] = true
		}
		if p.HasBomb != hasBomb {
			p.HasBomb = hasBomb
			changed = true
		}
		if !weaponsEqual(p.Weapons, newWeapons) {
			p.Weapons = newWeapons
			changed = true
		}
	}
	s.mu.Unlock()
	return changed
}

// weaponsEqual returns whether two weapon-name sets have identical keys.
func weaponsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func (s *ServerState) addSystemMessage(msg string) {
	s.mu.Lock()
	s.appendKill(Kill{Time: time.Now(), IsSystem: true, Message: msg})
	s.mu.Unlock()
	s.notify()
}

// ResetStats zeroes all player stats and scores, keeping players connected.
func (s *ServerState) ResetStats() {
	s.resetWithMessage("Match Restarted — Ready Up")
}

func (s *ServerState) resetWithMessage(reason string) {
	s.mu.Lock()
	for _, p := range s.stats {
		p.Kills = 0
		p.Deaths = 0
		p.Assists = 0
		p.Damage = 0
		p.HSPercent = 0
		p.KDR = 0
		p.ADR = 0
		p.MVPs = 0
		p.EF = 0
		p.UD = 0
		p.HeadshotKills = 0
		p.KnifeKills = 0
		p.ZeusKills = 0
		p.Level = 0
		p.LevelKills = 0
		p.Money = 0
		p.Weapons = make(map[string]bool)
		p.HasArmor = false
		p.HasHelmet = false
		p.HasDefuser = false
		p.HasBomb = false
		p.Alive = true
		p.Health = 0
		p.Armor = 0
	}
	s.round = 0
	s.ctScore = 0
	s.tScore = 0
	s.rounds = nil
	s.halfRound = 0
	s.maxRounds = 0
	s.isPaused = false
	s.roundsPlayed = 0
	s.markMetaDirty()
	s.appendKill(Kill{Time: time.Now(), IsSystem: true, Message: reason})
	s.mu.Unlock()
	s.notify()
}

// RCONFunc sends an RCON command to a server.
type RCONFunc func(addr, password, command string) (string, error)

// GameOverInfo contains the final state of a match when Game Over is detected.
type GameOverInfo struct {
	ServerName string
	Score      ScoreInfo
	Players    []PlayerStats
}

// GameOverFunc is called when a game ends, before stats are reset.
type GameOverFunc func(info GameOverInfo)

// RoundEndInfo contains the score after a round ends.
type RoundEndInfo struct {
	ServerName string
	CT         int
	T          int
}

// RoundEndFunc is called after each round win.
type RoundEndFunc func(info RoundEndInfo)

// PersistFunc is called when metadata fields change and should be persisted.
type PersistFunc func(serverName string, m TrackerMetadata)

// ReadyFunc is called when a player sends ".ready" in chat.
type ReadyFunc func(serverName, playerName, team string)

// Manager manages game trackers for all servers.
type Manager struct {
	mu         sync.Mutex
	servers    map[string]*ServerState
	cancels    map[string]context.CancelFunc
	relay      *cstv.Relay
	rconFn     RCONFunc
	panelPort  int
	gameOverFn GameOverFunc
	roundEndFn RoundEndFunc
	persistFn  PersistFunc
	readyFn    ReadyFunc
}

// NewManager constructs a Manager.
//
//   - relay is the CSTV broadcast relay the game will POST fragments to and the
//     parser will GET fragments from.
//   - rconFn sends RCON commands (used to enable broadcast + poll status).
//   - panelPort is the panel's HTTP listen port; used to build the broadcast
//     URL the game server is told to POST to (via host networking).
func NewManager(relay *cstv.Relay, rconFn RCONFunc, panelPort int) *Manager {
	return &Manager{
		servers:   make(map[string]*ServerState),
		cancels:   make(map[string]context.CancelFunc),
		relay:     relay,
		rconFn:    rconFn,
		panelPort: panelPort,
	}
}

// OnGameOver registers a callback that fires when any tracked server's game ends.
func (m *Manager) OnGameOver(fn GameOverFunc) {
	m.mu.Lock()
	m.gameOverFn = fn
	m.mu.Unlock()
}

// OnRoundEnd registers a callback that fires after each round win.
func (m *Manager) OnRoundEnd(fn RoundEndFunc) {
	m.mu.Lock()
	m.roundEndFn = fn
	m.mu.Unlock()
}

// OnMetadataChange registers a callback that fires when metadata fields change.
func (m *Manager) OnMetadataChange(fn PersistFunc) {
	m.mu.Lock()
	m.persistFn = fn
	m.mu.Unlock()
}

// OnPlayerReady registers a callback that fires when a player sends ".ready" in chat.
func (m *Manager) OnPlayerReady(fn ReadyFunc) {
	m.mu.Lock()
	m.readyFn = fn
	m.mu.Unlock()
}

// TrackServer starts tracking a server. Returns the state and whether it was newly created.
func (m *Manager) TrackServer(name string, gamePort int, rconPassword, gameMode, initialMap string) (*ServerState, bool) {
	m.mu.Lock()
	if s, ok := m.servers[name]; ok {
		m.mu.Unlock()
		return s, false
	}

	s := newServerState()
	s.gameMode = gameMode
	s.currentMap = initialMap
	s.serverName = name
	s.gameOverFn = m.gameOverFn
	s.roundEndFn = m.roundEndFn
	s.persistFn = m.persistFn
	s.readyFn = m.readyFn
	m.servers[name] = s
	ctx, cancel := context.WithCancel(context.Background())
	m.cancels[name] = cancel
	m.mu.Unlock()

	go m.setupAndTrack(ctx, name, gamePort, rconPassword, s)
	return s, true
}

func (m *Manager) GetState(name string) *ServerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.servers[name]
}

func (m *Manager) StopTracking(name string) {
	m.mu.Lock()
	state := m.servers[name]
	cancel, hasCancel := m.cancels[name]
	m.mu.Unlock()

	if state != nil && state.Status() == "" {
		state.MarkStopped()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if hasCancel {
		cancel()
		delete(m.cancels, name)
		delete(m.servers, name)
	}
	if m.relay != nil {
		m.relay.Close(name)
	}
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, cancel := range m.cancels {
		cancel()
		delete(m.cancels, name)
		delete(m.servers, name)
		if m.relay != nil {
			m.relay.Close(name)
		}
	}
}

// StopNotIn stops tracking servers not in the given set of running names.
func (m *Manager) StopNotIn(running map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, cancel := range m.cancels {
		if !running[name] {
			cancel()
			delete(m.cancels, name)
			delete(m.servers, name)
			if m.relay != nil {
				m.relay.Close(name)
			}
		}
	}
}

// setupAndTrack is the per-server worker goroutine: it enables CSTV broadcast
// via RCON, starts the RCON status poller, waits for the game to start POSTing
// fragments, then runs a demoinfocs parser against the relay URL.
func (m *Manager) setupAndTrack(ctx context.Context, name string, gamePort int, rconPassword string, state *ServerState) {
	addr := fmt.Sprintf("localhost:%d", gamePort)
	broadcastURL := fmt.Sprintf("http://127.0.0.1:%d/cstv/%s", m.panelPort, name)

	// Compose the setup commands. We keep the game registry generic and do
	// the per-server substitution here since only the tracker knows the URL.
	setupCmds := []string{
		"tv_delay 0",
		fmt.Sprintf(`tv_broadcast_url "%s"`, broadcastURL),
		"tv_broadcast 1",
	}
	for _, extra := range games.Default().RCON().SetupLogging {
		setupCmds = append(setupCmds, extra)
	}

	// CS2 can take 10-30s from container start to RCON being ready. Block
	// here until every command lands; otherwise tv_broadcast is never
	// enabled and no fragments ever arrive. Retry with backoff, up to ~60s.
	if !m.sendSetupWithRetry(ctx, name, addr, rconPassword, setupCmds) {
		return // context cancelled before the server came up
	}
	slog.Info("gametracker: started", "server", name, "mode", state.gameMode, "broadcast", broadcastURL)

	// NB: no background RCON status poller. Everything on the scoreboard
	// (team, money, alive, loadout, armor/helmet/defuser) now comes from the
	// broadcast's entity stream; polling RCON every 5 s just burned a push
	// cycle on stale ping/duration/address fields. RCON stays available for
	// on-demand commands (match restart, pause, chat) via rm.Execute.
	_ = addr
	_ = rconPassword

	retryDelay := 2 * time.Second
	maxDelay := 30 * time.Second

	for {
		// Wait for the relay to see the first ready fragment, or context cancellation.
		select {
		case <-ctx.Done():
			return
		case <-m.relay.Ready(name):
		}

		parser, err := demoinfocs.NewCSTVBroadcastParser(broadcastURL)
		if err != nil {
			slog.Warn("gametracker: broadcast parser init", "server", name, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryDelay):
				retryDelay = min(retryDelay*2, maxDelay)
				continue
			}
		}
		retryDelay = 2 * time.Second

		w := &eventWiring{state: state, parser: parser}
		w.register()

		// ParseToEnd blocks. Cancel from another goroutine when ctx fires.
		done := make(chan struct{})
		go func() {
			<-ctx.Done()
			parser.Cancel()
			// Evict fragments so in-flight /delta GETs return 404 and the
			// reader exits via its internal timeout.
			if m.relay != nil {
				m.relay.Close(name)
			}
			close(done)
		}()

		err = parser.ParseToEnd()
		// Ensure the cancel goroutine has exited (or will shortly).
		select {
		case <-done:
		default:
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Info("gametracker: broadcast parser ended", "server", name, "err", err)
		}

		// The broadcast stream ended (map change, server idle, EOF). Reset
		// the relay's buffered fragments and wait for a fresh signup.
		if m.relay != nil {
			m.relay.Close(name)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryDelay):
			retryDelay = min(retryDelay*2, maxDelay)
		}
	}
}

// sendSetupWithRetry runs the first RCON command in a loop until it succeeds
// (so we don't spam warnings during the ~10-30s container boot), then fires
// the rest. Returns false if the context is cancelled before RCON comes up.
func (m *Manager) sendSetupWithRetry(ctx context.Context, name, addr, rconPassword string, cmds []string) bool {
	if len(cmds) == 0 {
		return true
	}
	backoff := 1 * time.Second
	maxBackoff := 10 * time.Second
	firstWarn := true
	for {
		if ctx.Err() != nil {
			return false
		}
		// Probe with the first command. Once it succeeds the server is up.
		if _, err := m.rconFn(addr, rconPassword, cmds[0]); err != nil {
			if firstWarn {
				slog.Info("gametracker: waiting for rcon", "server", name, "err", err)
				firstWarn = false
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(backoff):
				backoff = min(backoff*2, maxBackoff)
				continue
			}
		}
		break
	}
	// RCON is up — send the remaining setup commands. These are best-effort;
	// log warnings but don't block on them.
	for _, cmd := range cmds[1:] {
		resp, err := m.rconFn(addr, rconPassword, cmd)
		if err != nil {
			slog.Warn("gametracker: rcon setup", "server", name, "cmd", cmd, "err", err)
		} else if resp != "" {
			slog.Info("gametracker: rcon setup", "server", name, "cmd", cmd, "resp", resp)
		}
	}
	return true
}

// eventWiring bundles the parser and state so event handlers can be methods
// on a value with shared context.
type eventWiring struct {
	state  *ServerState
	parser demoinfocs.Parser
	// Reusable flag dedupe: skip short flashes below a threshold for EF stat.
	// (CS2 scoreboard EF counts "enemies flashed for at least 0.7s", matching
	// mp_flashbang_flashduration logic used by MatchMaking.)
	efThreshold time.Duration

	// snapshotInterval throttles the per-frame GameState poll. We walk every
	// participant and copy authoritative fields (money, inventory, armor,
	// helmet, defuser, alive, team) at most once per this much game time.
	snapshotInterval time.Duration
	lastSnapshot     time.Duration
}

func (w *eventWiring) register() {
	if w.efThreshold == 0 {
		w.efThreshold = 700 * time.Millisecond
	}
	if w.snapshotInterval == 0 {
		w.snapshotInterval = 1 * time.Second
	}
	p := w.parser

	p.RegisterEventHandler(w.onKill)
	p.RegisterEventHandler(w.onPlayerHurt)
	p.RegisterEventHandler(w.onPlayerFlashed)
	p.RegisterEventHandler(w.onRoundStart)
	p.RegisterEventHandler(w.onRoundEnd)
	p.RegisterEventHandler(w.onRoundMVP)
	p.RegisterEventHandler(w.onMatchStartedChanged)
	p.RegisterEventHandler(w.onAnnouncementWinPanel)
	p.RegisterEventHandler(w.onPlayerConnect)
	p.RegisterEventHandler(w.onBotConnect)
	p.RegisterEventHandler(w.onPlayerDisconnected)
	p.RegisterEventHandler(w.onPlayerTeamChange)
	p.RegisterEventHandler(w.onChatMessage)
	p.RegisterEventHandler(w.onWarmupChanged)
	p.RegisterEventHandler(w.onConVarsUpdated)
	p.RegisterEventHandler(w.onBombPlanted)
	p.RegisterEventHandler(w.onBombDefused)
	p.RegisterEventHandler(w.onBombExplode)
	p.RegisterEventHandler(w.onTeamSideSwitch)
	p.RegisterEventHandler(w.onFrameDone)

	// CDemoFileHeader carries the map name at stream start; subsequent map
	// changes come through a fresh parser connection (tv_broadcast restarts).
	p.RegisterNetMessageHandler(w.onDemoHeader)
}

// --- event handlers ---

// playerName resolves a common.Player to the in-game name used as the
// ServerState key. Bots may share names; we let that ride — it matches the
// old log-based behavior.
func playerName(p *common.Player) string {
	if p == nil {
		return ""
	}
	return p.Name
}

// teamString maps demoinfocs team enum to the strings the rest of the panel
// UI expects ("CT", "TERRORIST", "SPECTATOR", "").
func teamString(t common.Team) string {
	switch t {
	case common.TeamCounterTerrorists:
		return "CT"
	case common.TeamTerrorists:
		return "TERRORIST"
	case common.TeamSpectators:
		return "SPECTATOR"
	}
	return ""
}

// weaponName normalizes an equipment to the lowercased name used as a loadout
// map key (e.g. "ak47", "usp_silencer", "hegrenade").
func weaponName(e *common.Equipment) string {
	if e == nil {
		return ""
	}
	// OriginalString is empty on CS2; fall back to Type name, lowercased and
	// with the canonical logfile aliases (knife covers both sides, etc.).
	switch e.Type {
	case common.EqAK47:
		return "ak47"
	case common.EqM4A4:
		return "m4a1"
	case common.EqM4A1:
		return "m4a1_silencer"
	case common.EqAWP:
		return "awp"
	case common.EqDeagle:
		return "deagle"
	case common.EqUSP:
		return "usp_silencer"
	case common.EqGlock:
		return "glock"
	case common.EqP2000:
		return "hkp2000"
	case common.EqFiveSeven:
		return "fiveseven"
	case common.EqTec9:
		return "tec9"
	case common.EqP250:
		return "p250"
	case common.EqCZ:
		return "cz75a"
	case common.EqDualBerettas:
		return "elite"
	case common.EqRevolver:
		return "revolver"
	case common.EqFamas:
		return "famas"
	case common.EqGalil:
		return "galilar"
	case common.EqAUG:
		return "aug"
	case common.EqSG553:
		return "sg556"
	case common.EqScout:
		return "ssg08"
	case common.EqScar20:
		return "scar20"
	case common.EqG3SG1:
		return "g3sg1"
	case common.EqMac10:
		return "mac10"
	case common.EqMP9:
		return "mp9"
	case common.EqMP7:
		return "mp7"
	case common.EqMP5:
		return "mp5sd"
	case common.EqUMP:
		return "ump45"
	case common.EqP90:
		return "p90"
	case common.EqBizon:
		return "bizon"
	case common.EqNova:
		return "nova"
	case common.EqXM1014:
		return "xm1014"
	case common.EqSwag7: // also EqMag7
		return "mag7"
	case common.EqSawedOff:
		return "sawedoff"
	case common.EqM249:
		return "m249"
	case common.EqNegev:
		return "negev"
	case common.EqKnife:
		return "knife"
	case common.EqZeus:
		return "taser"
	case common.EqHE:
		return "hegrenade"
	case common.EqFlash:
		return "flashbang"
	case common.EqSmoke:
		return "smokegrenade"
	case common.EqDecoy:
		return "decoy"
	case common.EqMolotov:
		return "molotov"
	case common.EqIncendiary:
		return "incgrenade"
	case common.EqBomb:
		return "c4"
	case common.EqKevlar:
		return "kevlar"
	case common.EqHelmet:
		return "assaultsuit"
	case common.EqDefuseKit:
		return "defuser"
	case common.EqWorld:
		return "world"
	}
	return ""
}

func (w *eventWiring) onKill(e events.Kill) {
	s := w.state
	killer := playerName(e.Killer)
	victim := playerName(e.Victim)
	if victim == "" {
		return
	}
	killerTeam := ""
	if e.Killer != nil {
		killerTeam = teamString(e.Killer.Team)
	}
	victimTeam := ""
	if e.Victim != nil {
		victimTeam = teamString(e.Victim.Team)
	}
	weapon := weaponName(e.Weapon)
	isKnife := e.Weapon != nil && e.Weapon.Type == common.EqKnife
	isZeus := e.Weapon != nil && e.Weapon.Type == common.EqZeus
	isAirborne := false
	if e.Killer != nil {
		isAirborne = e.Killer.IsAirborne()
	}

	s.mu.Lock()
	k := Kill{
		Time:         time.Now(),
		Killer:       killer,
		KillerTeam:   killerTeam,
		Victim:       victim,
		VictimTeam:   victimTeam,
		Weapon:       weapon,
		Headshot:     e.IsHeadshot,
		Wallbang:     e.PenetratedObjects > 0,
		Noscope:      e.NoScope,
		ThroughSmoke: e.ThroughSmoke,
		BlindKill:    e.AttackerBlind,
		InAir:        isAirborne,
	}
	if e.Assister != nil {
		k.Assister = playerName(e.Assister)
		k.AssisterTeam = teamString(e.Assister.Team)
		k.FlashAssist = e.AssistedFlash
	}
	s.appendKill(k)

	// Killer counters
	if killer != "" && killer != victim {
		p := s.ensurePlayer(killer)
		p.Kills++
		if killerTeam != "" {
			p.Team = killerTeam
		}
		if e.IsHeadshot {
			p.HeadshotKills++
		}
		if isZeus {
			p.ZeusKills++
		} else if isKnife {
			p.KnifeKills++
		}
	}

	// Victim counters
	p := s.ensurePlayer(victim)
	p.Deaths++
	p.Alive = false
	if victimTeam != "" {
		p.Team = victimTeam
	}

	// Assist counter (flash assists do not award a scoreboard assist in CS2)
	if e.Assister != nil && !e.AssistedFlash {
		ap := s.ensurePlayer(playerName(e.Assister))
		ap.Assists++
		if t := teamString(e.Assister.Team); t != "" {
			ap.Team = t
		}
	}
	s.mu.Unlock()
	s.notify()
}

func (w *eventWiring) onPlayerHurt(e events.PlayerHurt) {
	if e.Attacker == nil || e.Player == nil {
		return
	}
	name := playerName(e.Attacker)
	if name == "" || name == playerName(e.Player) {
		return
	}
	// Friendly fire still counts as damage dealt in CS2; filtering is left to
	// the scoreboard presentation layer.
	dmg := e.HealthDamageTaken
	isGrenade := e.Weapon != nil && e.Weapon.Class() == common.EqClassGrenade
	s := w.state
	s.mu.Lock()
	p := s.ensurePlayer(name)
	p.Damage += float64(dmg)
	if isGrenade {
		p.UD += float64(dmg)
	}
	s.mu.Unlock()
	s.notify()
}

func (w *eventWiring) onPlayerFlashed(e events.PlayerFlashed) {
	if e.Attacker == nil || e.Player == nil {
		return
	}
	if e.Attacker == e.Player {
		return
	}
	// Only count enemy flashes over the EF threshold.
	if e.Attacker.Team == e.Player.Team {
		return
	}
	dur := e.FlashDuration()
	if dur < w.efThreshold {
		return
	}
	s := w.state
	s.mu.Lock()
	p := s.ensurePlayer(playerName(e.Attacker))
	p.EF++
	s.mu.Unlock()
	s.notify()
}

func (w *eventWiring) onRoundStart(e events.RoundStart) {
	s := w.state
	s.mu.Lock()
	for _, p := range s.stats {
		p.Alive = true
		p.HasBomb = false
		// Clear per-round weapons; they'll be repopulated via ItemPickup/Equip.
		p.Weapons = make(map[string]bool)
	}
	// Pull canonical score/map/rounds from GameState for accuracy.
	gs := w.parser.GameState()
	if gs != nil {
		if gs.TeamCounterTerrorists() != nil {
			s.ctScore = gs.TeamCounterTerrorists().Score()
		}
		if gs.TeamTerrorists() != nil {
			s.tScore = gs.TeamTerrorists().Score()
		}
		s.round = gs.TotalRoundsPlayed() + 1
		s.inWarmup = gs.IsWarmupPeriod()
		s.markMetaDirty()
	}
	s.mu.Unlock()
	s.notify()
	_ = e
}

func (w *eventWiring) onRoundEnd(e events.RoundEnd) {
	s := w.state
	reason := roundEndReasonString(e.Reason)
	winner := ""
	switch e.Winner {
	case common.TeamCounterTerrorists:
		winner = "CT"
	case common.TeamTerrorists:
		winner = "T"
	}
	gs := w.parser.GameState()
	matchStarted := gs != nil && gs.IsMatchStarted()

	s.mu.Lock()
	// Round history (winner + reason). Match-started gate avoids warmup pollution.
	if winner != "" && matchStarted {
		s.rounds = append(s.rounds, RoundResult{Round: s.round, Winner: winner, Reason: reason})
		s.roundsPlayed++
	}
	// Sync scores from GameState (TeamState.Score() has already incremented).
	if gs != nil {
		if gs.TeamCounterTerrorists() != nil {
			s.ctScore = gs.TeamCounterTerrorists().Score()
		}
		if gs.TeamTerrorists() != nil {
			s.tScore = gs.TeamTerrorists().Score()
		}
	}
	s.markMetaDirty()
	ct, t := s.ctScore, s.tScore
	cb := s.roundEndFn
	name := s.serverName
	s.mu.Unlock()

	// Round-win killfeed line, e.g. "CT wins — 5:3". Skip warmup rounds and
	// non-team wins (draws). addSystemMessage calls notify() internally.
	if winner != "" && matchStarted {
		s.addSystemMessage(fmt.Sprintf("%s wins — %d:%d", winner, ct, t))
	} else {
		s.notify()
	}

	if cb != nil {
		go cb(RoundEndInfo{ServerName: name, CT: ct, T: t})
	}
}

// roundEndReasonString renders the demoinfocs RoundEndReason using the
// vocabulary the panel UI + DB already understand.
func roundEndReasonString(r events.RoundEndReason) string {
	switch r {
	case events.RoundEndReasonTargetBombed:
		return "bomb"
	case events.RoundEndReasonBombDefused:
		return "defuse"
	case events.RoundEndReasonCTWin, events.RoundEndReasonTerroristsWin:
		return "elimination"
	case events.RoundEndReasonTargetSaved:
		return "time"
	}
	return ""
}

func (w *eventWiring) onRoundMVP(e events.RoundMVPAnnouncement) {
	if e.Player == nil {
		return
	}
	s := w.state
	s.mu.Lock()
	p := s.ensurePlayer(playerName(e.Player))
	p.MVPs++
	s.mu.Unlock()
	s.notify()
}

func (w *eventWiring) onMatchStartedChanged(e events.MatchStartedChanged) {
	// The Source 2 entity-driven signal. events.MatchStart is the Source 1
	// game-event counterpart; on CSTV+ both can dispatch for the same match
	// start and produce a duplicate "Match Started" line — we intentionally
	// only subscribe to this one.
	if !e.OldIsStarted && e.NewIsStarted {
		w.state.resetWithMessage("Match Started")
	}
}

func (w *eventWiring) onAnnouncementWinPanel(_ events.AnnouncementWinPanelMatch) {
	s := w.state
	s.addSystemMessage("Game Over")
	if s.gameOverFn == nil {
		return
	}
	score := s.GetScore()
	scoreboard := s.GetScoreboard()
	slog.Info("gametracker: game over", "server", s.serverName,
		"ct", score.CT, "t", score.T, "rounds", len(score.Rounds), "players", len(scoreboard))
	cb := s.gameOverFn
	name := s.serverName
	go cb(GameOverInfo{ServerName: name, Score: score, Players: scoreboard})
}

func (w *eventWiring) onPlayerConnect(e events.PlayerConnect) {
	if e.Player == nil {
		return
	}
	w.recordConnect(e.Player, false)
}
func (w *eventWiring) onBotConnect(e events.BotConnect) {
	if e.Player == nil {
		return
	}
	w.recordConnect(e.Player, true)
}
func (w *eventWiring) recordConnect(pl *common.Player, isBot bool) {
	name := playerName(pl)
	if name == "" {
		return
	}
	s := w.state
	s.mu.Lock()
	p := s.ensurePlayer(name)
	p.Online = true
	p.IsBot = isBot || pl.IsBot
	if t := teamString(pl.Team); t != "" {
		p.Team = t
	}
	s.mu.Unlock()
	s.notify()
}

func (w *eventWiring) onPlayerDisconnected(e events.PlayerDisconnected) {
	if e.Player == nil {
		return
	}
	name := playerName(e.Player)
	s := w.state
	s.mu.Lock()
	if p, ok := s.stats[name]; ok {
		if p.IsBot {
			delete(s.stats, name)
		} else {
			p.Online = false
			p.Weapons = make(map[string]bool)
		}
	}
	s.mu.Unlock()
	s.notify()
}

func (w *eventWiring) onPlayerTeamChange(e events.PlayerTeamChange) {
	if e.Player == nil {
		return
	}
	name := playerName(e.Player)
	s := w.state
	s.mu.Lock()
	p := s.ensurePlayer(name)
	p.Team = teamString(e.NewTeam)
	s.mu.Unlock()
	s.notify()
}

func (w *eventWiring) onChatMessage(e events.ChatMessage) {
	if e.Sender == nil {
		return
	}
	text := strings.TrimSpace(e.Text)
	if text != ".ready" {
		return
	}
	s := w.state
	s.mu.RLock()
	cb := s.readyFn
	name := s.serverName
	s.mu.RUnlock()
	if cb == nil {
		return
	}
	go cb(name, playerName(e.Sender), teamString(e.Sender.Team))
}

func (w *eventWiring) onWarmupChanged(e events.IsWarmupPeriodChanged) {
	s := w.state
	s.mu.Lock()
	if s.inWarmup != e.NewIsWarmupPeriod {
		s.inWarmup = e.NewIsWarmupPeriod
		s.markMetaDirty()
	}
	s.mu.Unlock()
	s.notify()
}

func (w *eventWiring) onConVarsUpdated(e events.ConVarsUpdated) {
	paused := false
	seenPauseCvar := false
	for k, v := range e.UpdatedConVars {
		if k == "mp_pause_match" {
			seenPauseCvar = true
			paused = v == "1" || strings.EqualFold(v, "true")
		}
	}
	if !seenPauseCvar {
		return
	}
	s := w.state
	s.mu.Lock()
	if s.isPaused != paused {
		s.isPaused = paused
		s.markMetaDirty()
	}
	s.mu.Unlock()
	s.notify()
}

func (w *eventWiring) onBombPlanted(e events.BombPlanted) {
	msg := "Bomb Planted"
	if e.Player != nil && e.Player.Name != "" {
		msg = e.Player.Name + " planted the bomb"
	}
	w.state.addSystemMessage(msg)
}

func (w *eventWiring) onBombDefused(e events.BombDefused) {
	msg := "Bomb Defused"
	if e.Player != nil && e.Player.Name != "" {
		msg = e.Player.Name + " defused the bomb"
	}
	w.state.addSystemMessage(msg)
}

func (w *eventWiring) onBombExplode(_ events.BombExplode) {
	w.state.addSystemMessage("Bomb Exploded")
}

func (w *eventWiring) onDemoHeader(m *msg.CDemoFileHeader) {
	mapName := m.GetMapName()
	if mapName == "" {
		return
	}
	s := w.state
	s.mu.Lock()
	if s.currentMap != mapName {
		s.currentMap = mapName
		s.markMetaDirty()
	}
	s.mu.Unlock()
	s.notify()
}

// onTeamSideSwitch marks halftime and captures the halfRound bookkeeping used
// for overtime detection and the scoreboard's round divider. TeamSideSwitch
// is preferred over GameHalfEnded for two reasons: (1) it fires only on
// *actual* side swaps (regulation + each OT half), not on match end, so
// "Half Time" messaging isn't fired at the final round; (2) by the time
// the game-phase entity has advanced far enough to dispatch TeamSideSwitch,
// the final round's TeamState.Score() update has settled, so ctScore+tScore
// is authoritative (GameHalfEnded races with score updates on some Source 2
// demos, which produced a one-round-early divider).
//
// Per-player Team fields are corrected by the next snapshotParticipants pass.
func (w *eventWiring) onTeamSideSwitch(_ events.TeamSideSwitch) {
	s := w.state
	s.mu.Lock()
	if s.halfRound == 0 {
		played := s.ctScore + s.tScore
		if played == 0 {
			played = len(s.rounds)
		}
		s.halfRound = played
		s.maxRounds = played * 2
		s.markMetaDirty()
		slog.Info("gametracker: half ended", "server", s.serverName, "round", played, "max_rounds", s.maxRounds)
	}
	s.mu.Unlock()
	s.addSystemMessage("Half Time")
}

// onFrameDone is the authoritative loadout/money/alive pump. CS2 broadcasts
// don't reliably fire ItemPickup/Drop/Equip (library: "not available in all
// demos"), and we want money/armor/helmet/defuser tracking that doesn't
// depend on Source 1 game events. The parser's entity-graph view is always
// current, so we just snapshot from it at a bounded rate.
func (w *eventWiring) onFrameDone(_ events.FrameDone) {
	now := w.parser.CurrentTime()
	// CurrentTime() can go backwards briefly on broadcast reconnect; treat
	// any non-forward delta as "poll now" so we don't starve.
	if now >= w.lastSnapshot && now-w.lastSnapshot < w.snapshotInterval {
		return
	}
	w.lastSnapshot = now
	gs := w.parser.GameState()
	if gs == nil {
		return
	}
	parts := gs.Participants()
	if parts == nil {
		return
	}
	if w.state.snapshotParticipants(parts.All()) {
		w.state.notify()
	}
}


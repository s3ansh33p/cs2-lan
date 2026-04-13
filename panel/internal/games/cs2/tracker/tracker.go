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
	"html"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"unilan/internal/games"
	"unilan/internal/games/cs2/cstv"

	demoinfocs "github.com/markus-wa/demoinfocs-golang/v5/pkg/demoinfocs"
	"github.com/markus-wa/demoinfocs-golang/v5/pkg/demoinfocs/common"
	"github.com/markus-wa/demoinfocs-golang/v5/pkg/demoinfocs/events"
	"github.com/markus-wa/demoinfocs-golang/v5/pkg/demoinfocs/msg"
)

// Delta is one time-tagged state change published to subscribers over the
// game WebSocket. The payload is nested under "d" so its keys can't collide
// with the wrapper — important because the player-update payload uses "k"
// for kill count and the score payload uses "t" for the T-side score, both
// of which would otherwise clobber the wrapper's kind/timestamp.
//
// Wire format: {"t":<game_time_ms>,"k":<kind>,"d":{...payload}}
//   - kind "p": player update, d merges into _game.players[n]
//   - kind "hurt": damage event; d includes hp/ar post-damage
//   - kind "fire": weapon fire (shooter + weapon)
//   - kind "flash": flash event (victim, attacker, duration)
//   - kind "inferno_start" / "inferno_end": molotov fire on the ground
//   - kind "kill": killfeed entry (killJSON-shape payload)
//   - kind "sys": system killfeed message (msg, time)
//   - kind "score": scoreboard snapshot (scoreJSON-shape payload)
//   - kind "ready": ready-state snapshot (readyStateJSON-shape payload)
//   - kind "leave": player removed from scoreboard (bot disconnect)
type Delta struct {
	T    int64          `json:"t"`
	Kind string         `json:"k"`
	D    map[string]any `json:"d,omitempty"`
}

// KillPayload converts a Kill to the map shape the client expects in a "kill"
// delta. The field names match the ws-layer killJSON wire format so the same
// renderer path drives both the snapshot killfeed and the delta stream.
func KillPayload(k Kill) map[string]any {
	p := map[string]any{
		"killer": html.EscapeString(k.Killer),
		"victim": html.EscapeString(k.Victim),
		"weapon": k.Weapon,
		"time":   k.Time.Format("15:04:05"),
	}
	if t := shortTeam(k.KillerTeam); t != "" {
		p["kt"] = t
	}
	if t := shortTeam(k.VictimTeam); t != "" {
		p["vt"] = t
	}
	if k.Headshot {
		p["hs"] = true
	}
	if k.Wallbang {
		p["wb"] = true
	}
	if k.Noscope {
		p["ns"] = true
	}
	if k.BlindKill {
		p["bk"] = true
	}
	if k.InAir {
		p["ia"] = true
	}
	if k.ThroughSmoke {
		p["ts"] = true
	}
	if k.Assister != "" {
		p["assist"] = html.EscapeString(k.Assister)
		if t := shortTeam(k.AssisterTeam); t != "" {
			p["at"] = t
		}
		if k.FlashAssist {
			p["fa"] = true
		}
	}
	if k.IsSystem {
		p["sys"] = true
		p["msg"] = html.EscapeString(k.Message)
	}
	return p
}

// shortTeam maps internal team strings to the short codes the client uses.
func shortTeam(t string) string {
	switch t {
	case "TERRORIST":
		return "T"
	case "SPECTATOR":
		return "S"
	case "CT":
		return "CT"
	}
	return ""
}

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
	Level         int // arms race level (state-tracked from kill events)
	LevelKills    int // arms race kills at current level (resets on level up)
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

	// Delta buffer — event-stream payload that accumulates between flushes.
	// Drained by the WS layer on each notify. See Delta/appendDelta/DrainDeltas.
	deltaMu  sync.Mutex
	deltaBuf []Delta

	// Flush timers — FrameDone-idle detection aligned to CSTV fragment end.
	// armFlushTimer() resets flushTimer on every FrameDone; after 200 ms of
	// no frames (fragment boundary), flushNow fires and notifies subscribers.
	// ceilingTimer guarantees a flush even if FrameDones stop arriving (parser
	// stall, server paused) so the client doesn't starve.
	flushTimer   *time.Timer
	ceilingTimer *time.Timer

	// deltaTimeMs is the parser's in-game wall clock in ms, updated by the
	// eventWiring at the top of each handler (and every FrameDone). appendDelta
	// stamps deltas with this value so the client can replay a batch at
	// real-time pace — parsing a 3 s CSTV fragment takes ~60 ms wall-time, so
	// using time.Now() would collapse the whole fragment into a 60 ms replay.
	// Game-time preserves the 3 s spread so P90 ticks stay 200 ms apart.
	// Accessed via atomic ops; zero means "fall back to wall-time" for callers
	// outside the parser loop (e.g. ResetStats from web context).
	deltaTimeMs int64

	// parserCancelCh is closed by signalParserCancel to kick the tracker's
	// watchdog goroutine into tearing down the current parser (e.g. on
	// AnnouncementWinPanelMatch — we don't want to wait for the HTTP read
	// to time out on an abandoned token). The restart loop rotates this via
	// newParserCancel on every attach, so a stale close from a prior match
	// never carries over. cancelMu guards the swap and serialises close.
	cancelMu       sync.Mutex
	parserCancelCh chan struct{}
}

// newParserCancel installs a fresh cancel channel for the next parser attach
// and returns it. Must be called before the watchdog subscribes so any
// pending signal from the previous match is dropped with the old channel.
func (s *ServerState) newParserCancel() <-chan struct{} {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	s.parserCancelCh = make(chan struct{})
	return s.parserCancelCh
}

// signalParserCancel closes the current cancel channel (idempotent, safe to
// call from any goroutine). If no parser is attached yet the call is a no-op.
func (s *ServerState) signalParserCancel() {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	ch := s.parserCancelCh
	if ch == nil {
		return
	}
	select {
	case <-ch:
		// Already closed — nothing to do.
	default:
		close(ch)
	}
}

// flush cadence — FrameDone-idle debounce + ceiling. See armFlushTimer.
const (
	flushDebounce = 200 * time.Millisecond
	flushCeiling  = 4 * time.Second
)

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

// setDeltaTime updates the game-time clock used to stamp subsequent deltas.
// Called by eventWiring at the top of every parser handler.
func (s *ServerState) setDeltaTime(ms int64) {
	atomic.StoreInt64(&s.deltaTimeMs, ms)
}

// appendDelta buffers a delta for the next flush. Uses the parser's in-game
// clock (via setDeltaTime) so batched events preserve their real 3 s spread
// rather than collapsing into the 50 ms of wall-time it takes the parser to
// digest a fragment. Falls back to wall-time when called from outside the
// parser loop (e.g. ResetStats from a web request).
func (s *ServerState) appendDelta(kind string, payload map[string]any) {
	t := atomic.LoadInt64(&s.deltaTimeMs)
	if t <= 0 {
		t = time.Now().UnixMilli()
	}
	d := Delta{T: t, Kind: kind, D: payload}
	s.deltaMu.Lock()
	s.deltaBuf = append(s.deltaBuf, d)
	s.deltaMu.Unlock()
}

// pushPlayer emits a "p" (player update) delta with the given fields. The
// name is set on the payload automatically.
func (s *ServerState) pushPlayer(name string, fields map[string]any) {
	if name == "" {
		return
	}
	fields["n"] = html.EscapeString(name)
	s.appendDelta("p", fields)
}

// pushScoreDelta emits a "score" delta with the current scoreboard snapshot
// (round, ct/t scores, rounds[], halfRound, etc.). Called by event handlers
// that mutate match-level state (round start/end, warmup/pause flips,
// halftime, map change). Client replaces _game.score wholesale on apply.
func (s *ServerState) pushScoreDelta() {
	info := s.GetScore()
	rounds := make([]map[string]any, 0, len(info.Rounds))
	for _, r := range info.Rounds {
		rounds = append(rounds, map[string]any{
			"r":  r.Round,
			"w":  r.Winner,
			"rs": r.Reason,
		})
	}
	round := info.Round
	if round == 0 {
		round = 1
	}
	payload := map[string]any{
		"round":  round,
		"ct":     info.CT,
		"t":      info.T,
		"rounds": rounds,
	}
	if info.GameMode != "" {
		payload["mode"] = info.GameMode
	}
	if info.CurrentMap != "" {
		payload["map"] = html.EscapeString(info.CurrentMap)
	}
	if info.HalfRound != 0 {
		payload["half"] = info.HalfRound
	}
	if info.MaxRounds != 0 {
		payload["maxRounds"] = info.MaxRounds
	}
	if info.InWarmup {
		payload["warmup"] = true
	}
	if info.IsPaused {
		payload["paused"] = true
	}
	s.appendDelta("score", payload)
}

// DrainDeltas removes and returns the buffered deltas. Called by the WS layer
// after a notify signal.
func (s *ServerState) DrainDeltas() []Delta {
	s.deltaMu.Lock()
	defer s.deltaMu.Unlock()
	if len(s.deltaBuf) == 0 {
		return nil
	}
	out := s.deltaBuf
	s.deltaBuf = nil
	return out
}

// armFlushTimer resets the fragment-boundary debounce. Called at the end of
// every onFrameDone — a pause in FrameDones triggers flushNow, which notifies
// subscribers if any deltas are buffered. The ceiling timer ensures we flush
// at most every flushCeiling even if FrameDones never stop (which in practice
// can't happen, but guards against pathological streams).
func (s *ServerState) armFlushTimer() {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	if s.flushTimer == nil {
		s.flushTimer = time.AfterFunc(flushDebounce, s.flushNow)
	} else {
		s.flushTimer.Reset(flushDebounce)
	}
	if s.ceilingTimer == nil {
		s.ceilingTimer = time.AfterFunc(flushCeiling, s.flushNow)
	}
}

// flushNow triggers a notify if the delta buffer has anything in it. Also
// resets the ceiling timer so the next window starts from now.
func (s *ServerState) flushNow() {
	s.subMu.Lock()
	if s.ceilingTimer != nil {
		s.ceilingTimer.Stop()
		s.ceilingTimer = nil
	}
	s.subMu.Unlock()
	s.deltaMu.Lock()
	has := len(s.deltaBuf) > 0
	s.deltaMu.Unlock()
	if has {
		s.notify()
	}
}

// stopFlushTimers halts both flush timers (called on MarkStopped).
func (s *ServerState) stopFlushTimers() {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	if s.flushTimer != nil {
		s.flushTimer.Stop()
		s.flushTimer = nil
	}
	if s.ceilingTimer != nil {
		s.ceilingTimer.Stop()
		s.ceilingTimer = nil
	}
}

// MarkStopped marks this server as stopped and notifies all subscribers.
func (s *ServerState) MarkStopped() {
	s.stopFlushTimers()
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
// team, hp) into PlayerStats, emitting a "p" delta per player with just the
// fields that moved. The parser provides live-updated entity properties for
// all of these, which is strictly more reliable than the Source 1
// ItemPickup/Drop/Equip events that don't fire on CS2 CSTV+.
//
// Called every FrameDone (no throttle); most frames produce no deltas since
// entity fields rarely move tick-to-tick.
func (s *ServerState) snapshotParticipants(players []*common.Player) {
	type playerUpdate struct {
		name   string
		fields map[string]any
	}
	var updates []playerUpdate

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
		fields := map[string]any{}

		if t := teamString(pl.Team); t != "" && p.Team != t {
			p.Team = t
			fields["team"] = shortTeam(t)
		}
		if p.IsBot != pl.IsBot {
			p.IsBot = pl.IsBot
			fields["bot"] = pl.IsBot
		}
		if pl.IsConnected && !p.Online {
			p.Online = true
			fields["online"] = true
		}
		alive := pl.IsAlive()
		if p.Alive != alive {
			p.Alive = alive
			fields["alive"] = alive
		}
		money := pl.Money()
		if p.Money != money {
			p.Money = money
			fields["money"] = money
		}
		ping := pl.Ping()
		if p.Ping != ping {
			p.Ping = ping
			fields["ping"] = ping
		}
		health := pl.Health()
		if p.Health != health {
			p.Health = health
			fields["hp"] = health
		}
		armor := pl.Armor()
		if p.Armor != armor {
			p.Armor = armor
			fields["ar"] = armor
		}
		hasArmor := armor > 0
		if p.HasArmor != hasArmor {
			p.HasArmor = hasArmor
			// Client infers HasArmor from ar > 0, but keep the explicit bool
			// in the delta for parity with the snapshot JSON shape.
			fields["armor"] = hasArmor
		}
		helmet := pl.HasHelmet()
		if p.HasHelmet != helmet {
			p.HasHelmet = helmet
			fields["helmet"] = helmet
		}
		defuser := pl.HasDefuseKit()
		if p.HasDefuser != defuser {
			p.HasDefuser = defuser
			fields["defuser"] = defuser
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
			fields["bomb"] = hasBomb
		}
		if !weaponsEqual(p.Weapons, newWeapons) {
			p.Weapons = newWeapons
			// Emit sorted weapons + grenades so the client can replace lists
			// cleanly. Copy ps method logic.
			weapons := []string{}
			grenades := []string{}
			for w := range newWeapons {
				if IsGrenade(w) {
					grenades = append(grenades, w)
				} else if !IsEquipment(w) && w != "c4" && w != "weapon_c4" {
					weapons = append(weapons, w)
				}
			}
			sort.Strings(weapons)
			sort.Strings(grenades)
			fields["weapons"] = weapons
			fields["grenades"] = grenades
		}

		if len(fields) > 0 {
			updates = append(updates, playerUpdate{name: name, fields: fields})
		}
	}
	s.mu.Unlock()

	// Emit deltas outside the state lock.
	for _, u := range updates {
		s.pushPlayer(u.name, u.fields)
	}
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
	now := time.Now()
	s.mu.Lock()
	s.appendKill(Kill{Time: now, IsSystem: true, Message: msg})
	s.mu.Unlock()
	s.appendDelta("sys", map[string]any{
		"msg":  html.EscapeString(msg),
		"time": now.Format("15:04:05"),
	})
	// System messages (match started, bomb planted, half time, etc.) are user-
	// visible and should land at the next fragment boundary — the FrameDone
	// idle timer handles that. No direct notify() here; flush trigger covers it.
}

// ResetStats zeroes all player stats and scores, keeping players connected.
func (s *ServerState) ResetStats() {
	s.resetWithMessage("Match Restarted — Ready Up")
}

// resetForNewMatch is the map-change / new-broadcast variant of reset: it
// drops every player entry instead of just zeroing stats, so stale bots and
// humans who didn't carry over (a common case when CS2 changes map and the
// bot slate is re-rolled) don't linger on the scoreboard. The new parser's
// snapshotParticipants + onPlayerConnect/onBotConnect will repopulate the
// roster as entities show up in the fresh CSTV stream. Use this over
// resetWithMessage whenever the parser has been torn down and re-attached.
func (s *ServerState) resetForNewMatch(reason string) {
	now := time.Now()
	var leaveNames []string
	s.mu.Lock()
	for name := range s.stats {
		leaveNames = append(leaveNames, name)
	}
	s.stats = make(map[string]*PlayerStats)
	s.round = 0
	s.ctScore = 0
	s.tScore = 0
	s.rounds = nil
	s.halfRound = 0
	s.maxRounds = 0
	s.isPaused = false
	s.roundsPlayed = 0
	s.markMetaDirty()
	s.appendKill(Kill{Time: now, IsSystem: true, Message: reason})
	s.mu.Unlock()

	// Emit a tombstone per player so the client drops them from its model.
	// Names are already sanitised on ingest; escape again for parity with
	// the onPlayerDisconnected "leave" delta shape.
	for _, name := range leaveNames {
		s.appendDelta("leave", map[string]any{"n": html.EscapeString(name)})
	}
	s.pushScoreDelta()
	s.appendDelta("sys", map[string]any{
		"msg":  html.EscapeString(reason),
		"time": now.Format("15:04:05"),
	})
}

func (s *ServerState) resetWithMessage(reason string) {
	now := time.Now()
	var resetNames []string
	s.mu.Lock()
	for name, p := range s.stats {
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
		resetNames = append(resetNames, name)
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
	s.appendKill(Kill{Time: now, IsSystem: true, Message: reason})
	s.mu.Unlock()

	// Zero-out per-player deltas so the client scoreboard clears.
	for _, name := range resetNames {
		s.pushPlayer(name, map[string]any{
			"k": 0, "d": 0, "a": 0, "mvp": 0, "ef": 0, "ud": 0,
			"hsp": 0, "kdr": 0, "adr": 0,
			"knifek": 0, "zeusk": 0, "level": 0,
			"money": 0, "hp": 0, "ar": 0,
			"armor": false, "helmet": false, "defuser": false, "bomb": false,
			"alive":    true,
			"weapons":  []string{},
			"grenades": []string{},
		})
	}
	s.pushScoreDelta()
	s.appendDelta("sys", map[string]any{
		"msg":  html.EscapeString(reason),
		"time": now.Format("15:04:05"),
	})
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
	firstAttach := true

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

		// Re-attaching to a fresh CSTV match (map change, mp_restartgame,
		// server hop). Drop the entire carried-over roster and score — the
		// old bot slate rarely lines up with the new map, and humans who
		// didn't make it across would otherwise linger as offline ghosts.
		// The new parser's snapshotParticipants + connect events rebuild
		// the scoreboard from scratch. Arm the flush timer so the "leave"
		// tombstones reach subscribers even before the first FrameDone.
		if !firstAttach {
			state.resetForNewMatch("New Match")
			state.armFlushTimer()
		}
		firstAttach = false

		// ParseToEnd blocks. Cancel from a watchdog goroutine on any of:
		//   - ctx cancelled (tracker shutting down)
		//   - relay saw a token flip (new CSTV match, parser is pinned to old)
		//   - game-over handler flipped parserCancelled (AnnouncementWinPanel)
		// The tokenFlip flag distinguishes a clean hand-off (keep relay state
		// so the already-buffered new-match fragments survive) from a real
		// teardown (evict so the next attach starts from a known-empty slate).
		parserDone := make(chan struct{})
		watchdogDone := make(chan struct{})
		var tokenFlip bool
		// Install a fresh cancel channel BEFORE spawning the watchdog, so any
		// signal from the prior match is dropped with the old channel.
		parserCancelSig := state.newParserCancel()
		go func() {
			defer close(watchdogDone)
			select {
			case <-ctx.Done():
				parser.Cancel()
				if m.relay != nil {
					m.relay.Close(name)
				}
			case <-m.relay.TokenChanged(name):
				tokenFlip = true
				parser.Cancel()
			case <-parserCancelSig:
				parser.Cancel()
			case <-parserDone:
			}
		}()

		err = parser.ParseToEnd()
		close(parserDone)
		<-watchdogDone
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Info("gametracker: broadcast parser ended", "server", name, "err", err, "token_flip", tokenFlip)
		}

		// On a token flip the new match's fragments are already landing in
		// the relay — keep them so the next parser can /sync straight onto
		// the new token. Otherwise evict and wait for fresh signup.
		if !tokenFlip && m.relay != nil {
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
}

// stamp updates the state's delta-time clock to the parser's current game-time.
// Called at the top of every event handler so any delta emitted by this
// handler (or downstream helpers) is tagged with game-time, not wall-time —
// see ServerState.deltaTimeMs for the rationale.
func (w *eventWiring) stamp() {
	w.state.setDeltaTime(w.parser.CurrentTime().Milliseconds())
}

func (w *eventWiring) register() {
	if w.efThreshold == 0 {
		w.efThreshold = 700 * time.Millisecond
	}
	p := w.parser

	p.RegisterEventHandler(w.onKill)
	p.RegisterEventHandler(w.onPlayerHurt)
	p.RegisterEventHandler(w.onPlayerFlashed)
	p.RegisterEventHandler(w.onWeaponFire)
	p.RegisterEventHandler(w.onInfernoStart)
	p.RegisterEventHandler(w.onInfernoExpired)
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
	w.stamp()
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

	// Snapshot counter values under lock so the deltas emitted below are
	// consistent with what the client's aggregate scoreboard will show.
	var killerFields, victimFields, assisterFields map[string]any
	var assisterName string

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
		// Arms race level: knife kill = +1 level outright; any other kill
		// counts toward the 2-kill threshold for the current weapon tier.
		levelChanged := false
		if s.gameMode == "armsrace" {
			oldLevel := p.Level
			if isKnife {
				p.Level++
				p.LevelKills = 0
			} else {
				p.LevelKills++
				if p.LevelKills >= 2 {
					p.Level++
					p.LevelKills = 0
				}
			}
			levelChanged = p.Level != oldLevel
		}
		killerFields = map[string]any{"k": p.Kills}
		if e.IsHeadshot {
			// HS% is derived client-side from k + headshot count; since the
			// client JSON exposes hsp as a percentage, derive here.
			if p.Kills > 0 {
				killerFields["hsp"] = float64(p.HeadshotKills) / float64(p.Kills) * 100
			}
		}
		if isZeus {
			killerFields["zeusk"] = p.ZeusKills
		}
		if isKnife {
			killerFields["knifek"] = p.KnifeKills
		}
		if levelChanged {
			killerFields["level"] = p.Level
		}
		if killerTeam != "" {
			killerFields["team"] = shortTeam(killerTeam)
		}
	}

	p := s.ensurePlayer(victim)
	p.Deaths++
	p.Alive = false
	if victimTeam != "" {
		p.Team = victimTeam
	}
	// Arms race: knife death demotes the victim one level (floor 0).
	if s.gameMode == "armsrace" && isKnife && p.Level > 0 {
		p.Level--
		p.LevelKills = 0
	}
	victimFields = map[string]any{"d": p.Deaths, "alive": false, "hp": 0}
	// KDR derived for victim too (deaths changed).
	if p.Deaths > 0 && p.Kills > 0 {
		victimFields["kdr"] = float64(p.Kills) / float64(p.Deaths)
	}
	if s.gameMode == "armsrace" && isKnife {
		// Only emit when the demotion rule actually ran (Level mutated or
		// was already 0); send the current value so the client re-syncs.
		victimFields["level"] = p.Level
	}
	if victimTeam != "" {
		victimFields["team"] = shortTeam(victimTeam)
	}

	if e.Assister != nil && !e.AssistedFlash {
		ap := s.ensurePlayer(playerName(e.Assister))
		ap.Assists++
		if t := teamString(e.Assister.Team); t != "" {
			ap.Team = t
		}
		assisterName = playerName(e.Assister)
		assisterFields = map[string]any{"a": ap.Assists}
		if t := teamString(e.Assister.Team); t != "" {
			assisterFields["team"] = shortTeam(t)
		}
	}
	s.mu.Unlock()

	// kill delta — the killfeed entry — carries tick-accurate timing so the
	// client's replay shows the kill at the moment it happened within the
	// fragment window.
	s.appendDelta("kill", KillPayload(k))
	if killerFields != nil {
		s.pushPlayer(killer, killerFields)
	}
	s.pushPlayer(victim, victimFields)
	if assisterFields != nil {
		s.pushPlayer(assisterName, assisterFields)
	}
}

func (w *eventWiring) onPlayerHurt(e events.PlayerHurt) {
	w.stamp()
	if e.Player == nil {
		return
	}
	victim := playerName(e.Player)
	if victim == "" {
		return
	}
	attackerName := ""
	if e.Attacker != nil {
		attackerName = playerName(e.Attacker)
	}
	dmg := e.HealthDamageTaken
	isGrenade := e.Weapon != nil && e.Weapon.Class() == common.EqClassGrenade
	weapon := weaponName(e.Weapon)

	// Post-damage hp/armor from the entity graph (e.Player reflects the hit).
	newHP := e.Player.Health()
	newAR := e.Player.Armor()

	s := w.state
	s.mu.Lock()
	// Victim hp/ar always updated so the scoreboard reflects the hit.
	vp := s.ensurePlayer(victim)
	vp.Health = newHP
	vp.Armor = newAR
	vp.HasArmor = newAR > 0
	if !e.Player.IsAlive() {
		vp.Alive = false
	}

	// Attacker damage/util counters (friendly fire still counts).
	var attackerFields map[string]any
	if attackerName != "" && attackerName != victim {
		ap := s.ensurePlayer(attackerName)
		ap.Damage += float64(dmg)
		if isGrenade {
			ap.UD += float64(dmg)
		}
		attackerFields = map[string]any{}
		if isGrenade {
			attackerFields["ud"] = ap.UD
		}
	}
	s.mu.Unlock()

	// hurt delta — tick-accurate damage event, carries new hp/ar so the client
	// replay ticks the health bar down with the bullet's timing.
	payload := map[string]any{
		"v":   html.EscapeString(victim),
		"hp":  newHP,
		"ar":  newAR,
		"dmg": dmg,
	}
	if attackerName != "" {
		payload["a"] = html.EscapeString(attackerName)
	}
	if weapon != "" {
		payload["w"] = weapon
	}
	s.appendDelta("hurt", payload)

	if len(attackerFields) > 0 {
		s.pushPlayer(attackerName, attackerFields)
	}
}

func (w *eventWiring) onPlayerFlashed(e events.PlayerFlashed) {
	w.stamp()
	if e.Attacker == nil || e.Player == nil {
		return
	}
	if e.Attacker == e.Player {
		return
	}
	victim := playerName(e.Player)
	attacker := playerName(e.Attacker)
	dur := e.FlashDuration()

	// flash delta — emitted for every enemy flash, including short ones.
	// Client stubs it (no UI in v1) but the model records it.
	if e.Attacker.Team != e.Player.Team {
		w.state.appendDelta("flash", map[string]any{
			"v": html.EscapeString(victim),
			"a": html.EscapeString(attacker),
			"d": dur.Seconds(),
		})
	}

	// EF stat counter — only over threshold flashes count.
	if e.Attacker.Team == e.Player.Team || dur < w.efThreshold {
		return
	}
	s := w.state
	s.mu.Lock()
	p := s.ensurePlayer(attacker)
	p.EF++
	ef := p.EF
	s.mu.Unlock()
	s.pushPlayer(attacker, map[string]any{"ef": ef})
}

// onWeaponFire emits a `fire` delta per shot. Noisy (~30/sec during active
// fights) but the client stubs it; enables shot-by-shot cast features later.
func (w *eventWiring) onWeaponFire(e events.WeaponFire) {
	w.stamp()
	if e.Shooter == nil {
		return
	}
	name := playerName(e.Shooter)
	if name == "" {
		return
	}
	w.state.appendDelta("fire", map[string]any{
		"n": html.EscapeString(name),
		"w": weaponName(e.Weapon),
	})
}

// onInfernoStart fires when a molotov/incendiary's fire patch is created on
// the ground. Client stubs in v1.
func (w *eventWiring) onInfernoStart(e events.InfernoStart) {
	w.stamp()
	payload := map[string]any{}
	if e.Inferno != nil && e.Inferno.Thrower() != nil {
		payload["a"] = html.EscapeString(e.Inferno.Thrower().Name)
	}
	w.state.appendDelta("inferno_start", payload)
}

// onInfernoExpired fires when a fire patch burns out. Client stubs in v1.
func (w *eventWiring) onInfernoExpired(_ events.InfernoExpired) {
	w.stamp()
	w.state.appendDelta("inferno_end", map[string]any{})
}

func (w *eventWiring) onRoundStart(e events.RoundStart) {
	w.stamp()
	s := w.state
	var resetNames []string
	s.mu.Lock()
	for name, p := range s.stats {
		p.Alive = true
		p.HasBomb = false
		// Clear per-round weapons; they'll be repopulated from entity snapshot.
		p.Weapons = make(map[string]bool)
		resetNames = append(resetNames, name)
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

	// Per-player reset deltas — alive=true, empty weapons/grenades, bomb=false.
	for _, name := range resetNames {
		s.pushPlayer(name, map[string]any{
			"alive":    true,
			"bomb":     false,
			"weapons":  []string{},
			"grenades": []string{},
		})
	}
	s.pushScoreDelta()
	_ = e
}

func (w *eventWiring) onRoundEnd(e events.RoundEnd) {
	w.stamp()
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

	// score delta — carries updated ct/t/round/rounds[] so the client can
	// render the round-history bar as soon as the round settles.
	s.pushScoreDelta()

	// Round-win killfeed line, e.g. "CT wins — 5:3". Skip warmup rounds and
	// non-team wins (draws). addSystemMessage also emits a `sys` delta.
	if winner != "" && matchStarted {
		s.addSystemMessage(fmt.Sprintf("%s wins — %d:%d", winner, ct, t))
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
	w.stamp()
	if e.Player == nil {
		return
	}
	name := playerName(e.Player)
	s := w.state
	s.mu.Lock()
	p := s.ensurePlayer(name)
	p.MVPs++
	mvps := p.MVPs
	s.mu.Unlock()
	s.pushPlayer(name, map[string]any{"mvp": mvps})
}

func (w *eventWiring) onMatchStartedChanged(e events.MatchStartedChanged) {
	w.stamp()
	// The Source 2 entity-driven signal. events.MatchStart is the Source 1
	// game-event counterpart; on CSTV+ both can dispatch for the same match
	// start and produce a duplicate "Match Started" line — we intentionally
	// only subscribe to this one.
	if !e.OldIsStarted && e.NewIsStarted {
		w.state.resetWithMessage("Match Started")
	}
}

func (w *eventWiring) onAnnouncementWinPanel(_ events.AnnouncementWinPanelMatch) {
	w.stamp()
	s := w.state
	s.addSystemMessage("Game Over")
	score := s.GetScore()
	scoreboard := s.GetScoreboard()
	slog.Info("gametracker: game over", "server", s.serverName,
		"ct", score.CT, "t", score.T, "rounds", len(score.Rounds), "players", len(scoreboard))
	if s.gameOverFn != nil {
		cb := s.gameOverFn
		name := s.serverName
		go cb(GameOverInfo{ServerName: name, Score: score, Players: scoreboard})
	}
	// Kick the restart loop so it re-attaches to whatever CSTV match CS2
	// starts next (map change, mp_restartgame, nextlevel). Without this the
	// parser blocks on an abandoned token until its HTTP read times out —
	// a multi-second window where no deltas reach the UI.
	s.signalParserCancel()
}

func (w *eventWiring) onPlayerConnect(e events.PlayerConnect) {
	w.stamp()
	if e.Player == nil {
		return
	}
	w.recordConnect(e.Player, false)
}
func (w *eventWiring) onBotConnect(e events.BotConnect) {
	w.stamp()
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
	bot := p.IsBot
	team := p.Team
	if t := teamString(pl.Team); t != "" {
		p.Team = t
		team = t
	}
	s.mu.Unlock()
	fields := map[string]any{"online": true, "bot": bot}
	if team != "" {
		fields["team"] = shortTeam(team)
	}
	s.pushPlayer(name, fields)
}

func (w *eventWiring) onPlayerDisconnected(e events.PlayerDisconnected) {
	w.stamp()
	if e.Player == nil {
		return
	}
	name := playerName(e.Player)
	s := w.state
	var removed bool
	s.mu.Lock()
	if p, ok := s.stats[name]; ok {
		if p.IsBot {
			delete(s.stats, name)
			removed = true
		} else {
			p.Online = false
			p.Weapons = make(map[string]bool)
		}
	}
	s.mu.Unlock()
	if removed {
		// Tombstone delta — client drops the player from its model.
		s.appendDelta("leave", map[string]any{"n": html.EscapeString(name)})
	} else {
		s.pushPlayer(name, map[string]any{
			"online":   false,
			"weapons":  []string{},
			"grenades": []string{},
		})
	}
}

func (w *eventWiring) onPlayerTeamChange(e events.PlayerTeamChange) {
	w.stamp()
	if e.Player == nil {
		return
	}
	name := playerName(e.Player)
	newTeam := teamString(e.NewTeam)
	s := w.state
	s.mu.Lock()
	p := s.ensurePlayer(name)
	p.Team = newTeam
	s.mu.Unlock()
	s.pushPlayer(name, map[string]any{"team": shortTeam(newTeam)})
}

func (w *eventWiring) onChatMessage(e events.ChatMessage) {
	w.stamp()
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
	w.stamp()
	s := w.state
	changed := false
	s.mu.Lock()
	if s.inWarmup != e.NewIsWarmupPeriod {
		s.inWarmup = e.NewIsWarmupPeriod
		s.markMetaDirty()
		changed = true
	}
	s.mu.Unlock()
	if changed {
		s.pushScoreDelta()
	}
}

func (w *eventWiring) onConVarsUpdated(e events.ConVarsUpdated) {
	w.stamp()
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
	changed := false
	s.mu.Lock()
	if s.isPaused != paused {
		s.isPaused = paused
		s.markMetaDirty()
		changed = true
	}
	s.mu.Unlock()
	if changed {
		s.pushScoreDelta()
	}
}

func (w *eventWiring) onBombPlanted(e events.BombPlanted) {
	w.stamp()
	msg := "Bomb Planted"
	if e.Player != nil && e.Player.Name != "" {
		msg = e.Player.Name + " planted the bomb"
	}
	w.state.addSystemMessage(msg)
}

func (w *eventWiring) onBombDefused(e events.BombDefused) {
	w.stamp()
	msg := "Bomb Defused"
	if e.Player != nil && e.Player.Name != "" {
		msg = e.Player.Name + " defused the bomb"
	}
	w.state.addSystemMessage(msg)
}

func (w *eventWiring) onBombExplode(_ events.BombExplode) {
	w.stamp()
	w.state.addSystemMessage("Bomb Exploded")
}

func (w *eventWiring) onDemoHeader(m *msg.CDemoFileHeader) {
	w.stamp()
	mapName := m.GetMapName()
	if mapName == "" {
		return
	}
	s := w.state
	changed := false
	s.mu.Lock()
	if s.currentMap != mapName {
		s.currentMap = mapName
		s.markMetaDirty()
		changed = true
	}
	s.mu.Unlock()
	if changed {
		s.pushScoreDelta()
	}
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
	w.stamp()
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
	// Half-round changed: push score so the UI divider lands correctly.
	s.pushScoreDelta()
	s.addSystemMessage("Half Time")
}

// onFrameDone is the authoritative loadout/money/hp/armor/alive pump. CS2
// broadcasts don't reliably fire ItemPickup/Drop/Equip (library: "not
// available in all demos"), and we want live entity-property tracking that
// doesn't depend on Source 1 game events. The parser's entity-graph view is
// always current, so we snapshot it every frame — snapshotParticipants diffs
// against the previous state and only emits deltas for fields that moved, so
// idle frames are effectively free.
//
// armFlushTimer() at the end is the fragment-boundary flush trigger: it
// resets a 200 ms debounce that fires once FrameDones pause (i.e. the parser
// has consumed the fragment and is waiting for the next POST). See
// ServerState.armFlushTimer for the full rationale.
func (w *eventWiring) onFrameDone(_ events.FrameDone) {
	// Stamp the parser's current game-time on the state so every delta emitted
	// by this and subsequent handlers carries the right wall clock for client
	// replay. Without this, a 3 s fragment collapses into ~60 ms of replay.
	w.state.setDeltaTime(w.parser.CurrentTime().Milliseconds())

	gs := w.parser.GameState()
	if gs == nil {
		return
	}
	parts := gs.Participants()
	if parts == nil {
		return
	}
	w.state.snapshotParticipants(parts.All())
	w.state.armFlushTimer()
}


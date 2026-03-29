package gametracker

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kill represents a kill event or a system message in the killfeed.
type Kill struct {
	Time       time.Time
	Killer     string
	KillerTeam string // "CT", "TERRORIST"
	Victim     string
	VictimTeam string // "CT", "TERRORIST"
	Weapon     string
	Headshot   bool
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
	Name      string
	Team      string          // "CT", "TERRORIST", or ""
	Kills     int
	Deaths    int
	Assists   int
	Weapons   map[string]bool // current loadout
	Online    bool            // connected to server
	IsBot     bool
	Ping      int             // from RCON polling
	Duration  string          // from RCON polling
	Address   string          // from RCON polling
	Money     int             // from round_stats JSON
	AccountID string          // Steam account ID for JSON mapping
	HasArmor  bool
	HasHelmet bool
	HasDefuser bool
	HasBomb    bool
	Alive      bool
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

func IsGrenade(w string) bool  { return grenadeSet[w] }
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

// WeaponPrice maps weapon names to their buy cost in CS2.
var WeaponPrice = map[string]int{
	// Pistols
	"glock": 200, "usp_silencer": 200, "hkp2000": 200, "p250": 300,
	"fiveseven": 500, "tec9": 500, "cz75a": 500, "deagle": 700,
	"elite": 300, "revolver": 600,
	// SMGs
	"mac10": 1050, "mp9": 1250, "mp7": 1500, "mp5sd": 1500,
	"ump45": 1200, "p90": 2350, "bizon": 1400,
	// Rifles
	"famas": 2050, "galilar": 1800, "ak47": 2700, "m4a1": 3100,
	"m4a1_silencer": 2900, "aug": 3300, "sg556": 3000,
	"ssg08": 1700, "awp": 4750, "scar20": 5000, "g3sg1": 5000,
	// Heavy
	"nova": 1050, "xm1014": 2000, "mag7": 1300, "sawedoff": 1100,
	"m249": 5200, "negev": 1700,
	// Gear
	"kevlar": 650, "assaultsuit": 1000, "defuser": 400, "taser": 200,
	// Grenades
	"hegrenade": 300, "flashbang": 200, "smokegrenade": 300,
	"molotov": 400, "incgrenade": 600, "decoy": 50,
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
	mu       sync.RWMutex
	kills    []Kill
	stats    map[string]*PlayerStats
	maxKills int
	round    int
	ctScore  int
	tScore   int
	rounds   []RoundResult // round history
	gameMode         string
	currentMap       string
	halfRound        int // round number where half-time occurred
	inWarmup         bool

	// JSON block accumulator for round_stats
	jsonBuf    []string
	inJSON     bool

	// Player mappings for JSON round_stats
	accountMap map[string]string // accountID -> name (for human players)
	slotMap    map[int]string    // player slot ID -> name (for all players including bots)

	// Change notification
	subMu   sync.Mutex
	subs    []chan struct{}
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
	InWarmup   bool
}

func (s *ServerState) GetScore() ScoreInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rounds := make([]RoundResult, len(s.rounds))
	copy(rounds, s.rounds)
	return ScoreInfo{Round: s.round, CT: s.ctScore, T: s.tScore, GameMode: s.gameMode, CurrentMap: s.currentMap, Rounds: rounds, HalfRound: s.halfRound, InWarmup: s.inWarmup}
}


func newServerState() *ServerState {
	return &ServerState{
		stats:      make(map[string]*PlayerStats),
		accountMap: make(map[string]string),
		slotMap:    make(map[int]string),
		inWarmup:   true,
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
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default: // don't block if subscriber is slow
		}
	}
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

// KillCount returns the total number of killfeed entries.
func (s *ServerState) KillCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.kills)
}

// GetKillsSince returns kills added after index `since` (oldest first).
func (s *ServerState) GetKillsSince(since int) []Kill {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if since >= len(s.kills) {
		return nil
	}
	if since < 0 {
		since = 0
	}
	result := make([]Kill, len(s.kills)-since)
	copy(result, s.kills[since:])
	return result
}

func (s *ServerState) GetScoreboard() []PlayerStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]PlayerStats, 0, len(s.stats))
	for _, ps := range s.stats {
		cp := *ps
		// Copy the weapons map
		if ps.Weapons != nil {
			cp.Weapons = make(map[string]bool, len(ps.Weapons))
			for k, v := range ps.Weapons {
				cp.Weapons[k] = v
			}
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
	if len(s.kills) > s.maxKills {
		s.kills = s.kills[len(s.kills)-s.maxKills:]
	}
}

func (s *ServerState) ensurePlayer(name string) *PlayerStats {
	if _, ok := s.stats[name]; !ok {
		s.stats[name] = &PlayerStats{Name: name, Weapons: make(map[string]bool), Online: true, Alive: true}
	}
	return s.stats[name]
}

type KillModifiers struct {
	Headshot, Wallbang, Noscope, BlindKill, InAir, ThroughSmoke bool
}

func (s *ServerState) recordKill(killer, killerTeam, victim, victimTeam, weapon string, mods KillModifiers) {
	s.mu.Lock()
	k := Kill{
		Time: time.Now(), Killer: killer, KillerTeam: killerTeam,
		Victim: victim, VictimTeam: victimTeam,
		Weapon: weapon, Headshot: mods.Headshot, Wallbang: mods.Wallbang,
		Noscope: mods.Noscope, BlindKill: mods.BlindKill,
		InAir: mods.InAir, ThroughSmoke: mods.ThroughSmoke,
	}
	s.appendKill(k)
	if killer != "" {
		p := s.ensurePlayer(killer)
		p.Kills++
		if killerTeam != "" {
			p.Team = killerTeam
		}
	}
	p := s.ensurePlayer(victim)
	p.Deaths++
	p.Alive = false
	if victimTeam != "" {
		p.Team = victimTeam
	}
	p.Weapons = make(map[string]bool)
	s.mu.Unlock()
	s.notify()
}

func (s *ServerState) recordSuicide(name, team, weapon string) {
	s.mu.Lock()
	k := Kill{
		Time: time.Now(), Killer: name, KillerTeam: team,
		Victim: name, VictimTeam: team,
		Weapon: weapon,
	}
	s.appendKill(k)

	p := s.ensurePlayer(name)
	p.Deaths++
	p.Alive = false
	if team != "" {
		p.Team = team
	}
	p.Weapons = make(map[string]bool)
	s.mu.Unlock()
	s.notify()
}

func (s *ServerState) recordBombKill(name, team string) {
	s.mu.Lock()
	k := Kill{
		Time: time.Now(), Killer: "", Victim: name, VictimTeam: team,
		Weapon: "planted_c4",
	}
	s.appendKill(k)
	p := s.ensurePlayer(name)
	p.Deaths++
	p.Alive = false
	if team != "" {
		p.Team = team
	}
	p.Weapons = make(map[string]bool)
	s.mu.Unlock()
	s.notify()
}

func (s *ServerState) recordAssist(assister, team, victim string, flash bool) {
	s.mu.Lock()
	p := s.ensurePlayer(assister)
	p.Assists++
	if team != "" {
		p.Team = team
	}
	// Attach assister to the most recent kill of this victim
	for i := len(s.kills) - 1; i >= 0; i-- {
		if s.kills[i].Victim == victim && !s.kills[i].IsSystem {
			s.kills[i].Assister = assister
			s.kills[i].AssisterTeam = team
			s.kills[i].FlashAssist = flash
			break
		}
	}
	s.mu.Unlock()
	s.notify()
}

func (s *ServerState) recordPurchase(name, team, weapon string) {
	s.mu.Lock()
	p := s.ensurePlayer(name)
	if team != "" {
		p.Team = team
	}
	// Deduct cost from money
	if price, ok := WeaponPrice[weapon]; ok && p.Money >= price {
		p.Money -= price
	}
	// Normalize weapon name
	weapon = strings.ToLower(strings.TrimPrefix(weapon, "weapon_"))
	// Add weapon/equipment to loadout
	switch {
	case weapon == "kevlar":
		p.HasArmor = true
	case weapon == "assaultsuit":
		p.HasArmor = true
		p.HasHelmet = true
	case weapon == "defuser":
		p.HasDefuser = true
	case strings.HasPrefix(weapon, "knife"):
		// skip knives
	case weapon == "c4":
		p.HasBomb = true
	default:
		p.Weapons[weapon] = true
	}
	s.mu.Unlock()
	s.notify()
}

// recordBuyzone sets a player's full loadout from "left buyzone" log line.
// items is the bracket content, e.g. "weapon_knife weapon_hkp2000 defuser kevlar(100) helmet"
func (s *ServerState) recordBuyzone(name, team, items string) {
	s.mu.Lock()
	p := s.ensurePlayer(name)
	if team != "" {
		p.Team = team
	}
	p.Weapons = make(map[string]bool)
	p.HasArmor = false
	p.HasHelmet = false
	p.HasDefuser = false
	p.HasBomb = false

	for _, item := range strings.Fields(items) {
		item = strings.ToLower(strings.TrimPrefix(item, "weapon_"))
		if strings.HasPrefix(item, "kevlar") {
			p.HasArmor = true
			continue
		}
		if item == "helmet" {
			p.HasHelmet = true
			continue
		}
		if item == "defuser" {
			p.HasDefuser = true
			continue
		}
		if item == "c4" {
			p.HasBomb = true
			continue
		}
		// Skip knives
		if strings.HasPrefix(item, "knife") {
			continue
		}
		p.Weapons[item] = true
	}
	s.mu.Unlock()
	s.notify()
}

func (s *ServerState) recordThrow(name, team, weapon string) {
	s.mu.Lock()
	p := s.ensurePlayer(name)
	if team != "" {
		p.Team = team
	}
	delete(p.Weapons, weapon)
	s.mu.Unlock()
	s.notify()
}

func (s *ServerState) recordTeamSwitch(name, team string) {
	s.mu.Lock()
	p := s.ensurePlayer(name)
	p.Team = team
	s.mu.Unlock()
	s.notify()
}

func (s *ServerState) recordConnect(name string, isBot bool) {
	s.mu.Lock()
	p := s.ensurePlayer(name)
	p.Online = true
	p.IsBot = isBot
	s.mu.Unlock()
	s.notify()
}

func (s *ServerState) recordDisconnect(name string) {
	s.mu.Lock()
	if p, ok := s.stats[name]; ok {
		p.Online = false
		p.Weapons = make(map[string]bool)
	}
	s.mu.Unlock()
	s.notify()
}

// SyncRCON updates ping/duration/address from RCON status data.
func (s *ServerState) SyncRCON(rconPlayers map[string]RCONPlayerInfo) {
	s.mu.Lock()
	for name, info := range rconPlayers {
		p := s.ensurePlayer(name)
		p.Ping = info.Ping
		p.Duration = info.Duration
		p.Address = info.Address
		p.IsBot = info.IsBot
		p.Online = true
	}
	s.mu.Unlock()
	s.notify()
}

// RCONPlayerInfo holds supplementary data from RCON status.
type RCONPlayerInfo struct {
	IsBot    bool
	Ping     int
	Duration string
	Address  string
}

func (s *ServerState) clearWeaponsOnRound() {
	s.mu.Lock()
	for _, p := range s.stats {
		p.Weapons = make(map[string]bool)
		p.HasBomb = false
		p.Alive = true
	}
	s.mu.Unlock()
	s.notify()
}

// parseRoundStats processes the accumulated JSON round_stats block.
// Updates money for each player and cross-checks scores.
func (s *ServerState) parseRoundStats(lines []string) {
	// Join all lines, strip log prefixes
	var fields string
	playerLines := make(map[string]string)
	roundNum, scoreT, scoreCT := 0, 0, 0
	var mapName string

	for _, line := range lines {
		// Strip "L MM/DD/YYYY - HH:MM:SS: " prefix
		if idx := strings.Index(line, ": "); idx >= 0 {
			line = strings.TrimSpace(line[idx+2:])
		}
		// Remove surrounding quotes for key-value lines
		if strings.HasPrefix(line, "\"round_number\"") {
			fmt.Sscanf(line, `"round_number" : "%d"`, &roundNum)
		} else if strings.HasPrefix(line, "\"score_t\"") {
			fmt.Sscanf(line, `"score_t" : "%d"`, &scoreT)
		} else if strings.HasPrefix(line, "\"score_ct\"") {
			fmt.Sscanf(line, `"score_ct" : "%d"`, &scoreCT)
		} else if strings.HasPrefix(line, "\"map\"") {
			// "map" : "de_dust2"
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				mapName = strings.Trim(strings.TrimSpace(parts[1]), "\" ,")
			}
		} else if strings.HasPrefix(line, "\"fields\"") {
			// Extract field names
			if idx := strings.Index(line, ":"); idx >= 0 {
				fields = strings.Trim(strings.TrimSpace(line[idx+1:]), "\"")
			}
		} else if strings.HasPrefix(line, "\"player_") {
			// "player_0" : "  914801619, 2, 1000, ..."
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				key := strings.Trim(strings.TrimSpace(parts[0]), "\"")
				val := strings.Trim(strings.TrimSpace(parts[1]), "\"")
				playerLines[key] = val
			}
		}
	}

	_ = fields // fields header tells us column order, but it's always the same

	s.mu.Lock()
	defer s.mu.Unlock()

	if roundNum > 0 {
		s.round = roundNum
	}
	if mapName != "" {
		s.currentMap = mapName
	}
	s.ctScore = scoreCT
	s.tScore = scoreT

	// Parse each player line: accountid, team, money, kills, deaths, assists, ...
	// "player_N" maps to slot ID N in the slotMap
	for key, val := range playerLines {
		parts := strings.Split(val, ",")
		if len(parts) < 6 {
			continue
		}
		accountID := strings.TrimSpace(parts[0])
		teamNum := strings.TrimSpace(parts[1])
		money := 0
		fmt.Sscanf(strings.TrimSpace(parts[2]), "%d", &money)

		team := ""
		switch teamNum {
		case "2":
			team = "TERRORIST"
		case "3":
			team = "CT"
		}

		// Resolve player name: try slot ID first (works for bots), then account ID
		var playerName string

		// Extract slot from "player_N"
		slotID := -1
		fmt.Sscanf(key, "player_%d", &slotID)
		if slotID >= 0 {
			playerName = s.slotMap[slotID]
		}

		// Fall back to account ID for human players
		if playerName == "" && accountID != "0" {
			playerName = s.accountMap[accountID]
		}

		if playerName == "" {
			continue
		}

		if p, ok := s.stats[playerName]; ok {
			p.Money = money
			if team != "" {
				p.Team = team
			}
		}
	}
}

func (s *ServerState) addSystemMessage(msg string) {
	s.mu.Lock()
	s.appendKill(Kill{Time: time.Now(), IsSystem: true, Message: msg})
	s.mu.Unlock()
	s.notify()
}

func (s *ServerState) resetWithMessage(reason string) {
	s.mu.Lock()
	s.stats = make(map[string]*PlayerStats)
	s.round = 0
	s.ctScore = 0
	s.tScore = 0
	s.rounds = nil
	s.halfRound = 0
	s.appendKill(Kill{Time: time.Now(), IsSystem: true, Message: reason})
	s.mu.Unlock()
	s.notify()
}

// LogStreamFunc returns a channel of log lines for a container.
type LogStreamFunc func(ctx context.Context, name string) (<-chan string, func(), error)

// RCONFunc sends an RCON command to a server.
type RCONFunc func(addr, password, command string) (string, error)

// Manager manages game trackers for all servers.
type Manager struct {
	mu       sync.Mutex
	servers  map[string]*ServerState
	cancels  map[string]context.CancelFunc
	streamFn LogStreamFunc
	rconFn   RCONFunc
}

func NewManager(streamFn LogStreamFunc, rconFn RCONFunc) *Manager {
	return &Manager{
		servers:  make(map[string]*ServerState),
		cancels:  make(map[string]context.CancelFunc),
		streamFn: streamFn,
		rconFn:   rconFn,
	}
}

func (m *Manager) TrackServer(name string, gamePort int, rconPassword, gameMode string) *ServerState {
	m.mu.Lock()
	if s, ok := m.servers[name]; ok {
		m.mu.Unlock()
		return s
	}

	s := newServerState()
	s.gameMode = gameMode
	m.servers[name] = s
	ctx, cancel := context.WithCancel(context.Background())
	m.cancels[name] = cancel
	m.mu.Unlock()

	go m.setupAndTrack(ctx, name, gamePort, rconPassword, s)
	return s
}

func (m *Manager) GetState(name string) *ServerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.servers[name]
}

func (m *Manager) StopTracking(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancel, ok := m.cancels[name]; ok {
		cancel()
		delete(m.cancels, name)
		delete(m.servers, name)
	}
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, cancel := range m.cancels {
		cancel()
		delete(m.cancels, name)
		delete(m.servers, name)
	}
}

func (m *Manager) setupAndTrack(ctx context.Context, name string, gamePort int, rconPassword string, state *ServerState) {
	addr := fmt.Sprintf("localhost:%d", gamePort)
	for _, cmd := range []string{"sv_logecho 1", "log on", "mp_logdetail 3"} {
		resp, err := m.rconFn(addr, rconPassword, cmd)
		if err != nil {
			log.Printf("gametracker %s: rcon %q: %v", name, cmd, err)
		} else if resp != "" {
			log.Printf("gametracker %s: rcon %q -> %s", name, cmd, resp)
		}
	}
	log.Printf("gametracker %s: mode=%s, logging enabled, starting log stream", name, state.gameMode)

	// Start RCON poller for ping/duration (single goroutine per server)
	go m.rconPoller(ctx, name, addr, rconPassword, state)

	for {
		lines, cleanup, err := m.streamFn(ctx, name)
		if err != nil {
			log.Printf("gametracker %s: stream error: %v", name, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		for {
			select {
			case <-ctx.Done():
				cleanup()
				return
			case line, ok := <-lines:
				if !ok {
					log.Printf("gametracker %s: stream ended, reconnecting...", name)
					cleanup()
					select {
					case <-ctx.Done():
						return
					case <-time.After(2 * time.Second):
					}
					break
				}
				parseLine(line, state)
			}
		}
	}
}

// rconPoller polls RCON status every 5s to update ping/duration for players.
// Runs once per tracked server regardless of how many viewers are connected.
func (m *Manager) rconPoller(ctx context.Context, name, addr, rconPassword string, state *ServerState) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			resp, err := m.rconFn(addr, rconPassword, "status")
			if err != nil {
				continue
			}
			status := parseRCONStatus(resp)
			if len(status) > 0 {
				state.SyncRCON(status)
			}
		}
	}
}

// parseRCONStatus extracts player ping/duration from RCON status output.
func parseRCONStatus(raw string) map[string]RCONPlayerInfo {
	result := make(map[string]RCONPlayerInfo)
	lines := strings.Split(raw, "\n")
	inPlayers := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "---------players") {
			inPlayers = true
			continue
		}
		if trimmed == "#end" {
			break
		}
		if !inPlayers || strings.HasPrefix(trimmed, "id") || strings.Contains(trimmed, "[NoChan]") {
			continue
		}

		if m := rconPlayerRe.FindStringSubmatch(trimmed); m != nil {
			ping := 0
			fmt.Sscanf(m[3], "%d", &ping)
			isBot := m[2] == "BOT"
			info := RCONPlayerInfo{
				IsBot:    isBot,
				Ping:     ping,
				Duration: m[2],
				Address:  m[7],
			}
			result[m[8]] = info
		}
	}
	return result
}

var rconPlayerRe = regexp.MustCompile(`^\s*(\d+)\s+(\S+)\s+(\d+)\s+(\d+)\s+(\w+)\s+(\d+)\s+(?:(\S+)\s+)?'(.+?)'`)

// Log line patterns — all game event lines start with "L MM/DD/YYYY"
var (
	// Kill: "killer<id><steamid><TEAM>" ... killed "victim<id><steamid><TEAM>" ... with "weapon" (headshot)?
	killRe = regexp.MustCompile(`"(.+?)<(\d+)><(.+?)><(CT|TERRORIST|Unassigned)?>".*killed "(.+?)<(\d+)><(.+?)><(CT|TERRORIST|Unassigned)?>".*with "(.+?)"(.*)`)

	// Assist
	assistRe      = regexp.MustCompile(`"(.+?)<\d+><.+?><(CT|TERRORIST)?>" assisted killing "(.+?)<`)
	flashAssistRe = regexp.MustCompile(`"(.+?)<\d+><.+?><(CT|TERRORIST)?>" flash-assisted killing "(.+?)<`)

	// Purchase: "player<id><steamid><TEAM>" purchased "weapon"
	purchaseRe = regexp.MustCompile(`"(.+?)<\d+><.+?><(CT|TERRORIST)>" purchased "(.+?)"`)

	// Left buyzone: "player<id><steamid><TEAM>" left buyzone with [ items... ]
	buyzoneRe = regexp.MustCompile(`"(.+?)<(\d+)><(.+?)><(CT|TERRORIST)>" left buyzone with \[\s*(.+?)\s*\]`)

	// Killed by bomb: "player<id><steamid><TEAM>" was killed by the bomb.
	bombKillRe = regexp.MustCompile(`"(.+?)<(\d+)><(.+?)><(CT|TERRORIST|Unassigned)?>".*was killed by the bomb`)

	// Suicide: "player<id><steamid><TEAM>" committed suicide with "weapon"
	suicideRe = regexp.MustCompile(`"(.+?)<(\d+)><(.+?)><(CT|TERRORIST|Unassigned)?>" .* committed suicide with "(.+?)"`)

	// Killed by world/bomb: "player<id><steamid><TEAM>" was killed by ...  (less common)
	// Also handles: triggered "Killed_A_Hostage", blinded by, etc.

	// Player triggered: Got_The_Bomb, Dropped_The_Bomb, Planted_The_Bomb, Defused_The_Bomb
	bombActionRe = regexp.MustCompile(`"(.+?)<\d+><.+?><(CT|TERRORIST)>" triggered "(Got_The_Bomb|Dropped_The_Bomb|Planted_The_Bomb|Defused_The_Bomb)"`)

	// Threw grenade
	threwRe = regexp.MustCompile(`"(.+?)<\d+><.+?><(CT|TERRORIST)>" threw\s+(\w+)`)

	// Team switch
	teamSwitchRe = regexp.MustCompile(`"(.+?)<\d+><.+?><.+?>" switched from team \S+ to (CT|TERRORIST)`)

	// Player entered
	enteredRe = regexp.MustCompile(`"(.+?)<(\d+)><(BOT|.+?)><.*?>" entered the game`)

	// Player disconnected
	disconnectRe = regexp.MustCompile(`"(.+?)<\d+><.+?><.+?>" disconnected`)

	// World events
	worldRe = regexp.MustCompile(`World triggered "(Match_Start|Round_Start|Game_Over|Warmup_Start|Warmup_End|Game_Halftime)"`)

	// Team win: Team "CT" triggered "SFUI_Notice_CTs_Win" (CT "5") (T "3")
	teamWinRe = regexp.MustCompile(`Team "(CT|TERRORIST)" triggered "(SFUI_Notice_\w+)" \(CT "(\d+)"\) \(T "(\d+)"\)`)

	// MatchStatus: Score: 4:2 on map "de_dust2" RoundsPlayed: 6
	matchStatusRe = regexp.MustCompile(`MatchStatus: Score: (\d+):(\d+) on map ".+?" RoundsPlayed: (\d+)`)

	// Map change: Started map "de_dust2"
	mapChangeRe = regexp.MustCompile(`Started map "(.+?)"`)

	// Extract account ID from steam ID like [U:1:914801619]
	accountIDRe = regexp.MustCompile(`\[U:\d+:(\d+)\]`)
)

func parseLine(line string, state *ServerState) {
	// JSON round_stats block accumulation
	if strings.Contains(line, "JSON_BEGIN{") {
		state.mu.Lock()
		state.inJSON = true
		state.jsonBuf = nil
		state.mu.Unlock()
		return
	}
	if strings.Contains(line, "}}JSON_END") {
		state.mu.Lock()
		lines := state.jsonBuf
		state.inJSON = false
		state.jsonBuf = nil
		state.mu.Unlock()
		if len(lines) > 0 {
			state.parseRoundStats(lines)
			state.notify()
		}
		return
	}
	state.mu.RLock()
	inJSON := state.inJSON
	state.mu.RUnlock()
	if inJSON {
		state.mu.Lock()
		state.jsonBuf = append(state.jsonBuf, line)
		state.mu.Unlock()
		return
	}

	// Early bailout: all game events we parse contain a quoted string
	if !strings.Contains(line, `"`) {
		return
	}

	// Team win
	if m := teamWinRe.FindStringSubmatch(line); m != nil {
		ct, t := 0, 0
		fmt.Sscanf(m[3], "%d", &ct)
		fmt.Sscanf(m[4], "%d", &t)
		trigger := m[2]

		// Map trigger to win reason
		reason := "elimination"
		switch trigger {
		case "SFUI_Notice_Target_Bombed":
			reason = "bomb"
		case "SFUI_Notice_Bomb_Defused":
			reason = "defuse"
		case "SFUI_Notice_Target_Saved":
			reason = "time"
		}

		winner := "CT"
		if m[1] == "TERRORIST" {
			winner = "T"
		}

		state.mu.Lock()
		state.ctScore = ct
		state.tScore = t
		state.rounds = append(state.rounds, RoundResult{
			Round: state.round, Winner: winner, Reason: reason,
		})
		state.mu.Unlock()
		state.addSystemMessage(fmt.Sprintf("%s wins — Score: CT %d - %d T", winner, ct, t))
		return
	}

	// MatchStatus: authoritative score and round count
	if m := matchStatusRe.FindStringSubmatch(line); m != nil {
		ct, t, rounds := 0, 0, 0
		fmt.Sscanf(m[1], "%d", &ct)
		fmt.Sscanf(m[2], "%d", &t)
		fmt.Sscanf(m[3], "%d", &rounds)
		state.mu.Lock()
		state.ctScore = ct
		state.tScore = t
		state.round = rounds
		state.mu.Unlock()
		state.notify()
		return
	}

	// World events
	if m := worldRe.FindStringSubmatch(line); m != nil {
		switch m[1] {
		case "Match_Start":
			state.mu.Lock()
			state.inWarmup = false
			state.mu.Unlock()
		case "Game_Over":
			state.resetWithMessage("Game Over - Stats Reset")
		case "Warmup_Start":
			state.mu.RLock()
			already := state.inWarmup
			state.mu.RUnlock()
			if already {
				return
			}
			state.mu.Lock()
			state.inWarmup = true
			state.mu.Unlock()
			state.resetWithMessage("Warmup Started")
		case "Warmup_End":
			state.mu.Lock()
			state.inWarmup = false
			state.mu.Unlock()
			state.resetWithMessage("Warmup Ended - Stats Reset")
		case "Round_Start":
			state.mu.Lock()
			state.round++
			round := state.round
			state.mu.Unlock()
			state.clearWeaponsOnRound()
			state.addSystemMessage(fmt.Sprintf("Round %d", round))
		case "Game_Halftime":
			state.mu.Lock()
			state.halfRound = state.round
			state.mu.Unlock()
			state.addSystemMessage("Half Time")
		}
		return
	}

	// Map change
	if m := mapChangeRe.FindStringSubmatch(line); m != nil {
		state.mu.Lock()
		state.currentMap = m[1]
		state.mu.Unlock()
		state.resetWithMessage(fmt.Sprintf("Map Changed to %s - Stats Reset", m[1]))
		return
	}

	// Kill — also extracts slot IDs and account IDs for JSON mapping
	if m := killRe.FindStringSubmatch(line); m != nil {
		killerName, killerSlot, killerSteamID, killerTeam := m[1], m[2], m[3], m[4]
		victimName, victimSlot, victimSteamID, victimTeam := m[5], m[6], m[7], m[8]
		weapon := m[9]
		extras := m[10]
		mods := KillModifiers{
			Headshot:     strings.Contains(extras, "headshot"),
			Wallbang:     strings.Contains(extras, "penetrated"),
			Noscope:      strings.Contains(extras, "noscope"),
			BlindKill:    strings.Contains(extras, "blindkill"),
			InAir:        strings.Contains(extras, "inair"),
			ThroughSmoke: strings.Contains(extras, "throughsmoke"),
		}

		mapPlayerIDs(state, killerName, killerSlot, killerSteamID)
		mapPlayerIDs(state, victimName, victimSlot, victimSteamID)

		state.recordKill(killerName, killerTeam, victimName, victimTeam, weapon, mods)
		return
	}

	// Killed by bomb
	if m := bombKillRe.FindStringSubmatch(line); m != nil {
		name, slot, steamID, team := m[1], m[2], m[3], m[4]
		mapPlayerIDs(state, name, slot, steamID)
		state.recordBombKill(name, team)
		return
	}

	// Suicide — skip if player already dead (e.g. bomb kill followed by suicide "world")
	if m := suicideRe.FindStringSubmatch(line); m != nil {
		name, slot, steamID, team, weapon := m[1], m[2], m[3], m[4], m[5]
		mapPlayerIDs(state, name, slot, steamID)
		state.mu.RLock()
		alreadyDead := false
		if p, ok := state.stats[name]; ok {
			alreadyDead = !p.Alive
		}
		state.mu.RUnlock()
		if alreadyDead {
			return
		}
		state.recordSuicide(name, team, weapon)
		return
	}

	// Assist
	if m := flashAssistRe.FindStringSubmatch(line); m != nil {
		state.recordAssist(m[1], m[2], m[3], true)
		return
	}

	if m := assistRe.FindStringSubmatch(line); m != nil {
		state.recordAssist(m[1], m[2], m[3], false)
		return
	}

	// Purchase — deduct money
	if m := purchaseRe.FindStringSubmatch(line); m != nil {
		state.recordPurchase(m[1], m[2], m[3])
		return
	}

	// Left buyzone — definitive loadout
	if m := buyzoneRe.FindStringSubmatch(line); m != nil {
		mapPlayerIDs(state, m[1], m[2], m[3])
		state.recordBuyzone(m[1], m[4], m[5])
		return
	}

	// Threw grenade
	if m := threwRe.FindStringSubmatch(line); m != nil {
		state.recordThrow(m[1], m[2], m[3])
		return
	}

	// Bomb actions
	if m := bombActionRe.FindStringSubmatch(line); m != nil {
		name, team, action := m[1], m[2], m[3]
		state.mu.Lock()
		p := state.ensurePlayer(name)
		if team != "" {
			p.Team = team
		}
		switch action {
		case "Got_The_Bomb":
			p.HasBomb = true
		case "Dropped_The_Bomb":
			p.HasBomb = false
		case "Planted_The_Bomb":
			p.HasBomb = false
		case "Defused_The_Bomb":
			// nothing to update on inventory
		}
		state.mu.Unlock()
		state.notify()
		return
	}

	// Team switch
	if m := teamSwitchRe.FindStringSubmatch(line); m != nil {
		state.recordTeamSwitch(m[1], m[2])
		return
	}

	// Player entered
	if m := enteredRe.FindStringSubmatch(line); m != nil {
		mapPlayerIDs(state, m[1], m[2], m[3])
		isBot := m[3] == "BOT"
		state.recordConnect(m[1], isBot)
		return
	}

	// Player disconnected
	if m := disconnectRe.FindStringSubmatch(line); m != nil {
		state.recordDisconnect(m[1])
		return
	}
}

// mapPlayerIDs maps slot ID and account ID to player name for JSON round_stats lookup.
func mapPlayerIDs(state *ServerState, name, slotStr, steamID string) {
	slotID := 0
	fmt.Sscanf(slotStr, "%d", &slotID)

	state.mu.Lock()
	state.slotMap[slotID] = name
	// Only map account ID for non-bots (bots all have account 0)
	if m := accountIDRe.FindStringSubmatch(steamID); m != nil && m[1] != "0" {
		state.accountMap[m[1]] = name
	}
	state.mu.Unlock()
}

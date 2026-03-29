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
	Time     time.Time
	Killer   string
	Victim   string
	Weapon   string
	Headshot bool
	IsSystem bool   // true for system messages like "Stats Reset"
	Message  string // system message text
}

// PlayerStats tracks per-player game state.
type PlayerStats struct {
	Name     string
	Team     string          // "CT", "TERRORIST", or ""
	Kills    int
	Deaths   int
	Assists  int
	Weapons  map[string]bool // current loadout
	Online   bool            // connected to server
	IsBot    bool
	Ping     int             // from RCON polling
	Duration string          // from RCON polling
	Address  string          // from RCON polling
}

func (p PlayerStats) Score() int {
	return p.Kills*2 + p.Assists
}

// WeaponList returns sorted non-grenade weapons.
func (p PlayerStats) WeaponList() []string {
	var out []string
	for w := range p.Weapons {
		if !IsGrenade(w) && !IsEquipment(w) {
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
	"kevlar": true, "assaultsuit": true, "defuser": true, "taser": true,
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

// ServerState holds the parsed game state for one server.
type ServerState struct {
	mu       sync.RWMutex
	kills    []Kill
	stats    map[string]*PlayerStats
	maxKills int
	round    int // current round number
	ctScore  int
	tScore   int

	// Change notification
	subMu   sync.Mutex
	subs    []chan struct{}
}

// ScoreInfo returns the current round scores.
type ScoreInfo struct {
	Round   int
	CT      int
	T       int
}

func (s *ServerState) GetScore() ScoreInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ScoreInfo{Round: s.round, CT: s.ctScore, T: s.tScore}
}

func newServerState() *ServerState {
	return &ServerState{
		stats:    make(map[string]*PlayerStats),
		maxKills: 100,
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

func (s *ServerState) ensurePlayer(name string) *PlayerStats {
	if _, ok := s.stats[name]; !ok {
		s.stats[name] = &PlayerStats{Name: name, Weapons: make(map[string]bool), Online: true}
	}
	return s.stats[name]
}

func (s *ServerState) recordKill(killer, killerTeam, victim, victimTeam, weapon string, headshot bool) {
	s.mu.Lock()
	k := Kill{
		Time: time.Now(), Killer: killer, Victim: victim,
		Weapon: weapon, Headshot: headshot,
	}
	s.kills = append(s.kills, k)
	if len(s.kills) > s.maxKills {
		s.kills = s.kills[len(s.kills)-s.maxKills:]
	}
	if killer != "" {
		p := s.ensurePlayer(killer)
		p.Kills++
		if killerTeam != "" {
			p.Team = killerTeam
		}
	}
	p := s.ensurePlayer(victim)
	p.Deaths++
	if victimTeam != "" {
		p.Team = victimTeam
	}
	p.Weapons = make(map[string]bool)
	s.mu.Unlock()
	s.notify()
}

func (s *ServerState) recordAssist(assister, team string) {
	s.mu.Lock()
	p := s.ensurePlayer(assister)
	p.Assists++
	if team != "" {
		p.Team = team
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
	p.Weapons[weapon] = true
	s.mu.Unlock()
	s.notify()
}

func (s *ServerState) recordDrop(name, weapon string) {
	s.mu.Lock()
	if p, ok := s.stats[name]; ok {
		delete(p.Weapons, weapon)
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
		if p, ok := s.stats[name]; ok {
			p.Ping = info.Ping
			p.Duration = info.Duration
			p.Address = info.Address
		}
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
	}
	s.mu.Unlock()
	s.notify()
}

func (s *ServerState) addSystemMessage(msg string) {
	s.mu.Lock()
	s.kills = append(s.kills, Kill{
		Time: time.Now(), IsSystem: true, Message: msg,
	})
	if len(s.kills) > s.maxKills {
		s.kills = s.kills[len(s.kills)-s.maxKills:]
	}
	s.mu.Unlock()
	s.notify()
}

func (s *ServerState) resetWithMessage(reason string) {
	s.mu.Lock()
	s.stats = make(map[string]*PlayerStats)
	s.round = 0
	s.ctScore = 0
	s.tScore = 0
	s.kills = append(s.kills, Kill{
		Time:     time.Now(),
		IsSystem: true,
		Message:  reason,
	})
	if len(s.kills) > s.maxKills {
		s.kills = s.kills[len(s.kills)-s.maxKills:]
	}
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

func (m *Manager) TrackServer(name string, gamePort int, rconPassword string) *ServerState {
	m.mu.Lock()
	if s, ok := m.servers[name]; ok {
		m.mu.Unlock()
		return s
	}

	s := newServerState()
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
	log.Printf("gametracker %s: logging enabled, starting log stream", name)

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

// Log line patterns
var (
	// Kill: "killer<id><steamid><TEAM>" ... killed "victim<id><steamid><TEAM>" ... with "weapon" (headshot)?
	killRe = regexp.MustCompile(`"(.+?)<\d+><.+?><(CT|TERRORIST|Unassigned)?>".*killed "(.+?)<\d+><.+?><(CT|TERRORIST|Unassigned)?>".*with "(.+?)"(.*)`)

	// Assist: "player<id><steamid><TEAM>" assisted killing "victim<id><steamid><TEAM>"
	assistRe = regexp.MustCompile(`"(.+?)<\d+><.+?><(CT|TERRORIST)?>" assisted killing`)

	// Purchase: "player<id><steamid><TEAM>" purchased "weapon"
	purchaseRe = regexp.MustCompile(`"(.+?)<\d+><.+?><(CT|TERRORIST)>" purchased "(.+?)"`)

	// Dropped: "player<id><steamid><TEAM>" dropped "weapon"
	dropRe = regexp.MustCompile(`"(.+?)<\d+><.+?><(CT|TERRORIST)>" dropped "(.+?)"`)

	// Threw: "player<id><steamid><TEAM>" threw flashbang/smokegrenade/etc
	threwRe = regexp.MustCompile(`"(.+?)<\d+><.+?><(CT|TERRORIST)>" threw\s+(\w+)`)

	// Picked up: "player<id><steamid><TEAM>" picked up "weapon"
	pickupRe = regexp.MustCompile(`"(.+?)<\d+><.+?><(CT|TERRORIST)>" picked up "(.+?)"`)

	// Team switch: "player<id><steamid><TEAM>" switched from team <old> to <new>
	teamSwitchRe = regexp.MustCompile(`"(.+?)<\d+><.+?><.+?>" switched from team \S+ to (CT|TERRORIST)`)

	// Player entered: "player<id><steamid><>" entered the game
	enteredRe = regexp.MustCompile(`"(.+?)<\d+><(BOT|.+?)><.*?>" entered the game`)

	// Player disconnected: "player<id><steamid><TEAM>" disconnected
	disconnectRe = regexp.MustCompile(`"(.+?)<\d+><.+?><.+?>" disconnected`)

	// World events
	worldRe = regexp.MustCompile(`World triggered "(Match_Start|Round_Start|Game_Over|Warmup_End)"`)

	// Team win: Team "CT" triggered "SFUI_Notice_CTs_Win" (CT "5") (T "3")
	teamWinRe = regexp.MustCompile(`Team "(CT|TERRORIST)" triggered "SFUI_Notice_\w+" \(CT "(\d+)"\) \(T "(\d+)"\)`)

	// MatchStatus: Score: 4:2 on map "de_dust2" RoundsPlayed: 6
	matchStatusRe = regexp.MustCompile(`MatchStatus: Score: (\d+):(\d+) on map ".+?" RoundsPlayed: (\d+)`)

	// Map change: Started map "de_dust2"
	mapChangeRe = regexp.MustCompile(`Started map "(.+?)"`)
)

func parseLine(line string, state *ServerState) {
	// Team win: extract scores from the log line
	// e.g. Team "CT" triggered "SFUI_Notice_CTs_Win" (CT "5") (T "3")
	if m := teamWinRe.FindStringSubmatch(line); m != nil {
		ct, t := 0, 0
		fmt.Sscanf(m[2], "%d", &ct)
		fmt.Sscanf(m[3], "%d", &t)
		state.mu.Lock()
		state.ctScore = ct
		state.tScore = t
		state.mu.Unlock()
		winner := "CT"
		if m[1] == "TERRORIST" {
			winner = "T"
		}
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

	// World events (match start, round start, etc.)
	if m := worldRe.FindStringSubmatch(line); m != nil {
		switch m[1] {
		case "Match_Start":
			state.resetWithMessage("Match Started - Stats Reset")
			log.Printf("gametracker: match start, stats reset")
		case "Game_Over":
			state.resetWithMessage("Game Over - Stats Reset")
			log.Printf("gametracker: game over, stats reset")
		case "Warmup_End":
			state.resetWithMessage("Warmup Ended - Stats Reset")
			log.Printf("gametracker: warmup ended, stats reset")
		case "Round_Start":
			state.mu.Lock()
			state.round++
			round := state.round
			state.mu.Unlock()
			state.clearWeaponsOnRound()
			state.addSystemMessage(fmt.Sprintf("Round %d", round))
		}
		return
	}

	// Map change
	if m := mapChangeRe.FindStringSubmatch(line); m != nil {
		state.resetWithMessage(fmt.Sprintf("Map Changed to %s - Stats Reset", m[1]))
		log.Printf("gametracker: map change to %s, stats reset", m[1])
		return
	}

	// Kill
	if m := killRe.FindStringSubmatch(line); m != nil {
		killer, killerTeam := m[1], m[2]
		victim, victimTeam := m[3], m[4]
		weapon := m[5]
		headshot := strings.Contains(m[6], "headshot")
		state.recordKill(killer, killerTeam, victim, victimTeam, weapon, headshot)
		return
	}

	// Assist
	if m := assistRe.FindStringSubmatch(line); m != nil {
		state.recordAssist(m[1], m[2])
		return
	}

	// Purchase
	if m := purchaseRe.FindStringSubmatch(line); m != nil {
		state.recordPurchase(m[1], m[2], m[3])
		return
	}

	// Picked up
	if m := pickupRe.FindStringSubmatch(line); m != nil {
		state.recordPurchase(m[1], m[2], m[3]) // same as purchase for tracking
		return
	}

	// Dropped
	if m := dropRe.FindStringSubmatch(line); m != nil {
		state.recordDrop(m[1], m[3])
		return
	}

	// Threw grenade
	if m := threwRe.FindStringSubmatch(line); m != nil {
		state.recordThrow(m[1], m[2], m[3])
		return
	}

	// Team switch
	if m := teamSwitchRe.FindStringSubmatch(line); m != nil {
		state.recordTeamSwitch(m[1], m[2])
		return
	}

	// Player entered
	if m := enteredRe.FindStringSubmatch(line); m != nil {
		isBot := m[2] == "BOT"
		state.recordConnect(m[1], isBot)
		return
	}

	// Player disconnected
	if m := disconnectRe.FindStringSubmatch(line); m != nil {
		state.recordDisconnect(m[1])
		return
	}
}

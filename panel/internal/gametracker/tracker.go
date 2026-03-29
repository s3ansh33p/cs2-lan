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

// Kill represents a single kill event.
type Kill struct {
	Time     time.Time
	Killer   string
	Victim   string
	Weapon   string
	Headshot bool
}

// PlayerStats tracks K/D/A for a player.
type PlayerStats struct {
	Name    string
	Kills   int
	Deaths  int
	Assists int
}

// ServerState holds the parsed game state for one server.
type ServerState struct {
	mu       sync.RWMutex
	kills    []Kill
	stats    map[string]*PlayerStats
	maxKills int
}

func newServerState() *ServerState {
	return &ServerState{
		stats:    make(map[string]*PlayerStats),
		maxKills: 100,
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

func (s *ServerState) GetScoreboard() []PlayerStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]PlayerStats, 0, len(s.stats))
	for _, ps := range s.stats {
		result = append(result, *ps)
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

func (p PlayerStats) Score() int {
	return p.Kills*2 + p.Assists
}

func (s *ServerState) recordKill(killer, victim, weapon string, headshot bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	k := Kill{
		Time:     time.Now(),
		Killer:   killer,
		Victim:   victim,
		Weapon:   weapon,
		Headshot: headshot,
	}
	s.kills = append(s.kills, k)
	if len(s.kills) > s.maxKills {
		s.kills = s.kills[len(s.kills)-s.maxKills:]
	}

	if killer != "" {
		if _, ok := s.stats[killer]; !ok {
			s.stats[killer] = &PlayerStats{Name: killer}
		}
		s.stats[killer].Kills++
	}

	if _, ok := s.stats[victim]; !ok {
		s.stats[victim] = &PlayerStats{Name: victim}
	}
	s.stats[victim].Deaths++
}

func (s *ServerState) recordAssist(assister string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.stats[assister]; !ok {
		s.stats[assister] = &PlayerStats{Name: assister}
	}
	s.stats[assister].Assists++
}

func (s *ServerState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kills = nil
	s.stats = make(map[string]*PlayerStats)
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

// TrackServer starts tracking a server's log stream for kill events.
// Sends RCON commands to enable logging, then reads the container stdout.
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

// GetState returns the state for a server (nil if not tracked).
func (m *Manager) GetState(name string) *ServerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.servers[name]
}

// StopTracking stops tracking for a server.
func (m *Manager) StopTracking(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancel, ok := m.cancels[name]; ok {
		cancel()
		delete(m.cancels, name)
		delete(m.servers, name)
	}
}

// StopAll stops all trackers.
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
	// Enable logging via RCON so kill events appear in stdout
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

	// Read the container log stream
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

// CS2 log patterns (Source engine format, echoed to console via sv_logecho 1):
// Kill: L MM/DD/YYYY - HH:MM:SS: "killer<id><steamid><team>" [x y z] killed "victim<id><steamid><team>" [x y z] with "weapon" (headshot)?
// Assist: L MM/DD/YYYY - HH:MM:SS: "assister<id><steamid><team>" assisted killing "victim<id><steamid><team>"
// Match/Round: L MM/DD/YYYY - HH:MM:SS: World triggered "Match_Start" / "Round_Start"

var (
	killRe   = regexp.MustCompile(`"(.+?)<\d+><.+?><.+?>".*killed "(.+?)<\d+><.+?><.+?>".*with "(.+?)"(.*)`)
	assistRe = regexp.MustCompile(`"(.+?)<\d+><.+?><.+?>" assisted killing "(.+?)<\d+><.+?><.+?>"`)
	roundRe  = regexp.MustCompile(`World triggered "(Match_Start|Round_Start)"`)
)

func parseLine(line string, state *ServerState) {
	// Debug: log lines that look like game events
	if strings.Contains(line, "killed") || strings.Contains(line, "assisted") ||
		strings.Contains(line, "triggered") {
		log.Printf("gametracker [event]: %s", line)
	}

	if m := roundRe.FindStringSubmatch(line); m != nil {
		if m[1] == "Match_Start" {
			state.reset()
			log.Printf("gametracker: match start, stats reset")
		}
		return
	}

	if m := killRe.FindStringSubmatch(line); m != nil {
		killer := m[1]
		victim := m[2]
		weapon := m[3]
		headshot := strings.Contains(m[4], "headshot")
		state.recordKill(killer, victim, weapon, headshot)
		log.Printf("gametracker: %s killed %s with %s (hs=%v)", killer, victim, weapon, headshot)
		return
	}

	if m := assistRe.FindStringSubmatch(line); m != nil {
		assister := m[1]
		state.recordAssist(assister)
		return
	}
}

package rcon

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	gorcon "github.com/gorcon/rcon"
)

const idleTimeout = 5 * time.Minute

type connEntry struct {
	conn     *gorcon.Conn
	lastUsed time.Time
}

type Manager struct {
	mu    sync.Mutex
	conns map[string]*connEntry
}

func NewManager() *Manager {
	m := &Manager{conns: make(map[string]*connEntry)}
	go m.reaper()
	return m
}

func (m *Manager) Execute(addr, password, command string) (string, error) {
	conn, err := m.getConn(addr, password)
	if err != nil {
		return "", err
	}

	resp, err := conn.Execute(command)
	if err != nil {
		// Connection may be stale, remove and retry once
		m.closeAddr(addr)
		conn, err = m.getConn(addr, password)
		if err != nil {
			return "", err
		}
		resp, err = conn.Execute(command)
		if err != nil {
			m.closeAddr(addr)
			return "", fmt.Errorf("rcon execute: %w", err)
		}
	}

	m.mu.Lock()
	if e, ok := m.conns[addr]; ok {
		e.lastUsed = time.Now()
	}
	m.mu.Unlock()

	return resp, nil
}

func (m *Manager) getConn(addr, password string) (*gorcon.Conn, error) {
	m.mu.Lock()
	if e, ok := m.conns[addr]; ok {
		m.mu.Unlock()
		return e.conn, nil
	}
	m.mu.Unlock()

	conn, err := gorcon.Dial(addr, password, gorcon.SetDialTimeout(3*time.Second), gorcon.SetDeadline(5*time.Second))
	if err != nil {
		return nil, fmt.Errorf("rcon dial %s: %w", addr, err)
	}

	m.mu.Lock()
	m.conns[addr] = &connEntry{conn: conn, lastUsed: time.Now()}
	m.mu.Unlock()

	return conn, nil
}

func (m *Manager) closeAddr(addr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.conns[addr]; ok {
		e.conn.Close()
		delete(m.conns, addr)
	}
}

func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for addr, e := range m.conns {
		e.conn.Close()
		delete(m.conns, addr)
	}
}

func (m *Manager) reaper() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.Lock()
		for addr, e := range m.conns {
			if time.Since(e.lastUsed) > idleTimeout {
				e.conn.Close()
				delete(m.conns, addr)
			}
		}
		m.mu.Unlock()
	}
}

// StatusInfo holds parsed RCON status output.
type StatusInfo struct {
	Map        string
	Players    []Player
	Humans     int
	Bots       int
	MaxPlayers int
}

type Player struct {
	Name     string
	Duration string
	Ping     int
	IsBot    bool
	Address  string
}

// CS2 status player line format:
//    1      BOT    0    0     active      0 'Jaques'
//    2    02:50    0    0     active 786432 127.0.0.1:53616 's3ansh33p'
// Fields: id, time, ping, loss, state, rate, [addr], name
var playerLineRe = regexp.MustCompile(`^\s*(\d+)\s+(\S+)\s+(\d+)\s+(\d+)\s+(\w+)\s+(\d+)\s+(?:(\S+)\s+)?'(.+?)'`)

// ParseStatus parses the output of the CS2 RCON "status" command.
func ParseStatus(raw string) StatusInfo {
	info := StatusInfo{}
	lines := strings.Split(raw, "\n")
	inPlayers := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Map from spawngroup info: "loaded spawngroup(  1)  : SV:  [1: de_inferno | ..."
		// Or from "@ Current  :  game" — not useful. Use hostname line's map context.
		// Actually, map comes from the loaded spawngroup(1) line
		if info.Map == "" && strings.Contains(trimmed, "loaded spawngroup(  1)") {
			// Extract map name from "[1: de_inferno | ..."
			if idx := strings.Index(trimmed, "["); idx >= 0 {
				rest := trimmed[idx+1:]
				if colonIdx := strings.Index(rest, ":"); colonIdx >= 0 {
					mapPart := strings.TrimSpace(rest[colonIdx+1:])
					if pipeIdx := strings.Index(mapPart, "|"); pipeIdx >= 0 {
						info.Map = strings.TrimSpace(mapPart[:pipeIdx])
					}
				}
			}
		}

		// "players : 1 humans, 1 bots (0 max)"
		if strings.HasPrefix(trimmed, "players") && strings.Contains(trimmed, "humans") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				desc := strings.TrimSpace(parts[1])
				fmt.Sscanf(desc, "%d humans, %d bots (%d", &info.Humans, &info.Bots, &info.MaxPlayers)
			}
		}

		// Detect player section
		if strings.HasPrefix(trimmed, "---------players") {
			inPlayers = true
			continue
		}
		if trimmed == "#end" {
			inPlayers = false
			continue
		}

		// Skip header line "id     time ping loss ..."
		if inPlayers && strings.HasPrefix(trimmed, "id") {
			continue
		}

		// Skip NoChan entries (id 65535)
		if inPlayers && strings.Contains(trimmed, "[NoChan]") {
			continue
		}

		if inPlayers {
			if m := playerLineRe.FindStringSubmatch(trimmed); m != nil {
				ping, _ := strconv.Atoi(m[3])
				isBot := m[2] == "BOT"
				info.Players = append(info.Players, Player{
					Name:     m[8],
					Duration: m[2],
					Ping:     ping,
					IsBot:    isBot,
					Address:  m[7],
				})
			}
		}
	}

	return info
}

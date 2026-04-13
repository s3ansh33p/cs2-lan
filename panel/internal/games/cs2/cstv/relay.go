// Package cstv implements an in-memory CSTV+ HTTP broadcast relay.
//
// CS2 dedicated servers with "tv_broadcast 1" and "tv_broadcast_url" POST
// fragments of the live demo stream to the configured URL. The game appends
// its own generated match token and the fragment coordinates, producing
// URLs like:
//
//	POST /<server>/<gameToken>/<frag>/<field>
//
// A client (e.g. markus-wa/demoinfocs-golang's CSTV reader) that only knows
// the <server> prefix discovers <gameToken> by GETing /<server>/sync, which
// returns a token_redirect pointing at the current match.
//
// This relay sits in the panel process and plays both roles of Valve's
// reference CDN so the game and parser can operate point-to-point on
// localhost.
//
// Wire protocol (subset of Valve's reference implementation):
//
//	POST  /<server>/<token>/<frag>/start                — signup fragment data
//	POST  /<server>/<token>/<frag>/<field>              — "full" or "delta" data
//	GET   /<server>/sync                                — discovery; returns token_redirect
//	GET   /<server>/<token>/sync[?fragment=N]           — pointer to a ready fragment (JSON)
//	GET   /<server>/<token>/<frag>/start                — always served from fragment 0
//	GET   /<server>/<token>/<frag>/<field>              — fragment payload
//	GET   /<server>/<token>/<frag>                      — fragment metadata (JSON)
//
// Query params carried on POST: tick, endtick, tps, keyframe_interval, map,
// protocol, signup_fragment. They are stored per fragment/match and surfaced
// via /sync. See examples/broadcasts/cstv.js in markus-wa/demoinfocs-golang
// for the Node.js reference this is modelled after.
package cstv

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fragment holds one broadcast fragment's payload fields and metadata.
type fragment struct {
	fields    map[string][]byte // "start", "full", "delta"
	tick      int
	endtick   int
	tps       int
	timestamp time.Time
}

// match holds the state of one broadcast (one game-generated token).
type match struct {
	mu               sync.RWMutex
	signupFragment   int
	tps              int
	keyframeInterval int
	protocol         int
	mapName          string
	fragments        map[int]*fragment
	maxFragment      int // highest fragment index seen
	createdAt        time.Time
}

// server holds all matches under one URL prefix (one CS2 dedicated server).
// A single CS2 server typically produces one match at a time, but on map
// change it starts a fresh game-token; we keep the mapping so /sync can
// redirect new parser connections to the current one without state loss
// while old parser connections drain on the previous token.
type server struct {
	mu           sync.RWMutex
	matches      map[string]*match // keyed by game-token
	currentToken string            // latest-seen token, used for /sync redirects
	ready        chan struct{}     // closed once any match has a ready fragment
	readyOnce    sync.Once
	// tokenChanged is closed whenever currentToken flips from one non-empty
	// token to another (i.e. CS2 started a fresh CSTV match — map change or
	// mp_restartgame). Consumers grab the current channel, select on it, and
	// call TokenChanged again after handling the flip to pick up the next
	// rotation. NOT closed on the empty→first-token transition.
	tokenChanged chan struct{}
}

// Relay is a thread-safe in-memory CSTV+ broadcast relay.
// A single Relay instance can serve many concurrent servers.
type Relay struct {
	mu      sync.RWMutex
	servers map[string]*server
	// retention: keep at most this many fragments per match in memory.
	// Older fragments are evicted. 0 = unlimited.
	maxFragments int
}

// NewRelay returns an empty Relay with default retention (0 = unlimited).
func NewRelay() *Relay {
	return &Relay{
		servers:      make(map[string]*server),
		maxFragments: 0,
	}
}

// SetMaxFragments caps the number of fragments kept per match. 0 disables
// the cap. Must be called before traffic starts.
func (r *Relay) SetMaxFragments(n int) {
	r.mu.Lock()
	r.maxFragments = n
	r.mu.Unlock()
}

// Ready returns a channel closed as soon as the given server has posted a
// fragment that can satisfy /sync. The tracker's parser waits on this before
// attempting to connect.
func (r *Relay) Ready(serverName string) <-chan struct{} {
	return r.getOrCreateServer(serverName).ready
}

// TokenChanged returns a channel that is closed the next time the server's
// current CSTV match token flips to a different non-empty token. A fresh
// channel is installed on each flip, so callers must call TokenChanged again
// after handling one before selecting on the next. Used by the tracker to
// tear down a parser stuck on an abandoned token (map change, mp_restartgame)
// without waiting for the HTTP read to time out on dead fragments.
func (r *Relay) TokenChanged(serverName string) <-chan struct{} {
	s := r.getOrCreateServer(serverName)
	s.mu.RLock()
	ch := s.tokenChanged
	s.mu.RUnlock()
	return ch
}

// Close discards all fragments under the given server name so memory is
// freed when the CS2 server stops. Subsequent POSTs start a fresh server.
func (r *Relay) Close(serverName string) {
	r.mu.Lock()
	delete(r.servers, serverName)
	r.mu.Unlock()
}

func (r *Relay) getOrCreateServer(name string) *server {
	r.mu.RLock()
	s, ok := r.servers[name]
	r.mu.RUnlock()
	if ok {
		return s
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.servers[name]; ok {
		return s
	}
	s = &server{
		matches:      make(map[string]*match),
		ready:        make(chan struct{}),
		tokenChanged: make(chan struct{}),
	}
	r.servers[name] = s
	return s
}

func (r *Relay) lookupServer(name string) *server {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.servers[name]
}

func (s *server) getOrCreateMatch(token string) *match {
	s.mu.RLock()
	m, ok := s.matches[token]
	s.mu.RUnlock()
	if ok {
		return m
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.matches[token]; ok {
		return m
	}
	m = &match{
		fragments: make(map[int]*fragment),
		createdAt: time.Now(),
	}
	s.matches[token] = m
	return m
}

func (s *server) lookupMatch(token string) *match {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.matches[token]
}

// Handler returns an http.Handler that implements the relay protocol. Mount
// it with a prefix-stripping wrapper at /cstv/ on the router.
func (r *Relay) Handler() http.Handler {
	return http.HandlerFunc(r.serveHTTP)
}

func (r *Relay) serveHTTP(w http.ResponseWriter, req *http.Request) {
	// Paths arrive here as "/<server>/..." after prefix strip.
	path := strings.TrimPrefix(req.URL.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	serverName := parts[0]
	parts = parts[1:]

	switch req.Method {
	case http.MethodPost:
		// /<server>/<token>/<frag>/<field>
		if len(parts) != 3 {
			http.Error(w, fmt.Sprintf("POST needs /<token>/<frag>/<field>; got %d segments", len(parts)), http.StatusBadRequest)
			return
		}
		token := parts[0]
		frag, err := strconv.Atoi(parts[1])
		if err != nil {
			http.Error(w, "fragment must be int", http.StatusBadRequest)
			return
		}
		r.handlePost(w, req, serverName, token, frag, parts[2])

	case http.MethodGet:
		if len(parts) == 0 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// /<server>/sync — bare discovery
		if len(parts) == 1 && parts[0] == "sync" {
			r.handleServerSync(w, req, serverName)
			return
		}
		token := parts[0]
		parts = parts[1:]
		if len(parts) == 0 {
			http.Error(w, "need /sync or /<frag>/...", http.StatusBadRequest)
			return
		}
		// /<server>/<token>/sync
		if len(parts) == 1 && parts[0] == "sync" {
			r.handleMatchSync(w, req, serverName, token)
			return
		}
		frag, err := strconv.Atoi(parts[0])
		if err != nil {
			http.Error(w, "fragment must be int or 'sync'", http.StatusBadRequest)
			return
		}
		switch len(parts) {
		case 1:
			r.handleFragmentMetadata(w, serverName, token, frag)
		case 2:
			r.handleField(w, serverName, token, frag, parts[1])
		default:
			http.NotFound(w, req)
		}

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (r *Relay) handlePost(w http.ResponseWriter, req *http.Request, serverName, token string, frag int, field string) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	_ = req.Body.Close()

	srv := r.getOrCreateServer(serverName)
	m := srv.getOrCreateMatch(token)
	q := req.URL.Query()

	m.mu.Lock()
	if v := q.Get("tps"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			m.tps = n
		}
	}
	if v := q.Get("keyframe_interval"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			m.keyframeInterval = n
		}
	}
	if v := q.Get("protocol"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			m.protocol = n
		}
	}
	if v := q.Get("map"); v != "" {
		m.mapName = v
	}

	// "start" lives at index 0; the URL fragment is the signup fragment.
	storeIdx := frag
	if field == "start" {
		m.signupFragment = frag
		storeIdx = 0
	}
	f, ok := m.fragments[storeIdx]
	if !ok {
		f = &fragment{fields: make(map[string][]byte)}
		m.fragments[storeIdx] = f
	}
	f.fields[field] = body
	f.timestamp = time.Now()
	if v := q.Get("tick"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.tick = n
		}
	}
	if v := q.Get("endtick"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.endtick = n
		}
	}
	if storeIdx > m.maxFragment {
		m.maxFragment = storeIdx
	}
	if r.maxFragments > 0 {
		oldest := m.maxFragment - r.maxFragments
		for idx := range m.fragments {
			if idx != 0 && idx < oldest {
				delete(m.fragments, idx)
			}
		}
	}
	matchReady := m.hasReadyFragmentLocked()
	m.mu.Unlock()

	// Update server-level pointers. currentToken is promoted to this token
	// on any POST so a new match (e.g. after map change) overtakes the old.
	// On a real flip (old non-empty token → new different token) rotate the
	// tokenChanged channel so subscribers can observe the transition.
	srv.mu.Lock()
	flipped := srv.currentToken != "" && srv.currentToken != token
	srv.currentToken = token
	var oldTokenChanged chan struct{}
	if flipped {
		oldTokenChanged = srv.tokenChanged
		srv.tokenChanged = make(chan struct{})
	}
	srv.mu.Unlock()
	if flipped {
		close(oldTokenChanged)
	}
	if matchReady {
		srv.readyOnce.Do(func() { close(srv.ready) })
	}

	w.WriteHeader(http.StatusOK)
}

// hasReadyFragmentLocked reports whether any fragment >= signup_fragment has
// start data AND full+delta+tick+endtick. Caller holds m.mu.
func (m *match) hasReadyFragmentLocked() bool {
	f0, ok := m.fragments[0]
	if !ok || f0.fields["start"] == nil {
		return false
	}
	for idx, f := range m.fragments {
		if idx < m.signupFragment {
			continue
		}
		if f.fields["full"] != nil && f.fields["delta"] != nil && f.tick != 0 && f.endtick != 0 {
			return true
		}
	}
	return false
}

// syncResponse mirrors the JSON shape Valve's reference playcast emits.
// TokenRedirect, when non-empty, tells the client to retry with a joined URL
// so a parser that only knows the <server> prefix can discover the match.
type syncResponse struct {
	Tick             int     `json:"tick"`
	EndTick          int     `json:"endtick"`
	MaxTick          int     `json:"maxtick"`
	RtDelay          float64 `json:"rtdelay"`
	RcvAge           float64 `json:"rcvage"`
	Fragment         int     `json:"fragment"`
	SignupFragment   int     `json:"signup_fragment"`
	Tps              int     `json:"tps"`
	KeyframeInterval int     `json:"keyframe_interval"`
	Map              string  `json:"map"`
	Protocol         int     `json:"protocol"`
	TokenRedirect    string  `json:"token_redirect,omitempty"`
}

// handleServerSync answers GET /<server>/sync: the parser is bootstrapping
// and doesn't yet know the game-token. Find the current match under this
// server and return a sync response whose token_redirect points at it.
func (r *Relay) handleServerSync(w http.ResponseWriter, req *http.Request, serverName string) {
	srv := r.lookupServer(serverName)
	if srv == nil {
		http.Error(w, "server not found", http.StatusNotFound)
		return
	}
	srv.mu.RLock()
	token := srv.currentToken
	m := srv.matches[token]
	srv.mu.RUnlock()
	if token == "" || m == nil {
		http.Error(w, "no match yet", http.StatusNotFound)
		return
	}
	r.writeSyncResponse(w, req, m, token)
}

// handleMatchSync answers GET /<server>/<token>/sync: the parser has already
// been redirected to the concrete match; no token_redirect needed.
func (r *Relay) handleMatchSync(w http.ResponseWriter, req *http.Request, serverName, token string) {
	srv := r.lookupServer(serverName)
	if srv == nil {
		http.Error(w, "server not found", http.StatusNotFound)
		return
	}
	m := srv.lookupMatch(token)
	if m == nil {
		http.Error(w, "match not found", http.StatusNotFound)
		return
	}
	r.writeSyncResponse(w, req, m, "")
}

func (r *Relay) writeSyncResponse(w http.ResponseWriter, req *http.Request, m *match, tokenRedirect string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	start, _ := strconv.Atoi(req.URL.Query().Get("fragment"))
	idx, f := m.pickReadyLocked(start)
	if f == nil {
		http.Error(w, "no fragment ready", http.StatusNotFound)
		return
	}
	now := time.Now()
	resp := syncResponse{
		Tick:             f.tick,
		EndTick:          f.endtick,
		MaxTick:          m.maxEndTickLocked(),
		RtDelay:          now.Sub(f.timestamp).Seconds(),
		RcvAge:           now.Sub(m.latestTimestampLocked()).Seconds(),
		Fragment:         idx,
		SignupFragment:   m.signupFragment,
		Tps:              m.tps,
		KeyframeInterval: m.keyframeInterval,
		Map:              m.mapName,
		Protocol:         m.protocolOrDefault(),
		TokenRedirect:    tokenRedirect,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=1")
	_ = json.NewEncoder(w).Encode(resp)
}

func (m *match) protocolOrDefault() int {
	if m.protocol != 0 {
		return m.protocol
	}
	return 5 // Source 2 broadcast protocol
}

func (m *match) pickReadyLocked(startFrom int) (int, *fragment) {
	if startFrom == 0 {
		startFrom = m.maxFragment - 3
		if startFrom < m.signupFragment {
			startFrom = m.signupFragment
		}
		if startFrom < 0 {
			startFrom = 0
		}
	}
	for i := startFrom; i <= m.maxFragment; i++ {
		f, ok := m.fragments[i]
		if !ok {
			continue
		}
		if f.fields["full"] != nil && f.fields["delta"] != nil && f.tick != 0 && f.endtick != 0 {
			return i, f
		}
	}
	return 0, nil
}

func (m *match) maxEndTickLocked() int {
	maxET := 0
	for _, f := range m.fragments {
		if f.endtick > maxET {
			maxET = f.endtick
		}
	}
	return maxET
}

func (m *match) latestTimestampLocked() time.Time {
	var latest time.Time
	for _, f := range m.fragments {
		if f.timestamp.After(latest) {
			latest = f.timestamp
		}
	}
	return latest
}

func (r *Relay) handleField(w http.ResponseWriter, serverName, token string, frag int, field string) {
	srv := r.lookupServer(serverName)
	if srv == nil {
		http.Error(w, "server not found", http.StatusNotFound)
		return
	}
	m := srv.lookupMatch(token)
	if m == nil {
		http.Error(w, "match not found", http.StatusNotFound)
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Start data is always served from fragment 0.
	lookupIdx := frag
	if field == "start" {
		lookupIdx = 0
	}
	f, ok := m.fragments[lookupIdx]
	if !ok {
		http.Error(w, fmt.Sprintf("fragment %d not found", frag), http.StatusNotFound)
		return
	}
	blob, ok := f.fields[field]
	if !ok || blob == nil {
		http.Error(w, "field not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
	_, _ = w.Write(blob)
}

func (r *Relay) handleFragmentMetadata(w http.ResponseWriter, serverName, token string, frag int) {
	srv := r.lookupServer(serverName)
	if srv == nil {
		http.Error(w, "server not found", http.StatusNotFound)
		return
	}
	m := srv.lookupMatch(token)
	if m == nil {
		http.Error(w, "match not found", http.StatusNotFound)
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.fragments[frag]
	if !ok {
		http.Error(w, "fragment not found", http.StatusNotFound)
		return
	}
	meta := map[string]any{
		"tick":    f.tick,
		"endtick": f.endtick,
	}
	for field, blob := range f.fields {
		meta[field] = len(blob)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(meta)
}

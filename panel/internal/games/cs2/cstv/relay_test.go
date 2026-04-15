package cstv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestRoundtrip simulates CS2 POSTing fragments under a game-generated
// token, then a parser performing the discovery → sync → start → full →
// delta dance demoinfocs-golang's CSTV reader uses.
func TestRoundtrip(t *testing.T) {
	r := NewRelay()
	ts := httptest.NewServer(http.StripPrefix("/cstv", r.Handler()))
	defer ts.Close()

	const (
		serverName = "match1"
		token      = "s72057594037927936t1776076765"
		signup     = 42
	)
	base := ts.URL + "/cstv/" + serverName

	// Ready fires when ANY match under this server goes ready.
	select {
	case <-r.Ready(serverName):
		t.Fatal("ready channel fired before any fragments posted")
	default:
	}

	// Simulate the game: POST start at fragment N (signup), then full+delta for N..N+2.
	startBlob := []byte("start-data-blob")
	postRaw(t, base, "/"+token+"/"+itoa(signup)+"/start?tick=1000&endtick=1064&tps=64&keyframe_interval=3&map=de_dust2&protocol=5&signup_fragment="+itoa(signup), startBlob)

	for i := 0; i < 3; i++ {
		frag := signup + i
		full := []byte("full-" + itoa(frag))
		delta := []byte("delta-" + itoa(frag))
		tick := 1000 + i*64
		endtick := tick + 64
		qs := "?tick=" + itoa(tick) + "&endtick=" + itoa(endtick) + "&tps=64&keyframe_interval=3"
		postRaw(t, base, "/"+token+"/"+itoa(frag)+"/full"+qs, full)
		postRaw(t, base, "/"+token+"/"+itoa(frag)+"/delta"+qs, delta)
	}

	select {
	case <-r.Ready(serverName):
	case <-time.After(time.Second):
		t.Fatal("ready channel not fired after posting complete fragments")
	}

	// GET /<server>/sync — parser discovery. Returns token_redirect pointing
	// at the current match so the caller can join paths and continue.
	var s syncResponse
	getJSON(t, base+"/sync", &s)
	if s.TokenRedirect != token {
		t.Errorf("server-level sync token_redirect = %q, want %q", s.TokenRedirect, token)
	}
	if s.Map != "de_dust2" {
		t.Errorf("sync map = %q, want de_dust2", s.Map)
	}

	// GET /<server>/<token>/sync — match-level sync, no redirect.
	var s2 syncResponse
	getJSON(t, base+"/"+token+"/sync", &s2)
	if s2.TokenRedirect != "" {
		t.Errorf("match-level sync should not redirect, got %q", s2.TokenRedirect)
	}
	if s2.SignupFragment != signup {
		t.Errorf("sync signup_fragment = %d, want %d", s2.SignupFragment, signup)
	}

	// Payload roundtrip: start / full / delta all byte-identical.
	if got := getBytes(t, base+"/"+token+"/"+itoa(signup)+"/start"); !bytes.Equal(got, startBlob) {
		t.Errorf("start roundtrip mismatch: got %q want %q", got, startBlob)
	}
	for i := 0; i < 3; i++ {
		frag := signup + i
		wantFull := []byte("full-" + itoa(frag))
		wantDelta := []byte("delta-" + itoa(frag))
		if g := getBytes(t, base+"/"+token+"/"+itoa(frag)+"/full"); !bytes.Equal(g, wantFull) {
			t.Errorf("frag %d full mismatch: got %q want %q", frag, g, wantFull)
		}
		if g := getBytes(t, base+"/"+token+"/"+itoa(frag)+"/delta"); !bytes.Equal(g, wantDelta) {
			t.Errorf("frag %d delta mismatch: got %q want %q", frag, g, wantDelta)
		}
	}

	// sync?fragment=N on the match URL pinpoints a specific fragment.
	var s3 syncResponse
	getJSON(t, base+"/"+token+"/sync?fragment="+itoa(signup+1), &s3)
	if s3.Fragment != signup+1 {
		t.Errorf("sync?fragment=%d returned fragment=%d", signup+1, s3.Fragment)
	}

	// Close evicts the server — subsequent /sync returns 404.
	r.Close(serverName)
	resp, err := http.Get(base + "/sync")
	if err != nil {
		t.Fatalf("get after close: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("get after close: status %d, want 404", resp.StatusCode)
	}
}

// TestSyncParsesFloatTps covers the 2026-04-15 intdividebyzero crash: CS2
// ships the tickrate as a float string ("tps=64.0") on the /start POST, which
// strconv.Atoi rejects. That left m.tps at zero, shipped "tps": 0 down to the
// client, and crashed engine2.dll with integer divide-by-zero on stream start.
// This regression locks in float-tolerant parsing.
func TestSyncParsesFloatTps(t *testing.T) {
	r := NewRelay()
	ts := httptest.NewServer(http.StripPrefix("/cstv", r.Handler()))
	defer ts.Close()

	base := ts.URL + "/cstv/srv1"
	// Real CS2 query string shape, as observed in panel.log on 2026-04-15.
	postRaw(t, base, "/tok1/0/start?tick=78&tps=64.0&map=de_inferno&keyframe_interval=3&protocol=5", []byte("x"))
	postRaw(t, base, "/tok1/0/full?tick=1&endtick=2", []byte("y"))
	postRaw(t, base, "/tok1/0/delta?tick=1&endtick=2", []byte("z"))

	var s syncResponse
	getJSON(t, base+"/tok1/sync", &s)
	if s.Tps != 64 {
		t.Errorf("sync tps = %d, want 64 (parsed from 64.0)", s.Tps)
	}
}

// TestSyncDefaultsTpsWhenMissing ensures the fallback still protects the
// client if CS2 ever drops the tps param entirely. See tpsOrDefault.
func TestSyncDefaultsTpsWhenMissing(t *testing.T) {
	r := NewRelay()
	ts := httptest.NewServer(http.StripPrefix("/cstv", r.Handler()))
	defer ts.Close()

	base := ts.URL + "/cstv/srv1"
	// Note: no tps param on any POST.
	postRaw(t, base, "/tok1/0/start?tick=1&endtick=2&keyframe_interval=3&map=de_dust2&protocol=5&signup_fragment=0", []byte("x"))
	postRaw(t, base, "/tok1/0/full?tick=1&endtick=2", []byte("y"))
	postRaw(t, base, "/tok1/0/delta?tick=1&endtick=2", []byte("z"))

	var s syncResponse
	getJSON(t, base+"/tok1/sync", &s)
	if s.Tps != 64 {
		t.Errorf("sync tps = %d, want 64 (default fallback)", s.Tps)
	}
}

// TestPublicHandlerGatesByFragmentAge exercises the anti-screen-peek delay:
// the public mount must hide fragments younger than publicDelay and reveal
// them once aged. /sync picks the newest aged fragment; field GETs return
// 404 for too-fresh fragments.
func TestPublicHandlerGatesByFragmentAge(t *testing.T) {
	r := NewRelay()
	r.SetPublicDelay(100 * time.Millisecond)
	// Shrink the prefetch buffer to a hair under publicDelay so a single
	// ~150ms Sleep can age a fragment past both the per-fragment gate AND
	// the /sync cushion. The buffer behaviour has its own test below.
	r.SetPublicSyncBuffer(20 * time.Millisecond)
	// Internal (loopback) handler — no delay.
	internal := httptest.NewServer(http.StripPrefix("/cstv", r.Handler()))
	defer internal.Close()
	// Public handler — 100ms gate.
	public := httptest.NewServer(http.StripPrefix("/cstv", r.PublicHandler()))
	defer public.Close()

	base := "/cstv/srv1"
	postFrag := func(frag, tick int, full, delta []byte) {
		t.Helper()
		qs := fmt.Sprintf("?tick=%d&endtick=%d&tps=64&keyframe_interval=3", tick, tick+64)
		if frag == 0 {
			postRaw(t, internal.URL+base, "/tok1/0/start"+qs+"&map=de_dust2&protocol=5&signup_fragment=0", []byte("start"))
		}
		postRaw(t, internal.URL+base, "/tok1/"+itoa(frag)+"/full"+qs, full)
		postRaw(t, internal.URL+base, "/tok1/"+itoa(frag)+"/delta"+qs, delta)
	}

	// Fragment 0 posted now. Internal sees it immediately; public must 404
	// until the 100ms gate elapses.
	postFrag(0, 1000, []byte("full-0"), []byte("delta-0"))

	// Internal /sync succeeds right away.
	var si syncResponse
	getJSON(t, internal.URL+base+"/tok1/sync", &si)
	if si.Fragment != 0 {
		t.Errorf("internal sync fragment = %d, want 0", si.Fragment)
	}

	// Public /sync must 404 while fragment 0 is still fresh.
	resp, err := http.Get(public.URL + base + "/tok1/sync")
	if err != nil {
		t.Fatalf("public sync: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("public sync on fresh fragment: status = %d, want 404", resp.StatusCode)
	}

	// Public field GET on a fresh fragment is also gated.
	resp, err = http.Get(public.URL + base + "/tok1/0/delta")
	if err != nil {
		t.Fatalf("public field: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("public field on fresh fragment: status = %d, want 404", resp.StatusCode)
	}

	// Wait past the gate, then the public mount must reveal the fragment.
	time.Sleep(150 * time.Millisecond)

	var sp syncResponse
	getJSON(t, public.URL+base+"/tok1/sync", &sp)
	if sp.Fragment != 0 {
		t.Errorf("public sync after gate: fragment = %d, want 0", sp.Fragment)
	}
	if got := getBytes(t, public.URL+base+"/tok1/0/delta"); !bytes.Equal(got, []byte("delta-0")) {
		t.Errorf("public delta after gate: got %q want %q", got, []byte("delta-0"))
	}

	// Post a brand-new fragment. Public /sync must still return fragment 0
	// (the aged one), NOT the fresh fragment 1.
	postFrag(1, 1064, []byte("full-1"), []byte("delta-1"))
	var sp2 syncResponse
	getJSON(t, public.URL+base+"/tok1/sync", &sp2)
	if sp2.Fragment != 0 {
		t.Errorf("public sync with fresh newer fragment: returned %d, want 0 (aged-only pick)", sp2.Fragment)
	}
}

// TestPublicHandlerSyncBufferCoversPrefetch reproduces the 2026-04-15 connect
// storm: /sync used to return the fragment right at publicDelay age, so the
// CS2 client's 5-fragment prefetch immediately 404'd on fragments N+1..N+5
// (all fresher than the gate), causing ~45-55s of adaptive playback-rate
// damping before convergence. The fix biases /sync to a fragment old enough
// that the whole prefetch window clears the per-fragment gate.
//
// The test posts fragment 0 and waits well past the buffer, then posts
// fragment 1 right before calling /sync. Fragment 1 is fresh enough to fail
// the buffer; /sync must fall back to fragment 0. Margins are deliberately
// large (500ms buffer vs 50ms gate) so timing jitter under load can't flip
// the outcome: fragment 1 would need to age 500ms+ in the few-ms HTTP
// round-trip before sync, which won't happen.
func TestPublicHandlerSyncBufferCoversPrefetch(t *testing.T) {
	r := NewRelay()
	r.SetPublicDelay(50 * time.Millisecond)     // per-fragment gate
	r.SetPublicSyncBuffer(500 * time.Millisecond) // /sync extra cushion
	internal := httptest.NewServer(http.StripPrefix("/cstv", r.Handler()))
	defer internal.Close()
	public := httptest.NewServer(http.StripPrefix("/cstv", r.PublicHandler()))
	defer public.Close()

	base := "/cstv/srv1"
	// Ticks must be non-zero — pickReadyLocked filters out fragments with
	// tick==0 || endtick==0 as "not yet ready".
	postRaw(t, internal.URL+base, "/tok1/0/start?tick=1000&endtick=1064&tps=64&keyframe_interval=3&map=de_dust2&protocol=5&signup_fragment=0", []byte("start"))
	postRaw(t, internal.URL+base, "/tok1/0/full?tick=1000&endtick=1064&tps=64&keyframe_interval=3", []byte("full-0"))
	postRaw(t, internal.URL+base, "/tok1/0/delta?tick=1000&endtick=1064&tps=64&keyframe_interval=3", []byte("delta-0"))

	// Age fragment 0 comfortably past delay+buffer=550ms.
	time.Sleep(700 * time.Millisecond)

	// Post fragment 1 — fresh, age ~0. It passes the 50ms gate quickly but
	// stays well below the 550ms sync cushion.
	postRaw(t, internal.URL+base, "/tok1/1/full?tick=1064&endtick=1128&tps=64&keyframe_interval=3", []byte("full-1"))
	postRaw(t, internal.URL+base, "/tok1/1/delta?tick=1064&endtick=1128&tps=64&keyframe_interval=3", []byte("delta-1"))

	// Without the buffer, /sync picks the newest fragment past the 50ms gate
	// — that would be fragment 1 within ~50ms. With the buffer, fragment 1 is
	// still too fresh, so /sync falls back to fragment 0.
	var s syncResponse
	getJSON(t, public.URL+base+"/tok1/sync", &s)
	if s.Fragment != 0 {
		t.Errorf("public sync picked fragment %d; want 0 (buffer should reject the freshly-posted fragment 1 in favour of the well-aged fragment 0)", s.Fragment)
	}
}

func TestUnknownServerReturns404(t *testing.T) {
	r := NewRelay()
	ts := httptest.NewServer(http.StripPrefix("/cstv", r.Handler()))
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/cstv/nobody/sync")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestMapChange verifies that a second game-token (simulating a map change
// or match restart) supersedes the first at the server-level /sync endpoint.
func TestMapChange(t *testing.T) {
	r := NewRelay()
	ts := httptest.NewServer(http.StripPrefix("/cstv", r.Handler()))
	defer ts.Close()

	base := ts.URL + "/cstv/srv1"
	postMatch := func(token string, signup int, mapName string) {
		postRaw(t, base, "/"+token+"/"+itoa(signup)+"/start?tick=1&endtick=2&tps=64&keyframe_interval=3&map="+mapName+"&protocol=5&signup_fragment="+itoa(signup), []byte("x"))
		postRaw(t, base, "/"+token+"/"+itoa(signup)+"/full?tick=1&endtick=2", []byte("y"))
		postRaw(t, base, "/"+token+"/"+itoa(signup)+"/delta?tick=1&endtick=2", []byte("z"))
	}
	postMatch("tok1", 10, "de_dust2")
	var s1 syncResponse
	getJSON(t, base+"/sync", &s1)
	if s1.TokenRedirect != "tok1" {
		t.Fatalf("first sync token = %q, want tok1", s1.TokenRedirect)
	}

	postMatch("tok2", 5, "de_mirage")
	var s2 syncResponse
	getJSON(t, base+"/sync", &s2)
	if s2.TokenRedirect != "tok2" {
		t.Errorf("after map change sync token = %q, want tok2", s2.TokenRedirect)
	}
	if s2.Map != "de_mirage" {
		t.Errorf("after map change map = %q, want de_mirage", s2.Map)
	}
}

func TestTokenChangedFiresOnFlip(t *testing.T) {
	r := NewRelay()
	ts := httptest.NewServer(http.StripPrefix("/cstv", r.Handler()))
	defer ts.Close()

	base := ts.URL + "/cstv/srv1"
	postMatch := func(token string, signup int, mapName string) {
		postRaw(t, base, "/"+token+"/"+itoa(signup)+"/start?tick=1&endtick=2&tps=64&keyframe_interval=3&map="+mapName+"&protocol=5&signup_fragment="+itoa(signup), []byte("x"))
		postRaw(t, base, "/"+token+"/"+itoa(signup)+"/full?tick=1&endtick=2", []byte("y"))
		postRaw(t, base, "/"+token+"/"+itoa(signup)+"/delta?tick=1&endtick=2", []byte("z"))
	}

	ch := r.TokenChanged("srv1")

	// First match establishes the token — must NOT close the channel. The
	// empty→first transition is not a flip; consumers only care about
	// subsequent rotations.
	postMatch("tok1", 10, "de_dust2")
	select {
	case <-ch:
		t.Fatal("TokenChanged fired on empty→first-token transition")
	case <-time.After(50 * time.Millisecond):
	}

	// Second match flips the token — must close the channel captured before
	// the flip. The relay installs a fresh channel for the next flip.
	postMatch("tok2", 5, "de_mirage")
	select {
	case <-ch:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("TokenChanged did not fire on tok1→tok2 flip")
	}

	// Subsequent calls return a new, still-open channel.
	ch2 := r.TokenChanged("srv1")
	select {
	case <-ch2:
		t.Fatal("rotated TokenChanged channel already closed")
	case <-time.After(50 * time.Millisecond):
	}

	// Re-posting to the same token is not a flip.
	postMatch("tok2", 5, "de_mirage")
	select {
	case <-ch2:
		t.Fatal("TokenChanged fired on same-token re-post")
	case <-time.After(50 * time.Millisecond):
	}

	// A third distinct token triggers the new channel.
	postMatch("tok3", 1, "de_inferno")
	select {
	case <-ch2:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("TokenChanged did not fire on tok2→tok3 flip")
	}
}

// TestLastFragmentTime covers the tracker's staleness watchdog probe.
func TestLastFragmentTime(t *testing.T) {
	r := NewRelay()
	ts := httptest.NewServer(http.StripPrefix("/cstv", r.Handler()))
	defer ts.Close()

	// Unknown server returns zero time — don't false-positive as stale.
	if !r.LastFragmentTime("nobody").IsZero() {
		t.Error("LastFragmentTime for unknown server should be zero")
	}

	// Server that has a ready channel but no POSTs still returns zero.
	_ = r.Ready("srv1")
	if !r.LastFragmentTime("srv1").IsZero() {
		t.Error("LastFragmentTime before any POST should be zero")
	}

	base := ts.URL + "/cstv/srv1"
	before := time.Now()
	postRaw(t, base, "/tok1/10/start?tick=1&endtick=2&tps=64&keyframe_interval=3&map=de_dust2&protocol=5&signup_fragment=10", []byte("x"))
	postRaw(t, base, "/tok1/10/full?tick=1&endtick=2", []byte("y"))
	last := r.LastFragmentTime("srv1")
	if last.IsZero() {
		t.Fatal("LastFragmentTime zero after POST")
	}
	if last.Before(before) {
		t.Errorf("LastFragmentTime = %v, want >= %v", last, before)
	}

	// A newer POST bumps the timestamp forward.
	time.Sleep(5 * time.Millisecond)
	postRaw(t, base, "/tok1/11/full?tick=3&endtick=4", []byte("z"))
	next := r.LastFragmentTime("srv1")
	if !next.After(last) {
		t.Errorf("LastFragmentTime did not advance: %v then %v", last, next)
	}

	// Close evicts the server — zero again.
	r.Close("srv1")
	if !r.LastFragmentTime("srv1").IsZero() {
		t.Error("LastFragmentTime after Close should be zero")
	}
}

// --- helpers ---

func postRaw(t *testing.T, base, pathAndQuery string, body []byte) {
	t.Helper()
	resp, err := http.Post(base+pathAndQuery, "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", pathAndQuery, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: status %d", pathAndQuery, resp.StatusCode)
	}
}

func getBytes(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d body=%s", url, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return b
}

func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	b := getBytes(t, url)
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decode %s: %v (body=%s)", url, err, string(b))
	}
}

// itoa is a tiny no-alloc int→string for test paths.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	buf := make([]byte, 0, 16)
	for i > 0 {
		buf = append([]byte{byte('0' + i%10)}, buf...)
		i /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

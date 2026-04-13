package cstv

import (
	"bytes"
	"encoding/json"
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

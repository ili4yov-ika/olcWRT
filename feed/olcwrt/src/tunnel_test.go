package main

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestParseTunnelEvent(t *testing.T) {
	cases := []struct {
		line  string
		state string
		prof  string
		err   string
	}{
		{"session 5f2a opened (device=abc)", stateConnected, "", ""},
		{"session 5f2a reopened (device=abc)", stateConnected, "", ""},
		{"failover cycle=1 starting profile=s_0011223344 provider=jitsi transport=datachannel", stateConnecting, "s_0011223344", ""},
		{"failover cycle=1 profile=s_00 ended with error: room closed", stateReconnecting, "", "room closed"},
		{"client reconnect reason=pong timeout - tearing down smux session", stateReconnecting, "", ""},
		{"jitsi: waiting for peer in room (not a failure)", stateWaitingPeer, "", ""},
		{"validate config: key required", "", "", "validate config: key required"},
	}
	for _, c := range cases {
		ev, ok := parseTunnelEvent(c.line)
		if !ok || ev.state != c.state || ev.profile != c.prof || ev.err != c.err {
			t.Errorf("%q: got %+v %v", c.line, ev, ok)
		}
	}
	if _, ok := parseTunnelEvent("SOCKS5 server listening on 127.0.0.1:8808"); ok {
		t.Error("listener line is not a state change")
	}
}

func TestTunnelHandleLineFiltersConnections(t *testing.T) {
	dir := t.TempDir()
	log := newLogger(filepath.Join(dir, "log"), false)
	defer log.Close()
	changes := 0
	ts := &tunnelSupervisor{log: log, onChange: func() { changes++ }, st: tunnelState{State: stateConnecting}}

	ts.handleLine("2026/10/04 21:48:00 sid=3 tunnel to 1.2.3.4:443")
	ts.handleLine("2026/10/04 21:48:00 traffic: session=x addr=y in=1 out=2")
	ts.handleLine("2026/10/04 21:48:01 session abc opened (device=d)")
	ts.handleLine("2026/10/04 21:48:02 control missed pong peer=x missed=1")

	data, _ := os.ReadFile(filepath.Join(dir, "log"))
	logText := string(data)
	if strings.Contains(logText, "sid=3") || strings.Contains(logText, "traffic:") {
		t.Fatalf("per-connection lines leaked into the log:\n%s", logText)
	}
	if !strings.Contains(logText, "[olcrtc] session abc opened") || strings.Contains(logText, "2026/10/04") {
		t.Fatalf("unexpected log:\n%s", logText)
	}
	if st := ts.snapshot(); st.State != stateConnected || changes != 1 {
		t.Fatalf("state %+v changes %d", st, changes)
	}

	log.setDebug(true)
	ts.handleLine("sid=4 tunnel to 5.6.7.8:80")
	data, _ = os.ReadFile(filepath.Join(dir, "log"))
	if !strings.Contains(string(data), "sid=4 tunnel to") {
		t.Fatal("debug mode must keep per-connection lines")
	}
}

func TestLoggerRotationAndTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "olcwrt.log")
	l := newLogger(path, false)
	l.max = 200
	for i := 0; i < 20; i++ {
		l.write("t", strings.Repeat("x", 20))
	}
	l.Close()
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatal("expected rotated file")
	}
	lines := tailLog(path, 5)
	if len(lines) != 5 {
		t.Fatalf("tail returned %d lines", len(lines))
	}
}

package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSyncSubServers(t *testing.T) {
	pkg, err := parseUCIShow("olcwrt", uciSample)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := configFromUCI(pkg)
	// s_b belongs to the subscription and is selected; it disappears.
	cfg.ServerByID["s_b"].Subscription = "sub_1"
	cfg.Selected = []string{"s_b"}

	data := ParseSubscription(subDoc)
	b := newUCIBatch("olcwrt")
	added, updated, removed := syncSubServers(b, cfg, "sub_1", data.Entries)
	if added != 2 || updated != 0 || removed != 1 {
		t.Fatalf("added=%d updated=%d removed=%d", added, updated, removed)
	}
	script := b.String()
	first := data.Entries[0].Server.ID
	for _, want := range []string{
		"set olcwrt." + first + "='server'\n",
		"set olcwrt." + first + ".subscription='sub_1'\n",
		"set olcwrt." + first + ".sei_frag='900'\n",
		"delete olcwrt.s_b\n",
		"add_list olcwrt.main.server='" + first + "'\n",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("missing %q in:\n%s", want, script)
		}
	}

	// A second sync with identical data must not produce changes.
	for _, e := range data.Entries {
		cfg.ServerByID[e.Server.ID] = e.Server
		cfg.Servers = append(cfg.Servers, e.Server)
	}
	b2 := newUCIBatch("olcwrt")
	data2 := ParseSubscription(subDoc)
	if a, u, _ := syncSubServers(b2, cfg, "sub_1", data2.Entries); a != 0 || u != 0 {
		t.Fatalf("expected no changes, got added=%d updated=%d\n%s", a, u, b2.String())
	}
}

func TestOrphanBatch(t *testing.T) {
	pkg, _ := parseUCIShow("olcwrt", uciSample)
	cfg, _ := configFromUCI(pkg)
	if _, n := orphanBatch(cfg); n != 0 {
		t.Fatalf("no orphans expected, got %d", n)
	}
	cfg.ServerByID["s_a"].Subscription = "sub_gone"
	cfg.ServerByID["s_b"].Subscription = "sub_1" // still exists
	b, n := orphanBatch(cfg)
	if n != 1 || !strings.Contains(b.String(), "delete olcwrt.s_a\n") || strings.Contains(b.String(), "s_b\n") {
		t.Fatalf("n=%d\n%s", n, b.String())
	}
	if !strings.Contains(b.String(), "add_list olcwrt.main.server='s_b'\n") || strings.Contains(b.String(), "add_list olcwrt.main.server='s_a'") {
		t.Fatalf("selection not updated:\n%s", b.String())
	}
}

func TestSubDueAndInterval(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	sub := &Subscription{ID: "s", URL: "https://x", Enabled: true}
	if !subDue(sub, &SubMeta{}, now) {
		t.Fatal("never updated subscription must be due")
	}
	m := &SubMeta{LastUpdate: now.Add(-11 * time.Minute).Unix(), LastAttempt: now.Add(-11 * time.Minute).Unix(), Fields: map[string]string{"refresh": "10m"}}
	if !subDue(sub, m, now) {
		t.Fatal("refresh field must be honoured")
	}
	m.HeaderEvery = 3600
	if subDue(sub, m, now) {
		t.Fatal("header interval wins over refresh field")
	}
	sub.Interval = "5s"
	if got := subInterval(sub, m); got != minSubInterval {
		t.Fatalf("interval must be clamped, got %v", got)
	}
	sub.Interval = "0"
	if subDue(sub, m, now.Add(1000*time.Hour)) {
		t.Fatal("manual subscriptions are never due")
	}
	sub.Interval = ""
	m.Failures, m.LastAttempt = 3, now.Add(-10*time.Minute).Unix()
	if subDue(sub, m, now) {
		t.Fatal("third failure waits 20 minutes")
	}
	if !subDue(sub, m, now.Add(11*time.Minute)) {
		t.Fatal("retry after backoff")
	}
}

func TestApplyUserInfoHeader(t *testing.T) {
	h := http.Header{}
	h.Set("subscription-userinfo", "upload=1048576; download=1048576; total=10737418240; expire=1800000000")
	f := map[string]string{}
	applyUserInfoHeader(f, h)
	if f["used"] != "2.00mb/10.00gb" || f["available"] != "10.00gb" || f["expire"] != "1800000000" {
		t.Fatalf("got %v", f)
	}
	f = map[string]string{"used": "keep"}
	applyUserInfoHeader(f, h)
	if f["used"] != "keep" {
		t.Fatal("document fields win over the header")
	}
	h.Set("profile-update-interval", "12")
	if headerInterval(h) != 12*3600 {
		t.Fatal("profile-update-interval")
	}
}

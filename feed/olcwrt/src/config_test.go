package main

import (
	"strings"
	"testing"
	"time"
)

const uciSample = `olcwrt.main=main
olcwrt.main.enabled='1'
olcwrt.main.server='s_b' 's_missing' 's_a'
olcwrt.main.socks_port='8808'
olcwrt.main.dns_server='auto'
olcwrt.routing=routing
olcwrt.routing.mode='list'
olcwrt.routing.interface='lan' 'guest'
olcwrt.routing.source='192.168.1.10' 'aa:bb:cc:dd:ee:ff'
olcwrt.dns=dns
olcwrt.dns.upstream='9.9.9.9'
olcwrt.dns.set_timeout='2h'
olcwrt.s_a=server
olcwrt.s_a.label='It'\''s A'
olcwrt.s_a.provider='Jitsi'
olcwrt.s_a.transport='dc'
olcwrt.s_a.room='https://meet.example.org/a'
olcwrt.s_a.key='` + testKey + `'
olcwrt.s_b=server
olcwrt.s_b.label='B'
olcwrt.s_b.provider='wbstream'
olcwrt.s_b.transport='vp8channel'
olcwrt.s_b.room='room-b'
olcwrt.s_b.key='` + testKey + `'
olcwrt.s_b.vp8_fps='30'
olcwrt.cfg0a1b2c=server
olcwrt.cfg0a1b2c.label='broken'
olcwrt.cfg0a1b2c.provider='jitsi'
olcwrt.sub_1=subscription
olcwrt.sub_1.url='https://example.org/sub'
olcwrt.sub_1.enabled='0'
`

func TestParseUCIValues(t *testing.T) {
	vals, err := parseUCIValues(`'a' 'it'\''s' 'x y'`)
	if err != nil || len(vals) != 3 || vals[1] != "it's" || vals[2] != "x y" {
		t.Fatalf("got %q %v", vals, err)
	}
	if _, err := parseUCIValues(`'open`); err == nil {
		t.Fatal("expected error for unterminated quote")
	}
}

func TestConfigFromUCI(t *testing.T) {
	pkg, err := parseUCIShow("olcwrt", uciSample)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := configFromUCI(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.Mode != "list" || len(cfg.Interfaces) != 2 || cfg.SetTimeout != 2*time.Hour {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if len(cfg.Servers) != 3 || cfg.ServerByID["s_a"].Label != "It's A" || cfg.ServerByID["s_a"].Provider != "jitsi" ||
		cfg.ServerByID["s_a"].Transport != "datachannel" {
		t.Fatalf("servers: %+v", cfg.ServerByID["s_a"])
	}
	if cfg.ServerByID["s_b"].Opts["vp8_fps"] != "30" {
		t.Fatalf("opts: %v", cfg.ServerByID["s_b"].Opts)
	}
	if len(cfg.Subs) != 1 || cfg.Subs[0].Enabled || cfg.Subs[0].RequestMode != "identity" {
		t.Fatalf("subs: %+v", cfg.Subs[0])
	}
	chain, warnings := cfg.ActiveServers()
	if len(chain) != 2 || chain[0].ID != "s_b" || chain[1].ID != "s_a" || len(warnings) != 1 {
		t.Fatalf("chain=%v warnings=%v", chain, warnings)
	}
}

func TestActiveServersFallsBackToFirstValid(t *testing.T) {
	pkg, _ := parseUCIShow("olcwrt", strings.ReplaceAll(uciSample, "olcwrt.main.server='s_b' 's_missing' 's_a'\n", ""))
	cfg, _ := configFromUCI(pkg)
	chain, _ := cfg.ActiveServers()
	if len(chain) != 1 || chain[0].ID != "s_a" {
		t.Fatalf("chain=%v", chain)
	}
}

func TestConfigValidation(t *testing.T) {
	bad := []string{
		"olcwrt.main=main\nolcwrt.main.socks_port='18809'\n",
		"olcwrt.main=main\nolcwrt.main.lan_socks='1'\n",
		"olcwrt.routing=routing\nolcwrt.routing.mode='magic'\n",
		"olcwrt.main=main\nolcwrt.main.dns_server='1.2.3.4:99999'\n",
		"olcwrt.main=main\nolcwrt.main.dns_server='127.0.0.1'\n",
		"olcwrt.main=main\nolcwrt.main.dns_server='[::1]:53'\n",
		"olcwrt.main=main\nolcwrt.main.dns_server='localhost'\n",
	}
	for _, in := range bad {
		pkg, _ := parseUCIShow("olcwrt", in)
		if _, err := configFromUCI(pkg); err == nil {
			t.Errorf("expected validation error for %q", in)
		}
	}
}

func TestNormalizeHostPort(t *testing.T) {
	cases := map[string]string{
		"1.1.1.1":         "1.1.1.1:53",
		"1.1.1.1:5353":    "1.1.1.1:5353",
		"127.0.0.1#5053":  "127.0.0.1:5053",
		"2606:4700::1111": "[2606:4700::1111]:53",
		"[::1]:5353":      "[::1]:5353",
		"::1#5353":        "[::1]:5353",
		"dns.example":     "dns.example:53",
	}
	for in, want := range cases {
		got, err := normalizeHostPort(in, 53)
		if err != nil || got != want {
			t.Errorf("%s: got %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "1.2.3.4:0", "a:b:c:99999", "host:abc"} {
		if _, err := normalizeHostPort(in, 53); err == nil {
			t.Errorf("%q: expected error", in)
		}
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{"10m": 10 * time.Minute, "6h": 6 * time.Hour, "1d": 24 * time.Hour, "30": 30 * time.Second}
	for in, want := range cases {
		if got, ok := parseDuration(in); !ok || got != want {
			t.Errorf("%s: got %v %v", in, got, ok)
		}
	}
	if _, ok := parseDuration("soon"); ok {
		t.Error("expected failure")
	}
}

func TestNormalizeMemLimit(t *testing.T) {
	cases := map[string]string{"": "", "96": "96MiB", "96M": "96MiB", "96mb": "96MiB", "96MiB": "96MiB", "1g": "1GiB", "512k": "512KiB"}
	for in, want := range cases {
		if got, ok := normalizeMemLimit(in); !ok || got != want {
			t.Errorf("%q: got %q %v, want %q", in, got, ok, want)
		}
	}
	if _, ok := normalizeMemLimit("lots"); ok {
		t.Error("expected failure")
	}
}

func TestUCIBatchQuoting(t *testing.T) {
	b := newUCIBatch("olcwrt")
	b.Set("s_1", "", "server")
	b.Set("s_1", "label", "It's\nme")
	b.Set("s_1", "comment", "")
	b.SetList("main", "server", []string{"s_1", "s_2"})
	want := "set olcwrt.s_1='server'\n" +
		"set olcwrt.s_1.label='It'\\''s me'\n" +
		"delete olcwrt.s_1.comment\n" +
		"delete olcwrt.main.server\n" +
		"add_list olcwrt.main.server='s_1'\n" +
		"add_list olcwrt.main.server='s_2'\n"
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}

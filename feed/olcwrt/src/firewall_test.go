package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testLists(t *testing.T) (*RouteList, *RouteList) {
	t.Helper()
	p := newListAccumulator()
	p.read(strings.NewReader("example.com\n10.0.0.0/8\n1.2.3.4\n2001:db8::/32\n"))
	b := newListAccumulator()
	b.read(strings.NewReader("ru\n5.6.7.0/24\n"))
	return p.result(), b.result()
}

func TestBuildNftScriptAll(t *testing.T) {
	proxy, bypass := testLists(t)
	macs, v4, v6, bad := splitSources([]string{"AA:BB:CC:DD:EE:FF", "192.168.1.10", "fd00::5", "nonsense"})
	if len(bad) != 1 || len(macs) != 1 || len(v4) != 1 || len(v6) != 1 {
		t.Fatalf("splitSources: %v %v %v %v", macs, v4, v6, bad)
	}
	s := buildNftScript(&fwSpec{
		Mode: "all", Devices: []string{"br-lan", "br-guest"}, RedirPort: 18809, IPv6: true,
		BlockQUIC: true, DNSHijack: true, SourceMode: "exclude", SrcMAC: macs, SrcV4: v4, SrcV6: v6,
		Proxy: proxy, Bypass: bypass, SetTimeout: "43200s",
	})
	for _, want := range []string{
		"table inet olcwrt\ndelete table inet olcwrt\ntable inet olcwrt {\n",
		"type nat hook prerouting priority dstnat - 5; policy accept;",
		`iifname { "br-lan", "br-guest" } jump lan_dstnat`,
		"meta l4proto { tcp, udp } th dport 53 fib daddr type != local redirect to :53",
		"ether saddr @srcmac return\n\t\tip saddr @src4 return\n\t\tip6 saddr @src6 return\n\t\tgoto redirect_tcp",
		"ip daddr @bypass4 return",
		"ip6 daddr @bypass6_dns return",
		"\t\tmeta l4proto tcp redirect to :18809\n",
		"5.6.7.0/24",
		"192.168.0.0/16",
		"aa:bb:cc:dd:ee:ff",
		"timeout 43200s",
		"udp th dport 443 jump lan_quic",
		"\t\treject\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Count(s, "{") != strings.Count(s, "}") {
		t.Errorf("unbalanced braces:\n%s", s)
	}
}

func TestBuildNftScriptListNoIPv6(t *testing.T) {
	proxy, bypass := testLists(t)
	s := buildNftScript(&fwSpec{
		Mode: "list", Devices: []string{"br-lan"}, RedirPort: 1, IPv6: false,
		SourceMode: "include", SrcV4: parsePrefixes([]string{"192.168.1.0/28"}),
		Proxy: proxy, Bypass: bypass, SetTimeout: "60s",
	})
	for _, want := range []string{
		"ip saddr @src4 goto redirect_tcp\n\t\treturn",
		"meta nfproto ipv6 return",
		"ip daddr @proxy4 meta l4proto tcp redirect to :1",
		"ip daddr @proxy4_dns meta l4proto tcp redirect to :1",
		"1.2.3.4",
		"10.0.0.0/8",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	for _, unwanted := range []string{"ip6 daddr @proxy6", "lan_quic", "dport 53"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, s)
		}
	}
}

func TestDnsmasqPlan(t *testing.T) {
	dir := t.TempDir()
	insts := []dnsmasqInstance{{Name: "cfg01", ConfDir: filepath.Join(dir, "dnsmasq.cfg01.d")}}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(insts[0].ConfDir, name))
		return string(b)
	}

	all := dnsmasqPlan{Active: true, All: true, Port: 18853}
	restart, err := applyDnsmasqPlan(insts, all)
	if err != nil || !restart {
		t.Fatalf("first apply must restart: %v %v", restart, err)
	}
	if s := read(dnsmasqConfName); !strings.Contains(s, "servers-file="+filepath.Join(insts[0].ConfDir, dnsmasqServersName)) || !strings.Contains(s, "no-resolv") {
		t.Fatalf("snippet: %q", s)
	}
	if s := read(dnsmasqServersName); s != "server=127.0.0.1#18853\n" {
		t.Fatalf("servers: %q", s)
	}

	// Changing only the domains rewrites the servers file without a restart.
	all.Domains = []string{"example.com"}
	if restart, _ := applyDnsmasqPlan(insts, all); restart || !strings.Contains(read(dnsmasqServersName), "server=/example.com/127.0.0.1#18853\n") {
		t.Fatalf("domain change: restart=%v servers=%q", restart, read(dnsmasqServersName))
	}

	// Switching to list mode drops no-resolv, which needs a restart.
	lists := dnsmasqPlan{Active: true, Port: 18853, Domains: []string{"example.com", "ru"}}
	if restart, _ := applyDnsmasqPlan(insts, lists); !restart || strings.Contains(read(dnsmasqConfName), "no-resolv") {
		t.Fatalf("list mode: restart=%v snippet=%q", restart, read(dnsmasqConfName))
	}
	if s := read(dnsmasqServersName); s != "server=/example.com/127.0.0.1#18853\nserver=/ru/127.0.0.1#18853\n" {
		t.Fatalf("list servers: %q", s)
	}

	// Deactivation removes both files.
	if restart, _ := applyDnsmasqPlan(insts, dnsmasqPlan{}); !restart || read(dnsmasqConfName) != "" || read(dnsmasqServersName) != "" {
		t.Fatal("deactivation must remove the files")
	}
}

func TestDnsmasqWarnings(t *testing.T) {
	w := dnsmasqWarnings([]dnsmasqInstance{{Name: "cfg01", Servers: []string{"/lan/192.168.1.2", "8.8.8.8"}}}, true)
	if len(w) != 1 || !strings.Contains(w[0], "8.8.8.8") {
		t.Fatalf("warnings: %v", w)
	}
}

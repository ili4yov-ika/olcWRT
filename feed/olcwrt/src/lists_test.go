package main

import (
	"strings"
	"testing"
)

func TestListParsing(t *testing.T) {
	in := `# comment
Example.COM
*.wild.example.org
.dot.example.net
domain:v2.example
full:full.example
1.2.3.4
10.1.2.3/8 # trailing comment
2001:db8::1
nftset=/a.example/b.example/4#inet#fw4#vpn
server=/c.example/127.0.0.1#5353
0.0.0.0 hosts.example
bad_label-.example
-bad.example
`
	acc := newListAccumulator()
	bad := acc.read(strings.NewReader(in))
	l := acc.result()
	wantDomains := []string{"a.example", "b.example", "c.example", "dot.example.net", "example.com", "full.example", "v2.example", "wild.example.org"}
	if strings.Join(l.Domains, ",") != strings.Join(wantDomains, ",") {
		t.Fatalf("domains: %v", l.Domains)
	}
	var prefixes []string
	for _, p := range l.Prefixes {
		prefixes = append(prefixes, p.String())
	}
	// "0.0.0.0 hosts.example" keeps the first field only.
	want := "0.0.0.0/32,1.2.3.4/32,10.0.0.0/8,2001:db8::1/128"
	if strings.Join(prefixes, ",") != want {
		t.Fatalf("prefixes: %v", prefixes)
	}
	if bad != 2 {
		t.Fatalf("bad=%d", bad)
	}
	if len(l.v4()) != 3 || len(l.v6()) != 1 {
		t.Fatalf("v4=%v v6=%v", l.v4(), l.v6())
	}
}

func TestDomainMatcher(t *testing.T) {
	m := newDomainMatcher([]string{"example.com", "ru"})
	cases := map[string]int{
		"example.com":       11,
		"www.example.com":   11,
		"badexample.com":    -1,
		"yandex.ru":         2,
		"ru":                2,
		"example.org":       -1,
		"a.b.c.example.com": 11,
	}
	for name, want := range cases {
		if got := m.match(name); got != want {
			t.Errorf("%s: got %d want %d", name, got, want)
		}
	}
	var nilMatcher *domainMatcher
	if nilMatcher.match("x") != -1 {
		t.Fatal("nil matcher must not match")
	}
}

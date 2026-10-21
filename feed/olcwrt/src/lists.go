package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RouteList is the merged content of domain/IP lists of one kind.
type RouteList struct {
	Domains  []string
	Prefixes []netip.Prefix
}

func (l *RouteList) Empty() bool { return len(l.Domains) == 0 && len(l.Prefixes) == 0 }

// v4 and v6 split the prefixes by address family.
func (l *RouteList) v4() []netip.Prefix { return filterPrefixes(l.Prefixes, true) }
func (l *RouteList) v6() []netip.Prefix { return filterPrefixes(l.Prefixes, false) }

func filterPrefixes(in []netip.Prefix, v4 bool) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range in {
		if p.Addr().Is4() == v4 {
			out = append(out, p)
		}
	}
	return out
}

// listCachePath is where a downloaded list URL is stored.
func listCachePath(url string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(url)))
	return filepath.Join(listsDir(), hex.EncodeToString(sum[:6])+".lst")
}

// loadRouteList merges the user list file of the given kind ("proxy" or
// "bypass") with the cached downloads of its URLs.
func loadRouteList(kind string, urls []string) (*RouteList, []string) {
	acc := newListAccumulator()
	var warnings []string
	files := []string{userListPath(kind)}
	for _, u := range urls {
		files = append(files, listCachePath(u))
	}
	for i, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			if i > 0 || !os.IsNotExist(err) {
				warnings = append(warnings, fmt.Sprintf("%s list %s: not available", kind, displayListSource(i, urls, f)))
			}
			continue
		}
		bad := acc.read(fh)
		fh.Close()
		if bad > 0 {
			warnings = append(warnings, fmt.Sprintf("%s list %s: %d invalid entries ignored", kind, displayListSource(i, urls, f), bad))
		}
	}
	return acc.result(), warnings
}

func displayListSource(i int, urls []string, f string) string {
	if i == 0 {
		return f
	}
	return urls[i-1]
}

type listAccumulator struct {
	domains  map[string]bool
	prefixes map[netip.Prefix]bool
}

func newListAccumulator() *listAccumulator {
	return &listAccumulator{domains: map[string]bool{}, prefixes: map[netip.Prefix]bool{}}
}

// read consumes one list file and returns the number of rejected lines.
func (a *listAccumulator) read(r io.Reader) int {
	bad := 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		for _, entry := range listLineEntries(sc.Text()) {
			if p, ok := parsePrefix(entry); ok {
				a.prefixes[p] = true
			} else if d, ok := normalizeDomain(entry); ok {
				a.domains[d] = true
			} else {
				bad++
			}
		}
	}
	return bad
}

func (a *listAccumulator) result() *RouteList {
	l := &RouteList{}
	for d := range a.domains {
		l.Domains = append(l.Domains, d)
	}
	for p := range a.prefixes {
		l.Prefixes = append(l.Prefixes, p)
	}
	sort.Strings(l.Domains)
	sort.Slice(l.Prefixes, func(i, j int) bool { return l.Prefixes[i].String() < l.Prefixes[j].String() })
	return l
}

// listLineEntries extracts entries from one line. Besides plain domains and
// CIDRs it understands comments, v2ray-style "domain:"/"full:" prefixes,
// wildcards and dnsmasq "server=/a/b/..." or "nftset=/a/b/..." lines.
func listLineEntries(line string) []string {
	if i := strings.IndexAny(line, "#;"); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	if eq := strings.IndexByte(line, '='); eq > 0 && strings.Contains(line[eq:], "/") {
		parts := strings.Split(line[eq+1:], "/")
		if len(parts) >= 3 {
			var out []string
			for _, p := range parts[1 : len(parts)-1] {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
			return out
		}
	}
	for _, prefix := range []string{"domain:", "full:", "domain-suffix,", "host-suffix,", "ip-cidr,"} {
		if strings.HasPrefix(strings.ToLower(line), prefix) {
			line = line[len(prefix):]
			break
		}
	}
	if f := strings.Fields(line); len(f) > 0 {
		line = f[0]
	}
	return []string{line}
}

func parsePrefix(s string) (netip.Prefix, bool) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, false
		}
		return p.Masked(), true
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, false
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), true
}

// normalizeDomain lower-cases a domain, strips wildcards and dots and checks
// that it only contains valid hostname characters.
func normalizeDomain(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "*.")
	s = strings.Trim(s, ".")
	if s == "" || len(s) > 253 {
		return "", false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return "", false
			}
		}
	}
	return s, true
}

// domainMatcher answers suffix lookups for the DNS classifier.
type domainMatcher struct {
	set map[string]struct{}
}

func newDomainMatcher(domains []string) *domainMatcher {
	m := &domainMatcher{set: make(map[string]struct{}, len(domains))}
	for _, d := range domains {
		m.set[d] = struct{}{}
	}
	return m
}

// match returns the length of the longest listed suffix of name, or -1.
func (m *domainMatcher) match(name string) int {
	if m == nil || len(m.set) == 0 {
		return -1
	}
	for {
		if _, ok := m.set[name]; ok {
			return len(name)
		}
		i := strings.IndexByte(name, '.')
		if i < 0 {
			return -1
		}
		name = name[i+1:]
	}
}

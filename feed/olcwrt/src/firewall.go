package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const nftTable = "olcwrt"

// Destinations that are never redirected.
var (
	reservedV4 = []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	}
	reservedV6 = []string{
		"::/127", "::ffff:0:0/96", "100::/64", "2001:db8::/32", "fc00::/7", "fe80::/10", "ff00::/8",
	}
)

// fwSpec is everything the generated ruleset depends on.
type fwSpec struct {
	Mode       string
	Devices    []string
	RedirPort  int
	IPv6       bool
	BlockQUIC  bool
	DNSHijack  bool
	SourceMode string
	SrcMAC     []string
	SrcV4      []netip.Prefix
	SrcV6      []netip.Prefix
	Proxy      *RouteList
	Bypass     *RouteList
	SetTimeout string
}

// buildNftScript renders an nft script that atomically replaces the table.
func buildNftScript(s *fwSpec) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w("table inet %s", nftTable)
	w("delete table inet %s", nftTable)
	w("table inet %s {", nftTable)

	writeIntervalSet(&b, "bypass4", "ipv4_addr", append(parsePrefixes(reservedV4), s.Bypass.v4()...))
	writeIntervalSet(&b, "bypass6", "ipv6_addr", append(parsePrefixes(reservedV6), s.Bypass.v6()...))
	writeIntervalSet(&b, "proxy4", "ipv4_addr", s.Proxy.v4())
	writeIntervalSet(&b, "proxy6", "ipv6_addr", s.Proxy.v6())
	for _, set := range []struct{ name, typ string }{
		{"bypass4_dns", "ipv4_addr"}, {"bypass6_dns", "ipv6_addr"},
		{"proxy4_dns", "ipv4_addr"}, {"proxy6_dns", "ipv6_addr"},
	} {
		w("\tset %s {\n\t\ttype %s\n\t\tflags timeout\n\t\ttimeout %s\n\t\tsize 65535\n\t}", set.name, set.typ, s.SetTimeout)
	}
	writeIntervalSet(&b, "src4", "ipv4_addr", s.SrcV4)
	writeIntervalSet(&b, "src6", "ipv6_addr", s.SrcV6)
	writePlainSet(&b, "srcmac", "ether_addr", s.SrcMAC)

	ifaces := nftStringSet(s.Devices)
	w("\tchain dstnat {")
	w("\t\ttype nat hook prerouting priority dstnat - 5; policy accept;")
	w("\t\tiifname %s jump lan_dstnat", ifaces)
	w("\t}")
	w("\tchain lan_dstnat {")
	if s.DNSHijack {
		w("\t\tmeta l4proto { tcp, udp } th dport 53 fib daddr type != local redirect to :53")
	}
	w("\t\tmeta l4proto != tcp return")
	w("\t\tfib daddr type local return")
	writeSourceFilter(&b, s, "redirect_tcp")
	w("\t}")

	w("\tchain redirect_tcp {")
	writeBypass(&b, s.IPv6)
	// nft only accepts a port mapping after an explicit protocol match.
	writeTargetRules(&b, s, fmt.Sprintf("meta l4proto tcp redirect to :%d", s.RedirPort))
	w("\t}")

	if s.BlockQUIC {
		w("\tchain forward {")
		w("\t\ttype filter hook forward priority filter - 5; policy accept;")
		w("\t\tiifname %s meta l4proto udp th dport 443 jump lan_quic", ifaces)
		w("\t}")
		w("\tchain lan_quic {")
		writeSourceFilter(&b, s, "reject_quic")
		w("\t}")
		w("\tchain reject_quic {")
		writeBypass(&b, s.IPv6)
		writeTargetRules(&b, s, "reject")
		w("\t}")
	}
	w("}")
	return b.String()
}

// writeSourceFilter sends matching clients to the target chain.
func writeSourceFilter(b *strings.Builder, s *fwSpec, target string) {
	matchers := []string{}
	if len(s.SrcMAC) > 0 {
		matchers = append(matchers, "ether saddr @srcmac")
	}
	if len(s.SrcV4) > 0 {
		matchers = append(matchers, "ip saddr @src4")
	}
	if len(s.SrcV6) > 0 {
		matchers = append(matchers, "ip6 saddr @src6")
	}
	switch {
	case s.SourceMode == "include":
		for _, m := range matchers {
			fmt.Fprintf(b, "\t\t%s goto %s\n", m, target)
		}
		b.WriteString("\t\treturn\n")
	case s.SourceMode == "exclude":
		for _, m := range matchers {
			fmt.Fprintf(b, "\t\t%s return\n", m)
		}
		fmt.Fprintf(b, "\t\tgoto %s\n", target)
	default:
		fmt.Fprintf(b, "\t\tgoto %s\n", target)
	}
}

func writeBypass(b *strings.Builder, ipv6 bool) {
	b.WriteString("\t\tip daddr @bypass4 return\n")
	b.WriteString("\t\tip daddr @bypass4_dns return\n")
	if ipv6 {
		b.WriteString("\t\tip6 daddr @bypass6 return\n")
		b.WriteString("\t\tip6 daddr @bypass6_dns return\n")
	} else {
		b.WriteString("\t\tmeta nfproto ipv6 return\n")
	}
}

// writeTargetRules applies verdict to the traffic selected by the mode: every
// destination in "all" mode, listed destinations only in "list" mode.
func writeTargetRules(b *strings.Builder, s *fwSpec, verdict string) {
	if s.Mode != "list" {
		fmt.Fprintf(b, "\t\t%s\n", verdict)
		return
	}
	sets := []string{"ip daddr @proxy4", "ip daddr @proxy4_dns"}
	if s.IPv6 {
		sets = append(sets, "ip6 daddr @proxy6", "ip6 daddr @proxy6_dns")
	}
	for _, m := range sets {
		fmt.Fprintf(b, "\t\t%s %s\n", m, verdict)
	}
}

func writeIntervalSet(b *strings.Builder, name, typ string, prefixes []netip.Prefix) {
	fmt.Fprintf(b, "\tset %s {\n\t\ttype %s\n\t\tflags interval\n\t\tauto-merge\n", name, typ)
	writeElements(b, prefixStrings(prefixes))
	b.WriteString("\t}\n")
}

func writePlainSet(b *strings.Builder, name, typ string, elems []string) {
	fmt.Fprintf(b, "\tset %s {\n\t\ttype %s\n", name, typ)
	writeElements(b, elems)
	b.WriteString("\t}\n")
}

func writeElements(b *strings.Builder, elems []string) {
	if len(elems) == 0 {
		return
	}
	b.WriteString("\t\telements = {")
	for i, e := range elems {
		if i > 0 {
			b.WriteString(",")
		}
		if i%8 == 0 {
			b.WriteString("\n\t\t\t")
		} else {
			b.WriteString(" ")
		}
		b.WriteString(e)
	}
	b.WriteString("\n\t\t}\n")
}

func prefixStrings(in []netip.Prefix) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		if p.IsSingleIP() {
			out = append(out, p.Addr().String())
		} else {
			out = append(out, p.String())
		}
	}
	return out
}

func parsePrefixes(in []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(in))
	for _, s := range in {
		if p, ok := parsePrefix(s); ok {
			out = append(out, p)
		}
	}
	return out
}

func nftStringSet(items []string) string {
	q := make([]string, 0, len(items))
	for _, it := range items {
		q = append(q, fmt.Sprintf("%q", it))
	}
	return "{ " + strings.Join(q, ", ") + " }"
}

// splitSources sorts client selectors into MAC addresses and IP prefixes.
func splitSources(sources []string) (macs []string, v4, v6 []netip.Prefix, bad []string) {
	for _, src := range sources {
		if hw, err := net.ParseMAC(src); err == nil && len(hw) == 6 {
			macs = append(macs, hw.String())
			continue
		}
		p, ok := parsePrefix(src)
		switch {
		case !ok:
			bad = append(bad, src)
		case p.Addr().Is4():
			v4 = append(v4, p)
		default:
			v6 = append(v6, p)
		}
	}
	return
}

// applyNft loads a script with `nft -f`.
func applyNft(script string) error {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(runDir, "olcwrt.nft")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		return err
	}
	out, err := exec.Command("nft", "-f", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func nftTableExists() bool {
	return exec.Command("nft", "list", "table", "inet", nftTable).Run() == nil
}

func removeNft() error {
	if !nftTableExists() {
		return nil
	}
	out, err := exec.Command("nft", "delete", "table", "inet", nftTable).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft delete table: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// resolveDevices maps logical OpenWrt interfaces (e.g. "lan") to their L3
// devices; names that are already devices are kept as they are.
func resolveDevices(names []string) ([]string, []string) {
	var devs, warnings []string
	seen := map[string]bool{}
	add := func(d string) {
		if d != "" && !seen[d] {
			seen[d] = true
			devs = append(devs, d)
		}
	}
	for _, n := range names {
		if d := ubusInterfaceDevice(n); d != "" {
			add(d)
			continue
		}
		if _, err := os.Stat(filepath.Join("/sys/class/net", n)); err == nil {
			add(n)
			continue
		}
		warnings = append(warnings, fmt.Sprintf("interface %q not found", n))
	}
	return devs, warnings
}

func ubusInterfaceDevice(name string) string {
	out, err := exec.Command("ubus", "call", "network.interface."+name, "status").Output()
	if err != nil {
		return ""
	}
	var st struct {
		L3Device string `json:"l3_device"`
		Device   string `json:"device"`
	}
	if json.Unmarshal(bytes.TrimSpace(out), &st) != nil {
		return ""
	}
	if st.L3Device != "" {
		return st.L3Device
	}
	return st.Device
}

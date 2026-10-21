package main

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// uciSection is one UCI section. `uci show` does not distinguish an option
// from a single-element list, so every value is kept as a slice and the
// schema decides how it is read.
type uciSection struct {
	Name string
	Type string
	opts map[string][]string
}

func (s *uciSection) Get(key string) string {
	if v := s.opts[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func (s *uciSection) GetDef(key, def string) string {
	if v := s.Get(key); v != "" {
		return v
	}
	return def
}

func (s *uciSection) List(key string) []string {
	out := make([]string, 0, len(s.opts[key]))
	for _, v := range s.opts[key] {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (s *uciSection) Bool(key string, def bool) bool {
	switch strings.ToLower(s.Get(key)) {
	case "1", "on", "yes", "true", "enabled":
		return true
	case "0", "off", "no", "false", "disabled":
		return false
	}
	return def
}

func (s *uciSection) Int(key string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s.Get(key))); err == nil {
		return n
	}
	return def
}

// uciPackage is a parsed UCI configuration package.
type uciPackage struct {
	Name     string
	Sections []*uciSection
	index    map[string]*uciSection
}

func newUCIPackage(name string) *uciPackage {
	return &uciPackage{Name: name, index: map[string]*uciSection{}}
}

// Section returns the named section, or an empty one so lookups on a missing
// section fall back to defaults.
func (p *uciPackage) Section(name string) *uciSection {
	if s := p.index[name]; s != nil {
		return s
	}
	return &uciSection{Name: name, opts: map[string][]string{}}
}

func (p *uciPackage) Has(name string) bool { return p.index[name] != nil }

func (p *uciPackage) OfType(typ string) []*uciSection {
	var out []*uciSection
	for _, s := range p.Sections {
		if s.Type == typ {
			out = append(out, s)
		}
	}
	return out
}

var errUCIQuote = errors.New("unterminated quote in uci value")

// parseUCIShow parses the output of `uci -X show <pkg>`.
func parseUCIShow(pkg, out string) (*uciPackage, error) {
	p := newUCIPackage(pkg)
	prefix := pkg + "."
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		path := strings.Split(line[len(prefix):eq], ".")
		value := line[eq+1:]
		switch len(path) {
		case 1:
			s := &uciSection{Name: path[0], Type: value, opts: map[string][]string{}}
			p.Sections = append(p.Sections, s)
			p.index[s.Name] = s
		case 2:
			s := p.index[path[0]]
			if s == nil {
				continue
			}
			vals, err := parseUCIValues(value)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", path[0], path[1], err)
			}
			s.opts[path[1]] = vals
		}
	}
	return p, nil
}

// parseUCIValues splits a shell-quoted value list as printed by `uci show`,
// for example 'a' 'it'\”s'.
func parseUCIValues(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, errUCIQuote
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 2
			inWord = true
		case c == '\\' && i+1 < len(s):
			cur.WriteByte(s[i+1])
			i += 2
			inWord = true
		case c == ' ' || c == '\t':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
			i++
		default:
			cur.WriteByte(c)
			i++
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

// loadUCI reads a package through the uci CLI. A missing package yields an
// empty result rather than an error.
func loadUCI(pkg string) (*uciPackage, error) {
	cmd := exec.Command("uci", "-q", "-X", "show", pkg)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && stdout.Len() == 0 {
			return newUCIPackage(pkg), nil
		}
		return nil, fmt.Errorf("uci show %s: %w", pkg, err)
	}
	return parseUCIShow(pkg, stdout.String())
}

// uciBatch accumulates `uci batch` commands for one package.
type uciBatch struct {
	pkg string
	buf strings.Builder
	n   int
}

func newUCIBatch(pkg string) *uciBatch { return &uciBatch{pkg: pkg} }

func uciQuote(v string) string {
	v = strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

func (b *uciBatch) path(sec, opt string) string {
	if opt == "" {
		return b.pkg + "." + sec
	}
	return b.pkg + "." + sec + "." + opt
}

// Set assigns an option, or creates a section of type val when opt is empty.
// Empty option values delete the option, mirroring LuCI behaviour.
func (b *uciBatch) Set(sec, opt, val string) {
	if opt != "" && val == "" {
		b.Delete(sec, opt)
		return
	}
	fmt.Fprintf(&b.buf, "set %s=%s\n", b.path(sec, opt), uciQuote(val))
	b.n++
}

func (b *uciBatch) Delete(sec, opt string) {
	fmt.Fprintf(&b.buf, "delete %s\n", b.path(sec, opt))
	b.n++
}

func (b *uciBatch) SetList(sec, opt string, vals []string) {
	b.Delete(sec, opt)
	for _, v := range vals {
		fmt.Fprintf(&b.buf, "add_list %s=%s\n", b.path(sec, opt), uciQuote(v))
		b.n++
	}
}

func (b *uciBatch) Empty() bool { return b.n == 0 }

func (b *uciBatch) String() string { return b.buf.String() }

// Commit applies the batch and commits the package.
func (b *uciBatch) Commit() error {
	if b.Empty() {
		return nil
	}
	cmd := exec.Command("uci", "batch")
	cmd.Stdin = strings.NewReader(b.buf.String() + "commit " + b.pkg + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("uci batch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// uci batch reports failed commands on stderr but may still exit 0.
	// Deleting an option that does not exist is expected and harmless.
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "uci:") && !strings.Contains(line, "Entry not found") {
			return fmt.Errorf("uci batch: %s", strings.TrimSpace(line))
		}
	}
	return nil
}

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SubMeta is the persisted state of a subscription, shown in LuCI.
type SubMeta struct {
	ID          string            `json:"id"`
	Name        string            `json:"name,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
	Count       int               `json:"count"`
	LastUpdate  int64             `json:"last_update,omitempty"`
	LastAttempt int64             `json:"last_attempt,omitempty"`
	LastError   string            `json:"last_error,omitempty"`
	Failures    int               `json:"failures"`
	HeaderEvery int64             `json:"header_interval,omitempty"` // seconds
}

func subMetaPath(id string) string { return filepath.Join(subsDir(), id+".json") }

func loadSubMeta(id string) *SubMeta {
	m := &SubMeta{ID: id}
	_ = readJSON(subMetaPath(id), m)
	m.ID = id
	return m
}

func saveSubMeta(m *SubMeta) error { return writeJSON(subMetaPath(m.ID), m, 0o644) }

const (
	minSubInterval     = 5 * time.Minute
	defaultSubInterval = 24 * time.Hour
)

// subInterval returns the refresh period, or 0 for manual updates only.
func subInterval(sub *Subscription, m *SubMeta) time.Duration {
	if sub.Interval == "0" {
		return 0
	}
	d, ok := parseDuration(sub.Interval)
	if !ok && m.HeaderEvery > 0 {
		d, ok = time.Duration(m.HeaderEvery)*time.Second, true
	}
	if !ok {
		d, ok = parseDuration(m.Fields["refresh"])
	}
	if !ok || d == 0 {
		d = defaultSubInterval
	}
	return max(d, minSubInterval)
}

// subDue reports whether an automatic refresh should run now.
func subDue(sub *Subscription, m *SubMeta, now time.Time) bool {
	if !sub.Enabled || sub.URL == "" {
		return false
	}
	if m.LastUpdate == 0 && m.LastAttempt == 0 {
		return true
	}
	if m.Failures > 0 {
		retry := min(5*time.Minute<<min(m.Failures-1, 6), 6*time.Hour)
		return now.Sub(time.Unix(m.LastAttempt, 0)) >= retry
	}
	every := subInterval(sub, m)
	return every > 0 && now.Sub(time.Unix(m.LastUpdate, 0)) >= every
}

// hwid returns the persistent device identifier sent to subscription servers.
func hwid() string {
	path := filepath.Join(dataDir, "hwid")
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id
		}
	}
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	id := "install-" + hex.EncodeToString(buf)
	_ = writeFileAtomic(path, []byte(id+"\n"), 0o600)
	return id
}

func subHeaders(sub *Subscription) map[string]string {
	if sub.RequestMode == "browser" {
		return map[string]string{
			"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36",
		}
	}
	return map[string]string{
		"User-Agent":     "olcwrt/" + version,
		"x-hwid":         hwid(),
		"x-device-os":    "OpenWrt",
		"x-device-model": deviceModel(),
	}
}

func deviceModel() string {
	b, err := os.ReadFile("/tmp/sysinfo/model")
	if err != nil {
		return "router"
	}
	// Keep printable ASCII only: anything else is not a valid header value.
	model := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return -1
		}
		return r
	}, strings.TrimSpace(string(b)))
	if model == "" {
		return "router"
	}
	return model
}

// SubResult summarises one subscription refresh.
type SubResult struct {
	ID      string   `json:"id"`
	OK      bool     `json:"ok"`
	Error   string   `json:"error,omitempty"`
	Added   int      `json:"added"`
	Updated int      `json:"updated"`
	Removed int      `json:"removed"`
	Total   int      `json:"total"`
	Route   string   `json:"route,omitempty"`
	Skipped []string `json:"skipped,omitempty"`
}

// updateSubscription downloads sub and synchronises its servers into UCI.
// The caller must hold the config lock.
func updateSubscription(ctx context.Context, cfg *Config, sub *Subscription) *SubResult {
	res := &SubResult{ID: sub.ID}
	meta := loadSubMeta(sub.ID)
	meta.LastAttempt = time.Now().Unix()

	fail := func(err error) *SubResult {
		meta.LastError = err.Error()
		meta.Failures++
		_ = saveSubMeta(meta)
		res.Error = err.Error()
		return res
	}

	routes := []string{routeDirect, routeTunnel}
	if sub.ViaTunnel {
		routes = []string{routeTunnel, routeDirect}
	}
	fr, err := newFetcher(cfg).fetch(ctx, sub.URL, subHeaders(sub), routes...)
	if err != nil {
		return fail(err)
	}
	data := ParseSubscription(fr.Body)
	res.Skipped = data.Errors
	if len(data.Entries) == 0 {
		return fail(errors.New("no valid olcrtc:// servers in the subscription"))
	}

	b := newUCIBatch(uciPkg)
	res.Added, res.Updated, res.Removed = syncSubServers(b, cfg, sub.ID, data.Entries)
	if sub.Label == "" && data.Meta["name"] != "" {
		b.Set(sub.ID, "label", data.Meta["name"])
	}
	if err := b.Commit(); err != nil {
		return fail(err)
	}

	meta.Name = data.Meta["name"]
	meta.Fields = data.Meta
	applyUserInfoHeader(meta.Fields, fr.Header)
	meta.Count = len(data.Entries)
	meta.LastUpdate = meta.LastAttempt
	meta.LastError = ""
	meta.Failures = 0
	meta.HeaderEvery = headerInterval(fr.Header)
	_ = saveSubMeta(meta)

	res.OK, res.Total, res.Route = true, len(data.Entries), fr.Route
	return res
}

// syncSubServers makes the servers owned by subID match entries.
func syncSubServers(b *uciBatch, cfg *Config, subID string, entries []SubEntry) (added, updated, removed int) {
	keep := map[string]bool{}
	var firstNew string
	for _, e := range entries {
		s := e.Server
		if keep[s.ID] {
			continue
		}
		keep[s.ID] = true
		if firstNew == "" {
			firstNew = s.ID
		}
		s.Subscription = subID
		old := cfg.ServerByID[s.ID]
		if writeServer(b, old, s) {
			if old == nil {
				added++
			} else {
				updated++
			}
		}
	}

	var dropped []string
	for _, s := range cfg.Servers {
		if s.Subscription == subID && !keep[s.ID] {
			b.Delete(s.ID, "")
			dropped = append(dropped, s.ID)
			removed++
		}
	}
	if len(dropped) == 0 {
		return
	}
	var sel []string
	lostOwn := false
	for _, id := range cfg.Selected {
		if contains(dropped, id) {
			lostOwn = true
			continue
		}
		sel = append(sel, id)
	}
	if len(sel) == 0 && lostOwn {
		sel = []string{firstNew}
	}
	b.SetList("main", "server", sel)
	return
}

// pruneOrphans deletes servers whose subscription section was removed, for
// example from the command line. The caller must hold the config lock.
func pruneOrphans(cfg *Config) (int, error) {
	b, n := orphanBatch(cfg)
	if n == 0 {
		return 0, nil
	}
	return n, b.Commit()
}

func orphanBatch(cfg *Config) (*uciBatch, int) {
	b := newUCIBatch(uciPkg)
	var gone []string
	for _, s := range cfg.Servers {
		if s.Subscription != "" && cfg.SubByID[s.Subscription] == nil {
			b.Delete(s.ID, "")
			gone = append(gone, s.ID)
		}
	}
	if len(gone) == 0 {
		return b, 0
	}
	var sel []string
	for _, id := range cfg.Selected {
		if !contains(gone, id) {
			sel = append(sel, id)
		}
	}
	if len(sel) != len(cfg.Selected) {
		b.SetList("main", "server", sel)
	}
	return b, len(gone)
}

// writeServer stores s, writing only changed options. It reports whether
// anything changed.
func writeServer(b *uciBatch, old, s *Server) bool {
	if old == nil {
		old = &Server{Opts: map[string]string{}}
		b.Set(s.ID, "", "server")
	}
	n := b.n
	set := func(opt, nv, ov string) {
		if nv != ov {
			b.Set(s.ID, opt, nv)
		}
	}
	set("label", s.Label, old.Label)
	set("provider", s.Provider, old.Provider)
	set("transport", s.Transport, old.Transport)
	set("room", s.Room, old.Room)
	set("key", s.Key, old.Key)
	set("comment", s.Comment, old.Comment)
	set("subscription", s.Subscription, old.Subscription)
	for _, o := range transportOpts {
		set(o.uci, s.Opts[o.uci], old.Opts[o.uci])
	}
	return b.n != n
}

// applyUserInfoHeader maps the common "subscription-userinfo" header
// (upload=..; download=..; total=..; expire=..) to used/available/expire.
func applyUserInfoHeader(fields map[string]string, h http.Header) {
	v := h.Get("subscription-userinfo")
	if v == "" {
		return
	}
	vals := map[string]int64{}
	for _, part := range strings.Split(v, ";") {
		k, num, ok := strings.Cut(strings.TrimSpace(part), "=")
		if n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64); ok && err == nil {
			vals[strings.ToLower(strings.TrimSpace(k))] = n
		}
	}
	used := vals["upload"] + vals["download"]
	if _, ok := fields["used"]; !ok && (used > 0 || vals["total"] > 0) {
		fields["used"] = humanBytes(used) + "/" + humanBytes(vals["total"])
	}
	if _, ok := fields["available"]; !ok && vals["total"] > 0 {
		fields["available"] = humanBytes(max(vals["total"]-used, 0))
	}
	if exp := vals["expire"]; exp > 0 {
		fields["expire"] = strconv.FormatInt(exp, 10)
	}
}

func headerInterval(h http.Header) int64 {
	hours, err := strconv.Atoi(strings.TrimSpace(h.Get("profile-update-interval")))
	if err != nil || hours <= 0 {
		return 0
	}
	return int64(min(hours, 720)) * 3600
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%db", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f%cb", float64(n)/float64(div), "kmgtp"[exp])
}

// ImportResult summarises an import.
type ImportResult struct {
	Added         int      `json:"added"`
	Updated       int      `json:"updated"`
	Servers       []string `json:"servers,omitempty"`
	Subscriptions []string `json:"subscriptions,omitempty"`
	Errors        []string `json:"errors,omitempty"`
}

// importText imports olcrtc:// links, subscription documents or subscription
// URLs (one per line). New subscriptions are only registered here; the
// caller downloads them. The caller must hold the config lock.
func importText(cfg *Config, text string) (*ImportResult, error) {
	text = strings.TrimSpace(strings.TrimPrefix(text, utf8BOM))
	if text == "" {
		return nil, errors.New("nothing to import")
	}
	res := &ImportResult{}
	b := newUCIBatch(uciPkg)

	if urls, ok := subscriptionURLs(text); ok {
		for _, u := range urls {
			id := subscriptionID(u)
			if cfg.SubByID[id] == nil {
				b.Set(id, "", "subscription")
				b.Set(id, "url", u)
				b.Set(id, "label", hostLabel(u))
				b.Set(id, "enabled", "1")
				cfg.SubByID[id] = &Subscription{ID: id, URL: u}
			}
			res.Subscriptions = append(res.Subscriptions, id)
		}
		return res, b.Commit()
	}

	data := ParseSubscription(text)
	res.Errors = data.Errors
	if len(data.Entries) == 0 {
		if len(res.Errors) == 0 {
			res.Errors = []string{"no olcrtc:// links or subscription URLs found"}
		}
		return res, errors.New(res.Errors[0])
	}
	for _, e := range data.Entries {
		s := e.Server
		old := cfg.ServerByID[s.ID]
		if old != nil {
			s.Subscription = old.Subscription
		}
		if writeServer(b, old, s) {
			if old == nil {
				res.Added++
			} else {
				res.Updated++
			}
		}
		cfg.ServerByID[s.ID] = s
		res.Servers = append(res.Servers, s.ID)
	}
	if len(cfg.Selected) == 0 && len(res.Servers) > 0 {
		b.SetList("main", "server", res.Servers[:1])
	}
	return res, b.Commit()
}

// subscriptionURLs accepts one or more http(s) URLs, one per line, as well
// as olcbox://add?url=... deep links.
func subscriptionURLs(text string) ([]string, bool) {
	var urls []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "olcbox://add?") {
			if q, err := url.ParseQuery(line[len("olcbox://add?"):]); err == nil {
				line = strings.TrimSpace(q.Get("url"))
			}
		}
		if !isHTTPURL(line) {
			return nil, false
		}
		urls = append(urls, line)
	}
	return urls, len(urls) > 0
}

func hostLabel(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

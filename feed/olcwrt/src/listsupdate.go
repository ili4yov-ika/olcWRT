package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ListMeta is the persisted download state of one list URL.
type ListMeta struct {
	URL         string `json:"url"`
	Kind        string `json:"kind"`
	Entries     int    `json:"entries"`
	LastUpdate  int64  `json:"last_update,omitempty"`
	LastAttempt int64  `json:"last_attempt,omitempty"`
	LastError   string `json:"last_error,omitempty"`
}

const (
	listInterval    = 24 * time.Hour
	listRetryPeriod = 30 * time.Minute
)

func listIndexPath() string { return filepath.Join(listsDir(), "index.json") }

func loadListIndex() map[string]*ListMeta {
	idx := map[string]*ListMeta{}
	_ = readJSON(listIndexPath(), &idx)
	return idx
}

type listURL struct {
	url, kind string
}

func configuredListURLs(cfg *Config) []listURL {
	var out []listURL
	for _, u := range cfg.ProxyLists {
		out = append(out, listURL{u, "proxy"})
	}
	for _, u := range cfg.BypassLists {
		out = append(out, listURL{u, "bypass"})
	}
	return out
}

// listsDue reports whether any configured list needs downloading.
func listsDue(cfg *Config, now time.Time) bool {
	idx := loadListIndex()
	for _, l := range configuredListURLs(cfg) {
		if listDue(idx[l.url], l.url, now) {
			return true
		}
	}
	return false
}

func listDue(m *ListMeta, url string, now time.Time) bool {
	if _, err := os.Stat(listCachePath(url)); err != nil {
		if m == nil || m.LastAttempt == 0 {
			return true
		}
		return now.Sub(time.Unix(m.LastAttempt, 0)) >= listRetryPeriod
	}
	if m == nil {
		return true
	}
	if m.LastError != "" && now.Sub(time.Unix(m.LastAttempt, 0)) < listRetryPeriod {
		return false
	}
	return now.Sub(time.Unix(m.LastUpdate, 0)) >= listInterval
}

// updateLists downloads every configured list (or only due ones) and
// returns the per-URL state. It reports whether any cached file changed.
func updateLists(ctx context.Context, cfg *Config, onlyDue bool) (map[string]*ListMeta, bool) {
	idx := loadListIndex()
	f := newFetcher(cfg)
	now := time.Now()
	changed := false
	fresh := map[string]*ListMeta{}
	for _, l := range configuredListURLs(cfg) {
		m := idx[l.url]
		if m == nil {
			m = &ListMeta{URL: l.url}
		}
		m.Kind = l.kind
		fresh[l.url] = m
		if onlyDue && !listDue(m, l.url, now) {
			continue
		}
		m.LastAttempt = now.Unix()
		n, diff, err := downloadList(ctx, f, l.url)
		if err != nil {
			m.LastError = err.Error()
			continue
		}
		m.Entries, m.LastUpdate, m.LastError = n, now.Unix(), ""
		changed = changed || diff
	}
	_ = os.MkdirAll(listsDir(), 0o755)
	_ = writeJSON(listIndexPath(), fresh, 0o644)
	removeStaleLists(fresh)
	return fresh, changed
}

func downloadList(ctx context.Context, f *fetcher, url string) (int, bool, error) {
	if !isHTTPURL(url) {
		return 0, false, errors.New("not an http(s) URL")
	}
	res, err := f.fetch(ctx, url, map[string]string{"User-Agent": "olcwrt/" + version}, routeDirect, routeTunnel)
	if err != nil {
		return 0, false, err
	}
	acc := newListAccumulator()
	acc.read(strings.NewReader(res.Body))
	l := acc.result()
	n := len(l.Domains) + len(l.Prefixes)
	if n == 0 {
		return 0, false, errors.New("no domains or addresses in the list")
	}
	path := listCachePath(url)
	if old, err := os.ReadFile(path); err == nil && string(old) == res.Body {
		return n, false, nil
	}
	return n, true, writeFileAtomic(path, []byte(res.Body), 0o644)
}

// removeStaleLists deletes cached downloads of URLs no longer configured.
func removeStaleLists(keep map[string]*ListMeta) {
	want := map[string]bool{}
	for u := range keep {
		want[filepath.Base(listCachePath(u))] = true
	}
	entries, _ := os.ReadDir(listsDir())
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".lst") && !want[e.Name()] {
			_ = os.Remove(filepath.Join(listsDir(), e.Name()))
		}
	}
}

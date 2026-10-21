package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// loadConfigLenient returns the configuration even if validation failed,
// for commands that only manage servers and subscriptions.
func loadConfigLenient() (*Config, error) {
	pkg, err := loadUCI(uciPkg)
	if err != nil {
		return nil, err
	}
	cfg, _ := configFromUCI(pkg)
	return cfg, nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// withConfigLock runs fn with the UCI modification lock held.
func withConfigLock(fn func(cfg *Config) error) error {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	unlock, err := lockFile(lockPath())
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := loadConfigLenient()
	if err != nil {
		return err
	}
	return fn(cfg)
}

func reloadIfRunning() {
	if err := signalDaemonReload(); err != nil && !errors.Is(err, errNotRunning) {
		fmt.Fprintln(os.Stderr, "olcwrt: reload:", err)
	}
}

func cmdStatus() error { return printJSON(collectStatus()) }

func cmdImport(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: olcwrt import <olcrtc://...|subscription-url|file|->")
	}
	var text string
	switch {
	case args[0] == "-":
		b, err := io.ReadAll(io.LimitReader(os.Stdin, fetchLimit))
		if err != nil {
			return err
		}
		text = string(b)
	case len(args) == 1 && !isURI(args[0]) && !isHTTPURL(args[0]):
		b, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		text = string(b)
	default:
		text = strings.Join(args, "\n")
	}

	var res *ImportResult
	var subs []*SubResult
	err := withConfigLock(func(cfg *Config) error {
		var err error
		res, err = importText(cfg, text)
		if err != nil || len(res.Subscriptions) == 0 {
			return err
		}
		subs = updateSubsLocked(res.Subscriptions)
		return nil
	})
	if res != nil {
		out := map[string]any{"import": res}
		if len(subs) > 0 {
			out["subscriptions"] = subs
		}
		_ = printJSON(out)
	}
	reloadIfRunning()
	return err
}

// updateSubsLocked refreshes the given subscriptions (all enabled ones when
// ids is empty). The config lock must be held.
func updateSubsLocked(ids []string) []*SubResult {
	cfg, err := loadConfigLenient()
	if err != nil {
		return []*SubResult{{Error: err.Error()}}
	}
	if len(ids) == 0 {
		for _, s := range cfg.Subs {
			if s.Enabled {
				ids = append(ids, s.ID)
			}
		}
	}
	var out []*SubResult
	for _, id := range ids {
		// Reload: the previous refresh may have changed the server sections.
		if cfg, err = loadConfigLenient(); err != nil {
			return append(out, &SubResult{ID: id, Error: err.Error()})
		}
		sub := cfg.SubByID[id]
		if sub == nil {
			out = append(out, &SubResult{ID: id, Error: "no such subscription"})
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		out = append(out, updateSubscription(ctx, cfg, sub))
		cancel()
	}
	return out
}

func cmdSub(args []string) error {
	if len(args) == 0 || args[0] != "update" {
		return errors.New("usage: olcwrt sub update [id...]")
	}
	var res []*SubResult
	err := withConfigLock(func(*Config) error {
		res = updateSubsLocked(args[1:])
		return nil
	})
	if err != nil {
		return err
	}
	reloadIfRunning()
	return printJSON(res)
}

func cmdLists(args []string) error {
	if len(args) == 0 || args[0] != "update" {
		return errors.New("usage: olcwrt lists update")
	}
	var idx map[string]*ListMeta
	err := withConfigLock(func(cfg *Config) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		idx, _ = updateLists(ctx, cfg, false)
		return nil
	})
	if err != nil {
		return err
	}
	reloadIfRunning()
	return printJSON(idx)
}

func cmdCheck(args []string) error {
	cfg, err := loadConfigLenient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if len(args) == 0 {
		return printJSON(checkRunning(ctx, cfg))
	}
	s := cfg.ServerByID[args[0]]
	if s == nil {
		return fmt.Errorf("no such server %q", args[0])
	}
	return printJSON(checkServer(ctx, cfg, s))
}

func cmdGenconf() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	chain, warnings := cfg.ActiveServers()
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	if len(chain) == 0 {
		return errNoServers
	}
	user, pass := cfg.SocksAuth()
	host := "127.0.0.1"
	if cfg.LANSocks {
		host = "0.0.0.0"
	}
	fmt.Print(buildOlcrtcYAML(olcrtcParams{
		DNS: olcrtcDNS(cfg), SocksHost: host, SocksPort: cfg.SocksPort,
		SocksUser: user, SocksPass: pass, Debug: cfg.Debug, Retry: cfg.FailoverRetry,
	}, chain))
	return nil
}

func cmdExport(args []string) error {
	cfg, err := loadConfigLenient()
	if err != nil {
		return err
	}
	for _, s := range cfg.Servers {
		if len(args) > 0 && !contains(args, s.ID) {
			continue
		}
		if s.Validate() == nil {
			fmt.Println(FormatURI(s))
		}
	}
	return nil
}

// FullStatus is what LuCI shows on the overview page.
type FullStatus struct {
	Version       string               `json:"version"`
	Running       bool                 `json:"running"`
	Stale         bool                 `json:"stale,omitempty"`
	OlcrtcPresent bool                 `json:"olcrtc_present"`
	Daemon        *Status              `json:"daemon,omitempty"`
	Subscriptions map[string]*SubMeta  `json:"subscriptions"`
	Lists         map[string]*ListMeta `json:"lists"`
}

func collectStatus() *FullStatus {
	fs := &FullStatus{Version: version, Subscriptions: map[string]*SubMeta{}, Lists: loadListIndex()}
	_, fs.Running = daemonPID()
	if _, err := os.Stat(olcrtcPath); err == nil {
		fs.OlcrtcPresent = true
	}
	var st Status
	if readJSON(statusPath(), &st) == nil {
		fs.Daemon = &st
		fs.Stale = fs.Running && time.Since(time.Unix(st.Updated, 0)) > 30*time.Second
	}
	if cfg, err := loadConfigLenient(); err == nil {
		for _, s := range cfg.Subs {
			fs.Subscriptions[s.ID] = loadSubMeta(s.ID)
		}
	}
	return fs
}

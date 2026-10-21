package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Status is written to /var/run/olcwrt/status.json for LuCI and the CLI.
type Status struct {
	Version    string         `json:"version"`
	PID        int            `json:"pid"`
	Started    int64          `json:"started"`
	Updated    int64          `json:"updated"`
	Generation int            `json:"generation"` // completed reloads
	State      string         `json:"state"`
	Since      int64          `json:"since"`
	Server     string         `json:"server,omitempty"`
	ServerName string         `json:"server_name,omitempty"`
	Chain      []string       `json:"chain"`
	LastError  string         `json:"last_error,omitempty"`
	Restarts   int            `json:"restarts"`
	Mode       string         `json:"mode"`
	DNS        string         `json:"dns_mode"`
	Socks      string         `json:"socks"`
	Devices    []string       `json:"devices,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
	Redir      RedirStats     `json:"redir"`
	Resolver   DNSStats       `json:"resolver"`
	Lists      map[string]int `json:"lists,omitempty"`
}

type daemon struct {
	log     *logger
	started time.Time

	mu        sync.Mutex
	cfg       *Config
	idle      string // state when the tunnel is not running
	cfgErr    string
	chain     []*Server
	warnings  []string
	tunnel    *tunnelSupervisor
	tunnelKey string
	redir     *redirServer
	redirKey  string
	dns       *dnsServer
	tres      *tunnelResolver
	sets      *setUpdater
	devices   []string
	dnsMode   string
	fwActive  bool
	dnsmasqOn bool
	lists     map[string]int

	redirCnt redirCounters
	dnsCnt   dnsCounters

	reloadCh   chan struct{}
	statusCh   chan struct{}
	bgBusy     atomic.Bool
	generation int
}

func cmdRun() error {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	// A restarting predecessor may still be tearing down its firewall and
	// dnsmasq integration; wait for it so the two never interleave.
	unlockRun, err := lockFile(runLockPath())
	if err != nil {
		return err
	}
	defer unlockRun()
	d := &daemon{
		started:  time.Now(),
		idle:     stateStarting,
		reloadCh: make(chan struct{}, 1),
		statusCh: make(chan struct{}, 1),
	}
	d.log = newLogger(logPath, false)
	defer d.log.Close()
	if err := os.WriteFile(pidPath(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		return err
	}
	defer os.Remove(pidPath())

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	d.log.Infof("olcwrt %s starting", version)
	d.reload()

	statusTick := time.NewTicker(2 * time.Second)
	defer statusTick.Stop()
	schedTick := time.NewTicker(time.Minute)
	defer schedTick.Stop()
	for {
		select {
		case s := <-sigs:
			if s == syscall.SIGHUP {
				d.log.Infof("reloading configuration")
				d.reload()
				continue
			}
			d.log.Infof("stopping")
			d.shutdown()
			return nil
		case <-d.reloadCh:
			d.reload()
		case <-d.statusCh:
			d.writeStatus()
		case <-statusTick.C:
			d.writeStatus()
		case <-schedTick.C:
			d.scheduleUpdates()
		}
	}
}

func (d *daemon) requestStatus() {
	select {
	case d.statusCh <- struct{}{}:
	default:
	}
}

func (d *daemon) requestReload() {
	select {
	case d.reloadCh <- struct{}{}:
	default:
	}
}

// reload re-reads the configuration and converges every component to it.
// The tunnel keeps running when its configuration did not change.
func (d *daemon) reload() {
	cfg, err := loadConfig()
	d.mu.Lock()
	defer func() {
		d.generation++
		d.mu.Unlock()
		d.writeStatus()
		d.scheduleUpdates()
	}()
	d.warnings, d.cfgErr = nil, ""
	if err != nil {
		d.log.Infof("configuration error: %v", err)
		d.cfgErr = err.Error()
		d.teardown(stateError)
		return
	}
	d.cfg = cfg
	d.log.setDebug(cfg.Debug)
	if !cfg.Enabled {
		d.teardown(stateDisabled)
		return
	}
	chain, warnings := cfg.ActiveServers()
	d.warnings = append(d.warnings, warnings...)
	if len(chain) == 0 {
		d.warnings = append(d.warnings, errNoServers.Error())
		d.teardown(stateIdle)
		return
	}
	d.chain = chain
	d.applyTunnel(cfg, chain)
	d.applyRouting(cfg)
}

func (d *daemon) applyTunnel(cfg *Config, chain []*Server) {
	user, pass := cfg.SocksAuth()
	params := olcrtcParams{
		DNS: olcrtcDNS(cfg), SocksHost: "127.0.0.1", SocksPort: cfg.SocksPort,
		SocksUser: user, SocksPass: pass, Debug: cfg.Debug, Retry: cfg.FailoverRetry,
	}
	if cfg.LANSocks {
		params.SocksHost = "0.0.0.0"
	}
	yml := buildOlcrtcYAML(params, chain)
	memLimit, ok := normalizeMemLimit(cfg.MemLimit)
	if !ok {
		d.warnings = append(d.warnings, fmt.Sprintf("ignoring invalid memory limit %q", cfg.MemLimit))
	}
	env := olcrtcEnv(memLimit)
	key := yml + "\x00" + strings.Join(env, "\x00")
	if d.tunnel != nil && key == d.tunnelKey {
		return
	}
	if d.tunnel != nil {
		d.tunnel.Stop()
		d.tunnel = nil
	}
	if err := writeFileAtomic(yamlPath(), []byte(yml), 0o600); err != nil {
		d.warnings = append(d.warnings, "cannot write olcrtc config: "+err.Error())
		return
	}
	names := make([]string, 0, len(chain))
	for _, s := range chain {
		names = append(names, s.Name())
	}
	d.log.Infof("starting olcrtc: %s", strings.Join(names, " -> "))
	d.tunnel = startTunnel(olcrtcPath, yamlPath(), env, d.log, chain[0].ID, d.requestStatus)
	d.tunnelKey = key
}

func (d *daemon) applyRouting(cfg *Config) {
	if cfg.Mode == "off" {
		d.teardownRouting()
		return
	}
	user, pass := cfg.SocksAuth()
	dialer := &socksDialer{addr: cfg.SocksAddr(), user: user, pass: pass}

	proxy, w1 := loadRouteList("proxy", cfg.ProxyLists)
	bypass, w2 := loadRouteList("bypass", cfg.BypassLists)
	d.warnings = append(append(d.warnings, w1...), w2...)
	d.lists = map[string]int{
		"proxy_domains": len(proxy.Domains), "proxy_ips": len(proxy.Prefixes),
		"bypass_domains": len(bypass.Domains), "bypass_ips": len(bypass.Prefixes),
	}
	if cfg.Mode == "list" && proxy.Empty() {
		d.warnings = append(d.warnings, "list mode is selected but the proxy list is empty")
	}
	if !cfg.DNSEnabled && len(proxy.Domains)+len(bypass.Domains) > 0 {
		d.warnings = append(d.warnings, "domain lists need the dnsmasq integration (DNS settings), only addresses and subnets are used")
	}

	redirKey := fmt.Sprintf("%d|%s|%s|%s", cfg.RedirPort, dialer.addr, user, pass)
	if d.redir == nil || d.redirKey != redirKey {
		if d.redir != nil {
			d.redir.Close()
			d.redir = nil
		}
		r, err := startRedir(cfg.RedirPort, dialer, &d.redirCnt, d.log.Filef)
		if err != nil {
			d.warnings = append(d.warnings, "transparent proxy: "+err.Error())
			d.teardownRouting()
			return
		}
		d.redir, d.redirKey = r, redirKey
	}

	dnsOK := d.startResolver(cfg, dialer, proxy, bypass)
	fwOK := d.applyFirewall(cfg, proxy, bypass)
	d.applyDnsmasq(cfg, dnsOK, fwOK, proxy, bypass)
}

func (d *daemon) startResolver(cfg *Config, dialer *socksDialer, proxy, bypass *RouteList) bool {
	d.stopResolver()
	if !cfg.DNSEnabled {
		d.dnsMode = "off"
		return false
	}
	var upstreams []string
	for _, u := range cfg.Upstreams {
		if hp, err := normalizeHostPort(u, 53); err == nil {
			upstreams = append(upstreams, hp)
		} else {
			d.warnings = append(d.warnings, fmt.Sprintf("dns upstream %q: %v", u, err))
		}
	}
	d.sets = newSetUpdater(cfg.SetTimeout, &d.dnsCnt.learned, d.log.Filef)
	d.tres = newTunnelResolver(dialer, upstreams)
	router := &dnsRouter{
		proxy:         newDomainMatcher(proxy.Domains),
		bypass:        newDomainMatcher(bypass.Domains),
		defaultTunnel: cfg.Mode == "all" && cfg.DNSTunnel,
		filterAAAA:    cfg.FilterAAAA,
		tunnel:        d.tres,
		direct:        &directResolver{servers: func() []string { return directDNSServers(cfg) }},
		sets:          d.sets,
		counters:      &d.dnsCnt,
	}
	srv, err := startDNS(fmt.Sprintf("127.0.0.1:%d", cfg.DNSPort), router)
	if err != nil {
		d.warnings = append(d.warnings, "dns resolver: "+err.Error())
		d.stopResolver()
		d.dnsMode = "off"
		return false
	}
	d.dns = srv
	d.dnsMode = "lists"
	if router.defaultTunnel {
		d.dnsMode = "tunnel"
	}
	return true
}

func (d *daemon) stopResolver() {
	if d.dns != nil {
		d.dns.Close()
		d.dns = nil
	}
	if d.tres != nil {
		d.tres.Close()
		d.tres = nil
	}
	if d.sets != nil {
		d.sets.Close()
		d.sets = nil
	}
}

func (d *daemon) applyFirewall(cfg *Config, proxy, bypass *RouteList) bool {
	devs, warnings := resolveDevices(cfg.Interfaces)
	d.warnings = append(d.warnings, warnings...)
	d.devices = devs
	if len(devs) == 0 {
		d.warnings = append(d.warnings, "no LAN interface found, traffic is not redirected")
		d.removeFirewall()
		return false
	}
	macs, v4, v6, bad := splitSources(cfg.Sources)
	for _, b := range bad {
		d.warnings = append(d.warnings, fmt.Sprintf("invalid client address %q ignored", b))
	}
	spec := &fwSpec{
		Mode: cfg.Mode, Devices: devs, RedirPort: cfg.RedirPort, IPv6: cfg.IPv6,
		BlockQUIC: cfg.BlockQUIC, DNSHijack: cfg.DNSHijack,
		SourceMode: cfg.SourceMode, SrcMAC: macs, SrcV4: v4, SrcV6: v6,
		Proxy: proxy, Bypass: bypass, SetTimeout: fmt.Sprintf("%ds", int(cfg.SetTimeout.Seconds())),
	}
	if len(macs)+len(v4)+len(v6) == 0 {
		if spec.SourceMode == "include" {
			d.warnings = append(d.warnings, "client filter is \"only listed\" but no clients are listed")
		}
		if spec.SourceMode == "exclude" {
			spec.SourceMode = "all"
		}
	}
	if err := applyNft(buildNftScript(spec)); err != nil {
		d.warnings = append(d.warnings, "firewall: "+err.Error())
		d.log.Infof("firewall: %v", err)
		d.removeFirewall()
		return false
	}
	d.fwActive = true
	if d.sets != nil {
		d.sets.reset()
	}
	return true
}

func (d *daemon) removeFirewall() {
	if err := removeNft(); err != nil {
		d.log.Infof("%v", err)
	}
	d.fwActive = false
}

func (d *daemon) applyDnsmasq(cfg *Config, dnsOK, fwOK bool, proxy, bypass *RouteList) {
	plan := dnsmasqPlan{Port: cfg.DNSPort, All: cfg.Mode == "all" && cfg.DNSTunnel}
	if dnsOK {
		plan.Domains = append(append([]string(nil), proxy.Domains...), bypass.Domains...)
		sort.Strings(plan.Domains)
		plan.Active = plan.All || len(plan.Domains) > 0
	}
	insts := dnsmasqInstances()
	if plan.Active {
		d.warnings = append(d.warnings, dnsmasqWarnings(insts, plan.All)...)
	}
	restart, err := applyDnsmasqPlan(insts, plan)
	if err != nil {
		d.warnings = append(d.warnings, "dnsmasq: "+err.Error())
	}
	d.dnsmasqOn = plan.Active && err == nil
	switch {
	case restart:
		if err := restartDnsmasq(); err != nil {
			d.warnings = append(d.warnings, err.Error())
		}
	case d.dnsmasqOn || fwOK:
		// SIGHUP re-reads the servers file and drops cached answers whose
		// addresses are gone from the recreated nft sets.
		flushDnsmasqCache()
	}
}

func (d *daemon) teardownRouting() {
	if removeDnsmasqConf() {
		if err := restartDnsmasq(); err != nil {
			d.log.Infof("%v", err)
		}
	}
	d.dnsmasqOn = false
	d.removeFirewall()
	d.stopResolver()
	if d.redir != nil {
		d.redir.Close()
		d.redir = nil
	}
	d.devices, d.lists, d.dnsMode = nil, nil, ""
}

// teardown stops everything and records why the tunnel is not running.
func (d *daemon) teardown(idle string) {
	d.teardownRouting()
	if d.tunnel != nil {
		d.tunnel.Stop()
		d.tunnel = nil
		d.tunnelKey = ""
	}
	d.chain = nil
	d.idle = idle
}

func (d *daemon) shutdown() {
	d.mu.Lock()
	d.teardown(stateStopped)
	d.mu.Unlock()
	d.writeStatus()
}

func (d *daemon) writeStatus() {
	d.mu.Lock()
	st := Status{
		Version: version, PID: os.Getpid(), Started: d.started.Unix(), Updated: time.Now().Unix(),
		Generation: d.generation,
		State:      d.idle, Since: d.started.Unix(), LastError: d.cfgErr, Chain: []string{},
		DNS: d.dnsMode, Devices: d.devices, Warnings: d.warnings, Lists: d.lists,
		Redir: d.redirCnt.snapshot(), Resolver: d.dnsCnt.snapshot(),
	}
	if d.cfg != nil {
		st.Mode = d.cfg.Mode
		st.Socks = d.cfg.SocksAddr()
	}
	for _, s := range d.chain {
		st.Chain = append(st.Chain, s.ID)
	}
	if d.tunnel != nil {
		ts := d.tunnel.snapshot()
		st.State, st.Since, st.Restarts = ts.State, ts.Since.Unix(), ts.Restarts
		st.Server, st.LastError = ts.Profile, ts.LastError
		if d.cfg != nil && d.cfg.ServerByID[ts.Profile] != nil {
			st.ServerName = d.cfg.ServerByID[ts.Profile].Name()
		}
	}
	d.mu.Unlock()
	_ = writeJSON(statusPath(), st, 0o644)
}

// scheduleUpdates refreshes due subscriptions and lists in the background
// and reloads when anything changed.
func (d *daemon) scheduleUpdates() {
	if !d.bgBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer d.bgBusy.Store(false)
		if d.runDueUpdates() {
			d.requestReload()
		}
	}()
}

func (d *daemon) runDueUpdates() bool {
	unlock, err := lockFile(lockPath())
	if err != nil {
		return false
	}
	defer unlock()
	cfg, err := loadConfig()
	if err != nil || !cfg.Enabled {
		return false
	}
	changed := false
	if n, err := pruneOrphans(cfg); err != nil {
		d.log.Infof("removing servers of deleted subscriptions: %v", err)
	} else if n > 0 {
		d.log.Infof("removed %d servers of deleted subscriptions", n)
		changed = true
		if fresh, err := loadConfig(); err == nil {
			cfg = fresh
		}
	}
	now := time.Now()
	for _, sub := range cfg.Subs {
		if !subDue(sub, loadSubMeta(sub.ID), now) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		res := updateSubscription(ctx, cfg, sub)
		cancel()
		if !res.OK {
			d.log.Infof("subscription %s: %s", sub.ID, res.Error)
			continue
		}
		d.log.Infof("subscription %s updated: %d servers (+%d ~%d -%d)", sub.ID, res.Total, res.Added, res.Updated, res.Removed)
		if res.Added+res.Updated+res.Removed > 0 {
			changed = true
			if fresh, err := loadConfig(); err == nil {
				cfg = fresh
			}
		}
	}
	if listsDue(cfg, now) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		idx, listChanged := updateLists(ctx, cfg, true)
		cancel()
		for _, m := range idx {
			if m.LastError != "" && m.LastAttempt == now.Unix() {
				d.log.Infof("list %s: %s", m.URL, m.LastError)
			}
		}
		changed = changed || listChanged
	}
	return changed
}

// olcrtcDNS is the resolver olcrtc uses to reach the provider. It must not
// depend on the tunnel, so it never points at the local dnsmasq.
func olcrtcDNS(cfg *Config) string {
	if cfg.OlcDNS != "auto" {
		if hp, err := normalizeHostPort(cfg.OlcDNS, 53); err == nil {
			return hp
		}
	}
	return directDNSServers(cfg)[0]
}

func olcrtcEnv(memLimit string) []string {
	env := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/tmp"}
	if memLimit != "" {
		env = append(env, "GOMEMLIMIT="+memLimit)
	}
	return env
}

var memLimitRe = regexp.MustCompile(`^(\d+)\s*([kmg]?)(i?b?)$`)

// normalizeMemLimit converts values like "96", "96M" or "96MB" into the
// GOMEMLIMIT syntax ("96MiB"); a bare number means MiB.
func normalizeMemLimit(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "", true
	}
	m := memLimitRe.FindStringSubmatch(v)
	if m == nil {
		return "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return "", false
	}
	unit := map[string]string{"": "MiB", "k": "KiB", "m": "MiB", "g": "GiB"}[m[2]]
	if m[2] == "" && m[3] == "b" {
		unit = "B"
	}
	return fmt.Sprintf("%d%s", n, unit), true
}

// daemonPID returns the PID of a running daemon.
func daemonPID() (int, bool) {
	b, err := os.ReadFile(pidPath())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	// The CLI shares the binary name, so match the "run" argument too.
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return 0, false
	}
	args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
	if len(args) < 2 || !strings.HasSuffix(args[0], "olcwrt") || args[1] != "run" {
		return 0, false
	}
	return pid, true
}

var errNotRunning = errors.New("olcwrt daemon is not running")

func signalDaemonReload() error {
	pid, ok := daemonPID()
	if !ok {
		return errNotRunning
	}
	return signalPID(pid, syscall.SIGHUP)
}

// cmdReload asks the daemon to reload and waits until it has applied the
// configuration, so scripts can rely on the new state afterwards.
func cmdReload() error {
	var before Status
	_ = readJSON(statusPath(), &before)
	if err := signalDaemonReload(); err != nil {
		return err
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		var st Status
		if readJSON(statusPath(), &st) == nil && (st.PID != before.PID || st.Generation > before.Generation) {
			return nil
		}
		if _, ok := daemonPID(); !ok {
			return errNotRunning
		}
	}
	return errors.New("timed out waiting for the daemon to reload")
}

// cmdCleanup removes leftovers of a daemon that died without tearing down.
// A live daemon cleans up after itself, so nothing is done while it runs.
func cmdCleanup() error {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	unlock, free, err := tryLockFile(runLockPath())
	if err != nil {
		return err
	}
	if !free {
		return nil
	}
	defer unlock()
	if removeDnsmasqConf() {
		if err := restartDnsmasq(); err != nil {
			return err
		}
	}
	return removeNft()
}

package main

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Supported olcrtc providers and transports.
var (
	knownProviders  = []string{"jitsi", "telemost", "wbstream"}
	knownTransports = []string{"datachannel", "vp8channel", "seichannel", "videochannel"}
)

const (
	optInt = iota
	optStr
)

// transportOpt maps one transport tuning knob between the olcrtc:// payload
// key, the UCI option name and the olcrtc YAML field.
type transportOpt struct {
	uri       string
	uci       string
	section   string
	field     string
	kind      int
	transport string
}

var transportOpts = []transportOpt{
	{"vp8-fps", "vp8_fps", "vp8", "fps", optInt, "vp8channel"},
	{"vp8-batch", "vp8_batch", "vp8", "batch_size", optInt, "vp8channel"},
	{"fps", "sei_fps", "sei", "fps", optInt, "seichannel"},
	{"batch", "sei_batch", "sei", "batch_size", optInt, "seichannel"},
	{"frag", "sei_frag", "sei", "fragment_size", optInt, "seichannel"},
	{"ack-ms", "sei_ack_ms", "sei", "ack_timeout_ms", optInt, "seichannel"},
	{"video-w", "video_w", "video", "width", optInt, "videochannel"},
	{"video-h", "video_h", "video", "height", optInt, "videochannel"},
	{"video-fps", "video_fps", "video", "fps", optInt, "videochannel"},
	{"video-codec", "video_codec", "video", "codec", optStr, "videochannel"},
	{"video-qr-size", "video_qr_size", "video", "qr_size", optInt, "videochannel"},
	{"video-qr-recovery", "video_qr_recovery", "video", "qr_recovery", optStr, "videochannel"},
	{"video-tile-module", "video_tile_module", "video", "tile_module", optInt, "videochannel"},
	{"video-tile-rs", "video_tile_rs", "video", "tile_rs", optInt, "videochannel"},
}

func transportOptsFor(transport string) []transportOpt {
	var out []transportOpt
	for _, o := range transportOpts {
		if o.transport == transport {
			out = append(out, o)
		}
	}
	return out
}

// Server is one olcrtc endpoint (a room on a provider plus the shared key).
type Server struct {
	ID           string            `json:"id"`
	Label        string            `json:"label"`
	Provider     string            `json:"provider"`
	Transport    string            `json:"transport"`
	Room         string            `json:"room"`
	Key          string            `json:"-"`
	Token        string            `json:"-"`
	Channel      string            `json:"channel,omitempty"`
	Subscription string            `json:"subscription,omitempty"`
	Comment      string            `json:"comment,omitempty"`
	Opts         map[string]string `json:"opts,omitempty"` // keyed by UCI option name
}

// Name returns a human readable label.
func (s *Server) Name() string {
	if s.Label != "" {
		return s.Label
	}
	return s.Room
}

// Signature identifies the same endpoint across imports and subscription
// refreshes, matching the olcbox client.
func (s *Server) Signature() string {
	return strings.Join([]string{s.Provider, s.Transport, s.Room, s.Key}, "|")
}

// serverID derives a stable UCI section name from the signature.
func serverID(s *Server) string {
	sum := sha1.Sum([]byte(s.Signature()))
	return "s_" + hex.EncodeToString(sum[:5])
}

func subscriptionID(url string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(url)))
	return "sub_" + hex.EncodeToString(sum[:4])
}

var (
	errNoProvider  = errors.New("unsupported provider")
	errNoTransport = errors.New("unsupported transport")
	errNoRoom      = errors.New("room is empty")
	errBadKey      = errors.New("key must be 64 hex characters")
)

// Validate checks that olcrtc would accept the server.
func (s *Server) Validate() error {
	if !contains(knownProviders, s.Provider) {
		return fmt.Errorf("%w: %q", errNoProvider, s.Provider)
	}
	if !contains(knownTransports, s.Transport) {
		return fmt.Errorf("%w: %q", errNoTransport, s.Transport)
	}
	if strings.TrimSpace(s.Room) == "" {
		return errNoRoom
	}
	if b, err := hex.DecodeString(s.Key); err != nil || len(b) != 32 {
		return errBadKey
	}
	return nil
}

func normalizeProvider(v string) string {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case "yandex", "yandex_telemost", "yandex-telemost":
		return "telemost"
	case "wb-stream", "wb_stream", "wildberries", "wb":
		return "wbstream"
	case "jitsi-meet", "jitsi_meet", "meet":
		return "jitsi"
	}
	return v
}

func normalizeTransport(v string) string {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case "dc", "data":
		return "datachannel"
	case "vp8", "video_vp8", "video-vp8":
		return "vp8channel"
	case "sei", "sei_channel", "sei-channel", "h264_sei":
		return "seichannel"
	case "video", "qr":
		return "videochannel"
	}
	return v
}

// Subscription is a remote list of servers in the olcrtc subscription format.
type Subscription struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	URL         string `json:"url"`
	Enabled     bool   `json:"enabled"`
	Interval    string `json:"interval"`
	ViaTunnel   bool   `json:"via_tunnel"`
	RequestMode string `json:"request_mode"`
}

// Config is the typed view of /etc/config/olcwrt.
type Config struct {
	Enabled       bool
	Debug         bool
	Selected      []string
	OlcDNS        string
	SocksPort     int
	RedirPort     int
	DNSPort       int
	LANSocks      bool
	SocksUser     string
	SocksPass     string
	FailoverRetry string
	MemLimit      string
	CheckURL      string

	Mode        string // all, list, off
	Interfaces  []string
	IPv6        bool
	BlockQUIC   bool
	DNSHijack   bool
	SourceMode  string // all, include, exclude
	Sources     []string
	ProxyLists  []string
	BypassLists []string

	DNSEnabled bool
	DNSTunnel  bool
	Upstreams  []string
	Direct     []string
	FilterAAAA bool
	SetTimeout time.Duration

	Servers    []*Server
	ServerByID map[string]*Server
	Subs       []*Subscription
	SubByID    map[string]*Subscription
}

const (
	defaultSocksPort = 8808
	defaultRedirPort = 18809
	defaultDNSPort   = 18853
	defaultCheckURL  = "http://cp.cloudflare.com/generate_204"
)

func loadConfig() (*Config, error) {
	pkg, err := loadUCI(uciPkg)
	if err != nil {
		return nil, err
	}
	return configFromUCI(pkg)
}

func configFromUCI(pkg *uciPackage) (*Config, error) {
	m, r, d := pkg.Section("main"), pkg.Section("routing"), pkg.Section("dns")
	cfg := &Config{
		Enabled:       m.Bool("enabled", false),
		Debug:         m.Get("log_level") == "debug",
		Selected:      m.List("server"),
		OlcDNS:        m.GetDef("dns_server", "auto"),
		SocksPort:     m.Int("socks_port", defaultSocksPort),
		RedirPort:     m.Int("redir_port", defaultRedirPort),
		DNSPort:       m.Int("dns_port", defaultDNSPort),
		LANSocks:      m.Bool("lan_socks", false),
		SocksUser:     m.Get("socks_user"),
		SocksPass:     m.Get("socks_pass"),
		FailoverRetry: m.GetDef("failover_retry", "2s"),
		MemLimit:      m.Get("mem_limit"),
		CheckURL:      m.GetDef("check_url", defaultCheckURL),

		Mode:        r.GetDef("mode", "all"),
		Interfaces:  r.List("interface"),
		IPv6:        r.Bool("ipv6", true),
		BlockQUIC:   r.Bool("block_quic", true),
		DNSHijack:   r.Bool("dns_hijack", true),
		SourceMode:  r.GetDef("source_mode", "all"),
		Sources:     r.List("source"),
		ProxyLists:  r.List("proxy_list_url"),
		BypassLists: r.List("bypass_list_url"),

		DNSEnabled: d.Bool("enabled", true),
		DNSTunnel:  d.Bool("tunnel", true),
		Upstreams:  d.List("upstream"),
		Direct:     d.List("direct"),
		FilterAAAA: d.Bool("filter_aaaa", false),

		ServerByID: map[string]*Server{},
		SubByID:    map[string]*Subscription{},
	}
	if len(cfg.Interfaces) == 0 {
		cfg.Interfaces = []string{"lan"}
	}
	if len(cfg.Upstreams) == 0 {
		cfg.Upstreams = []string{"1.1.1.1:53", "8.8.8.8:53"}
	}
	cfg.SetTimeout = parseDurationDef(d.Get("set_timeout"), 12*time.Hour)

	for _, s := range pkg.OfType("server") {
		srv := serverFromSection(s)
		cfg.Servers = append(cfg.Servers, srv)
		cfg.ServerByID[srv.ID] = srv
	}
	for _, s := range pkg.OfType("subscription") {
		sub := &Subscription{
			ID:          s.Name,
			Label:       s.Get("label"),
			URL:         strings.TrimSpace(s.Get("url")),
			Enabled:     s.Bool("enabled", true),
			Interval:    s.Get("interval"),
			ViaTunnel:   s.Bool("via_tunnel", false),
			RequestMode: s.GetDef("request_mode", "identity"),
		}
		cfg.Subs = append(cfg.Subs, sub)
		cfg.SubByID[sub.ID] = sub
	}
	return cfg, cfg.validate()
}

func serverFromSection(s *uciSection) *Server {
	srv := &Server{
		ID:           s.Name,
		Label:        s.Get("label"),
		Provider:     normalizeProvider(s.Get("provider")),
		Transport:    normalizeTransport(s.Get("transport")),
		Room:         strings.TrimSpace(s.Get("room")),
		Key:          strings.ToLower(strings.TrimSpace(s.Get("key"))),
		Token:        s.Get("token"),
		Channel:      s.Get("channel"),
		Subscription: s.Get("subscription"),
		Comment:      s.Get("comment"),
		Opts:         map[string]string{},
	}
	for _, o := range transportOpts {
		if v := strings.TrimSpace(s.Get(o.uci)); v != "" {
			srv.Opts[o.uci] = v
		}
	}
	return srv
}

func (c *Config) validate() error {
	for _, p := range []struct {
		name string
		port int
	}{{"socks_port", c.SocksPort}, {"redir_port", c.RedirPort}, {"dns_port", c.DNSPort}} {
		if p.port < 1 || p.port > 65535 {
			return fmt.Errorf("main.%s: invalid port %d", p.name, p.port)
		}
	}
	if c.SocksPort == c.RedirPort || c.SocksPort == c.DNSPort || c.RedirPort == c.DNSPort {
		return errors.New("socks_port, redir_port and dns_port must differ")
	}
	if c.LANSocks && (c.SocksUser == "" || c.SocksPass == "") {
		return errors.New("LAN SOCKS5 access requires socks_user and socks_pass")
	}
	if !contains([]string{"all", "list", "off"}, c.Mode) {
		return fmt.Errorf("routing.mode: unknown mode %q", c.Mode)
	}
	if !contains([]string{"all", "include", "exclude"}, c.SourceMode) {
		return fmt.Errorf("routing.source_mode: unknown mode %q", c.SourceMode)
	}
	if c.OlcDNS != "auto" {
		hp, err := normalizeHostPort(c.OlcDNS, 53)
		if err != nil {
			return fmt.Errorf("main.dns_server: %w", err)
		}
		// The router's own resolver may forward through the tunnel itself.
		host, _, _ := net.SplitHostPort(hp)
		if ip := net.ParseIP(host); host == "localhost" || ip != nil && ip.IsLoopback() {
			return errors.New("main.dns_server must not point at the router itself")
		}
	}
	return nil
}

// ActiveServers returns the valid servers of the failover chain. When nothing
// is selected the first valid server is used.
func (c *Config) ActiveServers() ([]*Server, []string) {
	var out []*Server
	var warnings []string
	for _, id := range c.Selected {
		s := c.ServerByID[id]
		if s == nil {
			warnings = append(warnings, fmt.Sprintf("selected server %q does not exist", id))
			continue
		}
		if err := s.Validate(); err != nil {
			warnings = append(warnings, fmt.Sprintf("server %q skipped: %v", s.Name(), err))
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 && len(c.Selected) == 0 {
		for _, s := range c.Servers {
			if s.Validate() == nil {
				out = append(out, s)
				break
			}
		}
	}
	return out, warnings
}

// SocksAddr is where the olcrtc SOCKS5 listener is reached locally.
func (c *Config) SocksAddr() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(c.SocksPort))
}

// SocksAuth returns the credentials local clients must present, if any.
func (c *Config) SocksAuth() (string, string) {
	if c.SocksUser != "" && c.SocksPass != "" {
		return c.SocksUser, c.SocksPass
	}
	return "", ""
}

// normalizeHostPort accepts "host", "host:port", "host#port" (dnsmasq style)
// and bracketed IPv6, and returns host:port.
func normalizeHostPort(v string, defPort int) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.New("empty address")
	}
	if i := strings.LastIndexByte(v, '#'); i > 0 {
		v = net.JoinHostPort(strings.Trim(v[:i], "[]"), v[i+1:])
	}
	if ip := net.ParseIP(strings.Trim(v, "[]")); ip != nil {
		return net.JoinHostPort(ip.String(), strconv.Itoa(defPort)), nil
	}
	host, port, err := net.SplitHostPort(v)
	if err != nil {
		if !strings.Contains(v, ":") {
			return net.JoinHostPort(v, strconv.Itoa(defPort)), nil
		}
		return "", fmt.Errorf("invalid address %q", v)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || host == "" {
		return "", fmt.Errorf("invalid address %q", v)
	}
	return net.JoinHostPort(host, port), nil
}

// parseDurationDef accepts Go durations plus a "d" (days) suffix.
func parseDurationDef(v string, def time.Duration) time.Duration {
	if d, ok := parseDuration(v); ok {
		return d
	}
	return def
}

func parseDuration(v string) (time.Duration, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return 0, false
	}
	if strings.HasSuffix(v, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(v, "d"))
		if err != nil || n < 0 {
			return 0, false
		}
		return time.Duration(n) * 24 * time.Hour, true
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second, true
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, false
	}
	return d, true
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

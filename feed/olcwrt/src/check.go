package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// CheckResult is the outcome of a connectivity test.
type CheckResult struct {
	OK      bool   `json:"ok"`
	Server  string `json:"server,omitempty"`
	Latency int64  `json:"latency_ms,omitempty"`
	Connect int64  `json:"connect_ms,omitempty"`
	Status  int    `json:"status,omitempty"`
	IP      string `json:"ip,omitempty"`
	Error   string `json:"error,omitempty"`
}

const ipEchoURL = "http://api.ipify.org/"

// checkThrough requests checkURL through the SOCKS5 proxy and measures the
// time to the response headers. It also asks for the exit IP address.
func checkThrough(ctx context.Context, d *socksDialer, checkURL string) *CheckResult {
	res := &CheckResult{}
	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			Proxy:             nil,
			DialContext:       d.DialContext,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("User-Agent", "olcwrt/"+version)
	resp, err := client.Do(req)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	resp.Body.Close()
	res.Latency = time.Since(start).Milliseconds()
	res.Status = resp.StatusCode
	res.OK = resp.StatusCode < 500

	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, ipEchoURL, nil); err == nil {
		if resp, err := client.Do(req); err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
			resp.Body.Close()
			if ip := net.ParseIP(strings.TrimSpace(string(body))); ip != nil {
				res.IP = ip.String()
			}
		}
	}
	return res
}

// checkRunning tests the tunnel of the running daemon.
func checkRunning(ctx context.Context, cfg *Config) *CheckResult {
	user, pass := cfg.SocksAuth()
	return checkThrough(ctx, &socksDialer{addr: cfg.SocksAddr(), user: user, pass: pass}, cfg.CheckURL)
}

const serverCheckTimeout = 60 * time.Second

// checkServer starts a temporary olcrtc instance for one server on a free
// loopback port, waits for its session and tests it.
func checkServer(ctx context.Context, cfg *Config, s *Server) *CheckResult {
	res := &CheckResult{Server: s.ID}
	if err := s.Validate(); err != nil {
		res.Error = err.Error()
		return res
	}
	port, err := freePort()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	yml := buildOlcrtcYAML(olcrtcParams{
		DNS: olcrtcDNS(cfg), SocksHost: "127.0.0.1", SocksPort: port, Retry: "2s",
	}, []*Server{s})
	path := filepath.Join(runDir, fmt.Sprintf("check-%d.yaml", port))
	if err := writeFileAtomic(path, []byte(yml), 0o600); err != nil {
		res.Error = err.Error()
		return res
	}
	defer os.Remove(path)

	ctx, cancel := context.WithTimeout(ctx, serverCheckTimeout)
	defer cancel()
	start := time.Now()
	stop, ready, err := startCheckInstance(ctx, path)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer stop()
	select {
	case err := <-ready:
		if err != nil {
			res.Error = err.Error()
			return res
		}
	case <-ctx.Done():
		res.Error = "no session within " + serverCheckTimeout.String()
		return res
	}
	res.Connect = time.Since(start).Milliseconds()
	r := checkThrough(ctx, &socksDialer{addr: fmt.Sprintf("127.0.0.1:%d", port)}, cfg.CheckURL)
	r.Server, r.Connect = s.ID, res.Connect
	return r
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// startCheckInstance runs olcrtc and signals ready once a session opened, or
// the last error line if the process exits first.
func startCheckInstance(ctx context.Context, config string) (func(), <-chan error, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(olcrtcPath, config)
	cmd.Env = olcrtcEnv("")
	cmd.Stdout, cmd.Stderr = w, w
	cmd.SysProcAttr = childSysProcAttr()
	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		return nil, nil, err
	}
	w.Close()

	ready := make(chan error, 1)
	go func() {
		defer r.Close()
		lastErr := ""
		signalled, session, listening := false, false, false
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := strings.TrimSpace(logTimestamp.ReplaceAllString(sc.Text(), ""))
			ev, ok := parseTunnelEvent(line)
			if ok && ev.err != "" {
				lastErr = ev.err
			}
			session = session || ok && ev.state == stateConnected
			// olcrtc may report the session before its SOCKS5 listener is up.
			listening = listening || strings.HasPrefix(line, "SOCKS5 server listening")
			if session && listening && !signalled {
				signalled = true
				ready <- nil
			}
		}
		if !signalled {
			if lastErr == "" {
				lastErr = "olcrtc exited"
			}
			ready <- errors.New(lastErr)
		}
	}()

	stop := func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	go func() {
		<-ctx.Done()
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}()
	return stop, ready, nil
}

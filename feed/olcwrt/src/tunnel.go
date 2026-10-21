package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Tunnel states reported to LuCI.
const (
	stateStarting     = "starting"
	stateConnecting   = "connecting"
	stateWaitingPeer  = "waiting_peer"
	stateConnected    = "connected"
	stateReconnecting = "reconnecting"
	stateError        = "error"
	stateStopped      = "stopped"
	stateIdle         = "idle"
	stateDisabled     = "disabled"
)

type tunnelState struct {
	State     string
	Since     time.Time
	Profile   string
	LastError string
	Restarts  int
	PID       int
}

// tunnelSupervisor runs olcrtc, restarts it with backoff when it exits and
// derives the tunnel state from its log output.
type tunnelSupervisor struct {
	binary   string
	config   string
	env      []string
	log      *logger
	onChange func()

	mu     sync.Mutex
	st     tunnelState
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	tail   []string
	runErr string
}

func startTunnel(binary, config string, env []string, log *logger, firstProfile string, onChange func()) *tunnelSupervisor {
	ctx, cancel := context.WithCancel(context.Background())
	t := &tunnelSupervisor{
		binary: binary, config: config, env: env, log: log, onChange: onChange,
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
		st: tunnelState{State: stateStarting, Since: time.Now(), Profile: firstProfile},
	}
	go t.loop()
	return t
}

func (t *tunnelSupervisor) snapshot() tunnelState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st
}

func (t *tunnelSupervisor) update(fn func(st *tunnelState)) {
	t.mu.Lock()
	prev := t.st
	fn(&t.st)
	if t.st.State != prev.State {
		t.st.Since = time.Now()
	}
	changed := t.st != prev
	cur := t.st
	t.mu.Unlock()
	if !changed {
		return
	}
	if cur.State != prev.State || cur.Profile != prev.Profile {
		t.log.Infof("tunnel %s%s", cur.State, profileSuffix(cur.Profile))
	}
	if t.onChange != nil {
		t.onChange()
	}
}

func profileSuffix(p string) string {
	if p == "" {
		return ""
	}
	return " (server " + p + ")"
}

func (t *tunnelSupervisor) setState(state string) {
	t.update(func(st *tunnelState) { st.State = state })
}

// Stop terminates olcrtc and waits for the supervisor to finish.
func (t *tunnelSupervisor) Stop() {
	t.cancel()
	<-t.done
}

func (t *tunnelSupervisor) loop() {
	defer close(t.done)
	backoff := time.Second
	for t.ctx.Err() == nil {
		started := time.Now()
		err := t.runOnce()
		if t.ctx.Err() != nil {
			break
		}
		msg := "olcrtc exited"
		if err != nil {
			msg += ": " + err.Error()
		}
		t.mu.Lock()
		tail := append([]string(nil), t.tail...)
		if t.runErr != "" {
			msg = t.runErr
		}
		t.mu.Unlock()
		for _, line := range tail {
			t.log.Infof("olcrtc: %s", line)
		}
		t.update(func(st *tunnelState) {
			st.State = stateError
			st.PID = 0
			st.Restarts++
			st.LastError = msg
		})
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		if !sleepCtx(t.ctx, backoff) {
			break
		}
		backoff = min(backoff*2, 30*time.Second)
	}
	t.update(func(st *tunnelState) { st.State = stateStopped; st.PID = 0 })
}

func (t *tunnelSupervisor) runOnce() error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd := exec.Command(t.binary, t.config)
	cmd.Env = t.env
	cmd.Stdout = w
	cmd.Stderr = w
	cmd.SysProcAttr = childSysProcAttr()
	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		t.update(func(st *tunnelState) { st.LastError = err.Error() })
		return err
	}
	w.Close()
	t.mu.Lock()
	t.tail, t.runErr = nil, ""
	t.mu.Unlock()
	t.update(func(st *tunnelState) {
		st.State = stateConnecting
		st.PID = cmd.Process.Pid
	})

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		t.readOutput(r)
	}()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case err = <-waitDone:
	case <-t.ctx.Done():
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err = <-waitDone:
		case <-time.After(6 * time.Second):
			_ = cmd.Process.Kill()
			err = <-waitDone
		}
	}
	<-readDone
	r.Close()
	return err
}

func (t *tunnelSupervisor) readOutput(r *os.File) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		t.handleLine(sc.Text())
	}
}

var logTimestamp = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(\.\d+)? `)

// handleLine filters one olcrtc log line and updates the state machine.
func (t *tunnelSupervisor) handleLine(line string) {
	line = strings.TrimSpace(logTimestamp.ReplaceAllString(line, ""))
	if line == "" {
		return
	}
	if isPerConnectionLine(line) {
		// One line per proxied connection: keep it out of the log unless
		// debugging, it would flood the router's RAM and leak history.
		t.log.Debugf("olcrtc: %s", line)
		return
	}
	t.log.write("olcrtc", line)
	ev, ok := parseTunnelEvent(line)
	t.mu.Lock()
	t.tail = append(t.tail, line)
	if len(t.tail) > 5 {
		t.tail = t.tail[len(t.tail)-5:]
	}
	if ok && ev.err != "" {
		t.runErr = ev.err
	}
	t.mu.Unlock()

	if ok {
		t.update(func(st *tunnelState) {
			if ev.state != "" {
				st.State = ev.state
			}
			if ev.profile != "" {
				st.Profile = ev.profile
			}
			if ev.err != "" {
				st.LastError = ev.err
			}
			if ev.state == stateConnected {
				st.LastError = ""
			}
		})
	}
}

func isPerConnectionLine(line string) bool {
	return strings.HasPrefix(line, "sid=") || strings.HasPrefix(line, "traffic: session=")
}

type tunnelEvent struct {
	state   string
	profile string
	err     string
}

var profileRe = regexp.MustCompile(`profile=(\S+)`)

// parseTunnelEvent maps known olcrtc log messages to state changes.
func parseTunnelEvent(line string) (tunnelEvent, bool) {
	switch {
	case strings.HasPrefix(line, "session ") && (strings.Contains(line, " opened (device=") || strings.Contains(line, " reopened (device=")):
		return tunnelEvent{state: stateConnected}, true
	case strings.HasPrefix(line, "failover cycle=") && strings.Contains(line, "starting profile="):
		ev := tunnelEvent{state: stateConnecting}
		if m := profileRe.FindStringSubmatch(line); m != nil {
			ev.profile = m[1]
		}
		return ev, true
	case strings.HasPrefix(line, "failover cycle=") && strings.Contains(line, " ended"):
		ev := tunnelEvent{state: stateReconnecting}
		if _, after, ok := strings.Cut(line, "ended with error: "); ok {
			ev.err = after
		}
		return ev, true
	case strings.HasPrefix(line, "client reconnect"),
		strings.HasPrefix(line, "Client link reported conference end"),
		strings.HasPrefix(line, "control unhealthy"):
		return tunnelEvent{state: stateReconnecting}, true
	case strings.Contains(line, "waiting for peer"):
		return tunnelEvent{state: stateWaitingPeer}, true
	case isErrorLine(line):
		return tunnelEvent{err: line}, true
	}
	return tunnelEvent{}, false
}

var errorWords = []string{"error", "failed", "invalid", "required", "unsupported", "denied", "refused", "unreachable", "exhausted"}

func isErrorLine(line string) bool {
	l := strings.ToLower(line)
	for _, w := range errorWords {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

var errNoServers = errors.New("no valid server selected")

package main

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type setKey struct {
	set  string
	addr netip.Addr
}

type setOp struct {
	key     setKey
	refresh bool
	timeout time.Duration
}

// setUpdater adds addresses learned from DNS answers to the dynamic nft sets.
// Additions are batched into one `nft -f -` call; callers wait for the batch
// so the client's first connection is already routed correctly.
type setUpdater struct {
	timeout time.Duration
	learned *atomic.Int64
	logf    func(string, ...any)
	apply   func(script string) error

	mu     sync.Mutex
	known  map[setKey]time.Time
	queued map[setKey]struct{}
	queue  []setOp
	waitCh chan struct{}
	kick   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	warned bool
}

func newSetUpdater(timeout time.Duration, learned *atomic.Int64, logf func(string, ...any)) *setUpdater {
	ctx, cancel := context.WithCancel(context.Background())
	u := &setUpdater{
		timeout: timeout, learned: learned, logf: logf, apply: nftApplyStdin,
		known: map[setKey]time.Time{}, queued: map[setKey]struct{}{},
		kick: make(chan struct{}, 1), ctx: ctx, cancel: cancel,
	}
	go u.run()
	return u
}

func dynSetName(proxy bool, a netip.Addr) string {
	kind := "bypass"
	if proxy {
		kind = "proxy"
	}
	if a.Is4() {
		return kind + "4_dns"
	}
	return kind + "6_dns"
}

func (u *setUpdater) add(ctx context.Context, proxy bool, answers []dnsAnswer) {
	now := time.Now()
	var wait chan struct{}
	u.mu.Lock()
	pending := false
	for _, a := range answers {
		addr := a.Addr.Unmap()
		k := setKey{dynSetName(proxy, addr), addr}
		if _, queued := u.queued[k]; queued {
			pending = true // another query is adding it right now
			continue
		}
		t := max(u.timeout, time.Duration(a.TTL)*time.Second)
		exp, ok := u.known[k]
		if ok && exp.Sub(now) > t/2 {
			continue
		}
		u.queue = append(u.queue, setOp{key: k, refresh: ok && exp.After(now), timeout: t})
		u.queued[k] = struct{}{}
		u.known[k] = now.Add(t)
	}
	if len(u.queue) > 0 || pending {
		if u.waitCh == nil {
			u.waitCh = make(chan struct{})
		}
		wait = u.waitCh
		select {
		case u.kick <- struct{}{}:
		default:
		}
	}
	u.mu.Unlock()
	if wait == nil {
		return
	}
	t := time.NewTimer(2 * time.Second)
	defer t.Stop()
	select {
	case <-wait:
	case <-ctx.Done():
	case <-t.C:
	}
}

func (u *setUpdater) run() {
	for {
		select {
		case <-u.ctx.Done():
			return
		case <-u.kick:
		}
		time.Sleep(10 * time.Millisecond) // let concurrent answers join the batch
		u.mu.Lock()
		ops, wait := u.queue, u.waitCh
		u.queue, u.waitCh = nil, nil
		u.mu.Unlock()
		u.flush(ops)
		u.mu.Lock()
		for _, op := range ops {
			delete(u.queued, op.key)
		}
		u.mu.Unlock()
		if wait != nil {
			close(wait)
		}
	}
}

func (u *setUpdater) flush(ops []setOp) {
	if len(ops) == 0 {
		return
	}
	if err := u.apply(buildSetScript(ops, true)); err == nil {
		u.learned.Add(int64(len(ops)))
		return
	}
	// A refresh may fail if the kernel already expired the element; retry
	// each element as a plain addition.
	added := 0
	var lastErr error
	for _, op := range ops {
		if err := u.apply(buildSetScript([]setOp{op}, false)); err != nil {
			lastErr = err
			continue
		}
		added++
	}
	u.learned.Add(int64(added))
	if lastErr != nil && !u.warned {
		u.warned = true
		u.logf("nft set update failed: %v", lastErr)
	}
}

func buildSetScript(ops []setOp, withRefresh bool) string {
	var b strings.Builder
	for _, op := range ops {
		elem := op.key.addr.String()
		if withRefresh && op.refresh {
			fmt.Fprintf(&b, "delete element inet %s %s { %s }\n", nftTable, op.key.set, elem)
		}
		fmt.Fprintf(&b, "add element inet %s %s { %s timeout %ds }\n", nftTable, op.key.set, elem, int(op.timeout.Seconds()))
	}
	return b.String()
}

// reset forgets learned addresses; used after the nft table is recreated.
func (u *setUpdater) reset() {
	u.mu.Lock()
	u.known = map[setKey]time.Time{}
	u.mu.Unlock()
}

func (u *setUpdater) Close() { u.cancel() }

func nftApplyStdin(script string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := runWithTimeout(cmd, 10*time.Second)
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

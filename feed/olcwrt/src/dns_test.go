package main

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeExchanger answers every query with fixed addresses.
type fakeExchanger struct {
	addrs []string
	err   error
	calls atomic.Int64
}

func (f *fakeExchanger) exchange(_ context.Context, q []byte) ([]byte, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return buildResponse(q, 120, f.addrs...), nil
}

type recordingApply struct {
	mu      sync.Mutex
	scripts []string
	fail    bool
}

func (r *recordingApply) apply(s string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scripts = append(r.scripts, s)
	if r.fail && strings.Contains(s, "delete") {
		return errors.New("no such element")
	}
	return nil
}

func (r *recordingApply) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.scripts, "")
}

func newTestRouter(defaultTunnel bool) (*dnsRouter, *fakeExchanger, *fakeExchanger, *recordingApply) {
	tun := &fakeExchanger{addrs: []string{"104.16.1.1", "2606:4700::1"}}
	dir := &fakeExchanger{addrs: []string{"77.88.55.80"}}
	rec := &recordingApply{}
	counters := &dnsCounters{}
	sets := newSetUpdater(time.Hour, &counters.learned, func(string, ...any) {})
	sets.apply = rec.apply
	r := &dnsRouter{
		proxy:         newDomainMatcher([]string{"example.com", "special.ya.ru"}),
		bypass:        newDomainMatcher([]string{"ru"}),
		defaultTunnel: defaultTunnel,
		tunnel:        tun,
		direct:        dir,
		sets:          sets,
		counters:      counters,
	}
	return r, tun, dir, rec
}

func TestRouterClassify(t *testing.T) {
	r, _, _, _ := newTestRouter(true)
	defer r.sets.Close()
	cases := map[string]routeClass{
		"www.example.com":  classProxy,
		"ya.ru":            classBypass,
		"x.special.ya.ru":  classProxy, // more specific proxy entry wins
		"github.com":       classDefault,
		"notexample.com":   classDefault,
		"example.com.ru":   classBypass,
		"special.ya.ru.ru": classBypass,
	}
	for name, want := range cases {
		if got := r.classify(name); got != want {
			t.Errorf("%s: got %d want %d", name, got, want)
		}
	}
}

func TestRouterResolveRoutes(t *testing.T) {
	r, tun, dir, rec := newTestRouter(false)
	defer r.sets.Close()
	ctx := context.Background()

	resp := r.resolve(ctx, buildQuery(1, "www.example.com", dnsTypeA, 0))
	if addrs, _ := answerAddrs(resp); len(addrs) != 2 || tun.calls.Load() != 1 {
		t.Fatalf("proxy route: %v tunnel calls %d", addrs, tun.calls.Load())
	}
	r.resolve(ctx, buildQuery(2, "mail.ru", dnsTypeA, 0))
	r.resolve(ctx, buildQuery(3, "github.com", dnsTypeA, 0))
	if dir.calls.Load() != 2 || tun.calls.Load() != 1 {
		t.Fatalf("direct calls %d tunnel calls %d", dir.calls.Load(), tun.calls.Load())
	}
	scripts := rec.all()
	for _, want := range []string{
		"add element inet olcwrt proxy4_dns { 104.16.1.1 timeout 3600s }",
		"add element inet olcwrt proxy6_dns { 2606:4700::1 timeout 3600s }",
		"add element inet olcwrt bypass4_dns { 77.88.55.80 timeout 3600s }",
	} {
		if !strings.Contains(scripts, want) {
			t.Errorf("missing %q in %q", want, scripts)
		}
	}
	if strings.Contains(scripts, "github") || strings.Count(scripts, "77.88.55.80") != 1 {
		t.Errorf("default route must not learn addresses: %q", scripts)
	}

	// Fresh entries are not re-added.
	n := len(rec.scripts)
	r.resolve(ctx, buildQuery(4, "example.com", dnsTypeA, 0))
	if len(rec.scripts) != n {
		t.Errorf("fresh entries were re-added")
	}
	st := r.counters.snapshot()
	if st.Queries != 4 || st.Tunnel != 2 || st.Direct != 2 || st.Learned != 3 {
		t.Errorf("stats %+v", st)
	}
}

func TestRouterDefaultTunnelAndAAAAFilter(t *testing.T) {
	r, tun, dir, _ := newTestRouter(true)
	defer r.sets.Close()
	r.filterAAAA = true
	ctx := context.Background()
	r.resolve(ctx, buildQuery(1, "github.com", dnsTypeA, 0))
	if tun.calls.Load() != 1 || dir.calls.Load() != 0 {
		t.Fatalf("default must go through the tunnel")
	}
	resp := r.resolve(ctx, buildQuery(2, "github.com", dnsTypeAAAA, 0))
	if addrs, _ := answerAddrs(resp); len(addrs) != 0 || tun.calls.Load() != 1 || resp[3]&0x0f != 0 {
		t.Fatalf("AAAA must be answered empty locally: %v", addrs)
	}
	// Bypassed names keep AAAA.
	r.resolve(ctx, buildQuery(3, "ya.ru", dnsTypeAAAA, 0))
	if dir.calls.Load() != 1 {
		t.Fatal("bypass AAAA must be forwarded")
	}
}

func TestRouterServFail(t *testing.T) {
	r, tun, _, _ := newTestRouter(true)
	defer r.sets.Close()
	tun.err = errors.New("down")
	resp := r.resolve(context.Background(), buildQuery(9, "github.com", dnsTypeA, 0))
	if msgID(resp) != 9 || resp[3]&0x0f != rcodeServFail {
		t.Fatalf("expected SERVFAIL, got %x", resp)
	}
	if r.resolve(context.Background(), []byte{1, 2, 3}) != nil {
		t.Fatal("garbage must be dropped")
	}
}

func TestSetUpdaterRefreshFallback(t *testing.T) {
	rec := &recordingApply{fail: true}
	var learned atomic.Int64
	u := newSetUpdater(time.Hour, &learned, func(string, ...any) {})
	defer u.Close()
	u.apply = rec.apply
	ans := []dnsAnswer{{Addr: mustAddr("1.2.3.4"), TTL: 60}}
	u.add(context.Background(), true, ans)
	// Pretend the entry is about to expire so the next answer refreshes it.
	u.mu.Lock()
	for k := range u.known {
		u.known[k] = time.Now().Add(time.Minute)
	}
	u.mu.Unlock()
	u.add(context.Background(), true, ans)
	s := rec.all()
	if !strings.Contains(s, "delete element inet olcwrt proxy4_dns { 1.2.3.4 }") {
		t.Fatalf("refresh must delete first: %q", s)
	}
	if learned.Load() != 2 || !strings.HasSuffix(s, "add element inet olcwrt proxy4_dns { 1.2.3.4 timeout 3600s }\n") {
		t.Fatalf("fallback add missing (learned %d): %q", learned.Load(), s)
	}
}

// fakeDNSServer answers TCP DNS queries, replying to each batch of n queries
// in reverse order to exercise pipelining.
func fakeDNSServer(t *testing.T, batch int, addrs ...string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					var qs [][]byte
					for len(qs) < batch {
						q, err := readTCPMsg(c)
						if err != nil {
							return
						}
						qs = append(qs, q)
					}
					for i := len(qs) - 1; i >= 0; i-- {
						if writeTCPMsg(c, buildResponse(qs[i], 30, addrs...)) != nil {
							return
						}
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

func TestTunnelResolverPipelined(t *testing.T) {
	dnsAddr := fakeDNSServer(t, 3, "9.9.9.9")
	socks := newFakeSocks(t, "", "", func(target string) (net.Conn, error) {
		if target != "1.1.1.1:53" {
			return nil, errors.New("unexpected target " + target)
		}
		return net.Dial("tcp", dnsAddr)
	})
	tr := newTunnelResolver(&socksDialer{addr: socks.addr()}, []string{"1.1.1.1:53"})
	defer tr.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(id uint16) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, err := tr.exchange(ctx, buildQuery(id, "host"+strconv.Itoa(int(id))+".example", dnsTypeA, 0))
			if err != nil {
				errs <- err
				return
			}
			q, _ := parseQuestion(resp)
			if msgID(resp) != id || q.Name != "host"+strconv.Itoa(int(id))+".example" {
				errs <- errors.New("response mismatch for id " + strconv.Itoa(int(id)))
			}
		}(uint16(100 + i))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := len(socks.seen()); n != 1 {
		t.Fatalf("expected one shared tunnel connection, got %d", n)
	}
}

func TestDNSServerUDPAndTCP(t *testing.T) {
	r, _, _, _ := newTestRouter(true)
	defer r.sets.Close()
	// A large answer to force truncation over UDP.
	var many []string
	for i := 1; i <= 60; i++ {
		many = append(many, "10.0.0."+strconv.Itoa(i))
	}
	r.tunnel = &fakeExchanger{addrs: many}

	// Some ports are reserved on Windows hosts, so retry a few.
	var srv *dnsServer
	var addr string
	var err error
	for i := 0; i < 50 && srv == nil; i++ {
		addr = "127.0.0.1:" + strconv.Itoa(20000+int(time.Now().UnixNano()%20000))
		srv, err = startDNS(addr, r)
	}
	if srv == nil {
		t.Fatal(err)
	}
	defer srv.Close()

	uc, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer uc.Close()
	uc.SetDeadline(time.Now().Add(3 * time.Second))
	uc.Write(buildQuery(77, "github.com", dnsTypeA, 0))
	buf := make([]byte, 4096)
	n, err := uc.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if msgID(buf[:n]) != 77 || buf[2]&0x02 == 0 || n > 512 {
		t.Fatalf("expected truncated UDP reply, got %d bytes flags %x", n, buf[2])
	}

	tc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	tc.SetDeadline(time.Now().Add(3 * time.Second))
	for id := uint16(1); id <= 2; id++ {
		if err := writeTCPMsg(tc, buildQuery(id, "github.com", dnsTypeA, 0)); err != nil {
			t.Fatal(err)
		}
		resp, err := readTCPMsg(tc)
		if err != nil {
			t.Fatal(err)
		}
		if addrs, _ := answerAddrs(resp); msgID(resp) != id || len(addrs) != 60 {
			t.Fatalf("tcp reply id %d with %d addrs", msgID(resp), len(addrs))
		}
	}
}

func TestDirectResolverUDP(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(buildResponse(buf[:n], 10, "5.5.5.5"), from)
		}
	}()
	d := &directResolver{servers: func() []string { return []string{"127.0.0.1:1", pc.LocalAddr().String()} }}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := d.exchange(ctx, buildQuery(3, "a.example", dnsTypeA, 0))
	if err != nil {
		t.Fatal(err)
	}
	if addrs, _ := answerAddrs(resp); len(addrs) != 1 || addrs[0].Addr.String() != "5.5.5.5" {
		t.Fatalf("got %v", addrs)
	}
}

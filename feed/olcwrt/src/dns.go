package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// DNSStats are the counters of the resolver.
type DNSStats struct {
	Queries int64 `json:"queries"`
	Tunnel  int64 `json:"tunnel"`
	Direct  int64 `json:"direct"`
	Errors  int64 `json:"errors"`
	Learned int64 `json:"learned"`
}

type dnsCounters struct {
	queries, tunnel, direct, errors, learned atomic.Int64
}

func (c *dnsCounters) snapshot() DNSStats {
	return DNSStats{
		Queries: c.queries.Load(), Tunnel: c.tunnel.Load(), Direct: c.direct.Load(),
		Errors: c.errors.Load(), Learned: c.learned.Load(),
	}
}

type routeClass int

const (
	classDefault routeClass = iota
	classProxy
	classBypass
)

type exchanger interface {
	exchange(ctx context.Context, query []byte) ([]byte, error)
}

// dnsRouter decides where each query goes and which nft set learns the
// answer. Listed domains win over the default route; on a tie between the
// proxy and bypass lists the more specific suffix wins, bypass on equality.
type dnsRouter struct {
	proxy, bypass *domainMatcher
	defaultTunnel bool
	filterAAAA    bool
	tunnel        exchanger
	direct        exchanger
	sets          *setUpdater
	counters      *dnsCounters
}

func (r *dnsRouter) classify(name string) routeClass {
	p, b := r.proxy.match(name), r.bypass.match(name)
	switch {
	case b >= 0 && b >= p:
		return classBypass
	case p >= 0:
		return classProxy
	}
	return classDefault
}

const dnsQueryTimeout = 8 * time.Second

func (r *dnsRouter) resolve(ctx context.Context, query []byte) []byte {
	r.counters.queries.Add(1)
	q, err := parseQuestion(query)
	if err != nil {
		r.counters.errors.Add(1)
		return nil
	}
	class := r.classify(q.Name)
	viaTunnel := class == classProxy || class == classDefault && r.defaultTunnel
	if viaTunnel && r.filterAAAA && q.Type == dnsTypeAAAA {
		return replyFor(query, 0)
	}

	up := r.direct
	if viaTunnel {
		up = r.tunnel
		r.counters.tunnel.Add(1)
	} else {
		r.counters.direct.Add(1)
	}
	ctx, cancel := context.WithTimeout(ctx, dnsQueryTimeout)
	defer cancel()
	resp, err := up.exchange(ctx, query)
	if err != nil || len(resp) < dnsHeaderLen {
		r.counters.errors.Add(1)
		return replyFor(query, rcodeServFail)
	}
	if class != classDefault && r.sets != nil {
		if addrs, _ := answerAddrs(resp); len(addrs) > 0 {
			r.sets.add(ctx, class == classProxy, addrs)
		}
	}
	return resp
}

// dnsServer listens on UDP and TCP and answers through a dnsRouter.
type dnsServer struct {
	router *dnsRouter
	udp    *net.UDPConn
	tcp    net.Listener
	ctx    context.Context
	cancel context.CancelFunc
	sem    chan struct{}
	wg     sync.WaitGroup
}

func startDNS(addr string, router *dnsRouter) (*dnsServer, error) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	udp, err := net.ListenUDP("udp", ua)
	if err != nil {
		return nil, err
	}
	tcp, err := net.Listen("tcp", addr)
	if err != nil {
		udp.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &dnsServer{router: router, udp: udp, tcp: tcp, ctx: ctx, cancel: cancel, sem: make(chan struct{}, 256)}
	s.wg.Add(2)
	go s.serveUDP()
	go s.serveTCP()
	return s, nil
}

func (s *dnsServer) Close() {
	s.cancel()
	s.udp.Close()
	s.tcp.Close()
	s.wg.Wait()
}

func (s *dnsServer) serveUDP() {
	defer s.wg.Done()
	buf := make([]byte, 4096)
	for {
		n, from, err := s.udp.ReadFromUDP(buf)
		if err != nil {
			if s.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		query := append([]byte(nil), buf[:n]...)
		select {
		case s.sem <- struct{}{}:
		default:
			continue // overloaded: let the client retry
		}
		s.wg.Add(1)
		go func(query []byte, from *net.UDPAddr) {
			defer func() { <-s.sem; s.wg.Done() }()
			resp := s.router.resolve(s.ctx, query)
			if resp == nil {
				return
			}
			if len(resp) > udpPayloadLimit(query) {
				if resp = truncatedReply(resp); resp == nil {
					return
				}
			}
			_, _ = s.udp.WriteToUDP(resp, from)
		}(query, from)
	}
}

func (s *dnsServer) serveTCP() {
	defer s.wg.Done()
	for {
		conn, err := s.tcp.Accept()
		if err != nil {
			if s.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveTCPConn(conn)
		}()
	}
}

func (s *dnsServer) serveTCPConn(conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(s.ctx, func() { conn.Close() })
	defer stop()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		query, err := readTCPMsg(conn)
		if err != nil {
			return
		}
		resp := s.router.resolve(s.ctx, query)
		if resp == nil {
			return
		}
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := writeTCPMsg(conn, resp); err != nil {
			return
		}
	}
}

func readTCPMsg(r io.Reader) ([]byte, error) {
	var l [2]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(l[:]))
	if n < dnsHeaderLen {
		return nil, errDNSMalformed
	}
	msg := make([]byte, n)
	_, err := io.ReadFull(r, msg)
	return msg, err
}

func writeTCPMsg(w io.Writer, msg []byte) error {
	out := make([]byte, 2+len(msg))
	binary.BigEndian.PutUint16(out, uint16(len(msg)))
	copy(out[2:], msg)
	_, err := w.Write(out)
	return err
}

// directResolver queries plain DNS servers over UDP with TCP fallback.
type directResolver struct {
	servers func() []string
}

func (d *directResolver) exchange(ctx context.Context, query []byte) ([]byte, error) {
	servers := d.servers()
	if len(servers) == 0 {
		return nil, errors.New("no direct dns servers")
	}
	var lastErr error
	for _, srv := range servers {
		resp, err := udpExchange(ctx, srv, query)
		if err == nil && resp[2]&0x02 != 0 {
			resp, err = tcpExchange(ctx, srv, query)
		}
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

func udpExchange(ctx context.Context, server string, query []byte) ([]byte, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(2500 * time.Millisecond)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		if n >= dnsHeaderLen && msgID(buf) == msgID(query) {
			return append([]byte(nil), buf[:n]...), nil
		}
	}
}

func tcpExchange(ctx context.Context, server string, query []byte) ([]byte, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if err := writeTCPMsg(conn, query); err != nil {
		return nil, err
	}
	return readTCPMsg(conn)
}

// tunnelResolver sends queries over TCP through the SOCKS5 tunnel, keeping
// one pipelined connection to the first working upstream.
type tunnelResolver struct {
	dialer    *socksDialer
	upstreams []string
	ctx       context.Context
	cancel    context.CancelFunc

	mu      sync.Mutex
	conn    *pipelinedConn
	idx     int
	dialing chan struct{}
	dialErr error
}

func newTunnelResolver(dialer *socksDialer, upstreams []string) *tunnelResolver {
	ctx, cancel := context.WithCancel(context.Background())
	return &tunnelResolver{dialer: dialer, upstreams: upstreams, ctx: ctx, cancel: cancel}
}

func (t *tunnelResolver) exchange(ctx context.Context, query []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 2 && ctx.Err() == nil; attempt++ {
		pc, err := t.get(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := pc.exchange(ctx, query)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		t.drop(pc)
	}
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	return nil, lastErr
}

// get returns the live connection, dialing it when needed. Concurrent
// callers share one dial instead of queueing behind each other.
func (t *tunnelResolver) get(ctx context.Context) (*pipelinedConn, error) {
	t.mu.Lock()
	if t.conn != nil && !t.conn.isClosed() {
		pc := t.conn
		t.mu.Unlock()
		return pc, nil
	}
	if len(t.upstreams) == 0 {
		t.mu.Unlock()
		return nil, errors.New("no tunnel dns upstreams")
	}
	wait := t.dialing
	if wait == nil {
		wait = make(chan struct{})
		t.dialing = wait
		go t.dial(wait, t.upstreams[t.idx%len(t.upstreams)])
	}
	t.mu.Unlock()

	select {
	case <-wait:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn != nil && !t.conn.isClosed() {
		return t.conn, nil
	}
	return nil, t.dialErr
}

func (t *tunnelResolver) dial(done chan struct{}, server string) {
	ctx, cancel := context.WithTimeout(t.ctx, 30*time.Second)
	conn, err := t.dialer.DialContext(ctx, "tcp", server)
	cancel()
	t.mu.Lock()
	if err != nil {
		t.idx++ // try the next upstream on the following dial
		t.dialErr = fmt.Errorf("tunnel dns %s: %w", server, err)
	} else {
		t.conn = newPipelinedConn(conn)
		t.dialErr = nil
	}
	t.dialing = nil
	t.mu.Unlock()
	close(done)
}

func (t *tunnelResolver) drop(pc *pipelinedConn) {
	t.mu.Lock()
	if t.conn == pc {
		t.conn = nil
	}
	t.mu.Unlock()
	pc.close(errors.New("dropped"))
}

func (t *tunnelResolver) Close() {
	t.cancel()
	t.mu.Lock()
	pc := t.conn
	t.conn = nil
	t.mu.Unlock()
	if pc != nil {
		pc.close(net.ErrClosed)
	}
}

// pipelinedConn multiplexes DNS queries over one TCP connection (RFC 7766),
// rewriting message IDs so concurrent clients cannot collide.
type pipelinedConn struct {
	conn    net.Conn
	wmu     sync.Mutex
	mu      sync.Mutex
	pending map[uint16]chan []byte
	nextID  uint16
	done    chan struct{}
	err     error
}

func newPipelinedConn(conn net.Conn) *pipelinedConn {
	pc := &pipelinedConn{conn: conn, pending: map[uint16]chan []byte{}, done: make(chan struct{})}
	go pc.readLoop()
	return pc
}

func (p *pipelinedConn) isClosed() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *pipelinedConn) close(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.isClosed() {
		return
	}
	p.err = err
	close(p.done)
	p.conn.Close()
}

func (p *pipelinedConn) readLoop() {
	for {
		msg, err := readTCPMsg(p.conn)
		if err != nil {
			p.close(err)
			return
		}
		p.mu.Lock()
		ch := p.pending[msgID(msg)]
		delete(p.pending, msgID(msg))
		p.mu.Unlock()
		if ch != nil {
			ch <- msg
		}
	}
}

func (p *pipelinedConn) exchange(ctx context.Context, query []byte) ([]byte, error) {
	ch := make(chan []byte, 1)
	p.mu.Lock()
	if p.isClosed() {
		p.mu.Unlock()
		return nil, p.err
	}
	if len(p.pending) >= 1024 {
		p.mu.Unlock()
		return nil, errors.New("too many pending queries")
	}
	id := p.nextID
	for p.pending[id] != nil {
		id++
	}
	p.nextID = id + 1
	p.pending[id] = ch
	p.mu.Unlock()

	q := append([]byte(nil), query...)
	setMsgID(q, id)
	p.wmu.Lock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err := writeTCPMsg(p.conn, q)
	p.wmu.Unlock()
	if err != nil {
		p.close(err)
		return nil, err
	}

	select {
	case resp := <-ch:
		setMsgID(resp, msgID(query))
		return resp, nil
	case <-p.done:
		return nil, p.err
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return nil, ctx.Err()
	}
}

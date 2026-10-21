package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// RedirStats are the counters of the transparent proxy.
type RedirStats struct {
	Active    int64 `json:"active"`
	Total     int64 `json:"total"`
	Failed    int64 `json:"failed"`
	Rejected  int64 `json:"rejected"`
	BytesUp   int64 `json:"bytes_up"`
	BytesDown int64 `json:"bytes_down"`
}

type redirCounters struct {
	active, total, failed, rejected, up, down atomic.Int64
}

func (c *redirCounters) snapshot() RedirStats {
	return RedirStats{
		Active: c.active.Load(), Total: c.total.Load(), Failed: c.failed.Load(),
		Rejected: c.rejected.Load(), BytesUp: c.up.Load(), BytesDown: c.down.Load(),
	}
}

// redirServer accepts connections redirected by nftables and forwards them to
// their original destination through the olcrtc SOCKS5 listener.
type redirServer struct {
	port     int
	dialer   *socksDialer
	counters *redirCounters
	sem      chan struct{}
	logf     func(string, ...any)

	ctx       context.Context
	cancel    context.CancelFunc
	listeners []net.Listener
	mu        sync.Mutex
	conns     map[net.Conn]struct{}
	wg        sync.WaitGroup
	lastWarn  atomic.Int64
}

const (
	redirMaxConns      = 1024
	redirDialTimeout   = 75 * time.Second
	redirCopyBufferLen = 16 * 1024
)

var copyBufPool = sync.Pool{New: func() any {
	b := make([]byte, redirCopyBufferLen)
	return &b
}}

func startRedir(port int, dialer *socksDialer, counters *redirCounters, logf func(string, ...any)) (*redirServer, error) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &redirServer{
		port: port, dialer: dialer, counters: counters, logf: logf,
		sem: make(chan struct{}, redirMaxConns), ctx: ctx, cancel: cancel,
		conns: map[net.Conn]struct{}{},
	}
	ln4, err := net.Listen("tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		cancel()
		return nil, err
	}
	r.listeners = append(r.listeners, ln4)
	// IPv6 is optional: the kernel may be built without it.
	if ln6, err := net.Listen("tcp6", net.JoinHostPort("::", strconv.Itoa(port))); err == nil {
		r.listeners = append(r.listeners, ln6)
	}
	for _, ln := range r.listeners {
		r.wg.Add(1)
		go r.acceptLoop(ln)
	}
	return r, nil
}

func (r *redirServer) acceptLoop(ln net.Listener) {
	defer r.wg.Done()
	delay := 5 * time.Millisecond
	for {
		conn, err := ln.Accept()
		if err != nil {
			if r.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			r.logf("redir: accept: %v", err)
			if !sleepCtx(r.ctx, delay) {
				return
			}
			delay = min(delay*2, time.Second)
			continue
		}
		delay = 5 * time.Millisecond
		select {
		case r.sem <- struct{}{}:
		default:
			r.counters.rejected.Add(1)
			r.warnLimited("redir: %d connections reached, refusing new ones", redirMaxConns)
			conn.Close()
			continue
		}
		r.track(conn, true)
		r.wg.Add(1)
		go r.handle(conn.(*net.TCPConn))
	}
}

func (r *redirServer) warnLimited(format string, args ...any) {
	now := time.Now().Unix()
	if last := r.lastWarn.Load(); now-last >= 30 && r.lastWarn.CompareAndSwap(last, now) {
		r.logf(format, args...)
	}
}

func (r *redirServer) track(c net.Conn, add bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if add {
		r.conns[c] = struct{}{}
	} else {
		delete(r.conns, c)
	}
}

func (r *redirServer) handle(client *net.TCPConn) {
	defer func() {
		client.Close()
		r.track(client, false)
		<-r.sem
		r.wg.Done()
	}()
	r.counters.total.Add(1)

	dst, err := originalDst(client)
	if err != nil || !dst.IsValid() || r.isSelf(client, dst) {
		r.counters.failed.Add(1)
		return
	}

	ctx, cancel := context.WithTimeout(r.ctx, redirDialTimeout)
	upstream, err := r.dialer.DialContext(ctx, "tcp", dst.String())
	cancel()
	if err != nil {
		r.counters.failed.Add(1)
		if r.ctx.Err() == nil {
			r.warnLimited("redir: %s: %v", dst, err)
		}
		return
	}
	r.track(upstream, true)
	defer func() {
		upstream.Close()
		r.track(upstream, false)
	}()

	r.counters.active.Add(1)
	defer r.counters.active.Add(-1)
	pipe(client, upstream, &r.counters.up, &r.counters.down)
}

// isSelf detects connections made directly to the redirect port, which
// would otherwise loop back into the proxy.
func (r *redirServer) isSelf(c *net.TCPConn, dst netip.AddrPort) bool {
	if int(dst.Port()) != r.port {
		return false
	}
	local, ok := c.LocalAddr().(*net.TCPAddr)
	return ok && local.AddrPort().Addr().Unmap() == dst.Addr()
}

// Close stops accepting, drops active connections and waits for handlers.
func (r *redirServer) Close() {
	r.cancel()
	for _, ln := range r.listeners {
		ln.Close()
	}
	r.mu.Lock()
	for c := range r.conns {
		c.Close()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

// pipe copies data in both directions. A clean EOF is propagated as a
// half-close; any error tears down both sides.
func pipe(client, upstream net.Conn, up, down *atomic.Int64) {
	done := make(chan struct{})
	finish := func(dst, src net.Conn, err error) {
		if err != nil {
			dst.Close()
			src.Close()
			return
		}
		closeWrite(dst)
	}
	go func() {
		finish(upstream, client, copyCounted(upstream, client, up))
		close(done)
	}()
	finish(client, upstream, copyCounted(client, upstream, down))
	<-done
}

// copyCounted returns nil on EOF and the error otherwise.
func copyCounted(dst io.Writer, src io.Reader, counter *atomic.Int64) error {
	bp := copyBufPool.Get().(*[]byte)
	defer copyBufPool.Put(bp)
	buf := *bp
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			counter.Add(int64(n))
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

package main

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ch := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		ch <- c
	}()
	a, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return a, <-ch
}

// TestPipeHalfClose checks that a client half-close reaches the upstream and
// the reply still flows back, with bytes counted in both directions.
func TestPipeHalfClose(t *testing.T) {
	client, clientPeer := tcpPair(t)
	upstreamPeer, upstream := tcpPair(t)
	var up, down atomic.Int64
	done := make(chan struct{})
	go func() {
		pipe(clientPeer, upstreamPeer, &up, &down)
		close(done)
	}()

	client.Write([]byte("request"))
	client.(*net.TCPConn).CloseWrite()
	got, err := io.ReadAll(upstream)
	if err != nil || string(got) != "request" {
		t.Fatalf("upstream got %q %v", got, err)
	}
	upstream.Write([]byte("response!"))
	upstream.Close()
	got, err = io.ReadAll(client)
	if err != nil || string(got) != "response!" {
		t.Fatalf("client got %q %v", got, err)
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pipe did not finish")
	}
	if up.Load() != 7 || down.Load() != 9 {
		t.Fatalf("up=%d down=%d", up.Load(), down.Load())
	}
}

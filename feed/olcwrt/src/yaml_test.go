package main

import (
	"strings"
	"testing"
)

func testServer(t *testing.T, uri string) *Server {
	t.Helper()
	s, err := ParseURI(uri)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBuildOlcrtcYAMLSingle(t *testing.T) {
	s := testServer(t, "olcrtc://wbstream?vp8channel<vp8-fps=30&vp8-batch=64>@room-'1#"+testKey)
	s.Token = "tok"
	got := buildOlcrtcYAML(olcrtcParams{DNS: "77.88.8.8:53", SocksHost: "127.0.0.1", SocksPort: 8808, Retry: "2s"}, []*Server{s})
	want := `mode: cnc
socks:
  host: '127.0.0.1'
  port: 8808
auth:
  provider: 'wbstream'
  token: 'tok'
room:
  id: 'room-''1'
crypto:
  key: '` + testKey + `'
net:
  transport: 'vp8channel'
  dns: '77.88.8.8:53'
vp8:
  fps: 30
  batch_size: 64
`
	if body := got[strings.IndexByte(got, '\n')+1:]; body != want {
		t.Fatalf("got:\n%s\nwant:\n%s", body, want)
	}
}

func TestBuildOlcrtcYAMLFailover(t *testing.T) {
	a := testServer(t, "olcrtc://jitsi?datachannel@https://meet.example.org/a#"+testKey)
	b := testServer(t, "olcrtc://wbstream?seichannel<fps=60&frag=900>@room-b#"+testKey)
	got := buildOlcrtcYAML(olcrtcParams{
		DNS: "1.1.1.1:53", SocksHost: "0.0.0.0", SocksPort: 1080, SocksUser: "u", SocksPass: "p'w", Debug: true, Retry: "5s",
	}, []*Server{a, b})
	for _, want := range []string{
		"debug: true\n",
		"  host: '0.0.0.0'\n  port: 1080\n  user: 'u'\n  pass: 'p''w'\n",
		"net:\n  dns: '1.1.1.1:53'\nprofiles:\n",
		"  - name: '" + a.ID + "'\n    auth:\n      provider: 'jitsi'\n",
		"    net:\n      transport: 'datachannel'\n",
		"  - name: '" + b.ID + "'\n",
		"    sei:\n      fps: 60\n      fragment_size: 900\n",
		"failover:\n  retry_delay: '5s'\n  max_cycles: 0\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Count(got, "dns:") != 1 {
		t.Errorf("dns must only be set at the top level:\n%s", got)
	}
}

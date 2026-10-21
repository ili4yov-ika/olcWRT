package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

const testKey = "d823fa01cb3e0609b67322f7cf984c4ee2e4ce2e294936fc24ef38c9e59f4799"

func TestParseURIBasic(t *testing.T) {
	s, err := ParseURI("olcrtc://jitsi?datachannel@https://meet.example.org/myroom#" + testKey + "$RU / olc free sub")
	if err != nil {
		t.Fatal(err)
	}
	if s.Provider != "jitsi" || s.Transport != "datachannel" || s.Room != "https://meet.example.org/myroom" {
		t.Fatalf("unexpected server: %+v", s)
	}
	if s.Key != testKey || s.Label != "RU / olc free sub" || s.Comment != "RU / olc free sub" {
		t.Fatalf("unexpected key/label: %+v", s)
	}
	if !strings.HasPrefix(s.ID, "s_") || len(s.ID) != 12 {
		t.Fatalf("bad id %q", s.ID)
	}
}

func TestParseURITransportParams(t *testing.T) {
	cases := []struct {
		uri  string
		want map[string]string
	}{
		{
			"olcrtc://wbstream?vp8channel<vp8-fps=60&vp8-batch=64>@room-01#" + testKey,
			map[string]string{"vp8_fps": "60", "vp8_batch": "64"},
		},
		{
			"olcrtc://wbstream?seichannel<fps=60&batch=64&frag=900&ack-ms=2000>@room-01#" + testKey + "$DE",
			map[string]string{"sei_fps": "60", "sei_batch": "64", "sei_frag": "900", "sei_ack_ms": "2000"},
		},
		{
			"olcrtc://telemost?videochannel<video-w=1080&video-h=1080&video-fps=60&video-codec=qrcode&video-bitrate=1M>@room-01#" + testKey,
			map[string]string{"video_w": "1080", "video_h": "1080", "video_fps": "60", "video_codec": "qrcode"},
		},
		{
			// olcbox writes plain fps/batch for VP8.
			"olcrtc://wbstream?vp8channel<fps=25&batch=8>@room#" + testKey,
			map[string]string{"vp8_fps": "25", "vp8_batch": "8"},
		},
	}
	for _, c := range cases {
		s, err := ParseURI(c.uri)
		if err != nil {
			t.Fatalf("%s: %v", c.uri, err)
		}
		if len(s.Opts) != len(c.want) {
			t.Fatalf("%s: opts %v, want %v", c.uri, s.Opts, c.want)
		}
		for k, v := range c.want {
			if s.Opts[k] != v {
				t.Fatalf("%s: %s=%q, want %q", c.uri, k, s.Opts[k], v)
			}
		}
	}
}

func TestParseURILegacyClientAndAliases(t *testing.T) {
	s, err := ParseURI("OLCRTC://wb-stream?sei@room-01#" + strings.ToUpper(testKey) + "%android-01$RU / name")
	if err != nil {
		t.Fatal(err)
	}
	if s.Provider != "wbstream" || s.Transport != "seichannel" || s.Key != testKey || s.Label != "RU / name" {
		t.Fatalf("unexpected: %+v", s)
	}
}

func TestParseURIErrors(t *testing.T) {
	bad := []string{
		"vless://x",
		"olcrtc://jitsi@room#" + testKey,
		"olcrtc://jitsi?datachannel#" + testKey,
		"olcrtc://jitsi?datachannel@room",
		"olcrtc://jitsi?datachannel@room#abcd",
		"olcrtc://jazz?datachannel@room#" + testKey,
		"olcrtc://jitsi?carrierpigeon@room#" + testKey,
		"olcrtc://jitsi?datachannel@#" + testKey,
	}
	for _, u := range bad {
		if _, err := ParseURI(u); err == nil {
			t.Errorf("%s: expected error", u)
		}
	}
}

func TestFormatURIRoundTrip(t *testing.T) {
	in := "olcrtc://wbstream?seichannel<fps=60&batch=64&frag=900&ack-ms=2000>@room-01#" + testKey + "$DE / olc free sub"
	s, err := ParseURI(in)
	if err != nil {
		t.Fatal(err)
	}
	if out := FormatURI(s); out != in {
		t.Fatalf("round trip:\n got %s\nwant %s", out, in)
	}
	s2, err := ParseURI(FormatURI(s))
	if err != nil || s2.ID != s.ID {
		t.Fatalf("id changed after round trip: %v %v", s2, err)
	}
}

const subDoc = `#name: Zarazaex Free RU
#update: 1778011200
#refresh: 10m
#color: #4A90E2
#used: 10mb/10gb

olcrtc://wbstream?seichannel<fps=60&batch=64&frag=900&ack-ms=2000>@room-01#` + testKey + `$RU / olcng free sub / IPv6
##name: RU-1
##color: #4A90E2
##ip: 203.0.113.10
##comment: basic free node

olcrtc://wbstream?broken@room#zz
olcrtc://jitsi?datachannel@https://meet.example.org/r#` + testKey + `$Jitsi room
##comment: reserve
`

func TestParseSubscription(t *testing.T) {
	d := ParseSubscription(subDoc)
	if d.Meta["name"] != "Zarazaex Free RU" || d.Meta["color"] != "#4A90E2" || d.Meta["refresh"] != "10m" {
		t.Fatalf("meta: %v", d.Meta)
	}
	if len(d.Entries) != 2 || len(d.Errors) != 1 {
		t.Fatalf("entries=%d errors=%v", len(d.Entries), d.Errors)
	}
	if d.Entries[0].Server.Label != "RU-1" || d.Entries[0].Meta["ip"] != "203.0.113.10" {
		t.Fatalf("entry 0: %+v %v", d.Entries[0].Server, d.Entries[0].Meta)
	}
	if d.Entries[1].Server.Label != "Jitsi room" || d.Entries[1].Meta["comment"] != "reserve" {
		t.Fatalf("entry 1: %+v %v", d.Entries[1].Server, d.Entries[1].Meta)
	}
}

func TestParseSubscriptionBase64AndBOM(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString([]byte(subDoc))
	if d := ParseSubscription(enc); len(d.Entries) != 2 {
		t.Fatalf("base64: %d entries", len(d.Entries))
	}
	if d := ParseSubscription(utf8BOM + subDoc); len(d.Entries) != 2 || d.Meta["name"] == "" {
		t.Fatalf("bom: %d entries, meta %v", len(d.Entries), d.Meta)
	}
}

func TestSubscriptionURLs(t *testing.T) {
	urls, ok := subscriptionURLs("https://example.org/sub?a=1\n\nolcbox://add?url=https%3A%2F%2Fexample.org%2Fsub%3Ftoken%3Da%2Bb%26device%3Dphone\n")
	if !ok || len(urls) != 2 || urls[1] != "https://example.org/sub?token=a+b&device=phone" {
		t.Fatalf("got %v %v", urls, ok)
	}
	if _, ok := subscriptionURLs("https://a\nolcrtc://x"); ok {
		t.Fatal("mixed input must not be treated as URLs")
	}
}

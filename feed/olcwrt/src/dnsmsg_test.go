package main

import (
	"encoding/binary"
	"net/netip"
	"strings"
	"testing"
)

// buildQuery makes a DNS query; edns > 0 adds an OPT record with that size.
func buildQuery(id uint16, name string, qtype uint16, edns int) []byte {
	msg := make([]byte, dnsHeaderLen)
	binary.BigEndian.PutUint16(msg, id)
	msg[2] = 0x01 // RD
	binary.BigEndian.PutUint16(msg[4:], 1)
	for _, label := range strings.Split(name, ".") {
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	msg = append(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, qtype)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	if edns > 0 {
		binary.BigEndian.PutUint16(msg[10:], 1)
		msg = append(msg, 0) // root name
		msg = binary.BigEndian.AppendUint16(msg, dnsTypeOPT)
		msg = binary.BigEndian.AppendUint16(msg, uint16(edns))
		msg = append(msg, 0, 0, 0, 0, 0, 0)
	}
	return msg
}

// buildResponse answers query with a CNAME and the given addresses, using
// compressed names.
func buildResponse(query []byte, ttl uint32, addrs ...string) []byte {
	q, _ := parseQuestion(query)
	msg := append([]byte(nil), query[:q.End]...)
	msg[2] |= 0x80
	msg[3] = 0x80
	binary.BigEndian.PutUint16(msg[6:], uint16(len(addrs)+1))
	binary.BigEndian.PutUint16(msg[10:], 0)
	// A CNAME first, pointing at the question name, to exercise compression.
	msg = append(msg, 0xc0, 0x0c)
	msg = binary.BigEndian.AppendUint16(msg, 5)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	msg = binary.BigEndian.AppendUint32(msg, ttl)
	msg = binary.BigEndian.AppendUint16(msg, 2)
	msg = append(msg, 0xc0, 0x0c)
	for _, a := range addrs {
		ip := netip.MustParseAddr(a)
		msg = append(msg, 0xc0, 0x0c)
		if ip.Is4() {
			msg = binary.BigEndian.AppendUint16(msg, dnsTypeA)
		} else {
			msg = binary.BigEndian.AppendUint16(msg, dnsTypeAAAA)
		}
		msg = binary.BigEndian.AppendUint16(msg, 1)
		msg = binary.BigEndian.AppendUint32(msg, ttl)
		b := ip.AsSlice()
		msg = binary.BigEndian.AppendUint16(msg, uint16(len(b)))
		msg = append(msg, b...)
	}
	return msg
}

func TestParseQuestion(t *testing.T) {
	q, err := parseQuestion(buildQuery(7, "WWW.Example.com", dnsTypeAAAA, 0))
	if err != nil || q.Name != "www.example.com" || q.Type != dnsTypeAAAA || q.Class != 1 {
		t.Fatalf("got %+v %v", q, err)
	}
	for _, bad := range [][]byte{nil, make([]byte, 12), append(buildQuery(1, "a.b", 1, 0)[:14], 0xff)} {
		if _, err := parseQuestion(bad); err == nil {
			t.Errorf("expected error for %x", bad)
		}
	}
	// A pointer loop must not hang.
	loop := buildQuery(1, "a", 1, 0)
	loop[12] = 0xc0
	loop[13] = 0x0c
	if _, err := parseQuestion(loop); err == nil {
		t.Error("expected error for pointer loop")
	}
}

func TestAnswerAddrs(t *testing.T) {
	q := buildQuery(9, "example.com", dnsTypeA, 0)
	resp := buildResponse(q, 300, "93.184.216.34", "2606:2800:220:1::1")
	addrs, err := answerAddrs(resp)
	if err != nil || len(addrs) != 2 {
		t.Fatalf("got %v %v", addrs, err)
	}
	if addrs[0].Addr.String() != "93.184.216.34" || addrs[0].TTL != 300 || addrs[1].Addr.String() != "2606:2800:220:1::1" {
		t.Fatalf("got %v", addrs)
	}
	if _, err := answerAddrs(resp[:len(resp)-3]); err == nil {
		t.Fatal("expected error for truncated message")
	}
}

func TestUDPPayloadLimitAndTruncation(t *testing.T) {
	if n := udpPayloadLimit(buildQuery(1, "a.b", 1, 0)); n != 512 {
		t.Fatalf("no edns: %d", n)
	}
	if n := udpPayloadLimit(buildQuery(1, "a.b", 1, 1232)); n != 1232 {
		t.Fatalf("edns: %d", n)
	}
	if n := udpPayloadLimit(buildQuery(1, "a.b", 1, 100)); n != 512 {
		t.Fatalf("small edns: %d", n)
	}
	q := buildQuery(5, "example.com", dnsTypeA, 0)
	resp := buildResponse(q, 60, "1.1.1.1")
	tr := truncatedReply(resp)
	if tr[2]&0x02 == 0 || binary.BigEndian.Uint16(tr[6:]) != 0 || len(tr) != len(q) {
		t.Fatalf("truncated reply %x", tr)
	}
}

func TestReplyFor(t *testing.T) {
	q := buildQuery(0xbeef, "example.com", dnsTypeAAAA, 1232)
	r := replyFor(q, rcodeServFail)
	if msgID(r) != 0xbeef || r[2]&0x80 == 0 || r[2]&0x01 == 0 || r[3]&0x0f != rcodeServFail {
		t.Fatalf("reply %x", r)
	}
	if binary.BigEndian.Uint16(r[10:]) != 0 {
		t.Fatal("additional records must be dropped")
	}
	if rq, err := parseQuestion(r); err != nil || rq.Name != "example.com" {
		t.Fatalf("question: %v %v", rq, err)
	}
}

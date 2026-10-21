package main

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"
)

const (
	dnsHeaderLen = 12
	dnsTypeA     = 1
	dnsTypeAAAA  = 28
	dnsTypeOPT   = 41

	rcodeServFail = 2
	rcodeRefused  = 5
)

var errDNSMalformed = errors.New("malformed dns message")

type dnsAnswer struct {
	Addr netip.Addr
	TTL  uint32
}

// dnsQuestion is the first question of a message.
type dnsQuestion struct {
	Name  string // lower-case, without the trailing dot
	Type  uint16
	Class uint16
	End   int // offset right after the question
}

func parseQuestion(msg []byte) (dnsQuestion, error) {
	if len(msg) < dnsHeaderLen || binary.BigEndian.Uint16(msg[4:6]) == 0 {
		return dnsQuestion{}, errDNSMalformed
	}
	name, off, err := readName(msg, dnsHeaderLen)
	if err != nil || off+4 > len(msg) {
		return dnsQuestion{}, errDNSMalformed
	}
	return dnsQuestion{
		Name:  name,
		Type:  binary.BigEndian.Uint16(msg[off:]),
		Class: binary.BigEndian.Uint16(msg[off+2:]),
		End:   off + 4,
	}, nil
}

// readName decodes a possibly compressed name and returns the offset after
// it in the original position.
func readName(msg []byte, off int) (string, int, error) {
	var labels []string
	end := -1
	for jumps := 0; ; {
		if off >= len(msg) {
			return "", 0, errDNSMalformed
		}
		l := int(msg[off])
		switch {
		case l == 0:
			if end < 0 {
				end = off + 1
			}
			return strings.ToLower(strings.Join(labels, ".")), end, nil
		case l&0xc0 == 0xc0:
			if off+1 >= len(msg) || jumps > 32 {
				return "", 0, errDNSMalformed
			}
			if end < 0 {
				end = off + 2
			}
			off = int(binary.BigEndian.Uint16(msg[off:]) & 0x3fff)
			jumps++
		case l&0xc0 != 0:
			return "", 0, errDNSMalformed
		default:
			if off+1+l > len(msg) {
				return "", 0, errDNSMalformed
			}
			labels = append(labels, string(msg[off+1:off+1+l]))
			off += 1 + l
		}
	}
}

func skipName(msg []byte, off int) (int, error) {
	_, end, err := readName(msg, off)
	return end, err
}

// rrIter walks the resource records after the question section.
type rrIter struct {
	msg []byte
	off int
}

type dnsRR struct {
	Type  uint16
	Class uint16
	TTL   uint32
	Data  []byte
}

func (it *rrIter) next() (dnsRR, error) {
	off, err := skipName(it.msg, it.off)
	if err != nil || off+10 > len(it.msg) {
		return dnsRR{}, errDNSMalformed
	}
	rr := dnsRR{
		Type:  binary.BigEndian.Uint16(it.msg[off:]),
		Class: binary.BigEndian.Uint16(it.msg[off+2:]),
		TTL:   binary.BigEndian.Uint32(it.msg[off+4:]),
	}
	n := int(binary.BigEndian.Uint16(it.msg[off+8:]))
	off += 10
	if off+n > len(it.msg) {
		return dnsRR{}, errDNSMalformed
	}
	rr.Data = it.msg[off : off+n]
	it.off = off + n
	return rr, nil
}

// answerAddrs returns the A/AAAA records of the answer section.
func answerAddrs(msg []byte) ([]dnsAnswer, error) {
	q, err := parseQuestion(msg)
	if err != nil {
		return nil, err
	}
	count := int(binary.BigEndian.Uint16(msg[6:8]))
	it := &rrIter{msg: msg, off: q.End}
	var out []dnsAnswer
	for i := 0; i < count; i++ {
		rr, err := it.next()
		if err != nil {
			return out, err
		}
		switch {
		case rr.Type == dnsTypeA && len(rr.Data) == 4:
			out = append(out, dnsAnswer{netip.AddrFrom4([4]byte(rr.Data)), rr.TTL})
		case rr.Type == dnsTypeAAAA && len(rr.Data) == 16:
			out = append(out, dnsAnswer{netip.AddrFrom16([16]byte(rr.Data)), rr.TTL})
		}
	}
	return out, nil
}

// udpPayloadLimit returns the response size the client accepts over UDP,
// taken from its EDNS0 OPT record (512 without one).
func udpPayloadLimit(query []byte) int {
	q, err := parseQuestion(query)
	if err != nil {
		return 512
	}
	h := query[:dnsHeaderLen]
	skip := int(binary.BigEndian.Uint16(h[6:8])) + int(binary.BigEndian.Uint16(h[8:10]))
	add := int(binary.BigEndian.Uint16(h[10:12]))
	it := &rrIter{msg: query, off: q.End}
	for i := 0; i < skip+add; i++ {
		rr, err := it.next()
		if err != nil {
			return 512
		}
		if i >= skip && rr.Type == dnsTypeOPT {
			return min(max(int(rr.Class), 512), 4096)
		}
	}
	return 512
}

// truncatedReply keeps only the header and question and sets the TC bit, so
// the client retries over TCP.
func truncatedReply(resp []byte) []byte {
	q, err := parseQuestion(resp)
	if err != nil {
		return nil
	}
	out := append([]byte(nil), resp[:q.End]...)
	out[2] |= 0x02
	binary.BigEndian.PutUint16(out[4:], 1)
	for i := 6; i < 12; i++ {
		out[i] = 0
	}
	return out
}

// replyFor builds an answer-less response to query with the given rcode.
func replyFor(query []byte, rcode byte) []byte {
	q, err := parseQuestion(query)
	if err != nil {
		return nil
	}
	out := append([]byte(nil), query[:q.End]...)
	out[2] = 0x80 | (query[2] & 0x79) // QR, keep opcode and RD
	out[3] = 0x80 | rcode             // RA
	binary.BigEndian.PutUint16(out[4:], 1)
	for i := 6; i < 12; i++ {
		out[i] = 0
	}
	return out
}

func msgID(msg []byte) uint16 { return binary.BigEndian.Uint16(msg) }

func setMsgID(msg []byte, id uint16) { binary.BigEndian.PutUint16(msg, id) }

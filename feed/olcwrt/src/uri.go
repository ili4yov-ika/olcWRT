package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const uriScheme = "olcrtc://"

var errBadURI = errors.New("malformed olcrtc:// link")

// isURI reports whether s starts with the olcrtc:// scheme (case-insensitive).
func isURI(s string) bool {
	return len(s) >= len(uriScheme) && strings.EqualFold(s[:len(uriScheme)], uriScheme)
}

// ParseURI decodes a link in the olcrtc URI format v1:
//
//	olcrtc://<provider>?<transport>[<k=v&...>]@<room>#<key>[%client]$<comment>
//
// The legacy "%client" suffix of the key is accepted and ignored.
func ParseURI(line string) (*Server, error) {
	line = strings.TrimSpace(strings.TrimPrefix(line, utf8BOM))
	if !isURI(line) {
		return nil, errBadURI
	}
	payload := line[len(uriScheme):]

	q := strings.IndexByte(payload, '?')
	if q <= 0 {
		return nil, fmt.Errorf("%w: missing transport", errBadURI)
	}
	at := indexFrom(payload, '@', q+1)
	if at < 0 {
		return nil, fmt.Errorf("%w: missing room", errBadURI)
	}
	hash := indexFrom(payload, '#', at+1)
	if hash < 0 {
		return nil, fmt.Errorf("%w: missing key", errBadURI)
	}
	keyEnd := len(payload)
	comment := ""
	if d := indexFrom(payload, '$', hash+1); d >= 0 {
		keyEnd = d
		comment = strings.TrimSpace(payload[d+1:])
	}
	if p := indexFrom(payload, '%', hash+1); p >= 0 && p < keyEnd {
		keyEnd = p
	}

	transport, params := splitTransportToken(payload[q+1 : at])
	s := &Server{
		Provider:  normalizeProvider(payload[:q]),
		Transport: normalizeTransport(transport),
		Room:      strings.TrimSpace(payload[at+1 : hash]),
		Key:       strings.ToLower(strings.TrimSpace(payload[hash+1 : keyEnd])),
		Comment:   comment,
		Label:     comment,
		Opts:      map[string]string{},
	}
	applyURIParams(s, params)
	if err := s.Validate(); err != nil {
		return nil, err
	}
	s.ID = serverID(s)
	return s, nil
}

func indexFrom(s string, c byte, from int) int {
	if from > len(s) {
		return -1
	}
	if i := strings.IndexByte(s[from:], c); i >= 0 {
		return from + i
	}
	return -1
}

// splitTransportToken splits "vp8channel<vp8-fps=30&vp8-batch=64>".
func splitTransportToken(tok string) (string, map[string]string) {
	params := map[string]string{}
	lt := strings.IndexByte(tok, '<')
	gt := strings.LastIndexByte(tok, '>')
	if lt < 0 || gt <= lt {
		return strings.TrimSpace(tok), params
	}
	for _, part := range strings.Split(tok[lt+1:gt], "&") {
		k, v, ok := strings.Cut(part, "=")
		k = strings.ToLower(strings.TrimSpace(k))
		if !ok || k == "" {
			continue
		}
		params[k] = strings.TrimSpace(v)
	}
	return strings.TrimSpace(tok[:lt]), params
}

func applyURIParams(s *Server, params map[string]string) {
	// olcbox writes plain fps/batch for vp8channel too.
	if s.Transport == "vp8channel" {
		for alias, key := range map[string]string{"fps": "vp8-fps", "batch": "vp8-batch"} {
			if v, ok := params[alias]; ok {
				if _, set := params[key]; !set {
					params[key] = v
				}
			}
		}
	}
	for _, o := range transportOptsFor(s.Transport) {
		v, ok := params[o.uri]
		if !ok || v == "" {
			continue
		}
		if o.kind == optInt {
			if n, err := strconv.Atoi(v); err != nil || n < 0 {
				continue
			}
		}
		s.Opts[o.uci] = v
	}
}

// FormatURI encodes a server as an olcrtc:// link.
func FormatURI(s *Server) string {
	var b strings.Builder
	b.WriteString(uriScheme)
	b.WriteString(s.Provider)
	b.WriteByte('?')
	b.WriteString(s.Transport)
	var params []string
	for _, o := range transportOptsFor(s.Transport) {
		if v := s.Opts[o.uci]; v != "" {
			params = append(params, o.uri+"="+v)
		}
	}
	if len(params) > 0 {
		b.WriteString("<" + strings.Join(params, "&") + ">")
	}
	b.WriteByte('@')
	b.WriteString(s.Room)
	b.WriteByte('#')
	b.WriteString(s.Key)
	if name := s.Label; name != "" {
		b.WriteByte('$')
		b.WriteString(name)
	} else if s.Comment != "" {
		b.WriteByte('$')
		b.WriteString(s.Comment)
	}
	return b.String()
}

// SubEntry is one server of a subscription together with its ## fields.
type SubEntry struct {
	Server *Server
	Meta   map[string]string
}

// SubData is a parsed subscription document.
type SubData struct {
	Meta    map[string]string
	Entries []SubEntry
	Errors  []string
}

// ParseSubscription parses the olcrtc subscription format: global "#key:"
// fields, olcrtc:// lines and per-server "##key:" fields following them.
// Base64-wrapped documents are accepted as well.
func ParseSubscription(text string) *SubData {
	text = strings.TrimPrefix(strings.TrimSpace(text), utf8BOM)
	if !strings.Contains(strings.ToLower(text), uriScheme) {
		if dec, ok := decodeBase64Text(text); ok {
			text = dec
		}
	}
	data := &SubData{Meta: map[string]string{}}
	var cur map[string]string
	for n, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(raw, utf8BOM))
		switch {
		case line == "":
		case isURI(line):
			s, err := ParseURI(line)
			if err != nil {
				data.Errors = append(data.Errors, fmt.Sprintf("line %d: %v", n+1, err))
				cur = nil
				continue
			}
			cur = map[string]string{}
			data.Entries = append(data.Entries, SubEntry{Server: s, Meta: cur})
		case strings.HasPrefix(line, "##"):
			if k, v, ok := parseSubField(line[2:]); ok && cur != nil {
				cur[k] = v
			}
		case strings.HasPrefix(line, "#"):
			if k, v, ok := parseSubField(line[1:]); ok {
				data.Meta[k] = v
			}
		}
	}
	for _, e := range data.Entries {
		if name := e.Meta["name"]; name != "" {
			e.Server.Label = name
		}
		if e.Server.Label == "" {
			e.Server.Label = e.Server.Room
		}
	}
	return data
}

func parseSubField(s string) (string, string, bool) {
	k, v, ok := strings.Cut(s, ":")
	k = strings.ToLower(strings.TrimSpace(k))
	if !ok || k == "" || strings.ContainsAny(k, " \t") {
		return "", "", false
	}
	return k, strings.TrimSpace(v), true
}

func decodeBase64Text(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), "")
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil && strings.Contains(strings.ToLower(string(b)), uriScheme) {
			return string(b), true
		}
	}
	return "", false
}

// sortedKeys is used for deterministic output.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// utf8BOM is stripped from the start of imported text.
const utf8BOM = "\xef\xbb\xbf"

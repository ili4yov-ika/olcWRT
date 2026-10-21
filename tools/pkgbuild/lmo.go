package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"sort"
	"strconv"
	"strings"
)

// poEntry is one message of a .po file.
type poEntry struct {
	Ctxt   string
	ID     string
	Plural string
	Str    []string // msgstr or msgstr[n]
}

// parsePO reads a gettext catalog. Strings keep the escaping rules of
// LuCI's po2lmo: \" and \\ are unescaped, other escapes stay literal.
func parsePO(r io.Reader) []poEntry {
	var out []poEntry
	var cur poEntry
	var target *string
	started := false
	flush := func() {
		if started && (cur.ID != "" || len(cur.Str) > 0) {
			out = append(out, cur)
		}
		cur, target, started = poEntry{}, nil, false
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "msgctxt "):
			flush()
			started = true
			target = &cur.Ctxt
		case strings.HasPrefix(line, "msgid "):
			if started && cur.Ctxt == "" || len(cur.Str) > 0 {
				flush()
			}
			started = true
			target = &cur.ID
		case strings.HasPrefix(line, "msgid_plural "):
			target = &cur.Plural
		case strings.HasPrefix(line, "msgstr"):
			idx := 0
			if strings.HasPrefix(line, "msgstr[") {
				idx, _ = strconv.Atoi(line[7:strings.IndexByte(line, ']')])
			}
			for len(cur.Str) <= idx {
				cur.Str = append(cur.Str, "")
			}
			target = &cur.Str[idx]
		}
		if target != nil {
			*target += poString(line)
		}
	}
	flush()
	return out
}

// poString extracts the quoted part of a .po line.
func poString(line string) string {
	start := strings.IndexByte(line, '"')
	if start < 0 {
		return ""
	}
	var b strings.Builder
	for i := start + 1; i < len(line); i++ {
		c := line[i]
		if c == '\\' && i+1 < len(line) {
			next := line[i+1]
			if next == '"' || next == '\\' {
				b.WriteByte(next)
			} else {
				b.WriteByte('\\')
				b.WriteByte(next)
			}
			i++
			continue
		}
		if c == '"' {
			break
		}
		b.WriteByte(c)
	}
	return b.String()
}

// sfhHash is Paul Hsieh's SuperFastHash as implemented by LuCI.
func sfhHash(data []byte) uint32 {
	n := len(data)
	if n == 0 {
		return 0
	}
	hash := uint32(n)
	get16 := func(i int) uint32 { return uint32(data[i]) | uint32(data[i+1])<<8 }
	rem := n & 3
	i := 0
	for blocks := n >> 2; blocks > 0; blocks-- {
		hash += get16(i)
		tmp := (get16(i+2) << 11) ^ hash
		hash = (hash << 16) ^ tmp
		i += 4
		hash += hash >> 11
	}
	switch rem {
	case 3:
		hash += get16(i)
		hash ^= hash << 16
		hash ^= uint32(int32(int8(data[i+2]))) << 18
		hash += hash >> 11
	case 2:
		hash += get16(i)
		hash ^= hash << 11
		hash += hash >> 17
	case 1:
		hash += uint32(int32(int8(data[i])))
		hash ^= hash << 10
		hash += hash >> 1
	}
	hash ^= hash << 3
	hash += hash >> 5
	hash ^= hash << 4
	hash += hash >> 17
	hash ^= hash << 25
	hash += hash >> 6
	return hash
}

type lmoEntry struct {
	key, val, off, length uint32
}

// compileLMO converts .po entries into LuCI's binary LMO catalog, matching
// po2lmo byte for byte. It returns nil when there is nothing to store.
func compileLMO(entries []poEntry) []byte {
	var data bytes.Buffer
	var index []lmoEntry
	add := func(key, val uint32, s string) {
		index = append(index, lmoEntry{key, val, uint32(data.Len()), uint32(len(s))})
		data.WriteString(s)
		for data.Len()%4 != 0 {
			data.WriteByte(0)
		}
	}
	for _, e := range entries {
		if e.ID == "" {
			if pf := pluralForms(e); pf != "" {
				add(0, 0, pf)
			}
			continue
		}
		plurals := len(e.Str) - 1
		for i, s := range e.Str {
			if s == "" {
				continue
			}
			var key string
			switch {
			case e.Ctxt != "" && e.Plural != "":
				key = e.Ctxt + "\x01" + e.ID + "\x02" + strconv.Itoa(i)
			case e.Ctxt != "":
				key = e.Ctxt + "\x01" + e.ID
			case e.Plural != "":
				key = e.ID + "\x02" + strconv.Itoa(i)
			default:
				key = e.ID
			}
			k := sfhHash([]byte(key))
			if k == sfhHash([]byte(s)) {
				continue
			}
			add(k, uint32(plurals+1), s)
		}
	}
	if data.Len() == 0 {
		return nil
	}
	sort.SliceStable(index, func(i, j int) bool { return index[i].key < index[j].key })
	out := data.Bytes()
	for _, e := range index {
		out = binary.BigEndian.AppendUint32(out, e.key)
		out = binary.BigEndian.AppendUint32(out, e.val)
		out = binary.BigEndian.AppendUint32(out, e.off)
		out = binary.BigEndian.AppendUint32(out, e.length)
	}
	return binary.BigEndian.AppendUint32(out, uint32(data.Len()))
}

func pluralForms(header poEntry) string {
	if len(header.Str) == 0 {
		return ""
	}
	for _, field := range strings.Split(header.Str[0], `\n`) {
		if len(field) > 14 && strings.EqualFold(field[:14], "Plural-Forms: ") {
			return field[14:]
		}
	}
	return ""
}

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var jsMsgRe = regexp.MustCompile(`\b_\(\s*('(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*")`)

// extractMsgIDs collects translatable strings of the LuCI app: _('...') calls
// in JavaScript and menu titles.
func extractMsgIDs(luciDir string) ([]string, error) {
	seen := map[string]bool{}
	err := filepath.Walk(filepath.Join(luciDir, "htdocs"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".js") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range jsMsgRe.FindAllStringSubmatch(string(src), -1) {
			seen[unescapeJS(m[1][1:len(m[1])-1])] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	menus, _ := filepath.Glob(filepath.Join(luciDir, "root", "usr", "share", "luci", "menu.d", "*.json"))
	for _, f := range menus {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var menu map[string]struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(raw, &menu); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		for _, e := range menu {
			if e.Title != "" {
				seen[e.Title] = true
			}
		}
	}
	var ids []string
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func unescapeJS(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func poQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

// cmdPot rewrites po/templates/olcwrt.pot and appends untranslated entries
// to every existing .po so translators see what is missing.
func cmdPot(root string) error {
	luciDir := filepath.Join(root, "feed", "luci-app-olcwrt")
	ids, err := extractMsgIDs(luciDir)
	if err != nil {
		return err
	}
	var pot strings.Builder
	pot.WriteString("msgid \"\"\nmsgstr \"Content-Type: text/plain; charset=UTF-8\\n\"\n")
	for _, id := range ids {
		fmt.Fprintf(&pot, "\nmsgid %s\nmsgstr \"\"\n", poQuote(id))
	}
	potPath := filepath.Join(luciDir, "po", "templates", "olcwrt.pot")
	if err := os.MkdirAll(filepath.Dir(potPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(potPath, []byte(pot.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s: %d messages\n", potPath, len(ids))

	pos, _ := filepath.Glob(filepath.Join(luciDir, "po", "*", "olcwrt.po"))
	for _, po := range pos {
		f, err := os.Open(po)
		if err != nil {
			return err
		}
		entries := parsePO(f)
		f.Close()
		have := map[string]bool{}
		for _, e := range entries {
			have[e.ID] = true
		}
		var missing []string
		for _, id := range ids {
			if !have[id] {
				missing = append(missing, id)
			}
		}
		want := map[string]bool{}
		for _, id := range ids {
			want[id] = true
		}
		for _, e := range entries {
			if e.ID != "" && !want[e.ID] {
				fmt.Printf("%s: obsolete: %q\n", po, e.ID)
			}
		}
		if len(missing) == 0 {
			fmt.Printf("%s: complete\n", po)
			continue
		}
		var add strings.Builder
		for _, id := range missing {
			fmt.Fprintf(&add, "\nmsgid %s\nmsgstr \"\"\n", poQuote(id))
		}
		fh, err := os.OpenFile(po, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, err = fh.WriteString(add.String())
		fh.Close()
		if err != nil {
			return err
		}
		fmt.Printf("%s: %d untranslated messages appended\n", po, len(missing))
	}
	return nil
}

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// logger writes timestamped lines to a size-capped file in RAM and mirrors
// selected messages to stdout, which procd forwards to syslog as info.
type logger struct {
	mu    sync.Mutex
	path  string
	max   int64
	f     *os.File
	size  int64
	debug bool
}

const logMaxSize = 256 * 1024

func newLogger(path string, debug bool) *logger {
	l := &logger{path: path, max: logMaxSize, debug: debug}
	l.open()
	return l
}

func (l *logger) open() {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	st, _ := f.Stat()
	l.f = f
	if st != nil {
		l.size = st.Size()
	}
}

func (l *logger) setDebug(v bool) {
	l.mu.Lock()
	l.debug = v
	l.mu.Unlock()
}

func (l *logger) write(source, msg string) {
	line := fmt.Sprintf("%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), source, msg)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	if l.size+int64(len(line)) > l.max {
		l.f.Close()
		_ = os.Rename(l.path, l.path+".1")
		l.f, l.size = nil, 0
		l.open()
		if l.f == nil {
			return
		}
	}
	n, _ := l.f.WriteString(line)
	l.size += int64(n)
}

// Infof logs to the file and to syslog.
func (l *logger) Infof(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.write("olcwrt", msg)
	fmt.Fprintln(os.Stdout, msg)
}

// Filef logs to the file only.
func (l *logger) Filef(format string, args ...any) {
	l.write("olcwrt", fmt.Sprintf(format, args...))
}

func (l *logger) Debugf(format string, args ...any) {
	l.mu.Lock()
	debug := l.debug
	l.mu.Unlock()
	if debug {
		l.write("olcwrt", fmt.Sprintf(format, args...))
	}
}

func (l *logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

// tailLog returns the last n lines of the log, including the rotated file.
func tailLog(path string, n int) []string {
	var lines []string
	for _, p := range []string{path + ".1", path} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		lines = append(lines, readLines(f)...)
		f.Close()
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
	}
	return lines
}

func readLines(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if s := strings.TrimRight(sc.Text(), "\r"); s != "" {
			out = append(out, s)
		}
	}
	return out
}

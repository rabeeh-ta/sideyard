package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	maxLogSize  = 10 << 20  // rotate app.log to app.log.1 past this size
	maxLineSize = 64 << 10  // longer lines are split
	tailWindow  = 512 << 10 // how far back Tail reads
)

// Log is a project's app.log. Every process of the project (old and new
// releases during a deploy) and Sideyard's own events write to it, one
// timestamped, tagged line at a time.
type Log struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func OpenLog(path string) (*Log, error) {
	l := &Log{path: path}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Log) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, st.Size()
	return nil
}

func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// Printf writes a Sideyard event line.
func (l *Log) Printf(format string, args ...any) {
	l.writeLine("sideyard", fmt.Sprintf(format, args...))
}

func (l *Log) writeLine(tag, line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := time.Now().UTC().Format("2006-01-02 15:04:05") + " [" + tag + "] " + line + "\n"
	if l.size+int64(len(s)) > maxLogSize {
		l.f.Close()
		os.Rename(l.path, l.path+".1")
		if err := l.open(); err != nil {
			return
		}
	}
	n, _ := io.WriteString(l.f, s)
	l.size += int64(n)
}

// Tail returns up to n of the most recent lines.
func (l *Log) Tail(n int) ([]string, error) {
	f, err := os.Open(l.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := max(st.Size()-tailWindow, 0)
	buf := make([]byte, st.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, err
	}
	if start > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return []string{}, nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// Search returns up to n of the most recent lines containing match,
// ignoring case, from all of app.log and the rotated app.log.1.
func (l *Log) Search(n int, match string) ([]string, error) {
	match = strings.ToLower(match)
	var found []string
	for _, path := range []string{l.path + ".1", l.path} {
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if line != "" && strings.Contains(strings.ToLower(line), match) {
				found = append(found, line)
			}
		}
	}
	if len(found) > n {
		found = found[len(found)-n:]
	}
	if found == nil {
		found = []string{}
	}
	return found, nil
}

// lineWriter turns a process's output stream into tagged log lines.
type lineWriter struct {
	log *Log
	tag string
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.log.writeLine(w.tag, strings.TrimRight(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) >= maxLineSize {
		w.flush()
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.log.writeLine(w.tag, string(w.buf))
		w.buf = w.buf[:0]
	}
}

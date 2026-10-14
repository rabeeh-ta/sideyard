package runner

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const journalSocket = "/run/systemd/journal/socket"

// journalWriter sends entries over journald's native protocol, so Sideyard's
// events carry the same project field as the app's own output.
type journalWriter struct {
	mu   sync.Mutex
	conn net.Conn
}

func (j *journalWriter) send(fields [][2]string) {
	var buf bytes.Buffer
	for _, f := range fields {
		k, v := f[0], f[1]
		if strings.ContainsRune(v, '\n') {
			// Binary-safe form: name, newline, little-endian length, value.
			buf.WriteString(k)
			buf.WriteByte('\n')
			binary.Write(&buf, binary.LittleEndian, uint64(len(v)))
			buf.WriteString(v)
			buf.WriteByte('\n')
		} else {
			buf.WriteString(k + "=" + v + "\n")
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.conn == nil {
		c, err := net.Dial("unixgram", journalSocket)
		if err != nil {
			return
		}
		j.conn = c
	}
	if _, err := j.conn.Write(buf.Bytes()); err != nil {
		j.conn.Close()
		j.conn = nil
	}
}

// parseJournal turns `journalctl -o json` output into log lines in the same
// format as the process runner's app.log.
func parseJournal(out []byte) []string {
	lines := []string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e struct {
			Message  json.RawMessage `json:"MESSAGE"`
			Release  string          `json:"SIDEYARD_RELEASE"`
			Realtime string          `json:"__REALTIME_TIMESTAMP"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		tag := "sideyard"
		if e.Release != "" {
			tag = "#" + e.Release
		}
		us, _ := strconv.ParseInt(e.Realtime, 10, 64)
		ts := time.UnixMicro(us).UTC().Format("2006-01-02 15:04:05")
		lines = append(lines, ts+" ["+tag+"] "+journalMessage(e.Message))
	}
	return lines
}

// journalMessage decodes MESSAGE, which journalctl emits as a string, as an
// array of bytes when it isn't valid UTF-8, or as null when it's too large.
func journalMessage(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "[message too large to show]"
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b []byte
	var ints []int
	if json.Unmarshal(raw, &ints) == nil {
		for _, i := range ints {
			b = append(b, byte(i))
		}
		return strings.ToValidUTF8(string(b), "�")
	}
	return string(raw)
}

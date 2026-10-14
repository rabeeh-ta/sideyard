// Package mailtest is a minimal SMTP server that records what it receives,
// for tests.
package mailtest

import (
	"bufio"
	"net"
	"strings"
	"sync"

	"sideyard/internal/mail"
)

type Received struct {
	From string
	To   []string
	Data string
}

type Server struct {
	ln       net.Listener
	mu       sync.Mutex
	messages []Received
	// RejectRcpt makes the server refuse recipients containing this text.
	RejectRcpt string
}

// Start listens on localhost. Close stops it.
func Start() (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln}
	go s.serve()
	return s, nil
}

// Config is a mail.Server pointing at this server.
func (s *Server) Config() mail.Server {
	addr := s.ln.Addr().(*net.TCPAddr)
	return mail.Server{Host: "127.0.0.1", Port: addr.Port, Security: "none"}
}

func (s *Server) Close() { s.ln.Close() }

func (s *Server) Messages() []Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Received(nil), s.messages...)
}

func (s *Server) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	reply := func(line string) { conn.Write([]byte(line + "\r\n")) }
	reply("220 mailtest ready")
	var msg Received
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			reply("250 mailtest")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			msg = Received{From: addr(line)}
			reply("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			if s.RejectRcpt != "" && strings.Contains(line, s.RejectRcpt) {
				reply("550 no such user")
				continue
			}
			msg.To = append(msg.To, addr(line))
			reply("250 ok")
		case cmd == "DATA":
			reply("354 go ahead")
			var data strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				data.WriteString(l)
			}
			msg.Data = data.String()
			s.mu.Lock()
			s.messages = append(s.messages, msg)
			s.mu.Unlock()
			reply("250 queued")
		case cmd == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func addr(line string) string {
	if i, j := strings.Index(line, "<"), strings.Index(line, ">"); i >= 0 && j > i {
		return line[i+1 : j]
	}
	return ""
}

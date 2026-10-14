// Package mail sends email through an SMTP provider. Every major provider
// (Resend, Postmark, SES, Mailgun, SendGrid, Brevo) accepts SMTP, so one
// integration covers all of them.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"-"`
	// Security is "tls" (implicit TLS, usually port 465), "starttls"
	// (usually 587) or "none", which is only safe for a relay on localhost.
	Security string `json:"security"`
}

func (s Server) Validate() error {
	switch {
	case strings.TrimSpace(s.Host) == "":
		return errors.New("SMTP host is required")
	case s.Port < 1 || s.Port > 65535:
		return errors.New("SMTP port must be between 1 and 65535")
	case s.Security != "tls" && s.Security != "starttls" && s.Security != "none":
		return errors.New(`security must be "tls", "starttls" or "none"`)
	}
	return nil
}

type Message struct {
	From    netmail.Address
	To      []netmail.Address
	ReplyTo *netmail.Address
	Subject string
	Text    string
	HTML    string
}

// Check connects and authenticates without sending anything.
func Check(ctx context.Context, s Server) error {
	c, err := dial(ctx, s)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Quit()
}

func Send(ctx context.Context, s Server, m Message) error {
	body, err := m.Encode()
	if err != nil {
		return err
	}
	c, err := dial(ctx, s)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Mail(m.From.Address); err != nil {
		return fmt.Errorf("sender refused: %w", err)
	}
	for _, to := range m.To {
		if err := c.Rcpt(to.Address); err != nil {
			return fmt.Errorf("recipient %s refused: %w", to.Address, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("message refused: %w", err)
	}
	return c.Quit()
}

func dial(ctx context.Context, s Server) (*smtp.Client, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	d := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	if s.Security == "tls" {
		conn, err = (&tls.Dialer{NetDialer: d, Config: &tls.Config{ServerName: s.Host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", addr, err)
	}
	conn.SetDeadline(time.Now().Add(time.Minute))
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if s.Security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			c.Close()
			return nil, errors.New("the server doesn't offer STARTTLS; try security \"tls\" on port 465")
		}
		if err := c.StartTLS(&tls.Config{ServerName: s.Host}); err != nil {
			c.Close()
			return nil, fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if s.Username != "" {
		// PlainAuth refuses to send the password over an unencrypted
		// connection, except to localhost.
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			c.Close()
			return nil, fmt.Errorf("login refused: %w", err)
		}
	}
	return c, nil
}

// Encode renders the message as MIME: text, HTML, or both as alternatives.
func (m Message) Encode() ([]byte, error) {
	if len(m.To) == 0 {
		return nil, errors.New("no recipients")
	}
	if m.Text == "" && m.HTML == "" {
		return nil, errors.New("a message needs text or html")
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		return nil, errors.New("subject can't contain line breaks")
	}
	var b bytes.Buffer
	to := make([]string, len(m.To))
	for i, a := range m.To {
		to[i] = a.String()
	}
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", m.From.String())
	header("To", strings.Join(to, ", "))
	if m.ReplyTo != nil {
		header("Reply-To", m.ReplyTo.String())
	}
	header("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("Message-ID", messageID(m.From.Address))
	header("MIME-Version", "1.0")

	switch {
	case m.Text != "" && m.HTML != "":
		mw := multipart.NewWriter(&b)
		header("Content-Type", `multipart/alternative; boundary="`+mw.Boundary()+`"`)
		b.WriteString("\r\n")
		for _, part := range []struct{ typ, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
			w, err := mw.CreatePart(textproto.MIMEHeader{
				"Content-Type":              {part.typ + "; charset=utf-8"},
				"Content-Transfer-Encoding": {"quoted-printable"},
			})
			if err != nil {
				return nil, err
			}
			if err := writeQP(w, part.body); err != nil {
				return nil, err
			}
		}
		if err := mw.Close(); err != nil {
			return nil, err
		}
	default:
		typ, body := "text/plain", m.Text
		if m.HTML != "" {
			typ, body = "text/html", m.HTML
		}
		header("Content-Type", typ+"; charset=utf-8")
		header("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		if err := writeQP(&b, body); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

func writeQP(w interface{ Write([]byte) (int, error) }, s string) error {
	qp := quotedprintable.NewWriter(w)
	if _, err := qp.Write([]byte(s)); err != nil {
		return err
	}
	return qp.Close()
}

func messageID(from string) string {
	domain := "sideyard.local"
	if i := strings.LastIndex(from, "@"); i >= 0 {
		domain = from[i+1:]
	}
	b := make([]byte, 12)
	rand.Read(b)
	return "<" + hex.EncodeToString(b) + "@" + domain + ">"
}

package mail_test

import (
	"context"
	"io"
	"mime"
	netmail "net/mail"
	"strings"
	"testing"

	"sideyard/internal/mail"
	"sideyard/internal/mail/mailtest"
)

func TestSend(t *testing.T) {
	srv, err := mailtest.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ctx := context.Background()
	if err := mail.Check(ctx, srv.Config()); err != nil {
		t.Fatalf("check: %v", err)
	}

	m := mail.Message{
		From:    netmail.Address{Name: "Notes", Address: "hi@notes.app"},
		To:      []netmail.Address{{Address: "a@example.com"}, {Name: "Bé", Address: "b@example.com"}},
		Subject: "Your weekly report — 3 new notes",
		Text:    "Hello\nThree notes this week.",
		HTML:    "<p>Hello</p><p>Three notes this week.</p>",
	}
	if err := mail.Send(ctx, srv.Config(), m); err != nil {
		t.Fatal(err)
	}
	got := srv.Messages()
	if len(got) != 1 || got[0].From != "hi@notes.app" || strings.Join(got[0].To, ",") != "a@example.com,b@example.com" {
		t.Fatalf("received %+v", got)
	}

	parsed, err := netmail.ReadMessage(strings.NewReader(got[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if subject != m.Subject {
		t.Errorf("subject = %q", subject)
	}
	if !strings.HasPrefix(parsed.Header.Get("Content-Type"), "multipart/alternative") {
		t.Errorf("content type = %q", parsed.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(parsed.Body)
	if !strings.Contains(string(body), "Three notes this week.") || !strings.Contains(string(body), "text/html") {
		t.Errorf("body = %s", body)
	}
}

func TestRejections(t *testing.T) {
	srv, err := mailtest.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.RejectRcpt = "nobody"
	err = mail.Send(context.Background(), srv.Config(), mail.Message{
		From: netmail.Address{Address: "a@b.c"}, To: []netmail.Address{{Address: "nobody@example.com"}},
		Subject: "x", Text: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "nobody@example.com refused") {
		t.Fatalf("rejected recipient: %v", err)
	}
	if _, err := (mail.Message{To: []netmail.Address{{Address: "a@b.c"}}, Subject: "a\r\nBcc: x@y.z", Text: "x"}).Encode(); err == nil {
		t.Error("a subject with a line break must be refused (header injection)")
	}
	if err := mail.Check(context.Background(), mail.Server{Host: "127.0.0.1", Port: 1, Security: "none"}); err == nil {
		t.Error("connecting to a closed port should fail")
	}
}

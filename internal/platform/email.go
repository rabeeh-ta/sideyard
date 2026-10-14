package platform

import (
	"context"
	"errors"
	"fmt"
	netmail "net/mail"
	"slices"
	"strings"
	"time"

	"sideyard/internal/mail"
	"sideyard/internal/store"
)

// Email is one SMTP provider for the whole platform, configured in
// Platform settings. Apps never see its credentials: they send through
// Sideyard's API with their SIDEYARD_TOKEN, from their project's sender,
// and every attempt lands in the project's delivery log.
const (
	settingEmail      = "email"
	emailsPerMinute   = 60
	maxEmailReceivers = 50
)

// ErrRateLimited means a project sent more than emailsPerMinute.
var ErrRateLimited = errors.New("too many emails; try again in a minute")

// EmailSettings is the platform's email setup as the dashboard sees it.
// Password is only ever sent in.
type EmailSettings struct {
	Enabled     bool   `json:"enabled"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Security    string `json:"security"`
	Username    string `json:"username"`
	Password    string `json:"password,omitempty"`
	HasPassword bool   `json:"hasPassword"`
	// From is the default sender, e.g. "Sideyard <hello@example.com>".
	From string `json:"from"`
	// SenderDomains limits which domains projects may send from: the ones
	// verified with the provider. Empty allows any.
	SenderDomains []string `json:"senderDomains"`
}

type storedEmail struct {
	EmailSettings
	SealedPassword []byte `json:"sealedPassword"`
}

func (p *Platform) loadEmailSettings() (storedEmail, error) {
	st := storedEmail{EmailSettings: EmailSettings{Port: 587, Security: "starttls", SenderDomains: []string{}}}
	err := p.store.JSONSetting(settingEmail, &st)
	st.Password = ""
	st.HasPassword = len(st.SealedPassword) > 0
	if st.SenderDomains == nil {
		st.SenderDomains = []string{}
	}
	return st, err
}

func (p *Platform) EmailSettings() (EmailSettings, error) {
	st, err := p.loadEmailSettings()
	return st.EmailSettings, err
}

// SaveEmailSettings checks Sideyard can log in to the provider before
// saving. An empty Password keeps the saved one.
func (p *Platform) SaveEmailSettings(ctx context.Context, in EmailSettings) error {
	old, err := p.loadEmailSettings()
	if err != nil {
		return err
	}
	in.Host = strings.TrimSpace(in.Host)
	in.Username = strings.TrimSpace(in.Username)
	var domains []string
	for _, d := range in.SenderDomains {
		if d = strings.ToLower(strings.Trim(strings.TrimSpace(d), "@.")); d != "" && !slices.Contains(domains, d) {
			domains = append(domains, d)
		}
	}
	in.SenderDomains = domains
	if in.SenderDomains == nil {
		in.SenderDomains = []string{}
	}
	sealed := old.SealedPassword
	if in.Password != "" {
		sealed = p.box.Seal([]byte(in.Password))
	}
	st := storedEmail{EmailSettings: in, SealedPassword: sealed}
	st.Password, st.HasPassword = "", false
	if in.Enabled {
		from, err := netmail.ParseAddress(in.From)
		if err != nil {
			return invalid("default sender %q isn't an email address", in.From)
		}
		if !domainAllowed(from.Address, in.SenderDomains) {
			return invalid("the default sender must be on one of the sender domains")
		}
		server, err := p.mailServer(st)
		if err != nil {
			return err
		}
		if err := server.Validate(); err != nil {
			return invalid("%v", err)
		}
		if err := mail.Check(ctx, server); err != nil {
			return invalid("couldn't log in to the email provider: %v", err)
		}
	}
	return p.store.SetJSONSetting(settingEmail, st)
}

func (p *Platform) mailServer(st storedEmail) (mail.Server, error) {
	s := mail.Server{Host: st.Host, Port: st.Port, Security: st.Security, Username: st.Username}
	if len(st.SealedPassword) > 0 {
		pw, err := p.box.Open(st.SealedPassword)
		if err != nil {
			return s, err
		}
		s.Password = string(pw)
	}
	return s, nil
}

func domainAllowed(address string, domains []string) bool {
	if len(domains) == 0 {
		return true
	}
	at := strings.LastIndex(address, "@")
	return at >= 0 && slices.Contains(domains, strings.ToLower(address[at+1:]))
}

type EmailRequest struct {
	From    string   `json:"from"` // optional; the project's sender otherwise
	To      []string `json:"to"`
	ReplyTo string   `json:"replyTo"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
	HTML    string   `json:"html"`
}

// SendEmail sends for a project and logs the attempt.
func (p *Platform) SendEmail(ctx context.Context, name string, req EmailRequest) error {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return err
	}
	st, err := p.loadEmailSettings()
	if err != nil {
		return err
	}
	if !st.Enabled {
		return invalid("email isn't set up; configure a provider in Platform settings")
	}
	n, err := p.store.EmailsSince(proj.ID, time.Now().Add(-time.Minute))
	if err != nil {
		return err
	}
	if n >= emailsPerMinute {
		return ErrRateLimited
	}
	msg, err := buildMessage(proj, st, req)
	if err != nil {
		return err
	}
	server, err := p.mailServer(st)
	if err != nil {
		return err
	}
	sendErr := mail.Send(ctx, server, msg)
	rec := store.Email{ProjectID: proj.ID, Sender: msg.From.String(), Recipients: addressList(msg.To), Subject: req.Subject, Status: "sent"}
	if sendErr != nil {
		rec.Status, rec.Detail = "failed", sendErr.Error()
	}
	if err := p.store.LogEmail(rec); err != nil {
		return err
	}
	if sendErr != nil {
		return fmt.Errorf("the provider didn't accept the email: %w", sendErr)
	}
	return nil
}

func buildMessage(proj store.Project, st storedEmail, req EmailRequest) (mail.Message, error) {
	from := strings.TrimSpace(req.From)
	if from == "" {
		from = proj.EmailFrom
	}
	if from == "" {
		from = st.From
	}
	sender, err := netmail.ParseAddress(from)
	if err != nil {
		return mail.Message{}, invalid("sender %q isn't an email address", from)
	}
	if !domainAllowed(sender.Address, st.SenderDomains) {
		return mail.Message{}, invalid("%s isn't on a sender domain set up in Platform settings", sender.Address)
	}
	if len(req.To) == 0 || len(req.To) > maxEmailReceivers {
		return mail.Message{}, invalid("send to 1 to %d recipients", maxEmailReceivers)
	}
	msg := mail.Message{From: *sender, Subject: strings.TrimSpace(req.Subject), Text: req.Text, HTML: req.HTML}
	for _, to := range req.To {
		a, err := netmail.ParseAddress(to)
		if err != nil {
			return mail.Message{}, invalid("recipient %q isn't an email address", to)
		}
		msg.To = append(msg.To, *a)
	}
	if req.ReplyTo != "" {
		a, err := netmail.ParseAddress(req.ReplyTo)
		if err != nil {
			return mail.Message{}, invalid("reply-to %q isn't an email address", req.ReplyTo)
		}
		msg.ReplyTo = a
	}
	switch {
	case msg.Subject == "":
		return mail.Message{}, invalid("subject is required")
	case strings.ContainsAny(msg.Subject, "\r\n"):
		return mail.Message{}, invalid("subject can't contain line breaks")
	case msg.Text == "" && msg.HTML == "":
		return mail.Message{}, invalid("text or html is required")
	}
	return msg, nil
}

func addressList(as []netmail.Address) string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Address
	}
	return strings.Join(out, ", ")
}

// SendTestEmail sends from the platform's default sender, to check the
// provider end to end.
func (p *Platform) SendTestEmail(ctx context.Context, to string) error {
	st, err := p.loadEmailSettings()
	if err != nil {
		return err
	}
	if !st.Enabled {
		return invalid("email isn't set up yet")
	}
	a, err := netmail.ParseAddress(to)
	if err != nil {
		return invalid("%q isn't an email address", to)
	}
	from, err := netmail.ParseAddress(st.From)
	if err != nil {
		return invalid("default sender %q isn't an email address", st.From)
	}
	server, err := p.mailServer(st)
	if err != nil {
		return err
	}
	return mail.Send(ctx, server, mail.Message{
		From: *from, To: []netmail.Address{*a}, Subject: "Sideyard test email",
		Text: "This is a test email from Sideyard. Email from your apps will be delivered the same way.",
	})
}

type EmailLog struct {
	Sender     string    `json:"sender"`
	Recipients string    `json:"recipients"`
	Subject    string    `json:"subject"`
	Status     string    `json:"status"`
	Detail     string    `json:"detail"`
	CreatedAt  time.Time `json:"createdAt"`
}

type ProjectEmail struct {
	Configured    bool       `json:"configured"`
	Sender        string     `json:"sender"`        // the project's own, may be empty
	DefaultSender string     `json:"defaultSender"` // the platform's
	SenderDomains []string   `json:"senderDomains"`
	Log           []EmailLog `json:"log"`
}

func (p *Platform) ProjectEmail(name string) (ProjectEmail, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return ProjectEmail{}, err
	}
	st, err := p.loadEmailSettings()
	if err != nil {
		return ProjectEmail{}, err
	}
	emails, err := p.store.Emails(proj.ID, 100)
	if err != nil {
		return ProjectEmail{}, err
	}
	v := ProjectEmail{Configured: st.Enabled, Sender: proj.EmailFrom, DefaultSender: st.From,
		SenderDomains: st.SenderDomains, Log: []EmailLog{}}
	for _, e := range emails {
		v.Log = append(v.Log, EmailLog{e.Sender, e.Recipients, e.Subject, e.Status, e.Detail, e.CreatedAt})
	}
	return v, nil
}

// SetProjectSender sets who a project's email comes from, e.g.
// "Notes <notes@example.com>". Empty means the platform default.
func (p *Platform) SetProjectSender(name, from string) error {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return err
	}
	from = strings.TrimSpace(from)
	if from != "" {
		a, err := netmail.ParseAddress(from)
		if err != nil {
			return invalid("%q isn't an email address", from)
		}
		st, err := p.loadEmailSettings()
		if err != nil {
			return err
		}
		if !domainAllowed(a.Address, st.SenderDomains) {
			return invalid("%s isn't on a sender domain set up in Platform settings (%s)", a.Address, strings.Join(st.SenderDomains, ", "))
		}
		from = a.String()
	}
	return p.store.SetEmailFrom(proj.ID, from)
}

// AppAuthorized checks the SIDEYARD_TOKEN an app presented.
func (p *Platform) AppAuthorized(name, token string) bool {
	proj, err := p.store.ProjectByName(name)
	return err == nil && p.verifyAppToken(proj, token)
}

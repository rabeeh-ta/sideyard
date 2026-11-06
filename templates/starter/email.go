package main

import "context"

// sendEmail sends through Sideyard, which holds the email provider's
// credentials. The sender is the one set in the project's Email tab, and
// every email shows in that tab's log. html may be empty.
func sendEmail(ctx context.Context, to []string, subject, text, html string) error {
	return sideyardAPI(ctx, "email/send", map[string]any{"to": to, "subject": subject, "text": text, "html": html}, nil)
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// sendEmail sends through Sideyard, which holds the email provider's
// credentials: the app authenticates with its SIDEYARD_TOKEN. The sender is
// the one set in the project's Email tab. html may be empty.
func sendEmail(ctx context.Context, to []string, subject, text, html string) error {
	body, err := json.Marshal(map[string]any{"to": to, "subject": subject, "text": text, "html": html})
	if err != nil {
		return err
	}
	url := os.Getenv("SIDEYARD_API_URL") + "/api/projects/" + os.Getenv("SIDEYARD_PROJECT") + "/email/send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("SIDEYARD_TOKEN"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("sending email: %s", bytes.TrimSpace(msg))
	}
	return nil
}

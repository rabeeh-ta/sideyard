package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
)

// sideyardAPI posts in as JSON to Sideyard's API for this project, at
// $SIDEYARD_API_URL/api/projects/$SIDEYARD_PROJECT/<path>, authenticated
// with the app's SIDEYARD_TOKEN, and decodes the answer into out (if not nil).
func sideyardAPI(ctx context.Context, path string, in, out any) error {
	base, project := os.Getenv("SIDEYARD_API_URL"), os.Getenv("SIDEYARD_PROJECT")
	if base == "" || project == "" {
		return errors.New("not running on Sideyard: SIDEYARD_API_URL and SIDEYARD_PROJECT aren't set")
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/projects/"+project+"/"+path, bytes.NewReader(body))
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
		var e struct {
			Error string `json:"error"`
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if json.Unmarshal(msg, &e) == nil && e.Error != "" {
			msg = []byte(e.Error)
		}
		return fmt.Errorf("sideyard %s: %s", path, bytes.TrimSpace(msg))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

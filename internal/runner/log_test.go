package runner

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLogSearch(t *testing.T) {
	l, err := OpenLog(filepath.Join(t.TempDir(), "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Printf("release #1: starting")
	w := &lineWriter{log: l, tag: "#1"}
	w.Write([]byte("listening on :8080\nGET /healthz 200\nERROR db locked\nget /healthz 200\n"))

	got, err := l.Search(10, "HEALTHZ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.HasSuffix(got[0], "GET /healthz 200") || !strings.HasSuffix(got[1], "get /healthz 200") {
		t.Fatalf("Search(healthz) = %q, want both healthz lines, oldest first", got)
	}
	if got, _ := l.Search(1, "healthz"); len(got) != 1 || !strings.HasSuffix(got[0], "get /healthz 200") {
		t.Fatalf("Search(1, healthz) = %q, want only the newest", got)
	}
	if got, _ := l.Search(10, "nothing like this"); got == nil || len(got) != 0 {
		t.Fatalf("Search with no match = %#v, want an empty slice", got)
	}

	// Lines in the rotated file are searched too, before the current ones.
	l.Close()
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := l.open(); err != nil {
		t.Fatal(err)
	}
	l.Printf("ERROR again")
	got, _ = l.Search(10, "error")
	if !slices.EqualFunc(got, []string{"ERROR db locked", "ERROR again"}, strings.HasSuffix) {
		t.Fatalf("Search(error) across rotation = %q", got)
	}
}

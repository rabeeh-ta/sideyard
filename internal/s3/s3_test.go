package s3_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	. "sideyard/internal/s3"
	"sideyard/internal/s3/s3test"
)

func newFake(t *testing.T) Config {
	t.Helper()
	cfg, stop := s3test.FakeServer("test-bucket")
	t.Cleanup(stop)
	return cfg
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	c, err := New(newFake(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}
	for _, key := range []string{"uploads/a.txt", "uploads/b.txt", "uploads/deep/c.txt", "readme.txt"} {
		if err := c.Put(ctx, key, strings.NewReader("hello "+key), int64(len("hello "+key)), "text/plain"); err != nil {
			t.Fatal(err)
		}
	}

	top, next, err := c.List(ctx, "", "", 100)
	if err != nil || next != "" {
		t.Fatalf("list: %v next=%q", err, next)
	}
	var keys []string
	for _, o := range top {
		keys = append(keys, o.Key)
	}
	if strings.Join(keys, ",") != "readme.txt,uploads/" {
		t.Fatalf("top level = %q", keys)
	}
	if !top[1].IsPrefix || top[0].IsPrefix {
		t.Fatalf("prefix flags wrong: %+v", top)
	}

	page, next, err := c.List(ctx, "uploads/", "", 1)
	if err != nil || next == "" || len(page) != 1 {
		t.Fatalf("paged list: %+v next=%q err=%v", page, next, err)
	}
	// Pages are in name order, folders among files, and the empty object a
	// console's "create folder" leaves isn't an entry of its own folder.
	for _, key := range []string{"pages/a/1.txt", "pages/b.txt", "pages/c/3.txt", "pages/"} {
		if err := c.Put(ctx, key, strings.NewReader("x"), 1, "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	listed, _, err := c.List(ctx, "pages/", "", 100)
	if got := keysOf(listed); err != nil || got != "pages/a/,pages/b.txt,pages/c/" {
		t.Fatalf("pages/ = %q %v", got, err)
	}
	// Tokens page through a folder. (The fake goes on from inside a folder
	// that ends a page, unlike S3, so the boundaries here are files.)
	var seen []Object
	for token := ""; ; {
		page, next, err := c.List(ctx, "uploads/", token, 1)
		if err != nil || len(seen) > 5 {
			t.Fatalf("page %q: %+v %v", token, page, err)
		}
		seen = append(seen, page...)
		if next == "" {
			break
		}
		token = next
	}
	if got := keysOf(seen); got != "uploads/a.txt,uploads/b.txt,uploads/deep/" {
		t.Fatalf("uploads/ in pages = %q", got)
	}

	all, truncated, err := c.Walk(ctx, "uploads/", 100)
	if err != nil || truncated || len(all) != 3 {
		t.Fatalf("walk: %+v %v %v", all, truncated, err)
	}

	r, err := c.Get(ctx, "readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r)
	r.Close()
	if string(body) != "hello readme.txt" {
		t.Fatalf("get = %q", body)
	}

	u, err := c.PresignGet(ctx, "readme.txt", time.Minute, false)
	if err != nil || !strings.Contains(u, "X-Amz-Signature") {
		t.Fatalf("presign: %q %v", u, err)
	}

	if err := c.Delete(ctx, "readme.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "readme.txt"); err == nil {
		t.Fatal("deleted object still readable")
	}
}

func TestCheckFailures(t *testing.T) {
	cfg := newFake(t)
	cfg.Bucket = "missing"
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "doesn't exist") {
		t.Fatalf("missing bucket: %v", err)
	}
	if _, err := New(Config{Bucket: "b", AccessKey: "a"}); err == nil {
		t.Fatal("a missing secret key should be rejected")
	}
}

func TestEndpointURL(t *testing.T) {
	for in, want := range map[Config]string{
		{Endpoint: "abc.r2.cloudflarestorage.com"}: "https://abc.r2.cloudflarestorage.com",
		{Endpoint: "http://127.0.0.1:9000/"}:       "http://127.0.0.1:9000",
		{Region: "eu-west-1"}:                      "https://s3.eu-west-1.amazonaws.com",
		{}:                                         "https://s3.amazonaws.com",
		{Endpoint: "https://s3.us-west-002.backblazeb2.com"}: "https://s3.us-west-002.backblazeb2.com",
	} {
		if got := in.EndpointURL(); got != want {
			t.Errorf("%+v: got %s, want %s", in, got, want)
		}
	}
}

func keysOf(objects []Object) string {
	var keys []string
	for _, o := range objects {
		keys = append(keys, o.Key)
	}
	return strings.Join(keys, ",")
}

// Package s3 is the slice of S3 that Sideyard needs, for any S3-compatible
// provider: AWS, Cloudflare R2, Backblaze B2, MinIO and so on. It backs a
// project's Storage tab and the platform's database backups.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Config struct {
	// Endpoint is the provider's S3 URL, e.g.
	// https://<account>.r2.cloudflarestorage.com. Empty means AWS.
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"-"`
}

// EndpointURL is the endpoint with its scheme, as SDKs expect it in
// AWS_ENDPOINT_URL_S3.
func (c Config) EndpointURL() string {
	e := strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	switch {
	case e == "" && c.Region != "":
		return "https://s3." + c.Region + ".amazonaws.com"
	case e == "":
		return "https://s3.amazonaws.com"
	case !strings.Contains(e, "://"):
		return "https://" + e
	}
	return e
}

func (c Config) Validate() error {
	switch {
	case strings.TrimSpace(c.Bucket) == "":
		return errors.New("bucket is required")
	case strings.TrimSpace(c.AccessKey) == "" || c.SecretKey == "":
		return errors.New("access key and secret key are required")
	}
	u, err := url.Parse(c.EndpointURL())
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("endpoint %q isn't a URL like https://s3.example.com", c.Endpoint)
	}
	return nil
}

type Client struct {
	mc     *minio.Client
	bucket string
}

func New(c Config) (*Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	u, _ := url.Parse(c.EndpointURL())
	mc, err := minio.New(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(strings.TrimSpace(c.AccessKey), c.SecretKey, ""),
		Secure:       u.Scheme == "https",
		Region:       strings.TrimSpace(c.Region),
		BucketLookup: minio.BucketLookupAuto,
	})
	if err != nil {
		return nil, err
	}
	return &Client{mc: mc, bucket: strings.TrimSpace(c.Bucket)}, nil
}

// Check lists one object, proving the endpoint, credentials and bucket work
// together.
func (c *Client) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for obj := range c.mc.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{MaxKeys: 1}) {
		if obj.Err != nil {
			return describe(obj.Err)
		}
		break
	}
	return nil
}

type Object struct {
	Key        string    `json:"key"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt,omitzero"`
	// IsPrefix marks a "folder": a common prefix ending in /.
	IsPrefix bool `json:"isPrefix,omitempty"`
}

// List returns up to limit entries directly under prefix, folders first as
// S3 orders them, starting after startAfter. more reports whether there are
// further entries.
func (c *Client) List(ctx context.Context, prefix, startAfter string, limit int) (entries []Object, more bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	opts := minio.ListObjectsOptions{Prefix: prefix, StartAfter: startAfter}
	for obj := range c.mc.ListObjects(ctx, c.bucket, opts) {
		if obj.Err != nil {
			return nil, false, describe(obj.Err)
		}
		if len(entries) == limit {
			return entries, true, nil
		}
		entries = append(entries, Object{
			Key: obj.Key, Size: obj.Size, ModifiedAt: obj.LastModified.UTC(),
			IsPrefix: strings.HasSuffix(obj.Key, "/") && obj.Size == 0 && obj.LastModified.IsZero(),
		})
	}
	return entries, false, nil
}

// Walk lists every object under prefix, recursively, up to max.
func (c *Client) Walk(ctx context.Context, prefix string, max int) (objects []Object, truncated bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for obj := range c.mc.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, false, describe(obj.Err)
		}
		if len(objects) == max {
			return objects, true, nil
		}
		objects = append(objects, Object{Key: obj.Key, Size: obj.Size, ModifiedAt: obj.LastModified.UTC()})
	}
	return objects, false, nil
}

func (c *Client) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err := c.mc.PutObject(ctx, c.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	return describe(err)
}

// Get opens an object for reading. The caller closes it.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, describe(err)
	}
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		return nil, describe(err)
	}
	return obj, nil
}

func (c *Client) Delete(ctx context.Context, key string) error {
	return describe(c.mc.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{}))
}

// PresignGet returns a short-lived URL that downloads the object as a file
// straight from the provider.
func (c *Client) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	name := key[strings.LastIndex(key, "/")+1:]
	params := url.Values{}
	params.Set("response-content-disposition", fmt.Sprintf("attachment; filename=%q", name))
	u, err := c.mc.PresignedGetObject(ctx, c.bucket, key, ttl, params)
	if err != nil {
		return "", describe(err)
	}
	return u.String(), nil
}

// describe turns S3 error responses into one readable line.
func describe(err error) error {
	if err == nil {
		return nil
	}
	resp := minio.ToErrorResponse(err)
	switch resp.Code {
	case "":
		return err
	case "NoSuchBucket":
		return fmt.Errorf("bucket %q doesn't exist", resp.BucketName)
	case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch":
		return fmt.Errorf("the provider refused these credentials (%s): %s", resp.Code, resp.Message)
	case "NoSuchKey":
		return fmt.Errorf("no such object: %s", resp.Key)
	}
	return fmt.Errorf("%s: %s", resp.Code, resp.Message)
}

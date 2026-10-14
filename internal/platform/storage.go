package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"sideyard/internal/s3"
	"sideyard/internal/store"
)

// StorageInfo is a project's connected bucket, without its secret.
type StorageInfo struct {
	Connected bool      `json:"connected"`
	Endpoint  string    `json:"endpoint"`
	Region    string    `json:"region"`
	Bucket    string    `json:"bucket"`
	AccessKey string    `json:"accessKey"`
	UpdatedAt time.Time `json:"updatedAt,omitzero"`
}

func (p *Platform) Storage(name string) (StorageInfo, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return StorageInfo{}, err
	}
	st, err := p.store.Storage(proj.ID)
	if errors.Is(err, store.ErrNotFound) {
		return StorageInfo{}, nil
	}
	if err != nil {
		return StorageInfo{}, err
	}
	return StorageInfo{Connected: true, Endpoint: st.Endpoint, Region: st.Region, Bucket: st.Bucket,
		AccessKey: st.AccessKey, UpdatedAt: st.UpdatedAt}, nil
}

// ConnectStorage checks the bucket works with these credentials, saves them
// and restarts the app (without downtime) so it gets the new variables. An
// empty SecretKey keeps the one already saved.
func (p *Platform) ConnectStorage(ctx context.Context, name string, cfg s3.Config) (restarted bool, err error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return false, err
	}
	if cfg.SecretKey == "" {
		if old, err := p.storageConfig(proj); err == nil {
			cfg.SecretKey = old.SecretKey
		}
	}
	client, err := s3.New(cfg)
	if err != nil {
		return false, invalid("%v", err)
	}
	if err := client.Check(ctx); err != nil {
		return false, invalid("couldn't use the bucket: %v", err)
	}
	vars, err := p.store.Variables(proj.ID)
	if err != nil {
		return false, err
	}
	var clash []string
	for _, v := range vars {
		if storageKey(v.Key) {
			clash = append(clash, v.Key)
		}
	}
	if len(clash) > 0 {
		return false, invalid("delete the variables %s first; the Storage tab sets them", strings.Join(clash, ", "))
	}
	if err := p.store.SetStorage(proj.ID, store.Storage{
		Endpoint: strings.TrimSpace(cfg.Endpoint), Region: strings.TrimSpace(cfg.Region), Bucket: strings.TrimSpace(cfg.Bucket),
		AccessKey: strings.TrimSpace(cfg.AccessKey), SecretKey: p.box.Seal([]byte(cfg.SecretKey)),
	}); err != nil {
		return false, err
	}
	p.logf(proj, "storage connected: bucket %s", cfg.Bucket)
	return p.restartForEnv(ctx, proj)
}

func (p *Platform) DisconnectStorage(ctx context.Context, name string) (restarted bool, err error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return false, err
	}
	if err := p.store.DeleteStorage(proj.ID); err != nil {
		return false, err
	}
	p.logf(proj, "storage disconnected")
	return p.restartForEnv(ctx, proj)
}

// restartForEnv restarts a live app so a changed environment takes effect.
func (p *Platform) restartForEnv(ctx context.Context, proj store.Project) (bool, error) {
	if p.getLive(proj.ID) == nil {
		return false, nil
	}
	if err := p.Restart(ctx, proj.Name); err != nil {
		return true, fmt.Errorf("saved, but the restart failed (the previous process is still serving): %w", err)
	}
	return true, nil
}

func (p *Platform) storageConfig(proj store.Project) (s3.Config, error) {
	st, err := p.store.Storage(proj.ID)
	if err != nil {
		return s3.Config{}, err
	}
	secret, err := p.box.Open(st.SecretKey)
	if err != nil {
		return s3.Config{}, err
	}
	return s3.Config{Endpoint: st.Endpoint, Region: st.Region, Bucket: st.Bucket, AccessKey: st.AccessKey, SecretKey: string(secret)}, nil
}

// storageEnv is the bucket's variables for the app, or nil if none is
// connected.
func (p *Platform) storageEnv(proj store.Project) ([]string, error) {
	cfg, err := p.storageConfig(proj)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	region := cfg.Region
	if region == "" {
		region = "auto" // R2's convention; AWS wants a real one, set in the Storage tab
	}
	return []string{
		"AWS_ENDPOINT_URL_S3=" + cfg.EndpointURL(),
		"AWS_REGION=" + region,
		"AWS_ACCESS_KEY_ID=" + cfg.AccessKey,
		"AWS_SECRET_ACCESS_KEY=" + cfg.SecretKey,
		"S3_BUCKET=" + cfg.Bucket,
	}, nil
}

func (p *Platform) storageClient(name string) (*s3.Client, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return nil, err
	}
	cfg, err := p.storageConfig(proj)
	if errors.Is(err, store.ErrNotFound) {
		return nil, invalid("no bucket is connected; connect one in the Storage tab")
	}
	if err != nil {
		return nil, err
	}
	return s3.New(cfg)
}

// Objects lists one "folder" of the project's bucket.
func (p *Platform) Objects(ctx context.Context, name, prefix, startAfter string) ([]s3.Object, bool, error) {
	c, err := p.storageClient(name)
	if err != nil {
		return nil, false, err
	}
	return c.List(ctx, prefix, startAfter, 200)
}

// ObjectURL is a five-minute download link straight from the provider.
func (p *Platform) ObjectURL(ctx context.Context, name, key string) (string, error) {
	c, err := p.storageClient(name)
	if err != nil {
		return "", err
	}
	return c.PresignGet(ctx, key, 5*time.Minute)
}

func (p *Platform) PutObject(ctx context.Context, name, key string, r io.Reader, size int64, contentType string) error {
	key = strings.TrimLeft(key, "/")
	if key == "" || strings.HasSuffix(key, "/") {
		return invalid("object key can't be empty or end in /")
	}
	c, err := p.storageClient(name)
	if err != nil {
		return err
	}
	return c.Put(ctx, key, r, size, contentType)
}

func (p *Platform) DeleteObject(ctx context.Context, name, key string) error {
	c, err := p.storageClient(name)
	if err != nil {
		return err
	}
	return c.Delete(ctx, key)
}

type StorageUsage struct {
	Objects   int   `json:"objects"`
	Bytes     int64 `json:"bytes"`
	Truncated bool  `json:"truncated"` // counted the first 100,000 objects only
}

func (p *Platform) StorageUsage(ctx context.Context, name string) (StorageUsage, error) {
	c, err := p.storageClient(name)
	if err != nil {
		return StorageUsage{}, err
	}
	objs, truncated, err := c.Walk(ctx, "", 100_000)
	if err != nil {
		return StorageUsage{}, err
	}
	u := StorageUsage{Objects: len(objs), Truncated: truncated}
	for _, o := range objs {
		u.Bytes += o.Size
	}
	return u, nil
}

package platform

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"sideyard/internal/secrets"
	"sideyard/internal/store"
)

// Builtin describes a variable Sideyard sets for an app.
type Builtin struct {
	Key         string `json:"key"`
	Description string `json:"description"`
}

var baseBuiltins = []Builtin{
	{"PORT", "Port the app must listen on, on 127.0.0.1"},
	{"DATA_DIR", "Persistent folder that survives deploys; also the working directory"},
	{"DATABASE_PATH", "SQLite file to use: $DATA_DIR/app.db"},
	{"SIDEYARD_PROJECT", "This project's name"},
	{"SIDEYARD_RELEASE", "The release number running"},
	{"SIDEYARD_TOKEN", "Secret shared with Sideyard: job calls carry it, and the app sends it to the email API"},
	{"SIDEYARD_API_URL", "Sideyard's API on this server, for sending email"},
}

// storageBuiltins are set while the project has a bucket connected. The
// AWS SDKs read the AWS_ names without any configuration.
var storageBuiltins = []Builtin{
	{"AWS_ENDPOINT_URL_S3", "The bucket's S3 endpoint (Storage tab)"},
	{"AWS_REGION", "The bucket's region"},
	{"AWS_ACCESS_KEY_ID", "Access key for the bucket"},
	{"AWS_SECRET_ACCESS_KEY", "Secret key for the bucket"},
	{"S3_BUCKET", "The bucket's name"},
}

// Builtins lists the variables Sideyard sets for a project's app.
func (p *Platform) Builtins(name string) ([]Builtin, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(baseBuiltins)
	if _, err := p.store.Storage(proj.ID); err == nil {
		out = append(out, storageBuiltins...)
	}
	return out, nil
}

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func reservedKey(k string) bool {
	switch k {
	case "PORT", "DATA_DIR", "DATABASE_PATH", "PATH", "HOME":
		return true
	}
	return strings.HasPrefix(k, "SIDEYARD_")
}

func storageKey(k string) bool {
	return slices.ContainsFunc(storageBuiltins, func(b Builtin) bool { return b.Key == k })
}

// environment is the complete environment of an app process. Sideyard's own
// environment is deliberately not inherited.
func (p *Platform) environment(proj store.Project, rel store.Release, port int) ([]string, error) {
	data := p.dir(proj.Name, "data")
	token, err := p.appToken(proj)
	if err != nil {
		return nil, err
	}
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + data,
		"PORT=" + strconv.Itoa(port),
		"DATA_DIR=" + data,
		"DATABASE_PATH=" + filepath.Join(data, "app.db"),
		"SIDEYARD_PROJECT=" + proj.Name,
		"SIDEYARD_RELEASE=" + strconv.Itoa(rel.Number),
		"SIDEYARD_TOKEN=" + token,
		"SIDEYARD_API_URL=" + p.apiURL(),
	}
	storage, err := p.storageEnv(proj)
	if err != nil {
		return nil, err
	}
	vars, err := p.store.Variables(proj.ID)
	if err != nil {
		return nil, err
	}
	for _, v := range vars {
		if storage != nil && storageKey(v.Key) {
			continue // the connected bucket wins
		}
		plain, err := p.box.Open(v.Value)
		if err != nil {
			return nil, fmt.Errorf("decrypt %s: %w", v.Key, err)
		}
		env = append(env, v.Key+"="+string(plain))
	}
	return append(env, storage...), nil
}

// appToken returns the project's SIDEYARD_TOKEN, creating it on first use.
func (p *Platform) appToken(proj store.Project) (string, error) {
	if len(proj.AppToken) > 0 {
		plain, err := p.box.Open(proj.AppToken)
		return string(plain), err
	}
	token := secrets.NewToken("syt_")
	return token, p.store.SetAppToken(proj.ID, p.box.Seal([]byte(token)))
}

// verifyAppToken checks a token an app presented to Sideyard.
func (p *Platform) verifyAppToken(proj store.Project, token string) bool {
	want, err := p.appToken(proj)
	return err == nil && token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1
}

// apiURL is where apps on this server reach Sideyard's API.
func (p *Platform) apiURL() string {
	host, port, err := net.SplitHostPort(p.cfg.APIAddr)
	if err != nil {
		return "http://" + p.cfg.APIAddr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// expandArgs splits the start arguments on spaces and expands $VARS in each,
// e.g. "serve --http=127.0.0.1:$PORT --dir=$DATA_DIR/pb_data".
func expandArgs(args string, env []string) []string {
	vals := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		vals[k] = v
	}
	fields := strings.Fields(args)
	for i, f := range fields {
		fields[i] = os.Expand(f, func(k string) string { return vals[k] })
	}
	return fields
}

type VariableInfo struct {
	Key       string `json:"key"`
	UpdatedAt string `json:"updatedAt"`
}

func (p *Platform) Variables(name string) ([]VariableInfo, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return nil, err
	}
	vars, err := p.store.Variables(proj.ID)
	if err != nil {
		return nil, err
	}
	out := make([]VariableInfo, 0, len(vars))
	for _, v := range vars {
		out = append(out, VariableInfo{Key: v.Key, UpdatedAt: v.UpdatedAt.Format("2006-01-02T15:04:05Z")})
	}
	return out, nil
}

func (p *Platform) RevealVariable(name, key string) (string, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return "", err
	}
	v, err := p.store.Variable(proj.ID, key)
	if err != nil {
		return "", err
	}
	plain, err := p.box.Open(v.Value)
	return string(plain), err
}

// UpdateVariables saves changes, then restarts the app if it's running so the
// new values take effect. restarted reports whether a restart was attempted.
func (p *Platform) UpdateVariables(ctx context.Context, name string, set map[string]string, del []string) (restarted bool, err error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return false, err
	}
	sealed := make(map[string][]byte, len(set))
	for k, v := range set {
		if !keyRe.MatchString(k) {
			return false, invalid("%q is not a valid variable name", k)
		}
		if reservedKey(k) {
			return false, invalid("%s is set by Sideyard and can't be overridden", k)
		}
		if storageKey(k) {
			if _, err := p.store.Storage(proj.ID); err == nil {
				return false, invalid("%s comes from the connected bucket (Storage tab)", k)
			}
		}
		sealed[k] = p.box.Seal([]byte(v))
	}
	if err := p.store.UpdateVariables(proj.ID, sealed, del); err != nil {
		return false, err
	}
	if p.getLive(proj.ID) == nil {
		return false, nil
	}
	if err := p.Restart(ctx, name); err != nil {
		return true, fmt.Errorf("variables saved, but the restart failed (the previous process is still serving): %w", err)
	}
	return true, nil
}

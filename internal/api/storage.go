package api

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"sideyard/internal/s3"
)

func (s *Server) getStorage(w http.ResponseWriter, r *http.Request) {
	info, err := s.p.Storage(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// connectStorage takes the bucket settings; an empty secretKey keeps the
// saved one.
func (s *Server) connectStorage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		s3.Config
		SecretKey string `json:"secretKey"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	body.Config.SecretKey = body.SecretKey
	restarted, err := s.p.ConnectStorage(r.Context(), r.PathValue("name"), body.Config)
	writeRestart(w, restarted, err)
}

func (s *Server) disconnectStorage(w http.ResponseWriter, r *http.Request) {
	restarted, err := s.p.DisconnectStorage(r.Context(), r.PathValue("name"))
	writeRestart(w, restarted, err)
}

// writeRestart reports a change that restarts the app: a failed restart
// still saved the change, so it's a 200 with an error message.
func writeRestart(w http.ResponseWriter, restarted bool, err error) {
	if err != nil && !restarted {
		writeErr(w, err)
		return
	}
	resp := map[string]any{"restarted": restarted}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) listObjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	objs, more, err := s.p.Objects(r.Context(), r.PathValue("name"), q.Get("prefix"), q.Get("after"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if objs == nil {
		objs = []s3.Object{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"objects": objs, "more": more})
}

// downloadObject redirects to a short-lived link on the provider, so large
// files don't pass through Sideyard.
func (s *Server) downloadObject(w http.ResponseWriter, r *http.Request) {
	u, err := s.p.ObjectURL(r.Context(), r.PathValue("name"), r.URL.Query().Get("key"))
	if err != nil {
		writeErr(w, err)
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

// uploadObject takes a multipart form: "prefix" (the folder), then one or
// more "file" parts, each stored at prefix + file name.
func (s *Server) uploadObject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	r.Body = http.MaxBytesReader(w, r.Body, 5<<30)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}
	var prefix string
	var keys []string
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "reading upload: "+err.Error())
			return
		}
		switch part.FormName() {
		case "prefix":
			prefix = readField(part)
		case "file":
			key := strings.TrimLeft(prefix, "/") + part.FileName()
			if err := s.putPart(r, name, key, part); err != nil {
				writeErr(w, err)
				return
			}
			keys = append(keys, key)
		}
		part.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploaded": keys})
}

// putPart stages a part on disk so its size is known, then uploads it.
func (s *Server) putPart(r *http.Request, name, key string, part io.Reader) error {
	f, err := os.CreateTemp("", "sideyard-upload-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	size, err := io.Copy(f, part)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return s.p.PutObject(r.Context(), name, key, f, size, "")
}

func (s *Server) deleteObject(w http.ResponseWriter, r *http.Request) {
	if err := s.p.DeleteObject(r.Context(), r.PathValue("name"), r.URL.Query().Get("key")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) storageUsage(w http.ResponseWriter, r *http.Request) {
	u, err := s.p.StorageUsage(r.Context(), r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

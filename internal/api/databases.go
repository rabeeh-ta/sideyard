package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"time"

	"sideyard/internal/platform"
	"sideyard/internal/store"
)

func (s *Server) listDatabases(w http.ResponseWriter, r *http.Request) {
	dbs, backups, err := s.p.Databases(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if dbs == nil {
		dbs = []platform.Database{}
	}
	if backups == nil {
		backups = []platform.Database{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"databases": dbs, "backups": backups})
}

// downloadDatabase serves a consistent snapshot of ?path=, safe to take
// while the app is running.
func (s *Server) downloadDatabase(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	snap, cleanup, err := s.p.SnapshotDatabase(r.Context(), r.PathValue("name"), rel)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer cleanup()
	f, err := os.Open(snap)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer f.Close()
	name := r.PathValue("name") + "-" + path.Base(rel)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	http.ServeContent(w, r, name, time.Now(), f)
}

// importDatabase takes a multipart upload: "path" (relative to DATA_DIR)
// and the "database" file.
func (s *Server) importDatabase(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := s.p.Project(name); err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<30)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data with \"path\" and a \"database\" file")
		return
	}
	var upload, rel string
	defer func() {
		if upload != "" {
			os.Remove(upload) // no-op once the import has moved it
		}
	}()
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
		case "database":
			if upload, err = saveUpload(s.p.ImportUploadDir(name), part); err != nil {
				writeError(w, http.StatusBadRequest, "saving upload: "+err.Error())
				return
			}
		case "path":
			rel = readField(part)
		}
		part.Close()
	}
	if upload == "" {
		writeError(w, http.StatusBadRequest, "missing \"database\" file field")
		return
	}
	backup, err := s.p.ImportDatabase(r.Context(), name, rel, upload)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": rel, "backup": backup})
}

func (s *Server) dbSchema(w http.ResponseWriter, r *http.Request) {
	tables, err := s.p.DatabaseSchema(r.Context(), r.PathValue("name"), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tables)
}

func (s *Server) dbRows(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		store.RowsRequest
	}
	if !readJSON(w, r, &body) {
		return
	}
	res, err := s.p.TableRows(r.Context(), r.PathValue("name"), body.Path, body.RowsRequest)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// dbEdit takes {path, table, kind, values, keys}. Numbers are decoded
// exactly, so large integer keys and values aren't rounded.
func (s *Server) dbEdit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		store.Mutation
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	res, err := s.p.EditRows(r.Context(), r.PathValue("name"), body.Path, body.Mutation)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) dbAlter(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		store.Alter
	}
	if !readJSON(w, r, &body) {
		return
	}
	q, err := s.p.AlterDatabase(r.Context(), r.PathValue("name"), body.Path, body.Alter)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sql": q})
}

// dbSQL runs the SQL editor's script. Without "write" it's read-only and
// answers needsWrite for anything that would change data.
func (s *Server) dbSQL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path  string `json:"path"`
		SQL   string `json:"sql"`
		Write bool   `json:"write"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	started := time.Now()
	res, err := s.p.ExecSQL(r.Context(), r.PathValue("name"), body.Path, body.SQL, body.Write)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "ms": time.Since(started).Milliseconds()})
}

func (s *Server) remoteBackups(w http.ResponseWriter, r *http.Request) {
	backups, ok, err := s.p.RemoteBackups(r.Context(), r.PathValue("name"), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": ok, "backups": backups})
}

func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		Key  string `json:"key"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	backup, err := s.p.RestoreBackup(r.Context(), r.PathValue("name"), body.Path, body.Key)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": body.Path, "backup": backup})
}

func (s *Server) downloadRemoteBackup(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	u, err := s.p.BackupURL(r.Context(), r.PathValue("name"), q.Get("path"), q.Get("key"))
	if err != nil {
		writeErr(w, err)
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

func (s *Server) savedQueries(w http.ResponseWriter, r *http.Request) {
	queries, err := s.p.SavedQueries(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, queries)
}

func (s *Server) saveQuery(w http.ResponseWriter, r *http.Request) {
	var q platform.SavedQuery
	if !readJSON(w, r, &q) {
		return
	}
	q.ID = r.PathValue("id")
	if err := s.p.SaveQuery(r.PathValue("name"), q); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteQuery(w http.ResponseWriter, r *http.Request) {
	if err := s.p.DeleteQuery(r.PathValue("name"), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

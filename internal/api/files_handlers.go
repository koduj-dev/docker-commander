package api

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"path"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	p := r.URL.Query().Get("path")
	entries, err := s.docker.ListPath(r.Context(), hostID, id, p)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": normPath(p), "entries": entries})
}

// handleDownloadFile streams a path out of the container. A single file is
// extracted from the archive and sent as-is; a directory is sent as a tar.
func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	p := r.URL.Query().Get("path")
	if p == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	rc, stat, err := s.docker.CopyFrom(r.Context(), hostID, id, p)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	defer rc.Close()
	s.audit(r, "container.cp.download", id, p)

	if stat.Mode.IsDir() {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Content-Disposition", `attachment; filename="`+stat.Name+`.tar"`)
		_, _ = io.Copy(w, rc)
		return
	}
	// Single file: pull the one entry out of the tar and stream its bytes.
	tr := tar.NewReader(rc)
	hdr, err := tr.Next()
	if err != nil {
		writeErr(w, http.StatusBadGateway, "empty archive")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+path.Base(hdr.Name)+`"`)
	_, _ = io.Copy(w, tr)
}

// handleUploadFile writes an uploaded file into a container directory. The file
// bytes are the raw request body; ?path is the destination directory and ?name
// the file name to create there.
func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	destDir := r.URL.Query().Get("path")
	name := path.Base(r.URL.Query().Get("name"))
	if destDir == "" || name == "" || name == "." || name == "/" {
		writeErr(w, http.StatusBadRequest, "path and name are required")
		return
	}

	// Buffer the upload so we know its size for the tar header.
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<32)) // 4 GiB guard
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body failed")
		return
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
		writeErr(w, http.StatusInternalServerError, "tar error")
		return
	}
	if _, err := tw.Write(data); err != nil {
		writeErr(w, http.StatusInternalServerError, "tar error")
		return
	}
	tw.Close()

	if err := s.docker.CopyTo(r.Context(), hostID, id, destDir, &buf); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "container.cp.upload", id, destDir+"/"+name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bytes": len(data)})
}

// handleMakeDir creates a directory inside a container (?path = the new dir).
func (s *Server) handleMakeDir(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	p := r.URL.Query().Get("path")
	if p == "" || p == "/" {
		writeErr(w, http.StatusBadRequest, "a non-root path is required")
		return
	}
	if err := s.docker.MakeDir(r.Context(), hostID, id, p); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "container.file.mkdir", id, p)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleExtractFile extracts an uploaded archive (.zip/.tar/.tar.gz) into a
// container directory. ?path is the destination, ?name the archive file name
// (its extension picks the format); the body is the raw archive.
func (s *Server) handleExtractFile(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	destDir := r.URL.Query().Get("path")
	name := r.URL.Query().Get("name")
	if destDir == "" || name == "" {
		writeErr(w, http.StatusBadRequest, "path and name are required")
		return
	}
	// Bound the streamed tar/tar.gz body, like the upload endpoints' 4 GiB guard.
	body := http.MaxBytesReader(w, r.Body, 1<<32)
	if err := s.docker.UploadExtract(r.Context(), hostID, id, destDir, name, body); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "container.cp.extract", id, destDir+"/"+name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	p := r.URL.Query().Get("path")
	if p == "" || p == "/" {
		writeErr(w, http.StatusBadRequest, "a non-root path is required")
		return
	}
	if err := s.docker.DeletePath(r.Context(), hostID, id, p); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "container.file.delete", id, p)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// normPath returns a clean absolute path, defaulting to "/".
func normPath(p string) string {
	if p == "" {
		return "/"
	}
	return path.Clean("/" + p)
}

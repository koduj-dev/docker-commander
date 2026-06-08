package api

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"path"

	"github.com/go-chi/chi/v5"
)

// Volume file browser: a named volume has no path reachable through the Docker
// API, so the docker layer mounts it in a throwaway helper container and reuses
// the in-container file ops. These handlers mirror the container file browser.

func (s *Server) handleListVolumeFiles(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	name := chi.URLParam(r, "name")
	p := r.URL.Query().Get("path")
	entries, err := s.docker.VolumeListPath(r.Context(), hostID, name, p)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": normPath(p), "entries": entries})
}

func (s *Server) handleDownloadVolumeFile(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	name := chi.URLParam(r, "name")
	p := r.URL.Query().Get("path")
	if p == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	rc, stat, err := s.docker.VolumeCopyFrom(r.Context(), hostID, name, p)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	defer rc.Close()
	s.audit(r, "volume.cp.download", name, p)

	if stat.Mode.IsDir() {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Content-Disposition", `attachment; filename="`+stat.Name+`.tar"`)
		_, _ = io.Copy(w, rc)
		return
	}
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

func (s *Server) handleUploadVolumeFile(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	name := chi.URLParam(r, "name")
	destDir := r.URL.Query().Get("path")
	fname := path.Base(r.URL.Query().Get("name"))
	if destDir == "" || fname == "" || fname == "." || fname == "/" {
		writeErr(w, http.StatusBadRequest, "path and name are required")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<32)) // 4 GiB guard
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body failed")
		return
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: fname, Mode: 0o644, Size: int64(len(data))}); err != nil {
		writeErr(w, http.StatusInternalServerError, "tar error")
		return
	}
	if _, err := tw.Write(data); err != nil {
		writeErr(w, http.StatusInternalServerError, "tar error")
		return
	}
	tw.Close()

	if err := s.docker.VolumeCopyTo(r.Context(), hostID, name, destDir, &buf); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "volume.cp.upload", name, destDir+"/"+fname)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bytes": len(data)})
}

func (s *Server) handleMakeVolumeDir(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	name := chi.URLParam(r, "name")
	p := r.URL.Query().Get("path")
	if p == "" || p == "/" {
		writeErr(w, http.StatusBadRequest, "a non-root path is required")
		return
	}
	if err := s.docker.VolumeMakeDir(r.Context(), hostID, name, p); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "volume.file.mkdir", name, p)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleExtractVolumeFile(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	name := chi.URLParam(r, "name")
	destDir := r.URL.Query().Get("path")
	fname := r.URL.Query().Get("name")
	if destDir == "" || fname == "" {
		writeErr(w, http.StatusBadRequest, "path and name are required")
		return
	}
	// Bound the streamed tar/tar.gz body, like the upload endpoints' 4 GiB guard.
	body := http.MaxBytesReader(w, r.Body, 1<<32)
	if err := s.docker.VolumeUploadExtract(r.Context(), hostID, name, destDir, fname, body); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "volume.cp.extract", name, destDir+"/"+fname)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteVolumeFile(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	name := chi.URLParam(r, "name")
	p := r.URL.Query().Get("path")
	if p == "" || p == "/" {
		writeErr(w, http.StatusBadRequest, "a non-root path is required")
		return
	}
	if err := s.docker.VolumeDeletePath(r.Context(), hostID, name, p); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "volume.file.delete", name, p)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleCloseVolumeBrowser removes the volume's helper container (called when
// the user closes the browser).
func (s *Server) handleCloseVolumeBrowser(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	s.docker.CloseVolumeBrowser(r.Context(), hostID, chi.URLParam(r, "name"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

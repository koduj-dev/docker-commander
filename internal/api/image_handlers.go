package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/coder/websocket"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	imgs, err := s.docker.ListImages(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, imgs)
}

// handleSearchImages backs image-name autocomplete: a Docker Hub repository
// search proxied through the selected host's daemon. Best-effort — any daemon/
// network error yields an empty list so the editor still offers local images.
func (s *Server) handleSearchImages(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	res, err := s.docker.SearchImages(r.Context(), hostID, r.URL.Query().Get("q"), 25)
	if err != nil {
		writeJSON(w, http.StatusOK, []docker.ImageSearchResult{})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleImageTags lists Docker Hub tags for a repository (?repo=), for tag
// autocomplete. Best-effort: errors and non-Hub repos return an empty list.
func (s *Server) handleImageTags(w http.ResponseWriter, r *http.Request) {
	tags, err := s.docker.ImageTags(r.Context(), r.URL.Query().Get("repo"))
	if err != nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	writeJSON(w, http.StatusOK, tags)
}

func (s *Server) handleRemoveImage(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		writeErr(w, http.StatusBadRequest, "ref is required")
		return
	}
	force := r.URL.Query().Get("force") == "1"
	changed, err := s.docker.RemoveImage(r.Context(), hostID, ref, force)
	if err != nil {
		// Surface the daemon's "image is in use" conflict so the UI can offer
		// a force retry instead of treating it as a hard failure.
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "image.remove", ref, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed})
}

func (s *Server) handlePruneImages(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	res, err := s.docker.PruneImages(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	s.audit(r, "image.prune", "", "")
	writeJSON(w, http.StatusOK, res)
}

// handlePullImage upgrades to a WebSocket and streams pull progress as JSON
// text frames (server → browser only), mirroring the exec bridge. The image
// reference comes from the "ref" query parameter.
func (s *Server) handlePullImage(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		writeErr(w, http.StatusBadRequest, "ref is required")
		return
	}

	opts := &websocket.AcceptOptions{}
	if s.cfg.Dev {
		opts.InsecureSkipVerify = true
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	send := func(p any) {
		if b, err := json.Marshal(p); err == nil {
			_ = conn.Write(ctx, websocket.MessageText, b)
		}
	}

	s.audit(r, "image.pull", ref, "")
	err = s.docker.PullImage(ctx, hostID, ref, func(p docker.PullProgress) {
		send(p)
	})
	if err != nil {
		send(map[string]any{"error": err.Error()})
		return
	}
	send(map[string]any{"done": true, "status": "Pull complete"})
}

// handlePushImage streams push progress over a WebSocket, mirroring pull.
func (s *Server) handlePushImage(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		writeErr(w, http.StatusBadRequest, "ref is required")
		return
	}

	opts := &websocket.AcceptOptions{}
	if s.cfg.Dev {
		opts.InsecureSkipVerify = true
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	send := func(p any) {
		if b, err := json.Marshal(p); err == nil {
			_ = conn.Write(ctx, websocket.MessageText, b)
		}
	}
	s.audit(r, "image.push", ref, "")
	if err := s.docker.PushImage(ctx, hostID, ref, func(p docker.PullProgress) { send(p) }); err != nil {
		send(map[string]any{"error": err.Error()})
		return
	}
	send(map[string]any{"done": true, "status": "Push complete"})
}

// tagBody retags a local image under a new (often registry-qualified) name.
type tagBody struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

func (s *Server) handleTagImage(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	var b tagBody
	if err := decodeJSON(r, &b); err != nil || b.Source == "" || b.Target == "" {
		writeErr(w, http.StatusBadRequest, "source and target are required")
		return
	}
	if err := s.docker.TagImage(r.Context(), hostID, b.Source, b.Target); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "image.tag", b.Source, b.Target)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleSaveImage streams one or more images as a downloadable tar archive
// (docker save format). Multiple ?ref= params bundle several images.
func (s *Server) handleSaveImage(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	refs := r.URL.Query()["ref"]
	if len(refs) == 0 {
		writeErr(w, http.StatusBadRequest, "ref is required")
		return
	}
	rc, err := s.docker.SaveImage(r.Context(), hostID, refs)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	defer rc.Close()
	s.audit(r, "image.save", strings.Join(refs, ","), "")
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", `attachment; filename="images.tar"`)
	_, _ = io.Copy(w, rc)
}

// handleLoadImage loads images from an uploaded tar (docker save format).
func (s *Server) handleLoadImage(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	out, err := s.docker.LoadImage(r.Context(), hostID, r.Body)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "image.load", "", "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
}

// handleImportImage imports a filesystem tarball as a new image tagged ?ref=.
func (s *Server) handleImportImage(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		writeErr(w, http.StatusBadRequest, "ref is required")
		return
	}
	out, err := s.docker.ImportImage(r.Context(), hostID, r.Body, ref)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "image.import", ref, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
}

// handleBuildImage builds an image from an uploaded tar context (the request
// body) and streams the daemon's build output back as newline-delimited JSON.
// Build params come from query string: tag (repeatable), dockerfile, nocache,
// and buildarg (repeatable, "KEY=VALUE").
func (s *Server) handleBuildImage(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	q := r.URL.Query()
	opts := docker.BuildOptions{
		Tags:       q["tag"],
		Dockerfile: q.Get("dockerfile"),
		NoCache:    q.Get("nocache") == "1",
		BuildArgs:  map[string]string{},
	}
	for _, kv := range q["buildarg"] {
		if i := strings.IndexByte(kv, '='); i > 0 {
			opts.BuildArgs[kv[:i]] = kv[i+1:]
		}
	}

	// Stream NDJSON as the build proceeds. Flush each line so the browser sees
	// progress live rather than buffered to the end.
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	send := func(v any) {
		_ = enc.Encode(v)
		if flusher != nil {
			flusher.Flush()
		}
	}

	s.audit(r, "image.build", strings.Join(opts.Tags, ","), "")
	err = s.docker.BuildImage(r.Context(), hostID, r.Body, opts, func(m docker.BuildMessage) { send(m) })
	if err != nil {
		send(map[string]any{"error": err.Error()})
		return
	}
	send(map[string]any{"done": true})
}

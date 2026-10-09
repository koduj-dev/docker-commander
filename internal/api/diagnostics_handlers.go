package api

import "net/http"

// handleRunDiagnostics runs the read-only diagnostic check battery against
// the named host and returns the report. It is a POST (not GET) because — at
// least once the host-introspection checks land — this actively runs commands
// on the target host, the same risk class the permissions middleware already
// write-gates for /stats/ports.
func (s *Server) handleRunDiagnostics(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	report, err := s.docker.RunDiagnostics(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	s.audit(r, "diagnostics.run", "", "")
	writeJSON(w, http.StatusOK, report)
}

package api

import (
	"net/http"
	"net/http/pprof"
)

// PProfHandler returns an http.Handler exposing Go's net/http/pprof endpoints
// under /debug/pprof.
//
// It carries NO authentication or IP filtering by design: the caller MUST serve
// it only on a loopback-bound listener (see cmd/dockercmd's pprof server). That
// is deliberate — gating by client IP on the *main* router would be unsafe,
// because the app runs behind chi's RealIP middleware, which rewrites
// r.RemoteAddr from the (spoofable) X-Forwarded-For / X-Real-IP headers. A
// physically loopback-only listener can't be reached off-box regardless of
// headers, so it's the correct boundary for endpoints that leak goroutine
// stacks / heap layout and can stall the process (/profile).
func PProfHandler() http.Handler {
	mux := http.NewServeMux()
	// pprof.Index serves the index AND the named profiles (heap, goroutine,
	// allocs, …) by routing on the path suffix; the four below have dedicated
	// handlers and take precedence as more-specific patterns.
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

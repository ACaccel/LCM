package app

import (
	"net/http"
	"net/http/pprof"
	"time"
)

// pprofHandler serves the runtime profiles. It is mounted on its own
// loopback listener, never on the API, and only when LCM_PPROF_LISTEN is set.
func pprofHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

func newPprofServer() *http.Server {
	return &http.Server{
		Handler:           pprofHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		// CPU profiles and traces stream for as long as they are asked to.
		WriteTimeout: 0,
	}
}

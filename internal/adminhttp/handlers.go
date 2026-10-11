package adminhttp

import (
	"embed"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Muti-Metroo/Mutiauk/internal/api"
)

//go:embed index.html
var indexHTML []byte

// staticFS holds the vendored Bootstrap 5.3 assets (CSS + JS bundle).
// They are embedded so the dashboard works fully offline, with no CDN
// or runtime network dependency. The embedded paths keep the "static/"
// prefix, so a request for /static/bootstrap.min.css resolves directly.
//
//go:embed static
var staticFS embed.FS

// routes builds the request multiplexer. Method+path patterns require
// Go 1.22+ ServeMux semantics (the project builds on go1.25.5). Every
// route is wrapped in the Host-header / token / Content-Type guards.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/agent", s.handleAgent)
	mux.HandleFunc("GET /api/routes", s.handleRoutes)
	mux.HandleFunc("GET /api/route-events", s.handleRouteEvents)
	mux.HandleFunc("GET /api/hosts", s.handleHosts)
	mux.HandleFunc("GET /api/expose", s.handleExposeList)
	mux.HandleFunc("POST /api/expose", s.handleExposeAdd)
	mux.HandleFunc("DELETE /api/expose/{key}", s.handleExposeRemove)
	return s.secure(mux)
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.state.GetStatus())
}

func (s *Server) handleRoutes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, api.RouteListResult{
		Routes:  s.state.GetRoutes(),
		Refused: s.state.GetRefusedRoutes(),
	})
}

func (s *Server) handleRouteEvents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.state.GetRouteEvents())
}

func (s *Server) handleHosts(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.state.GetMeshHosts())
}

func (s *Server) handleExposeList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.state.GetExposeStatus())
}

// AgentStatus is the consolidated view of this Mutiauk's connection
// to the upstream Muti Metroo agent: the SOCKS5 data-plane endpoint, the
// autoroutes poller, and the expose control stream. It lets the
// dashboard render "connection info and status to the Muti Metroo agent" in
// a single request.
type AgentStatus struct {
	SOCKS5Server  string               `json:"socks5_server"`
	SOCKS5Status  string               `json:"socks5_status"`
	AutoRoutes    api.AutoRoutesStatus `json:"autoroutes"`
	ExposeEnabled bool                 `json:"expose_enabled"`
	ExposeConn    bool                 `json:"expose_connected"`
	ExposeSince   time.Time            `json:"expose_connected_since,omitempty"`
	ExposeError   string               `json:"expose_error,omitempty"`
}

func (s *Server) handleAgent(w http.ResponseWriter, _ *http.Request) {
	st := s.state.GetStatus()
	ex := s.state.GetExposeStatus()
	agent := AgentStatus{
		SOCKS5Server:  st.SOCKS5Server,
		SOCKS5Status:  st.SOCKS5Status,
		AutoRoutes:    st.AutoRoutes,
		ExposeEnabled: ex.Enabled,
		ExposeConn:    ex.Connected,
		ExposeSince:   ex.ConnectedSince,
		ExposeError:   ex.LastError,
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleExposeAdd(w http.ResponseWriter, r *http.Request) {
	var p api.ExposeAddParams
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil {
		writeExposeError(w, "invalid request body: "+err.Error())
		return
	}
	// AddExpose errors are operator-facing (bad path, duplicate key, ...),
	// so report them in the body rather than as a 5xx.
	writeExposeResult(w, s.state.AddExpose(p.Key, p.Type, p.Path, p.Target, p.Comment))
}

func (s *Server) handleExposeRemove(w http.ResponseWriter, r *http.Request) {
	writeExposeResult(w, s.state.RemoveExpose(r.PathValue("key")))
}

// writeExposeResult renders an api.ExposeMutationResult: success when err
// is nil, otherwise a 400 carrying the operator-facing error message.
func writeExposeResult(w http.ResponseWriter, err error) {
	if err != nil {
		writeExposeError(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ExposeMutationResult{Success: true})
}

// writeExposeError writes a 400 mutation failure with the given message.
func writeExposeError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, api.ExposeMutationResult{Success: false, Message: msg})
}

// writeJSON encodes v as the response body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

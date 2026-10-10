package adminhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/postalsys/mutiauk/internal/api"
	"github.com/postalsys/mutiauk/internal/config"
)

func testCfg() config.AdminConfig {
	enabled := true
	return config.AdminConfig{Enabled: &enabled, Listen: "127.0.0.1:0"}
}

// fakeState is a minimal api.StateProvider for exercising the HTTP
// handlers without a running daemon. It records mutating calls.
type fakeState struct {
	status      *api.StatusResult
	routes      []api.RouteInfo
	refused     []api.RefusedRoute
	expose      *api.ExposeStatusResult
	hosts       *api.HostListResult
	routeEvents []api.RouteEvent
	addCalls    []api.ExposeAddParams
	removed     []string
	addErr      error
	removeErr   error
}

func (f *fakeState) GetStatus() *api.StatusResult         { return f.status }
func (f *fakeState) GetRoutes() []api.RouteInfo           { return f.routes }
func (f *fakeState) GetRefusedRoutes() []api.RefusedRoute { return f.refused }
func (f *fakeState) GetRouteEvents() *api.RouteEventsResult {
	return &api.RouteEventsResult{Events: f.routeEvents}
}
func (f *fakeState) GetExposeStatus() *api.ExposeStatusResult { return f.expose }
func (f *fakeState) GetMeshHosts() *api.HostListResult        { return f.hosts }
func (f *fakeState) GetDNSStatus() *api.DNSStatusResult       { return &api.DNSStatusResult{} }
func (f *fakeState) GetConfigPath() string                    { return "/etc/mutiauk.yaml" }
func (f *fakeState) AddRoute(string, string, bool) error      { return nil }
func (f *fakeState) RemoveRoute(string, bool) error           { return nil }
func (f *fakeState) TraceRoute(string) (*api.RouteTraceResult, error) {
	return &api.RouteTraceResult{}, nil
}
func (f *fakeState) ReloadConfig() error { return nil }
func (f *fakeState) RequestStop() error  { return nil }

func (f *fakeState) AddExpose(key, ftype, path, target, comment string) error {
	f.addCalls = append(f.addCalls, api.ExposeAddParams{Key: key, Type: ftype, Path: path, Target: target, Comment: comment})
	return f.addErr
}

func (f *fakeState) RemoveExpose(key string) error {
	f.removed = append(f.removed, key)
	return f.removeErr
}

func newTestServer() (*fakeState, http.Handler) {
	st := &fakeState{
		status: &api.StatusResult{Running: true, PID: 4242, TUNName: "tun0", SOCKS5Server: "127.0.0.1:1080", SOCKS5Status: "connected"},
		routes: []api.RouteInfo{{Destination: "10.0.0.0/8", Interface: "tun0", Source: "auto", Enabled: true}},
		expose: &api.ExposeStatusResult{
			Enabled: true, Connected: true, MaxStreamsPerKey: 256,
			Forwards: []api.ExposeForwardStatus{{Key: "docs", Type: "folder", Source: "config", Path: "/srv/docs", Registered: true}},
		},
		hosts: &api.HostListResult{Enabled: true},
		routeEvents: []api.RouteEvent{
			{Seq: 1, Action: "added", Destination: "1.2.3.4/32", Source: "auto", Comment: "autoroute:abc"},
		},
	}
	srv := NewServer(st, testCfg(), nil)
	return st, srv.routes()
}

func TestRouteEventsEndpoint(t *testing.T) {
	_, h := newTestServer()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/route-events", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got api.RouteEventsResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Events) != 1 || got.Events[0].Action != "added" || got.Events[0].Destination != "1.2.3.4/32" {
		t.Fatalf("unexpected events: %+v", got.Events)
	}
}

func TestStatusEndpoint(t *testing.T) {
	_, h := newTestServer()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got api.StatusResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.PID != 4242 || !got.Running {
		t.Fatalf("unexpected status: %+v", got)
	}
}

func TestRoutesEndpoint(t *testing.T) {
	_, h := newTestServer()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/routes", nil))
	var got api.RouteListResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Routes) != 1 || got.Routes[0].Destination != "10.0.0.0/8" {
		t.Fatalf("unexpected routes: %+v", got.Routes)
	}
}

func TestRoutesEndpoint_Refused(t *testing.T) {
	st, h := newTestServer()
	st.refused = []api.RefusedRoute{{
		Destination: "1.2.3.0/24",
		Source:      "auto",
		Reason:      "covers the SOCKS5 server 1.2.3.4; routing it through the tunnel would loop",
	}}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/routes", nil))
	var got api.RouteListResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Refused) != 1 || got.Refused[0].Destination != "1.2.3.0/24" {
		t.Fatalf("expected refused route surfaced on /api/routes, got %+v", got.Refused)
	}
}

func TestAgentEndpoint(t *testing.T) {
	_, h := newTestServer()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent", nil))
	var got AgentStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SOCKS5Server != "127.0.0.1:1080" || !got.ExposeConn || !got.ExposeEnabled {
		t.Fatalf("unexpected agent status: %+v", got)
	}
}

func TestExposeAdd(t *testing.T) {
	st, h := newTestServer()
	body := `{"key":"web","type":"tcp","target":"127.0.0.1:8080","comment":"x"}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/expose", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("add status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var res api.ExposeMutationResult
	json.Unmarshal(rec.Body.Bytes(), &res)
	if !res.Success {
		t.Fatalf("expected success, got %+v", res)
	}
	if len(st.addCalls) != 1 || st.addCalls[0].Key != "web" || st.addCalls[0].Target != "127.0.0.1:8080" {
		t.Fatalf("AddExpose not called as expected: %+v", st.addCalls)
	}
}

func TestExposeAddBadBody(t *testing.T) {
	_, h := newTestServer()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/expose", bytes.NewReader([]byte("not json"))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestExposeRemove(t *testing.T) {
	st, h := newTestServer()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/expose/docs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("remove status = %d", rec.Code)
	}
	if len(st.removed) != 1 || st.removed[0] != "docs" {
		t.Fatalf("RemoveExpose not called as expected: %+v", st.removed)
	}
}

func TestIndexServesHTML(t *testing.T) {
	_, h := newTestServer()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("unexpected content-type %q", ct)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("Mutiauk")) {
		t.Fatalf("index body missing title")
	}
	// The dashboard must load Bootstrap from the local /static/ mount,
	// never a CDN, so it works offline.
	if !bytes.Contains(rec.Body.Bytes(), []byte("/static/bootstrap.min.css")) {
		t.Fatalf("index does not reference vendored /static/bootstrap.min.css")
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("cdn.")) {
		t.Fatalf("index references a CDN; assets must be vendored")
	}
}

func TestStaticAssetsServed(t *testing.T) {
	_, h := newTestServer()
	for _, tc := range []struct{ path, wantCT string }{
		{"/static/bootstrap.min.css", "text/css"},
		{"/static/bootstrap.bundle.min.js", "javascript"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", tc.path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, tc.wantCT) {
			t.Errorf("%s: content-type %q, want substring %q", tc.path, ct, tc.wantCT)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s: empty body", tc.path)
		}
	}
}

func TestStartServesAndStops(t *testing.T) {
	st := &fakeState{status: &api.StatusResult{Running: true, PID: 1}, expose: &api.ExposeStatusResult{}}
	srv := NewServer(st, testCfg(), nil)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	addr := srv.listeners[0].Addr().String()
	resp, err := http.Get("http://" + addr + "/api/status")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestMethodRouting(t *testing.T) {
	_, h := newTestServer()
	// POST to a GET-only route must not succeed.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/status", nil))
	if rec.Code == http.StatusOK {
		t.Fatalf("POST /api/status should not be allowed")
	}
}

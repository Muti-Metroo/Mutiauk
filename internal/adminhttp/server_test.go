package adminhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/postalsys/mutiauk/internal/api"
	"github.com/postalsys/mutiauk/internal/config"
)

const testToken = "test-admin-token"

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
	srv := NewServer(st, testCfg(), testToken, nil)
	return st, srv.routes()
}

// authedReq builds a request that passes all request guards: loopback
// Host header, valid bearer token, and a JSON Content-Type when a body
// is present. Tests for the guards themselves build requests by hand.
func authedReq(method, path, body string) *http.Request {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Host = "127.0.0.1"
	req.Header.Set("Authorization", "Bearer "+testToken)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// authedDelete builds an authorized DELETE; the admin API enforces
// Content-Type on POST/DELETE even when no body is sent.
func authedDelete(path string) *http.Request {
	req := authedReq(http.MethodDelete, path, "")
	req.Header.Set("Content-Type", "application/json")
	return req
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRouteEventsEndpoint(t *testing.T) {
	_, h := newTestServer()
	rec := serve(h, authedReq(http.MethodGet, "/api/route-events", ""))
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
	rec := serve(h, authedReq(http.MethodGet, "/api/status", ""))
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
	rec := serve(h, authedReq(http.MethodGet, "/api/routes", ""))
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
	rec := serve(h, authedReq(http.MethodGet, "/api/routes", ""))
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
	rec := serve(h, authedReq(http.MethodGet, "/api/agent", ""))
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
	rec := serve(h, authedReq(http.MethodPost, "/api/expose", body))
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
	rec := serve(h, authedReq(http.MethodPost, "/api/expose", "not json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestExposeRemove(t *testing.T) {
	st, h := newTestServer()
	rec := serve(h, authedDelete("/api/expose/docs"))
	if rec.Code != http.StatusOK {
		t.Fatalf("remove status = %d", rec.Code)
	}
	if len(st.removed) != 1 || st.removed[0] != "docs" {
		t.Fatalf("RemoveExpose not called as expected: %+v", st.removed)
	}
}

func TestIndexServesHTML(t *testing.T) {
	_, h := newTestServer()
	rec := serve(h, authedReq(http.MethodGet, "/", ""))
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
		rec := serve(h, authedReq(http.MethodGet, tc.path, ""))
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

// TestAuthRequired verifies that every endpoint - API, dashboard, and
// static assets - rejects requests without a token.
func TestAuthRequired(t *testing.T) {
	st, h := newTestServer()
	for _, path := range []string{
		"/", "/static/bootstrap.min.css",
		"/api/status", "/api/agent", "/api/routes", "/api/route-events",
		"/api/hosts", "/api/expose",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "127.0.0.1"
		if rec := serve(h, req); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without token: status = %d, want 401", path, rec.Code)
		}
	}

	// The mutating endpoints must be unreachable too, and must not have
	// touched daemon state.
	post := httptest.NewRequest(http.MethodPost, "/api/expose", strings.NewReader(`{"key":"x","type":"tcp","target":"127.0.0.1:22"}`))
	post.Host = "127.0.0.1"
	post.Header.Set("Content-Type", "application/json")
	if rec := serve(h, post); rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /api/expose without token: status = %d, want 401", rec.Code)
	}
	del := httptest.NewRequest(http.MethodDelete, "/api/expose/docs", nil)
	del.Host = "127.0.0.1"
	del.Header.Set("Content-Type", "application/json")
	if rec := serve(h, del); rec.Code != http.StatusUnauthorized {
		t.Errorf("DELETE /api/expose/docs without token: status = %d, want 401", rec.Code)
	}
	if len(st.addCalls) != 0 || len(st.removed) != 0 {
		t.Fatalf("unauthenticated requests mutated state: add=%v remove=%v", st.addCalls, st.removed)
	}
}

// TestAuthWrongToken rejects a well-formed but incorrect bearer token and
// confirms the mutating endpoints do not fire.
func TestAuthWrongToken(t *testing.T) {
	st, h := newTestServer()
	req := authedReq(http.MethodPost, "/api/expose", `{"key":"x","type":"tcp","target":"127.0.0.1:22"}`)
	req.Header.Set("Authorization", "Bearer wrong-token")
	if rec := serve(h, req); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d, want 401", rec.Code)
	}
	if len(st.addCalls) != 0 {
		t.Fatalf("wrong-token request mutated state: %v", st.addCalls)
	}
	if got := serve(h, req).Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("401 response missing WWW-Authenticate: Bearer, got %q", got)
	}
}

// TestHostHeaderLoopbackOnly rejects non-loopback Host headers (DNS
// rebinding defense) even when the token is valid.
func TestHostHeaderLoopbackOnly(t *testing.T) {
	_, h := newTestServer()

	for _, host := range []string{"example.com", "192.168.1.10", "10.0.0.5:48088", "evil.localhost.evil.com"} {
		req := authedReq(http.MethodGet, "/api/status", "")
		req.Host = host
		if rec := serve(h, req); rec.Code != http.StatusForbidden {
			t.Errorf("Host %q: status = %d, want 403", host, rec.Code)
		}
	}

	for _, host := range []string{"127.0.0.1", "127.0.0.1:48088", "127.5.5.5:48088", "localhost", "localhost:48088", "[::1]", "[::1]:48088"} {
		req := authedReq(http.MethodGet, "/api/status", "")
		req.Host = host
		if rec := serve(h, req); rec.Code != http.StatusOK {
			t.Errorf("Host %q: status = %d, want 200", host, rec.Code)
		}
	}
}

// TestContentTypeEnforced requires application/json on the mutating
// POST/DELETE endpoints: cross-origin form posts cannot set it, which
// closes the CSRF vector independent of the token check.
func TestContentTypeEnforced(t *testing.T) {
	st, h := newTestServer()

	// A cross-origin form POST with enctype=text/plain, the classic CSRF shape.
	req := authedReq(http.MethodPost, "/api/expose", `{"key":"x","type":"tcp","target":"127.0.0.1:22"}`)
	req.Header.Set("Content-Type", "text/plain")
	if rec := serve(h, req); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("POST text/plain: status = %d, want 415", rec.Code)
	}

	// A charset parameter stays acceptable.
	req = authedReq(http.MethodPost, "/api/expose", `{"key":"x","type":"tcp","target":"127.0.0.1:22"}`)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if rec := serve(h, req); rec.Code != http.StatusOK {
		t.Errorf("POST application/json; charset=utf-8: status = %d, want 200", rec.Code)
	}

	// DELETE is checked too, even without a body.
	req = authedReq(http.MethodDelete, "/api/expose/docs", "")
	if rec := serve(h, req); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("DELETE without Content-Type: status = %d, want 415", rec.Code)
	}
	if rec := serve(h, authedDelete("/api/expose/docs")); rec.Code != http.StatusOK {
		t.Errorf("DELETE with application/json: status = %d, want 200", rec.Code)
	}

	if len(st.addCalls) != 1 || len(st.removed) != 1 {
		t.Fatalf("expected exactly one accepted add and remove, got add=%v remove=%v", st.addCalls, st.removed)
	}
}

// TestTokenQueryBootstrap covers the browser login flow: a GET with a
// valid ?token= parameter sets a SameSite=Strict HttpOnly cookie and
// redirects to the clean URL; subsequent cookie-carrying requests pass.
func TestTokenQueryBootstrap(t *testing.T) {
	_, h := newTestServer()

	req := httptest.NewRequest(http.MethodGet, "/?token="+testToken, nil)
	req.Host = "127.0.0.1"
	rec := serve(h, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("bootstrap: status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("bootstrap: Location = %q, want /", loc)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == tokenCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("bootstrap: no %s cookie set", tokenCookieName)
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie flags: HttpOnly=%v SameSite=%v, want HttpOnly + Strict", cookie.HttpOnly, cookie.SameSite)
	}

	// The cookie alone must authenticate subsequent requests.
	req = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Host = "127.0.0.1"
	req.AddCookie(cookie)
	if rec := serve(h, req); rec.Code != http.StatusOK {
		t.Errorf("cookie-authed GET /api/status: status = %d, want 200", rec.Code)
	}

	// A wrong token in the query string is not a login.
	req = httptest.NewRequest(http.MethodGet, "/?token=wrong", nil)
	req.Host = "127.0.0.1"
	if rec := serve(h, req); rec.Code != http.StatusUnauthorized {
		t.Errorf("bootstrap with wrong token: status = %d, want 401", rec.Code)
	}
}

func TestStartServesAndStops(t *testing.T) {
	st := &fakeState{status: &api.StatusResult{Running: true, PID: 1}, expose: &api.ExposeStatusResult{}}
	srv := NewServer(st, testCfg(), testToken, nil)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	addr := srv.listeners[0].Addr().String()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/api/status", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
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
	req := authedReq(http.MethodPost, "/api/status", `{}`)
	if rec := serve(h, req); rec.Code == http.StatusOK {
		t.Fatalf("POST /api/status should not be allowed")
	}
}

// TestLoadOrCreateToken exercises the token file lifecycle: first call
// generates and persists a 0600 token, later calls read it back.
func TestLoadOrCreateToken(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "mutiauk.yaml")

	tok, err := LoadOrCreateToken(cfgPath)
	if err != nil {
		t.Fatalf("LoadOrCreateToken: %v", err)
	}
	if len(tok) != 64 {
		t.Errorf("token length = %d, want 64 hex chars", len(tok))
	}

	path := TokenPath(cfgPath)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("token file not created: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file mode = %o, want 600", perm)
	}

	// Second call must return the same token without rewriting the file.
	tok2, err := LoadOrCreateToken(cfgPath)
	if err != nil {
		t.Fatalf("LoadOrCreateToken (second): %v", err)
	}
	if tok2 != tok {
		t.Errorf("second call returned a different token")
	}

	got, err := ReadToken(cfgPath)
	if err != nil {
		t.Fatalf("ReadToken: %v", err)
	}
	if got != tok {
		t.Errorf("ReadToken = %q, want %q", got, tok)
	}

	if _, err := ReadToken(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("ReadToken on a missing file should fail")
	}
}

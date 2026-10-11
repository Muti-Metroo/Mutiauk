package adminhttp

import (
	"crypto/subtle"
	"mime"
	"net"
	"net/http"
	"strings"
)

// tokenCookieName carries the admin token for browser sessions after the
// one-time ?token= bootstrap redirect.
const tokenCookieName = "mutiauk_admin_token"

// secure wraps the route mux with the admin interface's request guards,
// in order: loopback Host header (defeats DNS rebinding), bearer-token
// authentication on every endpoint including the dashboard (defeats
// unauthenticated local access and CSRF), and Content-Type enforcement
// on POST/DELETE (defeats cross-origin form posts, which cannot send
// application/json).
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "admin interface only answers requests with a loopback Host header",
			})
			return
		}

		if !s.authorized(r) {
			// Browser bootstrap: a GET carrying the token as a query
			// parameter (as printed by `mutiauk admin`) sets a
			// SameSite=Strict HttpOnly cookie and redirects to the clean
			// URL, so the token does not linger in the address bar or in
			// Referer headers.
			if r.Method == http.MethodGet && s.tokenValid(r.URL.Query().Get("token")) {
				http.SetCookie(w, &http.Cookie{
					Name:     tokenCookieName,
					Value:    s.token,
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteStrictMode,
				})
				target := r.URL.Path
				q := r.URL.Query()
				q.Del("token")
				if len(q) > 0 {
					target += "?" + q.Encode()
				}
				http.Redirect(w, r, target, http.StatusSeeOther)
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="mutiauk-admin"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "admin token required: send Authorization: Bearer <token> (token file: " + TokenFileName + " next to the config, or run `mutiauk admin`)",
			})
			return
		}

		if r.Method == http.MethodPost || r.Method == http.MethodDelete {
			if !isJSONContentType(r.Header.Get("Content-Type")) {
				writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{
					"error": "Content-Type must be application/json",
				})
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// authorized reports whether the request carries the admin token, either
// as an Authorization: Bearer header or as the session cookie set by the
// ?token= bootstrap.
func (s *Server) authorized(r *http.Request) bool {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		if s.tokenValid(strings.TrimPrefix(auth, "Bearer ")) {
			return true
		}
	}
	if c, err := r.Cookie(tokenCookieName); err == nil && s.tokenValid(c.Value) {
		return true
	}
	return false
}

// tokenValid compares in constant time; an unconfigured (empty) server
// token never validates.
func (s *Server) tokenValid(tok string) bool {
	return s.token != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) == 1
}

// loopbackHost reports whether the Host header names a loopback address:
// a loopback IP (optionally with a port) or "localhost". Anything else
// (a LAN address, a hostname resolving via DNS rebinding) is rejected.
func loopbackHost(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// isJSONContentType reports whether the Content-Type header value parses
// as application/json (parameters such as charset are allowed).
func isJSONContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && mt == "application/json"
}

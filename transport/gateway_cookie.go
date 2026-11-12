package transport

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mejango/blossm-toolbox-go/procedures/command"
	"github.com/mejango/blossm-toolbox-go/security/authn"
	"github.com/mejango/blossm-toolbox-go/stores/event"
)

type CookiePolicy struct {
	// Type is the authentication cookie type; Types allow token/revoke output.
	// Names are always __Host-<type>, Secure, Path=/, with no Domain attribute.
	Type    string
	Types   []string
	Origins []string
}
type CookieToken struct {
	Type    string `json:"type"`
	Value   string `json:"value"`
	Expires int64  `json:"expires"`
}

func (p CookiePolicy) validate() error {
	if !cookieType(p.Type) || len(p.Types) == 0 || len(p.Origins) == 0 {
		return errors.New("cookie type, output types and exact HTTPS origins required")
	}
	seen := map[string]bool{}
	for _, v := range p.Types {
		if !cookieType(v) || seen[v] {
			return errors.New("invalid/duplicate cookie type")
		}
		seen[v] = true
	}
	if !seen[p.Type] {
		return errors.New("authentication cookie must be an allowed type")
	}
	for _, origin := range p.Origins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("cookie origins must be exact HTTPS origins")
		}
	}
	return nil
}
func cookieType(v string) bool {
	if v == "" || v == "csrf" {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func oneCookie(r *http.Request, name string) (string, error) {
	value := ""
	count := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == name {
			value = cookie.Value
			count++
		}
	}
	if count > 1 || (count == 1 && value == "") {
		return "", authn.ErrUnauthorized
	}
	return value, nil
}
func unsafeGatewayMethod(method string) bool {
	return method != "GET" && method != "HEAD" && method != "OPTIONS"
}
func (p CookiePolicy) checkOrigin(r *http.Request, required bool) error {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 && !required {
		return nil
	}
	if len(origins) != 1 {
		return authn.ErrForbidden
	}
	for _, allowed := range p.Origins {
		if origins[0] == allowed {
			return nil
		}
	}
	return authn.ErrForbidden
}
func (p CookiePolicy) credential(r *http.Request) (string, error) {
	if err := p.validate(); err != nil {
		return "", err
	}
	value, err := oneCookie(r, "__Host-"+p.Type)
	if err != nil || value == "" {
		return value, err
	}
	if err = p.checkOrigin(r, unsafeGatewayMethod(r.Method)); err != nil {
		return "", err
	}
	if unsafeGatewayMethod(r.Method) {
		csrf, err := oneCookie(r, "__Host-csrf")
		if err != nil {
			return "", authn.ErrForbidden
		}
		values := r.Header.Values("X-CSRF-Token")
		if csrf == "" || len(values) != 1 || subtle.ConstantTimeCompare([]byte(csrf), []byte(values[0])) != 1 {
			return "", authn.ErrForbidden
		}
	}
	return value, nil
}

// SetCSRF issues a host-only, script-readable random double-submit token. Origin
// checks remain mandatory; do not disable them merely because CSRF values match.
func (p CookiePolicy) SetCSRF(w http.ResponseWriter) (string, error) {
	if err := p.validate(); err != nil {
		return "", err
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	http.SetCookie(w, &http.Cookie{Name: "__Host-csrf", Value: token, Path: "/", Secure: true, SameSite: http.SameSiteStrictMode})
	return token, nil
}
func (p CookiePolicy) apply(w http.ResponseWriter, out *command.Response) error {
	allowed := map[string]bool{}
	for _, kind := range p.Types {
		allowed[kind] = true
	}
	pending := map[string]*http.Cookie{}
	order := []string{}
	for _, raw := range out.Tokens {
		var token CookieToken
		if eventstore.Decode(raw, &token) != nil || !allowed[token.Type] || token.Value == "" || token.Expires <= time.Now().Unix() || token.Expires > 253402300799 {
			return errors.New("invalid cookie token response")
		}
		cookie := &http.Cookie{Name: "__Host-" + token.Type, Value: token.Value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: time.Unix(token.Expires, 0)}
		if cookie.Valid() != nil || len(cookie.String()) > 4096 {
			return errors.New("invalid cookie encoding")
		}
		if _, exists := pending[token.Type]; !exists {
			order = append(order, token.Type)
		}
		pending[token.Type] = cookie
	}
	for _, kind := range out.Revoke {
		if !allowed[kind] {
			return errors.New("unknown revoked cookie type")
		}
		if _, exists := pending[kind]; !exists {
			order = append(order, kind)
		}
		pending[kind] = &http.Cookie{Name: "__Host-" + kind, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)}
	}
	for _, kind := range order {
		http.SetCookie(w, pending[kind])
	}
	out.Tokens = nil
	out.Revoke = nil
	for k := range out.Headers {
		if strings.EqualFold(k, "Set-Cookie") {
			delete(out.Headers, k)
		}
	}
	return nil
}

type gatewayBuffer struct {
	header http.Header
	status int
	body   bytes.Buffer
	limit  int
	err    error
}

func (b *gatewayBuffer) Header() http.Header { return b.header }
func (b *gatewayBuffer) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}
func (b *gatewayBuffer) Write(data []byte) (int, error) {
	if b.status == 0 {
		b.status = 200
	}
	if b.err != nil {
		return 0, b.err
	}
	if len(data) > b.limit-b.body.Len() {
		b.err = errors.New("command response too large")
		return 0, b.err
	}
	return b.body.Write(data)
}

// CookieGateway buffers only explicitly selected POST command responses. It must
// wrap an already authenticated Framework handler. No cookie attributes, domains,
// or credential destinations are selected from request bodies.
func CookieGateway(next http.Handler, policy CookiePolicy, paths []string, maxResponseBytes int) (http.Handler, error) {
	if next == nil || maxResponseBytes <= 0 {
		return nil, errors.New("handler and positive response limit required")
	}
	if err := policy.validate(); err != nil {
		return nil, err
	}
	policy.Types = append([]string(nil), policy.Types...)
	policy.Origins = append([]string(nil), policy.Origins...)
	selected := map[string]bool{}
	for _, path := range paths {
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#{ }") {
			return nil, errors.New("exact command paths required")
		}
		selected[path] = true
	}
	if len(selected) == 0 {
		return nil, errors.New("command paths required")
	}
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if !selected[r.URL.Path] {
			next.ServeHTTP(w, r)
			return nil
		}
		if err := policy.checkOrigin(r, true); err != nil {
			return err
		}
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		if r.Method == "OPTIONS" {
			if r.Header.Get("Access-Control-Request-Method") != "POST" {
				return authn.ErrForbidden
			}
			for _, name := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
				switch strings.ToLower(strings.TrimSpace(name)) {
				case "", "authorization", "content-type", "idempotency-key", "x-csrf-token", "x-api-key":
				default:
					return authn.ErrForbidden
				}
			}
			w.Header().Set("Access-Control-Allow-Methods", "POST")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-CSRF-Token, X-API-Key")
			w.WriteHeader(204)
			return nil
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return nil
		}
		// Validate cookie CSRF before executing a command, even if an inner custom
		// authenticator accidentally overlooks cookie-origin checks.
		if _, err := policy.credential(r); err != nil {
			return err
		}
		response := &gatewayBuffer{header: make(http.Header), limit: maxResponseBytes}
		next.ServeHTTP(response, r)
		if response.err != nil {
			return response.err
		}
		status := response.status
		if status == 0 {
			status = 200
		}
		if status >= 200 && status < 300 && response.body.Len() > 0 {
			var out command.Response
			if err := eventstore.Decode(response.body.Bytes(), &out); err != nil {
				return errors.New("invalid command response")
			}
			// Validate all cookie instructions before emitting any headers.
			prepared := &gatewayBuffer{header: make(http.Header)}
			issued := len(out.Tokens) > 0
			if err := policy.apply(prepared, &out); err != nil {
				return err
			}
			if issued {
				csrf, err := oneCookie(r, "__Host-csrf")
				if err != nil {
					return err
				}
				if csrf == "" {
					if _, err := policy.SetCSRF(prepared); err != nil {
						return err
					}
				}
			}

			for k, values := range forwardGatewayHeaders(response.header) {
				w.Header()[k] = values
			}
			for _, cookie := range prepared.header.Values("Set-Cookie") {
				w.Header().Add("Set-Cookie", cookie)
			}
			return Write(w, status, out)
		}
		for k, values := range forwardGatewayHeaders(response.header) {
			w.Header()[k] = values
		}
		w.WriteHeader(status)
		_, err := w.Write(response.body.Bytes())
		return err
	}), nil
}

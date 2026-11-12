package cors

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// CORS permits only explicit origins. Bearer authentication remains mandatory;
// cookies are not an alternate authentication channel in the Go HTTP contract.
func CORS(next http.Handler, origins []string) (http.Handler, error) {
	allowed := map[string]bool{}
	for _, origin := range origins {
		u, e := url.Parse(origin)
		if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("CORS requires exact HTTP(S) origins")
		}
		allowed[origin] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		if !allowed[origin] {
			http.Error(w, "origin forbidden", 403)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		if r.Method == "OPTIONS" {
			method := r.Header.Get("Access-Control-Request-Method")
			if method != "GET" && method != "POST" {
				http.Error(w, "method forbidden", 403)
				return
			}
			for _, h := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
				switch strings.ToLower(strings.TrimSpace(h)) {
				case "", "authorization", "content-type", "idempotency-key":
				default:
					http.Error(w, "header forbidden", 403)
					return
				}
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}

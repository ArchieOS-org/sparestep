package web

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/ArchieOS-org/sparestep/internal/store"
)

//go:embed assets/index.html assets/app.js assets/style.css
var assets embed.FS

type handler struct {
	s              *store.Store
	project, token string
	mu             sync.Mutex
	sessions       map[string]string
}

// NewHandler returns the authenticated local report application.
func NewHandler(s *store.Store, project, token string) http.Handler {
	h := &handler{s: s, project: project, token: token, sessions: make(map[string]string)}
	m := http.NewServeMux()
	m.HandleFunc("/", h.page)
	m.HandleFunc("/assets/", h.asset)
	m.HandleFunc("/api/auth", h.auth)
	m.HandleFunc("/api/report", h.report)
	m.HandleFunc("/api/finding/disposition", h.disposition)
	m.HandleFunc("/api/draft", h.draft)
	m.HandleFunc("/api/issue-url", h.issueURL)
	m.HandleFunc("/api/pause", h.pause)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if !loopbackHost(r.Host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.ContentLength > 65536 {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 65536)
		m.ServeHTTP(w, r)
	})
}

func loopbackHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}
func (h *handler) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, _ := assets.ReadFile("assets/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}
func (h *handler) asset(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	b, err := assets.ReadFile(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", map[string]string{"assets/app.js": "text/javascript; charset=utf-8", "assets/style.css": "text/css; charset=utf-8"}[p])
	_, _ = w.Write(b)
}
func (h *handler) bearer(r *http.Request) bool {
	if h.token == "" {
		return false
	}
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, "Bearer ") {
		return false
	}
	x := []byte(strings.TrimPrefix(v, "Bearer "))
	return len(x) == len(h.token) && subtle.ConstantTimeCompare(x, []byte(h.token)) == 1
}
func (h *handler) session(r *http.Request) bool {
	c, e := r.Cookie("sparestep_session")
	if e != nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.sessions[c.Value]
	return ok
}
func (h *handler) authenticated(r *http.Request) bool { return h.bearer(r) || h.session(r) }
func (h *handler) auth(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	if r.ContentLength > 65536 {
		http.Error(w, "request too large", 413)
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if h.token == "" || json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&in) != nil || len(in.Token) != len(h.token) || subtle.ConstantTimeCompare([]byte(in.Token), []byte(h.token)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		http.Error(w, "random", 500)
		return
	}
	sid := fmt.Sprintf("%x", b)
	csrf := make([]byte, 16)
	if _, e := rand.Read(csrf); e != nil {
		http.Error(w, "could not create a session", 500)
		return
	}
	cv := fmt.Sprintf("%x", csrf)
	h.mu.Lock()
	h.sessions[sid] = cv
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "sparestep_session", Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: "sparestep_csrf", Value: cv, Path: "/", SameSite: http.SameSiteStrictMode})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"csrf": cv})
}
func (h *handler) writeOK(r *http.Request) bool {
	if h.bearer(r) {
		return true
	}
	c, e := r.Cookie("sparestep_session")
	if e != nil || !h.session(r) {
		return false
	}
	h.mu.Lock()
	csrf := h.sessions[c.Value]
	h.mu.Unlock()
	o := r.Header.Get("Origin")
	u, e := url.Parse(o)
	return e == nil && u.Scheme == "http" && u.Host == r.Host && loopbackHost(u.Host) && subtle.ConstantTimeCompare([]byte(csrf), []byte(r.Header.Get("X-CSRF-Token"))) == 1
}
func (h *handler) report(w http.ResponseWriter, r *http.Request) {
	if !h.authenticated(r) {
		http.Error(w, "unauthorized", 401)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "method", 405)
		return
	}
	x, e := h.s.Report(h.project)
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	jsonWrite(w, x)
}
func (h *handler) disposition(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	if !h.writeOK(r) {
		http.Error(w, "forbidden", 403)
		return
	}
	var x struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil || (x.Value != "open" && x.Value != "necessary" && x.Value != "dismissed") {
		http.Error(w, "bad request", 400)
		return
	}
	if e := h.s.SetDisposition(h.project, x.ID, x.Value); e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	jsonWrite(w, map[string]string{"status": "saved"})
}
func (h *handler) draft(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	if !h.writeOK(r) {
		http.Error(w, "forbidden", 403)
		return
	}
	var x struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil || x.ID == "" {
		http.Error(w, "bad request", 400)
		return
	}
	d, e := h.s.Draft(h.project, x.ID)
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	jsonWrite(w, d)
}
func (h *handler) issueURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	if !h.writeOK(r) {
		http.Error(w, "forbidden", 403)
		return
	}
	var x struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil || x.ID == "" {
		http.Error(w, "bad request", 400)
		return
	}
	if e := h.s.SetIssueURL(h.project, x.ID, x.URL); e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	jsonWrite(w, map[string]string{"status": "saved"})
}
func (h *handler) pause(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	if !h.writeOK(r) {
		http.Error(w, "forbidden", 403)
		return
	}
	var x struct {
		Paused bool `json:"paused"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil {
		http.Error(w, "bad request", 400)
		return
	}
	if e := h.s.SetPaused(h.project, x.Paused); e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	jsonWrite(w, map[string]bool{"paused": x.Paused})
}
func jsonWrite(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArchieOS-org/sparestep/internal/store"
)

func testServer(t *testing.T) (*store.Store, *httptest.Server) {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/data.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ts := httptest.NewServer(NewHandler(s, "/p", "secret"))
	t.Cleanup(ts.Close)
	return s, ts
}
func TestHandlerAuthAndHeaders(t *testing.T) {
	_, ts := testServer(t)
	r, _ := http.Get(ts.URL + "/api/report")
	if r.StatusCode != 401 {
		t.Fatalf("status=%d", r.StatusCode)
	}
	if r.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP")
	}
	req, _ := http.NewRequest("GET", ts.URL+"/", nil)
	req.Host = "attacker.test"
	x, _ := http.DefaultClient.Do(req)
	if x.StatusCode != 403 {
		t.Fatalf("dns rebinding status=%d", x.StatusCode)
	}
	req, _ = http.NewRequest("GET", ts.URL+"/api/report", nil)
	req.Header.Set("Authorization", "Bearer secret")
	x, _ = http.DefaultClient.Do(req)
	if x.StatusCode != 200 {
		t.Fatalf("bearer status=%d", x.StatusCode)
	}
}
func TestHandlerWritesRequirePostAndCSRF(t *testing.T) {
	_, ts := testServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/api/pause", nil)
	req.Header.Set("Authorization", "Bearer secret")
	r, _ := http.DefaultClient.Do(req)
	if r.StatusCode != 405 {
		t.Fatalf("method=%d", r.StatusCode)
	}
	req, _ = http.NewRequest("POST", ts.URL+"/api/pause", strings.NewReader(`{"paused":true}`))
	req.Header.Set("Origin", ts.URL)
	r, _ = http.DefaultClient.Do(req)
	if r.StatusCode != 403 {
		t.Fatalf("csrf=%d", r.StatusCode)
	}
}

func authenticatedClient(t *testing.T, ts *httptest.Server) (*http.Client, string) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/auth", strings.NewReader(`{"token":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth status=%d", resp.StatusCode)
	}
	var auth struct {
		CSRF string `json:"csrf"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&auth); err != nil {
		t.Fatal(err)
	}
	if auth.CSRF == "" {
		t.Fatal("auth response did not include csrf")
	}
	return client, auth.CSRF
}

func reportPaused(t *testing.T, client *http.Client, ts *httptest.Server) bool {
	t.Helper()
	resp, err := client.Get(ts.URL + "/api/report")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report status=%d", resp.StatusCode)
	}
	var report struct {
		Paused bool `json:"paused"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatal(err)
	}
	return report.Paused
}

func pauseRequest(t *testing.T, client *http.Client, ts *httptest.Server, csrf, origin string, paused bool) int {
	t.Helper()
	body := `{"paused":true}`
	if !paused {
		body = `{"paused":false}`
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/pause", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestCookieCSRFAllowsPauseAndRejectsMismatches(t *testing.T) {
	_, ts := testServer(t)
	client, csrf := authenticatedClient(t, ts)

	if status := pauseRequest(t, client, ts, csrf, ts.URL, true); status != http.StatusOK {
		t.Fatalf("valid pause status=%d", status)
	}
	if !reportPaused(t, client, ts) {
		t.Fatal("valid CSRF request did not pause capture")
	}

	if status := pauseRequest(t, client, ts, "wrong-csrf", ts.URL, false); status != http.StatusForbidden {
		t.Fatalf("wrong csrf status=%d", status)
	}
	if !reportPaused(t, client, ts) {
		t.Fatal("wrong CSRF request changed pause state")
	}
	if status := pauseRequest(t, client, ts, csrf, "http://other.example", false); status != http.StatusForbidden {
		t.Fatalf("wrong origin status=%d", status)
	}
	if !reportPaused(t, client, ts) {
		t.Fatal("wrong origin request changed pause state")
	}
	if status := pauseRequest(t, client, ts, "", ts.URL, false); status != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d", status)
	}
	if !reportPaused(t, client, ts) {
		t.Fatal("missing CSRF request changed pause state")
	}

	if status := pauseRequest(t, client, ts, csrf, ts.URL, false); status != http.StatusOK {
		t.Fatalf("valid resume status=%d", status)
	}
	if reportPaused(t, client, ts) {
		t.Fatal("valid resume did not clear pause state")
	}
}

func TestAuthRejectsMissingInvalidAndOversizeTokens(t *testing.T) {
	_, ts := testServer(t)
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "missing token", body: `{}`, want: http.StatusUnauthorized},
		{name: "empty token", body: `{"token":""}`, want: http.StatusUnauthorized},
		{name: "invalid token", body: `{"token":"wrong"}`, want: http.StatusUnauthorized},
		{name: "oversize body", body: strings.Repeat("x", 65537), want: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/auth", strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Fatalf("status=%d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

func TestUnauthenticatedEmptyBearerAndDraftMethodAreRejected(t *testing.T) {
	_, ts := testServer(t)
	for _, bearer := range []string{"", "Bearer ", "Bearer wrong"} {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/report", nil)
		if err != nil {
			t.Fatal(err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("bearer %q status=%d", bearer, resp.StatusCode)
		}
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/draft", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("draft method status=%d", resp.StatusCode)
	}
}

func TestAuthFragmentAndEscapedAsset(t *testing.T) {
	_, ts := testServer(t)
	req, _ := http.NewRequest("POST", ts.URL+"/api/auth", strings.NewReader(`{"token":"secret"}`))
	r, _ := http.DefaultClient.Do(req)
	if r.StatusCode != 200 {
		t.Fatal(r.Status)
	}
	b, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(b), "csrf") {
		t.Fatal("csrf missing")
	}
	r, _ = http.Get(ts.URL + "/assets/app.js")
	if r.StatusCode != 200 {
		t.Fatal(r.Status)
	}
}

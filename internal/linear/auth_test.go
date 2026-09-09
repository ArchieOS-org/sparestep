package linear

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestPublicCallbackOptions(t *testing.T) {
	for _, opts := range []ConnectOptions{
		{}, {CallbackURL: "https://example.com/linear/callback", CallbackPort: 43123},
	} {
		if err := validateCallbackOptions(opts); err != nil {
			t.Fatal(err)
		}
	}
	for _, opts := range []ConnectOptions{
		{CallbackPort: -1}, {CallbackPort: 65536},
		{CallbackURL: "https://example.com/callback"},
		{CallbackURL: "http://example.com/callback", CallbackPort: 43123},
		{CallbackURL: "https://user:pass@example.com/callback", CallbackPort: 43123},
		{CallbackURL: "https://example.com/callback?code=x", CallbackPort: 43123},
		{CallbackURL: "https://example.com/callback#x", CallbackPort: 43123},
	} {
		if err := validateCallbackOptions(opts); err == nil {
			t.Fatalf("accepted invalid options: %+v", opts)
		}
	}
}

func TestCallbackRemainsLoopbackAndDoesNotBlockDuplicates(t *testing.T) {
	address, results, stop, err := localCallback(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	parsed, _ := url.Parse(address)
	if parsed.Hostname() != "127.0.0.1" {
		t.Fatalf("listener is not loopback: %s", address)
	}
	client := &http.Client{Timeout: time.Second}
	get := func(path string, want int) {
		t.Helper()
		r, err := client.Get(path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		_, _ = io.Copy(io.Discard, r.Body)
		if r.StatusCode != want {
			t.Fatalf("status %d, want %d", r.StatusCode, want)
		}
	}
	get(address, http.StatusBadRequest)
	get(address+"?code=example&state=state&iss=issuer", http.StatusOK)
	get(address+"?code=duplicate&state=state", http.StatusConflict)
	got := <-results
	if got.Code != "example" || got.State != "state" || got.Iss != "issuer" {
		t.Fatalf("callback changed OAuth parameters: %+v", got)
	}
}

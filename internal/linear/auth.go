package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

type ConnectOptions struct {
	Endpoint   string
	DisplayURL func(string)
	OnURL      func(string) error
	HTTPClient *http.Client
	NoBrowser  bool
}

// Connect establishes an MCP session using Linear's Streamable HTTP endpoint.
// The callback/display arguments are intentionally accepted as functions so a
// CLI can print the URL, a desktop app can open it, and headless SSH callers
// can forward the local callback port themselves.
func (o *Outbox) Connect(ctx context.Context, stateDir string, callbacks ...any) error {
	var opts ConnectOptions
	for _, raw := range callbacks {
		switch v := raw.(type) {
		case ConnectOptions:
			opts = v
		case *ConnectOptions:
			if v != nil {
				opts = *v
			}
		case func(string) error:
			opts.OnURL = v
		case func(string):
			opts.DisplayURL = v
		}
	}
	if opts.Endpoint == "" {
		opts.Endpoint = o.endpoint
	}
	if stateDir == "" {
		return errors.New("linear: OAuth state directory is required")
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return err
	}
	_ = os.Chmod(stateDir, 0700)
	statePath := filepath.Join(stateDir, "linear-oauth.json")
	stored, _ := loadOAuthState(statePath)
	var initial oauth2.TokenSource
	if stored != nil && stored.Config.ClientID != "" && stored.Token.AccessToken != "" {
		initial = authSavingTokenSource(stored.Config, stored.Token, statePath)
	}
	if opts.NoBrowser && initial == nil {
		o.markAllAuthRequired("browser authorization is required; run `sparestep linear connect` interactively")
		return ErrAuthRequired
	}
	preferredCallback := ""
	if stored != nil {
		preferredCallback = stored.Config.RedirectURL
	}
	callbackURL, waitCallback, stopCallback, err := localCallback(ctx, preferredCallback)
	if err != nil {
		return err
	}
	defer stopCallback()
	fetcher := func(fetchCtx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		if opts.NoBrowser {
			return nil, ErrAuthRequired
		}
		if opts.OnURL != nil {
			if err := opts.OnURL(args.URL); err != nil {
				return nil, err
			}
		}
		if opts.DisplayURL != nil {
			opts.DisplayURL(args.URL)
		} else if opts.OnURL == nil {
			fmt.Fprintf(os.Stderr, "Open Linear authorization URL in a browser:\n%s\nHeadless SSH: forward the callback port shown in the URL (ssh -L local:remote) and keep this process running.\n", args.URL)
		}
		select {
		case result := <-waitCallback:
			return result, nil
		case <-fetchCtx.Done():
			return nil, fetchCtx.Err()
		}
	}
	var stateMu sync.Mutex
	save := func(cfg *oauth2.Config, tok *oauth2.Token) error {
		stateMu.Lock()
		defer stateMu.Unlock()
		return saveOAuthState(statePath, cfg, tok)
	}
	config := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL:                     callbackURL,
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{RedirectURIs: []string{callbackURL}, TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, ClientName: "Sparestep", Scope: "mcp"}},
		AuthorizationCodeFetcher:        fetcher, RequestRefreshToken: true, Client: opts.HTTPClient, InitialTokenSource: initial,
		NewTokenSource: func(c context.Context, cfg *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			if err := save(cfg, tok); err != nil {
				return nil, err
			}
			return savingTokenSource(cfg.TokenSource(c, tok), cfg, tok, save), nil
		},
	}
	if stored != nil && stored.Config.ClientID != "" {
		config.PreregisteredClient = &oauthex.ClientCredentials{ClientID: stored.Config.ClientID}
		if stored.Config.ClientSecret != "" {
			config.PreregisteredClient.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: stored.Config.ClientSecret}
		}
	}
	handler, err := auth.NewAuthorizationCodeHandler(config)
	if err != nil {
		return err
	}
	transport := &mcp.StreamableClientTransport{Endpoint: opts.Endpoint, OAuthHandler: handler, HTTPClient: opts.HTTPClient, DisableStandaloneSSE: true, MaxRetries: 0}
	client := mcp.NewClient(&mcp.Implementation{Name: "sparestep", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		if isAuthError(err) {
			o.markAllAuthRequired(err.Error())
		}
		return err
	}
	o.SetClient(&sdkClient{session: session})
	// A successful user sign-in makes previously auth-blocked work eligible for
	// the next guarded flush. Ambiguous creates remain untouched.
	_, _ = o.db.Exec(`UPDATE linear_outbox SET status=?,last_error='',lease_until=0,updated_at=? WHERE status=?`, string(StatusQueued), time.Now().UTC().UnixNano(), string(StatusAuthRequired))
	return nil
}

func (o *Outbox) markAllAuthRequired(msg string) {
	_, _ = o.db.Exec(`UPDATE linear_outbox SET status=?,last_error=?,lease_until=0,updated_at=? WHERE status IN (?,?)`, string(StatusAuthRequired), boundedRedacted(msg, 2000), time.Now().UTC().UnixNano(), string(StatusQueued), string(StatusSending))
}

type oauthState struct {
	Config oauthConfig  `json:"config"`
	Token  oauth2.Token `json:"token"`
}
type oauthConfig struct {
	ClientID, ClientSecret string
	Endpoint               oauth2.Endpoint
	RedirectURL            string
	Scopes                 []string
}

func toState(c *oauth2.Config, t *oauth2.Token) oauthState {
	return oauthState{Config: oauthConfig{c.ClientID, c.ClientSecret, c.Endpoint, c.RedirectURL, c.Scopes}, Token: *t}
}
func (s oauthState) config() *oauth2.Config {
	return &oauth2.Config{ClientID: s.Config.ClientID, ClientSecret: s.Config.ClientSecret, Endpoint: s.Config.Endpoint, RedirectURL: s.Config.RedirectURL, Scopes: s.Config.Scopes}
}
func loadOAuthState(path string) (*oauthState, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var s oauthState
	if e = json.Unmarshal(b, &s); e != nil {
		return nil, e
	}
	return &s, nil
}
func saveOAuthState(path string, c *oauth2.Config, t *oauth2.Token) error {
	if c == nil || t == nil {
		return errors.New("linear: incomplete OAuth state")
	}
	b, e := json.MarshalIndent(toState(c, t), "", "  ")
	if e != nil {
		return e
	}
	tmp := path + ".tmp"
	if e = os.WriteFile(tmp, b, 0600); e != nil {
		return e
	}
	_ = os.Chmod(tmp, 0600)
	return os.Rename(tmp, path)
}
func authSavingTokenSource(c oauthConfig, t oauth2.Token, path string) oauth2.TokenSource {
	cfg := &oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, Endpoint: c.Endpoint, RedirectURL: c.RedirectURL, Scopes: c.Scopes}
	return savingTokenSource(cfg.TokenSource(context.Background(), &t), cfg, &t, func(cfg *oauth2.Config, tok *oauth2.Token) error { return saveOAuthState(path, cfg, tok) })
}

type savingSource struct {
	mu      sync.Mutex
	src     oauth2.TokenSource
	cfg     *oauth2.Config
	initial string
	save    func(*oauth2.Config, *oauth2.Token) error
}

func savingTokenSource(src oauth2.TokenSource, cfg *oauth2.Config, initial *oauth2.Token, save func(*oauth2.Config, *oauth2.Token) error) oauth2.TokenSource {
	if src == nil {
		return nil
	}
	s := &savingSource{src: src, cfg: cfg, save: save}
	if initial != nil {
		s.initial = initial.AccessToken
	}
	return s
}
func (s *savingSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, e := s.src.Token()
	if e != nil {
		return nil, e
	}
	if tok.AccessToken != s.initial {
		s.initial = tok.AccessToken
		if s.save != nil {
			if e := s.save(s.cfg, tok); e != nil {
				return nil, e
			}
		}
	}
	return tok, nil
}

func localCallback(ctx context.Context, preferred string) (string, <-chan *auth.AuthorizationResult, func(), error) {
	listener, e := netListen(preferred)
	if e != nil {
		return "", nil, nil, e
	}
	result := make(chan *auth.AuthorizationResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("error") != "" {
			http.Error(w, "authorization failed", http.StatusBadRequest)
			select {
			case result <- nil:
			default:
			}
			return
		}
		if q.Get("code") == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		fmt.Fprintln(w, "Sparestep is authorized. You may return to the terminal.")
		result <- &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	stop := func() { _ = server.Shutdown(context.Background()) }
	_ = ctx
	return "http://127.0.0.1:" + listener.Addr().String()[strings.LastIndex(listener.Addr().String(), ":")+1:] + "/callback", result, stop, nil
}

// Split out of localCallback so tests and future platforms can replace only
// the listener policy. It always binds loopback and an ephemeral port.
func netListen(preferred string) (net.Listener, error) {
	if preferred != "" {
		if u, err := url.Parse(preferred); err == nil && u.Hostname() == "127.0.0.1" && u.Port() != "" {
			return net.Listen("tcp", net.JoinHostPort(u.Hostname(), u.Port()))
		}
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

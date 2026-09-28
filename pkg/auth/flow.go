package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

// Scopes requested at consent. The monetary scope is a superset of the plain
// analytics scope; it only yields revenue data for YouTube Partner channels.
var Scopes = []string{
	"https://www.googleapis.com/auth/youtube.readonly",
	"https://www.googleapis.com/auth/yt-analytics.readonly",
	"https://www.googleapis.com/auth/yt-analytics-monetary.readonly",
}

// flowLifetime is how long a started consent flow keeps its loopback
// listener open, independent of how long the caller waits.
const flowLifetime = 10 * time.Minute

// OAuthConfig builds the OAuth client config for a Desktop-app client.
func OAuthConfig(clientID, clientSecret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     endpoints.Google,
		Scopes:       Scopes,
	}
}

// pending is one in-flight consent flow on a loopback port.
type pending struct {
	url     string
	expires time.Time
	done    chan struct{}
	acct    Account
	err     error
}

func (p *pending) finished() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// identifyFunc turns a fresh token into the channel it was granted for.
type identifyFunc func(ctx context.Context, tok *oauth2.Token) (Account, error)

// startFlow opens a loopback listener and returns the consent URL. The
// callback exchanges the code (PKCE), identifies the channel and calls
// finish; the listener closes on completion or after flowLifetime.
func startFlow(oc *oauth2.Config, identify identifyFunc, finish func(Account) error) (*pending, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("open loopback listener: %w", err)
	}
	cfg := *oc
	cfg.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	state := randomHex(16)
	verifier := oauth2.GenerateVerifier()

	p := &pending{
		url: cfg.AuthCodeURL(state,
			oauth2.AccessTypeOffline,
			oauth2.SetAuthURLParam("prompt", "select_account consent"),
			oauth2.S256ChallengeOption(verifier)),
		expires: time.Now().Add(flowLifetime),
		done:    make(chan struct{}),
	}

	var once sync.Once
	complete := func(a Account, err error) {
		once.Do(func() {
			p.acct, p.err = a, err
			close(p.done)
		})
	}

	mux := http.NewServeMux()
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "state mismatch; start the connection again", http.StatusBadRequest)
			return
		}
		if e := q.Get("error"); e != "" {
			complete(Account{}, fmt.Errorf("authorization was not granted (%s)", e))
			writePage(w, "Not connected", "Authorization was not granted: "+e)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		tok, err := cfg.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(verifier))
		if err != nil {
			complete(Account{}, fmt.Errorf("token exchange failed: %w", err))
			writePage(w, "Not connected", "Token exchange failed: "+err.Error())
			return
		}
		if tok.RefreshToken == "" {
			err := errors.New("Google returned no refresh token; remove this app at https://myaccount.google.com/permissions and connect again")
			complete(Account{}, err)
			writePage(w, "Not connected", err.Error())
			return
		}
		acct, err := identify(ctx, tok)
		if err == nil {
			err = finish(acct)
		}
		complete(acct, err)
		if err != nil {
			writePage(w, "Not connected", err.Error())
			return
		}
		writePage(w, "Channel connected", fmt.Sprintf("%s is connected. You can close this tab and return to your assistant.", acct.Title))
	})

	go func() { _ = srv.Serve(ln) }()
	go func() {
		select {
		case <-p.done:
		case <-time.After(flowLifetime):
			complete(Account{}, errors.New("authorization timed out; start the connection again"))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	return p, nil
}

func writePage(w http.ResponseWriter, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>%s</title>
<body style="font-family:system-ui,sans-serif;max-width:36rem;margin:4rem auto;padding:0 1rem">
<h2>%s</h2><p>%s</p></body>`, html.EscapeString(title), html.EscapeString(title), html.EscapeString(msg))
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// OpenBrowser tries to open url in the user's default browser.
func OpenBrowser(url string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start() == nil
}

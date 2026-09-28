package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gomcpgo/youtube_analytics/internal/fakegoogle"
	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
	"golang.org/x/oauth2"
)

func TestConnectFlow(t *testing.T) {
	fg := fakegoogle.New()
	defer fg.Close()
	old := youtube.BaseURLs
	youtube.BaseURLs.Data = fg.URL + "/youtube/v3"
	defer func() { youtube.BaseURLs = old }()

	var verifier string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		verifier = r.Form.Get("code_verifier")
		if r.Form.Get("code") != "good-code" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"access_token": "at", "refresh_token": "rt", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	oc := &oauth2.Config{ClientID: "id", ClientSecret: "secret", Scopes: Scopes,
		Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.example/auth", TokenURL: tokenSrv.URL}}
	store, _ := OpenStore(t.TempDir())
	var mu sync.Mutex
	var connected []string
	m := NewManager(oc, store, 5*time.Second, nil, func(a Account, _ *youtube.Client) {
		mu.Lock()
		connected = append(connected, a.ChannelID)
		mu.Unlock()
	})

	res, err := m.Connect(context.Background(), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Completed {
		t.Fatal("should still be pending")
	}
	u, _ := url.Parse(res.URL)
	q := u.Query()
	for k, want := range map[string]string{"access_type": "offline", "code_challenge_method": "S256", "prompt": "select_account consent"} {
		if q.Get(k) != want {
			t.Errorf("auth URL %s = %q, want %q", k, q.Get(k), want)
		}
	}
	if !strings.Contains(q.Get("scope"), "yt-analytics.readonly") {
		t.Errorf("scope = %q", q.Get("scope"))
	}
	// Reusing a pending flow returns the same URL.
	if again, _ := m.Connect(context.Background(), 0, false); again.URL != res.URL {
		t.Error("a pending flow should be reused")
	}

	// A wrong state is rejected without finishing the flow.
	bad, _ := http.Get(q.Get("redirect_uri") + "?code=good-code&state=wrong")
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("wrong state status = %d", bad.StatusCode)
	}
	// A caller waiting on the pending flow sees it complete.
	type result struct {
		r   *ConnectResult
		err error
	}
	waiting := make(chan result, 1)
	go func() {
		r, err := m.Connect(context.Background(), 5*time.Second, false)
		waiting <- result{r, err}
	}()
	time.Sleep(100 * time.Millisecond)
	resp, err := http.Get(q.Get("redirect_uri") + "?code=good-code&state=" + q.Get("state"))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("callback: %v %v", resp, err)
	}
	done := <-waiting
	if done.err != nil || !done.r.Completed || done.r.Account.ChannelID != fakegoogle.ChannelID || done.r.URL != res.URL {
		t.Fatalf("connect result = %+v, %v", done.r, done.err)
	}
	// After completion the next call starts a new flow (to connect another channel).
	if next, _ := m.Connect(context.Background(), 0, false); next.URL == res.URL {
		t.Error("a finished flow must not be reused")
	}
	if verifier == "" {
		t.Error("token exchange must send a PKCE code_verifier")
	}
	accts := m.Accounts()
	if len(accts) != 1 || accts[0].Token.RefreshToken != "rt" || accts[0].Handle != "@fakechannel" {
		t.Errorf("stored accounts = %+v", accts)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(connected) != 1 {
		t.Errorf("onConnect calls = %v", connected)
	}
}

func TestResolve(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	m := NewManager(nil, store, time.Second, nil, nil)
	if _, err := m.Resolve(""); err == nil || !strings.Contains(err.Error(), "connect_channel") {
		t.Errorf("empty store: %v", err)
	}
	_ = store.Put(Account{ChannelID: "UC1", Title: "Cooking Lab", Handle: "@cookinglab"})
	if a, err := m.Resolve(""); err != nil || a.ChannelID != "UC1" {
		t.Errorf("single channel default: %v %v", a, err)
	}
	_ = store.Put(Account{ChannelID: "UC2", Title: "Cooking Shorts", Handle: "@cookshorts"})
	if _, err := m.Resolve(""); err == nil || !strings.Contains(err.Error(), "Cooking Lab") {
		t.Errorf("ambiguous default should list channels: %v", err)
	}
	for sel, want := range map[string]string{"UC2": "UC2", "@cookinglab": "UC1", "cookshorts": "UC2", "cooking lab": "UC1", "shorts": "UC2"} {
		if a, err := m.Resolve(sel); err != nil || a.ChannelID != want {
			t.Errorf("Resolve(%q) = %v, %v; want %s", sel, a.ChannelID, err, want)
		}
	}
	if _, err := m.Resolve("cooking"); err == nil {
		t.Error("an ambiguous substring must not pick a channel")
	}
	if _, err := m.Client(Account{ChannelID: "UC1"}); err != ErrNoClient {
		t.Errorf("Client without OAuth config = %v", err)
	}
}

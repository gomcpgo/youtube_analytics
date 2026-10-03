package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
	"golang.org/x/oauth2"
)

// ErrNoClient is returned when the OAuth client env vars are missing.
var ErrNoClient = errors.New("the Google OAuth client is not configured: set YOUTUBE_OAUTH_CLIENT_ID and " +
	"YOUTUBE_OAUTH_CLIENT_SECRET to a Google Cloud OAuth client of type 'Desktop app' (see the README for the 5-minute setup)")

// Manager resolves channels to authorized API clients and runs the connect flow.
type Manager struct {
	oc        *oauth2.Config // nil when the client is not configured
	store     *Store
	timeout   time.Duration
	logf      func(string)
	onConnect func(Account, *youtube.Client)

	mu      sync.Mutex
	flow    *pending
	clients map[string]*youtube.Client
}

// NewManager creates a manager. oc may be nil; onConnect runs after a
// channel is connected (used to schedule reach reporting jobs).
func NewManager(oc *oauth2.Config, store *Store, timeout time.Duration, logf func(string), onConnect func(Account, *youtube.Client)) *Manager {
	if logf == nil {
		logf = func(string) {}
	}
	return &Manager{oc: oc, store: store, timeout: timeout, logf: logf, onConnect: onConnect, clients: map[string]*youtube.Client{}}
}

// Accounts lists connected channels.
func (m *Manager) Accounts() []Account { return m.store.List() }

// Resolve picks a connected channel by ID, @handle or title. An empty
// selector is allowed when exactly one channel is connected.
func (m *Manager) Resolve(sel string) (Account, error) {
	accts := m.store.List()
	if len(accts) == 0 {
		return Account{}, errors.New("no YouTube channel is connected yet; call connect_channel first")
	}
	sel = strings.TrimSpace(sel)
	if sel == "" {
		if len(accts) == 1 {
			return accts[0], nil
		}
		return Account{}, fmt.Errorf("several channels are connected; pass channel as one of: %s", names(accts))
	}
	norm := strings.ToLower(strings.TrimPrefix(sel, "@"))
	for _, a := range accts {
		if a.ChannelID == sel || strings.ToLower(strings.TrimPrefix(a.Handle, "@")) == norm || strings.EqualFold(a.Title, sel) {
			return a, nil
		}
	}
	var hits []Account
	for _, a := range accts {
		if strings.Contains(strings.ToLower(a.Title), strings.ToLower(sel)) {
			hits = append(hits, a)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	return Account{}, fmt.Errorf("channel %q is not connected; connected channels: %s", sel, names(accts))
}

func names(accts []Account) string {
	parts := make([]string, len(accts))
	for i, a := range accts {
		p := a.Title
		if a.Handle != "" {
			p += " (" + a.Handle + ")"
		}
		parts[i] = p
	}
	return strings.Join(parts, ", ")
}

// Client returns the API client for a connected channel.
func (m *Manager) Client(a Account) (*youtube.Client, error) {
	if m.oc == nil {
		return nil, ErrNoClient
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clients[a.ChannelID]; ok {
		return c, nil
	}
	c := m.newClient(a.Token, a.ChannelID)
	m.clients[a.ChannelID] = c
	return c, nil
}

func (m *Manager) newClient(tok *oauth2.Token, channelID string) *youtube.Client {
	base := &http.Client{Timeout: m.timeout}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, base)
	var src oauth2.TokenSource = &savingSource{base: m.oc.TokenSource(ctx, tok), store: m.store, channelID: channelID, last: tok.AccessToken}
	hc := oauth2.NewClient(ctx, src)
	hc.Timeout = m.timeout
	return youtube.New(hc, m.logf)
}

// ConnectResult reports the state of a connect flow.
type ConnectResult struct {
	URL           string
	BrowserOpened bool
	Completed     bool
	Account       Account
}

// Connect starts (or resumes) the browser consent flow and waits up to wait
// for it to finish. The flow stays open for 10 minutes regardless of wait.
func (m *Manager) Connect(ctx context.Context, wait time.Duration, openBrowser bool) (*ConnectResult, error) {
	if m.oc == nil {
		return nil, ErrNoClient
	}
	m.mu.Lock()
	p := m.flow
	fresh := p == nil || p.finished() || time.Now().After(p.expires)
	if fresh {
		var err error
		p, err = startFlow(m.oc, m.identify, m.finish)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		m.flow = p
	}
	m.mu.Unlock()

	res := &ConnectResult{URL: p.url}
	if openBrowser {
		res.BrowserOpened = OpenBrowser(p.url)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-p.done:
		if p.err != nil {
			return nil, p.err
		}
		res.Completed = true
		res.Account = p.acct
	case <-timer.C:
	case <-ctx.Done():
	}
	return res, nil
}

func (m *Manager) identify(ctx context.Context, tok *oauth2.Token) (Account, error) {
	ch, err := m.newClient(tok, "").MyChannel(ctx)
	if err != nil {
		return Account{}, fmt.Errorf("connected, but reading the channel failed: %w", err)
	}
	return Account{ChannelID: ch.ID, Title: ch.Title, Handle: ch.Handle, Token: tok, ConnectedAt: time.Now().UTC()}, nil
}

func (m *Manager) finish(a Account) error {
	if err := m.store.Put(a); err != nil {
		return fmt.Errorf("saving the token failed: %w", err)
	}
	m.mu.Lock()
	delete(m.clients, a.ChannelID)
	m.mu.Unlock()
	if m.onConnect != nil {
		if c, err := m.Client(a); err == nil {
			go m.onConnect(a, c)
		}
	}
	return nil
}

// savingSource persists refreshed access tokens so restarts reuse them.
type savingSource struct {
	base      oauth2.TokenSource
	store     *Store
	channelID string

	mu   sync.Mutex
	last string
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	t, err := s.base.Token()
	if err != nil {
		return nil, youtube.WrapTokenError(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channelID != "" && t.AccessToken != s.last {
		s.last = t.AccessToken
		_ = s.store.SetToken(s.channelID, t)
	}
	return t, nil
}

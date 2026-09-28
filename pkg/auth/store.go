// Package auth handles Google OAuth for the user's channels: the loopback
// consent flow, the on-disk token store, and per-channel API clients.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Account is one connected channel and its OAuth token. A Google account with
// several channels (brand accounts) yields one Account per channel, because
// the consent screen binds each grant to the channel picked there.
type Account struct {
	ChannelID   string        `json:"channel_id"`
	Title       string        `json:"title"`
	Handle      string        `json:"handle,omitempty"`
	Token       *oauth2.Token `json:"token"`
	ConnectedAt time.Time     `json:"connected_at"`
}

// Store persists accounts in <dir>/tokens.json with 0600 permissions.
type Store struct {
	path     string
	mu       sync.Mutex
	accounts map[string]*Account
}

// OpenStore loads the token file, if any.
func OpenStore(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, "tokens.json"), accounts: map[string]*Account{}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Account
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	for _, a := range list {
		if a.ChannelID != "" {
			s.accounts[a.ChannelID] = a
		}
	}
	return s, nil
}

// List returns copies of all accounts sorted by title.
func (s *Store) List() []Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title) })
	return out
}

// Put adds or replaces an account.
func (s *Store) Put(a Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accounts[a.ChannelID] = &a
	return s.saveLocked()
}

// SetToken records a refreshed token for a channel.
func (s *Store) SetToken(channelID string, t *oauth2.Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[channelID]
	if !ok {
		return nil
	}
	a.Token = t
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	list := make([]*Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ChannelID < list[j].ChannelID })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

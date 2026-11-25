// Package store keeps the bot's state (users, strategies, subscriptions) in a
// single JSON file, optionally encrypted with AES-256-GCM so it can be kept in
// a public Git repository.
package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Strategy is a named pair of buy/sell conditions written in the dsl language.
type Strategy struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Buy         string    `json:"buy,omitempty"`
	Sell        string    `json:"sell,omitempty"`
	CreatedBy   int64     `json:"created_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Subscription asks for signals of one strategy on one symbol and timeframe.
type Subscription struct {
	Symbol   string `json:"symbol"`
	Interval string `json:"interval"`
	Strategy string `json:"strategy"`
	// LastChecked is the open time (Unix ms) of the newest closed candle that
	// has already been evaluated. It prevents duplicate and missed signals.
	LastChecked int64 `json:"last_checked,omitempty"`
}

// Key identifies a subscription for comparisons.
func (s *Subscription) Key() string {
	return s.Symbol + "|" + s.Interval + "|" + s.Strategy
}

// User is a Telegram chat that talks to the bot.
type User struct {
	ChatID      int64           `json:"chat_id"`
	Name        string          `json:"name,omitempty"`
	Approved    bool            `json:"approved"`
	RequestedAt time.Time       `json:"requested_at,omitempty"`
	Subs        []*Subscription `json:"subs,omitempty"`
}

// State is everything the bot persists between runs.
type State struct {
	Version      int                  `json:"version"`
	UpdateOffset int64                `json:"update_offset"` // next Telegram update_id to fetch
	Seeded       bool                 `json:"seeded"`        // default strategies were added once
	Strategies   map[string]*Strategy `json:"strategies"`
	Users        map[int64]*User      `json:"users"`
}

// New returns an empty state.
func New() *State {
	return &State{Version: 1, Strategies: map[string]*Strategy{}, Users: map[int64]*User{}}
}

// StrategyNames returns strategy names sorted alphabetically. The 1-based
// position in this list is the strategy's number shown to users.
func (s *State) StrategyNames() []string {
	names := make([]string, 0, len(s.Strategies))
	for n := range s.Strategies {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// FindStrategy looks a strategy up by name or by its number in StrategyNames.
func (s *State) FindStrategy(ref string) (*Strategy, bool) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if st, ok := s.Strategies[ref]; ok {
		return st, true
	}
	if n, err := strconv.Atoi(ref); err == nil {
		names := s.StrategyNames()
		if n >= 1 && n <= len(names) {
			return s.Strategies[names[n-1]], true
		}
	}
	return nil, false
}

// StrategyUsers counts how many subscriptions use the named strategy.
func (s *State) StrategyUsers(name string) int {
	count := 0
	for _, u := range s.Users {
		for _, sub := range u.Subs {
			if sub.Strategy == name {
				count++
			}
		}
	}
	return count
}

// --- persistence -----------------------------------------------------------

var magic = []byte("CSBENC1\n")

// Load reads the state from path. A missing file yields an empty state.
// If key is non-empty the file is expected to be encrypted with it.
func Load(path, key string) (*State, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return New(), nil
	}
	encrypted := len(raw) >= len(magic) && string(raw[:len(magic)]) == string(magic)
	switch {
	case encrypted && key == "":
		return nil, errors.New("state file is encrypted but STATE_KEY is not set")
	case encrypted:
		raw, err = decrypt(raw[len(magic):], key)
		if err != nil {
			return nil, fmt.Errorf("decrypt state (wrong STATE_KEY?): %w", err)
		}
	}
	st := New()
	if err := json.Unmarshal(raw, st); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	if st.Strategies == nil {
		st.Strategies = map[string]*Strategy{}
	}
	if st.Users == nil {
		st.Users = map[int64]*User{}
	}
	return st, nil
}

// Save writes the state atomically (temp file + rename).
func Save(path, key string, st *State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if key != "" {
		enc, err := encrypt(data, key)
		if err != nil {
			return err
		}
		data = append(append([]byte{}, magic...), enc...)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func gcm(key string) (cipher.AEAD, error) {
	k := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func encrypt(plain []byte, key string) ([]byte, error) {
	aead, err := gcm(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plain, nil), nil
}

func decrypt(data []byte, key string) ([]byte, error) {
	aead, err := gcm(key)
	if err != nil {
		return nil, err
	}
	if len(data) < aead.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := data[:aead.NonceSize()], data[aead.NonceSize():]
	return aead.Open(nil, nonce, ct, nil)
}

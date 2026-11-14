// Package creds supplies credentials (API keys, tokens) that a quickstart
// needs, without ever handing them to the agent.
//
// Flow: the agent calls the devbox MCP tool request_credential. The MCP
// server asks this package's Broker over a unix socket. The broker answers
// from the user's Store or by prompting the user, and the value goes only to
// the MCP server, which injects it into commands run in the throwaway devbox
// container and redacts it from everything the agent and the logs see.
package creds

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
)

var nameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

// ValidName reports whether name is a valid environment variable name for a
// credential (upper case, digits, underscores).
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Store is the user's saved credentials: a JSON object of name → value in a
// 0600 file.
type Store struct {
	Path string
	mu   sync.Mutex
}

// DefaultStore returns the store at <user config dir>/rtfm/credentials.json.
func DefaultStore() (*Store, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	return &Store{Path: filepath.Join(dir, "rtfm", "credentials.json")}, nil
}

func (s *Store) load() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("creds: %s: %w", s.Path, err)
	}
	return m, nil
}

func (s *Store) save(m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Get returns a saved credential.
func (s *Store) Get(name string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return "", false, err
	}
	v, ok := m[name]
	return v, ok && v != "", nil
}

// Set saves a credential.
func (s *Store) Set(name, value string) error {
	if !ValidName(name) {
		return fmt.Errorf("creds: invalid name %q (use an environment variable name like GEMINI_API_KEY)", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return err
	}
	m[name] = value
	return s.save(m)
}

// Delete removes a credential. Removing a missing name is not an error.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return err
	}
	delete(m, name)
	return s.save(m)
}

// Names lists saved credential names (never values).
func (s *Store) Names() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}

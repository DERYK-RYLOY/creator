package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/creatorcli/creator/internal/config"
)

// Cred is a credential saved by the connect flow. BaseURL carries overrides
// the registry cannot know (a Cloudflare account id, a custom endpoint).
type Cred struct {
	Key     string `json:"key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
}

var credMu sync.Mutex

// KeyFile is where saved credentials live. It is a separate file from
// config.json so a shared config never leaks keys.
func KeyFile() string { return filepath.Join(config.Dir(), "keys.json") }

// ErrKeysCorrupt is returned (wrapped) when keys.json exists but cannot be
// parsed as JSON. It is never returned for a merely missing/empty file.
var ErrKeysCorrupt = errors.New("keys.json is corrupt")

// loadCreds reads the credentials file. A missing or empty file yields an
// empty map and no error; a present but unparseable file yields an error —
// silently returning an empty map here would let the next SaveCred wipe
// every other saved credential.
func loadCreds() (map[string]Cred, error) {
	b, err := os.ReadFile(KeyFile())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Cred{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read keys file: %w", err)
	}
	if len(b) == 0 {
		return map[string]Cred{}, nil
	}
	m := map[string]Cred{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%w; refusing to touch it so it can be repaired by hand (%v): %w",
			ErrKeysCorrupt, KeyFile(), err)
	}
	return m, nil
}

// writeCreds writes m atomically: temp file in the same directory with mode
// 0600, fsync, rename over the target. The previous file is kept as
// keys.json.bak (best effort) so a bad write is recoverable.
func writeCreds(m map[string]Cred) error {
	dir := filepath.Dir(KeyFile())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(dir, ".keys-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	// Move aside the previous keys.json so the rename below never has to
	// replace an existing file (os.Rename fails on Windows if dst exists).
	if _, err := os.Stat(KeyFile()); err == nil {
		_ = os.Rename(KeyFile(), KeyFile()+".bak") // best effort backup
	}
	if err := os.Rename(tmpName, KeyFile()); err != nil {
		// Restore the backup rather than leave no keys at all.
		_ = os.Rename(KeyFile()+".bak", KeyFile())
		cleanup()
		return fmt.Errorf("replace keys file: %w", err)
	}
	return nil
}

// LoadCred returns the saved credential for a provider, if any. The found
// flag reports whether a usable credential entry exists; err is non-nil when
// the keys file could not be read (including corruption).
func LoadCred(slug string) (Cred, bool, error) {
	credMu.Lock()
	defer credMu.Unlock()
	m, err := loadCreds()
	if err != nil {
		return Cred{}, false, err
	}
	c, ok := m[slug]
	return c, ok && (c.Key != "" || c.BaseURL != ""), nil
}

// SaveCred stores a provider's credential (key and optional base URL).
// It refuses to write when the existing keys file is corrupt, so a bad
// file is never silently overwritten and destroy other providers' keys.
func SaveCred(slug string, c Cred) error {
	credMu.Lock()
	defer credMu.Unlock()
	m, err := loadCreds()
	if err != nil {
		return err
	}
	m[slug] = c
	return writeCreds(m)
}

// DeleteCred removes a saved credential.
func DeleteCred(slug string) error {
	credMu.Lock()
	defer credMu.Unlock()
	m, err := loadCreds()
	if err != nil {
		return err
	}
	if _, ok := m[slug]; !ok {
		return nil
	}
	delete(m, slug)
	return writeCreds(m)
}

// SavedCreds returns every saved credential.
func SavedCreds() (map[string]Cred, error) {
	credMu.Lock()
	defer credMu.Unlock()
	return loadCreds()
}

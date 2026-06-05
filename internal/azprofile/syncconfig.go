package azprofile

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SyncConfig is the plaintext schema persisted (encrypted) at ConfigPath().
type SyncConfig struct {
	AblyAPIKey    string `json:"ably_api_key"`
	ChannelPrefix string `json:"channel_prefix"`
	SenderID      string `json:"sender_id"`
}

// SyncState is the plaintext per-profile counter persisted at StatePath().
type SyncState struct {
	MonotonicSeq map[string]int64            `json:"monotonic_seq"`
	LastPublish  map[string]time.Time        `json:"last_publish,omitempty"`
	LastReceive  map[string]time.Time        `json:"last_receive,omitempty"`
}

// ConfigDir is the directory holding sync config + state.
// Honors AZPROFILE_HOME (so cron/test isolation extends to sync setup),
// then XDG_CONFIG_HOME, then ~/.config.
func ConfigDir() string {
	if v := os.Getenv("AZPROFILE_HOME"); v != "" {
		return filepath.Join(v, ".config", "azprofile")
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "azprofile")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "azprofile")
}

func ConfigPath() string { return filepath.Join(ConfigDir(), "config.enc") }
func StatePath() string  { return filepath.Join(ConfigDir(), "state.json") }

// LoadConfig decrypts and unmarshals the sync config. Returns os.ErrNotExist
// if the config file is missing — callers should treat that as "not configured".
func LoadConfig() (*SyncConfig, error) {
	blob, err := os.ReadFile(ConfigPath())
	if err != nil {
		return nil, err
	}
	key, err := LoadMasterKey()
	if err != nil {
		return nil, err
	}
	pt, err := DecryptGCM(key, blob)
	if err != nil {
		return nil, fmt.Errorf("decrypt config: %w", err)
	}
	var c SyncConfig
	if err := json.Unmarshal(pt, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// SaveConfig encrypts and writes the config atomically.
func SaveConfig(c *SyncConfig) error {
	if c.SenderID == "" {
		c.SenderID = newSenderID()
	}
	pt, err := json.Marshal(c)
	if err != nil {
		return err
	}
	key, err := LoadMasterKey()
	if err != nil {
		return err
	}
	blob, err := EncryptGCM(key, pt)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		return err
	}
	return atomicWrite(ConfigPath(), blob, 0o600)
}

// RecordPublish stamps the last-publish time for a profile. Best-effort — errors are swallowed.
func RecordPublish(profile string) {
	s, err := LoadState()
	if err != nil {
		return
	}
	if s.LastPublish == nil {
		s.LastPublish = map[string]time.Time{}
	}
	s.LastPublish[profile] = time.Now().UTC()
	_ = SaveState(s)
}

// RecordReceive stamps the last-receive time for a profile. Best-effort — errors are swallowed.
func RecordReceive(profile string) {
	s, err := LoadState()
	if err != nil {
		return
	}
	if s.LastReceive == nil {
		s.LastReceive = map[string]time.Time{}
	}
	s.LastReceive[profile] = time.Now().UTC()
	_ = SaveState(s)
}

func LoadState() (*SyncState, error) {
	b, err := os.ReadFile(StatePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &SyncState{MonotonicSeq: map[string]int64{}}, nil
		}
		return nil, err
	}
	var s SyncState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.MonotonicSeq == nil {
		s.MonotonicSeq = map[string]int64{}
	}
	return &s, nil
}

func SaveState(s *SyncState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		return err
	}
	return atomicWrite(StatePath(), b, 0o600)
}

// NextSeq increments and persists the per-profile monotonic counter.
func NextSeq(profile string) (int64, error) {
	s, err := LoadState()
	if err != nil {
		return 0, err
	}
	n := s.MonotonicSeq[profile] + 1
	s.MonotonicSeq[profile] = n
	if err := SaveState(s); err != nil {
		return 0, err
	}
	return n, nil
}

func newSenderID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	// RFC 4122 variant + version 4 bits
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// File is one item inside a transfer. Name may contain "/" to keep folder structure.
type File struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`       // original (plaintext) size
	StoredSize int64  `json:"storedSize"` // bytes the server will hold (ciphertext size when encrypted)
	Type       string `json:"type,omitempty"`
	Received   int64  `json:"received"`
}

func (f *File) Complete() bool { return f.Received >= f.StoredSize }

// Transfer is a share identified by a short numeric code.
type Transfer struct {
	Code       string    `json:"code"`
	Owner      string    `json:"owner"`
	Sender     string    `json:"sender,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Text       string    `json:"text,omitempty"`
	Encrypted  bool      `json:"encrypted"`
	Salt       string    `json:"salt,omitempty"`
	Check      string    `json:"check,omitempty"`
	BurnAfter  bool      `json:"burnAfter"`
	Downloads  int       `json:"downloads"`
	Views      int       `json:"views"`
	Files      []*File   `json:"files"`
	HistoryID  string    `json:"historyId,omitempty"`
	burnArmed  bool
}

func (t *Transfer) TotalSize() (n int64) {
	for _, f := range t.Files {
		n += f.Size
	}
	return
}

func (t *Transfer) Ready() bool {
	for _, f := range t.Files {
		if !f.Complete() {
			return false
		}
	}
	return true
}

func (t *Transfer) File(id string) *File {
	for _, f := range t.Files {
		if f.ID == id {
			return f
		}
	}
	return nil
}

func (t *Transfer) IsOwner(token string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(t.Owner)) == 1
}

// Store keeps transfers in memory and mirrors metadata to disk so restarts keep them.
type Store struct {
	mu        sync.Mutex
	dir       string
	transfers map[string]*Transfer
	dirty     chan struct{}
}

func NewStore(dataDir string) (*Store, error) {
	s := &Store{
		dir:       dataDir,
		transfers: map[string]*Transfer{},
		dirty:     make(chan struct{}, 1),
	}
	if err := os.MkdirAll(s.filesDir(), 0o755); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(s.metaPath()); err == nil {
		var list []*Transfer
		if json.Unmarshal(b, &list) == nil {
			for _, t := range list {
				s.transfers[t.Code] = t
			}
		}
	}
	go s.saver()
	go s.janitor()
	return s, nil
}

func (s *Store) metaPath() string { return filepath.Join(s.dir, "transfers.json") }
func (s *Store) filesDir() string { return filepath.Join(s.dir, "files") }

func (s *Store) FilePath(code, id string) string {
	return filepath.Join(s.filesDir(), code, id)
}

func (s *Store) markDirty() {
	select {
	case s.dirty <- struct{}{}:
	default:
	}
}

func (s *Store) saver() {
	for range s.dirty {
		time.Sleep(300 * time.Millisecond)
		s.mu.Lock()
		list := make([]*Transfer, 0, len(s.transfers))
		for _, t := range s.transfers {
			list = append(list, t)
		}
		b, _ := json.Marshal(list)
		s.mu.Unlock()
		tmp := s.metaPath() + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, s.metaPath())
		}
	}
}

func (s *Store) janitor() {
	s.sweep()
	t := time.NewTicker(30 * time.Second)
	for range t.C {
		s.sweep()
	}
}

func (s *Store) sweep() {
	now := time.Now()
	var dead []string
	s.mu.Lock()
	for code, t := range s.transfers {
		if now.After(t.ExpiresAt) {
			dead = append(dead, code)
			delete(s.transfers, code)
		}
	}
	s.mu.Unlock()
	for _, code := range dead {
		_ = os.RemoveAll(filepath.Join(s.filesDir(), code))
	}
	// Remove orphaned file folders (e.g. after a crash).
	entries, _ := os.ReadDir(s.filesDir())
	s.mu.Lock()
	for _, e := range entries {
		if _, ok := s.transfers[e.Name()]; !ok {
			_ = os.RemoveAll(filepath.Join(s.filesDir(), e.Name()))
		}
	}
	s.mu.Unlock()
	if len(dead) > 0 {
		s.markDirty()
	}
}

func randomCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(900000))
	return fmt.Sprintf("%06d", n.Int64()+100000)
}

func randomToken(nbytes int) string {
	b := make([]byte, nbytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) Create(t *Transfer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < 50; i++ {
		c := randomCode()
		if _, taken := s.transfers[c]; !taken {
			t.Code = c
			break
		}
	}
	if t.Code == "" {
		return errors.New("no free codes")
	}
	t.Owner = randomToken(16)
	for i, f := range t.Files {
		f.ID = fmt.Sprintf("f%d", i)
		f.Received = 0
	}
	if err := os.MkdirAll(filepath.Join(s.filesDir(), t.Code), 0o755); err != nil {
		return err
	}
	s.transfers[t.Code] = t
	s.markDirty()
	return nil
}

// Get returns the live transfer (nil if missing/expired). Callers must use With for mutations.
func (s *Store) Get(code string) *Transfer {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.transfers[code]
	if t == nil || time.Now().After(t.ExpiresAt) {
		return nil
	}
	return t
}

// With runs fn while holding the store lock.
func (s *Store) With(fn func()) {
	s.mu.Lock()
	fn()
	s.mu.Unlock()
	s.markDirty()
}

func (s *Store) Delete(code string) {
	s.mu.Lock()
	delete(s.transfers, code)
	s.mu.Unlock()
	_ = os.RemoveAll(filepath.Join(s.filesDir(), code))
	s.markDirty()
}

// ArmBurn shortens the life of a "delete after download" transfer once it has been received.
// A short grace period lets an interrupted download resume.
func (s *Store) ArmBurn(t *Transfer) {
	s.With(func() {
		if t.BurnAfter && !t.burnArmed {
			t.burnArmed = true
			grace := time.Now().Add(5 * time.Minute)
			if grace.Before(t.ExpiresAt) {
				t.ExpiresAt = grace
			}
		}
	})
}

func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.transfers)
}

package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

func loadJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---------- This device ----------

type Identity struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Port     int    `json:"port"`
	FP       string `json:"fp"`
}

func loadDeviceID(dir string) string {
	var d struct{ ID string }
	p := filepath.Join(dir, "device.json")
	if loadJSON(p, &d) && len(d.ID) >= 16 {
		return d.ID
	}
	d.ID = randomToken(12)
	_ = saveJSON(p, d)
	return d.ID
}

// ---------- Paired (trusted) devices ----------

type TrustedDevice struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Platform      string    `json:"platform"`
	FP            string    `json:"fp"`
	Key           string    `json:"key"`
	AutoAccept    bool      `json:"autoAccept"`
	SyncClipboard bool      `json:"syncClipboard"`
	PairedAt      time.Time `json:"pairedAt"`
	LastURL       string    `json:"lastUrl"`
}

type pairSession struct {
	Token   string
	PIN     string
	Expires time.Time
	Done    *TrustedDevice
	Tries   int
}

type Trust struct {
	mu      sync.Mutex
	path    string
	devices map[string]*TrustedDevice
	session *pairSession
}

func newTrust(dir string) *Trust {
	t := &Trust{path: filepath.Join(dir, "trusted.json"), devices: map[string]*TrustedDevice{}}
	var list []*TrustedDevice
	if loadJSON(t.path, &list) {
		for _, d := range list {
			t.devices[d.ID] = d
		}
	}
	return t
}

func (t *Trust) saveLocked() {
	list := make([]*TrustedDevice, 0, len(t.devices))
	for _, d := range t.devices {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].PairedAt.Before(list[j].PairedAt) })
	_ = saveJSON(t.path, list)
}

func (t *Trust) List() []TrustedDevice {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TrustedDevice, 0, len(t.devices))
	for _, d := range t.devices {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PairedAt.Before(out[j].PairedAt) })
	return out
}

func (t *Trust) Get(id string) *TrustedDevice {
	t.mu.Lock()
	defer t.mu.Unlock()
	if d, ok := t.devices[id]; ok {
		c := *d
		return &c
	}
	return nil
}

func (t *Trust) Update(id string, fn func(d *TrustedDevice)) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	d, ok := t.devices[id]
	if !ok {
		return false
	}
	fn(d)
	t.saveLocked()
	return true
}

func (t *Trust) Put(d *TrustedDevice) {
	t.mu.Lock()
	t.devices[d.ID] = d
	t.saveLocked()
	t.mu.Unlock()
}

func (t *Trust) Remove(id string) {
	t.mu.Lock()
	delete(t.devices, id)
	t.saveLocked()
	t.mu.Unlock()
}

// Check validates the credentials a paired device sends with its requests.
func (t *Trust) Check(id, key string) *TrustedDevice {
	if id == "" || key == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	d, ok := t.devices[id]
	if !ok || subtle.ConstantTimeCompare([]byte(d.Key), []byte(key)) != 1 {
		return nil
	}
	c := *d
	return &c
}

func (t *Trust) StartSession() pairSession {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.session = &pairSession{Token: randomToken(16), PIN: randomCode(), Expires: time.Now().Add(5 * time.Minute)}
	return *t.session
}

func (t *Trust) CancelSession() {
	t.mu.Lock()
	t.session = nil
	t.mu.Unlock()
}

func (t *Trust) SessionStatus() (active bool, done *TrustedDevice, expires time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.session == nil {
		return false, nil, time.Time{}
	}
	return time.Now().Before(t.session.Expires), t.session.Done, t.session.Expires
}

// Redeem checks a QR token or a PIN against the open pairing session.
func (t *Trust) Redeem(token, pin string, d *TrustedDevice) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.session
	if s == nil || time.Now().After(s.Expires) || s.Done != nil {
		return fmt.Errorf("Pairing isn't open on that device. Open Pair a device there and try again.")
	}
	ok := (token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.Token)) == 1) ||
		(pin != "" && subtle.ConstantTimeCompare([]byte(pin), []byte(s.PIN)) == 1)
	if !ok {
		s.Tries++
		if s.Tries >= 5 {
			t.session = nil
			return fmt.Errorf("Too many wrong PINs. Start pairing again on that device.")
		}
		return fmt.Errorf("That PIN doesn't match. Check the PIN shown on the other device.")
	}
	t.devices[d.ID] = d
	s.Done = d
	t.saveLocked()
	return nil
}

// ---------- Settings ----------

type Settings struct {
	RequirePin        bool   `json:"requirePin"`
	AccessPin         string `json:"accessPin"`
	WatchClipboard    bool   `json:"watchClipboard"`
	AutoCopyClipboard bool   `json:"autoCopyClipboard"`
	AllowNearby       bool   `json:"allowNearby"`
	StartAtLogin      bool   `json:"startAtLogin"`
	Secret            string `json:"secret"`
}

type SettingsStore struct {
	mu   sync.Mutex
	path string
	s    Settings
}

func newSettings(dir string) *SettingsStore {
	st := &SettingsStore{path: filepath.Join(dir, "settings.json")}
	st.s.AutoCopyClipboard = true
	st.s.AllowNearby = true
	loadJSON(st.path, &st.s)
	if st.s.Secret == "" {
		st.s.Secret = randomToken(32)
		_ = saveJSON(st.path, st.s)
	}
	if st.s.AccessPin == "" {
		st.s.AccessPin = randomCode()
		_ = saveJSON(st.path, st.s)
	}
	return st
}

func (st *SettingsStore) Get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.s
}

func (st *SettingsStore) Update(fn func(s *Settings)) Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	fn(&st.s)
	_ = saveJSON(st.path, st.s)
	return st.s
}

// guestToken is the cookie value that proves a browser entered the access PIN.
func (st *SettingsStore) guestToken() string {
	s := st.Get()
	m := hmac.New(sha256.New, []byte(s.Secret))
	m.Write([]byte("guest:" + s.AccessPin))
	return hex.EncodeToString(m.Sum(nil))
}

// ---------- History ----------

type HistoryEntry struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"`
	Direction string    `json:"direction"` // "sent" | "received"
	Kind      string    `json:"kind"`      // "files" | "text" | "clipboard"
	Summary   string    `json:"summary"`
	Count     int       `json:"count"`
	Size      int64     `json:"size"`
	Peer      string    `json:"peer,omitempty"` // device name, or "Code 123456"
	Code      string    `json:"code,omitempty"`
	Path      string    `json:"path,omitempty"` // where received files were saved
	Text      string    `json:"text,omitempty"` // short text/clipboard content
	Status    string    `json:"status,omitempty"`
}

type History struct {
	mu      sync.Mutex
	path    string
	entries []HistoryEntry
}

const historyMax = 500

func newHistory(dir string) *History {
	h := &History{path: filepath.Join(dir, "history.json")}
	loadJSON(h.path, &h.entries)
	return h
}

func (h *History) Add(e HistoryEntry) string {
	if e.ID == "" {
		e.ID = randomToken(8)
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if len(e.Text) > 2000 {
		e.Text = e.Text[:2000]
	}
	h.mu.Lock()
	h.entries = append([]HistoryEntry{e}, h.entries...)
	if len(h.entries) > historyMax {
		h.entries = h.entries[:historyMax]
	}
	_ = saveJSON(h.path, h.entries)
	h.mu.Unlock()
	return e.ID
}

func (h *History) Update(id string, fn func(e *HistoryEntry)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.entries {
		if h.entries[i].ID == id {
			fn(&h.entries[i])
			_ = saveJSON(h.path, h.entries)
			return
		}
	}
}

func (h *History) List() []HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]HistoryEntry{}, h.entries...)
}

func (h *History) Clear() {
	h.mu.Lock()
	h.entries = nil
	_ = saveJSON(h.path, h.entries)
	h.mu.Unlock()
}

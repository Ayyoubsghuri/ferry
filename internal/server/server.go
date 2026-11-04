package server

import (
	"archive/zip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	DataDir  string
	Port     int    // 0 = try 80, then 7070…
	Name     string // device name shown to others
	Hostname string // mDNS name, "ferry" → ferry.local
	NoMDNS   bool
	IPHints  []string
	Version  string
	UI       fs.FS // built web UI

	Platform       string             // reported to other devices (default: runtime.GOOS)
	SaveDir        string             // where files sent straight to this device are saved ("" = let the app UI save them)
	Clipboard      ClipboardIO        // system clipboard access (nil when unavailable)
	OpenPath       func(path string)  // opens a folder in the file manager (desktop only)
	IPFile         string             // file with current LAN addresses, kept up to date by the Android app
	DiscoveryPort  int                // UDP port for finding other devices
	DiscoveryExtra []string           // extra announce targets (testing)
	SetStartAtLogin func(on bool) error // desktop: start Ferry when the user logs in
	AutoAccept     bool               // accept everything from paired devices (headless servers)
	NoLocalOwner   bool               // testing: browsers on this computer's LAN address count as guests
}

type Server struct {
	cfg   Config
	store *Store
	port  int

	ipMu     sync.Mutex
	ips      []string
	ipsAt    time.Time
	limiter  *limiter
	httpSrvs []*http.Server

	me       Identity
	trust    *Trust
	settings *SettingsStore
	hist     *History
	inbox    *Inbox
	chats    *Chats
	guests   *guestState
	disc     *Discovery

	clipMu   sync.Mutex
	lastClip string
}

// Port 80 gives the cleanest address (http://192.168.1.5); 7070 is the fallback and
// what the Android app uses. Each port serves both plain HTTP (browsers) and TLS (devices).
var DefaultPorts = []int{80, 7070, 7071, 7072}

// Start opens the port and begins serving. It returns once the server is listening.
func Start(cfg Config) (*Server, error) {
	if cfg.Hostname == "" {
		cfg.Hostname = "ferry"
	}
	if cfg.Name == "" {
		cfg.Name, _ = os.Hostname()
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	store, err := NewStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if cfg.Platform == "" {
		cfg.Platform = runtime.GOOS
	}
	if cfg.DiscoveryPort == 0 {
		cfg.DiscoveryPort = DefaultDiscoveryPort
	}
	s := &Server{
		cfg: cfg, store: store, limiter: newLimiter(),
		trust: newTrust(cfg.DataDir), settings: newSettings(cfg.DataDir),
		hist: newHistory(cfg.DataDir), inbox: newInbox(), chats: newChats(cfg.DataDir), guests: newGuestState(),
	}

	ports := DefaultPorts
	if cfg.Port != 0 {
		ports = []int{cfg.Port}
	}
	var raw net.Listener
	for _, p := range ports {
		raw, err = net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err == nil {
			s.port = p
			break
		}
		log.Printf("port %d unavailable: %v", p, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("could not open a port: %w", err)
	}

	cert, fp, err := loadOrCreateCert(cfg.DataDir, []string{cfg.Hostname + ".local", "localhost"}, s.LANIPs())
	if err != nil {
		return nil, err
	}
	s.me = Identity{ID: loadDeviceID(cfg.DataDir), Name: cfg.Name, Platform: cfg.Platform, Port: s.port, FP: fp}
	tlsSide, plainSide := splitListener(raw)

	app := s.routes()
	secure := &http.Server{
		Handler:           app,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 20 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0), // browsers reject the self-signed cert noisily
	}
	plain := &http.Server{Handler: s.plainHandler(app), ReadHeaderTimeout: 20 * time.Second}
	s.httpSrvs = []*http.Server{secure, plain}
	go func() { _ = secure.Serve(tls.NewListener(tlsSide, secure.TLSConfig)) }()
	go func() { _ = plain.Serve(plainSide) }()

	if !cfg.NoMDNS {
		startMDNS(cfg.Hostname, s.LANIPs)
	}
	s.disc = newDiscovery(cfg.DiscoveryPort, cfg.DiscoveryExtra, func() discPacket {
		return discPacket{ID: s.me.ID, Name: s.me.Name, Platform: s.me.Platform, Port: s.port, FP: s.me.FP, Locked: s.settings.Get().RequirePin}
	}, s.LANIPs)
	go s.watchClipboard()
	log.Printf("Ferry %s listening on port %d (data: %s)", cfg.Version, s.port, cfg.DataDir)
	for _, u := range s.LANURLs() {
		log.Printf("  on your network: %s", u)
	}
	return s, nil
}

func (s *Server) Port() int { return s.port }

func (s *Server) LocalURL() string { return fmt.Sprintf("http://127.0.0.1:%d/", s.port) }

func (s *Server) portSuffix() string {
	if s.port == 80 {
		return ""
	}
	return fmt.Sprintf(":%d", s.port)
}

// MDNSURL and LANURLs are the addresses people open in a browser (plain HTTP, so no
// certificate warning). Devices talk to each other over HTTPS on the same port.
func (s *Server) MDNSURL() string {
	if s.cfg.NoMDNS {
		return ""
	}
	return "http://" + s.cfg.Hostname + ".local" + s.portSuffix()
}

func (s *Server) LANURLs() []string {
	out := []string{}
	for _, ip := range s.LANIPs() {
		out = append(out, "http://"+ip+s.portSuffix())
	}
	return out
}

// peerURLs are the HTTPS addresses other Ferry devices use.
func (s *Server) peerURLs() []string {
	out := []string{}
	for _, ip := range s.LANIPs() {
		out = append(out, baseURL(ip, s.port))
	}
	return out
}

func (s *Server) LANIPs() []string {
	s.ipMu.Lock()
	defer s.ipMu.Unlock()
	if time.Since(s.ipsAt) > 10*time.Second {
		hints := s.cfg.IPHints
		if s.cfg.IPFile != "" {
			if b, err := os.ReadFile(s.cfg.IPFile); err == nil {
				hints = append(strings.Fields(strings.ReplaceAll(string(b), ",", " ")), hints...)
			}
		}
		s.ips = LANIPs(hints)
		s.ipsAt = time.Now()
	}
	return s.ips
}

func (s *Server) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, h := range s.httpSrvs {
		_ = h.Shutdown(ctx)
	}
}

// Plain HTTP is what browsers use: the app window on this device (loopback) and other
// browsers on the network. Loopback requests must use a loopback host name, which blocks
// DNS-rebinding tricks against the owner-only API.
func (s *Server) plainHandler(app http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopback(r.RemoteAddr) {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			if host != "127.0.0.1" && host != "localhost" && host != "[::1]" && host != "::1" {
				http.Error(w, "use 127.0.0.1", http.StatusMisdirectedRequest)
				return
			}
		}
		app.ServeHTTP(w, r)
	})
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", s.handleInfo)
	mux.HandleFunc("POST /api/t", s.handleCreate)
	mux.HandleFunc("GET /api/t/{code}", s.handleGet)
	mux.HandleFunc("DELETE /api/t/{code}", s.handleDelete)
	mux.HandleFunc("PUT /api/t/{code}/f/{id}", s.handleUpload)
	mux.HandleFunc("GET /api/t/{code}/f/{id}", s.handleDownload)
	mux.HandleFunc("GET /api/t/{code}/f/{id}/{name...}", s.handleDownload)
	mux.HandleFunc("GET /api/t/{code}/zip", s.handleZip)
	mux.HandleFunc("GET /api/t/{code}/zipindex/{id}", s.handleZipIndex)
	mux.HandleFunc("GET /api/remote/zipindex", s.handleRemoteZipIndex)
	mux.HandleFunc("GET /api/exists/{code}", s.handleExists)
	mux.HandleFunc("GET /api/find/{code}", s.handleFind)
	mux.HandleFunc("GET /api/remote/share", s.handleRemoteShare)
	mux.HandleFunc("GET /api/remote/file", s.handleRemoteFile)
	mux.HandleFunc("GET /api/remote/zip", s.handleRemoteZip)

	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("POST /api/unlock", s.handleUnlock)
	mux.HandleFunc("PUT /api/settings", s.ownerOnly(s.handleSettings))
	mux.HandleFunc("GET /api/devices", s.handleDevices)
	mux.HandleFunc("PATCH /api/devices/{id}", s.ownerOnly(s.handleDeviceUpdate))
	mux.HandleFunc("DELETE /api/devices/{id}", s.ownerOnly(s.handleUnpair))
	mux.HandleFunc("POST /api/pair/start", s.ownerOnly(s.handlePairStart))
	mux.HandleFunc("GET /api/pair/status", s.ownerOnly(s.handlePairStatus))
	mux.HandleFunc("DELETE /api/pair", s.ownerOnly(s.handlePairCancel))
	mux.HandleFunc("POST /api/pair/join", s.ownerOnly(s.handlePairJoin))
	mux.HandleFunc("POST /api/pair/accept", s.handlePairAccept)
	mux.HandleFunc("POST /api/send", s.ownerOnly(s.handleSend))
	mux.HandleFunc("GET /api/outbox", s.ownerOnly(s.handleOutbox))
	mux.HandleFunc("GET /api/inbox", s.ownerOnly(s.handleInbox))
	mux.HandleFunc("POST /api/inbox/{id}/accept", s.ownerOnly(s.handleInboxAccept))
	mux.HandleFunc("POST /api/inbox/{id}/decline", s.ownerOnly(s.handleInboxDecline))
	mux.HandleFunc("POST /api/inbox/{id}/done", s.ownerOnly(s.handleInboxDone))
	mux.HandleFunc("DELETE /api/inbox/{id}", s.ownerOnly(s.handleInboxDismiss))
	mux.HandleFunc("POST /api/clipboard", s.ownerOnly(s.handleClipboardSend))
	mux.HandleFunc("GET /api/clipboard", s.ownerOnly(s.handleClipboardList))
	mux.HandleFunc("/api/history", s.ownerOnly(s.handleHistory))
	mux.HandleFunc("POST /api/open-folder", s.ownerOnly(s.handleOpenFolder))

	mux.HandleFunc("POST /api/peer/offer", s.peerOnly(s.handlePeerOffer))
	mux.HandleFunc("POST /api/peer/status", s.peerOnly(s.handlePeerStatus))
	mux.HandleFunc("POST /api/peer/clipboard", s.peerOnly(s.handlePeerClipboard))
	mux.HandleFunc("POST /api/peer/unpair", s.peerOnly(s.handlePeerUnpair))
	mux.HandleFunc("POST /api/peer/cancel", s.peerOnly(s.handlePeerCancel))
	mux.HandleFunc("POST /api/peer/message", s.peerOnly(s.handlePeerMessage))

	mux.HandleFunc("POST /api/nearby/offer", s.handleNearbyOffer)
	mux.HandleFunc("POST /api/nearby/status", s.handleNearbyStatus)
	mux.HandleFunc("POST /api/nearby/cancel", s.handleNearbyCancel)
	mux.HandleFunc("POST /api/nearby/message", s.handleNearbyMessage)

	mux.HandleFunc("POST /api/outbox/{id}/cancel", s.ownerOnly(s.handleOutboxCancel))
	mux.HandleFunc("POST /api/guest/hello", s.handleGuestHello)
	mux.HandleFunc("GET /api/guest/poll", s.handleGuestPoll)
	mux.HandleFunc("POST /api/guest/offer/{id}/{action}", s.handleGuestAnswer)
	mux.HandleFunc("POST /api/guest/send", s.handleGuestSend)
	mux.HandleFunc("POST /api/guest/cancel/{id}", s.handleGuestCancel)
	mux.HandleFunc("GET /api/messages", s.ownerOnly(s.handleMessagesGet))
	mux.HandleFunc("POST /api/messages", s.ownerOnly(s.handleMessageSend))
	mux.HandleFunc("POST /api/messages/read", s.ownerOnly(s.handleMessagesRead))
	mux.HandleFunc("GET /api/speed", s.handleSpeedDown)
	mux.HandleFunc("POST /api/speed", s.handleSpeedUp)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		jsonErr(w, http.StatusNotFound, "Unknown API route")
	})
	mux.Handle("/", s.uiHandler())
	return securityHeaders(s.gate(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) uiHandler() http.Handler {
	files := http.FileServer(http.FS(s.cfg.UI))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" {
			if st, err := fs.Stat(s.cfg.UI, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		// Single-page app: every other path gets index.html.
		b, err := fs.ReadFile(s.cfg.UI, "index.html")
		if err != nil {
			http.Error(w, "UI not built", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	})
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func contentDisposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, ascii, url.PathEscape(name))
}

// cleanName keeps folder structure but strips anything that could escape it.
func cleanName(n string) string {
	n = strings.ReplaceAll(n, "\\", "/")
	var parts []string
	for _, p := range strings.Split(n, "/") {
		p = strings.TrimSpace(p)
		if p == "" || p == "." || p == ".." {
			continue
		}
		parts = append(parts, p)
	}
	out := strings.Join(parts, "/")
	if out == "" {
		out = "file"
	}
	if len(out) > 500 {
		out = out[len(out)-500:]
	}
	return out
}

// ---------- rate limiting for code guessing ----------

type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLimiter() *limiter { return &limiter{hits: map[string][]time.Time{}} }

const missLimit = 40 // failed code lookups per minute per address

func (l *limiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := time.Now().Add(-time.Minute)
	h := l.hits[ip]
	for len(h) > 0 && h[0].Before(cut) {
		h = h[1:]
	}
	l.hits[ip] = h
	return len(h) >= missLimit
}

func (l *limiter) miss(ip string) {
	l.mu.Lock()
	l.hits[ip] = append(l.hits[ip], time.Now())
	l.mu.Unlock()
}

// lookup finds a transfer by code, applying the guess limiter.
func (s *Server) lookup(w http.ResponseWriter, r *http.Request) *Transfer {
	ip := clientIP(r)
	if s.limiter.blocked(ip) {
		jsonErr(w, http.StatusTooManyRequests, "Too many wrong codes. Wait a minute and try again.")
		return nil
	}
	t := s.store.Get(r.PathValue("code"))
	if t == nil {
		s.limiter.miss(ip)
		jsonErr(w, http.StatusNotFound, "No share with that code. It may have expired.")
		return nil
	}
	return t
}

// ---------- handlers ----------

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"app":       "ferry",
		"id":        s.me.ID,
		"fp":        s.me.FP,
		"locked":    s.settings.Get().RequirePin,
		"name":      s.cfg.Name,
		"version":   s.cfg.Version,
		"platform":  s.me.Platform,
		"port":      s.port,
		"local":     isLoopback(r.RemoteAddr),
		"mdnsUrl":   s.MDNSURL(),
		"lanUrls":   s.LANURLs(),
		"transfers": s.store.Count(),
	})
}

type createReq struct {
	Text           string `json:"text"`
	Encrypted      bool   `json:"encrypted"`
	Salt           string `json:"salt"`
	Check          string `json:"check"`
	ExpiresMinutes int    `json:"expiresMinutes"`
	BurnAfter      bool   `json:"burnAfter"`
	Sender         string `json:"sender"`
	Files          []File `json:"files"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		jsonErr(w, http.StatusUnsupportedMediaType, "Expected JSON")
		return
	}
	var req createReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if req.Text == "" && len(req.Files) == 0 {
		jsonErr(w, http.StatusBadRequest, "Add a file or some text first")
		return
	}
	if len(req.Text) > 4<<20 {
		jsonErr(w, http.StatusRequestEntityTooLarge, "Text is too long (4 MB max). Send it as a file instead.")
		return
	}
	if len(req.Files) > 20000 {
		jsonErr(w, http.StatusBadRequest, "Too many files in one share (20,000 max)")
		return
	}
	if req.Encrypted && (req.Salt == "" || req.Check == "") {
		jsonErr(w, http.StatusBadRequest, "Encrypted shares need a salt and check value")
		return
	}
	mins := req.ExpiresMinutes
	if mins <= 0 {
		mins = 60
	}
	if mins > 7*24*60 {
		mins = 7 * 24 * 60
	}
	now := time.Now()
	t := &Transfer{
		Sender:    trim(req.Sender, 80),
		CreatedAt: now,
		ExpiresAt: now.Add(time.Duration(mins) * time.Minute),
		Text:      req.Text,
		Encrypted: req.Encrypted,
		Salt:      trim(req.Salt, 200),
		Check:     trim(req.Check, 500),
		BurnAfter: req.BurnAfter,
	}
	for _, f := range req.Files {
		if f.Size < 0 || f.StoredSize < 0 {
			jsonErr(w, http.StatusBadRequest, "Invalid file size")
			return
		}
		stored := f.StoredSize
		if !req.Encrypted {
			stored = f.Size
		}
		t.Files = append(t.Files, &File{Name: cleanName(f.Name), Size: f.Size, StoredSize: stored, Type: trim(f.Type, 120)})
	}
	if err := s.store.Create(t); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.isOwner(r) {
		kind := "files"
		if len(t.Files) == 0 {
			kind = "text"
		}
		var files []File
		for _, f := range t.Files {
			files = append(files, *f)
		}
		text := ""
		if !t.Encrypted {
			text = t.Text
		}
		t.HistoryID = s.hist.Add(HistoryEntry{Direction: "sent", Kind: kind, Summary: summarize(files, text),
			Count: len(t.Files), Size: t.TotalSize(), Code: t.Code, Peer: "Code " + t.Code, Text: text})
	}
	writeJSON(w, http.StatusCreated, s.view(t, true))
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Server) view(t *Transfer, owner bool) map[string]any {
	files := make([]File, len(t.Files))
	for i, f := range t.Files {
		files[i] = *f
	}
	v := map[string]any{
		"code":      t.Code,
		"sender":    t.Sender,
		"createdAt": t.CreatedAt,
		"expiresAt": t.ExpiresAt,
		"text":      t.Text,
		"encrypted": t.Encrypted,
		"salt":      t.Salt,
		"check":     t.Check,
		"burnAfter": t.BurnAfter,
		"downloads": t.Downloads,
		"views":     t.Views,
		"files":     files,
		"ready":     t.Ready(),
		"totalSize": t.TotalSize(),
		"isOwner":   owner,
	}
	if owner {
		v["owner"] = t.Owner
	}
	return v
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	t := s.lookup(w, r)
	if t == nil {
		return
	}
	owner := t.IsOwner(r.Header.Get("X-Owner"))
	var v map[string]any
	s.store.With(func() {
		if !owner {
			t.Views++
		}
		v = s.view(t, owner)
	})
	if !owner && len(t.Files) == 0 {
		s.store.ArmBurn(t) // a text-only share counts as received once opened
	}
	writeJSON(w, 200, v)
}

func (s *Server) handleExists(w http.ResponseWriter, r *http.Request) {
	if t := s.lookup(w, r); t != nil {
		writeJSON(w, 200, map[string]any{"ok": true, "name": s.cfg.Name})
	}
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	t := s.store.Get(r.PathValue("code"))
	if t == nil {
		writeJSON(w, 200, map[string]any{"deleted": true})
		return
	}
	if !t.IsOwner(r.Header.Get("X-Owner")) {
		jsonErr(w, http.StatusForbidden, "Only the device that created this share can delete it")
		return
	}
	s.store.Delete(t.Code)
	writeJSON(w, 200, map[string]any{"deleted": true})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	t := s.store.Get(r.PathValue("code"))
	if t == nil {
		jsonErr(w, http.StatusNotFound, "This share has expired")
		return
	}
	if !t.IsOwner(r.Header.Get("X-Owner")) {
		jsonErr(w, http.StatusForbidden, "Not allowed")
		return
	}
	var f *File
	var received, stored int64
	s.store.With(func() {
		f = t.File(r.PathValue("id"))
		if f != nil {
			received, stored = f.Received, f.StoredSize
		}
	})
	if f == nil {
		jsonErr(w, http.StatusNotFound, "Unknown file")
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 || offset > received {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "Upload out of order", "received": received})
		return
	}
	path := s.store.FilePath(t.Code, f.ID)
	fh, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	defer fh.Close()
	if _, err := fh.Seek(offset, io.SeekStart); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	limit := stored - offset
	n, err := io.Copy(fh, io.LimitReader(r.Body, limit))
	end := offset + n
	if err == nil {
		var extra [1]byte
		if m, _ := r.Body.Read(extra[:]); m > 0 {
			s.store.With(func() {
				if end > f.Received {
					f.Received = end
				}
			})
			jsonErr(w, http.StatusRequestEntityTooLarge, "More data than declared")
			return
		}
	}
	s.store.With(func() {
		if end > f.Received {
			f.Received = end
		}
		received = f.Received
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Upload interrupted", "received": received})
		return
	}
	writeJSON(w, 200, map[string]any{"received": received, "complete": received >= stored})
}

type countingWriter struct {
	http.ResponseWriter
	n      int64
	status int
}

func (c *countingWriter) WriteHeader(code int) { c.status = code; c.ResponseWriter.WriteHeader(code) }
func (c *countingWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = 200
	}
	n, err := c.ResponseWriter.Write(b)
	c.n += int64(n)
	return n, err
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	t := s.lookup(w, r)
	if t == nil {
		return
	}
	var f *File
	var ready bool
	s.store.With(func() {
		f = t.File(r.PathValue("id"))
		if f != nil {
			ready = f.Complete()
		}
	})
	if f == nil {
		jsonErr(w, http.StatusNotFound, "Unknown file")
		return
	}
	if !ready {
		jsonErr(w, http.StatusConflict, "This file is still uploading")
		return
	}
	fh, err := os.Open(s.store.FilePath(t.Code, f.ID))
	if err != nil {
		jsonErr(w, http.StatusGone, "File is no longer available")
		return
	}
	defer fh.Close()
	base := path.Base(f.Name)
	ctype := f.Type
	if t.Encrypted || ctype == "" {
		ctype = "application/octet-stream"
		if !t.Encrypted {
			if c := mime.TypeByExtension(filepath.Ext(base)); c != "" {
				ctype = c
			}
		}
	}
	inline := r.URL.Query().Get("inline") == "1" && !t.Encrypted
	if inline {
		// Only media, PDFs and plain text are shown in the browser; HTML/SVG never run here.
		switch {
		case strings.HasPrefix(ctype, "image/") && !strings.Contains(ctype, "svg"),
			strings.HasPrefix(ctype, "video/"), strings.HasPrefix(ctype, "audio/"), ctype == "application/pdf":
		case strings.HasPrefix(ctype, "text/"):
			ctype = "text/plain; charset=utf-8"
		default:
			inline = false
		}
	}
	w.Header().Set("Content-Type", ctype)
	if ctype != "application/pdf" {
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'; media-src 'self'")
	}
	if !inline {
		w.Header().Set("Content-Disposition", contentDisposition(base))
	}
	w.Header().Set("Cache-Control", "no-store")
	cw := &countingWriter{ResponseWriter: w}
	rangeHdr := r.Header.Get("Range")
	http.ServeContent(cw, r, "", t.CreatedAt, fh)

	full := false
	if rangeHdr == "" {
		full = cw.status == 200 && cw.n == f.StoredSize
	} else if strings.HasPrefix(rangeHdr, "bytes=") && strings.HasSuffix(rangeHdr, "-") {
		start, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(rangeHdr, "bytes="), "-"), 10, 64)
		full = err == nil && cw.status == 206 && start+cw.n == f.StoredSize
	}
	if full && r.Method == http.MethodGet && !t.IsOwner(r.URL.Query().Get("owner")) {
		s.store.With(func() { t.Downloads++ })
		s.store.ArmBurn(t)
		s.noteDownload(t)
	}
}

func (s *Server) handleZip(w http.ResponseWriter, r *http.Request) {
	t := s.lookup(w, r)
	if t == nil {
		return
	}
	if t.Encrypted {
		jsonErr(w, http.StatusBadRequest, "Locked shares are downloaded file by file so they can be unlocked on your device")
		return
	}
	var files []File
	ready := true
	s.store.With(func() {
		ready = t.Ready()
		for _, f := range t.Files {
			files = append(files, *f)
		}
	})
	if !ready {
		jsonErr(w, http.StatusConflict, "Some files are still uploading")
		return
	}
	name := "Ferry-" + t.Code
	if len(files) > 0 {
		if first := strings.SplitN(files[0].Name, "/", 2); len(first) == 2 {
			common := true
			for _, f := range files {
				if !strings.HasPrefix(f.Name, first[0]+"/") {
					common = false
					break
				}
			}
			if common {
				name = first[0]
			}
		}
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition(name+".zip"))
	w.Header().Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)
	ok := true
	for _, f := range files {
		hdr := &zip.FileHeader{Name: f.Name, Method: zip.Store, Modified: t.CreatedAt}
		dst, err := zw.CreateHeader(hdr)
		if err != nil {
			ok = false
			break
		}
		fh, err := os.Open(s.store.FilePath(t.Code, f.ID))
		if err != nil {
			ok = false
			break
		}
		_, err = io.Copy(dst, fh)
		fh.Close()
		if err != nil {
			ok = false
			break
		}
	}
	if ok && zw.Close() == nil {
		s.store.With(func() { t.Downloads++ })
		s.store.ArmBurn(t)
		s.noteDownload(t)
	}
}

// ---------- finding a code on other devices ----------

func (s *Server) noteDownload(t *Transfer) {
	if t.HistoryID == "" {
		return
	}
	n := t.Downloads
	s.hist.Update(t.HistoryID, func(e *HistoryEntry) {
		if strings.HasPrefix(e.Status, "Sent to") || e.Status == "" || strings.HasPrefix(e.Status, "Downloaded") {
			if n == 1 {
				e.Status = "Downloaded once"
			} else {
				e.Status = fmt.Sprintf("Downloaded %d times", n)
			}
		}
	})
}

func (s *Server) handleFind(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	ip := clientIP(r)
	if s.limiter.blocked(ip) {
		jsonErr(w, http.StatusTooManyRequests, "Too many wrong codes. Wait a minute and try again.")
		return
	}
	if s.store.Get(code) != nil {
		writeJSON(w, 200, map[string]any{"where": "here"})
		return
	}
	try := func(peers []PeerInfo) *PeerInfo {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		hit := make(chan PeerInfo, len(peers))
		var wg sync.WaitGroup
		for _, p := range peers {
			wg.Add(1)
			go func(p PeerInfo) {
				defer wg.Done()
				req, _ := http.NewRequestWithContext(ctx, "GET", p.URL+"/api/exists/"+url.PathEscape(code), nil)
				client := insecureClient
				if d := s.trust.Get(p.ID); d != nil {
					s.authHeaders(req.Header, d)
					client = pinnedClient(d.FP, false)
				}
				resp, err := client.Do(req)
				if err != nil {
					return
				}
				resp.Body.Close()
				if resp.StatusCode == 200 {
					hit <- p
				}
			}(p)
		}
		go func() { wg.Wait(); close(hit) }()
		if p, ok := <-hit; ok {
			return &p
		}
		return nil
	}
	peers := s.disc.Online()
	if len(peers) == 0 {
		peers = s.disc.Refresh(false)
	}
	p := try(peers)
	if p == nil {
		p = try(s.disc.Refresh(true))
	}
	if p != nil {
		writeJSON(w, 200, map[string]any{"where": "peer", "url": p.URL, "name": p.Name, "id": p.ID})
		return
	}
	s.limiter.miss(ip)
	jsonErr(w, http.StatusNotFound, "No share with that code on this device or nearby devices")
}

// ---------- speed test ----------

var speedBuf = func() []byte {
	b := make([]byte, 256<<10)
	for i := range b {
		b[i] = byte(i*7 + i>>8)
	}
	return b
}()

func (s *Server) handleSpeedDown(w http.ResponseWriter, r *http.Request) {
	mb, _ := strconv.Atoi(r.URL.Query().Get("mb"))
	if mb <= 0 || mb > 200 {
		mb = 8
	}
	total := int64(mb) << 20
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(total, 10))
	w.Header().Set("Cache-Control", "no-store")
	for total > 0 {
		n := int64(len(speedBuf))
		if n > total {
			n = total
		}
		if _, err := w.Write(speedBuf[:n]); err != nil {
			return
		}
		total -= n
	}
}

func (s *Server) handleSpeedUp(w http.ResponseWriter, r *http.Request) {
	n, err := io.Copy(io.Discard, io.LimitReader(r.Body, 200<<20))
	if err != nil && !errors.Is(err, io.EOF) {
		jsonErr(w, 400, "interrupted")
		return
	}
	writeJSON(w, 200, map[string]any{"bytes": n})
}

// handleZipIndex lists what's inside a .zip without downloading it.
func (s *Server) handleZipIndex(w http.ResponseWriter, r *http.Request) {
	t := s.lookup(w, r)
	if t == nil {
		return
	}
	var f *File
	s.store.With(func() { f = t.File(r.PathValue("id")) })
	if f == nil || t.Encrypted || !f.Complete() || !strings.HasSuffix(strings.ToLower(f.Name), ".zip") {
		jsonErr(w, 400, "Only finished, unlocked .zip files can be listed")
		return
	}
	fh, err := os.Open(s.store.FilePath(t.Code, f.ID))
	if err != nil {
		jsonErr(w, http.StatusGone, "File is no longer available")
		return
	}
	defer fh.Close()
	zr, err := zip.NewReader(fh, f.StoredSize)
	if err != nil {
		jsonErr(w, 400, "This .zip file can't be read")
		return
	}
	type entry struct {
		Name string `json:"name"`
		Size uint64 `json:"size"`
		Dir  bool   `json:"dir"`
	}
	list := []entry{}
	for i, e := range zr.File {
		if i >= 5000 {
			break
		}
		list = append(list, entry{Name: e.Name, Size: e.UncompressedSize64, Dir: strings.HasSuffix(e.Name, "/")})
	}
	writeJSON(w, 200, map[string]any{"entries": list, "total": len(zr.File)})
}

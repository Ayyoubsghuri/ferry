package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ---------- who is asking ----------

// isOwner is true for the device's own app window (desktop app or Android app), which
// talks to the server over loopback. Other browsers on the network are guests.
func (s *Server) isOwner(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if isLoopback(r.RemoteAddr) {
		return host == "127.0.0.1" || host == "localhost" || host == "::1"
	}
	// A browser on this same computer using its network address. Only IP-literal or
	// .local host names count, which keeps DNS-rebinding pages out.
	if s.cfg.NoLocalOwner || !s.isOwnIP(clientIP(r)) {
		return false
	}
	return net.ParseIP(host) != nil || host == "localhost" || strings.EqualFold(host, s.cfg.Hostname+".local")
}

func (s *Server) peerAuth(r *http.Request) *TrustedDevice {
	return s.trust.Check(r.Header.Get("X-SL-Device"), r.Header.Get("X-SL-Key"))
}

func (s *Server) ownerOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isOwner(r) {
			jsonErr(w, http.StatusForbidden, "Only available in the Ferry app on this device")
			return
		}
		h(w, r)
	}
}

func (s *Server) peerOnly(h func(http.ResponseWriter, *http.Request, *TrustedDevice)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d := s.peerAuth(r)
		if d == nil {
			jsonErr(w, http.StatusUnauthorized, "This device isn't paired")
			return
		}
		h(w, r, d)
	}
}

// gate enforces the optional access PIN for other browsers on the network.
func (s *Server) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !strings.HasPrefix(p, "/api/") || !s.settings.Get().RequirePin || s.isOwner(r) || s.peerAuth(r) != nil {
			next.ServeHTTP(w, r)
			return
		}
		switch {
		case p == "/api/info", p == "/api/me", p == "/api/unlock", p == "/api/pair/accept",
			strings.HasPrefix(p, "/api/peer/"), strings.HasPrefix(p, "/api/nearby/"), s.offerTokenAllows(r):
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie("sl_guest"); err == nil && c.Value == s.settings.guestToken() {
			next.ServeHTTP(w, r)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "This device is locked. Enter its access PIN.", "locked": true})
	})
}

// ---------- this device ----------

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	owner := s.isOwner(r)
	st := s.settings.Get()
	guestOK := true
	if st.RequirePin && !owner {
		c, err := r.Cookie("sl_guest")
		guestOK = err == nil && c.Value == s.settings.guestToken()
	}
	v := map[string]any{
		"id":       s.me.ID,
		"name":     s.me.Name,
		"platform": s.me.Platform,
		"owner":    owner,
		"locked":   !guestOK,
	}
	if owner {
		v["settings"] = map[string]any{
			"requirePin":        st.RequirePin,
			"accessPin":         st.AccessPin,
			"watchClipboard":    st.WatchClipboard,
			"autoCopyClipboard": st.AutoCopyClipboard,
			"allowNearby":       st.AllowNearby,
			"startAtLogin":      st.StartAtLogin,
		}
		v["startAtLoginSupported"] = s.cfg.SetStartAtLogin != nil
		v["clipboardSupported"] = s.cfg.Clipboard != nil
		v["saveDir"] = s.cfg.SaveDir
		v["canOpenFolder"] = s.cfg.OpenPath != nil && s.cfg.SaveDir != ""
	}
	writeJSON(w, 200, v)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequirePin        *bool `json:"requirePin"`
		WatchClipboard    *bool `json:"watchClipboard"`
		AutoCopyClipboard *bool `json:"autoCopyClipboard"`
		AllowNearby       *bool `json:"allowNearby"`
		StartAtLogin      *bool `json:"startAtLogin"`
		NewPin            bool  `json:"newPin"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req) != nil {
		jsonErr(w, 400, "Invalid request")
		return
	}
	if req.StartAtLogin != nil && s.cfg.SetStartAtLogin != nil {
		if err := s.cfg.SetStartAtLogin(*req.StartAtLogin); err != nil {
			jsonErr(w, 500, "Couldn't change the startup setting: "+err.Error())
			return
		}
	}
	st := s.settings.Update(func(st *Settings) {
		if req.AllowNearby != nil {
			st.AllowNearby = *req.AllowNearby
		}
		if req.StartAtLogin != nil && s.cfg.SetStartAtLogin != nil {
			st.StartAtLogin = *req.StartAtLogin
		}
		if req.RequirePin != nil {
			st.RequirePin = *req.RequirePin
		}
		if req.WatchClipboard != nil {
			st.WatchClipboard = *req.WatchClipboard
		}
		if req.AutoCopyClipboard != nil {
			st.AutoCopyClipboard = *req.AutoCopyClipboard
		}
		if req.NewPin {
			st.AccessPin = randomCode()
		}
	})
	if req.WatchClipboard != nil && *req.WatchClipboard {
		s.resetClipboardWatch()
	}
	writeJSON(w, 200, map[string]any{
		"requirePin": st.RequirePin, "accessPin": st.AccessPin,
		"watchClipboard": st.WatchClipboard, "autoCopyClipboard": st.AutoCopyClipboard,
		"allowNearby": st.AllowNearby, "startAtLogin": st.StartAtLogin,
	})
}

func (s *Server) handleUnlock(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.limiter.blocked(ip) {
		jsonErr(w, http.StatusTooManyRequests, "Too many wrong PINs. Wait a minute and try again.")
		return
	}
	var req struct{ Pin string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	if strings.TrimSpace(req.Pin) != s.settings.Get().AccessPin {
		s.limiter.miss(ip)
		jsonErr(w, http.StatusForbidden, "That PIN isn't right. It's shown in Ferry on this device, under Devices.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "sl_guest", Value: s.settings.guestToken(), Path: "/", MaxAge: 30 * 24 * 3600,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil,
	})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- devices ----------

type deviceView struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Platform      string    `json:"platform"`
	URL           string    `json:"url,omitempty"`
	Online        bool      `json:"online"`
	Paired        bool      `json:"paired"`
	Locked        bool      `json:"locked,omitempty"`
	AutoAccept    bool      `json:"autoAccept,omitempty"`
	SyncClipboard bool      `json:"syncClipboard,omitempty"`
	PairedAt      time.Time `json:"pairedAt,omitempty"`
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	var online []PeerInfo
	switch r.URL.Query().Get("refresh") {
	case "1":
		online = s.disc.Refresh(false)
	case "scan":
		online = s.disc.Refresh(true)
	default:
		online = s.disc.Online()
	}
	byID := map[string]PeerInfo{}
	for _, p := range online {
		byID[p.ID] = p
	}
	paired := []deviceView{}
	nearby := []deviceView{}
	owner := s.isOwner(r)
	trusted := map[string]bool{}
	for _, d := range s.trust.List() {
		trusted[d.ID] = true
		p, on := byID[d.ID]
		if !owner {
			continue
		}
		v := deviceView{ID: d.ID, Name: d.Name, Platform: d.Platform, Online: on, Paired: true,
			AutoAccept: d.AutoAccept, SyncClipboard: d.SyncClipboard, PairedAt: d.PairedAt}
		if on {
			v.URL, v.Name = p.URL, p.Name
		}
		paired = append(paired, v)
	}
	if !owner {
		// A browser can send to the device it's connected to.
		writeJSON(w, 200, map[string]any{"paired": []deviceView{}, "nearby": []deviceView{
			{ID: "host", Name: s.me.Name, Platform: s.me.Platform, Online: true},
		}})
		return
	}
	for _, p := range online {
		if trusted[p.ID] {
			continue
		}
		nearby = append(nearby, deviceView{ID: p.ID, Name: p.Name, Platform: p.Platform, URL: p.URL, Online: true, Locked: p.Locked})
	}
	for _, g := range s.onlineGuests() {
		nearby = append(nearby, deviceView{ID: "g:" + g.ID, Name: g.Name, Platform: "browser", Online: true})
	}
	writeJSON(w, 200, map[string]any{"paired": paired, "nearby": nearby})
}

func (s *Server) handleDeviceUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AutoAccept    *bool `json:"autoAccept"`
		SyncClipboard *bool `json:"syncClipboard"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil {
		jsonErr(w, 400, "Invalid request")
		return
	}
	ok := s.trust.Update(r.PathValue("id"), func(d *TrustedDevice) {
		if req.AutoAccept != nil {
			d.AutoAccept = *req.AutoAccept
		}
		if req.SyncClipboard != nil {
			d.SyncClipboard = *req.SyncClipboard
		}
	})
	if !ok {
		jsonErr(w, 404, "Not a paired device")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleUnpair(w http.ResponseWriter, r *http.Request) {
	d := s.trust.Get(r.PathValue("id"))
	if d == nil {
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	go func() { _ = s.callDevice(d, "POST", "/api/peer/unpair", map[string]any{}, nil) }()
	s.trust.Remove(d.ID)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handlePeerUnpair(w http.ResponseWriter, r *http.Request, d *TrustedDevice) {
	s.trust.Remove(d.ID)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- pairing ----------
//
// Device A opens pairing and shows a QR code (its address, a one-time token and the start of
// its key fingerprint) plus a 6-digit PIN. Device B scans the QR or picks A from the nearby
// list and types the PIN. B's server then contacts A's, checks A's fingerprint, and both
// store each other with a shared secret and each other's key fingerprint.

// bestURLFor picks this device's address on the same network as peerHost.
// browser=true gives the http:// address people open; false the https:// one devices use.
func (s *Server) bestURLFor(peerHost string, browser bool) string {
	urls := s.peerURLs()
	if browser {
		urls = s.LANURLs()
	}
	if ip := net.ParseIP(peerHost).To4(); ip != nil {
		for _, u := range urls {
			h := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
			if hh, _, err := net.SplitHostPort(h); err == nil {
				h = hh
			}
			if mine := net.ParseIP(h).To4(); mine != nil && mine[0] == ip[0] && mine[1] == ip[1] && mine[2] == ip[2] {
				return u
			}
		}
	}
	if len(urls) > 0 {
		return urls[0]
	}
	return fmt.Sprintf("http://127.0.0.1:%d", s.port)
}

func (s *Server) handlePairStart(w http.ResponseWriter, r *http.Request) {
	sess := s.trust.StartSession()
	base := s.bestURLFor("", true)
	qr := fmt.Sprintf("%s/pair?t=%s&fp=%s&n=%s", base, sess.Token, s.me.FP[:16], url.QueryEscape(s.me.Name))
	writeJSON(w, 200, map[string]any{"pin": sess.PIN, "qr": qr, "expiresAt": sess.Expires})
}

func (s *Server) handlePairStatus(w http.ResponseWriter, r *http.Request) {
	active, done, exp := s.trust.SessionStatus()
	v := map[string]any{"active": active, "expiresAt": exp}
	if done != nil {
		v["paired"] = map[string]any{"id": done.ID, "name": done.Name, "platform": done.Platform}
	}
	writeJSON(w, 200, v)
}

func (s *Server) handlePairCancel(w http.ResponseWriter, r *http.Request) {
	s.trust.CancelSession()
	writeJSON(w, 200, map[string]any{"ok": true})
}

type pairPeer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Port     int    `json:"port"`
	FP       string `json:"fp"`
}

// handlePairJoin runs on device B (the one that scanned the QR or typed the PIN).
func (s *Server) handlePairJoin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL   string `json:"url"`
		Token string `json:"token"`
		PIN   string `json:"pin"`
		FP    string `json:"fp"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req) != nil {
		jsonErr(w, 400, "Invalid request")
		return
	}
	base, err := cleanPeerBase(req.URL)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	// 1. Learn the other device's key fingerprint and check it against the QR code.
	resp, err := insecureClient.Get(base + "/api/info")
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "Can't reach that device. Make sure both are on the same Wi-Fi and Ferry is open on it.")
		return
	}
	var info struct{ App, ID, Name, Platform, FP string }
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&info)
	resp.Body.Close()
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 || info.App != "ferry" {
		jsonErr(w, http.StatusBadGateway, "That address isn't a Ferry device")
		return
	}
	peerFP := SPKIFingerprint(resp.TLS.PeerCertificates[0])
	if req.FP != "" && !strings.HasPrefix(peerFP, strings.ToLower(req.FP)) {
		jsonErr(w, http.StatusBadGateway, "The device at that address isn't the one in the QR code. Pairing stopped.")
		return
	}
	if info.ID == s.me.ID {
		jsonErr(w, 400, "That's this device. Scan the code on the other device.")
		return
	}
	// 2. Present the token/PIN over a connection pinned to that key.
	body, _ := json.Marshal(map[string]any{
		"token": req.Token, "pin": strings.TrimSpace(req.PIN),
		"device": pairPeer{ID: s.me.ID, Name: s.me.Name, Platform: s.me.Platform, Port: s.port, FP: s.me.FP},
	})
	hreq, _ := http.NewRequest("POST", base+"/api/pair/accept", bytes.NewReader(body))
	hreq.Header.Set("Content-Type", "application/json")
	hresp, err := pinnedClient(peerFP, false).Do(hreq)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "Lost the connection while pairing. Try again.")
		return
	}
	defer hresp.Body.Close()
	var out struct {
		Error  string   `json:"error"`
		Key    string   `json:"key"`
		Device pairPeer `json:"device"`
	}
	_ = json.NewDecoder(io.LimitReader(hresp.Body, 1<<16)).Decode(&out)
	if hresp.StatusCode != 200 || out.Key == "" {
		if out.Error == "" {
			out.Error = "The other device refused pairing"
		}
		jsonErr(w, http.StatusBadRequest, out.Error)
		return
	}
	d := &TrustedDevice{
		ID: info.ID, Name: out.Device.Name, Platform: out.Device.Platform, FP: peerFP, Key: out.Key,
		SyncClipboard: true, PairedAt: time.Now(), LastURL: base,
	}
	s.trust.Put(d)
	writeJSON(w, 200, map[string]any{"device": deviceView{ID: d.ID, Name: d.Name, Platform: d.Platform, Paired: true, Online: true, URL: base}})
}

// handlePairAccept runs on device A (the one showing the QR/PIN).
func (s *Server) handlePairAccept(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.limiter.blocked(ip) {
		jsonErr(w, http.StatusTooManyRequests, "Too many attempts. Wait a minute.")
		return
	}
	var req struct {
		Token  string   `json:"token"`
		PIN    string   `json:"pin"`
		Device pairPeer `json:"device"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req) != nil || req.Device.ID == "" || len(req.Device.FP) != 64 {
		jsonErr(w, 400, "Invalid pairing request")
		return
	}
	d := &TrustedDevice{
		ID: req.Device.ID, Name: trim(req.Device.Name, 80), Platform: trim(req.Device.Platform, 20),
		FP: strings.ToLower(req.Device.FP), Key: randomToken(32), SyncClipboard: true, PairedAt: time.Now(),
		LastURL: baseURL(ip, req.Device.Port),
	}
	if err := s.trust.Redeem(req.Token, req.PIN, d); err != nil {
		s.limiter.miss(ip)
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"key":    d.Key,
		"device": pairPeer{ID: s.me.ID, Name: s.me.Name, Platform: s.me.Platform, Port: s.port, FP: s.me.FP},
	})
}

// cleanPeerBase accepts only http(s)://<private address>[:port] and returns the HTTPS
// address of that device (every Ferry port speaks both), so the server can't be used to
// reach arbitrary hosts.
func cleanPeerBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("Not a Ferry device address")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if ip == nil || !(ip.IsPrivate() || ip.IsLoopback()) {
		return "", fmt.Errorf("Only devices on your local network can be used")
	}
	return baseURL(host, urlPort(u)), nil
}

func urlPort(u *url.URL) int {
	if p := u.Port(); p != "" {
		n, _ := strconv.Atoi(p)
		return n
	}
	if u.Scheme == "http" {
		return 80
	}
	return 443
}

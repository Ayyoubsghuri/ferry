package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Offer is something a paired device wants to send to this one.
type Offer struct {
	ID           string    `json:"id"`
	FromID       string    `json:"fromId"`
	FromName     string    `json:"fromName"`
	FromPlatform string    `json:"fromPlatform"`
	Kind         string    `json:"kind"` // "files" | "text"
	Code         string    `json:"code"`
	URL          string    `json:"url"` // sender's address
	Summary      string    `json:"summary"`
	Count        int       `json:"count"`
	Size         int64     `json:"size"`
	Text         string    `json:"text,omitempty"`
	Files        []File    `json:"files,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	State        string    `json:"state"` // pending, accepted, saving, done, declined, failed
	Progress     float64   `json:"progress"`
	SavedPath    string    `json:"savedPath,omitempty"`
	Error        string    `json:"error,omitempty"`
	Auto         bool      `json:"auto,omitempty"`
	Unpaired     bool      `json:"unpaired,omitempty"`
	Token        string    `json:"token,omitempty"` // lets the receiver fetch files and report back
	FP           string    `json:"fp,omitempty"`    // sender's key fingerprint
	Port         int       `json:"port,omitempty"`
	Local        bool      `json:"local,omitempty"` // files are already on this device (sent from a browser)
	FromIP       string    `json:"-"`
	ToGuest      string    `json:"-"`
}

// Outgoing tracks something this device pushed to a paired device.
type Outgoing struct {
	ID        string    `json:"id"`
	ToID      string    `json:"toId"`
	ToName    string    `json:"toName"`
	Code      string    `json:"code"`
	State     string    `json:"state"` // waiting, accepted, saving, done, declined, failed
	Progress  float64   `json:"progress"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	Unpaired  bool      `json:"unpaired,omitempty"`
	historyID string
	token     string
	toURL     string
	toFP      string
	fromGuest string
}

type Clip struct {
	Seq      int64     `json:"seq"`
	FromID   string    `json:"fromId,omitempty"`
	FromName string    `json:"fromName"`
	Text     string    `json:"text"`
	At       time.Time `json:"at"`
}

type Inbox struct {
	mu     sync.Mutex
	offers []*Offer
	out    map[string]*Outgoing
	clips  []Clip
	seq    int64
}

func newInbox() *Inbox { return &Inbox{out: map[string]*Outgoing{}} }

func (ib *Inbox) offer(id string) *Offer {
	for _, o := range ib.offers {
		if o.ID == id {
			return o
		}
	}
	return nil
}

// ---------- sending to a paired device ----------

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string `json:"code"`
		Owner    string `json:"owner"`
		DeviceID string `json:"deviceId"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil {
		jsonErr(w, 400, "Invalid request")
		return
	}
	t := s.store.Get(req.Code)
	if t == nil || !t.IsOwner(req.Owner) {
		jsonErr(w, 404, "That share has expired")
		return
	}
	if t.Encrypted {
		jsonErr(w, 400, "Password-locked shares can't be sent straight to a device. Turn the lock off, or share the code instead.")
		return
	}
	var guest *Guest
	if strings.HasPrefix(req.DeviceID, "g:") {
		for _, g := range s.onlineGuests() {
			if "g:"+g.ID == req.DeviceID {
				gg := g
				guest = &gg
			}
		}
		if guest == nil {
			jsonErr(w, 404, "That browser isn't open anymore")
			return
		}
	}
	d := s.trust.Get(req.DeviceID)
	var peer *PeerInfo
	if d == nil && guest == nil {
		if peer = s.disc.Find(req.DeviceID); peer == nil {
			jsonErr(w, 404, "That device isn't on the network anymore")
			return
		}
	}
	var files []File
	var size int64
	s.store.With(func() {
		for _, f := range t.Files {
			files = append(files, File{ID: f.ID, Name: f.Name, Size: f.Size, Type: f.Type})
			size += f.Size
		}
		// A request waits until the other person answers, so keep the files around.
		if min := time.Now().Add(7 * 24 * time.Hour); t.ExpiresAt.Before(min) {
			t.ExpiresAt = min
		}
	})
	kind := "files"
	if len(files) == 0 {
		kind = "text"
	}
	o := Offer{
		ID: randomToken(8), Kind: kind, Code: t.Code, Summary: summarize(files, t.Text), Count: len(files),
		Size: size, Text: t.Text, Files: files, CreatedAt: time.Now(), Token: randomToken(16),
		FromID: s.me.ID, FromName: s.me.Name, FromPlatform: s.me.Platform, FP: s.me.FP, Port: s.port,
	}
	out := &Outgoing{ID: o.ID, Code: t.Code, State: "waiting", CreatedAt: time.Now(), historyID: t.HistoryID, token: o.Token}
	var resp struct{ State string }
	if guest != nil {
		o.Local, o.Unpaired, o.ToGuest, o.State = true, true, guest.ID, "pending"
		oc := o
		s.guests.mu.Lock()
		s.guests.offers = append([]*Offer{&oc}, s.guests.offers...)
		if len(s.guests.offers) > 200 {
			s.guests.offers = s.guests.offers[:200]
		}
		s.guests.mu.Unlock()
		out.ToID, out.ToName, out.Unpaired = req.DeviceID, guest.Name, true
	} else if d != nil {
		o.URL = s.bestURLFor(hostOf(s.deviceURL(d)), false)
		if err := s.callDevice(d, "POST", "/api/peer/offer", o, &resp); err != nil {
			jsonErr(w, http.StatusBadGateway, err.Error())
			return
		}
		out.ToID, out.ToName = d.ID, d.Name
	} else {
		o.URL = s.bestURLFor(hostOf(peer.URL), false)
		if err := s.callNearby(peer.URL, peer.FP, "/api/nearby/offer", o, &resp); err != nil {
			jsonErr(w, http.StatusBadGateway, err.Error())
			return
		}
		out.ToID, out.ToName, out.Unpaired, out.toURL, out.toFP = peer.ID, peer.Name, true, peer.URL, peer.FP
	}
	if resp.State != "" && resp.State != "pending" {
		out.State = resp.State
	}
	s.inbox.mu.Lock()
	s.inbox.out[o.ID] = out
	s.inbox.mu.Unlock()
	if t.HistoryID != "" {
		s.hist.Update(t.HistoryID, func(e *HistoryEntry) { e.Peer = out.ToName; e.Status = "Sent to " + out.ToName })
	}
	writeJSON(w, 200, out)
}

// callNearby sends a JSON request to an unpaired device, checking it still has the key it
// announced on the network.
func (s *Server) callNearby(base, fp, path string, in, out any) error {
	b, _ := json.Marshal(in)
	req, _ := http.NewRequest("POST", base+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	client := insecureClient
	if fp != "" {
		client = pinnedClient(fp, false)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("That device isn't reachable right now")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("The device answered %d", resp.StatusCode)
		}
		return errors.New(e.Error)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func hostOf(base string) string {
	if u, err := url.Parse(base); err == nil {
		return u.Hostname()
	}
	return ""
}

func summarize(files []File, text string) string {
	switch {
	case len(files) == 0:
		t := strings.TrimSpace(text)
		if len(t) > 60 {
			t = t[:60] + "…"
		}
		return t
	case len(files) == 1:
		return files[0].Name
	default:
		top := strings.SplitN(files[0].Name, "/", 2)
		if len(top) == 2 {
			return fmt.Sprintf("%s (%d files)", top[0], len(files))
		}
		return fmt.Sprintf("%d files", len(files))
	}
}

func (s *Server) handleOutbox(w http.ResponseWriter, r *http.Request) {
	s.inbox.mu.Lock()
	list := make([]Outgoing, 0, len(s.inbox.out))
	for _, o := range s.inbox.out {
		list = append(list, *o)
	}
	s.inbox.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
	writeJSON(w, 200, map[string]any{"outgoing": list})
}

func (s *Server) handlePeerStatus(w http.ResponseWriter, r *http.Request, d *TrustedDevice) {
	var req struct {
		ID       string  `json:"id"`
		State    string  `json:"state"`
		Progress float64 `json:"progress"`
		Error    string  `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req)
	s.inbox.mu.Lock()
	o := s.inbox.out[req.ID]
	if o != nil && o.ToID == d.ID {
		o.State, o.Progress, o.Error = trim(req.State, 20), req.Progress, trim(req.Error, 300)
	}
	s.inbox.mu.Unlock()
	if o != nil && (req.State == "done" || req.State == "declined" || req.State == "cancelled") {
		s.shortenShare(o.Code)
	}
	if o != nil && o.historyID != "" {
		switch req.State {
		case "done":
			s.hist.Update(o.historyID, func(e *HistoryEntry) { e.Status = "Received by " + d.Name })
		case "declined":
			s.hist.Update(o.historyID, func(e *HistoryEntry) { e.Status = "Declined by " + d.Name })
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- receiving from a paired device ----------

func (s *Server) handlePeerOffer(w http.ResponseWriter, r *http.Request, d *TrustedDevice) {
	var o Offer
	if json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&o) != nil || o.ID == "" || o.Code == "" {
		jsonErr(w, 400, "Invalid offer")
		return
	}
	// The files must come from the device that made the offer.
	p := 443
	if u, err := url.Parse(o.URL); err == nil {
		p = urlPort(u)
	}
	o.URL = baseURL(clientIP(r), p)
	o.FromID, o.FromName, o.FromPlatform, o.FP, o.Unpaired = d.ID, d.Name, d.Platform, d.FP, false
	o.State, o.Progress, o.Error, o.SavedPath = "pending", 0, "", ""
	o.CreatedAt = time.Now()
	s.trust.Update(d.ID, func(t *TrustedDevice) { t.LastURL = o.URL })
	s.inbox.mu.Lock()
	s.inbox.offers = append([]*Offer{&o}, s.inbox.offers...)
	if len(s.inbox.offers) > 100 {
		s.inbox.offers = s.inbox.offers[:100]
	}
	s.inbox.mu.Unlock()
	state := "pending"
	if d.AutoAccept || s.cfg.AutoAccept {
		o.Auto = true
		state = s.acceptOffer(o.ID)
	}
	writeJSON(w, 200, map[string]any{"state": state})
}

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	s.inbox.mu.Lock()
	list := make([]Offer, 0, len(s.inbox.offers))
	for _, o := range s.inbox.offers {
		list = append(list, *o)
	}
	s.inbox.mu.Unlock()
	writeJSON(w, 200, map[string]any{"offers": list})
}

func (s *Server) notifySender(o Offer) {
	if o.Local {
		s.guests.mu.Lock()
		if out := s.guests.out[o.ID]; out != nil {
			out.State, out.Progress, out.Error = o.State, o.Progress, o.Error
			if out.State == "accepted" {
				out.State = "saving"
			}
		}
		s.guests.mu.Unlock()
		return
	}
	d := s.trust.Get(o.FromID)
	if d == nil || o.Unpaired {
		go func() {
			_ = s.callNearby(o.URL, o.FP, "/api/nearby/status", map[string]any{
				"id": o.ID, "token": o.Token, "state": o.State, "progress": o.Progress, "error": o.Error,
			}, nil)
		}()
		return
	}
	go func() {
		_ = s.callDevice(d, "POST", "/api/peer/status", map[string]any{
			"id": o.ID, "state": o.State, "progress": o.Progress, "error": o.Error,
		}, nil)
	}()
}

func (s *Server) setOffer(id string, fn func(o *Offer)) (Offer, bool) {
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	o := s.inbox.offer(id)
	if o == nil {
		return Offer{}, false
	}
	fn(o)
	return *o, true
}

// acceptOffer starts receiving. Desktop apps save straight into Downloads/Ferry;
// elsewhere the app UI downloads the files itself and reports back.
func (s *Server) acceptOffer(id string) string {
	o, ok := s.setOffer(id, func(o *Offer) {
		if o.State == "pending" {
			o.State = "accepted"
		}
	})
	if !ok {
		return ""
	}
	if o.Kind == "text" {
		o, _ = s.setOffer(id, func(o *Offer) { o.State = "done"; o.Progress = 1 })
		s.hist.Add(HistoryEntry{Direction: "received", Kind: "text", Summary: o.Summary, Peer: o.FromName, Text: o.Text})
	} else if s.cfg.SaveDir != "" {
		o, _ = s.setOffer(id, func(o *Offer) { o.State = "saving" })
		go s.saveOffer(o)
	}
	s.notifySender(o)
	return o.State
}

func (s *Server) handleInboxAccept(w http.ResponseWriter, r *http.Request) {
	st := s.acceptOffer(r.PathValue("id"))
	if st == "" {
		jsonErr(w, 404, "That offer is gone")
		return
	}
	writeJSON(w, 200, map[string]any{"state": st, "saveDir": s.cfg.SaveDir})
}

func (s *Server) handleInboxDecline(w http.ResponseWriter, r *http.Request) {
	o, ok := s.setOffer(r.PathValue("id"), func(o *Offer) { o.State = "declined" })
	if !ok {
		jsonErr(w, 404, "That offer is gone")
		return
	}
	s.notifySender(o)
	writeJSON(w, 200, map[string]any{"state": o.State})
}

// handleInboxDone is called by the app UI after it saved the files itself (Android).
func (s *Server) handleInboxDone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	o, ok := s.setOffer(r.PathValue("id"), func(o *Offer) {
		if req.OK {
			o.State, o.Progress = "done", 1
		} else {
			o.State, o.Error = "failed", trim(req.Error, 300)
		}
	})
	if !ok {
		jsonErr(w, 404, "That offer is gone")
		return
	}
	if req.OK {
		s.hist.Add(HistoryEntry{Direction: "received", Kind: "files", Summary: o.Summary, Count: o.Count, Size: o.Size, Peer: o.FromName, Path: "Downloads/Ferry"})
	}
	s.notifySender(o)
	writeJSON(w, 200, map[string]any{"state": o.State})
}

func (s *Server) handleInboxDismiss(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.inbox.mu.Lock()
	for i, o := range s.inbox.offers {
		if o.ID == id {
			s.inbox.offers = append(s.inbox.offers[:i], s.inbox.offers[i+1:]...)
			break
		}
	}
	s.inbox.mu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true})
}

// saveOffer downloads the offered files from the sender into the save folder.
func (s *Server) saveOffer(o Offer) {
	var d *TrustedDevice
	if !o.Unpaired && !o.Local {
		d = s.trust.Get(o.FromID)
	}
	fail := func(msg string) {
		o2, _ := s.setOffer(o.ID, func(x *Offer) { x.State, x.Error = "failed", msg })
		s.notifySender(o2)
	}
	if d == nil && !o.Unpaired && !o.Local {
		fail("The sender isn't paired anymore")
		return
	}
	client := &http.Client{Transport: streamTransport}
	if d != nil {
		client = pinnedClient(d.FP, true)
	} else if o.FP != "" {
		client = pinnedClient(o.FP, true)
	}
	if err := os.MkdirAll(s.cfg.SaveDir, 0o755); err != nil {
		fail("Can't write to " + s.cfg.SaveDir)
		return
	}
	var total, done int64
	for _, f := range o.Files {
		total += f.Size
	}
	// Keep folder structure; avoid overwriting existing files or folders.
	renamed := map[string]string{}
	topOf := func(name string) string { return strings.SplitN(name, "/", 2)[0] }
	var savedTop []string
	lastNotify := time.Now()
	for _, f := range o.Files {
		top := topOf(f.Name)
		if _, ok := renamed[top]; !ok {
			renamed[top] = uniqueName(s.cfg.SaveDir, top)
			savedTop = append(savedTop, renamed[top])
		}
		rel := renamed[top] + strings.TrimPrefix(f.Name, top)
		dst := filepath.Join(s.cfg.SaveDir, filepath.FromSlash(rel))
		if !strings.HasPrefix(filepath.Clean(dst), filepath.Clean(s.cfg.SaveDir)) {
			continue
		}
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		var resp *http.Response
		if o.Local {
			fh, err := os.Open(s.store.FilePath(o.Code, f.ID))
			if err != nil {
				fail("The files are no longer available")
				return
			}
			resp = &http.Response{StatusCode: 200, Body: fh}
		} else {
			req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/t/%s/f/%s", o.URL, o.Code, f.ID), nil)
			if d != nil {
				s.authHeaders(req.Header, d)
			}
			req.Header.Set("X-SL-Offer", o.Token)
			var err error
			resp, err = client.Do(req)
			if err != nil || resp.StatusCode != 200 {
				if resp != nil {
					resp.Body.Close()
				}
				fail("Download from " + o.FromName + " failed")
				return
			}
		}
		fh, err := os.Create(dst + ".part")
		if err != nil {
			resp.Body.Close()
			fail("Can't write " + rel)
			return
		}
		buf := make([]byte, 256<<10)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := fh.Write(buf[:n]); werr != nil {
					rerr = werr
				}
				done += int64(n)
				p := float64(done) / float64(max64(total, 1))
				oo, _ := s.setOffer(o.ID, func(x *Offer) { x.Progress = p })
				if time.Since(lastNotify) > time.Second {
					lastNotify = time.Now()
					s.notifySender(oo)
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				fh.Close()
				resp.Body.Close()
				os.Remove(dst + ".part")
				fail("Transfer interrupted")
				return
			}
		}
		fh.Close()
		resp.Body.Close()
		_ = os.Rename(dst+".part", dst)
	}
	path := s.cfg.SaveDir
	if len(savedTop) == 1 {
		path = filepath.Join(s.cfg.SaveDir, savedTop[0])
	}
	o2, _ := s.setOffer(o.ID, func(x *Offer) { x.State, x.Progress, x.SavedPath = "done", 1, path })
	s.hist.Add(HistoryEntry{Direction: "received", Kind: "files", Summary: o.Summary, Count: o.Count, Size: o.Size, Peer: o.FromName, Path: path})
	s.notifySender(o2)
	log.Printf("received %s from %s into %s", o.Summary, o.FromName, path)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func uniqueName(dir, name string) string {
	if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		n := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(filepath.Join(dir, n)); os.IsNotExist(err) {
			return n
		}
	}
}

// ---------- clipboard ----------

type ClipboardIO interface {
	Read() (string, error)
	Write(string) error
}

func (s *Server) handleClipboardSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text       string   `json:"text"`
		FromSystem bool     `json:"fromSystem"`
		DeviceIDs  []string `json:"deviceIds"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req) != nil {
		jsonErr(w, 400, "Invalid request")
		return
	}
	if req.FromSystem && s.cfg.Clipboard != nil {
		t, err := s.cfg.Clipboard.Read()
		if err != nil {
			jsonErr(w, 500, "Couldn't read the clipboard")
			return
		}
		req.Text = t
	}
	if strings.TrimSpace(req.Text) == "" {
		jsonErr(w, 400, "The clipboard is empty. Copy some text first.")
		return
	}
	sent, failed := s.pushClipboard(req.Text, req.DeviceIDs)
	if len(sent) == 0 && len(failed) == 0 {
		jsonErr(w, 400, "No paired devices have clipboard sharing on. Pair a device first.")
		return
	}
	writeJSON(w, 200, map[string]any{"sent": sent, "failed": failed})
}

func (s *Server) pushClipboard(text string, ids []string) (sent []string, failed []map[string]string) {
	var targets []TrustedDevice
	if len(ids) > 0 {
		for _, id := range ids {
			if d := s.trust.Get(id); d != nil {
				targets = append(targets, *d)
			}
		}
	} else {
		for _, d := range s.trust.List() {
			if d.SyncClipboard {
				targets = append(targets, d)
			}
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, d := range targets {
		wg.Add(1)
		go func(d TrustedDevice) {
			defer wg.Done()
			err := s.callDevice(&d, "POST", "/api/peer/clipboard", map[string]string{"text": text}, nil)
			mu.Lock()
			if err != nil {
				failed = append(failed, map[string]string{"name": d.Name, "error": err.Error()})
			} else {
				sent = append(sent, d.Name)
			}
			mu.Unlock()
		}(d)
	}
	wg.Wait()
	if len(sent) > 0 {
		s.hist.Add(HistoryEntry{Direction: "sent", Kind: "clipboard", Summary: oneLine(text), Peer: strings.Join(sent, ", "), Text: text, Size: int64(len(text))})
	}
	return
}

func oneLine(t string) string {
	t = strings.Join(strings.Fields(t), " ")
	if len(t) > 80 {
		t = t[:80] + "…"
	}
	return t
}

func (s *Server) handlePeerClipboard(w http.ResponseWriter, r *http.Request, d *TrustedDevice) {
	var req struct{ Text string }
	if json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req) != nil || req.Text == "" {
		jsonErr(w, 400, "Empty clipboard")
		return
	}
	s.inbox.mu.Lock()
	s.inbox.seq++
	c := Clip{Seq: s.inbox.seq, FromID: d.ID, FromName: d.Name, Text: req.Text, At: time.Now()}
	s.inbox.clips = append(s.inbox.clips, c)
	if len(s.inbox.clips) > 50 {
		s.inbox.clips = s.inbox.clips[len(s.inbox.clips)-50:]
	}
	s.inbox.mu.Unlock()
	copied := false
	if s.settings.Get().AutoCopyClipboard && s.cfg.Clipboard != nil {
		s.clipMu.Lock()
		s.lastClip = req.Text // don't echo it back
		s.clipMu.Unlock()
		copied = s.cfg.Clipboard.Write(req.Text) == nil
	}
	s.hist.Add(HistoryEntry{Direction: "received", Kind: "clipboard", Summary: oneLine(req.Text), Peer: d.Name, Text: req.Text, Size: int64(len(req.Text))})
	writeJSON(w, 200, map[string]any{"ok": true, "copied": copied})
}

func (s *Server) handleClipboardList(w http.ResponseWriter, r *http.Request) {
	var since int64
	fmt.Sscanf(r.URL.Query().Get("since"), "%d", &since)
	s.inbox.mu.Lock()
	var list []Clip
	for _, c := range s.inbox.clips {
		if c.Seq > since {
			list = append(list, c)
		}
	}
	seq := s.inbox.seq
	s.inbox.mu.Unlock()
	if list == nil {
		list = []Clip{}
	}
	writeJSON(w, 200, map[string]any{"seq": seq, "clips": list, "autoCopied": s.cfg.Clipboard != nil && s.settings.Get().AutoCopyClipboard})
}

func (s *Server) resetClipboardWatch() {
	if s.cfg.Clipboard == nil {
		return
	}
	t, _ := s.cfg.Clipboard.Read()
	s.clipMu.Lock()
	s.lastClip = t
	s.clipMu.Unlock()
}

// watchClipboard sends new clipboard text to paired devices (desktop, when turned on).
func (s *Server) watchClipboard() {
	if s.cfg.Clipboard == nil {
		return
	}
	s.resetClipboardWatch()
	for range time.Tick(time.Second) {
		if !s.settings.Get().WatchClipboard {
			continue
		}
		t, err := s.cfg.Clipboard.Read()
		if err != nil || strings.TrimSpace(t) == "" || len(t) > 1<<20 {
			continue
		}
		s.clipMu.Lock()
		changed := t != s.lastClip
		s.lastClip = t
		s.clipMu.Unlock()
		if changed {
			go s.pushClipboard(t, nil)
		}
	}
}

// ---------- history ----------

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodDelete:
		s.hist.Clear()
		writeJSON(w, 200, map[string]any{"ok": true})
	case http.MethodPost:
		var e HistoryEntry
		if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&e) != nil {
			jsonErr(w, 400, "Invalid entry")
			return
		}
		e.ID, e.At = "", time.Time{}
		if e.Direction != "sent" && e.Direction != "received" {
			e.Direction = "received"
		}
		writeJSON(w, 200, map[string]any{"id": s.hist.Add(e)})
	default:
		writeJSON(w, 200, map[string]any{"entries": s.hist.List()})
	}
}

func (s *Server) handleOpenFolder(w http.ResponseWriter, r *http.Request) {
	if s.cfg.OpenPath == nil || s.cfg.SaveDir == "" {
		jsonErr(w, 400, "Not available on this device")
		return
	}
	var req struct{ Path string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	p := s.cfg.SaveDir
	if req.Path != "" {
		c := filepath.Clean(req.Path)
		if strings.HasPrefix(c, filepath.Clean(s.cfg.SaveDir)) {
			if st, err := os.Stat(c); err == nil {
				if st.IsDir() {
					p = c
				} else {
					p = filepath.Dir(c)
				}
			}
		}
	}
	_ = os.MkdirAll(p, 0o755)
	s.cfg.OpenPath(p)
	writeJSON(w, 200, map[string]any{"ok": true})
}

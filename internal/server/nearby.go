package server

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------- requests from devices that aren't paired ----------
//
// Anyone running Ferry on the network can ask to send something. It always needs the
// person to accept (never automatic), at most 3 can be waiting from one address, and it
// can be switched off under Devices → "Allow requests from unpaired devices".

func (s *Server) handleNearbyOffer(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.settings.Get().AllowNearby {
		jsonErr(w, http.StatusForbidden, "That device only accepts files from devices it's paired with")
		return
	}
	if s.limiter.blocked(ip) {
		jsonErr(w, http.StatusTooManyRequests, "Too many requests. Wait a minute.")
		return
	}
	var o Offer
	if json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&o) != nil || o.ID == "" || o.Code == "" || o.Token == "" {
		jsonErr(w, 400, "Invalid request")
		return
	}
	if d := s.trust.Get(o.FromID); d != nil {
		// A paired device should use its authenticated channel; treat this as unpaired anyway.
		o.FromName = trim(o.FromName, 60)
	}
	s.inbox.mu.Lock()
	waiting := 0
	for _, x := range s.inbox.offers {
		if x.Unpaired && x.FromIP == ip && x.State == "pending" {
			waiting++
		}
	}
	s.inbox.mu.Unlock()
	if waiting >= 3 {
		s.limiter.miss(ip)
		jsonErr(w, http.StatusTooManyRequests, "That device already has requests from you waiting")
		return
	}
	if o.Port <= 0 || o.Port > 65535 {
		o.Port = 443
	}
	o.URL = baseURL(ip, o.Port)
	o.FromIP = ip
	o.FromName = trim(strings.TrimSpace(o.FromName), 60)
	if o.FromName == "" {
		o.FromName = ip
	}
	o.FromPlatform = trim(o.FromPlatform, 20)
	o.FP = trim(strings.ToLower(o.FP), 64)
	o.Unpaired, o.Auto = true, false
	o.State, o.Progress, o.Error, o.SavedPath = "pending", 0, "", ""
	o.CreatedAt = time.Now()
	if len(o.Files) > 20000 {
		o.Files = o.Files[:20000]
	}
	s.inbox.mu.Lock()
	s.inbox.offers = append([]*Offer{&o}, s.inbox.offers...)
	if len(s.inbox.offers) > 100 {
		s.inbox.offers = s.inbox.offers[:100]
	}
	s.inbox.mu.Unlock()
	writeJSON(w, 200, map[string]any{"state": "pending"})
}

// handleNearbyStatus: an unpaired receiver reports progress back, proving itself with the
// offer's token.
func (s *Server) handleNearbyStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string  `json:"id"`
		Token    string  `json:"token"`
		State    string  `json:"state"`
		Progress float64 `json:"progress"`
		Error    string  `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req)
	s.inbox.mu.Lock()
	o := s.inbox.out[req.ID]
	ok := o != nil && o.token != "" && o.token == req.Token
	if ok {
		o.State, o.Progress, o.Error = trim(req.State, 20), req.Progress, trim(req.Error, 300)
	}
	s.inbox.mu.Unlock()
	if !ok {
		jsonErr(w, 404, "Unknown request")
		return
	}
	if req.State == "done" || req.State == "declined" {
		s.shortenShare(o.Code)
		if o.historyID != "" {
			word := "Received by "
			if req.State == "declined" {
				word = "Declined by "
			}
			s.hist.Update(o.historyID, func(e *HistoryEntry) { e.Status = word + o.ToName })
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// shortenShare: once a direct send is answered, the share doesn't need its long life.
func (s *Server) shortenShare(code string) {
	t := s.store.Get(code)
	if t == nil {
		return
	}
	s.store.With(func() {
		if soon := time.Now().Add(time.Hour); t.ExpiresAt.After(soon) {
			t.ExpiresAt = soon
		}
	})
}

// offerToken lets a receiver fetch a direct send's files even if the sender requires an
// access PIN for browsers.
func (s *Server) offerTokenAllows(r *http.Request) bool {
	tok := r.Header.Get("X-SL-Offer")
	if tok == "" {
		return false
	}
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	for _, o := range s.inbox.out {
		if o.token == tok && strings.HasPrefix(r.URL.Path, "/api/t/"+o.Code) {
			return true
		}
	}
	return false
}

// ---------- cancelling ----------

func (s *Server) handleOutboxCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.inbox.mu.Lock()
	o := s.inbox.out[id]
	if o != nil {
		o.State = "cancelled"
	}
	s.inbox.mu.Unlock()
	if o == nil {
		jsonErr(w, 404, "Unknown request")
		return
	}
	body := map[string]any{"id": id, "token": o.token}
	if strings.HasPrefix(o.ToID, "g:") {
		s.guests.mu.Lock()
		for _, x := range s.guests.offers {
			if x.ID == id && x.State == "pending" {
				x.State = "cancelled"
			}
		}
		s.guests.mu.Unlock()
	}
	go func() {
		if strings.HasPrefix(o.ToID, "g:") {
			return
		}
		if d := s.trust.Get(o.ToID); d != nil && !o.Unpaired {
			_ = s.callDevice(d, "POST", "/api/peer/cancel", body, nil)
		} else if o.toURL != "" {
			_ = s.callNearby(o.toURL, o.toFP, "/api/nearby/cancel", body, nil)
		}
	}()
	writeJSON(w, 200, map[string]any{"state": "cancelled"})
}

func (s *Server) cancelIncoming(id, token, fromID string) bool {
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	o := s.inbox.offer(id)
	if o == nil || (token != "" && o.Token != token) || (fromID != "" && o.FromID != fromID) {
		return false
	}
	if o.State == "pending" {
		o.State = "cancelled"
	}
	return true
}

func (s *Server) handlePeerCancel(w http.ResponseWriter, r *http.Request, d *TrustedDevice) {
	var req struct{ ID string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	s.cancelIncoming(req.ID, "", d.ID)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleNearbyCancel(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID, Token string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	if req.Token == "" || !s.cancelIncoming(req.ID, req.Token, "") {
		jsonErr(w, 404, "Unknown request")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- messages ----------

type Message struct {
	ID       string    `json:"id"`
	PeerID   string    `json:"peerId"`
	PeerName string    `json:"peerName"`
	Out      bool      `json:"out"`
	Text     string    `json:"text"`
	At       time.Time `json:"at"`
	Read     bool      `json:"read"`
	Unpaired bool      `json:"unpaired,omitempty"`
	Failed   bool      `json:"failed,omitempty"`
}

type Chats struct {
	mu   sync.Mutex
	path string
	msgs []Message
}

func newChats(dir string) *Chats {
	c := &Chats{path: filepath.Join(dir, "messages.json")}
	loadJSON(c.path, &c.msgs)
	return c
}

func (c *Chats) add(m Message) Message {
	if m.ID == "" {
		m.ID = randomToken(8)
	}
	m.At = time.Now()
	if len(m.Text) > 8000 {
		m.Text = m.Text[:8000]
	}
	c.mu.Lock()
	c.msgs = append(c.msgs, m)
	if len(c.msgs) > 2000 {
		c.msgs = c.msgs[len(c.msgs)-2000:]
	}
	_ = saveJSON(c.path, c.msgs)
	c.mu.Unlock()
	return m
}

func (s *Server) handleMessagesGet(w http.ResponseWriter, r *http.Request) {
	peer := r.URL.Query().Get("peer")
	s.chats.mu.Lock()
	defer s.chats.mu.Unlock()
	if peer != "" {
		var thread []Message
		for _, m := range s.chats.msgs {
			if m.PeerID == peer {
				thread = append(thread, m)
			}
		}
		if len(thread) > 300 {
			thread = thread[len(thread)-300:]
		}
		if thread == nil {
			thread = []Message{}
		}
		writeJSON(w, 200, map[string]any{"messages": thread})
		return
	}
	// Summary: latest message and unread count per conversation.
	type conv struct {
		PeerID   string  `json:"peerId"`
		PeerName string  `json:"peerName"`
		Unread   int     `json:"unread"`
		Last     Message `json:"last"`
	}
	byPeer := map[string]*conv{}
	for _, m := range s.chats.msgs {
		c := byPeer[m.PeerID]
		if c == nil {
			c = &conv{PeerID: m.PeerID}
			byPeer[m.PeerID] = c
		}
		c.PeerName, c.Last = m.PeerName, m
		if !m.Out && !m.Read {
			c.Unread++
		}
	}
	list := make([]conv, 0, len(byPeer))
	for _, c := range byPeer {
		list = append(list, *c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Last.At.After(list[j].Last.At) })
	writeJSON(w, 200, map[string]any{"conversations": list})
}

func (s *Server) handleMessagesRead(w http.ResponseWriter, r *http.Request) {
	var req struct{ PeerID string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	s.chats.mu.Lock()
	for i := range s.chats.msgs {
		if s.chats.msgs[i].PeerID == req.PeerID {
			s.chats.msgs[i].Read = true
		}
	}
	_ = saveJSON(s.chats.path, s.chats.msgs)
	s.chats.mu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleMessageSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PeerID string `json:"peerId"`
		Text   string `json:"text"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req) != nil || strings.TrimSpace(req.Text) == "" {
		jsonErr(w, 400, "Write a message first")
		return
	}
	body := map[string]any{
		"text": req.Text, "fromId": s.me.ID, "fromName": s.me.Name, "platform": s.me.Platform,
		"port": s.port, "fp": s.me.FP,
	}
	var name string
	var err error
	unpaired := false
	if d := s.trust.Get(req.PeerID); d != nil {
		name = d.Name
		err = s.callDevice(d, "POST", "/api/peer/message", body, nil)
	} else if p := s.disc.Find(req.PeerID); p != nil {
		name, unpaired = p.Name, true
		err = s.callNearby(p.URL, p.FP, "/api/nearby/message", body, nil)
	} else {
		jsonErr(w, 404, "That device isn't on the network right now")
		return
	}
	m := s.chats.add(Message{PeerID: req.PeerID, PeerName: name, Out: true, Text: req.Text, Read: true, Unpaired: unpaired, Failed: err != nil})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "message": m})
		return
	}
	writeJSON(w, 200, map[string]any{"message": m})
}

func (s *Server) receiveMessage(w http.ResponseWriter, r *http.Request, d *TrustedDevice) {
	var req struct {
		Text     string `json:"text"`
		FromID   string `json:"fromId"`
		FromName string `json:"fromName"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req) != nil || strings.TrimSpace(req.Text) == "" {
		jsonErr(w, 400, "Empty message")
		return
	}
	m := Message{Text: req.Text}
	if d != nil {
		m.PeerID, m.PeerName = d.ID, d.Name
	} else {
		if req.FromID == "" {
			jsonErr(w, 400, "Unknown sender")
			return
		}
		m.PeerID, m.PeerName, m.Unpaired = trim(req.FromID, 40), trim(req.FromName, 60), true
		if m.PeerName == "" {
			m.PeerName = clientIP(r)
		}
	}
	s.chats.add(m)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handlePeerMessage(w http.ResponseWriter, r *http.Request, d *TrustedDevice) {
	s.receiveMessage(w, r, d)
}

func (s *Server) handleNearbyMessage(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.settings.Get().AllowNearby {
		jsonErr(w, http.StatusForbidden, "That device only accepts messages from devices it's paired with")
		return
	}
	if s.limiter.blocked(ip) {
		jsonErr(w, http.StatusTooManyRequests, "Too many messages. Wait a minute.")
		return
	}
	s.limiter.miss(ip) // counts toward the per-minute limit
	s.receiveMessage(w, r, nil)
}

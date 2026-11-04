package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Browsers without the Ferry app (a friend's phone, a work PC) become devices too: when
// one opens this device's page it registers, shows up in the owner's device list, and can
// send to and receive from this device with the same accept/refuse prompts.

type Guest struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	IP       string    `json:"ip"`
	LastSeen time.Time `json:"lastSeen"`
}

type guestState struct {
	mu     sync.Mutex
	guests map[string]*Guest
	offers []*Offer              // things this device offers to guests
	out    map[string]*Outgoing // things guests offered to this device
}

func newGuestState() *guestState {
	return &guestState{guests: map[string]*Guest{}, out: map[string]*Outgoing{}}
}

const guestCookie = "ferry_guest"

func (s *Server) guestFrom(r *http.Request) *Guest {
	c, err := r.Cookie(guestCookie)
	if err != nil {
		return nil
	}
	s.guests.mu.Lock()
	defer s.guests.mu.Unlock()
	g := s.guests.guests[c.Value]
	if g != nil {
		g.LastSeen = time.Now()
		g.IP = clientIP(r)
	}
	return g
}

func (s *Server) onlineGuests() []Guest {
	s.guests.mu.Lock()
	defer s.guests.mu.Unlock()
	var out []Guest
	for id, g := range s.guests.guests {
		if time.Since(g.LastSeen) > 15*time.Second {
			if time.Since(g.LastSeen) > 24*time.Hour {
				delete(s.guests.guests, id)
			}
			continue
		}
		out = append(out, *g)
	}
	return out
}

func (s *Server) handleGuestHello(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	name := trim(strings.TrimSpace(req.Name), 60)
	if name == "" {
		name = "Browser on " + clientIP(r)
	}
	g := s.guestFrom(r)
	if g == nil {
		g = &Guest{ID: randomToken(10), IP: clientIP(r), LastSeen: time.Now()}
		s.guests.mu.Lock()
		s.guests.guests[g.ID] = g
		s.guests.mu.Unlock()
		http.SetCookie(w, &http.Cookie{
			Name: guestCookie, Value: g.ID, Path: "/", MaxAge: 365 * 24 * 3600,
			HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
	}
	s.guests.mu.Lock()
	g.Name = name
	s.guests.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"id": g.ID, "name": name,
		"host": map[string]any{"id": s.me.ID, "name": s.me.Name, "platform": s.me.Platform},
	})
}

// handleGuestPoll: the guest's view of requests to it and the state of what it sent.
func (s *Server) handleGuestPoll(w http.ResponseWriter, r *http.Request) {
	g := s.guestFrom(r)
	if g == nil {
		jsonErr(w, http.StatusUnauthorized, "Not registered")
		return
	}
	s.guests.mu.Lock()
	offers := []Offer{}
	for _, o := range s.guests.offers {
		if o.ToGuest == g.ID {
			offers = append(offers, *o)
		}
	}
	out := []Outgoing{}
	for _, o := range s.guests.out {
		if o.fromGuest == g.ID {
			out = append(out, *o)
		}
	}
	s.guests.mu.Unlock()
	writeJSON(w, 200, map[string]any{"offers": offers, "outgoing": out})
}

func (s *Server) guestOffer(g *Guest, id string) *Offer {
	for _, o := range s.guests.offers {
		if o.ID == id && o.ToGuest == g.ID {
			return o
		}
	}
	return nil
}

// handleGuestAnswer: accept, decline or done for something this device offered a guest.
func (s *Server) handleGuestAnswer(w http.ResponseWriter, r *http.Request) {
	g := s.guestFrom(r)
	if g == nil {
		jsonErr(w, http.StatusUnauthorized, "Not registered")
		return
	}
	action := r.PathValue("action")
	state := map[string]string{"accept": "accepted", "decline": "declined", "done": "done", "dismiss": "dismissed"}[action]
	if state == "" {
		jsonErr(w, 400, "Unknown action")
		return
	}
	s.guests.mu.Lock()
	o := s.guestOffer(g, r.PathValue("id"))
	if o != nil {
		if state == "dismissed" {
			for i, x := range s.guests.offers {
				if x == o {
					s.guests.offers = append(s.guests.offers[:i], s.guests.offers[i+1:]...)
					break
				}
			}
		} else {
			o.State = state
			if state == "done" {
				o.Progress = 1
			}
		}
	}
	s.guests.mu.Unlock()
	if o == nil {
		jsonErr(w, 404, "That request is gone")
		return
	}
	if state != "dismissed" {
		s.inbox.mu.Lock()
		if out := s.inbox.out[o.ID]; out != nil {
			out.State = state
			if state == "done" {
				out.Progress = 1
			}
		}
		s.inbox.mu.Unlock()
		if state == "done" || state == "declined" {
			s.shortenShare(o.Code)
			if t := s.store.Get(o.Code); t != nil && t.HistoryID != "" {
				word := map[string]string{"done": "Received by ", "declined": "Refused by "}[state]
				s.hist.Update(t.HistoryID, func(e *HistoryEntry) { e.Status = word + g.Name })
			}
		}
	}
	writeJSON(w, 200, map[string]any{"state": state})
}

// handleGuestSend: a guest uploaded a share to this device and asks the owner to accept it.
func (s *Server) handleGuestSend(w http.ResponseWriter, r *http.Request) {
	g := s.guestFrom(r)
	if g == nil {
		jsonErr(w, http.StatusUnauthorized, "Not registered")
		return
	}
	var req struct{ Code, Owner string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	t := s.store.Get(req.Code)
	if t == nil || !t.IsOwner(req.Owner) {
		jsonErr(w, 404, "That upload has expired")
		return
	}
	if !s.settings.Get().AllowNearby {
		jsonErr(w, http.StatusForbidden, s.me.Name+" only accepts files from devices it's paired with")
		return
	}
	var files []File
	var size int64
	s.store.With(func() {
		for _, f := range t.Files {
			files = append(files, File{ID: f.ID, Name: f.Name, Size: f.Size, Type: f.Type})
			size += f.Size
		}
		if min := time.Now().Add(7 * 24 * time.Hour); t.ExpiresAt.Before(min) {
			t.ExpiresAt = min
		}
	})
	kind := "files"
	if len(files) == 0 {
		kind = "text"
	}
	o := &Offer{
		ID: randomToken(8), FromID: "g:" + g.ID, FromName: g.Name, FromPlatform: "browser", Kind: kind,
		Code: t.Code, Summary: summarize(files, t.Text), Count: len(files), Size: size, Text: t.Text,
		Files: files, CreatedAt: time.Now(), State: "pending", Unpaired: true, Local: true, FromIP: clientIP(r),
	}
	s.inbox.mu.Lock()
	s.inbox.offers = append([]*Offer{o}, s.inbox.offers...)
	s.inbox.mu.Unlock()
	s.guests.mu.Lock()
	s.guests.out[o.ID] = &Outgoing{ID: o.ID, ToID: s.me.ID, ToName: s.me.Name, Code: t.Code, State: "waiting", CreatedAt: time.Now(), fromGuest: g.ID}
	s.guests.mu.Unlock()
	writeJSON(w, 200, map[string]any{"id": o.ID, "toName": s.me.Name, "state": "waiting"})
}

func (s *Server) handleGuestCancel(w http.ResponseWriter, r *http.Request) {
	g := s.guestFrom(r)
	if g == nil {
		jsonErr(w, http.StatusUnauthorized, "Not registered")
		return
	}
	id := r.PathValue("id")
	s.guests.mu.Lock()
	out := s.guests.out[id]
	ok := out != nil && out.fromGuest == g.ID
	if ok {
		out.State = "cancelled"
	}
	s.guests.mu.Unlock()
	if !ok {
		jsonErr(w, 404, "Unknown request")
		return
	}
	s.cancelIncoming(id, "", "g:"+g.ID)
	writeJSON(w, 200, map[string]any{"state": "cancelled"})
}

// isOwnIP reports whether ip belongs to this machine, i.e. the request came from a browser
// running on this same computer (it then gets the full app, like the app window).
func (s *Server) isOwnIP(ip string) bool {
	for _, mine := range s.LANIPs() {
		if mine == ip {
			return true
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.String() == ip {
				return true
			}
		}
	}
	return false
}

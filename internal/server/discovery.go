package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Devices find each other with small UDP broadcasts on the local network:
// each announces itself on start and every 20 s, and a "query" makes everyone answer at once.
// A slower subnet scan remains as a fallback for networks that block broadcasts.

const DefaultDiscoveryPort = 47321

type PeerInfo struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Platform string    `json:"platform"`
	URL      string    `json:"url"`
	FP       string    `json:"fp"`
	Locked   bool      `json:"locked"`
	LastSeen time.Time `json:"lastSeen"`
}

type discPacket struct {
	SL       int    `json:"sl"`
	T        string `json:"t"` // "hi" or "q"
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Port     int    `json:"port"`
	FP       string `json:"fp"`
	Locked   bool   `json:"locked"`
}

type Discovery struct {
	mu     sync.Mutex
	self   func() discPacket
	lanIPs func() []string
	port   int
	extra  []string
	conn   *net.UDPConn
	peers  map[string]*PeerInfo
}

func newDiscovery(port int, extra []string, self func() discPacket, lanIPs func() []string) *Discovery {
	d := &Discovery{self: self, lanIPs: lanIPs, port: port, extra: extra, peers: map[string]*PeerInfo{}}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		log.Printf("discovery: UDP port %d unavailable (%v); using network scan only", port, err)
		return d
	}
	d.conn = conn
	go d.listen()
	go func() {
		d.send("q")
		for i := 0; ; i++ {
			d.send("hi")
			time.Sleep(20 * time.Second)
		}
	}()
	return d
}

func (d *Discovery) targets() []*net.UDPAddr {
	seen := map[string]bool{}
	var out []*net.UDPAddr
	add := func(host string, port int) {
		k := net.JoinHostPort(host, strconv.Itoa(port))
		if seen[k] {
			return
		}
		seen[k] = true
		if a, err := net.ResolveUDPAddr("udp4", k); err == nil {
			out = append(out, a)
		}
	}
	add("255.255.255.255", d.port)
	for _, ip := range d.lanIPs() {
		if v4 := net.ParseIP(ip).To4(); v4 != nil {
			add(fmt.Sprintf("%d.%d.%d.255", v4[0], v4[1], v4[2]), d.port)
		}
	}
	for _, e := range d.extra {
		if a, err := net.ResolveUDPAddr("udp4", e); err == nil {
			add(a.IP.String(), a.Port)
		}
	}
	return out
}

func (d *Discovery) send(kind string) {
	if d.conn == nil {
		return
	}
	p := d.self()
	p.SL, p.T = 2, kind
	b, _ := json.Marshal(p)
	for _, a := range d.targets() {
		_, _ = d.conn.WriteToUDP(b, a)
	}
}

func (d *Discovery) listen() {
	buf := make([]byte, 2048)
	for {
		n, src, err := d.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		var p discPacket
		if json.Unmarshal(buf[:n], &p) != nil || p.SL < 2 || p.ID == "" || p.Port <= 0 {
			continue
		}
		me := d.self()
		if p.ID == me.ID {
			continue
		}
		d.record(p, src.IP.String())
		if p.T == "q" {
			me.SL, me.T = 2, "hi"
			b, _ := json.Marshal(me)
			_, _ = d.conn.WriteToUDP(b, src)
		}
	}
}

// baseURL is the HTTPS address another Ferry device uses to reach host:port.
func baseURL(host string, port int) string {
	if port == 443 {
		return "https://" + host
	}
	return "https://" + net.JoinHostPort(host, strconv.Itoa(port))
}

func (d *Discovery) record(p discPacket, host string) {
	d.mu.Lock()
	d.peers[p.ID] = &PeerInfo{
		ID: p.ID, Name: p.Name, Platform: p.Platform, FP: p.FP, Locked: p.Locked,
		URL: baseURL(host, p.Port), LastSeen: time.Now(),
	}
	d.mu.Unlock()
}

// Online returns devices heard from recently.
func (d *Discovery) Online() []PeerInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	cut := time.Now().Add(-70 * time.Second)
	var out []PeerInfo
	for id, p := range d.peers {
		if p.LastSeen.Before(cut) {
			delete(d.peers, id)
			continue
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (d *Discovery) Find(id string) *PeerInfo {
	for _, p := range d.Online() {
		if p.ID == id {
			return &p
		}
	}
	return nil
}

// Refresh asks everyone to answer now; with scan=true it also probes the subnet.
func (d *Discovery) Refresh(scan bool) []PeerInfo {
	d.send("q")
	if scan {
		for _, p := range scanSubnet(d.lanIPs(), d.self().ID) {
			d.mu.Lock()
			if _, known := d.peers[p.ID]; !known || time.Since(d.peers[p.ID].LastSeen) > 10*time.Second {
				pp := p
				pp.LastSeen = time.Now()
				d.peers[p.ID] = &pp
			}
			d.mu.Unlock()
		}
	} else {
		time.Sleep(700 * time.Millisecond)
	}
	return d.Online()
}

// scanSubnet probes the usual Ferry ports on every address of the local /24 networks.
func scanSubnet(lanIPs []string, selfID string) []PeerInfo {
	self := map[string]bool{}
	for _, ip := range lanIPs {
		self[ip] = true
	}
	type cand struct {
		ip   string
		port int
	}
	cands := make(chan cand, 256)
	go func() {
		seenNet := map[string]bool{}
		for _, ip := range lanIPs {
			v4 := net.ParseIP(ip).To4()
			if v4 == nil {
				continue
			}
			key := fmt.Sprintf("%d.%d.%d", v4[0], v4[1], v4[2])
			if seenNet[key] {
				continue
			}
			seenNet[key] = true
			for h := 1; h < 255; h++ {
				host := fmt.Sprintf("%s.%d", key, h)
				if self[host] {
					continue
				}
				for _, p := range DefaultPorts[:2] {
					cands <- cand{host, p}
				}
			}
		}
		close(cands)
	}()
	var mu sync.Mutex
	byID := map[string]PeerInfo{}
	var wg sync.WaitGroup
	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range cands {
				conn, err := net.DialTimeout("tcp", net.JoinHostPort(c.ip, strconv.Itoa(c.port)), 350*time.Millisecond)
				if err != nil {
					continue
				}
				conn.Close()
				base := baseURL(c.ip, c.port)
				resp, err := insecureClient.Get(base + "/api/info")
				if err != nil {
					continue
				}
				var info struct {
					App, ID, Name, Platform, FP string
					Locked                      bool
				}
				_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&info)
				resp.Body.Close()
				if info.App != "ferry" || info.ID == "" || info.ID == selfID {
					continue
				}
				mu.Lock()
				if _, dup := byID[info.ID]; !dup {
					byID[info.ID] = PeerInfo{ID: info.ID, Name: info.Name, Platform: info.Platform, FP: info.FP, Locked: info.Locked, URL: base}
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	out := make([]PeerInfo, 0, len(byID))
	for _, p := range byID {
		out = append(out, p)
	}
	return out
}

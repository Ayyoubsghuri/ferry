package server

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// insecureClient talks to Ferry devices we haven't paired with (self-signed certificates).
// Only public data (shares reachable with a code) goes over it.
var insecureClient = &http.Client{
	Timeout: 3 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		Proxy:           nil,
	},
}

// streamTransport is used for long downloads from other devices (no overall timeout).
var streamTransport = &http.Transport{
	TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
	Proxy:                 nil,
	ResponseHeaderTimeout: 15 * time.Second,
}

var (
	pinnedMu      sync.Mutex
	pinnedClients = map[string]*http.Client{}
)

// pinnedClient only accepts the certificate key learned when the devices were paired.
func pinnedClient(fp string, stream bool) *http.Client {
	key := fmt.Sprintf("%s/%v", fp, stream)
	pinnedMu.Lock()
	defer pinnedMu.Unlock()
	if c, ok := pinnedClients[key]; ok {
		return c
	}
	tr := &http.Transport{
		Proxy: nil,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 {
					return errors.New("no certificate")
				}
				c, err := x509.ParseCertificate(raw[0])
				if err != nil {
					return err
				}
				if SPKIFingerprint(c) != fp {
					return errors.New("device identity changed; pair again")
				}
				return nil
			},
		},
		ResponseHeaderTimeout: 15 * time.Second,
	}
	c := &http.Client{Transport: tr}
	if !stream {
		c.Timeout = 8 * time.Second
	}
	pinnedClients[key] = c
	return c
}

// deviceURL is where a paired device can be reached right now.
func (s *Server) deviceURL(d *TrustedDevice) string {
	if p := s.disc.Find(d.ID); p != nil {
		if p.URL != d.LastURL {
			s.trust.Update(d.ID, func(t *TrustedDevice) { t.LastURL = p.URL; t.Name = p.Name })
		}
		return p.URL
	}
	return d.LastURL
}

func (s *Server) authHeaders(h http.Header, d *TrustedDevice) {
	h.Set("X-SL-Device", s.me.ID)
	h.Set("X-SL-Key", d.Key)
}

// callDevice sends an authenticated JSON request to a paired device.
func (s *Server) callDevice(d *TrustedDevice, method, path string, in, out any) error {
	base := s.deviceURL(d)
	if base == "" {
		return fmt.Errorf("%s isn't reachable right now", d.Name)
	}
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, body)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	s.authHeaders(req.Header, d)
	resp, err := pinnedClient(d.FP, false).Do(req)
	if err != nil {
		return fmt.Errorf("%s isn't reachable right now. Make sure it's on the same Wi-Fi with Ferry open", d.Name)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("%s answered %d", d.Name, resp.StatusCode)
		}
		return errors.New(e.Error)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// trustedFor returns the paired device whose current address is base, if any.
func (s *Server) trustedFor(base string) *TrustedDevice {
	for _, p := range s.disc.Online() {
		if p.URL == base {
			if d := s.trust.Get(p.ID); d != nil {
				return d
			}
		}
	}
	for _, d := range s.trust.List() {
		if d.LastURL == base {
			dd := d
			return &dd
		}
	}
	return nil
}

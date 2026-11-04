package server

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// The remote endpoints let a browser use a share that lives on another Ferry device
// through this one: no certificate warning, no leaving the page, and range requests pass
// through so videos and audio can stream and play before they're saved.

func (s *Server) remoteRequest(r *http.Request, path string, stream bool) (*http.Response, error) {
	base, err := cleanPeerBase(r.URL.Query().Get("u"))
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(r.Context(), "GET", base+path, nil)
	if rg := r.Header.Get("Range"); rg != "" {
		req.Header.Set("Range", rg)
	}
	if t := r.URL.Query().Get("t"); t != "" {
		req.Header.Set("X-SL-Offer", t)
	}
	client := insecureClient
	if stream {
		client = &http.Client{Transport: streamTransport}
	}
	if d := s.trustedFor(base); d != nil {
		s.authHeaders(req.Header, d)
		client = pinnedClient(d.FP, stream)
	}
	return client.Do(req)
}

func relay(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Content-Disposition", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func codeParam(r *http.Request) (string, bool) {
	c := r.URL.Query().Get("code")
	if len(c) != 6 {
		return "", false
	}
	for _, ch := range c {
		if ch < '0' || ch > '9' {
			return "", false
		}
	}
	return c, true
}

func (s *Server) handleRemoteShare(w http.ResponseWriter, r *http.Request) {
	code, ok := codeParam(r)
	if !ok {
		jsonErr(w, 400, "Invalid code")
		return
	}
	resp, err := s.remoteRequest(r, "/api/t/"+code, false)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "That device isn't reachable right now")
		return
	}
	relay(w, resp)
}

func (s *Server) handleRemoteFile(w http.ResponseWriter, r *http.Request) {
	code, ok := codeParam(r)
	id := r.URL.Query().Get("id")
	if !ok || id == "" || len(id) > 12 {
		jsonErr(w, 400, "Invalid request")
		return
	}
	path := fmt.Sprintf("/api/t/%s/f/%s", code, url.PathEscape(id))
	if r.URL.Query().Get("inline") == "1" {
		path += "?inline=1"
	}
	resp, err := s.remoteRequest(r, path, true)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "That device isn't reachable right now")
		return
	}
	relay(w, resp)
}

func (s *Server) handleRemoteZip(w http.ResponseWriter, r *http.Request) {
	code, ok := codeParam(r)
	if !ok {
		jsonErr(w, 400, "Invalid code")
		return
	}
	resp, err := s.remoteRequest(r, "/api/t/"+code+"/zip", true)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "That device isn't reachable right now")
		return
	}
	relay(w, resp)
}

func (s *Server) handleRemoteZipIndex(w http.ResponseWriter, r *http.Request) {
	code, ok := codeParam(r)
	id := r.URL.Query().Get("id")
	if !ok || id == "" || len(id) > 12 {
		jsonErr(w, 400, "Invalid request")
		return
	}
	resp, err := s.remoteRequest(r, fmt.Sprintf("/api/t/%s/zipindex/%s", code, url.PathEscape(id)), false)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "That device isn't reachable right now")
		return
	}
	relay(w, resp)
}

package server

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// LANIPs returns the private IPv4 addresses of this machine. On Android (where interface
// enumeration can be blocked) it falls back to the UDP routing trick and extra hints.
func LANIPs(hints []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		ip := net.ParseIP(s).To4()
		if ip == nil || !ip.IsPrivate() || seen[ip.String()] {
			return
		}
		seen[ip.String()] = true
		out = append(out, ip.String())
	}
	for _, h := range hints {
		add(h)
	}
	if conn, err := net.Dial("udp4", "192.168.255.255:9"); err == nil {
		add(conn.LocalAddr().(*net.UDPAddr).IP.String())
		conn.Close()
	}
	if conn, err := net.Dial("udp4", "8.8.8.8:53"); err == nil {
		add(conn.LocalAddr().(*net.UDPAddr).IP.String())
		conn.Close()
	}
	if ifaces, err := net.Interfaces(); err == nil {
		for _, ifc := range ifaces {
			if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, _ := ifc.Addrs()
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok {
					add(ipn.IP.String())
				}
			}
		}
	}
	// Prefer common home-LAN ranges first.
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

func rank(ip string) int {
	switch {
	case len(ip) > 8 && ip[:8] == "192.168.":
		return 0
	case len(ip) > 3 && ip[:3] == "10.":
		return 1
	default:
		return 2
	}
}

// ---------- TLS certificate ----------

// loadOrCreateCert keeps one private key for the life of the install (so paired devices can
// pin it) and re-issues the certificate whenever the network addresses change.
func loadOrCreateCert(dir string, hosts []string, ips []string) (tls.Certificate, string, error) {
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		if leaf, err := x509.ParseCertificate(c.Certificate[0]); err == nil && certCovers(leaf, hosts, ips) && time.Now().Before(leaf.NotAfter.Add(-24*time.Hour)) {
			return c, SPKIFingerprint(leaf), nil
		}
	}
	var key *ecdsa.PrivateKey
	if b, err := os.ReadFile(keyPath); err == nil {
		if blk, _ := pem.Decode(b); blk != nil {
			key, _ = x509.ParseECPrivateKey(blk.Bytes)
		}
	}
	if key == nil {
		var err error
		if key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			return tls.Certificate{}, "", err
		}
		kb, _ := x509.MarshalECPrivateKey(key)
		_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: hosts[0], Organization: []string{"Ferry"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(2, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              hosts,
	}
	for _, s := range append([]string{"127.0.0.1"}, ips...) {
		tpl.IPAddresses = append(tpl.IPAddresses, net.ParseIP(s))
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	_ = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return c, "", err
	}
	leaf, _ := x509.ParseCertificate(c.Certificate[0])
	return c, SPKIFingerprint(leaf), nil
}

// SPKIFingerprint identifies a device by its public key (stable across certificate re-issues).
func SPKIFingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

func certCovers(c *x509.Certificate, hosts, ips []string) bool {
	for _, h := range hosts {
		if c.VerifyHostname(h) != nil {
			return false
		}
	}
	for _, ip := range ips {
		if c.VerifyHostname(ip) != nil {
			return false
		}
	}
	return true
}

// ---------- One port, two protocols ----------
// splitListener accepts raw TCP connections and routes each to the TLS or plain-HTTP
// side by peeking at the first byte (0x16 = TLS handshake).

type peekConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekConn) Read(p []byte) (int, error) { return c.r.Read(p) }

type chanListener struct {
	addr   net.Addr
	ch     chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, errors.New("listener closed")
	}
}
func (l *chanListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *chanListener) Addr() net.Addr { return l.addr }

func splitListener(raw net.Listener) (tlsSide, plainSide *chanListener) {
	tlsSide = &chanListener{addr: raw.Addr(), ch: make(chan net.Conn), closed: make(chan struct{})}
	plainSide = &chanListener{addr: raw.Addr(), ch: make(chan net.Conn), closed: make(chan struct{})}
	go func() {
		for {
			c, err := raw.Accept()
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					continue
				}
				tlsSide.Close()
				plainSide.Close()
				return
			}
			go func(c net.Conn) {
				_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
				br := bufio.NewReader(c)
				b, err := br.Peek(1)
				_ = c.SetReadDeadline(time.Time{})
				if err != nil {
					c.Close()
					return
				}
				pc := &peekConn{Conn: c, r: br}
				target := plainSide
				if b[0] == 0x16 {
					target = tlsSide
				}
				select {
				case target.ch <- pc:
				case <-target.closed:
					c.Close()
				}
			}(c)
		}
	}()
	return
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

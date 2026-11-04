package server

import (
	"encoding/binary"
	"log"
	"net"
	"strings"
)

// A minimal mDNS responder that answers A queries for "<host>.local" with this
// machine's LAN address, so other devices can open https://ferry.local.

type mdnsResponder struct {
	host  string // fully qualified, lower-case, e.g. "ferry.local."
	ips   func() []string
	conn  *net.UDPConn
	group *net.UDPAddr
}

func startMDNS(hostname string, ips func() []string) {
	group := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	conn, err := net.ListenMulticastUDP("udp4", nil, group)
	if err != nil {
		log.Printf("mDNS disabled: %v", err)
		return
	}
	r := &mdnsResponder{host: strings.ToLower(hostname) + ".local.", ips: ips, conn: conn, group: group}
	log.Printf("mDNS: answering for %s", r.host)
	go r.loop()
}

func (r *mdnsResponder) loop() {
	buf := make([]byte, 9000)
	for {
		n, src, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		r.handle(buf[:n], src)
	}
}

func readName(msg []byte, off int) (string, int, bool) {
	var parts []string
	jumped := false
	next := 0
	for hops := 0; hops < 20; hops++ {
		if off >= len(msg) {
			return "", 0, false
		}
		l := int(msg[off])
		switch {
		case l == 0:
			if !jumped {
				next = off + 1
			}
			return strings.ToLower(strings.Join(parts, ".") + "."), next, true
		case l&0xC0 == 0xC0:
			if off+1 >= len(msg) {
				return "", 0, false
			}
			if !jumped {
				next = off + 2
			}
			off = int(binary.BigEndian.Uint16(msg[off:]) & 0x3FFF)
			jumped = true
		default:
			if off+1+l > len(msg) {
				return "", 0, false
			}
			parts = append(parts, string(msg[off+1:off+1+l]))
			off += 1 + l
		}
	}
	return "", 0, false
}

func (r *mdnsResponder) handle(msg []byte, src *net.UDPAddr) {
	if len(msg) < 12 {
		return
	}
	flags := binary.BigEndian.Uint16(msg[2:])
	if flags&0x8000 != 0 { // a response, not a query
		return
	}
	qd := int(binary.BigEndian.Uint16(msg[4:]))
	off := 12
	match := false
	unicast := false
	for i := 0; i < qd; i++ {
		name, n, ok := readName(msg, off)
		if !ok || n+4 > len(msg) {
			return
		}
		qtype := binary.BigEndian.Uint16(msg[n:])
		qclass := binary.BigEndian.Uint16(msg[n+2:])
		off = n + 4
		if name == r.host && (qtype == 1 || qtype == 255) {
			match = true
			if qclass&0x8000 != 0 {
				unicast = true
			}
		}
	}
	if !match {
		return
	}
	ips := r.pick(src.IP)
	if len(ips) == 0 {
		return
	}
	legacy := src.Port != 5353
	var id uint16
	if legacy {
		id = binary.BigEndian.Uint16(msg[0:])
	}
	resp := make([]byte, 12)
	binary.BigEndian.PutUint16(resp[0:], id)
	binary.BigEndian.PutUint16(resp[2:], 0x8400)
	binary.BigEndian.PutUint16(resp[6:], uint16(len(ips)))
	for _, ip := range ips {
		for _, label := range strings.Split(strings.TrimSuffix(r.host, "."), ".") {
			resp = append(resp, byte(len(label)))
			resp = append(resp, label...)
		}
		resp = append(resp, 0)
		cls := uint16(0x8001)
		if legacy {
			cls = 1
		}
		resp = binary.BigEndian.AppendUint16(resp, 1)
		resp = binary.BigEndian.AppendUint16(resp, cls)
		resp = binary.BigEndian.AppendUint32(resp, 120)
		resp = binary.BigEndian.AppendUint16(resp, 4)
		resp = append(resp, ip.To4()...)
	}
	dst := r.group
	if legacy || unicast {
		dst = src
	}
	_, _ = r.conn.WriteToUDP(resp, dst)
}

// pick prefers the address on the same /24 as the asker.
func (r *mdnsResponder) pick(asker net.IP) []net.IP {
	var all []net.IP
	for _, s := range r.ips() {
		if ip := net.ParseIP(s).To4(); ip != nil {
			all = append(all, ip)
		}
	}
	a4 := asker.To4()
	if a4 != nil {
		for _, ip := range all {
			if ip[0] == a4[0] && ip[1] == a4[1] && ip[2] == a4[2] {
				return []net.IP{ip}
			}
		}
	}
	return all
}

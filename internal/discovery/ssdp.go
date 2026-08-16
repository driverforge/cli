// Package discovery finds Control4 controllers on the local network via SSDP.
//
// Control4 directors announce themselves with the search target "c4:director"
// (the same mechanism Home Assistant's Control4 integration uses). The CLI sends
// an SSDP M-SEARCH for that ST and reads the unicast replies; each reply's
// LOCATION header carries the controller's *current* IP, which is what we use to
// confirm a graph device is live on this segment and to refresh a drifted DHCP
// address.
//
// SSDP is multicast, so it only reaches controllers on the same layer-2 segment
// as the machine running the CLI — a cross-VLAN controller won't answer here even
// though a direct unicast deploy to it would still route. So discovery is a
// liveness/address signal layered over the graph's device list, never the source
// of truth.
//
// We probe from every multicast-capable interface (one socket bound per interface
// address) rather than the default route only, so a controller reached over a
// secondary NIC/VLAN isn't missed.
package discovery

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ssdpMulticast is the standard SSDP IPv4 multicast endpoint.
const ssdpMulticast = "239.255.255.250:1900"

// SearchTargetC4Director is the SSDP ST that Control4 directors respond to.
const SearchTargetC4Director = "c4:director"

// SearchTargetAll matches any SSDP responder — useful for diagnostics ("is any
// SSDP device reachable from here at all?").
const SearchTargetAll = "ssdp:all"

// Controller is one SSDP responder.
type Controller struct {
	ST       string // search target it answered for (e.g. "c4:director")
	USN      string // unique service name, "uuid:<UDN>" — may or may not encode the MAC
	Location string // raw LOCATION header (a URL on the controller)
	Host     string // host/IP parsed from Location — the controller's current address
	Server   string // SERVER header (often "<os>/<ver> UPnP/1.0 <product>/<ver>")
}

// Source is a local interface address the search probes from.
type Source struct {
	Iface string
	IP    net.IP
}

func (s Source) String() string { return fmt.Sprintf("%s (%s)", s.Iface, s.IP) }

type options struct {
	onPacket     func(src Source, from net.Addr, raw []byte)
	onController func(Controller)
}

// Option configures a search.
type Option func(*options)

// WithPacketSink registers a callback invoked for every UDP datagram received,
// on any interface — for diagnostics (raw packet dumps). It may be called
// concurrently from multiple goroutines.
func WithPacketSink(fn func(src Source, from net.Addr, raw []byte)) Option {
	return func(o *options) { o.onPacket = fn }
}

// WithController registers a callback fired once per newly-discovered controller
// (deduped), as it is found — for live UIs that flip a row "online" the moment a
// controller's advertisement arrives rather than waiting for the whole window.
// It may be called concurrently from multiple goroutines.
func WithController(fn func(Controller)) Option {
	return func(o *options) { o.onController = fn }
}

// SearchControllers issues an SSDP M-SEARCH for Control4 directors and collects
// replies until timeout elapses (or ctx is cancelled), deduped by USN/Location.
func SearchControllers(ctx context.Context, timeout time.Duration, opts ...Option) ([]Controller, error) {
	return Search(ctx, SearchTargetC4Director, timeout, opts...)
}

// Search runs an M-SEARCH for an arbitrary ST across all multicast interfaces.
func Search(ctx context.Context, st string, timeout time.Duration, opts ...Option) ([]Controller, error) {
	var o options
	for _, fn := range opts {
		fn(&o)
	}

	ifaces, err := multicastInterfaces()
	if err != nil || len(ifaces) == 0 {
		return nil, fmt.Errorf("discovery: no multicast-capable IPv4 interfaces")
	}

	var mu sync.Mutex
	seen := map[string]bool{}
	var found []Controller
	add := func(c Controller) {
		key := c.USN
		if key == "" {
			key = c.Location
		}
		mu.Lock()
		if seen[key] {
			mu.Unlock()
			return
		}
		seen[key] = true
		found = append(found, c)
		mu.Unlock()
		if o.onController != nil {
			o.onController(c)
		}
	}

	var wg sync.WaitGroup
	for _, ifi := range ifaces {
		wg.Add(1)
		go func(ifi net.Interface) {
			defer wg.Done()
			probe(ctx, ifi, st, timeout, &o, add)
		}(ifi)
	}
	wg.Wait()
	return found, nil
}

func probe(ctx context.Context, ifi net.Interface, st string, timeout time.Duration, o *options, add func(Controller)) {
	group := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}

	// Group socket: joins the SSDP group on this interface to receive the
	// controllers' ssdp:alive NOTIFY advertisements (Control4 announces rather
	// than answering M-SEARCH). On macOS/Linux a group-bound socket only
	// receives datagrams addressed to the GROUP — a unicast M-SEARCH reply does
	// not arrive here, which is what the unicast socket below is for.
	mcast, err := net.ListenMulticastUDP("udp4", &ifi, group)
	if err != nil {
		return // interface can't join (down, no v4) — skip it
	}
	defer mcast.Close()
	_ = mcast.SetReadBuffer(1 << 20)

	// Unicast socket: bound to this interface's own IPv4, it both SENDS the
	// M-SEARCH and RECEIVES the unicast "HTTP/1.1 200 OK" replies spec-conformant
	// responders send back to the sender's address:port. Best-effort — if it
	// can't bind we fall back to sending from the group socket (NOTIFY-only,
	// today's behaviour).
	sender := mcast
	if ip := primaryV4(ifi); ip != nil {
		if uni, uerr := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: 0}); uerr == nil {
			defer uni.Close()
			_ = uni.SetReadBuffer(1 << 20)
			sender = uni
		}
	}

	pkt := mSearch(st, timeout)
	for i := 0; i < 3; i++ { // multicast is lossy; nudge it a few times
		_, _ = sender.WriteToUDP(pkt, group)
	}

	deadline := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}

	// Read both sockets until the window closes. onPacket/add are already
	// concurrency-safe (Search fans out one probe per interface).
	src := Source{Iface: ifi.Name}
	var wg sync.WaitGroup
	for _, conn := range distinct(mcast, sender) {
		wg.Add(1)
		go func(conn *net.UDPConn) {
			defer wg.Done()
			readResponses(ctx, conn, deadline, st, src, o, add)
		}(conn)
	}
	wg.Wait()
}

// distinct returns the unique connections (mcast and sender are the same when
// the unicast socket couldn't be bound).
func distinct(a, b *net.UDPConn) []*net.UDPConn {
	if a == b {
		return []*net.UDPConn{a}
	}
	return []*net.UDPConn{a, b}
}

// readResponses reads SSDP datagrams from conn until the deadline, forwarding
// each to the diagnostic sink and adding controllers that match the search.
func readResponses(ctx context.Context, conn *net.UDPConn, deadline time.Time, st string, src Source, o *options, add func(Controller)) {
	_ = conn.SetReadDeadline(deadline)
	buf := make([]byte, 2048)
	for ctx.Err() == nil {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			break // read deadline reached → search window over
		}
		raw := append([]byte(nil), buf[:n]...)
		if o.onPacket != nil {
			o.onPacket(src, from, raw)
		}
		if c, ok := parseResponse(raw); ok && matches(st, c.ST) {
			if c.Host == "" && from != nil {
				c.Host = from.IP.String() // NOTIFY may omit LOCATION; the sender IS the controller
			}
			add(c)
		}
	}
}

// matches reports whether a response ST satisfies the requested ST. A search for
// ssdp:all accepts any non-empty ST (responders reply with their own).
func matches(want, got string) bool {
	if got == "" {
		return false
	}
	return want == SearchTargetAll || got == want
}

// Sources lists the interfaces the search probes on (name + primary IPv4), for
// diagnostics.
func Sources() ([]Source, error) {
	ifaces, err := multicastInterfaces()
	if err != nil {
		return nil, err
	}
	out := make([]Source, 0, len(ifaces))
	for _, ifi := range ifaces {
		out = append(out, Source{Iface: ifi.Name, IP: primaryV4(ifi)})
	}
	return out, nil
}

// multicastInterfaces returns up, multicast-capable, non-loopback interfaces that
// have an IPv4 address (so we probe each segment, not just the default route).
func multicastInterfaces() ([]net.Interface, error) {
	all, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []net.Interface
	for _, ifi := range all {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if primaryV4(ifi) != nil {
			out = append(out, ifi)
		}
	}
	return out, nil
}

// primaryV4 returns the interface's first IPv4 address, or nil.
func primaryV4(ifi net.Interface) net.IP {
	addrs, _ := ifi.Addrs()
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip4 := ip.To4(); ip4 != nil {
			return ip4
		}
	}
	return nil
}

// mSearch builds an SSDP M-SEARCH datagram. MX (max wait the responder spreads
// its reply over) is capped to the search window, at least 1s per the spec.
func mSearch(st string, mx time.Duration) []byte {
	secs := int(mx.Seconds())
	if secs < 1 {
		secs = 1
	}
	return []byte(strings.Join([]string{
		"M-SEARCH * HTTP/1.1",
		"HOST: " + ssdpMulticast,
		`MAN: "ssdp:discover"`,
		fmt.Sprintf("MX: %d", secs),
		"ST: " + st,
		"", "",
	}, "\r\n"))
}

// parseResponse parses an SSDP message — both an M-SEARCH reply ("HTTP/1.1 200")
// and a passive "NOTIFY * HTTP/1.1" advertisement (Control4 controllers announce
// via NOTIFY rather than answering M-SEARCH). Only ssdp:alive NOTIFYs are kept;
// ssdp:byebye/update and anything else are dropped.
func parseResponse(b []byte) (Controller, bool) {
	tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(b)))
	start, err := tp.ReadLine()
	if err != nil {
		return Controller{}, false
	}
	isReply := strings.HasPrefix(start, "HTTP/1.1 200")
	isNotify := strings.HasPrefix(start, "NOTIFY ")
	if !isReply && !isNotify {
		return Controller{}, false
	}
	// Tolerate a missing trailing blank line: real controllers often send the
	// header block without the terminating CRLFCRLF, which makes ReadMIMEHeader
	// return an error alongside the (complete) headers it did parse.
	h, err := tp.ReadMIMEHeader()
	if err != nil && len(h) == 0 {
		return Controller{}, false
	}
	if isNotify && !strings.EqualFold(h.Get("Nts"), "ssdp:alive") {
		return Controller{}, false // ignore byebye/update advertisements
	}
	// A NOTIFY carries its type in NT; an M-SEARCH reply carries it in ST. Fall
	// back to NT so a spec-conformant advertisement (NT, no ST) isn't dropped by
	// matches().
	target := h.Get("St")
	if target == "" {
		target = h.Get("Nt")
	}
	c := Controller{
		ST:       target,
		USN:      h.Get("Usn"),
		Location: h.Get("Location"),
		Server:   h.Get("Server"),
	}
	c.Host = hostFromLocation(c.Location)
	return c, true
}

// MAC returns the controller's MAC parsed from the USN, lowercase and
// colon-separated (e.g. "00:0f:ff:a1:3a:46"), or "" if absent. Control4 encodes
// it as the trailing 12 hex digits of the USN ("…core5-000FFFA13A46"), which is
// what lets us match an SSDP hit to a graph device's deviceId.
func (c Controller) MAC() string {
	hex := longestHexRun(c.USN)
	if len(hex) < 12 {
		return ""
	}
	hex = strings.ToLower(hex[len(hex)-12:])
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(hex[i : i+2])
	}
	return b.String()
}

// NormalizeMAC reduces a MAC to bare lowercase hex (no separators), so a graph
// deviceId ("00:0D:6F:AB:12:34") and a controller's USN-derived MAC compare
// equal. Non-hex input yields "".
func NormalizeMAC(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
			b.WriteByte(c)
		case c >= 'A' && c <= 'F':
			b.WriteByte(c + 32)
		}
	}
	return b.String()
}

func longestHexRun(s string) string {
	best, start := "", -1
	isHex := func(r byte) bool {
		return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
	}
	for i := 0; i <= len(s); i++ {
		if i < len(s) && isHex(s[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if run := s[start:i]; len(run) > len(best) {
				best = run
			}
			start = -1
		}
	}
	return best
}

// hostFromLocation extracts the bare host/IP from a LOCATION URL.
func hostFromLocation(loc string) string {
	if loc == "" {
		return ""
	}
	if u, err := url.Parse(loc); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return ""
}

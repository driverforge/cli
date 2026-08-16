package discovery

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestMSearchFormat(t *testing.T) {
	got := string(mSearch("c4:director", 2*time.Second))
	for _, want := range []string{
		"M-SEARCH * HTTP/1.1\r\n",
		"HOST: 239.255.255.250:1900\r\n",
		"MAN: \"ssdp:discover\"\r\n",
		"MX: 2\r\n",
		"ST: c4:director\r\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("M-SEARCH missing %q in:\n%q", want, got)
		}
	}
	if !strings.HasSuffix(got, "\r\n\r\n") {
		t.Errorf("M-SEARCH must end with a blank line: %q", got)
	}
}

func TestMSearchMXFloorsToOne(t *testing.T) {
	if got := string(mSearch("x", 100*time.Millisecond)); !strings.Contains(got, "MX: 1\r\n") {
		t.Errorf("sub-second MX should floor to 1: %q", got)
	}
}

func TestParseResponse(t *testing.T) {
	raw := "HTTP/1.1 200 OK\r\n" +
		"CACHE-CONTROL: max-age=1800\r\n" +
		"LOCATION: https://192.168.1.10:443/c4/director.xml\r\n" +
		"ST: c4:director\r\n" +
		"USN: uuid:000d6f-aabbccdd\r\n" +
		"SERVER: Linux/4.14 UPnP/1.0 Control4/3.4.2\r\n\r\n"

	c, ok := parseResponse([]byte(raw))
	if !ok {
		t.Fatal("expected a parsed controller")
	}
	if c.ST != "c4:director" {
		t.Errorf("ST = %q", c.ST)
	}
	if c.USN != "uuid:000d6f-aabbccdd" {
		t.Errorf("USN = %q", c.USN)
	}
	if c.Host != "192.168.1.10" {
		t.Errorf("Host = %q, want 192.168.1.10 (from LOCATION)", c.Host)
	}
	if !strings.Contains(c.Server, "Control4") {
		t.Errorf("Server = %q", c.Server)
	}
}

func TestParseAliveNotify(t *testing.T) {
	// Control4 controllers announce via NOTIFY ssdp:alive (no LOCATION); the USN
	// carries model+MAC and the source IP supplies the address.
	notify := "NOTIFY * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"NTS: ssdp:alive\r\n" +
		"ST: c4:director\r\n" +
		"USN: c4:director:core5-000FFFA13A46\r\n\r\n"
	c, ok := parseResponse([]byte(notify))
	if !ok {
		t.Fatal("ssdp:alive NOTIFY should parse")
	}
	if c.ST != "c4:director" || c.USN != "c4:director:core5-000FFFA13A46" {
		t.Errorf("parsed = %+v", c)
	}
}

func TestParseAliveNotifyWithoutTrailingBlankLine(t *testing.T) {
	// Real controllers often omit the terminating CRLFCRLF; we must still parse.
	notify := "NOTIFY * HTTP/1.1\r\n" +
		"NTS: ssdp:alive\r\n" +
		"ST: c4:director\r\n" +
		"USN: c4:director:core5-000FFFA13A46\r\n" // no trailing blank line
	c, ok := parseResponse([]byte(notify))
	if !ok || c.ST != "c4:director" {
		t.Fatalf("should parse headers without a trailing blank line: %+v ok=%v", c, ok)
	}
}

func TestControllerMAC(t *testing.T) {
	if got := (Controller{USN: "c4:director:core5-000FFFA13A46"}).MAC(); got != "00:0f:ff:a1:3a:46" {
		t.Errorf("MAC = %q, want 00:0f:ff:a1:3a:46", got)
	}
	if got := (Controller{USN: "uuid:short-abc"}).MAC(); got != "" {
		t.Errorf("no 12-hex run should yield empty, got %q", got)
	}
}

func TestNormalizeMAC(t *testing.T) {
	cases := map[string]string{
		"00:0D:6F:AB:12:34": "000d6fab1234",
		"00-0f-ff-a1-3a-46": "000fffa13a46",
		"000FFFA13A46":      "000fffa13a46",
		"":                  "",
		"not-a-mac!":        "aac", // only hex letters survive (a, a, c)
	}
	for in, want := range cases {
		if got := NormalizeMAC(in); got != want {
			t.Errorf("NormalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
	// A controller's USN-derived MAC and a graph deviceId normalize equal.
	usn := (Controller{USN: "c4:director:core5-000FFFA13A46"}).MAC()
	if NormalizeMAC(usn) != NormalizeMAC("00:0F:FF:A1:3A:46") {
		t.Errorf("USN MAC %q should match graph deviceId after normalize", usn)
	}
}

func TestParseRejectsByebyeAndGarbage(t *testing.T) {
	byebye := "NOTIFY * HTTP/1.1\r\nNTS: ssdp:byebye\r\nST: c4:director\r\nUSN: x\r\n\r\n"
	if _, ok := parseResponse([]byte(byebye)); ok {
		t.Error("ssdp:byebye should be ignored")
	}
	if _, ok := parseResponse([]byte("garbage")); ok {
		t.Error("garbage should not parse")
	}
}

// A spec-conformant NOTIFY carries its type in NT (no ST); parseResponse must
// fall back to NT so matches() doesn't drop it.
func TestParseNotifyUsesNTWhenNoST(t *testing.T) {
	notify := "NOTIFY * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"NTS: ssdp:alive\r\n" +
		"NT: c4:director\r\n" +
		"USN: c4:director:core5-000FFFA13A46\r\n\r\n"
	c, ok := parseResponse([]byte(notify))
	if !ok {
		t.Fatal("NT-only NOTIFY should parse")
	}
	if c.ST != "c4:director" {
		t.Errorf("ST fell back to NT wrong: %q", c.ST)
	}
	if !matches(SearchTargetAll, c.ST) || !matches("c4:director", c.ST) {
		t.Error("NT-derived target should satisfy matches()")
	}
}

// An M-SEARCH reply's ST takes precedence over any NT.
func TestParseReplyPrefersST(t *testing.T) {
	reply := "HTTP/1.1 200 OK\r\n" +
		"ST: c4:director\r\n" +
		"NT: something-else\r\n" +
		"USN: c4:director:core5-000FFFA13A46\r\n\r\n"
	c, ok := parseResponse([]byte(reply))
	if !ok || c.ST != "c4:director" {
		t.Fatalf("reply ST should win: %+v", c)
	}
}

// readResponses must receive a UNICAST datagram sent to a plain UDP socket —
// the reply path that a group-bound socket misses on macOS/Linux. It drives
// the helper against a loopback socket pair.
func TestReadResponsesReceivesUnicastReply(t *testing.T) {
	recv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer recv.Close()

	reply := "HTTP/1.1 200 OK\r\n" +
		"ST: c4:director\r\n" +
		"USN: c4:director:core5-000FFFA13A46\r\n" +
		"LOCATION: http://127.0.0.1:80/\r\n\r\n"
	sender, err := net.DialUDP("udp4", nil, recv.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	if _, err := sender.Write([]byte(reply)); err != nil {
		t.Fatal(err)
	}

	var got []Controller
	add := func(c Controller) { got = append(got, c) }
	readResponses(context.Background(), recv, time.Now().Add(500*time.Millisecond),
		"c4:director", Source{Iface: "lo"}, &options{}, add)

	if len(got) != 1 {
		t.Fatalf("expected 1 controller from the unicast reply, got %d", len(got))
	}
	if got[0].ST != "c4:director" || got[0].MAC() != "00:0f:ff:a1:3a:46" {
		t.Errorf("parsed controller wrong: %+v", got[0])
	}
}

func TestDistinct(t *testing.T) {
	a, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	defer a.Close()
	b, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	defer b.Close()
	if len(distinct(a, a)) != 1 {
		t.Error("same conn should collapse to one")
	}
	if len(distinct(a, b)) != 2 {
		t.Error("different conns should both be read")
	}
}

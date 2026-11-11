package remote4

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Relay timing. The ping goes to the stub and back, so a pong proves the whole
// LAN path, agent included.
var (
	relayAttachTimeout = 5 * time.Second
	relayPingEvery     = 3 * time.Second
	relayPongTimeout   = 10 * time.Second
)

// maxRelayLine bounds one line on the relay; HTTP bodies the driver fetches
// travel inline.
const maxRelayLine = 16 << 20

// RelayState is reported as the LAN relay comes and goes.
type RelayState int

const (
	RelayAttached RelayState = iota
	RelayLost
)

// Transport is the session's Broker: the broker connection, plus the LAN
// relay through the controller's agent whenever one is attached (see the
// protocol doc's "LAN relay"). It sends over the relay while it is up and
// over the broker otherwise, and delivers what arrives on either.
type Transport struct {
	mqtt    Broker
	userID  string
	session string
	onState func(RelayState, error)

	attachTimeout, pingEvery, pongTimeout time.Duration

	mu       sync.Mutex
	handlers map[string]func(string, []byte) // by topic name
	relay    *relayLink                      // nil unless attached
	pending  map[int][]byte                  // c4_calls sent over the relay, awaiting a response
	answered map[int]bool                    // c4_response ids already delivered
	order    []int
}

type relayLink struct {
	conn    io.ReadWriteCloser
	writeMu sync.Mutex
	done    chan struct{}

	mu       sync.Mutex
	lastPong time.Time
}

// NewTransport wraps the session's broker connection. onState may be nil; it
// is called from the transport's goroutines and must not block.
func NewTransport(mqtt Broker, userID, sessionID string, onState func(RelayState, error)) *Transport {
	return &Transport{
		mqtt: mqtt, userID: userID, session: sessionID, onState: onState,
		attachTimeout: relayAttachTimeout, pingEvery: relayPingEvery, pongTimeout: relayPongTimeout,
		handlers: map[string]func(string, []byte){},
		pending:  map[int][]byte{},
		answered: map[int]bool{},
	}
}

func topicName(topic string) string { return topic[strings.LastIndexByte(topic, '/')+1:] }

// Publish sends over the relay when attached, otherwise over the broker.
func (t *Transport) Publish(topic string, payload []byte) error {
	name := topicName(topic)
	t.mu.Lock()
	link := t.relay
	tracked := false
	if link != nil && name == TopicC4Call {
		if id, ok := messageID(payload); ok {
			t.pending[id] = payload
			tracked = true
		}
	}
	t.mu.Unlock()

	if link != nil {
		err := link.write(name, payload)
		if err == nil {
			return nil
		}
		// The relay is gone; dropping it re-sends the pending calls, this one
		// included, over the broker.
		t.drop(link, fmt.Errorf("remote4: relay write: %w", err))
		if tracked {
			return nil
		}
	}
	return t.mqtt.Publish(topic, payload)
}

// Subscribe listens on the broker and on the relay. A c4_response is
// delivered once, whichever way it arrives first.
func (t *Transport) Subscribe(topic string, handler func(string, []byte)) error {
	name := topicName(topic)
	h := handler
	if name == TopicC4Response {
		h = func(topic string, payload []byte) {
			if t.firstResponse(payload) {
				handler(topic, payload)
			}
		}
	}
	t.mu.Lock()
	t.handlers[name] = h
	t.mu.Unlock()
	return t.mqtt.Subscribe(topic, h)
}

// Close drops the relay and disconnects from the broker.
func (t *Transport) Close() {
	t.mu.Lock()
	link := t.relay
	t.relay = nil
	t.mu.Unlock()
	if link != nil {
		link.close()
	}
	t.mqtt.Close()
}

// RelayActive reports whether traffic is on the LAN relay.
func (t *Transport) RelayActive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.relay != nil
}

func (t *Transport) firstResponse(payload []byte) bool {
	id, ok := messageID(payload)
	if !ok {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pending, id)
	if t.answered[id] {
		return false
	}
	t.answered[id] = true
	t.order = append(t.order, id)
	if len(t.order) > 256 {
		delete(t.answered, t.order[0])
		t.order = t.order[1:]
	}
	return true
}

// Attach takes over conn, a relay the agent has just upgraded, and moves the
// session's traffic onto it once the stub acknowledges. Until then, and if it
// never does, the broker carries everything. Attach returns once the relay is
// live or has failed; conn is closed on failure.
func (t *Transport) Attach(ctx context.Context, conn io.ReadWriteCloser) error {
	link := &relayLink{conn: conn, done: make(chan struct{})}
	attached := make(chan struct{})
	var once sync.Once
	go t.read(link, func() { once.Do(func() { close(attached) }) })

	timer := time.NewTimer(t.attachTimeout)
	defer timer.Stop()
	select {
	case <-attached:
	case <-link.done:
		return errors.New("remote4: the relay closed before the stub answered")
	case <-timer.C:
		link.close()
		return errors.New("remote4: the stub didn't answer on the relay")
	case <-ctx.Done():
		link.close()
		return ctx.Err()
	}

	link.mu.Lock()
	link.lastPong = time.Now()
	link.mu.Unlock()
	t.mu.Lock()
	t.relay = link
	t.mu.Unlock()
	select {
	case <-link.done: // closed between the ack and now
		t.drop(link, errors.New("remote4: the relay closed as it attached"))
		return errors.New("remote4: the relay closed as it attached")
	default:
	}
	if t.onState != nil {
		t.onState(RelayAttached, nil)
	}
	go t.ping(link)
	return nil
}

type relayLine struct {
	Topic string          `json:"topic"`
	Msg   json.RawMessage `json:"msg"`
}

func (t *Transport) read(link *relayLink, onAttached func()) {
	sc := bufio.NewScanner(link.conn)
	sc.Buffer(make([]byte, 64<<10), maxRelayLine)
	for sc.Scan() {
		var l relayLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil || l.Topic == "" {
			continue
		}
		if l.Topic == "relay" {
			var m struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(l.Msg, &m)
			switch m.Type {
			case "attached":
				onAttached()
			case "pong":
				link.mu.Lock()
				link.lastPong = time.Now()
				link.mu.Unlock()
			}
			continue
		}
		t.mu.Lock()
		h := t.handlers[l.Topic]
		t.mu.Unlock()
		if h != nil {
			h(Topic(t.userID, t.session, l.Topic), []byte(l.Msg))
		}
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	link.close()
	t.drop(link, fmt.Errorf("remote4: relay closed: %w", err))
}

func (t *Transport) ping(link *relayLink) {
	tick := time.NewTicker(t.pingEvery)
	defer tick.Stop()
	for id := 1; ; id++ {
		select {
		case <-link.done:
			return
		case <-tick.C:
		}
		link.mu.Lock()
		silent := time.Since(link.lastPong)
		link.mu.Unlock()
		if silent > t.pongTimeout {
			t.drop(link, fmt.Errorf("remote4: no answer on the relay for %s", silent.Round(time.Second)))
			return
		}
		if err := link.write("ping", []byte(fmt.Sprintf(`{"id":%d}`, id))); err != nil {
			t.drop(link, fmt.Errorf("remote4: relay write: %w", err))
			return
		}
	}
}

// drop takes traffic off link and back to the broker: the stub is told to do
// the same, and any C4 call still waiting on the relay is sent again (the stub
// answers a repeat from its cache, so nothing runs twice).
func (t *Transport) drop(link *relayLink, cause error) {
	t.mu.Lock()
	if t.relay != link {
		t.mu.Unlock()
		return
	}
	t.relay = nil
	ids := make([]int, 0, len(t.pending))
	for id := range t.pending {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	resend := make([][]byte, 0, len(ids))
	for _, id := range ids {
		resend = append(resend, t.pending[id])
	}
	t.pending = map[int][]byte{}
	t.mu.Unlock()

	link.close()
	if t.onState != nil {
		t.onState(RelayLost, cause)
	}
	_ = t.mqtt.Publish(Topic(t.userID, t.session, TopicControl), Encode(Message{Type: "relay_detach"}))
	for _, payload := range resend {
		_ = t.mqtt.Publish(Topic(t.userID, t.session, TopicC4Call), payload)
	}
}

func (l *relayLink) write(name string, payload []byte) error {
	line := make([]byte, 0, len(payload)+len(name)+24)
	line = append(line, `{"topic":"`...)
	line = append(line, name...)
	line = append(line, `","msg":`...)
	line = append(line, payload...)
	line = append(line, "}\n"...)
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	_, err := l.conn.Write(line)
	return err
}

func (l *relayLink) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.done:
	default:
		close(l.done)
		_ = l.conn.Close()
	}
}

func messageID(payload []byte) (int, bool) {
	var m struct {
		ID *int `json:"id"`
	}
	if json.Unmarshal(payload, &m) != nil || m.ID == nil {
		return 0, false
	}
	return *m.ID, true
}

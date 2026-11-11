package remote4

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRelay is the agent and stub end of a relay: it records the lines the
// CLI sends and lets the test answer.
type fakeRelay struct {
	conn  net.Conn
	lines chan relayLine
}

func newFakeRelay(t *testing.T) (*fakeRelay, net.Conn) {
	t.Helper()
	cli, far := net.Pipe()
	f := &fakeRelay{conn: far, lines: make(chan relayLine, 64)}
	go func() {
		sc := bufio.NewScanner(far)
		for sc.Scan() {
			var l relayLine
			if json.Unmarshal(sc.Bytes(), &l) == nil {
				f.lines <- l
			}
		}
		close(f.lines)
	}()
	t.Cleanup(func() { far.Close() })
	return f, cli
}

func (f *fakeRelay) send(t *testing.T, topic, msg string) {
	t.Helper()
	if _, err := f.conn.Write([]byte(`{"topic":"` + topic + `","msg":` + msg + "}\n")); err != nil {
		t.Fatalf("fake relay write: %v", err)
	}
}

// next returns the next line the CLI sent that isn't a ping.
func (f *fakeRelay) next(t *testing.T) relayLine {
	t.Helper()
	for {
		select {
		case l, ok := <-f.lines:
			if !ok {
				t.Fatal("relay closed")
			}
			if l.Topic != "ping" {
				return l
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no line from the CLI")
		}
	}
}

type stateLog struct {
	mu     sync.Mutex
	states []RelayState
}

func (s *stateLog) record(st RelayState, _ error) {
	s.mu.Lock()
	s.states = append(s.states, st)
	s.mu.Unlock()
}

func (s *stateLog) last() (RelayState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.states) == 0 {
		return 0, false
	}
	return s.states[len(s.states)-1], true
}

func attachedTransport(t *testing.T) (*Transport, *MemoryBroker, *fakeRelay, *stateLog, chan Message) {
	t.Helper()
	mb := NewMemoryBroker()
	states := &stateLog{}
	tr := NewTransport(mb, "u", "s", states.record)
	responses := make(chan Message, 8)
	if err := tr.Subscribe(Topic("u", "s", TopicC4Response), func(_ string, p []byte) {
		m, _ := Decode(p)
		responses <- m
	}); err != nil {
		t.Fatal(err)
	}
	fake, conn := newFakeRelay(t)
	done := make(chan error, 1)
	go func() { done <- tr.Attach(context.Background(), conn) }()
	fake.send(t, "relay", `{"type":"attached"}`)
	if err := <-done; err != nil {
		t.Fatalf("Attach: %v", err)
	}
	t.Cleanup(tr.Close)
	return tr, mb, fake, states, responses
}

func published(mb *MemoryBroker, name string) []Message {
	var out []Message
	for _, p := range mb.Published() {
		if strings.HasSuffix(p.Topic, "/"+name) {
			m, _ := Decode(p.Payload)
			out = append(out, m)
		}
	}
	return out
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTransportSendsOverTheRelayOnceAttached(t *testing.T) {
	tr, mb, fake, states, responses := attachedTransport(t)
	if !tr.RelayActive() {
		t.Fatal("relay not active")
	}
	if st, _ := states.last(); st != RelayAttached {
		t.Errorf("state = %v", st)
	}

	call := Encode(Message{ID: 7, Method: "GetDeviceID"})
	if err := tr.Publish(Topic("u", "s", TopicC4Call), call); err != nil {
		t.Fatal(err)
	}
	l := fake.next(t)
	if l.Topic != TopicC4Call || string(l.Msg) != string(call) {
		t.Errorf("relay got %s %s", l.Topic, l.Msg)
	}
	if n := len(published(mb, TopicC4Call)); n != 0 {
		t.Errorf("%d c4_calls went over the broker", n)
	}

	fake.send(t, TopicC4Response, `{"v":1,"id":7,"ok":true,"results":[42],"argc":1}`)
	select {
	case m := <-responses:
		if m.ID != 7 {
			t.Errorf("response id %d", m.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no response delivered")
	}
}

func TestTransportDeliversAResponseOnceWhicheverWayItComes(t *testing.T) {
	tr, mb, fake, _, responses := attachedTransport(t)
	_ = tr.Publish(Topic("u", "s", TopicC4Call), Encode(Message{ID: 3, Method: "X"}))
	fake.next(t)
	fake.send(t, TopicC4Response, `{"v":1,"id":3,"ok":true}`)
	<-responses
	mb.Deliver(Topic("u", "s", TopicC4Response), []byte(`{"v":1,"id":3,"ok":true}`))
	select {
	case m := <-responses:
		t.Fatalf("duplicate response %d delivered", m.ID)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTransportFallsBackAndResendsTheWaitingCallWhenTheRelayCloses(t *testing.T) {
	tr, mb, fake, states, _ := attachedTransport(t)
	call := Encode(Message{ID: 9, Method: "GetDeviceID"})
	_ = tr.Publish(Topic("u", "s", TopicC4Call), call)
	fake.next(t)
	fake.conn.Close()

	waitFor(t, func() bool { return !tr.RelayActive() })
	waitFor(t, func() bool { return len(published(mb, TopicC4Call)) == 1 })
	if st, _ := states.last(); st != RelayLost {
		t.Errorf("state = %v", st)
	}
	control := published(mb, TopicControl)
	if len(control) != 1 || control[0].Type != "relay_detach" {
		t.Errorf("control = %+v", control)
	}
	// The resend comes after the detach, so the stub answers it on the broker.
	all := mb.Published()
	if !strings.HasSuffix(all[len(all)-1].Topic, "/"+TopicC4Call) || string(all[len(all)-1].Payload) != string(call) {
		t.Errorf("last publish = %s %s", all[len(all)-1].Topic, all[len(all)-1].Payload)
	}

	// Later traffic goes over the broker.
	_ = tr.Publish(Topic("u", "s", TopicCallbackResponse), Encode(Message{ID: 1}))
	if n := len(published(mb, TopicCallbackResponse)); n != 1 {
		t.Errorf("%d callback_responses over the broker", n)
	}
}

func TestTransportDoesNotResendAnsweredCalls(t *testing.T) {
	tr, mb, fake, _, responses := attachedTransport(t)
	_ = tr.Publish(Topic("u", "s", TopicC4Call), Encode(Message{ID: 1, Method: "X"}))
	fake.next(t)
	fake.send(t, TopicC4Response, `{"v":1,"id":1,"ok":true}`)
	<-responses
	fake.conn.Close()
	waitFor(t, func() bool { return !tr.RelayActive() })
	if n := len(published(mb, TopicC4Call)); n != 0 {
		t.Errorf("%d answered calls resent", n)
	}
}

func TestTransportDropsASilentRelay(t *testing.T) {
	defer func(every, timeout time.Duration) { relayPingEvery, relayPongTimeout = every, timeout }(relayPingEvery, relayPongTimeout)
	relayPingEvery, relayPongTimeout = 10*time.Millisecond, 50*time.Millisecond

	tr, _, fake, _, _ := attachedTransport(t)
	go func() {
		for range fake.lines { // swallow pings without answering
		}
	}()
	waitFor(t, func() bool { return !tr.RelayActive() })
}

func TestTransportKeepsARelayThatAnswersPings(t *testing.T) {
	defer func(every, timeout time.Duration) { relayPingEvery, relayPongTimeout = every, timeout }(relayPingEvery, relayPongTimeout)
	relayPingEvery, relayPongTimeout = 10*time.Millisecond, 50*time.Millisecond

	tr, _, fake, _, _ := attachedTransport(t)
	go func() {
		for l := range fake.lines {
			if l.Topic == "ping" {
				_, _ = fake.conn.Write([]byte(`{"topic":"relay","msg":{"type":"pong"}}` + "\n"))
			}
		}
	}()
	time.Sleep(150 * time.Millisecond)
	if !tr.RelayActive() {
		t.Fatal("relay dropped despite pongs")
	}
}

func TestTransportAttachFailsWithoutTheStubsAnswer(t *testing.T) {
	defer func(d time.Duration) { relayAttachTimeout = d }(relayAttachTimeout)
	relayAttachTimeout = 30 * time.Millisecond

	mb := NewMemoryBroker()
	tr := NewTransport(mb, "u", "s", nil)
	_, conn := newFakeRelay(t)
	if err := tr.Attach(context.Background(), conn); err == nil {
		t.Fatal("Attach succeeded without an ack")
	}
	if tr.RelayActive() {
		t.Fatal("relay active")
	}
	_ = tr.Publish(Topic("u", "s", TopicC4Call), Encode(Message{ID: 1}))
	if n := len(published(mb, TopicC4Call)); n != 1 {
		t.Errorf("%d calls over the broker", n)
	}
}

func TestTransportDeliversStubMessagesFromTheRelay(t *testing.T) {
	mb := NewMemoryBroker()
	tr := NewTransport(mb, "u", "s", nil)
	got := make(chan string, 4)
	_ = tr.Subscribe(Topic("u", "s", TopicStatus), func(topic string, p []byte) { got <- topic + " " + string(p) })
	fake, conn := newFakeRelay(t)
	go func() { _ = tr.Attach(context.Background(), conn) }()
	fake.send(t, "relay", `{"type":"attached"}`)
	fake.send(t, TopicStatus, `{"v":1,"type":"online"}`)
	select {
	case s := <-got:
		if s != `remote4/u/s/status {"v":1,"type":"online"}` {
			t.Errorf("got %s", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("status not delivered")
	}
	tr.Close()
}

package remote4

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// EventKind classifies what the bridge reports to the terminal.
type EventKind int

const (
	EventCompiled EventKind = iota
	EventReady
	EventCallback
	EventCallbackResult
	EventC4Call
	EventC4Result
	EventPrint
	EventRuntimeLog
	EventReloaded
	EventFatal
	EventStubOnline
	EventStubOffline
	EventStubLog
	EventRuntimeExited
)

// Event is one thing worth showing the developer.
type Event struct {
	Kind    EventKind
	Message Message
	Err     error
}

// Bridge relays messages between the local runtime and the broker for one
// session, and reports what passes through as Events.
type Bridge struct {
	rt      *Runtime
	broker  Broker
	userID  string
	session string
	events  chan Event

	mu    sync.Mutex
	ended bool

	// eventsMu guards closing events: broker callbacks run on the MQTT client's
	// goroutines and can still deliver after Run has returned.
	eventsMu     sync.Mutex
	eventsClosed bool
}

// NewBridge wires rt and broker together for the session; Run starts relaying.
func NewBridge(rt *Runtime, broker Broker, userID, sessionID string) *Bridge {
	return &Bridge{rt: rt, broker: broker, userID: userID, session: sessionID, events: make(chan Event, 256)}
}

// Events is the stream the terminal renders. Closed when Run returns.
func (b *Bridge) Events() <-chan Event { return b.events }

func (b *Bridge) topic(name string) string { return Topic(b.userID, b.session, name) }

func (b *Bridge) emit(e Event) {
	b.eventsMu.Lock()
	defer b.eventsMu.Unlock()
	if b.eventsClosed {
		return
	}
	select {
	case b.events <- e:
	default: // a stalled renderer must not stall the proxy
	}
}

// Run subscribes to the stub's channels and pumps runtime output to the
// broker until the runtime exits or ctx ends.
func (b *Bridge) Run(ctx context.Context) error {
	defer func() {
		b.eventsMu.Lock()
		b.eventsClosed = true
		close(b.events)
		b.eventsMu.Unlock()
	}()

	fromStub := func(kind EventKind, to string) func(string, []byte) {
		return func(_ string, payload []byte) {
			m, err := Decode(payload)
			if err != nil {
				b.emit(Event{Kind: EventStubLog, Err: fmt.Errorf("bad %s message from stub: %w", to, err)})
				return
			}
			m.Type = to
			b.emit(Event{Kind: kind, Message: m})
			if err := b.rt.Send(m); err != nil {
				b.emit(Event{Kind: EventRuntimeLog, Err: err})
			}
		}
	}
	if err := b.broker.Subscribe(b.topic(TopicCallback), fromStub(EventCallback, "callback")); err != nil {
		return err
	}
	if err := b.broker.Subscribe(b.topic(TopicC4Response), fromStub(EventC4Result, "c4_response")); err != nil {
		return err
	}
	if err := b.broker.Subscribe(b.topic(TopicStatus), func(_ string, payload []byte) {
		m, err := Decode(payload)
		if err != nil {
			return
		}
		switch m.Type {
		case "online":
			b.emit(Event{Kind: EventStubOnline, Message: m})
		case "offline":
			b.emit(Event{Kind: EventStubOffline, Message: m})
		default:
			b.emit(Event{Kind: EventStubLog, Message: m})
		}
	}); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m, ok := <-b.rt.Messages():
			if !ok {
				b.emit(Event{Kind: EventRuntimeExited, Err: b.rt.Err()})
				return errors.New("runtime exited")
			}
			b.fromRuntime(m)
		}
	}
}

func (b *Bridge) fromRuntime(m Message) {
	switch m.Type {
	case "c4_call":
		b.emit(Event{Kind: EventC4Call, Message: m})
		b.publish(TopicC4Call, m)
	case "callback_response":
		b.emit(Event{Kind: EventCallbackResult, Message: m})
		b.publish(TopicCallbackResponse, m)
	case "compiled":
		b.emit(Event{Kind: EventCompiled, Message: m})
	case "ready":
		b.emit(Event{Kind: EventReady, Message: m})
	case "print", "raw":
		b.emit(Event{Kind: EventPrint, Message: m})
	case "reloaded":
		b.emit(Event{Kind: EventReloaded, Message: m})
	case "fatal":
		b.emit(Event{Kind: EventFatal, Message: m})
	case "forward_log":
		// The driver's log line, already printed locally, on its way to Anvil
		// through the stub's SDK.
		b.toStub(Message{Type: "log", Level: m.Level, Args: m.Args, Argc: m.Argc}, "a log line")
	case "forward_error":
		// The driver's error, already shown locally, on its way to Anvil.
		b.toStub(Message{Type: "error", Error: m.Error, Name: m.Name, Trace: m.Trace}, "an error")
	default:
		b.emit(Event{Kind: EventRuntimeLog, Message: m})
	}
}

func (b *Bridge) publish(name string, m Message) {
	m.Type = "" // the broker envelopes carry no type; the topic says it
	if err := b.broker.Publish(b.topic(name), Encode(m)); err != nil {
		b.emit(Event{Kind: EventStubLog, Err: fmt.Errorf("publishing %s: %w", name, err)})
	}
}

// toStub sends the driver's telemetry to the stub over the control topic, for
// its SDK to pass to Anvil.
func (b *Bridge) toStub(m Message, what string) {
	if err := b.broker.Publish(b.topic(TopicControl), Encode(m)); err != nil {
		b.emit(Event{Kind: EventStubLog, Err: fmt.Errorf("forwarding %s: %w", what, err)})
	}
}

// SendToken pushes a refreshed broker token to the stub for its reconnects.
func (b *Bridge) SendToken(token string) error {
	return b.broker.Publish(b.topic(TopicControl), Encode(Message{Type: "token", Token: token}))
}

// Load tells the runtime to run the driver; sent once the stub is online so
// file-scope C4 calls have somewhere to go.
// Load starts the driver in the runtime. env is the context the stub announced
// itself with — the controller's constant globals the driver may read.
func (b *Bridge) Load(env json.RawMessage) error {
	return b.rt.Send(Message{Type: "load", Ctx: env})
}

// Reload tells the runtime to re-read the driver source.
func (b *Bridge) Reload() error { return b.rt.Send(Message{Type: "reload"}) }

// End tells the stub the session is over. Idempotent.
func (b *Bridge) End() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ended {
		return nil
	}
	b.ended = true
	return b.broker.Publish(b.topic(TopicControl), Encode(Message{Type: "end"}))
}

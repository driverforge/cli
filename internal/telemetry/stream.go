package telemetry

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	centrifuge "github.com/centrifugal/centrifuge-go"
)

// maxReconnectDelay caps the SDK's exponential reconnect backoff, so a dropped
// connection is retried at least this often.
const maxReconnectDelay = 10 * time.Second

// StreamState is a change in a Stream's subscription.
type StreamState int

const (
	// Subscribed: the subscription is live, first or after a reconnect.
	// Publications sent while it was down were not delivered, so a reader that
	// must not miss any fills the gap from the query API.
	Subscribed StreamState = iota + 1
	// Lost: the connection or subscription dropped. The client reconnects on
	// its own, with backoff capped at 10 seconds.
	Lost
)

// Stream is a live subscription to a project's telemetry channel that delivers
// every publication as the raw JSON it arrived as. Unlike Watcher it never
// drops one: publications queue without bound until read, because a reader
// following a driver's output must see every entry.
type Stream struct {
	client *centrifuge.Client
	pubs   chan json.RawMessage
	states chan StreamState

	mu      sync.Mutex
	queue   []json.RawMessage
	wake    chan struct{}
	done    chan struct{}
	closing sync.Once
}

// OpenStream connects to Centrifugo and subscribes to the project's channel.
// It returns once the connection is started, not once it is live: wait for
// Subscribed on States before relying on it. getToken is called for the first
// connection and again whenever the SDK needs to refresh the token, so a long
// follow outlives one token. Call Close when done.
func OpenStream(ctx context.Context, projectID string, getToken TokenFunc) (*Stream, error) {
	token, err := getToken(ctx)
	if err != nil {
		return nil, err
	}
	client := newClient(centrifuge.Config{
		Token: token,
		GetToken: func(centrifuge.ConnectionTokenEvent) (string, error) {
			return getToken(ctx)
		},
		MaxReconnectDelay: maxReconnectDelay,
	})
	sub, err := client.NewSubscription(channel(projectID))
	if err != nil {
		client.Close()
		return nil, err
	}

	s := &Stream{
		client: client,
		pubs:   make(chan json.RawMessage),
		// Buffered so a burst of reconnects never blocks the SDK's callback
		// goroutine; a reader only needs the latest few transitions.
		states: make(chan StreamState, 16),
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}

	ch := channel(projectID)
	client.OnError(func(e centrifuge.ErrorEvent) { debugf("client error: %v", e.Error) })
	client.OnDisconnected(func(e centrifuge.DisconnectedEvent) {
		debugf("disconnected: code=%d reason=%q", e.Code, e.Reason)
		s.state(Lost)
	})
	client.OnConnecting(func(e centrifuge.ConnectingEvent) {
		debugf("connecting: code=%d reason=%q", e.Code, e.Reason)
		s.state(Lost)
	})
	sub.OnSubscribed(func(centrifuge.SubscribedEvent) {
		debugf("subscribed to %s", ch)
		s.state(Subscribed)
	})
	sub.OnSubscribing(func(e centrifuge.SubscribingEvent) {
		debugf("resubscribing to %s: code=%d reason=%q", ch, e.Code, e.Reason)
		s.state(Lost)
	})
	sub.OnError(func(e centrifuge.SubscriptionErrorEvent) { debugf("subscribe error on %s: %v", ch, e.Error) })
	sub.OnPublication(func(e centrifuge.PublicationEvent) {
		s.mu.Lock()
		s.queue = append(s.queue, json.RawMessage(e.Data))
		s.mu.Unlock()
		select {
		case s.wake <- struct{}{}:
		default:
		}
	})

	go s.forward()

	if err := sub.Subscribe(); err != nil {
		s.Close()
		return nil, err
	}
	if err := client.Connect(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// state reports a transition without ever blocking the SDK. When the buffer
// is full the oldest transitions are the ones lost, which a reader acting on
// the latest state does not need.
func (s *Stream) state(st StreamState) {
	select {
	case s.states <- st:
	default:
	}
}

// forward moves queued publications to the unbuffered pubs channel in arrival
// order, until Close.
func (s *Stream) forward() {
	for {
		s.mu.Lock()
		batch := s.queue
		s.queue = nil
		s.mu.Unlock()
		for _, p := range batch {
			select {
			case s.pubs <- p:
			case <-s.done:
				return
			}
		}
		select {
		case <-s.wake:
		case <-s.done:
			return
		}
	}
}

// Publications delivers every publication on the channel, in arrival order.
func (s *Stream) Publications() <-chan json.RawMessage { return s.pubs }

// States delivers subscription transitions.
func (s *Stream) States() <-chan StreamState { return s.states }

// Close tears down the subscription and connection. Safe to call more than
// once and on a nil Stream.
func (s *Stream) Close() {
	if s == nil {
		return
	}
	s.closing.Do(func() {
		close(s.done)
		s.client.Close()
	})
}

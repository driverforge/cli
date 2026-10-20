package remote4

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Endpoint configuration, overridable at build time via -ldflags -X and at
// runtime via DRIVERFORGE_MQTT_URL — the same pattern as internal/telemetry.
var (
	// BrokerURL is the Anvil MQTT broker both ends of a session connect to.
	// Dev: tcp://localhost:1883.
	BrokerURL = "ssl://ingest.driverforge.com:8883"
)

func resolveBrokerURL() string {
	if v := os.Getenv("DRIVERFORGE_MQTT_URL"); v != "" {
		return v
	}
	return BrokerURL
}

// BrokerEndpoint is the broker address split the way the stub's config and
// the Go client each need it.
type BrokerEndpoint struct {
	URL  string
	Host string
	Port int
	TLS  bool
}

// ResolveBroker parses the configured broker URL.
func ResolveBroker() (BrokerEndpoint, error) {
	raw := resolveBrokerURL()
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return BrokerEndpoint{}, fmt.Errorf("remote4: invalid broker URL %q", raw)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		host = u.Host
		portStr = ""
	}
	ep := BrokerEndpoint{URL: raw, Host: host}
	switch u.Scheme {
	case "ssl", "tls", "mqtts", "tcps":
		ep.TLS = true
		ep.Port = 8883
	case "tcp", "mqtt":
		ep.Port = 1883
	default:
		return BrokerEndpoint{}, fmt.Errorf("remote4: unsupported broker scheme %q (use tcp:// or ssl://)", u.Scheme)
	}
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil {
			return BrokerEndpoint{}, fmt.Errorf("remote4: invalid broker port %q", portStr)
		}
		ep.Port = p
	}
	return ep, nil
}

// Broker is the slice of an MQTT client the bridge needs. The paho-backed
// implementation is the real thing; tests use an in-memory one.
type Broker interface {
	Publish(topic string, payload []byte) error
	Subscribe(topic string, handler func(topic string, payload []byte)) error
	Close()
}

// ConnectionState events let the terminal show broker connectivity.
type ConnectionState int

const (
	StateConnected ConnectionState = iota
	StateLost
	StateReconnecting
)

// BrokerOptions configures Connect.
type BrokerOptions struct {
	Endpoint BrokerEndpoint
	ClientID string
	// Token returns the current mqtt-broker JWT; called on every (re)connect
	// so a refreshed token is used after a drop.
	Token func() (string, error)
	// OnState is invoked from the client's goroutines and must not block.
	OnState func(ConnectionState, error)
}

type pahoBroker struct {
	client mqtt.Client
}

// Connect dials the broker and blocks until the first connection succeeds
// or ctx ends. Later drops reconnect automatically with backoff.
func Connect(ctx context.Context, o BrokerOptions) (Broker, error) {
	opts := mqtt.NewClientOptions().
		AddBroker(o.Endpoint.URL).
		SetClientID(o.ClientID).
		SetCredentialsProvider(func() (string, string) {
			tok, err := o.Token()
			if err != nil {
				return "remote4-cli", ""
			}
			return "remote4-cli", tok
		}).
		SetAutoReconnect(true).
		SetConnectRetry(false).
		SetMaxReconnectInterval(30 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetCleanSession(true).
		SetOrderMatters(false). // handlers hand off to channels; ordering is kept per topic by the bridge
		SetConnectTimeout(15 * time.Second).
		SetWriteTimeout(10 * time.Second)
	if o.Endpoint.TLS {
		tc, err := brokerTLS()
		if err != nil {
			return nil, err
		}
		opts.SetTLSConfig(tc)
	}
	first := true
	opts.SetOnConnectHandler(func(mqtt.Client) {
		if o.OnState != nil && !first {
			o.OnState(StateConnected, nil)
		}
		first = false
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		if o.OnState != nil {
			o.OnState(StateLost, err)
		}
	})
	opts.SetReconnectingHandler(func(mqtt.Client, *mqtt.ClientOptions) {
		if o.OnState != nil {
			o.OnState(StateReconnecting, nil)
		}
	})

	c := mqtt.NewClient(opts)
	tok := c.Connect()
	select {
	case <-tok.Done():
	case <-ctx.Done():
		c.Disconnect(0)
		return nil, ctx.Err()
	}
	if err := tok.Error(); err != nil {
		return nil, fmt.Errorf("remote4: connecting to %s: %w", o.Endpoint.URL, err)
	}
	return &pahoBroker{client: c}, nil
}

func brokerTLS() (*tls.Config, error) {
	if os.Getenv("DRIVERFORGE_MQTT_INSECURE") != "" {
		return &tls.Config{InsecureSkipVerify: true}, nil //nolint:gosec // explicit local-testing override
	}
	pem := BrokerCA()
	if p := os.Getenv("DRIVERFORGE_MQTT_CA"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("remote4: reading DRIVERFORGE_MQTT_CA: %w", err)
		}
		pem = b
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("remote4: broker CA bundle contains no certificates")
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

func (b *pahoBroker) Publish(topic string, payload []byte) error {
	tok := b.client.Publish(topic, 1, false, payload)
	if !tok.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("remote4: publish to %s timed out", topic)
	}
	return tok.Error()
}

func (b *pahoBroker) Subscribe(topic string, handler func(string, []byte)) error {
	tok := b.client.Subscribe(topic, 1, func(_ mqtt.Client, m mqtt.Message) {
		handler(m.Topic(), m.Payload())
	})
	if !tok.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("remote4: subscribe to %s timed out", topic)
	}
	return tok.Error()
}

func (b *pahoBroker) Close() {
	b.client.Disconnect(250)
}

// MemoryBroker is an in-process Broker for tests: publishes are delivered
// synchronously to matching subscribers and recorded.
type MemoryBroker struct {
	mu        sync.Mutex
	subs      map[string][]func(string, []byte)
	published []PublishedMessage
}

// PublishedMessage is one recorded publish.
type PublishedMessage struct {
	Topic   string
	Payload []byte
}

// NewMemoryBroker returns an empty in-memory broker.
func NewMemoryBroker() *MemoryBroker {
	return &MemoryBroker{subs: map[string][]func(string, []byte){}}
}

func (m *MemoryBroker) Publish(topic string, payload []byte) error {
	m.mu.Lock()
	m.published = append(m.published, PublishedMessage{Topic: topic, Payload: append([]byte(nil), payload...)})
	handlers := append([]func(string, []byte){}, m.subs[topic]...)
	m.mu.Unlock()
	for _, h := range handlers {
		h(topic, payload)
	}
	return nil
}

// Published returns a snapshot of everything published so far. Publishes come
// from the bridge's goroutine, so reading the live slice would race.
func (m *MemoryBroker) Published() []PublishedMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]PublishedMessage(nil), m.published...)
}

func (m *MemoryBroker) Subscribe(topic string, handler func(string, []byte)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs[topic] = append(m.subs[topic], handler)
	return nil
}

func (m *MemoryBroker) Close() {}

// Deliver injects a message as if a remote client had published it.
func (m *MemoryBroker) Deliver(topic string, payload []byte) {
	m.mu.Lock()
	handlers := append([]func(string, []byte){}, m.subs[topic]...)
	m.mu.Unlock()
	for _, h := range handlers {
		h(topic, payload)
	}
}

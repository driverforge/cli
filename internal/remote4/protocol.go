package remote4

import (
	"encoding/json"
	"fmt"
)

// ProtocolVersion is the "v" every message carries. Bump together with the
// Lua side (stub.lua, runtime.lua).
const ProtocolVersion = 1

// Topic names under the session root, as the protocol doc lists them.
const (
	TopicCallback         = "callback"
	TopicCallbackResponse = "callback_response"
	TopicC4Call           = "c4_call"
	TopicC4Response       = "c4_response"
	TopicControl          = "control"
	TopicStatus           = "status"
)

// Topic returns the full broker topic for one channel of a session.
func Topic(userID, sessionID, name string) string {
	return fmt.Sprintf("remote4/%s/%s/%s", userID, sessionID, name)
}

// Message is the superset envelope used on both the broker and the runtime's
// stdio. Only the fields a given message type uses are populated; values
// (args, results, ctx) are relayed opaquely, so they stay raw JSON here.
type Message struct {
	V    int    `json:"v"`
	Type string `json:"type,omitempty"` // stdio only: callback, c4_call, …, plus ready/print/reloaded/fatal

	ID int `json:"id,omitempty"`

	// callback
	Name string          `json:"name,omitempty"`
	Fn   *int            `json:"fn,omitempty"`
	Ctx  json.RawMessage `json:"ctx,omitempty"`

	// c4_call
	Ref    *int   `json:"ref,omitempty"`
	Method string `json:"method,omitempty"`

	Args    json.RawMessage `json:"args,omitempty"`
	Argc    *int            `json:"argc,omitempty"`
	Results json.RawMessage `json:"results,omitempty"`

	// responses
	OK      *bool  `json:"ok,omitempty"`
	Handled *bool  `json:"handled,omitempty"`
	Error   string `json:"error,omitempty"`

	// control / status / runtime chatter
	Token    string          `json:"token,omitempty"`
	Level    string          `json:"level,omitempty"`
	Text     string          `json:"text,omitempty"`
	Entry    string          `json:"entry,omitempty"`
	Handlers []string        `json:"handlers,omitempty"`
	Stub     string          `json:"stub,omitempty"`
	Driver   string          `json:"driver,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

// Decode parses one envelope, tolerating unknown fields.
func Decode(b []byte) (Message, error) {
	var m Message
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	m.Raw = append(json.RawMessage(nil), b...)
	return m, nil
}

// Encode serialises m with the protocol version stamped.
func Encode(m Message) []byte {
	m.V = ProtocolVersion
	b, err := json.Marshal(m)
	if err != nil {
		panic("remote4: encoding message: " + err.Error()) // only Raw fields could fail, and they are valid JSON
	}
	return b
}

// Summary is a one-line rendering of a callback or C4 call for the terminal.
func (m Message) Summary() string {
	name := m.Name
	if m.Method != "" {
		name = "C4:" + m.Method
		if m.Ref != nil {
			name = fmt.Sprintf("#%d:%s", *m.Ref, m.Method)
		}
	}
	if m.Name == "__fn" && m.Fn != nil {
		name = fmt.Sprintf("callback #%d", *m.Fn)
	}
	args := string(m.Args)
	if len(args) > 120 {
		args = args[:117] + "…"
	}
	if args == "" || args == "[]" {
		return name + "()"
	}
	return name + "(" + args[1:len(args)-1] + ")"
}

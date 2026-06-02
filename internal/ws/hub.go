// Package ws implements the WebSocket endpoint that streams real-time data
// (container stats and logs) to authenticated frontend clients.
//
// A single connection is multiplexed: the client sends "subscribe" messages
// naming a channel + target, and the server pushes tagged frames back. This
// lets one dashboard watch many containers over one socket.
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// errUnknownChannel is reported when a client subscribes to an unknown channel.
var errUnknownChannel = errors.New("unknown channel (expected 'stats' or 'logs')")

// Streamer is the subset of the docker manager the hub needs.
type Streamer interface {
	StreamStats(ctx context.Context, hostID int64, id string, emit func(docker.StatsSample)) error
	StreamLogs(ctx context.Context, hostID int64, id string, follow bool, tail string, emit func(docker.LogLine)) error
}

// Hub serves WebSocket connections backed by a Streamer.
type Hub struct {
	docker Streamer
}

// NewHub creates a hub.
func NewHub(d Streamer) *Hub { return &Hub{docker: d} }

// clientMsg is an inbound control message from the browser.
type clientMsg struct {
	Type        string `json:"type"`    // "subscribe" | "unsubscribe" | "ping"
	SubID       string `json:"subId"`   // client-chosen subscription id
	Channel     string `json:"channel"` // "stats" | "logs"
	HostID      int64  `json:"hostId"`
	ContainerID string `json:"containerId"`
	Tail        string `json:"tail"`
}

// serverMsg is an outbound frame to the browser.
type serverMsg struct {
	Type    string `json:"type"` // "stats" | "log" | "error" | "pong" | "end"
	SubID   string `json:"subId"`
	Data    any    `json:"data,omitempty"`
	Message string `json:"message,omitempty"`
}

// Serve handles one accepted WebSocket connection until it closes.
func (h *Hub) Serve(ctx context.Context, conn *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	c := &connState{
		conn:    conn,
		docker:  h.docker,
		subs:    make(map[string]context.CancelFunc),
		writeMu: &sync.Mutex{},
	}
	defer c.closeAll()

	for {
		var msg clientMsg
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			return // client disconnected or context done
		}
		switch msg.Type {
		case "subscribe":
			c.subscribe(ctx, msg)
		case "unsubscribe":
			c.unsubscribe(msg.SubID)
		case "ping":
			c.write(ctx, serverMsg{Type: "pong", SubID: msg.SubID})
		}
	}
}

// connState tracks one connection's active subscriptions and serialises writes
// (a websocket connection allows only one concurrent writer).
type connState struct {
	conn    *websocket.Conn
	docker  Streamer
	writeMu *sync.Mutex

	mu   sync.Mutex
	subs map[string]context.CancelFunc
}

func (c *connState) subscribe(parent context.Context, msg clientMsg) {
	if msg.SubID == "" || msg.ContainerID == "" {
		c.write(parent, serverMsg{Type: "error", SubID: msg.SubID, Message: "subId and containerId required"})
		return
	}
	c.unsubscribe(msg.SubID) // replace any existing sub with the same id

	ctx, cancel := context.WithCancel(parent)
	c.mu.Lock()
	c.subs[msg.SubID] = cancel
	c.mu.Unlock()

	go func() {
		defer c.finish(msg.SubID)
		var err error
		switch msg.Channel {
		case "stats":
			err = c.docker.StreamStats(ctx, msg.HostID, msg.ContainerID, func(s docker.StatsSample) {
				c.write(ctx, serverMsg{Type: "stats", SubID: msg.SubID, Data: s})
			})
		case "logs":
			err = c.docker.StreamLogs(ctx, msg.HostID, msg.ContainerID, true, msg.Tail, func(l docker.LogLine) {
				c.write(ctx, serverMsg{Type: "log", SubID: msg.SubID, Data: l})
			})
		default:
			err = errUnknownChannel
		}
		if err != nil && ctx.Err() == nil {
			c.write(ctx, serverMsg{Type: "error", SubID: msg.SubID, Message: err.Error()})
		}
	}()
}

func (c *connState) unsubscribe(subID string) {
	c.mu.Lock()
	if cancel, ok := c.subs[subID]; ok {
		cancel()
		delete(c.subs, subID)
	}
	c.mu.Unlock()
}

// finish removes a subscription that ended on its own and notifies the client.
func (c *connState) finish(subID string) {
	c.mu.Lock()
	_, existed := c.subs[subID]
	delete(c.subs, subID)
	c.mu.Unlock()
	if existed {
		c.write(context.Background(), serverMsg{Type: "end", SubID: subID})
	}
}

func (c *connState) closeAll() {
	c.mu.Lock()
	for _, cancel := range c.subs {
		cancel()
	}
	c.subs = make(map[string]context.CancelFunc)
	c.mu.Unlock()
}

// write serialises a frame to the connection with a short timeout so a slow or
// dead client can't wedge a streaming goroutine forever.
func (c *connState) write(ctx context.Context, msg serverMsg) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	_ = c.conn.Write(wctx, websocket.MessageText, data)
}

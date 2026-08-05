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

// Serve handles one accepted WebSocket connection until it closes. allow gates
// each subscription by its channel AND the host it targets: it is called with
// both and must return true for the stream to start (the caller maps the channel
// to an RBAC section and checks the user's live permissions for that host). The
// host matters because a subscribe frame names one — without it, a user scoped
// to one host could stream a container's logs from another. A nil allow permits all
// channels (used in tests).
func (h *Hub) Serve(ctx context.Context, conn *websocket.Conn, allow func(channel string, hostID int64) bool) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	c := &connState{
		conn:    conn,
		docker:  h.docker,
		subs:    make(map[string]subscription),
		writeMu: &sync.Mutex{},
		allow:   allow,
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
	allow   func(channel string, hostID int64) bool

	mu   sync.Mutex
	subs map[string]subscription
	// gen numbers subscriptions so a stream that ends can tell whether the entry
	// under its id is still its own.
	gen uint64
}

// subscription is one live stream: how to stop it, and which generation it is.
type subscription struct {
	cancel context.CancelFunc
	gen    uint64
}

func (c *connState) subscribe(parent context.Context, msg clientMsg) {
	if msg.SubID == "" || msg.ContainerID == "" {
		c.write(parent, serverMsg{Type: "error", SubID: msg.SubID, Message: "subId and containerId required"})
		return
	}
	// Replace any existing sub with the same id FIRST, so a re-subscribe that is
	// then denied doesn't leave the previous stream running under that id.
	c.unsubscribe(msg.SubID)
	// RBAC gate: a user may only stream channels whose section they can access,
	// and only on hosts their grant reaches. A non-positive hostId is the local
	// daemon, the same convention the docker manager uses — normalised so the gate
	// and the streamer agree on which host is being named.
	hostID := msg.HostID
	if hostID < 0 {
		hostID = 0
	}
	if c.allow != nil && !c.allow(msg.Channel, hostID) {
		c.write(parent, serverMsg{Type: "error", SubID: msg.SubID, Message: "access to this section is not permitted"})
		return
	}

	ctx, cancel := context.WithCancel(parent)
	c.mu.Lock()
	c.gen++
	gen := c.gen
	c.subs[msg.SubID] = subscription{cancel: cancel, gen: gen}
	c.mu.Unlock()

	go func() {
		defer c.finish(msg.SubID, gen)
		var err error
		// The STREAM runs under ctx (cancelling it is how a subscription stops),
		// but the WRITES run under parent — the connection's own context.
		//
		// coder/websocket registers a context.AfterFunc on the context given to
		// Write, and that function closes the whole connection. Writing under the
		// subscription's context therefore meant: unsubscribe while a frame is in
		// flight and every other subscription on that socket dies with it. Leaving a
		// container page mid-stream is exactly that. A write must only be abortable
		// by the connection going away, which is what parent means.
		switch msg.Channel {
		case "stats":
			err = c.docker.StreamStats(ctx, msg.HostID, msg.ContainerID, func(s docker.StatsSample) {
				c.writeSub(parent, msg.SubID, gen, serverMsg{Type: "stats", SubID: msg.SubID, Data: s})
			})
		case "logs":
			err = c.docker.StreamLogs(ctx, msg.HostID, msg.ContainerID, true, msg.Tail, func(l docker.LogLine) {
				c.writeSub(parent, msg.SubID, gen, serverMsg{Type: "log", SubID: msg.SubID, Data: l})
			})
		default:
			err = errUnknownChannel
		}
		if err != nil && ctx.Err() == nil {
			c.writeSub(parent, msg.SubID, gen, serverMsg{Type: "error", SubID: msg.SubID, Message: err.Error()})
		}
	}()
}

func (c *connState) unsubscribe(subID string) {
	c.mu.Lock()
	if sub, ok := c.subs[subID]; ok {
		sub.cancel()
		delete(c.subs, subID)
	}
	c.mu.Unlock()
}

// finish removes a subscription that ended on its own and notifies the client —
// but only if the entry under that id is still the one it registered.
//
// The frontend reuses deterministic ids ("stats:<container>"), so leaving a
// container page and coming straight back — or React StrictMode's double mount in
// dev — replaces a subscription while the old stream is still winding down. Its
// deferred finish would then delete the NEW entry: the new stream keeps running
// with nothing able to cancel it (until the socket closes) and the client is told
// its live subscription ended. Comparing generations is what makes "mine" mean
// mine.
func (c *connState) finish(subID string, gen uint64) {
	c.mu.Lock()
	sub, existed := c.subs[subID]
	mine := existed && sub.gen == gen
	if mine {
		delete(c.subs, subID)
	}
	c.mu.Unlock()
	if mine {
		c.write(context.Background(), serverMsg{Type: "end", SubID: subID})
	}
}

func (c *connState) closeAll() {
	c.mu.Lock()
	for _, sub := range c.subs {
		sub.cancel()
	}
	c.subs = make(map[string]subscription)
	c.mu.Unlock()
}

// write serialises a frame to the connection with a short timeout so a slow or
// dead client can't wedge a streaming goroutine forever.
// writeSub writes a frame on behalf of one subscription, and only while that
// subscription is still the current one under its id.
//
// A cancelled stream keeps emitting for a moment while it winds down. Those tail
// frames used to be impossible to observe because cancelling took the whole
// connection down with it; now that writes survive a cancellation, they would be
// delivered — and subscription ids are deterministic ("stats:<container>"), so a
// re-subscribe (leave a container page and come straight back, or React's
// StrictMode double mount) would hand the OLD stream's tail to the NEW handler:
// duplicated log lines, or a stats sample from before the reset.
//
// The generation check is the same one finish() uses, for the same reason: "mine"
// has to mean mine. A frame that passes the check and is then replaced can still
// go out — that race is one frame wide and the client drops unknown ids — but the
// re-subscribe case, where the id is NOT unknown, is closed.
func (c *connState) writeSub(ctx context.Context, subID string, gen uint64, msg serverMsg) {
	c.mu.Lock()
	sub, ok := c.subs[subID]
	c.mu.Unlock()
	if !ok || sub.gen != gen {
		return
	}
	c.write(ctx, msg)
}

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

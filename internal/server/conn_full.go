package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/testengine"
)

// fullConn is a Full agent on the WebSocket control channel.
type fullConn struct {
	id      string
	ws      *websocket.Conn
	wmu     sync.Mutex
	seq     atomic.Uint64
	pmu     sync.Mutex
	pending map[string]chan proto.Envelope
	closed  chan struct{}
	once    sync.Once
}

func newFullConn(id string, ws *websocket.Conn) *fullConn {
	return &fullConn{id: id, ws: ws, pending: map[string]chan proto.Envelope{}, closed: make(chan struct{})}
}

func (f *fullConn) ID() string { return f.id }
func (f *fullConn) Lite() bool { return false }

func (f *fullConn) Close() {
	f.once.Do(func() {
		close(f.closed)
		_ = f.ws.Close(websocket.StatusNormalClosure, "replaced")
	})
}

func (f *fullConn) send(ctx context.Context, env proto.Envelope) error {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(wctx, f.ws, env)
}

// Request sends a message and waits for the reply with the same id.
func (f *fullConn) Request(ctx context.Context, typ string, data any) (json.RawMessage, error) {
	id := strconv.FormatUint(f.seq.Add(1), 10)
	env, err := proto.NewEnvelope(typ, id, data)
	if err != nil {
		return nil, err
	}
	ch := make(chan proto.Envelope, 1)
	f.pmu.Lock()
	f.pending[id] = ch
	f.pmu.Unlock()
	defer func() {
		f.pmu.Lock()
		delete(f.pending, id)
		f.pmu.Unlock()
	}()
	if err := f.send(ctx, env); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.Type == proto.MsgError {
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(r.Data, &e)
			return nil, fmt.Errorf("agent %s: %s", f.id, e.Error)
		}
		return r.Data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.closed:
		return nil, ErrOffline
	}
}

func (f *fullConn) deliver(env proto.Envelope) bool {
	if env.ID == "" {
		return false
	}
	f.pmu.Lock()
	ch := f.pending[env.ID]
	f.pmu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- env:
	default:
	}
	return true
}

func (f *fullConn) Grant(ctx context.Context, runID uint32, token testengine.Token) error {
	_, err := f.Request(ctx, proto.MsgGrant, proto.GrantMsg{RunID: runID, Token: token[:], TTLS: proto.TokenTTL})
	return err
}

func (f *fullConn) Test(ctx context.Context, spec TestSpec) (agent.TestResult, error) {
	var r agent.TestResult
	raw, err := f.Request(ctx, proto.MsgTest, spec)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	if r.Ping == nil && r.LSTP == nil {
		return r, errors.New("empty test result")
	}
	return r, nil
}

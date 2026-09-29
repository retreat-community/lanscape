package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/retreat-community/lanscape/internal/proto"
)

// ServeLocal answers local tools (LuCI, "lanscape-agent status") on a Unix socket. One request
// line per connection: "status", "run reachability|full" or "last"; the reply is one JSON
// document. (No HTTP server: it would add a quarter of a megabyte to router builds.)
func (a *Agent) ServeLocal(ctx context.Context, path string) error {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600) //nolint:gosec // root-only socket
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err() // the socket was closed on shutdown
			}
			return err
		}
		go a.serveLocalConn(ctx, c)
	}
}

func (a *Agent) serveLocalConn(ctx context.Context, c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	line, err := bufio.NewReader(io.LimitReader(c, 256)).ReadString('\n')
	if err != nil && line == "" {
		return
	}
	f := strings.Fields(line)
	if len(f) == 0 {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var v any
	switch f[0] {
	case "status":
		v = a.Status()
	case "run":
		kind := "reachability"
		if len(f) > 1 && f[1] == "full" {
			kind = "full"
		}
		v, err = a.Request(rctx, proto.MsgRunRequest, proto.RunRequestMsg{Kind: kind})
	case "last":
		v, err = a.Request(rctx, proto.MsgRunSummary, nil)
	default:
		err = errors.New("unknown command " + f[0])
	}
	if err != nil {
		v = map[string]string{"error": err.Error()}
	}
	_ = json.NewEncoder(c).Encode(v)
}

// LocalCall sends one command to a running agent and returns its JSON reply.
func LocalCall(path, cmd string) ([]byte, error) {
	c, err := net.DialTimeout("unix", path, 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := io.WriteString(c, cmd+"\n"); err != nil {
		return nil, err
	}
	return io.ReadAll(c)
}

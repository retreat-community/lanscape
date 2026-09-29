package testengine

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/retreat-community/lanscape/internal/netio"
)

// iperf3 control states (iperf_api.h).
const (
	ipTestStart       = 1
	ipTestRunning     = 2
	ipTestEnd         = 4
	ipParamExchange   = 9
	ipCreateStreams   = 10
	ipServerTerminate = 11
	ipExchangeResults = 13
	ipDisplayResults  = 14
	ipIperfDone       = 16
	ipAccessDenied    = -1
	ipServerError     = -2
	ipCookieSize      = 37
)

// Iperf3Options describes a test against a device running "iperf3 -s" (§14): one without an agent.
type Iperf3Options struct {
	Host     string        `json:"host"`
	Port     int           `json:"port,omitempty"` // default 5201
	Duration time.Duration `json:"duration"`
	Streams  int           `json:"streams,omitempty"`
	Reverse  bool          `json:"reverse,omitempty"` // the server sends
	Dev      string        `json:"dev,omitempty"`     // bind to this interface
}

// Iperf3Result is the measured TCP throughput.
type Iperf3Result struct {
	BPS         uint64  `json:"bps"`
	Bytes       uint64  `json:"bytes"`
	Seconds     float64 `json:"seconds"`
	Streams     int     `json:"streams"`
	Reverse     bool    `json:"reverse"`
	Retransmits int64   `json:"retransmits"` // reported by the sender, -1 when unknown
}

type ipStreamResult struct {
	ID          int     `json:"id"`
	Bytes       uint64  `json:"bytes"`
	Retransmits float64 `json:"retransmits"`
	Jitter      float64 `json:"jitter"`
	Errors      int     `json:"errors"`
	Packets     int     `json:"packets"`
	StartTime   float64 `json:"start_time"`
	EndTime     float64 `json:"end_time"`
}

type ipResults struct {
	CPUTotal      float64          `json:"cpu_util_total"`
	CPUUser       float64          `json:"cpu_util_user"`
	CPUSystem     float64          `json:"cpu_util_system"`
	HasRetransmit float64          `json:"sender_has_retransmits"` // iperf3 writes -1 as an unsigned 64-bit number
	Streams       []ipStreamResult `json:"streams"`
}

// Iperf3 runs a TCP test against an iperf3 server with its own control protocol, so the agent
// needs no iperf3 binary.
func Iperf3(ctx context.Context, o Iperf3Options) (Iperf3Result, error) {
	if o.Port == 0 {
		o.Port = 5201
	}
	if o.Duration <= 0 {
		o.Duration = 5 * time.Second
	}
	o.Duration = min(max(o.Duration.Round(time.Second), time.Second), time.Minute)
	o.Streams = min(max(o.Streams, 1), 16)
	res := Iperf3Result{Streams: o.Streams, Reverse: o.Reverse, Retransmits: -1}
	addr := net.JoinHostPort(o.Host, strconv.Itoa(o.Port))
	d := net.Dialer{Timeout: 5 * time.Second, Control: netio.BindControl(o.Dev, false)}
	ctl, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return res, fmt.Errorf("iperf3: %w", err)
	}
	defer ctl.Close()
	stop := context.AfterFunc(ctx, func() { _ = ctl.SetDeadline(time.Now()) })
	defer stop()
	_ = ctl.SetDeadline(time.Now().Add(o.Duration + 30*time.Second))

	cookie := make([]byte, ipCookieSize)
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	_, _ = rand.Read(cookie)
	for i := range cookie[:ipCookieSize-1] {
		cookie[i] = alphabet[int(cookie[i])%len(alphabet)]
	}
	cookie[ipCookieSize-1] = 0
	if _, err := ctl.Write(cookie); err != nil {
		return res, fmt.Errorf("iperf3: %w", err)
	}
	if err := ipExpect(ctl, ipParamExchange); err != nil {
		return res, err
	}
	params := map[string]any{"tcp": true, "omit": 0, "time": int(o.Duration.Seconds()), "num": 0, "blockcount": 0,
		"parallel": o.Streams, "len": 128 * 1024, "pacing_timer": 1000, "client_version": "3.16"}
	if o.Reverse {
		params["reverse"] = true
	}
	if err := ipWriteJSON(ctl, params); err != nil {
		return res, err
	}
	if err := ipExpect(ctl, ipCreateStreams); err != nil {
		return res, err
	}
	conns := make([]net.Conn, 0, o.Streams)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for range o.Streams {
		c, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return res, fmt.Errorf("iperf3: stream: %w", err)
		}
		conns = append(conns, c)
		if _, err := c.Write(cookie); err != nil {
			return res, fmt.Errorf("iperf3: stream: %w", err)
		}
	}
	if err := ipExpect(ctl, ipTestStart); err != nil {
		return res, err
	}
	if err := ipExpect(ctl, ipTestRunning); err != nil {
		return res, err
	}

	// transfer until the duration is over, then tell the server
	counts := make([]atomic.Uint64, len(conns))
	start := time.Now()
	end := start.Add(o.Duration)
	var wg sync.WaitGroup
	for i, c := range conns {
		wg.Add(1)
		go func(i int, c net.Conn) {
			defer wg.Done()
			_ = c.SetDeadline(end)
			buf := make([]byte, 128*1024)
			for {
				var n int
				var err error
				if o.Reverse {
					n, err = c.Read(buf)
				} else {
					n, err = c.Write(buf)
				}
				counts[i].Add(uint64(n))
				if err != nil {
					return
				}
			}
		}(i, c)
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	if _, err := ctl.Write([]byte{ipTestEnd}); err != nil {
		return res, fmt.Errorf("iperf3: %w", err)
	}
	if err := ipExpect(ctl, ipExchangeResults); err != nil {
		return res, err
	}
	mine := ipResults{Streams: make([]ipStreamResult, len(conns))}
	for i := range conns {
		// iperf3 numbers streams 1, 3, 4, 5 …
		id := 1
		if i > 0 {
			id = i + 2
		}
		mine.Streams[i] = ipStreamResult{ID: id, Bytes: counts[i].Load(), Retransmits: -1, EndTime: elapsed}
	}
	if err := ipWriteJSON(ctl, mine); err != nil {
		return res, err
	}
	var theirs ipResults
	if err := ipReadJSON(ctl, &theirs); err != nil {
		return res, err
	}
	if err := ipExpect(ctl, ipDisplayResults); err != nil {
		return res, err
	}
	_, _ = ctl.Write([]byte{ipIperfDone})

	// the receiver's count is the throughput; retransmits come from the sender
	var sent, received uint64
	for i := range mine.Streams {
		sent += mine.Streams[i].Bytes
	}
	var retr int64
	for _, s := range theirs.Streams {
		received += s.Bytes
		if s.Retransmits > 0 && s.Retransmits < 1e15 {
			retr += int64(s.Retransmits)
		}
	}
	if o.Reverse {
		sent, received = received, sent
		if theirs.HasRetransmit == 1 {
			res.Retransmits = retr
		}
	}
	res.Bytes, res.Seconds = min(sent, received), elapsed
	if received == 0 {
		res.Bytes = sent
	}
	if elapsed > 0 {
		res.BPS = uint64(float64(res.Bytes) * 8 / elapsed)
	}
	return res, nil
}

// ipExpect reads control states until want, failing on errors and termination.
func ipExpect(c net.Conn, want int8) error {
	var b [1]byte
	for {
		if _, err := io.ReadFull(c, b[:]); err != nil {
			return fmt.Errorf("iperf3: waiting for state %d: %w", want, err)
		}
		st := int8(b[0])
		switch st {
		case want:
			return nil
		case ipAccessDenied:
			return errors.New("iperf3: the server is busy with another test")
		case ipServerError:
			return errors.New("iperf3: server error")
		case ipServerTerminate:
			return errors.New("iperf3: the server ended the test")
		}
	}
}

func ipWriteJSON(c net.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b))) //nolint:gosec // small JSON documents
	if _, err := c.Write(append(n[:], b...)); err != nil {
		return fmt.Errorf("iperf3: %w", err)
	}
	return nil
}

func ipReadJSON(c net.Conn, v any) error {
	var n [4]byte
	if _, err := io.ReadFull(c, n[:]); err != nil {
		return fmt.Errorf("iperf3: results: %w", err)
	}
	size := binary.BigEndian.Uint32(n[:])
	if size > 1<<20 {
		return errors.New("iperf3: results too large")
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(c, b); err != nil {
		return fmt.Errorf("iperf3: results: %w", err)
	}
	return json.Unmarshal(b, v)
}

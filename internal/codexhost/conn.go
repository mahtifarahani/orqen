// Package codexhost speaks the Codex App Server protocol (newline-delimited JSON-RPC) to a child process Orqen
// supervises. It never starts a process itself: it works over any io.ReadWriteCloser, so tests use an in-process peer.
package codexhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// maxLine bounds one protocol line.
const maxLine = 32 << 20

// ErrClosed means the connection ended (EOF or Close).
var ErrClosed = errors.New("codexhost: connection closed")

// RPCError is an error response from the peer.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("codex app-server error %d: %s", e.Code, e.Message)
}

// Event is a notification from the server.
type Event struct {
	Method string
	Params json.RawMessage
}

// ServerRequest is a request from the server that needs an answer.
type ServerRequest struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
	conn   *Conn
}

// Respond answers the request with a result.
func (r ServerRequest) Respond(result any) error {
	return r.conn.write(map[string]any{"id": r.ID, "result": result})
}

// RespondError answers the request with an error.
func (r ServerRequest) RespondError(code int, message string) error {
	return r.conn.write(map[string]any{"id": r.ID, "error": map[string]any{"code": code, "message": message}})
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

// queue is an unbounded FIFO so the reader goroutine never blocks on a slow consumer.
type queue[T any] struct {
	mu     sync.Mutex
	items  []T
	closed bool
	wake   chan struct{}
}

func newQueue[T any]() *queue[T] { return &queue[T]{wake: make(chan struct{}, 1)} }
func (q *queue[T]) push(v T) {
	q.mu.Lock()
	q.items = append(q.items, v)
	q.mu.Unlock()
	q.signal()
}
func (q *queue[T]) close() { q.mu.Lock(); q.closed = true; q.mu.Unlock(); q.signal() }
func (q *queue[T]) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// pop waits for an item. ok is false when the queue is closed and empty, or the context ended.
func (q *queue[T]) pop(ctx context.Context) (v T, ok bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			v = q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			return v, true
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return v, false
		}
		select {
		case <-q.wake:
		case <-ctx.Done():
			return v, false
		}
	}
}

// Conn is a JSON-RPC connection over lines of JSON.
type Conn struct {
	rwc    io.ReadWriteCloser
	wmu    sync.Mutex
	nextID atomic.Int64

	mu      sync.Mutex
	pending map[string]chan message
	closed  bool

	in     *queue[Inbound]
	done   chan struct{}
	inOnce sync.Once
	inCh   chan Inbound
}

// Inbound is a notification or a server request, delivered in the order the server sent them.
type Inbound struct {
	Event   *Event
	Request *ServerRequest
}

// NewConn starts the reader goroutine.
func NewConn(rwc io.ReadWriteCloser) *Conn {
	c := &Conn{rwc: rwc, pending: map[string]chan message{}, in: newQueue[Inbound](), done: make(chan struct{})}
	go c.readLoop()
	return c
}

func (c *Conn) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.isClosed() {
		return ErrClosed
	}
	_, err = c.rwc.Write(append(b, '\n'))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrClosed, err)
	}
	return nil
}

func (c *Conn) isClosed() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.closed }

func (c *Conn) readLoop() {
	r := bufio.NewReaderSize(c.rwc, 64<<10)
	for {
		line, err := readLine(r)
		if len(bytes.TrimSpace(line)) > 0 {
			var m message
			if json.Unmarshal(line, &m) == nil {
				c.dispatch(m)
			}
		}
		if err != nil {
			break
		}
	}
	c.shutdown()
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		out = append(out, chunk...)
		if len(out) > maxLine {
			return nil, io.ErrShortBuffer
		}
		if err != nil || !isPrefix {
			return out, err
		}
	}
}

func (c *Conn) dispatch(m message) {
	switch {
	case m.Method != "" && len(m.ID) > 0 && string(m.ID) != "null":
		c.in.push(Inbound{Request: &ServerRequest{ID: append(json.RawMessage(nil), m.ID...), Method: m.Method, Params: m.Params, conn: c}})
	case m.Method != "":
		c.in.push(Inbound{Event: &Event{Method: m.Method, Params: m.Params}})
	case len(m.ID) > 0:
		c.mu.Lock()
		ch := c.pending[string(m.ID)]
		delete(c.pending, string(m.ID))
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
}

func (c *Conn) shutdown() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	for k, ch := range c.pending {
		close(ch)
		delete(c.pending, k)
	}
	c.mu.Unlock()
	c.in.close()
	close(c.done)
	_ = c.rwc.Close()
}

// Close ends the connection.
func (c *Conn) Close() error { c.shutdown(); return nil }

// Done is closed when the connection ended.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Call sends a request and decodes its result. A nil result discards it.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)
	key := fmt.Sprint(id)
	ch := make(chan message, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.pending[key] = ch
	c.mu.Unlock()
	req := map[string]any{"id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := c.write(req); err != nil {
		c.forget(key)
		return err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if m.Error != nil {
			return m.Error
		}
		if result != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	case <-ctx.Done():
		c.forget(key)
		return ctx.Err()
	}
}

func (c *Conn) forget(key string) { c.mu.Lock(); delete(c.pending, key); c.mu.Unlock() }

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	n := map[string]any{"method": method}
	if params != nil {
		n["params"] = params
	}
	return c.write(n)
}

// Inbound delivers notifications and server requests in order. The channel closes when the connection ends.
func (c *Conn) Inbound() <-chan Inbound {
	c.inOnce.Do(func() {
		c.inCh = make(chan Inbound)
		go func() {
			defer close(c.inCh)
			for {
				v, ok := c.in.pop(context.Background())
				if !ok {
					return
				}
				c.inCh <- v
			}
		}()
	})
	return c.inCh
}

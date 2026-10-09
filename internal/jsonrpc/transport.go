package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
)

// Transport reads requests and writes responses over a byte stream.
type Transport struct {
	reader  *bufio.Reader
	writer  io.Writer
	writeMu sync.Mutex
}

// NewTransport wraps an io.Reader and io.Writer as a JSON-RPC transport.
// Each JSON message is expected to be a single line terminated by newline.
func NewTransport(r io.Reader, w io.Writer) *Transport {
	return &Transport{
		reader: bufio.NewReader(r),
		writer: w,
	}
}

// ReadRequest reads one JSON-RPC request (newline-delimited JSON).
// It also returns the raw JSON bytes so callers can inspect the original payload.
func (t *Transport) ReadRequest() (*Request, []byte, error) {
	line, err := t.reader.ReadBytes('\n')
	if err != nil {
		return nil, nil, err
	}

	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return nil, nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return &req, line, nil
}

// WriteResponse sends a JSON-RPC response (newline-delimited).
func (t *Transport) WriteResponse(resp *Response) error {
	return t.writeMessage(context.Background(), resp)
}

// WriteResponseContext checks cancellation at native write commitment, after
// acquiring the output lock. The owner must close interruptible I/O to abort a
// write already in progress; a complete write cannot be retracted.
func (t *Transport) WriteResponseContext(ctx context.Context, resp *Response) error {
	return t.writeMessage(ctx, resp)
}

// WriteNotification sends a JSON-RPC notification (newline-delimited).
func (t *Transport) WriteNotification(notif *Notification) error {
	return t.writeMessage(context.Background(), notif)
}

func (t *Transport) writeMessage(ctx context.Context, message any) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := ctx.Err(); err != nil {
		return err
	}
	n, err := t.writer.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

// TCPListener listens for TCP connections and serves each with the given server.
type TCPListener struct {
	listener net.Listener
	server   *Server
}

// NewTCPListener creates a TCP listener on the given address.
func NewTCPListener(addr string, server *Server) (*TCPListener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", addr, err)
	}
	return &TCPListener{listener: ln, server: server}, nil
}

// Addr returns the listener's network address.
func (tl *TCPListener) Addr() net.Addr {
	return tl.listener.Addr()
}

// Serve accepts connections in a loop. It blocks until the listener is closed.
func (tl *TCPListener) Serve() error {
	for {
		conn, err := tl.listener.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close() //nolint:errcheck
			transport := NewTransport(conn, conn)
			tl.server.ServeTransport(transport)
		}()
	}
}

// Close shuts down the TCP listener.
func (tl *TCPListener) Close() error {
	return tl.listener.Close()
}

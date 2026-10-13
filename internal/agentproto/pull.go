package agentproto

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mhsanaei/3x-ui/v3/internal/web/network"
)

const (
	// ConnectPath is where an agent in pull mode opens its connection, below the base path of the panel:
	// <master>/agent/connect/<node id>.
	ConnectPath = "agent/connect"
	// ConnectProtocol is the WebSocket subprotocol both ends name, so that a client of another kind is not upgraded.
	ConnectProtocol = "x-ui-agent.v1"

	connChunk     = 32 << 10
	connReadLimit = 1 << 20
	maxPinLen     = 2*32 + 31

	// The master pings an agent that has sent nothing for this long, and drops it when it does not answer in time.
	tunnelPingAfter   = 15 * time.Second
	tunnelPingTimeout = 10 * time.Second
	// tunnelStreams is how many requests an agent answers at once.
	tunnelStreams = 32
)

// NewClientConn is the HTTP/2 client of the master on conn, the connection an agent opened: requests go out on it
// as if the master had dialled the agent, without TLS, since it is the WebSocket that was authenticated.
func NewClientConn(ctx context.Context, conn net.Conn) (*http.ClientConn, error) {
	return newClientConn(ctx, conn, tunnelPingAfter, tunnelPingTimeout)
}

func newClientConn(ctx context.Context, conn net.Conn, pingAfter, pingTimeout time.Duration) (*http.ClientConn, error) {
	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	var dialled atomic.Bool
	transport := &http.Transport{
		Protocols: &protocols,
		HTTP2:     &http.HTTP2Config{SendPingTimeout: pingAfter, PingTimeout: pingTimeout},
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			if dialled.Swap(true) {
				return nil, errors.New("the connection of an agent is dialled once")
			}
			return conn, nil
		},
	}
	return transport.NewClientConn(ctx, "http", "agent.invalid:80")
}

// ServeConn answers the requests that arrive on conn as HTTP/2 without TLS, which is how an agent serves the master
// that it connected to, until the connection ends or ctx does. srv is spent by it: it holds the handler and the limits.
func ServeConn(ctx context.Context, conn net.Conn, srv *http.Server) {
	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	srv.Protocols = &protocols
	srv.HTTP2 = &http.HTTP2Config{MaxConcurrentStreams: tunnelStreams}
	watched := &watchedConn{Conn: conn, gone: make(chan struct{})}
	stop := context.AfterFunc(ctx, func() { _ = watched.Close() })
	defer stop()
	// Serve returns once the one connection has gone, since there is no other for its listener to hand out.
	network.ServeHTTP(srv, &singleConnListener{conn: watched}, "Agent connection")
}

// watchedConn tells when it was closed, whichever end did it.
type watchedConn struct {
	net.Conn
	once sync.Once
	gone chan struct{}
}

func (c *watchedConn) Close() error {
	c.once.Do(func() { close(c.gone) })
	return c.Conn.Close()
}

// singleConnListener hands out one connection, and holds the server there until it has gone.
type singleConnListener struct {
	conn *watchedConn
	used sync.Once
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	var conn net.Conn
	l.used.Do(func() { conn = l.conn })
	if conn != nil {
		return conn, nil
	}
	<-l.conn.gone
	return nil, http.ErrServerClosed
}

func (l *singleConnListener) Close() error   { return nil }
func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// ConnectURL is the address the agent of node dials: the master's, with ConnectPath and the node below it.
func ConnectURL(master string, node int) (string, error) {
	u, err := url.Parse(strings.TrimSpace(master))
	if err != nil {
		return "", fmt.Errorf("the address of the master is not a URL: %w", err)
	}
	switch {
	case u.Scheme != "https":
		return "", errors.New("the address of the master must be an https:// URL")
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "":
		return "", errors.New("the address of the master must be a plain URL, with no user, query or fragment")
	case node < 1:
		return "", errors.New("the node of the pairing bundle is missing")
	}
	if err := validateAddress(u.Hostname()); err != nil {
		return "", fmt.Errorf("the address of the master: %w", err)
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("the port %q of the master is out of range", port)
		}
	}
	u.Scheme = "wss"
	u.Path = strings.TrimRight(u.Path, "/") + "/" + ConnectPath + "/" + strconv.Itoa(node)
	u.RawPath = ""
	return u.String(), nil
}

// ParsePin reads a SHA-256 pin written as hex, with or without the colons a browser shows it with.
func ParsePin(pin string) ([]byte, error) {
	trimmed := strings.TrimSpace(pin)
	if len(trimmed) > maxPinLen {
		return nil, errors.New("the pin is not a SHA-256 hash")
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(trimmed, ":", ""))
	if err != nil || len(raw) != 32 {
		return nil, errors.New("the pin must be a SHA-256 hash written in hex")
	}
	return raw, nil
}

// Conn carries a byte stream over a WebSocket, which is how the master can speak to an agent over a connection
// that the agent opened. Every Write is one or more binary messages and Read joins them again.
type Conn struct {
	ws      *websocket.Conn
	onClose func()
	heard   atomic.Int64 // when the other end last sent a message or answered a ping, in Unix nanoseconds

	rmu    sync.Mutex // one reader, which holds the message it is partway through
	reader io.Reader

	wmu       sync.Mutex // a WebSocket takes one writer at a time
	closeOnce sync.Once
}

// NewConn wraps ws. onClose, when not nil, runs once, when the connection is closed from either end.
func NewConn(ws *websocket.Conn, onClose func()) *Conn {
	ws.SetReadLimit(connReadLimit)
	c := &Conn{ws: ws, onClose: onClose}
	c.hearOf()
	return c
}

func (c *Conn) hearOf() { c.heard.Store(time.Now().UnixNano()) }

func (c *Conn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for {
		if c.reader == nil {
			kind, r, err := c.ws.NextReader()
			if err != nil {
				return 0, err
			}
			c.hearOf()
			if kind != websocket.BinaryMessage {
				return 0, errors.New("the other end sent a text message on a byte stream")
			}
			c.reader = r
		}
		n, err := c.reader.Read(p)
		if errors.Is(err, io.EOF) {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *Conn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	written := 0
	for len(p) > 0 {
		chunk := p[:min(len(p), connChunk)]
		if err := c.ws.WriteMessage(websocket.BinaryMessage, chunk); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

// Close says goodbye when it can, which lets the other end tell a connection that was shut from one that was lost.
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		err = c.ws.Close()
		if c.onClose != nil {
			c.onClose()
		}
	})
	return err
}

// KeepAlive pings the other end every interval and closes a connection that heard nothing from it for idle, so a peer
// that vanished does not hold it for ever. It keeps its own clock, since whatever serves the connection sets deadlines too.
func (c *Conn) KeepAlive(interval, idle time.Duration) {
	c.hearOf()
	c.ws.SetPongHandler(func(string) error {
		c.hearOf()
		return nil
	})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			if time.Since(time.Unix(0, c.heard.Load())) > idle {
				_ = c.Close()
				return
			}
			if err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(interval)); err != nil {
				return
			}
		}
	}()
}

func (c *Conn) LocalAddr() net.Addr                { return c.ws.LocalAddr() }
func (c *Conn) RemoteAddr() net.Addr               { return c.ws.RemoteAddr() }
func (c *Conn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }

func (c *Conn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}

var _ net.Conn = (*Conn)(nil)

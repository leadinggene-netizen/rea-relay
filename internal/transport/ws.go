// Package transport implements the v1 real transport for delivering resolved
// orders to a terminal agent (spec §5, §13):
//
//   - a hand-rolled RFC 6455 WebSocket endpoint (this file + endpoint.go,
//     server.go) that is the primary push channel (TR-1..TR-6), and
//   - an HTTP long-poll fallback (longpoll.go) carrying the identical typed
//     envelope and the same auth + at-least-once + idempotency guarantees
//     (TR-7).
//
// Both slot behind the existing delivery.Transport interface so they reuse the
// delivery.Channel's fan-out, pause/resume and per-connection ordering
// (FR-8..11) rather than re-implementing it (TR-8).
//
// The WebSocket server is built on the Go standard library only: net/http
// Hijack for the HTTP upgrade, crypto/sha1 + encoding/base64 for the
// Sec-WebSocket-Accept key, and a from-scratch frame reader/writer with the
// RFC 6455 masking rules, control frames (ping/pong/close) and fragmentation.
package transport

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// wsGUID is the RFC 6455 magic value concatenated with the client's
// Sec-WebSocket-Key before hashing to form Sec-WebSocket-Accept.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WebSocket opcodes (RFC 6455 §5.2).
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// errClosed is returned by ReadMessage once the peer has closed the socket.
var errClosed = errors.New("transport: websocket closed")

// acceptKey computes the Sec-WebSocket-Accept value for a client key
// (base64(sha1(key + GUID))), per RFC 6455 §4.2.2.
func acceptKey(clientKey string) string {
	h := sha1.New()
	_, _ = io.WriteString(h, clientKey+wsGUID)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// isUpgrade reports whether r is a well-formed RFC 6455 upgrade request.
func isUpgrade(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if !tokenHeaderContains(r.Header.Get("Connection"), "upgrade") {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	if strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key")) == "" {
		return false
	}
	return strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")) == "13"
}

// tokenHeaderContains reports whether a comma-separated header value contains
// the given token (case-insensitive), e.g. "keep-alive, Upgrade".
func tokenHeaderContains(header, token string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

// wsConn is one hijacked WebSocket connection. Writes are serialized by wmu so
// several goroutines (deliver, resume replay, pong) can share one socket.
type wsConn struct {
	raw net.Conn
	br  *bufio.Reader

	wmu    sync.Mutex
	closed bool
}

// upgrade performs the RFC 6455 handshake over an http.ResponseWriter that
// supports Hijack, returning a ready wsConn. It writes the 101 response itself.
func upgrade(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !isUpgrade(r) {
		return nil, errors.New("transport: not a websocket upgrade request")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("transport: ResponseWriter does not support Hijack")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("transport: hijack: %w", err)
	}
	accept := acceptKey(strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key")))
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := brw.WriteString(resp); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := brw.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &wsConn{raw: conn, br: brw.Reader}, nil
}

// ReadMessage reads one complete application message (reassembling fragments),
// transparently answering ping with pong and handling close. It returns the
// opcode of the first frame (opText or opBinary) and the full payload.
func (c *wsConn) ReadMessage() (int, []byte, error) {
	var (
		msgOp   int
		payload []byte
	)
	for {
		fin, op, data, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case opPing:
			if err := c.writeFrame(opPong, data); err != nil {
				return 0, nil, err
			}
			continue
		case opPong:
			// Keepalive acknowledgement; surfaced to the caller as a zero-length
			// control read so the endpoint can refresh its heartbeat clock.
			return opPong, nil, nil
		case opClose:
			_ = c.writeFrame(opClose, data)
			c.close()
			return 0, nil, errClosed
		case opContinuation:
			payload = append(payload, data...)
		case opText, opBinary:
			msgOp = op
			payload = append(payload, data...)
		default:
			return 0, nil, fmt.Errorf("transport: unknown opcode 0x%x", op)
		}
		if fin {
			return msgOp, payload, nil
		}
	}
}

// readFrame reads a single RFC 6455 frame. Client-to-server frames MUST be
// masked (§5.1); an unmasked data frame is a protocol error.
func (c *wsConn) readFrame() (fin bool, opcode int, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(c.br, hdr[:]); err != nil {
		return false, 0, nil, err
	}
	fin = hdr[0]&0x80 != 0
	opcode = int(hdr[0] & 0x0f)
	masked := hdr[1]&0x80 != 0
	length := int64(hdr[1] & 0x7f)

	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint64(ext[:]))
	}
	if length < 0 || length > 1<<22 { // 4 MiB cap guards against hostile frames.
		return false, 0, nil, errors.New("transport: frame too large")
	}
	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, maskKey[:]); err != nil {
			return false, 0, nil, err
		}
	} else if opcode != opClose {
		return false, 0, nil, errors.New("transport: client frame not masked")
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i&3]
		}
	}
	return fin, opcode, payload, nil
}

// writeFrame writes a single unmasked server-to-client frame (FIN set). Server
// frames MUST NOT be masked (RFC 6455 §5.1).
func (c *wsConn) writeFrame(opcode int, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return errClosed
	}
	var hdr []byte
	b0 := byte(0x80 | opcode) // FIN + opcode
	n := len(payload)
	switch {
	case n <= 125:
		hdr = []byte{b0, byte(n)}
	case n <= 0xffff:
		hdr = []byte{b0, 126, 0, 0}
		binary.BigEndian.PutUint16(hdr[2:], uint16(n))
	default:
		hdr = make([]byte, 10)
		hdr[0], hdr[1] = b0, 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
	}
	if _, err := c.raw.Write(hdr); err != nil {
		return err
	}
	if n > 0 {
		if _, err := c.raw.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// WriteJSON marshals v and sends it as a single text message.
func (c *wsConn) WriteJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.writeFrame(opText, data)
}

// close sends a close frame (best effort) and tears down the socket.
func (c *wsConn) close() {
	c.wmu.Lock()
	if c.closed {
		c.wmu.Unlock()
		return
	}
	c.closed = true
	c.wmu.Unlock()
	_ = c.raw.Close()
}

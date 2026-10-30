package transport

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/connection"
	"github.com/leadinggene-netizen/rea-relay/internal/relay"
	"github.com/leadinggene-netizen/rea-relay/internal/risk"
)

// Shared test fixtures mirroring the relay test harness.
const (
	tKey    = "transport-conn-key"
	tSymbol = "GOLD"
	tBroker = "XAUUSD.m"
)

var tGoldSpec = connection.BrokerSymbolSpec{
	BrokerSymbol: tBroker, PipSize: 0.1, PipValuePerLot: 1.0,
	LotMin: 0.01, LotMax: 100, LotStep: 0.01,
}

// tclock is an injectable, monotonic-free clock for deterministic time control.
type tclock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *tclock { return &tclock{t: time.Unix(1_700_000_000, 0).UTC()} }
func (c *tclock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *tclock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// harness wires a relay + transport server over an httptest server.
type harness struct {
	srv   *httptest.Server
	ts    *Server
	relay *relay.Relay
	store *connection.Store
	conn  *connection.Connection
	audit *relay.AuditLog
	clk   *tclock
}

type harnessOpts struct {
	requireSig bool
	secret     string
	maxConns   int
}

func newHarness(t *testing.T, o harnessOpts) *harness {
	t.Helper()
	if o.maxConns == 0 {
		o.maxConns = 3
	}
	store := connection.NewStore()
	store.AddSubscription(&connection.Subscription{ID: "sub-1", Tier: "pro", MaxConnections: o.maxConns})
	conn := &connection.Connection{
		ID: "conn-1", Key: tKey, SubscriptionID: "sub-1",
		RequireSignature: o.requireSig, Secret: o.secret,
		SymbolTable: map[string]connection.BrokerSymbolSpec{tSymbol: tGoldSpec},
		Accounts:    []connection.Account{{ID: "acct-1", Mode: "demo", Equity: 10000}},
	}
	store.AddConnection(conn)
	quotes := risk.MapQuoteSource{tBroker: {Bid: 2400.0, Ask: 2400.2, Spread: 0.2}}
	audit := relay.NewAuditLog()
	r := relay.New(relay.DefaultConfig(), store, quotes, audit)

	clk := newClock()
	ts := NewServer(r, store, audit, Config{Endpoint: EndpointConfig{DeliverTimeout: 2 * time.Second}})
	ts.SetClock(clk.now)

	mux := http.NewServeMux()
	ts.Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &harness{srv: srv, ts: ts, relay: r, store: store, conn: conn, audit: audit, clk: clk}
}

// openLongBody is a standard market-long webhook body for the test symbol.
func openLongBody(idem string) []byte {
	m := map[string]any{
		"schema_version": 1,
		"connection_key": tKey,
		"action":         "open_long",
		"symbol":         tSymbol,
		"sizing":         map[string]any{"mode": "fixed_lots", "value": 0.10},
	}
	if idem != "" {
		m["idempotency_key"] = idem
	}
	b, _ := json.Marshal(m)
	return b
}

// --- hand-rolled stdlib WebSocket test client -------------------------------

type wsClient struct {
	conn net.Conn
	br   *bufio.Reader
}

// dialWS performs the RFC 6455 client handshake against the harness server and
// returns the client plus the HTTP response (status 101 on success).
func dialWS(t *testing.T, base, path string, headers map[string]string) (*wsClient, *http.Response) {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	var kb [16]byte
	_, _ = rand.Read(kb[:])
	key := base64.StdEncoding.EncodeToString(kb[:])
	req := "GET " + path + " HTTP/1.1\r\nHost: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n"
	for k, v := range headers {
		req += k + ": " + v + "\r\n"
	}
	req += "\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	br := bufio.NewReader(conn)
	dummy, _ := http.NewRequest("GET", path, nil)
	resp, err := http.ReadResponse(br, dummy)
	if err != nil {
		t.Fatalf("read handshake response: %v", err)
	}
	return &wsClient{conn: conn, br: br}, resp
}

func (c *wsClient) close() { _ = c.conn.Close() }

// writeMessage writes a masked client text frame (client frames MUST be masked).
func (c *wsClient) writeMessage(payload []byte) error {
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	n := len(payload)
	var hdr []byte
	b0 := byte(0x80 | opText)
	switch {
	case n <= 125:
		hdr = []byte{b0, byte(0x80 | n)}
	case n <= 0xffff:
		hdr = []byte{b0, 0x80 | 126, 0, 0}
		binary.BigEndian.PutUint16(hdr[2:], uint16(n))
	default:
		hdr = make([]byte, 10)
		hdr[0], hdr[1] = b0, 0x80|127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
	}
	hdr = append(hdr, mask[:]...)
	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = payload[i] ^ mask[i&3]
	}
	if _, err := c.conn.Write(hdr); err != nil {
		return err
	}
	_, err := c.conn.Write(masked)
	return err
}

func (c *wsClient) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.writeMessage(b)
}

// readFrame reads one server frame (server frames are unmasked).
func (c *wsClient) readFrame() (int, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return 0, nil, err
	}
	op := int(hdr[0] & 0x0f)
	masked := hdr[1]&0x80 != 0
	length := int64(hdr[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int64(binary.BigEndian.Uint64(ext[:]))
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(c.br, buf); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range buf {
			buf[i] ^= mask[i&3]
		}
	}
	return op, buf, nil
}

// readEnvelope reads the next server text frame and decodes it, with a deadline.
func (c *wsClient) readEnvelope(t *testing.T, within time.Duration) (Envelope, error) {
	t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(within))
	defer c.conn.SetReadDeadline(time.Time{})
	op, payload, err := c.readFrame()
	if err != nil {
		return Envelope{}, err
	}
	if op != opText {
		return Envelope{}, fmt.Errorf("want text frame, got opcode 0x%x", op)
	}
	var env Envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

// expectEnvelope reads one envelope and fails if it is absent or the wrong type.
func (c *wsClient) expectEnvelope(t *testing.T, typ string) Envelope {
	t.Helper()
	env, err := c.readEnvelope(t, 2*time.Second)
	if err != nil {
		t.Fatalf("read %s: %v", typ, err)
	}
	if env.Type != typ {
		t.Fatalf("want envelope type %q, got %q", typ, env.Type)
	}
	return env
}

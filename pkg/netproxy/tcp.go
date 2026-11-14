package netproxy

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"

	"kurukshetra/pkg/trace"
)

// Stream mode (scenario mode "tcp"): every accepted connection on link i->j is
// piped to node j. Each Read chunk from the client side is one message with ID
// "<from>-<to>-<n>" (meta mode=tcp, conn=<k>) that goes through the scheduler
// like an HTTP message. Allowed fates: deliver; delay (chunks of a connection
// are written in order, so a delayed chunk holds back later ones); drop, which
// resets the connection (msg_drop reason=reset). Duplicate is treated as
// deliver. Partitioned links refuse new connections and reset existing ones.
// The reply direction is piped directly but recorded as msg_send/msg_deliver
// with meta dir=reply.

// chunkSize is the read buffer of one client-side chunk.
const chunkSize = 32 << 10

type tcpConn struct {
	id             int
	lk             link
	client, server net.Conn

	mu      sync.Mutex
	nextSeq int            // next chunk number assigned by the reader
	written int            // next chunk number to write to the server
	ready   map[int][]byte // decided-to-deliver chunks waiting for their turn
	dead    bool
}

type tcpState struct {
	mu      sync.Mutex
	conns   map[*tcpConn]bool
	connIDs map[link]int
}

func newTCPState() *tcpState {
	return &tcpState{conns: map[*tcpConn]bool{}, connIDs: map[link]int{}}
}

func (p *Proxy) acceptTCP(l net.Listener, lk link) {
	defer p.wg.Done()
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.serveTCP(c, lk)
		}()
	}
}

func (p *Proxy) connMeta(tc *tcpConn, extra ...string) map[string]string {
	m := map[string]string{"mode": "tcp", "conn": strconv.Itoa(tc.id)}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	return m
}

func (p *Proxy) serveTCP(client net.Conn, lk link) {
	t := p.tcp
	t.mu.Lock()
	t.connIDs[lk]++
	tc := &tcpConn{id: t.connIDs[lk], lk: lk, client: client, ready: map[int][]byte{}}
	t.mu.Unlock()

	if p.Blocked(lk.from, lk.to) {
		p.emit(trace.Event{Kind: trace.Note, From: lk.from, To: lk.to, Meta: p.connMeta(tc, "msg", "connection refused", "reason", "partition")})
		resetConn(client)
		return
	}
	p.mu.RLock()
	target := p.targets[lk.to]
	p.mu.RUnlock()
	server, err := net.Dial("tcp", target)
	if err != nil {
		p.emit(trace.Event{Kind: trace.Note, From: lk.from, To: lk.to, Meta: p.connMeta(tc, "msg", "dial failed", "error", err.Error())})
		resetConn(client)
		return
	}
	tc.server = server
	t.mu.Lock()
	select {
	case <-p.closed:
		t.mu.Unlock()
		resetConn(client)
		resetConn(server)
		return
	default:
	}
	t.conns[tc] = true
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.conns, tc)
		t.mu.Unlock()
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // reply direction: piped directly, recorded
		defer wg.Done()
		p.pipeReplies(tc)
	}()
	p.readChunks(tc)
	wg.Wait()
}

// readChunks turns each client-side Read into a parked message.
func (p *Proxy) readChunks(tc *tcpConn) {
	buf := make([]byte, chunkSize)
	for {
		n, err := tc.client.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			tc.mu.Lock()
			seq := tc.nextSeq
			tc.nextSeq++
			tc.mu.Unlock()
			m := &Message{ID: p.nextID(tc.lk), From: tc.lk.from, To: tc.lk.to, Size: n, ArrivedStep: p.step(),
				body: chunk, conn: tc, seq: seq}
			p.emit(trace.Event{Step: m.ArrivedStep, Kind: trace.MsgSend, From: m.From, To: m.To, MsgID: m.ID, Size: n, Meta: p.connMeta(tc)})
			select {
			case p.incoming <- m:
			case <-p.closed:
				return
			}
		}
		if err != nil {
			// Client finished sending: once every chunk is written, half-close.
			tc.mu.Lock()
			tc.ready[-1] = nil // marker: EOF after nextSeq chunks
			tc.mu.Unlock()
			p.flushTCP(tc)
			return
		}
	}
}

func (p *Proxy) pipeReplies(tc *tcpConn) {
	buf := make([]byte, chunkSize)
	n := 0
	for {
		k, err := tc.server.Read(buf)
		if k > 0 {
			n++
			id := fmt.Sprintf("%s-%s-r%d.%d", tc.lk.to, tc.lk.from, tc.id, n)
			meta := p.connMeta(tc, "dir", "reply")
			p.emit(trace.Event{Kind: trace.MsgSend, From: tc.lk.to, To: tc.lk.from, MsgID: id, Size: k, Meta: meta})
			if _, werr := tc.client.Write(buf[:k]); werr != nil {
				return
			}
			p.emit(trace.Event{Kind: trace.MsgDeliver, From: tc.lk.to, To: tc.lk.from, MsgID: id, Size: k, Meta: meta})
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				closeWrite(tc.client)
			}
			return
		}
	}
}

// flushTCP writes every decided chunk whose turn has come, in order.
func (p *Proxy) flushTCP(tc *tcpConn) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.dead {
		return errors.New("connection reset")
	}
	for {
		b, ok := tc.ready[tc.written]
		if !ok {
			break
		}
		delete(tc.ready, tc.written)
		tc.written++
		if _, err := tc.server.Write(b); err != nil {
			return err
		}
	}
	if _, eof := tc.ready[-1]; eof && tc.written == tc.nextSeq {
		delete(tc.ready, -1)
		closeWrite(tc.server)
	}
	return nil
}

func (p *Proxy) deliverTCP(m *Message) error {
	p.emit(trace.Event{Kind: trace.MsgDeliver, From: m.From, To: m.To, MsgID: m.ID, Size: m.Size, Meta: p.connMeta(m.conn)})
	m.conn.mu.Lock()
	m.conn.ready[m.seq] = m.body
	m.conn.mu.Unlock()
	return p.flushTCP(m.conn)
}

func (p *Proxy) deliverTCPAsync(m *Message, wg *sync.WaitGroup, onErr func(error)) {
	p.emit(trace.Event{Kind: trace.MsgDeliver, From: m.From, To: m.To, MsgID: m.ID, Size: m.Size, Meta: p.connMeta(m.conn)})
	m.conn.mu.Lock()
	m.conn.ready[m.seq] = m.body
	m.conn.mu.Unlock()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := p.flushTCP(m.conn); err != nil && onErr != nil {
			onErr(fmt.Errorf("deliver %s: %w", m.ID, err))
		}
	}()
}

// dropTCP resets the connection (a dropped chunk cannot be skipped in a stream).
func (p *Proxy) dropTCP(m *Message, meta map[string]string) {
	meta = p.connMeta(m.conn)
	meta["reason"] = "reset"
	p.emit(trace.Event{Kind: trace.MsgDrop, From: m.From, To: m.To, MsgID: m.ID, Size: m.Size, Meta: meta})
	p.killConn(m.conn)
}

func (p *Proxy) killConn(tc *tcpConn) {
	tc.mu.Lock()
	tc.dead = true
	tc.mu.Unlock()
	resetConn(tc.client)
	if tc.server != nil {
		resetConn(tc.server)
	}
}

// resetBlockedTCP resets existing connections on links that became blocked.
func (p *Proxy) resetBlockedTCP() {
	p.tcp.mu.Lock()
	var victims []*tcpConn
	for tc := range p.tcp.conns {
		if p.Blocked(tc.lk.from, tc.lk.to) {
			victims = append(victims, tc)
		}
	}
	p.tcp.mu.Unlock()
	for _, tc := range victims {
		p.emit(trace.Event{Kind: trace.Note, From: tc.lk.from, To: tc.lk.to, Meta: p.connMeta(tc, "msg", "connection reset", "reason", "partition")})
		p.killConn(tc)
	}
}

func (p *Proxy) closeTCP() {
	p.tcp.mu.Lock()
	var all []*tcpConn
	for tc := range p.tcp.conns {
		all = append(all, tc)
	}
	p.tcp.mu.Unlock()
	for _, tc := range all {
		p.killConn(tc)
	}
}

func resetConn(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	_ = c.Close()
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
		return
	}
	_ = c.Close()
}

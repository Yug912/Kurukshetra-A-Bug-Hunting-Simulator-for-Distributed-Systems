package netproxy

import (
	"net"
	"sync"
)

// tcpConn and the tcp-mode functions are implemented in M8.
type tcpConn struct{}

type tcpState struct{ mu sync.Mutex }

func newTCPState() *tcpState { return &tcpState{} }

func (p *Proxy) acceptTCP(l net.Listener, lk link) {
	defer p.wg.Done()
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		c.Close()
	}
}

func (p *Proxy) deliverTCP(m *Message) error                { return nil }
func (p *Proxy) dropTCP(m *Message, meta map[string]string) {}
func (p *Proxy) resetBlockedTCP()                           {}
func (p *Proxy) closeTCP()                                  {}

func (p *Proxy) deliverTCPAsync(m *Message, wg *sync.WaitGroup, onErr func(error)) {}

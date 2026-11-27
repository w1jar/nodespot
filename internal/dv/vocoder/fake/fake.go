// Package fake is an in-process stand-in for the software AMBE vocoder: a UDP server with a
// deterministic codec, used by tests and `nodespot-dv bench -fake`.
//
// Codec: encode (320 bytes in) answers {0xA5, s0>>8, s0, 0, 0, 0, n} where s0
// is the first sample and n counts encode requests (first one is 0); decode
// (7 bytes in) answers 160 samples all equal to int16(bytes 1-2, big endian).
package fake

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	pcmBytes  = 320
	ambeBytes = 7
)

// Server is the fake software AMBE vocoder.
type Server struct {
	conn *net.UDPConn

	mu    sync.Mutex
	delay time.Duration
	drop  func(n uint64) bool

	reqs   atomic.Uint64
	encs   atomic.Uint64
	closed chan struct{}
	wg     sync.WaitGroup
}

// New listens on addr (use "127.0.0.1:0").
func New(addr string) (*Server, error) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	c, err := net.ListenUDP("udp", ua)
	if err != nil {
		return nil, err
	}
	s := &Server{conn: c, closed: make(chan struct{})}
	s.wg.Add(1)
	go s.loop()
	return s, nil
}

// Addr is the listen address as host:port.
func (s *Server) Addr() string { return s.conn.LocalAddr().String() }

// SetDelay delays every reply by d (a reply later than the client timeout
// arrives on a socket the client already closed).
func (s *Server) SetDelay(d time.Duration) {
	s.mu.Lock()
	s.delay = d
	s.mu.Unlock()
}

// SetDrop installs a predicate; a request n (1-based, counting every request)
// for which it returns true gets no reply.
func (s *Server) SetDrop(f func(n uint64) bool) {
	s.mu.Lock()
	s.drop = f
	s.mu.Unlock()
}

// Requests is the number of requests received.
func (s *Server) Requests() uint64 { return s.reqs.Load() }

// Close stops the server.
func (s *Server) Close() error {
	select {
	case <-s.closed:
		return nil
	default:
	}
	close(s.closed)
	err := s.conn.Close()
	s.wg.Wait()
	return err
}

func (s *Server) loop() {
	defer s.wg.Done()
	buf := make([]byte, 2048)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		var reply []byte
		switch n {
		case pcmBytes:
			seq := byte(s.encs.Add(1) - 1)
			s0 := int16(uint16(buf[0]) | uint16(buf[1])<<8)
			reply = []byte{0xA5, byte(s0 >> 8), byte(s0), 0, 0, 0, seq}
		case ambeBytes:
			v := int16(uint16(buf[1])<<8 | uint16(buf[2]))
			reply = make([]byte, pcmBytes)
			for i := 0; i < 160; i++ {
				reply[2*i] = byte(v)
				reply[2*i+1] = byte(v >> 8)
			}
		default:
			continue
		}
		num := s.reqs.Add(1)
		s.mu.Lock()
		delay, drop := s.delay, s.drop
		s.mu.Unlock()
		if drop != nil && drop(num) {
			continue
		}
		if delay <= 0 {
			s.conn.WriteToUDP(reply, from)
			continue
		}
		time.AfterFunc(delay, func() {
			select {
			case <-s.closed:
			default:
				s.conn.WriteToUDP(reply, from)
			}
		})
	}
}

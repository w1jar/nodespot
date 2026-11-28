package fake

import (
	"bytes"
	"io"
	"sync"
)

// pipe is one direction of a buffered in-memory link. Writes never block;
// Read blocks until data arrives or the link closes.
type pipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    bytes.Buffer
	closed bool
}

func newPipe() *pipe {
	p := &pipe{}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *pipe) read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.buf.Len() == 0 && !p.closed {
		p.cond.Wait()
	}
	if p.buf.Len() == 0 {
		return 0, io.EOF
	}
	return p.buf.Read(b)
}

func (p *pipe) write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	p.buf.Write(b)
	p.cond.Broadcast()
	return len(b), nil
}

func (p *pipe) close() {
	p.mu.Lock()
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
}

// end is one side of a link made of two pipes. Closing either side closes
// the link both ways.
type end struct {
	in, out *pipe
	once    *sync.Once
}

func (e *end) Read(b []byte) (int, error)  { return e.in.read(b) }
func (e *end) Write(b []byte) (int, error) { return e.out.write(b) }
func (e *end) Close() error {
	e.once.Do(func() {
		e.in.close()
		e.out.close()
	})
	return nil
}

// link returns the two ends of a new buffered duplex link.
func link() (a, b io.ReadWriteCloser) {
	x, y := newPipe(), newPipe()
	once := new(sync.Once)
	return &end{in: x, out: y, once: once}, &end{in: y, out: x, once: once}
}

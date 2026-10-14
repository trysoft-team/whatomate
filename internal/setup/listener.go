package setup

import (
	"net"
	"sync"
)

// Shared accepts on one socket and hands each connection to whichever view
// is accepting, so the socket outlives the server that used it first.
// Connections that arrive while no view is accepting wait for the next one.
type Shared struct {
	raw   net.Listener
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
	err   error // set before done is closed; nil after an orderly Close
}

// Share starts accepting on raw.
func Share(raw net.Listener) *Shared {
	s := &Shared{raw: raw, conns: make(chan net.Conn), done: make(chan struct{})}
	go s.loop()
	return s
}

func (s *Shared) loop() {
	for {
		c, err := s.raw.Accept()
		if err != nil {
			s.fail(err)
			return
		}
		select {
		case s.conns <- c:
		case <-s.done:
			_ = c.Close()
			return
		}
	}
}

// Close closes the socket and every view.
func (s *Shared) Close() error {
	var err error
	s.once.Do(func() {
		close(s.done)
		err = s.raw.Close()
	})
	return err
}

// fail shuts s down because the socket stopped accepting; views report cause.
func (s *Shared) fail(cause error) {
	s.once.Do(func() {
		s.err = cause
		close(s.done)
		_ = s.raw.Close()
	})
}

// closedErr is what views return once s.done is closed.
func (s *Shared) closedErr() error {
	if s.err != nil {
		return s.err
	}
	return net.ErrClosed
}

// View returns a listener that takes connections from s until it is closed.
// Closing an owner view also closes s.
func (s *Shared) View(owner bool) net.Listener {
	return &view{s: s, owner: owner, done: make(chan struct{})}
}

type view struct {
	s     *Shared
	owner bool
	done  chan struct{}
	once  sync.Once
}

func (v *view) Accept() (net.Conn, error) {
	select {
	case <-v.done:
		return nil, net.ErrClosed
	case <-v.s.done:
		return nil, v.s.closedErr()
	default:
	}
	select {
	case c := <-v.s.conns:
		return c, nil
	case <-v.done:
		return nil, net.ErrClosed
	case <-v.s.done:
		return nil, v.s.closedErr()
	}
}

func (v *view) Close() error {
	v.once.Do(func() { close(v.done) })
	if v.owner {
		return v.s.Close()
	}
	return nil
}

func (v *view) Addr() net.Addr { return v.s.raw.Addr() }

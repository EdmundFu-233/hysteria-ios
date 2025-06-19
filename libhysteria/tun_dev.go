package libhysteria

import (
	"errors"
	"fmt"
	"sync"
)

type tunDevice struct {
	in   chan []byte
	out  chan []byte
	cl   chan struct{}
	once sync.Once
}

func newTunDevice() *tunDevice {
	goLogger(1, "[TunDevice] newTunDevice called.")
	return &tunDevice{
		in:  make(chan []byte, 4096),
		out: make(chan []byte, 4096),
		cl:  make(chan struct{}),
	}
}

func (d *tunDevice) ReadFromInChan() ([]byte, error) {
	goLogger(1, "[TunDevice] ReadFromInChan: Waiting on 'in' channel.")
	select {
	case p := <-d.in:
		goLogger(1, fmt.Sprintf("[TunDevice] ReadFromInChan: Read %d bytes.", len(p)))
		return p, nil
	case <-d.cl:
		goLogger(1, "[TunDevice] ReadFromInChan: 'cl' channel closed.")
		return nil, errors.New("closed")
	}
}
func (d *tunDevice) WriteToInChan(p []byte) error {
	goLogger(1, fmt.Sprintf("[TunDevice] WriteToInChan: Writing %d bytes to 'in' channel.", len(p)))
	select {
	case d.in <- p:
		goLogger(1, "[TunDevice] WriteToInChan: Write successful.")
		return nil
	case <-d.cl:
		goLogger(1, "[TunDevice] WriteToInChan: 'cl' channel closed, write failed.")
		return errors.New("closed")
	}
}
func (d *tunDevice) ReadFromOutChan() ([]byte, error) {
	goLogger(1, "[TunDevice] ReadFromOutChan: Waiting on 'out' channel.")
	select {
	case p := <-d.out:
		goLogger(1, fmt.Sprintf("[TunDevice] ReadFromOutChan: Read %d bytes.", len(p)))
		return p, nil
	case <-d.cl:
		goLogger(1, "[TunDevice] ReadFromOutChan: 'cl' channel closed.")
		return nil, errors.New("closed")
	}
}
func (d *tunDevice) WriteToOutChan(p []byte) error {
	goLogger(1, fmt.Sprintf("[TunDevice] WriteToOutChan: Writing %d bytes to 'out' channel.", len(p)))
	select {
	case d.out <- p:
		goLogger(1, "[TunDevice] WriteToOutChan: Write successful.")
		return nil
	case <-d.cl:
		goLogger(1, "[TunDevice] WriteToOutChan: 'cl' channel closed, write failed.")
		return errors.New("closed")
	}
}
func (d *tunDevice) Close() {
	goLogger(1, "[TunDevice] Close called.")
	d.once.Do(func() {
		goLogger(1, "[TunDevice] Close: Closing 'cl' channel.")
		close(d.cl)
	})
}

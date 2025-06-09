package libhysteria

import (
	"errors"
	"sync"
)

type tunDevice struct {
	in   chan []byte
	out  chan []byte
	cl   chan struct{}
	once sync.Once
}

func newTunDevice() *tunDevice {
	return &tunDevice{
		in:  make(chan []byte, 4096),
		out: make(chan []byte, 4096),
		cl:  make(chan struct{}),
	}
}

func (d *tunDevice) ReadFromInChan() ([]byte, error) {
	select {
	case p := <-d.in:
		return p, nil
	case <-d.cl:
		return nil, errors.New("closed")
	}
}
func (d *tunDevice) WriteToInChan(p []byte) error {
	select {
	case d.in <- p:
		return nil
	case <-d.cl:
		return errors.New("closed")
	}
}
func (d *tunDevice) ReadFromOutChan() ([]byte, error) {
	select {
	case p := <-d.out:
		return p, nil
	case <-d.cl:
		return nil, errors.New("closed")
	}
}
func (d *tunDevice) WriteToOutChan(p []byte) error {
	select {
	case d.out <- p:
		return nil
	case <-d.cl:
		return errors.New("closed")
	}
}
func (d *tunDevice) Close() { d.once.Do(func() { close(d.cl) }) }

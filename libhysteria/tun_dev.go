// tun_dev.go

package libhysteria

import (
	"errors"
	"sync"
)

type tunDevice struct {
	in   chan []byte
	out  chan *[]byte
	cl   chan struct{}
	once sync.Once
}

func newTunDevice() *tunDevice {
	goLogger(1, "[TunDevice] newTunDevice called.")
	// ==================== MODIFICATION START (FINAL TUNING) ====================
	// 进一步减小通道缓冲区大小。
	return &tunDevice{
		in:  make(chan []byte, 1024),
		out: make(chan *[]byte, 1024),
		cl:  make(chan struct{}),
	}
	// ===================== MODIFICATION END (FINAL TUNING) =====================
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
	default:
		goLogger(2, "[TunDevice] WriteToInChan: 'in' channel is full, dropping packet.")
		return nil
	}
}

func (d *tunDevice) ReadFromOutChan() (*[]byte, error) {
	select {
	case p, ok := <-d.out:
		if !ok {
			return nil, errors.New("closed")
		}
		return p, nil
	case <-d.cl:
		return nil, errors.New("closed")
	default:
		return nil, nil
	}
}

func (d *tunDevice) WriteToOutChan(p *[]byte) error {
	select {
	case d.out <- p:
		return nil
	case <-d.cl:
		return errors.New("closed")
	default:
		goLogger(2, "[TunDevice] WriteToOutChan: 'out' channel is full, dropping packet.")
		outBufPool.Put(p)
		return nil
	}
}

func (d *tunDevice) Close() {
	goLogger(1, "[TunDevice] Close called.")
	d.once.Do(func() {
		goLogger(1, "[TunDevice] Close: Closing 'cl' channel.")
		close(d.cl)
	})
}

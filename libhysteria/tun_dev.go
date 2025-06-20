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
	goLogger(1, "[TunDevice] newTunDevice called.")
	return &tunDevice{
		in:  make(chan []byte, 1024),
		out: make(chan []byte, 1024),
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
	default:
		goLogger(2, "[TunDevice] WriteToInChan: 'in' channel is full, dropping packet.")
		return nil
	}
}

// MARK: - CORRECTED
// 此函数已恢复为非阻塞读取。这是为了防止在 Swift Actor 中调用时发生死锁。
// 当没有数据时，它会立即返回 nil, nil，由 Swift 端的指数退避逻辑来处理休眠，从而节省 CPU。
func (d *tunDevice) ReadFromOutChan() ([]byte, error) {
	select {
	case p, ok := <-d.out:
		if !ok {
			return nil, errors.New("closed")
		}
		return p, nil
	case <-d.cl:
		return nil, errors.New("closed")
	default:
		// 立即返回，表示当前没有数据包。
		return nil, nil
	}
}

func (d *tunDevice) WriteToOutChan(p []byte) error {
	select {
	case d.out <- p:
		return nil
	case <-d.cl:
		return errors.New("closed")
	default:
		goLogger(2, "[TunDevice] WriteToOutChan: 'out' channel is full, dropping packet.")
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

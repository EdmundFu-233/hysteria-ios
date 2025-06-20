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

// [PLAN STEP 3] 修正：缩小 channel 容量
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

// [PLAN STEP 3] 修正：使用非阻塞写入，如果队列满则丢弃
func (d *tunDevice) WriteToInChan(p []byte) error {
	select {
	case d.in <- p:
		return nil
	case <-d.cl:
		return errors.New("closed")
	default:
		// 队列已满，丢弃数据包以防止阻塞上游
		goLogger(2, "[TunDevice] WriteToInChan: 'in' channel is full, dropping packet.")
		return nil // 返回 nil 表示“已处理”（即使是丢弃），避免上游报错
	}
}

func (d *tunDevice) ReadFromOutChan() ([]byte, error) {
	select {
	case p := <-d.out:
		return p, nil
	case <-d.cl:
		return nil, errors.New("closed")
	default:
		return nil, nil
	}
}

// [PLAN STEP 3] 修正：使用非阻塞写入，如果队列满则丢弃
func (d *tunDevice) WriteToOutChan(p []byte) error {
	select {
	case d.out <- p:
		return nil
	case <-d.cl:
		return errors.New("closed")
	default:
		// 队列已满，丢弃数据包以防止阻塞上游
		goLogger(2, "[TunDevice] WriteToOutChan: 'out' channel is full, dropping packet.")
		return nil // 返回 nil 表示“已处理”（即使是丢弃），避免上游报错
	}
}

func (d *tunDevice) Close() {
	goLogger(1, "[TunDevice] Close called.")
	d.once.Do(func() {
		goLogger(1, "[TunDevice] Close: Closing 'cl' channel.")
		close(d.cl)
	})
}

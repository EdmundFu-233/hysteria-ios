// libhysteria/tun_dev.go (完整、修正后的文件)
package libhysteria

import (
	"errors"
	"sync"
)

type tunDevice struct {
	inChan  chan []byte
	outChan chan []byte
	closeCh chan struct{}
	once    sync.Once
}

func newTunDevice() *tunDevice {
	return &tunDevice{
		inChan:  make(chan []byte, 4096),
		outChan: make(chan []byte, 4096),
		closeCh: make(chan struct{}),
	}
}

// ReadFromInChan 从上行通道读取数据包 (供 lwIP 使用)
func (d *tunDevice) ReadFromInChan() ([]byte, error) {
	select {
	case pkt := <-d.inChan:
		return pkt, nil
	case <-d.closeCh:
		return nil, errors.New("tun device closed")
	}
}

// WriteToInChan 向上行通道写入数据包 (供 Swift/TUN 接口使用)
func (d *tunDevice) WriteToInChan(pkt []byte) error {
	select {
	case d.inChan <- pkt:
		return nil
	case <-d.closeCh:
		return errors.New("tun device closed")
	}
}

// ReadFromOutChan 从下行通道读取数据包 (供 Swift/TUN 接口使用)
func (d *tunDevice) ReadFromOutChan() ([]byte, error) {
	select {
	case pkt := <-d.outChan:
		return pkt, nil
	case <-d.closeCh:
		return nil, errors.New("tun device closed")
	}
}

// WriteToOutChan 向下行通道写入数据包 (供 lwIP 使用)
func (d *tunDevice) WriteToOutChan(pkt []byte) error {
	select {
	case d.outChan <- pkt:
		return nil
	case <-d.closeCh:
		return errors.New("tun device closed")
	}
}

func (d *tunDevice) Close() {
	d.once.Do(func() { close(d.closeCh) })
}

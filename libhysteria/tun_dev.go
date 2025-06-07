// libhysteria/tun_dev.go
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
        inChan:  make(chan []byte, 2048),
        outChan: make(chan []byte, 2048),
        closeCh: make(chan struct{}),
    }
}

func (d *tunDevice) ReadPacket() ([]byte, error) {
    select {
    case pkt := <-d.outChan:
        return pkt, nil
    case <-d.closeCh:
        return nil, errors.New("tun device closed")
    }
}

func (d *tunDevice) WritePacket(pkt []byte) error {
    select {
    case d.inChan <- pkt:
        return nil
    case <-d.closeCh:
        return errors.New("tun device closed")
    }
}

func (d *tunDevice) Close() {
    d.once.Do(func() { close(d.closeCh) })
}


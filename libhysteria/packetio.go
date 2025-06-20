// packetio.go

package libhysteria

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/xjasonlyu/tun2socks/v2/core"
	"github.com/xjasonlyu/tun2socks/v2/core/adapter"
	"github.com/xjasonlyu/tun2socks/v2/core/device/iobased"
	"github.com/xjasonlyu/tun2socks/v2/tunnel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

var (
	ioOnce      sync.Once
	tunDev      *tunDevice
	gvisorStack *stack.Stack
)

var outBufPool = sync.Pool{New: func() interface{} {
	b := make([]byte, 2048)
	return &b
}}

type tunDeviceWrapper struct {
	dev *tunDevice
}

func (w *tunDeviceWrapper) Read(p []byte) (int, error) {
	data, err := w.dev.ReadFromInChan()
	if err != nil {
		return 0, err
	}
	if len(data) > len(p) {
		return 0, io.ErrShortBuffer
	}
	n := copy(p, data)
	return n, nil
}

func (w *tunDeviceWrapper) Write(p []byte) (int, error) {
	bufPtr := outBufPool.Get().(*[]byte)
	buf := *bufPtr

	if cap(buf) < len(p) {
		buf = make([]byte, len(p))
	}
	buf = buf[:len(p)]
	copy(buf, p)
	*bufPtr = buf

	err := w.dev.WriteToOutChan(bufPtr)
	if err != nil {
		goLogger(2, fmt.Sprintf(">>>>>> PROBE 3 FAILED: WriteToOutChan error: %v", err))
		outBufPool.Put(bufPtr)
		return 0, err
	}
	return len(p), nil
}

func (w *tunDeviceWrapper) Close() error {
	w.dev.Close()
	return nil
}

// ==================== MODIFICATION START (FINAL TUNING) ====================
// 核心优化：将UDP会话的空闲超时从60秒大幅缩短到3秒。
// 这将使失败和空闲的连接被更快地清理，显著降低内存峰值。
const udpTimeout = 3 * time.Second

// ===================== MODIFICATION END (FINAL TUNING) =====================

func initPacketIO(c *ClientBridge) {
	goLogger(0, "[PacketIO] initPacketIO: ENTERED (packetio.go).")
	ioOnce.Do(func() {
		goLogger(1, "[PacketIO] initPacketIO: Running inside sync.Once.")
		tunDev = newTunDevice()
		goLogger(1, "[PacketIO] initPacketIO: newTunDevice created.")

		tunnel.T().SetUDPTimeout(udpTimeout)
		goLogger(0, fmt.Sprintf("[PacketIO] Set global UDP timeout to %v", udpTimeout))

		wrapper := &tunDeviceWrapper{dev: tunDev}
		mtu := c.tunConfig.MTU
		if mtu == 0 {
			mtu = 1420
		}
		dev, err := iobased.New(wrapper, uint32(mtu), 0)
		if err != nil {
			goLogger(2, fmt.Sprintf("[PacketIO] initPacketIO: Failed to create iobased device: %s", err.Error()))
			return
		}
		goLogger(1, "[PacketIO] initPacketIO: iobased device created.")

		udpHandler := newUDPHandler(c)
		proxyHandler := &proxyHandler{c: c, udp: udpHandler}

		stack, err := core.CreateStack(&core.Config{
			LinkEndpoint:     dev,
			TransportHandler: proxyHandler,
		})
		if err != nil {
			goLogger(2, fmt.Sprintf("[PacketIO] initPacketIO: Failed to create gVisor stack: %s", err.Error()))
			return
		}
		gvisorStack = stack
		goLogger(1, "[PacketIO] initPacketIO: gVisor stack created.")

		goLogger(1, "[PacketIO] initPacketIO: Starting UDP handler loop goroutine.")
		go udpHandler.loop()

		goLogger(0, "[PacketIO] initPacketIO: All handlers and goroutines started.")
	})
}

func (c *ClientBridge) WriteFromTun(p []byte) error {
	if c == nil {
		goLogger(2, ">>> [ClientBridge] WriteFromTun: CRITICAL ERROR - ClientBridge instance (c) is nil!")
		return errors.New("ClientBridge instance is nil in WriteFromTun")
	}
	initPacketIO(c)
	if tunDev == nil {
		goLogger(2, ">>> [ClientBridge] WriteFromTun: ERROR - tunDev is nil after initPacketIO call. Cannot write to InChan.")
		return errors.New("tunDevice not initialized in WriteFromTun (packetio.go)")
	}
	return tunDev.WriteToInChan(p)
}

func (c *ClientBridge) ReadToTun(out []byte) (int, error) {
	if c == nil {
		goLogger(2, ">>> [ClientBridge] ReadToTun: CRITICAL ERROR - ClientBridge instance (c) is nil!")
		return 0, errors.New("ClientBridge instance is nil in ReadToTun")
	}
	initPacketIO(c)
	if tunDev == nil {
		goLogger(2, ">>> [ClientBridge] ReadToTun: ERROR - tunDev is nil after initPacketIO call. Cannot read from OutChan.")
		return 0, errors.New("tunDevice not initialized in ReadToTun (packetio.go)")
	}

	pPtr, err := tunDev.ReadFromOutChan()
	if err != nil {
		if err.Error() != "closed" {
			goLogger(2, fmt.Sprintf("[ClientBridge] ReadToTun: tunDev.ReadFromOutChan error: %s (packetio.go)", err.Error()))
		}
		return 0, err
	}
	if pPtr == nil {
		return 0, nil
	}

	defer outBufPool.Put(pPtr)

	p := *pPtr
	if len(p) > len(out) {
		goLogger(2, "[ClientBridge] ReadToTun: Buffer too small! (packetio.go)")
		return 0, errors.New("buf too small")
	}
	n := copy(out, p)
	return n, nil
}

func (c *ClientBridge) Close() error {
	goLogger(0, "[ClientBridge] Close called.")
	if gvisorStack != nil {
		goLogger(1, "[ClientBridge] Close: Closing gVisor stack.")
		gvisorStack.Close()
		gvisorStack.Wait()
		gvisorStack = nil
		goLogger(1, "[ClientBridge] Close: gVisor stack closed.")
	}
	goLogger(1, "[ClientBridge] Close: Resetting ioOnce for packetio.")
	ioOnce = sync.Once{}
	if c.cl != nil {
		goLogger(1, "[ClientBridge] Close: Closing Hysteria client.")
		return c.cl.Close()
	}
	goLogger(1, "[ClientBridge] Close: No Hysteria client to close.")
	return nil
}

type proxyHandler struct {
	c   *ClientBridge
	udp *udpHandler
}

func (h *proxyHandler) HandleTCP(conn adapter.TCPConn) {
	dst := conn.LocalAddr().String()
	goLogger(1, fmt.Sprintf("[TCPHandler] Handle: New TCP connection for %s.", dst))

	r, err := h.c.cl.TCP(dst)
	if err != nil {
		goLogger(2, fmt.Sprintf("[TCPHandler] Handle: Hysteria TCP dial failed for %s: %s", dst, err.Error()))
		_ = conn.Close()
		return
	}
	goLogger(0, fmt.Sprintf(">>>>>> PROBE A.1 (TCP): Hysteria TCP dial for %s successful. Starting bi-directional copy.", dst))

	var closeOnce sync.Once
	cleanup := func() {
		conn.Close()
		r.Close()
		goLogger(1, fmt.Sprintf("[TCPHandler] Cleanup executed for %s.", dst))
	}

	go func() {
		_, err := io.Copy(r, conn)
		if err != nil && err != io.EOF {
			goLogger(1, fmt.Sprintf("[TCPHandler] gVisor->Hysteria copy for %s finished with error: %v", dst, err))
		}
		closeOnce.Do(cleanup)
	}()

	go func() {
		_, err := io.Copy(conn, r)
		if err != nil && err != io.EOF {
			goLogger(1, fmt.Sprintf("[TCPHandler] Hysteria->gVisor copy for %s finished with error: %v", dst, err))
		}
		closeOnce.Do(cleanup)
	}()
}

func (h *proxyHandler) HandleUDP(conn adapter.UDPConn) {
	dst := conn.LocalAddr().String()
	goLogger(1, fmt.Sprintf("[UDPHandler] Handle: New UDP association for %s.", dst))
	h.udp.conns.Store(dst, conn)

	go func() {
		defer h.udp.conns.Delete(dst)
		defer conn.Close()

		buf := make([]byte, 65535)
		for {
			err := conn.SetReadDeadline(time.Now().Add(udpTimeout))
			if err != nil {
				goLogger(1, fmt.Sprintf("[UDPHandler] SetReadDeadline for %s failed: %v", dst, err))
				return
			}

			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					goLogger(0, fmt.Sprintf("[UDPHandler] Connection for %s timed out due to inactivity. Cleaning up.", dst))
				} else if !strings.Contains(err.Error(), "use of closed network connection") {
					goLogger(1, fmt.Sprintf("[UDPHandler] ReadFrom gVisor for %s failed: %v. Cleaning up.", dst, err))
				}
				return
			}

			if h.c.udp == nil {
				goLogger(2, fmt.Sprintf("[UDPHandler] Hysteria UDP session (h.c.udp) is nil. Cannot send packet to %s.", dst))
				return
			}
			err = h.c.udp.Send(buf[:n], dst)
			if err != nil {
				goLogger(2, fmt.Sprintf("[UDPHandler] Hysteria UDP Send for %s failed: %v.", dst, err))
			}
		}
	}()
}

type udpHandler struct {
	c     *ClientBridge
	conns sync.Map
}

func newUDPHandler(c *ClientBridge) *udpHandler { return &udpHandler{c: c} }

func (h *udpHandler) loop() {
	goLogger(1, "[UDPHandler] loop: Goroutine started, waiting for inbound UDP packets from Hysteria.")
	for p := range h.c.inboundUDPQ {
		if p == nil {
			continue
		}
		dst := p.SrcAddr.String()
		if v, ok := h.conns.Load(dst); ok {
			conn := v.(adapter.UDPConn)
			_, err := conn.Write(p.Data)
			if err != nil {
				goLogger(2, fmt.Sprintf(">>>>>> PROBE 2 FAILED (UDP %s): Write to gVisor FAILED: %s", dst, err.Error()))
				h.conns.Delete(dst)
			}
		} else {
			goLogger(1, fmt.Sprintf(">>>>>> PROBE 2 FAILED (UDP %s): No UDP association found! Packet dropped. (This might be due to a timeout or connection already closed)", dst))
		}
	}
	goLogger(0, "[UDPHandler] loop: Inbound UDP queue (h.c.inboundUDPQ) closed, goroutine finished.")
}

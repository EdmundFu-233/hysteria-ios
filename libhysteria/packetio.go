package libhysteria

import (
	"errors"
	"fmt"
	"io"
	"strings" // 确保导入 strings 包
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
	// ================== 探针 3: gVisor -> TUN ==================
	goLogger(0, fmt.Sprintf(">>>>>> PROBE 3: gVisor is writing %d bytes to TUN.", len(p)))
	// ==========================================================

	buf := make([]byte, len(p))
	copy(buf, p)
	err := w.dev.WriteToOutChan(buf)
	if err != nil {
		goLogger(2, fmt.Sprintf(">>>>>> PROBE 3 FAILED: WriteToOutChan error: %v", err))
		return 0, err
	}
	goLogger(0, ">>>>>> PROBE 3 SUCCESS: Packet sent to tun_dev.out channel.")
	return len(p), nil
}

func (w *tunDeviceWrapper) Close() error {
	w.dev.Close()
	return nil
}

func initPacketIO(c *ClientBridge) {
	goLogger(0, "[PacketIO] initPacketIO: ENTERED (packetio.go).")
	ioOnce.Do(func() {
		goLogger(1, "[PacketIO] initPacketIO: Running inside sync.Once.")
		tunDev = newTunDevice()
		goLogger(1, "[PacketIO] initPacketIO: newTunDevice created.")

		udpTimeout := 2 * time.Minute
		tunnel.T().SetUDPTimeout(udpTimeout)
		goLogger(0, fmt.Sprintf("[PacketIO] Set global UDP timeout to %v", udpTimeout))

		wrapper := &tunDeviceWrapper{dev: tunDev}
		dev, err := iobased.New(wrapper, 1280, 0)
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
	// 使用在 api.go 中定义的 goLogger。
	// 这是我们最关心的日志，确认 Swift 是否成功调用到这里。
	goLogger(0, fmt.Sprintf(">>> [ClientBridge] WriteFromTun: ENTERED (packetio.go). Packet size: %d. Attempting to init packet IO.", len(p)))

	// 确保 ClientBridge 实例本身不是 nil (防御性编程)
	if c == nil {
		// 即使在这里，goLogger 也应该能安全工作（如果 logChan 已初始化）或安全返回（如果未初始化）
		goLogger(2, ">>> [ClientBridge] WriteFromTun: CRITICAL ERROR - ClientBridge instance (c) is nil!")
		return errors.New("ClientBridge instance is nil in WriteFromTun")
	}

	initPacketIO(c) // 调用 initPacketIO，它内部有 sync.Once 保护

	// 再次检查 tunDev 是否被 initPacketIO 正确初始化
	if tunDev == nil {
		goLogger(2, ">>> [ClientBridge] WriteFromTun: ERROR - tunDev is nil after initPacketIO call. Cannot write to InChan.")
		return errors.New("tunDevice not initialized in WriteFromTun (packetio.go)")
	}

	// goLogger(1, ">>> [ClientBridge] WriteFromTun: tunDev is NOT nil. Preparing to write to InChan.") // 可以暂时注释掉，减少日志量
	buf := make([]byte, len(p))
	copy(buf, p)
	return tunDev.WriteToInChan(buf)
}

func (c *ClientBridge) ReadToTun(out []byte) (int, error) {
	goLogger(0, ">>> [ClientBridge] ReadToTun: ENTERED (packetio.go). Attempting to init packet IO.")

	if c == nil {
		goLogger(2, ">>> [ClientBridge] ReadToTun: CRITICAL ERROR - ClientBridge instance (c) is nil!")
		return 0, errors.New("ClientBridge instance is nil in ReadToTun")
	}

	initPacketIO(c)

	if tunDev == nil {
		goLogger(2, ">>> [ClientBridge] ReadToTun: ERROR - tunDev is nil after initPacketIO call. Cannot read from OutChan.")
		return 0, errors.New("tunDevice not initialized in ReadToTun (packetio.go)")
	}

	p, err := tunDev.ReadFromOutChan()
	if err != nil {
		if err.Error() != "closed" {
			goLogger(2, fmt.Sprintf("[ClientBridge] ReadToTun: tunDev.ReadFromOutChan error: %s (packetio.go)", err.Error()))
		}
		return 0, err
	}
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
	dst := conn.RemoteAddr().String()
	goLogger(1, fmt.Sprintf("[TCPHandler] Handle: New TCP connection for %s.", dst))

	r, err := h.c.cl.TCP(dst)
	if err != nil {
		goLogger(2, fmt.Sprintf("[TCPHandler] Handle: Hysteria TCP dial failed for %s: %s", dst, err.Error()))
		_ = conn.Close()
		return
	}
	goLogger(1, fmt.Sprintf("[TCPHandler] Handle: Hysteria TCP dial successful for %s. Starting io.Copy.", dst))

	go func() {
		defer r.Close()
		defer conn.Close()
		goLogger(1, fmt.Sprintf("[TCPHandler] io.Copy local->remote for %s started.", dst))
		_, errCopy := io.Copy(r, conn)
		if errCopy != nil && errCopy != io.EOF {
			// Corrected:
			goLogger(1, fmt.Sprintf("[TCPHandler] io.Copy local->remote for %s finished with error: %v", dst, errCopy))
		} else {
			goLogger(1, fmt.Sprintf("[TCPHandler] io.Copy local->remote for %s finished.", dst))
		}
	}()
	go func() {
		defer conn.Close()
		defer r.Close()
		goLogger(1, fmt.Sprintf("[TCPHandler] io.Copy remote->local for %s started.", dst))
		written, errCopy := io.Copy(conn, r)
		if errCopy != nil && errCopy != io.EOF {
			// Corrected:
			goLogger(2, fmt.Sprintf(">>>>>> PROBE 2 FAILED (TCP %s): io.Copy remote->local finished with error: %v. Bytes written: %d", dst, errCopy, written))
		} else {
			goLogger(0, fmt.Sprintf(">>>>>> PROBE 2 SUCCESS (TCP %s): io.Copy remote->local finished, %d bytes written.", dst, written))
		}
		// Corrected:
		goLogger(1, fmt.Sprintf("[TCPHandler] io.Copy remote->local for %s finished.", dst))
	}()
}

func (h *proxyHandler) HandleUDP(conn adapter.UDPConn) {
	dst := conn.RemoteAddr().String()
	goLogger(1, fmt.Sprintf("[UDPHandler] Handle: New UDP association for %s.", dst))
	h.udp.conns.Store(dst, conn)
	go func() {
		defer h.udp.conns.Delete(dst)
		defer conn.Close()
		buf := make([]byte, 65535)
		for {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				if !strings.Contains(err.Error(), "use of closed network connection") && !strings.Contains(err.Error(), "connection refused") { // Added "connection refused" as it can also be expected
					// Corrected:
					goLogger(1, fmt.Sprintf("[UDPHandler] ReadFrom gVisor for %s failed: %v. Closing association.", dst, err))
				}
				return
			}
			if h.c.udp == nil {
				// Corrected:
				goLogger(2, fmt.Sprintf("[UDPHandler] Hysteria UDP session (h.c.udp) is nil. Cannot send packet to %s.", dst))
				return
			}
			// Corrected:
			goLogger(1, fmt.Sprintf("[UDPHandler] Sending %d bytes from gVisor to remote UDP %s.", n, dst))
			err = h.c.udp.Send(buf[:n], dst)
			if err != nil {
				// Corrected:
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
			goLogger(1, "[UDPHandler] loop: Received nil packet from inboundUDPQ, skipping.")
			continue
		}
		dst := p.SrcAddr.String()
		goLogger(0, fmt.Sprintf(">>>>>> PROBE 1: Received UDP packet from Hysteria for %s (%d bytes).", dst, len(p.Data)))

		if v, ok := h.conns.Load(dst); ok {
			conn := v.(adapter.UDPConn)
			goLogger(0, fmt.Sprintf(">>>>>> PROBE 2: Found UDP association for %s. Writing %d bytes to gVisor.", dst, len(p.Data)))
			_, err := conn.Write(p.Data)
			if err != nil {
				// Corrected:
				goLogger(2, fmt.Sprintf(">>>>>> PROBE 2 FAILED (UDP %s): Write to gVisor FAILED: %s", dst, err.Error()))
			}
		} else {
			// Corrected:
			goLogger(2, fmt.Sprintf(">>>>>> PROBE 2 FAILED (UDP %s): No UDP association found! Packet dropped. (This might be due to a timeout or connection already closed)", dst))
		}
	}
	goLogger(0, "[UDPHandler] loop: Inbound UDP queue (h.c.inboundUDPQ) closed, goroutine finished.")
}

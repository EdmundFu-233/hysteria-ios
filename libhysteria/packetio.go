// libhysteria/packetio.go (已修正)
package libhysteria

import (
	"errors"
	"net"
	"strconv"
	"sync"

	"github.com/eycorsican/go-tun2socks/core"
)

var (
	initOnce sync.Once
	tunDev   *tunDevice
)

func initPacketIO(cli *ClientBridge) {
	initOnce.Do(func() {
		tunDev = newTunDevice()
		stack := core.NewLWIPStack()

		// 下行: lwIP 生成的包 -> tunDev.outChan -> Swift
		core.RegisterOutputFn(func(data []byte) (int, error) {
			err := tunDev.WriteToOutChan(data)
			if err != nil {
				return 0, err
			}
			return len(data), nil
		})

		// 注册有状态的 TCP 和 UDP 处理器
		core.RegisterTCPConnHandler(&tcpHandler{cli: cli})
		udpHandler := newUDPHandler(cli)
		core.RegisterUDPConnHandler(udpHandler)

		// 启动 UDP 处理器的主循环，处理来自 Hysteria 的入站包
		go udpHandler.processInbound()

		// 上行: Swift/TUN -> tunDev.inChan -> lwIP
		go func() {
			for {
				pkt, err := tunDev.ReadFromInChan()
				if err != nil {
					return // tunDev closed
				}
				_, _ = stack.Write(pkt)
			}
		}()
	})
}

// WriteFromTun 由 Swift 调用，将来自 iOS TUN 的 IP 包写入上行通道
func (c *ClientBridge) WriteFromTun(pkt []byte) error {
	initPacketIO(c)
	buf := make([]byte, len(pkt))
	copy(buf, pkt)
	return tunDev.WriteToInChan(buf)
}

// ReadToTun 由 Swift 调用，从下行通道拉一个 IP 包返回给 iOS TUN
func (c *ClientBridge) ReadToTun(out []byte) (int, error) {
	initPacketIO(c)
	pkt, err := tunDev.ReadFromOutChan()
	if err != nil {
		return 0, err
	}
	if len(pkt) > len(out) {
		return 0, errors.New("output buffer too small")
	}
	copy(out, pkt)
	return len(pkt), nil
}

// Close 优先关闭 tunDevice，然后关闭 Hysteria 客户端
func (c *ClientBridge) Close() error {
	if tunDev != nil {
		tunDev.Close()
	}
	return c.cl.Close()
}

// --- TCP Handler (保持不变) ---
type tcpHandler struct {
	cli *ClientBridge
}

func (h *tcpHandler) Handle(conn net.Conn, target *net.TCPAddr) error {
	hostPort := net.JoinHostPort(target.IP.String(), strconv.Itoa(target.Port))
	hyConn, err := h.cli.cl.TCP(hostPort)
	if err != nil {
		_ = conn.Close()
		return err
	}
	go func() {
		defer conn.Close()
		defer hyConn.Close()
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				_, err = hyConn.Write(buf[:n])
				if err != nil {
					return
				}
			}
		}
	}()
	go func() {
		defer conn.Close()
		defer hyConn.Close()
		buf := make([]byte, 4096)
		for {
			n, err := hyConn.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				_, err = conn.Write(buf[:n])
				if err != nil {
					return
				}
			}
		}
	}()
	return nil
}

// --- UDP Handler (完全重写) ---
type udpHandler struct {
	cli   *ClientBridge
	conns sync.Map // key: targetAddr (string), value: core.UDPConn
}

func newUDPHandler(cli *ClientBridge) *udpHandler {
	return &udpHandler{cli: cli}
}

// Connect 当 lwIP 创建一个新的 UDP "连接" 时被调用
func (h *udpHandler) Connect(conn core.UDPConn, target *net.UDPAddr) error {
	// 将这个新的连接存入 map，以便入站时可以找到它
	h.conns.Store(target.String(), conn)
	return nil
}

// ReceiveTo 当 App 通过 TUN 发送 UDP 包时被调用 (出站)
func (h *udpHandler) ReceiveTo(conn core.UDPConn, data []byte, addr *net.UDPAddr) error {
	if h.cli.udp == nil {
		return errors.New("UDP not enabled on Hysteria client")
	}
	// 将数据包通过 Hysteria 发送到目标地址
	return h.cli.udp.Send(data, addr.String())
}

// Close 当 lwIP 关闭一个 UDP "连接" 时被调用
func (h *udpHandler) Close(conn core.UDPConn) {
	// 从 map 中移除，清理资源
	h.conns.Range(func(key, value interface{}) bool {
		if value == conn {
			h.conns.Delete(key)
			return false // stop iteration
		}
		return true
	})
}

// processInbound 是 UDP 处理器的主循环，处理来自 Hysteria 的入站包
func (h *udpHandler) processInbound() {
	for pkt := range h.cli.inboundUDPQ {
		// pkt.SrcAddr 是远端服务器地址，例如 "1.1.1.1:53"
		// 我们需要根据这个地址找到对应的 lwIP 连接
		val, ok := h.conns.Load(pkt.SrcAddr.String())
		if !ok {
			// 找不到对应的连接，可能已经关闭，丢弃
			continue
		}
		conn := val.(core.UDPConn)
		// 将收到的数据注入回 lwIP，lwIP 会处理并将其发送给正确的 App
		_, err := conn.WriteFrom(pkt.Data, pkt.SrcAddr)
		if err != nil {
			goLogger(2, "[Go] udpHandler WriteFrom failed: "+err.Error())
		}
	}
}

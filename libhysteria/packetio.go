// libhysteria/packetio.go
package libhysteria

import (
	"errors"
	"net"
	"strconv"
	"sync"

	"github.com/eycorsican/go-tun2socks/core"
)

// 全局只初始化一次
var (
	initOnce sync.Once
	tunDev   *tunDevice
)

// initPacketIO 第一次调用时完成：
// 1. newTunDevice()
// 2. core.NewLWIPStack()
// 3. core.RegisterOutputFn / core.RegisterTCPConnHandler / core.RegisterUDPConnHandler
// 4. 一条 goroutine 处理 tunDev.inChan → stack.Write()
func initPacketIO(cli *ClientBridge) {
	initOnce.Do(func() {
		// 1. 创建一个虚拟 TUN 设备
		tunDev = newTunDevice()

		// 2. 创建并启动 lwIP 栈
		stack := core.NewLWIPStack()

		// 3. 注册“下行包输出”回调：lwIP 生成一个下行 IP 包，会调用此处，把 data 推入 tunDev.outChan
		core.RegisterOutputFn(func(data []byte) (int, error) {
			select {
			case tunDev.outChan <- data:
				return len(data), nil
			case <-tunDev.closeCh:
				return 0, errors.New("tun device closed")
			}
		})

		// 4. 注册 TCP 连接回调：当 lwIP 要发起 TCP 连接时，调用 tcpHandler.Handle 去用 Hysteria 建立到目标的连接
		core.RegisterTCPConnHandler(&tcpHandler{cli: cli})

		// 5. 注册 UDP 连接回调：当 lwIP 要发起 UDP 连接或发送 UDP 数据时，调用 udpHandler.Connect/ReceiveTo
		core.RegisterUDPConnHandler(&udpHandler{cli: cli})

		// 6. 上行循环：不停地从 tunDev.inChan 取“来自 iOS TUN 的 IP 包”，调用 stack.Write(pkt) 注入到 lwIP
		go func() {
			for {
				select {
				case <-tunDev.closeCh:
					return
				default:
					pkt, err := tunDev.ReadPacket()
					if err != nil {
						return
					}
					_, _ = stack.Write(pkt)
				}
			}
		}()

		// 注：lwIP 的定时器已经在 NewLWIPStack() 内部启动
	})
}

// WriteFromTun 将“来自 iOS TUN 的 IP 包”写给 lwIP
func (c *ClientBridge) WriteFromTun(pkt []byte) error {
	initPacketIO(c)

	buf := make([]byte, len(pkt))
	copy(buf, pkt)

	select {
	case tunDev.inChan <- buf:
		return nil
	case <-tunDev.closeCh:
		return errors.New("tun device closed")
	}
}

// ReadToTun 从 tunDev.outChan 拉一个下行 IP 包，返回给 iOS TUN
func (c *ClientBridge) ReadToTun(out []byte) (int, error) {
	initPacketIO(c)

	select {
	case pkt := <-tunDev.outChan:
		if len(pkt) > len(out) {
			return 0, errors.New("output buffer too small")
		}
		copy(out, pkt)
		return len(pkt), nil
	case <-tunDev.closeCh:
		return 0, errors.New("tun device closed")
	}
}

// Close 优先关闭 tunDevice，然后关闭 Hysteria 客户端
func (c *ClientBridge) Close() error {
	if tunDev != nil {
		tunDev.Close()
	}
	return c.cl.Close()
}

//
// —— TCP 处理 Handler ——
//

// tcpHandler 实现 core.TCPConnHandler
type tcpHandler struct {
	cli *ClientBridge
}

// Handle 当 lwIP 内部想与目标服务器 (target *net.TCPAddr) 建立 TCP 连接时被调用
func (h *tcpHandler) Handle(conn net.Conn, target *net.TCPAddr) error {
	hostPort := net.JoinHostPort(target.IP.String(), strconv.Itoa(target.Port))
	hyConn, err := h.cli.cl.TCP(hostPort)
	if err != nil {
		return err
	}

	// lwIP → Hysteria
	go func() {
		buf := make([]byte, 65535)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				_ = conn.Close()
				return
			}
			if n > 0 {
				_, _ = hyConn.Write(buf[:n])
			}
		}
	}()

	// Hysteria → lwIP
	go func() {
		buf := make([]byte, 65535)
		for {
			n, err := hyConn.Read(buf)
			if err != nil {
				_ = conn.Close()
				return
			}
			if n > 0 {
				_, _ = conn.Write(buf[:n])
			}
		}
	}()

	return nil
}

//
// —— UDP 处理 Handler ——
//

// udpHandler 实现 core.UDPConnHandler
type udpHandler struct {
	cli *ClientBridge
}

// Connect 当 lwIP 需要“打开一个新的 UDP 会话”时调用。
// target 为 nil 表示只是绑定本地端口，否则表示要与目标 target *net.UDPAddr 通信。
func (h *udpHandler) Connect(conn core.UDPConn, target *net.UDPAddr) error {
	if target == nil {
		// 仅绑定本地端口，lwIP 后续会在 ReceiveTo 中把数据发给 target
		return nil
	}
	hyConn, err := h.cli.cl.UDP()
	if err != nil {
		return err
	}

	// Hysteria → lwIP：远端回包写回给 lwIP
	go func() {
		for {
			data, raddr, err := hyConn.Receive()
			if err != nil {
				return
			}
			pkt := buildUDPPacket(raddr, data)
			_, _ = conn.WriteFrom(pkt, nil)
		}
	}()
	return nil
}

// ReceiveTo 当 lwIP 想“从 conn 发送 UDP 数据到 target”时调用
func (h *udpHandler) ReceiveTo(conn core.UDPConn, data []byte, addr *net.UDPAddr) error {
	hostPort := net.JoinHostPort(addr.IP.String(), strconv.Itoa(addr.Port))
	if h.cli.udp == nil {
		return errors.New("UDP not enabled")
	}
	return h.cli.udp.Send(data, hostPort)
}

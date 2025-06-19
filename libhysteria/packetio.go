// packetio.go
package libhysteria

import (
	"errors"
	"io"
	"net"
	"strconv"
	"sync"

	"github.com/eycorsican/go-tun2socks/core"
)

var (
	once   sync.Once
	tunDev *tunDevice
)

func initPacketIO(c *ClientBridge) {
	once.Do(func() {
		tunDev = newTunDevice()
		stack := core.NewLWIPStack()

		core.RegisterOutputFn(func(p []byte) (int, error) {
			return len(p), tunDev.WriteToOutChan(p)
		})

		core.RegisterTCPConnHandler(&tcpHandler{c})
		udph := newUDPHandler(c)
		core.RegisterUDPConnHandler(udph)
		// 启动 UDP 输入循环
		go udph.loop()

		// 启动 TUN 输入循环
		go func() {
			for {
				p, err := tunDev.ReadFromInChan()
				if err != nil {
					// tunDevice 已关闭
					return
				}
				_, _ = stack.Write(p)
			}
		}()
	})
}

/* ---------- TUN <-> ClientBridge (已修复) ---------- */

func (c *ClientBridge) WriteFromTun(p []byte) error {
	initPacketIO(c)
	return tunDev.WriteToInChan(append([]byte(nil), p...))
}
func (c *ClientBridge) ReadToTun(out []byte) (int, error) {
	initPacketIO(c)
	p, err := tunDev.ReadFromOutChan()
	if err != nil {
		return 0, err
	}
	if len(p) > len(out) {
		return 0, errors.New("buf too small")
	}
	return copy(out, p), nil
}

// Close 方法更加健壮
func (c *ClientBridge) Close() error {
	if tunDev != nil {
		tunDev.Close()
	}
	// 重置 sync.Once 以便下次连接可以重新初始化
	once = sync.Once{}
	if c.cl == nil {
		return nil
	}
	return c.cl.Close()
}

/* ---------- TCP (保持不变) ---------- */

type tcpHandler struct{ c *ClientBridge }

func (h *tcpHandler) Handle(conn net.Conn, target *net.TCPAddr) error {
	dst := net.JoinHostPort(target.IP.String(), strconv.Itoa(target.Port))
	r, err := h.c.cl.TCP(dst)
	if err != nil {
		_ = conn.Close()
		return err
	}
	go func() {
		defer r.Close()
		defer conn.Close()
		io.Copy(r, conn)
	}()
	go func() {
		defer r.Close()
		defer conn.Close()
		io.Copy(conn, r)
	}()
	return nil
}

/* ---------- UDP (已修复) ---------- */

type udpHandler struct {
	c     *ClientBridge
	conns sync.Map
}

func newUDPHandler(c *ClientBridge) *udpHandler { return &udpHandler{c: c} }

func (h *udpHandler) Connect(conn core.UDPConn, dst *net.UDPAddr) error {
	h.conns.Store(dst.String(), conn)
	return nil
}

// ReceiveTo 方法现在是防崩溃的
func (h *udpHandler) ReceiveTo(conn core.UDPConn, b []byte, a *net.UDPAddr) error {
	// --- 核心修复 ---
	// 在使用 c.udp 之前，必须检查它是否为 nil
	if h.c.udp == nil {
		// 如果 UDP 不可用，则通知 tun2socks 连接已关闭
		return errors.New("UDP session is not available")
	}
	return h.c.udp.Send(b, a.String())
}

func (h *udpHandler) Close(conn core.UDPConn) {
	h.conns.Range(func(k, v any) bool {
		if v == conn {
			h.conns.Delete(k)
			return false // 停止遍历
		}
		return true // 继续遍历
	})
}

// loop 方法现在能优雅地处理 UDP 不可用的情况
func (h *udpHandler) loop() {
	// --- 核心修复 ---
	// 从通道读取数据。如果通道被关闭（在 udpReader 退出或 Connect 阶段），
	// 这个循环会自动结束，goroutine 会被回收。
	for p := range h.c.inboundUDPQ {
		if p == nil {
			continue
		}
		if v, ok := h.conns.Load(p.SrcAddr.String()); ok {
			v.(core.UDPConn).WriteFrom(p.Data, p.SrcAddr)
		}
	}
	goLogger(0, "udpHandler loop finished.")
}

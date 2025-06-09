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
		go udph.loop()

		go func() {
			for {
				p, err := tunDev.ReadFromInChan()
				if err != nil {
					return
				}
				_, _ = stack.Write(p)
			}
		}()
	})
}

/* ---------- TUN <-> ClientBridge ---------- */

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
func (c *ClientBridge) Close() error {
	if tunDev != nil {
		tunDev.Close()
	}
	once = sync.Once{}
	return c.cl.Close()
}

/* ---------- TCP ---------- */

type tcpHandler struct{ c *ClientBridge }

func (h *tcpHandler) Handle(conn net.Conn, target *net.TCPAddr) error {
	dst := net.JoinHostPort(target.IP.String(), strconv.Itoa(target.Port))
	r, err := h.c.cl.TCP(dst)
	if err != nil {
		conn.Close()
		return err
	}
	go io.Copy(conn, r)
	go io.Copy(r, conn)
	return nil
}

/* ---------- UDP ---------- */

type udpHandler struct {
	c     *ClientBridge
	conns sync.Map
}

func newUDPHandler(c *ClientBridge) *udpHandler { return &udpHandler{c: c} }

func (h *udpHandler) Connect(conn core.UDPConn, dst *net.UDPAddr) error {
	h.conns.Store(dst.String(), conn)
	return nil
}
func (h *udpHandler) ReceiveTo(conn core.UDPConn, b []byte, a *net.UDPAddr) error {
	return h.c.udp.Send(b, a.String())
}
func (h *udpHandler) Close(conn core.UDPConn) {
	h.conns.Range(func(k, v any) bool {
		if v == conn {
			h.conns.Delete(k)
			return false
		}
		return true
	})
}
func (h *udpHandler) loop() {
	for p := range h.c.inboundUDPQ {
		if v, ok := h.conns.Load(p.SrcAddr.String()); ok {
			v.(core.UDPConn).WriteFrom(p.Data, p.SrcAddr)
		}
	}
}

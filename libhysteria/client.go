package libhysteria

import (
	"encoding/json"
	"net"

	hy "github.com/apernet/hysteria/core/v2/client"
	"github.com/apernet/hysteria/extras/v2/obfs"
)

/* ---------- JSON ---------- */

type JSONTLSConfig struct {
	SNI string `json:"sni,omitempty"`
}

type JSONConfig struct {
	Server   string        `json:"server"`
	Auth     string        `json:"auth"`
	PSK      string        `json:"psk,omitempty"`
	FastOpen bool          `json:"fastOpen,omitempty"`
	TLS      JSONTLSConfig `json:"tls,omitempty"`
}

/* ---------- 桥 ---------- */

type InboundUDPPacket struct {
	Data    []byte
	SrcAddr *net.UDPAddr
}

type ClientBridge struct {
	cl          hy.Client
	udp         hy.HyUDPConn
	inboundUDPQ chan *InboundUDPPacket
}

/* ---------- 构造 ---------- */

func newClientBridge(raw string) (*ClientBridge, error) {
	var jc JSONConfig
	if err := json.Unmarshal([]byte(raw), &jc); err != nil {
		return nil, err
	}

	cfg := &hy.Config{
		Auth:     jc.Auth,
		FastOpen: jc.FastOpen,
		TLSConfig: hy.TLSConfig{
			ServerName: chooseSNI(jc.TLS.SNI, jc.Server), // ← 自动回退 SNI
		},
	}

	rAddr, err := net.ResolveUDPAddr("udp", jc.Server)
	if err != nil {
		return nil, err
	}
	cfg.ServerAddr = rAddr

	var connFactory hy.ConnFactory = &udpConnFactory{}
	if jc.PSK != "" {
		ob, err := obfs.NewSalamanderObfuscator([]byte(jc.PSK))
		if err != nil {
			return nil, err
		}
		connFactory = &obfsFactory{Obfs: ob}
	}
	cfg.ConnFactory = connFactory

	rc, err := hy.NewReconnectableClient(
		func() (*hy.Config, error) { return cfg, nil },
		nil,
		true,
	)
	if err != nil {
		return nil, err
	}

	cb := &ClientBridge{
		cl:          rc,
		inboundUDPQ: make(chan *InboundUDPPacket, 4096),
	}

	if udpConn, err := rc.UDP(); err == nil {
		cb.udp = udpConn
		go cb.udpReader()
	}
	return cb, nil
}

/* ---------- UDP Reader ---------- */

func (c *ClientBridge) udpReader() {
	for {
		data, srcAddrStr, err := c.udp.Receive()
		if err != nil {
			close(c.inboundUDPQ)
			return
		}
		srcAddr, err := net.ResolveUDPAddr("udp", srcAddrStr)
		if err != nil {
			continue
		}
		select {
		case c.inboundUDPQ <- &InboundUDPPacket{Data: data, SrcAddr: srcAddr}:
		default:
		}
	}
}

/* ---------- 连接工厂 ---------- */

type udpConnFactory struct{}

func (u *udpConnFactory) New(addr net.Addr) (net.PacketConn, error) {
	return net.ListenUDP("udp", nil)
}

type obfsFactory struct{ Obfs obfs.Obfuscator }

func (f *obfsFactory) New(addr net.Addr) (net.PacketConn, error) {
	p, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}
	return obfs.WrapPacketConn(p, f.Obfs), nil
}

/* ---------- helper: 自动 SNI ---------- */

func chooseSNI(sni, server string) string {
	if sni != "" {
		return sni
	}
	host, _, _ := net.SplitHostPort(server)
	return host
}

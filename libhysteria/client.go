package libhysteria

import (
	"encoding/json"
	"net"

	hy "github.com/apernet/hysteria/core/v2/client"
	"github.com/apernet/hysteria/extras/v2/obfs"
)

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

// InboundUDPPacket 用于在 Go 内部传递入站的 UDP 包
type InboundUDPPacket struct {
	Data    []byte
	SrcAddr *net.UDPAddr
}

type ClientBridge struct {
	cl          hy.Client
	udp         hy.HyUDPConn
	inboundUDPQ chan *InboundUDPPacket
}

func newClientBridge(raw string) (*ClientBridge, error) {
	var jc JSONConfig
	if err := json.Unmarshal([]byte(raw), &jc); err != nil {
		goLogger(2, "[Go] JSON unmarshal failed: "+err.Error())
		return nil, err
	}
	goLogger(0, "[Go] Parsed SNI from config: "+jc.TLS.SNI)

	cfg := &hy.Config{
		Auth:     jc.Auth,
		FastOpen: jc.FastOpen,
		TLSConfig: hy.TLSConfig{
			ServerName: jc.TLS.SNI,
		},
	}

	rAddr, err := net.ResolveUDPAddr("udp", jc.Server)
	if err != nil {
		// goLogger(2, "[Go] ResolveUDPAddr failed: "+err.Error())
		return nil, err
	}
	cfg.ServerAddr = rAddr

	var connFactory hy.ConnFactory = &udpConnFactory{}
	if jc.PSK != "" {
		ob, err := obfs.NewSalamanderObfuscator([]byte(jc.PSK))
		if err != nil {
			// goLogger(2, "[Go] NewSalamanderObfuscator failed: "+err.Error())
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
		// goLogger(2, "[Go] NewReconnectableClient failed: "+err.Error())
		return nil, err
	}

	cb := &ClientBridge{
		cl:          rc,
		inboundUDPQ: make(chan *InboundUDPPacket, 4096),
	}

	if udpConn, err := rc.UDP(); err == nil {
		cb.udp = udpConn
		// 启动 UDP 读取器，它会将收到的包放入 inboundUDPQ
		go cb.udpReader()
	} else {
		// goLogger(2, "[Go] Failed to enable UDP: "+err.Error())
	}
	return cb, nil
}

// udpReader 从 Hysteria 连接读取入站 UDP 包，并将其放入队列
func (c *ClientBridge) udpReader() {
	for {
		data, srcAddrStr, err := c.udp.Receive()
		if err != nil {
			// goLogger(2, "[Go] udp.Receive() error: "+err.Error())
			close(c.inboundUDPQ)
			return
		}
		srcAddr, err := net.ResolveUDPAddr("udp", srcAddrStr)
		if err != nil {
			// goLogger(2, "[Go] ResolveUDPAddr for inbound packet failed: "+err.Error())
			continue
		}
		pkt := &InboundUDPPacket{
			Data:    data,
			SrcAddr: srcAddr,
		}
		select {
		case c.inboundUDPQ <- pkt:
		default:
			// 队列满，丢弃包
		}
	}
}

type udpConnFactory struct{}

func (u *udpConnFactory) New(addr net.Addr) (net.PacketConn, error) {
	return net.ListenUDP("udp", nil)
}

type obfsFactory struct {
	Obfs obfs.Obfuscator
}

func (f *obfsFactory) New(addr net.Addr) (net.PacketConn, error) {
	p, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}
	return obfs.WrapPacketConn(p, f.Obfs), nil
}

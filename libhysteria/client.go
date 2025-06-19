package libhysteria

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	hy "github.com/apernet/hysteria/core/v2/client"
	"github.com/apernet/hysteria/extras/v2/obfs"
)

/* ---------- JSON 结构体定义 ---------- */

type JSONTLSConfig struct {
	SNI string `json:"sni,omitempty"`
}

type JSONTunConfig struct {
	Name        string   `json:"name"`
	MTU         int      `json:"mtu"`
	Address     []string `json:"address"`
	Route       []string `json:"route"`
	IPv4Exclude []string `json:"ipv4Exclude"`
}

type JSONBandwidthConfig struct {
	Up   string `json:"up"`
	Down string `json:"down"`
}

type JSONConfig struct {
	Server    string              `json:"server"`
	Auth      string              `json:"auth"`
	TLS       JSONTLSConfig       `json:"tls,omitempty"`
	Bandwidth JSONBandwidthConfig `json:"bandwidth"`
	Tun       JSONTunConfig       `json:"tun"`
	PSK       string              `json:"psk,omitempty"`
	FastOpen  bool                `json:"fastOpen,omitempty"`
	ALPN      string              `json:"alpn,omitempty"`
}

/* ---------- 桥接核心结构体 ---------- */

type InboundUDPPacket struct {
	Data    []byte
	SrcAddr *net.UDPAddr
}

type ClientBridge struct {
	cl          hy.Client
	udp         hy.HyUDPConn
	inboundUDPQ chan *InboundUDPPacket
	tunConfig   JSONTunConfig
	rawConfig   string
	state       atomic.Value
}

/* ---------- 构造与连接 (with Detailed Logging) ---------- */

func newClientBridge(raw string) (*ClientBridge, error) {
	goLogger(0, "[ClientBridge] newClientBridge called.")
	cb := &ClientBridge{
		inboundUDPQ: make(chan *InboundUDPPacket, 4096),
		rawConfig:   raw,
	}
	cb.state.Store("connecting")
	goLogger(1, "[ClientBridge] newClientBridge: State set to 'connecting'.")
	return cb, nil
}

func parseBandwidth(s string) uint64 {
	s = strings.ToLower(strings.TrimSpace(s))
	var multiplier uint64 = 1
	if strings.HasSuffix(s, "kbps") {
		multiplier = 1000 / 8
		s = strings.TrimSuffix(s, "kbps")
	} else if strings.HasSuffix(s, "mbps") {
		multiplier = 1000 * 1000 / 8
		s = strings.TrimSuffix(s, "mbps")
	} else if strings.HasSuffix(s, "gbps") {
		multiplier = 1000 * 1000 * 1000 / 8
		s = strings.TrimSuffix(s, "gbps")
	} else if strings.HasSuffix(s, "k") || strings.HasSuffix(s, "kb") {
		multiplier = 1024
		s = strings.TrimSuffix(s, "k")
		s = strings.TrimSuffix(s, "b")
	} else if strings.HasSuffix(s, "m") || strings.HasSuffix(s, "mb") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "m")
		s = strings.TrimSuffix(s, "b")
	} else if strings.HasSuffix(s, "g") || strings.HasSuffix(s, "gb") {
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "g")
		s = strings.TrimSuffix(s, "b")
	}
	val, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return val * multiplier
}

func (c *ClientBridge) Connect() {
	goLogger(0, "[ClientBridge] Connect: Starting connection process in goroutine.")
	var jc JSONConfig
	if err := json.Unmarshal([]byte(c.rawConfig), &jc); err != nil {
		goLogger(2, "[ClientBridge] Connect: json.Unmarshal failed: "+err.Error())
		c.state.Store("failed: config error")
		return
	}
	goLogger(1, "[ClientBridge] Connect: JSON config parsed successfully.")
	c.tunConfig = jc.Tun

	goLogger(1, fmt.Sprintf("[ClientBridge] Connect: Resolving UDP address for %s.", jc.Server))
	rAddr, err := net.ResolveUDPAddr("udp", jc.Server)
	if err != nil {
		goLogger(2, "[ClientBridge] Connect: net.ResolveUDPAddr failed: "+err.Error())
		c.state.Store("failed: dns error")
		return
	}
	goLogger(1, fmt.Sprintf("[ClientBridge] Connect: Resolved to %s.", rAddr.String()))

	var connFactory hy.ConnFactory = &udpConnFactory{}
	if jc.PSK != "" {
		goLogger(1, "[ClientBridge] Connect: PSK found, creating Salamander obfuscator.")
		ob, err := obfs.NewSalamanderObfuscator([]byte(jc.PSK))
		if err != nil {
			goLogger(2, "[ClientBridge] Connect: NewSalamanderObfuscator failed: "+err.Error())
			c.state.Store("failed: obfs error")
			return
		}
		connFactory = &obfsFactory{Obfs: ob}
	}

	bwUp := parseBandwidth(jc.Bandwidth.Up)
	bwDown := parseBandwidth(jc.Bandwidth.Down)
	goLogger(1, fmt.Sprintf("[ClientBridge] Connect: Parsed bandwidth: Up=%d Bps, Down=%d Bps.", bwUp, bwDown))

	cfg := &hy.Config{
		ConnFactory:     connFactory,
		ServerAddr:      rAddr,
		Auth:            jc.Auth,
		TLSConfig:       hy.TLSConfig{ServerName: chooseSNI(jc.TLS.SNI, jc.Server)},
		BandwidthConfig: hy.BandwidthConfig{MaxTx: bwUp, MaxRx: bwDown},
		QUICConfig:      hy.QUICConfig{MaxIdleTimeout: 30 * time.Second, KeepAlivePeriod: 10 * time.Second},
		FastOpen:        jc.FastOpen,
	}

	goLogger(1, "[ClientBridge] Connect: Calling hy.NewClient (this may block).")
	client, handshakeInfo, err := hy.NewClient(cfg)
	if err != nil {
		goLogger(2, "[ClientBridge] Connect: hy.NewClient failed: "+err.Error())
		c.state.Store("failed: " + err.Error())
		return
	}
	goLogger(0, fmt.Sprintf("[ClientBridge] Connect: Control plane connected. UDPEnabled: %t.", handshakeInfo.UDPEnabled))
	c.cl = client

	goLogger(1, "[ClientBridge] Connect: Warming up data path by dialing 1.1.1.1:53.")
	testConn, err := client.TCP("1.1.1.1:53")
	if err != nil {
		goLogger(2, "[ClientBridge] Connect: Data path warm-up failed: "+err.Error())
		c.state.Store("failed: data path warm-up error")
		_ = client.Close()
		return
	}
	_ = testConn.Close()
	goLogger(1, "[ClientBridge] Connect: Data path warm-up successful.")

	if handshakeInfo.UDPEnabled {
		goLogger(1, "[ClientBridge] Connect: UDP enabled, calling client.UDP().")
		udpConn, err := client.UDP()
		if err != nil {
			goLogger(2, "[ClientBridge] Connect: client.UDP() failed: "+err.Error())
			c.state.Store("failed: udp setup error")
			_ = client.Close()
			return
		}
		goLogger(1, "[ClientBridge] Connect: UDP session created. Starting udpReader goroutine.")
		c.udp = udpConn
		go c.udpReader()
	} else {
		goLogger(1, "[ClientBridge] Connect: UDP not enabled by server. Closing inbound UDP queue.")
		close(c.inboundUDPQ)
	}

	goLogger(0, "[ClientBridge] Connect: Setup complete. State changed to 'connected'.")
	c.state.Store("connected")
}

func (c *ClientBridge) GetState() string {
	return c.state.Load().(string)
}

/* ---------- UDP Reader & 其他辅助函数 (with Detailed Logging) ---------- */

func (c *ClientBridge) udpReader() {
	goLogger(1, "[ClientBridge] udpReader: Goroutine started.")
	defer func() {
		goLogger(1, "[ClientBridge] udpReader: Closing inbound UDP queue and exiting goroutine.")
		close(c.inboundUDPQ)
	}()
	for {
		goLogger(1, "[ClientBridge] udpReader: Calling c.udp.Receive() (blocking).")
		data, srcAddrStr, err := c.udp.Receive()
		if err != nil {
			goLogger(2, "[ClientBridge] udpReader: c.udp.Receive() error: "+err.Error())
			return
		}
		goLogger(1, fmt.Sprintf("[ClientBridge] udpReader: Received %d bytes from %s.", len(data), srcAddrStr))

		srcAddr, err := net.ResolveUDPAddr("udp", srcAddrStr)
		if err != nil {
			goLogger(2, fmt.Sprintf("[ClientBridge] udpReader: Failed to resolve source addr %s: %s", srcAddrStr, err.Error()))
			continue
		}

		select {
		case c.inboundUDPQ <- &InboundUDPPacket{Data: data, SrcAddr: srcAddr}:
			goLogger(1, "[ClientBridge] udpReader: Packet sent to inboundUDPQ.")
		default:
			goLogger(2, "[ClientBridge] udpReader: inboundUDPQ is full, dropping packet!")
		}
	}
}

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

func chooseSNI(sni, server string) string {
	if sni != "" {
		return sni
	}
	host, _, err := net.SplitHostPort(server)
	if err != nil {
		return server
	}
	return host
}

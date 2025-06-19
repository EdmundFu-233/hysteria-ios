// client.go

package libhysteria

import (
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	hy "github.com/apernet/hysteria/core/v2/client"
	"github.com/apernet/hysteria/extras/v2/obfs"
)

/* ---------- JSON 结构体定义 (保持不变) ---------- */

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

/* ---------- 桥接核心结构体 (保持不变) ---------- */

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

/* ---------- 构造与连接 (已修复) ---------- */

func newClientBridge(raw string) (*ClientBridge, error) {
	cb := &ClientBridge{
		inboundUDPQ: make(chan *InboundUDPPacket, 4096),
		rawConfig:   raw,
	}
	cb.state.Store("connecting")
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
	goLogger(0, "Bridge.Connect: Starting blocking connection process...")
	var jc JSONConfig
	if err := json.Unmarshal([]byte(c.rawConfig), &jc); err != nil {
		goLogger(2, "Connect: json.Unmarshal failed: "+err.Error())
		c.state.Store("failed: config error")
		return
	}
	c.tunConfig = jc.Tun

	rAddr, err := net.ResolveUDPAddr("udp", jc.Server)
	if err != nil {
		goLogger(2, "Connect: net.ResolveUDPAddr for '"+jc.Server+"' failed: "+err.Error())
		c.state.Store("failed: dns error")
		return
	}

	var connFactory hy.ConnFactory = &udpConnFactory{}
	if jc.PSK != "" {
		ob, err := obfs.NewSalamanderObfuscator([]byte(jc.PSK))
		if err != nil {
			goLogger(2, "Connect: NewSalamanderObfuscator failed: "+err.Error())
			c.state.Store("failed: obfs error")
			return
		}
		connFactory = &obfsFactory{Obfs: ob}
	}

	bwUp := parseBandwidth(jc.Bandwidth.Up)
	bwDown := parseBandwidth(jc.Bandwidth.Down)
	goLogger(0, "Parsed bandwidth: Up="+strconv.FormatUint(bwUp, 10)+" Bps, Down="+strconv.FormatUint(bwDown, 10)+" Bps")

	cfg := &hy.Config{
		ConnFactory: connFactory,
		ServerAddr:  rAddr,
		Auth:        jc.Auth,
		TLSConfig: hy.TLSConfig{
			ServerName: chooseSNI(jc.TLS.SNI, jc.Server),
		},
		BandwidthConfig: hy.BandwidthConfig{
			MaxTx: bwUp,
			MaxRx: bwDown,
		},
		QUICConfig: hy.QUICConfig{
			MaxIdleTimeout:  30 * time.Second,
			KeepAlivePeriod: 10 * time.Second,
		},
		FastOpen: jc.FastOpen,
	}

	client, handshakeInfo, err := hy.NewClient(cfg)
	if err != nil {
		goLogger(2, "Connect: hy.NewClient failed: "+err.Error())
		c.state.Store("failed: " + err.Error())
		return
	}
	goLogger(0, "Bridge.Connect: Control plane connected. Handshake Info: UDPEnabled="+strconv.FormatBool(handshakeInfo.UDPEnabled))
	c.cl = client

	goLogger(0, "Bridge.Connect: Warming up data path by dialing 1.1.1.1:53...")
	testConn, err := client.TCP("1.1.1.1:53")
	if err != nil {
		goLogger(2, "Connect: Data path warm-up failed at client.TCP(): "+err.Error())
		c.state.Store("failed: data path warm-up error")
		_ = client.Close()
		return
	}
	_ = testConn.Close()
	goLogger(0, "Bridge.Connect: Data path warm-up successful.")

	// --- 核心修复 ---
	// 严格处理 UDP 会话建立
	if handshakeInfo.UDPEnabled {
		goLogger(0, "Bridge.Connect: UDP is enabled by server, setting up UDP session...")
		udpConn, err := client.UDP()
		if err != nil {
			// 如果服务器说支持UDP，但我们无法建立UDP会话，这是一个致命错误
			goLogger(2, "Connect: client.UDP() failed: "+err.Error())
			c.state.Store("failed: udp setup error")
			_ = client.Close()
			return // 中止连接过程
		}
		goLogger(0, "Bridge.Connect: UDP session created successfully.")
		c.udp = udpConn
		go c.udpReader()
	} else {
		goLogger(0, "Bridge.Connect: UDP is not enabled by server, will run in TCP-only mode.")
		// 确保 inboundUDPQ 被关闭，以防止下游的 goroutine 永久阻塞
		close(c.inboundUDPQ)
	}

	goLogger(0, "Bridge.Connect: Hysteria client is fully connected and data path is ready.")
	c.state.Store("connected")
}

func (c *ClientBridge) GetState() string {
	return c.state.Load().(string)
}

/* ---------- UDP Reader & 其他辅助函数 (已修复) ---------- */

func (c *ClientBridge) udpReader() {
	// 当这个 goroutine 退出时（例如，连接断开），关闭通道
	// 这会通知消费者（udpHandler.loop）没有更多的数据了
	defer close(c.inboundUDPQ)
	for {
		data, srcAddrStr, err := c.udp.Receive()
		if err != nil {
			goLogger(1, "udpReader closing: "+err.Error())
			return // 退出循环, defer 将被执行
		}
		srcAddr, err := net.ResolveUDPAddr("udp", srcAddrStr)
		if err != nil {
			continue
		}
		select {
		case c.inboundUDPQ <- &InboundUDPPacket{Data: data, SrcAddr: srcAddr}:
		default:
			// 如果通道满了，丢弃数据包以避免阻塞
			goLogger(1, "udpReader: inbound UDP queue full, dropping packet.")
		}
	}
}

// udpConnFactory, obfsFactory, chooseSNI 保持不变
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

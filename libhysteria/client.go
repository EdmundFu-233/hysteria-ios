package libhysteria

import (
	"encoding/json"
	"net"
	"strconv"     // 确保导入 strconv
	"sync/atomic" // 确保导入 atomic

	hy "github.com/apernet/hysteria/core/v2/client"
	"github.com/apernet/hysteria/extras/v2/obfs"
)

/* ---------- JSON 结构体定义 ---------- */
// 这些结构体与 Swift 端构建的 JSON 配置完全对应

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
	state       atomic.Value // 安全地在 goroutine 之间共享状态
}

/* ---------- 构造与连接 ---------- */

// newClientBridge 仅执行最轻量的初始化，并将状态设置为 "connecting"
func newClientBridge(raw string) (*ClientBridge, error) {
	cb := &ClientBridge{
		inboundUDPQ: make(chan *InboundUDPPacket, 4096),
		rawConfig:   raw,
	}
	cb.state.Store("connecting")
	return cb, nil
}

// Connect 方法在后台 goroutine 中执行，它包含阻塞式的连接逻辑
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

	// 使用 hy.Config 来配置 NewClient
	// 注意：Hysteria v2 的 core client 不直接处理带宽字符串，这通常在应用层处理。
	// 我们在这里保持配置完整性，但 core client 会忽略它。
	cfg := &hy.Config{
		ConnFactory: connFactory,
		ServerAddr:  rAddr,
		Auth:        jc.Auth,
		TLSConfig: hy.TLSConfig{
			ServerName: chooseSNI(jc.TLS.SNI, jc.Server),
		},
		FastOpen: jc.FastOpen,
		// QUICConfig 等其他字段可以使用默认值
	}

	// hy.NewClient 是一个阻塞式调用，它会完成握手
	client, handshakeInfo, err := hy.NewClient(cfg)
	if err != nil {
		goLogger(2, "Connect: hy.NewClient failed: "+err.Error())
		c.state.Store("failed: " + err.Error())
		return
	}

	goLogger(0, "Bridge.Connect: hy.NewClient successful. Handshake Info: UDPEnabled="+strconv.FormatBool(handshakeInfo.UDPEnabled))
	c.cl = client

	if handshakeInfo.UDPEnabled {
		udpConn, err := client.UDP()
		if err != nil {
			goLogger(2, "Connect: client.UDP() failed: "+err.Error())
			// UDP 失败不一定是致命错误，但需要记录
		} else {
			c.udp = udpConn
			go c.udpReader()
		}
	}

	goLogger(0, "Bridge.Connect: Hysteria client truly connected.")
	c.state.Store("connected")
}

// GetState 原子地读取当前连接状态
func (c *ClientBridge) GetState() string {
	return c.state.Load().(string)
}

/* ---------- UDP Reader & 其他辅助函数 ---------- */

// udpReader 从 Hysteria 客户端读取 UDP 包并放入队列
func (c *ClientBridge) udpReader() {
	for {
		data, srcAddrStr, err := c.udp.Receive()
		if err != nil {
			// 连接关闭时会出错，这是正常的退出路径
			goLogger(1, "udpReader closing: "+err.Error())
			close(c.inboundUDPQ)
			return
		}
		srcAddr, err := net.ResolveUDPAddr("udp", srcAddrStr)
		if err != nil {
			continue // 忽略无法解析的源地址
		}
		select {
		case c.inboundUDPQ <- &InboundUDPPacket{Data: data, SrcAddr: srcAddr}:
		default:
			// 如果队列满了，丢弃数据包以防阻塞
		}
	}
}

// udpConnFactory 是 Hysteria 的标准 UDP 连接工厂
type udpConnFactory struct{}

func (u *udpConnFactory) New(addr net.Addr) (net.PacketConn, error) {
	return net.ListenUDP("udp", nil)
}

// obfsFactory 是用于混淆的连接工厂
type obfsFactory struct{ Obfs obfs.Obfuscator }

func (f *obfsFactory) New(addr net.Addr) (net.PacketConn, error) {
	p, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}
	return obfs.WrapPacketConn(p, f.Obfs), nil
}

// chooseSNI 根据配置或服务器地址选择 SNI
func chooseSNI(sni, server string) string {
	if sni != "" {
		return sni
	}
	host, _, err := net.SplitHostPort(server)
	if err != nil {
		// 如果 server 不含端口（虽然不规范），直接使用它
		return server
	}
	return host
}

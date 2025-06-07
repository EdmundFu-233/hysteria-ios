// libhysteria/client.go
package libhysteria

import (
    "encoding/json"
    "encoding/binary"
    "net"
    "strconv"

    hy "github.com/apernet/hysteria/core/v2/client"
    "github.com/apernet/hysteria/extras/v2/obfs"
)

type JSONConfig struct {
    Server   string `json:"server"`           // 格式："host:port"
    Auth     string `json:"auth"`             // Hysteria 认证字段
    PSK      string `json:"psk,omitempty"`    // 可选 PSK 用于 Salamander 混淆
    FastOpen bool   `json:"fastOpen,omitempty"`
}

type ClientBridge struct {
    cl    hy.Client
    udp   hy.HyUDPConn
    downQ chan []byte
}

// newClientBridge 解析 JSONConfig，并创建一个 lazy 模式的 Hysteria 客户端。
// 如果服务器支持 UDP，就启动一个 goroutine 持续从 Hysteria 收取 UDP 回包到 downQ。
func newClientBridge(raw string) (*ClientBridge, error) {
    var jc JSONConfig
    if err := json.Unmarshal([]byte(raw), &jc); err != nil {
        return nil, err
    }

    cfg := &hy.Config{
        Auth:     jc.Auth,
        FastOpen: jc.FastOpen,
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
        cl:    rc,
        downQ: make(chan []byte, 2048),
    }

    if udpConn, err := rc.UDP(); err == nil {
        cb.udp = udpConn
        go cb.udpReader()
    }
    return cb, nil
}

// udpReader 不断从 Hysteria UDP 会话读取原始载荷，并封装成 IP+UDP 包推到 downQ
func (c *ClientBridge) udpReader() {
    for {
        data, addr, err := c.udp.Receive()
        if err != nil {
            return
        }
        pkt := buildUDPPacket(addr, data)
        select {
        case c.downQ <- pkt:
        default:
            // 丢包
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

// buildUDPPacket：把 Hysteria 从远端收到的“裸 UDP 载荷”封装成最简单的 IPv4+UDP 包（不含校验和）。
func buildUDPPacket(dstAddr string, payload []byte) []byte {
    host, portStr, _ := net.SplitHostPort(dstAddr)
    port, _ := strconv.Atoi(portStr)
    ip := net.ParseIP(host).To4()

    header := make([]byte, 20)
    header[0] = 0x45                                        // Version=4, IHL=5
    totalLen := 20 + 8 + len(payload)                       // IP 头 + UDP 头 + 载荷
    binary.BigEndian.PutUint16(header[2:], uint16(totalLen))
    header[8] = 64                                          // TTL
    header[9] = 17                                          // Protocol = UDP
    copy(header[12:16], net.IPv4zero.To4())                 // Src = 0.0.0.0（由 lwIP 覆盖）
    copy(header[16:20], ip)                                 // Dst = 目标 IP

    udp := make([]byte, 8+len(payload))
    binary.BigEndian.PutUint16(udp[0:], uint16(12345))      // SrcPort (随意)
    binary.BigEndian.PutUint16(udp[2:], uint16(port))       // DstPort
    udpLen := uint16(8 + len(payload))
    binary.BigEndian.PutUint16(udp[4:], udpLen)
    // checksum 保留 0
    copy(udp[8:], payload)

    return append(header, udp...)
}


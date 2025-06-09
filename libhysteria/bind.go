package libhysteria

import (
	"errors"
	"fmt"
	"sync"
)

// 全局日志通道
var (
	logChan chan string
	logOnce sync.Once
)

// initLogBuffer 初始化带缓冲的日志通道
func initLogBuffer() {
	logOnce.Do(func() {
		logChan = make(chan string, 100) // 缓冲100条日志
	})
}

// goLogger 将日志写入通道
func goLogger(level int, message string) {
	initLogBuffer()
	// 为日志添加级别前缀
	var levelPrefix string
	switch level {
	case 1:
		levelPrefix = "DEBUG"
	case 2:
		levelPrefix = "ERROR"
	default:
		levelPrefix = "INFO"
	}
	logMsg := fmt.Sprintf("[%s] %s", levelPrefix, message)

	// 非阻塞写入，如果通道满了就丢弃旧日志
	select {
	case logChan <- logMsg:
	default:
		// Channel is full, drop the log to prevent blocking
	}
}

// API 是暴露给 Swift 的唯一入口点。
type API struct{}

var (
	cliMu sync.RWMutex
	cli   *ClientBridge
)

// ==========================================================
//  ↓↓↓ 新增：拉取日志的API方法 ↓↓↓
// ==========================================================

// PollLogs 拉取所有当前缓冲的日志，以换行符分隔。
func (a *API) PollLogs() string {
	initLogBuffer()
	var logs []string
	for {
		select {
		case logMsg := <-logChan:
			logs = append(logs, logMsg)
		default:
			// No more logs in the channel
			if len(logs) > 0 {
				// 在Go端用特殊的分隔符连接，而不是返回数组
				// 避免复杂的类型转换
				return_str := ""
				for i, s := range logs {
					if i > 0 {
						return_str += "\n"
					}
					return_str += s
				}
				return return_str
			}
			return ""
		}
	}
}

// ==========================================================

// Start 启动 Hysteria 客户端。
func (a *API) Start(jsonCfg string) error {
	goLogger(0, "[Go] API.Start called.")
	cb, err := newClientBridge(jsonCfg)
	if err != nil {
		goLogger(2, "[Go] newClientBridge failed: "+err.Error())
		return err
	}
	cliMu.Lock()
	if cli != nil {
		_ = cli.Close()
	}
	cli = cb
	cliMu.Unlock()
	goLogger(0, "[Go] API.Start finished successfully.")
	return nil
}

// Stop 停止 Hysteria 客户端。
func (a *API) Stop() {
	goLogger(0, "[Go] API.Stop called.")
	cliMu.Lock()
	if cli != nil {
		_ = cli.Close()
		cli = nil
	}
	cliMu.Unlock()
}

// WriteTunPacket 将来自 TUN 的数据包写入 Hysteria。
func (a *API) WriteTunPacket(pkt []byte) error {
	cliMu.RLock()
	defer cliMu.RUnlock()
	if cli == nil {
		// goLogger(2, "[Go] WriteTunPacket called but client is nil.")
		return errors.New("client not started")
	}
	// 性能敏感：注释掉频繁的 DEBUG 级日志记录
	// goLogger(1, "[Go] WriteTunPacket writing packet size: "+strconv.Itoa(len(pkt)))
	return cli.WriteFromTun(pkt)
}

// ReadTunPacket 从 Hysteria 读取一个 TUN 数据包。
func (a *API) ReadTunPacket() ([]byte, error) {
	cliMu.RLock()
	defer cliMu.RUnlock()
	if cli == nil {
		// goLogger(2, "[Go] ReadTunPacket called but client is nil.")
		return nil, errors.New("client not started")
	}
	buf := make([]byte, 65535)
	n, err := cli.ReadToTun(buf)
	if err != nil {
		// goLogger(2, "[Go] ReadToTun returned error: "+err.Error())
		return nil, err
	}
	if n > 0 {
		// 性能敏感：注释掉频繁的 DEBUG 级日志记录
		// goLogger(1, "[Go] ReadTunPacket returning packet size: "+strconv.Itoa(n))
		return buf[:n], nil
	}
	return nil, nil
}

// NewAPI 创建并返回一个 API 实例。
func NewAPI() *API {
	return &API{}
}

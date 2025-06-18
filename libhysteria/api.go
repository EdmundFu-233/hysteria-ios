package libhysteria

import (
	"errors"
	"fmt"
	"sync"
)

/* ---------- 日志缓冲 ---------- */

var (
	logChan chan string
	logOnce sync.Once
)

func initLogBuffer() {
	logOnce.Do(func() { logChan = make(chan string, 100) })
}
func goLogger(level int, msg string) {
	initLogBuffer()
	prefix := [...]string{"INFO", "DEBUG", "ERROR"}[level%3]
	select {
	case logChan <- fmt.Sprintf("[%s] %s", prefix, msg):
	default:
	}
}

/* ---------- Swift 暴露的 API ---------- */

type API struct{}

var (
	cliMu sync.RWMutex
	cli   *ClientBridge
)

func (a *API) PollLogs() string {
	initLogBuffer()
	out := ""
	for {
		select {
		case s := <-logChan:
			if out != "" {
				out += "\n"
			}
			out += s
		default:
			return out
		}
	}
}

// --- 核心修改: Start 现在是非阻塞的，在后台启动连接 ---
func (a *API) Start(jsonCfg string) error {
	goLogger(0, "API.Start called")
	cb, err := newClientBridge(jsonCfg)
	if err != nil {
		goLogger(2, "newClientBridge failed: "+err.Error())
		return err
	}

	// 启动一个 goroutine 在后台进行实际的连接
	go cb.Connect()

	cliMu.Lock()
	if cli != nil {
		_ = cli.Close()
	}
	cli = cb
	cliMu.Unlock()
	return nil
}

// --- 新增: 获取客户端状态的方法 ---
func (a *API) GetState() string {
	cliMu.RLock()
	defer cliMu.RUnlock()
	if cli == nil {
		return "stopped"
	}
	return cli.GetState()
}

func (a *API) Stop() {
	goLogger(0, "API.Stop")
	cliMu.Lock()
	if cli != nil {
		_ = cli.Close()
		cli = nil
	}
	cliMu.Unlock()
}

func (a *API) WriteTunPacket(pkt []byte) error {
	cliMu.RLock()
	defer cliMu.RUnlock()
	if cli == nil {
		return errors.New("client not started")
	}
	return cli.WriteFromTun(pkt)
}

func (a *API) ReadTunPacket() ([]byte, error) {
	cliMu.RLock()
	defer cliMu.RUnlock()
	if cli == nil {
		return nil, errors.New("client not started")
	}
	buf := make([]byte, 65535)
	n, err := cli.ReadToTun(buf)
	if err != nil || n == 0 {
		return nil, err
	}
	return buf[:n], nil
}

func NewAPI() *API { return &API{} }

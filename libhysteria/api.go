package libhysteria

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

/* ---------- 日志缓冲 (严格遵循您提供的版本) ---------- */

var (
	logChan chan string
)

var ErrNoData = errors.New("no data")

// goLogger (严格遵循您提供的版本)
func goLogger(level int, msg string) {
	if logChan == nil {
		return
	}
	prefix := [...]string{"INFO", "DEBUG", "ERROR"}[level%3]
	select {
	case logChan <- fmt.Sprintf("[%s] %s", prefix, msg):
	default:
		// Channel is full, drop log.
	}
}

/* ---------- Swift 暴露的 API ---------- */

type API struct{}

var (
	cliMu sync.RWMutex
	cli   *ClientBridge
)

// NewAPI (严格遵循您提供的版本)
func NewAPI() *API {
	if logChan == nil {
		logChan = make(chan string, 100)
		logChan <- "[INFO] >>>>>> Log channel initialized successfully by NewAPI! (User-provided version) <<<<<<"
	}
	goLogger(0, "[API] NewAPI instance created. (User-provided version)")
	return &API{}
}

func (a *API) PollLogs() string {
	if logChan == nil {
		return "Log channel not initialized. (User-provided version)"
	}
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

func (a *API) Start(jsonCfg string) error {
	goLogger(0, "[API] Start called. (User-provided version base)") // 标记一下基于哪个版本

	cliMu.Lock()
	defer cliMu.Unlock()

	if cli != nil {
		goLogger(1, "[API] Start: Existing client found, closing it first. (User-provided version base)")
		_ = cli.Close()
		cli = nil
	}

	goLogger(1, "[API] Start: Creating new client bridge. (User-provided version base)")
	cb, err := newClientBridge(jsonCfg)
	if err != nil {
		goLogger(2, "[API] Start: newClientBridge failed: "+err.Error()+" (User-provided version base)")
		return err
	}

	cli = cb
	goLogger(1, "[API] Start: New client bridge created. Starting connection in background. (User-provided version base)")

	go cb.Connect()

	goLogger(0, "[API] Start finished. Connection process running in background. (User-provided version base)")
	return nil
}

func (a *API) GetState() string {
	// goLogger 调用前 logChan 应该已经被 NewAPI 初始化了
	cliMu.RLock()
	defer cliMu.RUnlock()
	if cli == nil {
		goLogger(1, "[API] GetState: cli is nil, returning 'stopped'. (User-provided version base)")
		return "stopped"
	}
	state := cli.GetState()
	goLogger(1, fmt.Sprintf("[API] GetState called, returning '%s'. (User-provided version base)", state))
	return state
}

func (a *API) Stop() {
	goLogger(0, "[API] Stop called. (User-provided version base)")
	cliMu.Lock()
	defer cliMu.Unlock()
	if cli != nil {
		goLogger(1, "[API] Stop: Client found, calling Close(). (User-provided version base)")
		_ = cli.Close()
		cli = nil
		goLogger(1, "[API] Stop: Client closed and set to nil. (User-provided version base)")
	} else {
		goLogger(1, "[API] Stop: No client to stop. (User-provided version base)")
	}
}

// ==================== 唯一的修改点 ====================
func (a *API) WriteTunPacket(pkt []byte) error {
	// 立即将任务派发到新的 goroutine，并快速返回 nil。
	// 这样可以防止任何下游的锁或IO操作阻塞 gomobile 的调用线程。
	go func(p []byte) {
		// 使用在 api.go 中定义的 goLogger。
		goLogger(0, fmt.Sprintf(">>> [API-goroutine] WriteTunPacket: ENTERED. Packet size: %d.", len(p)))

		// 在 goroutine 内部处理 panic，避免搞垮整个程序
		defer func() {
			if r := recover(); r != nil {
				panicMsg := fmt.Sprintf("!!!!!!!!!! PANIC RECOVERED IN WriteTunPacket GOROUTINE !!!!!!!!!! : %v", r)
				goLogger(2, panicMsg)
				fmt.Println(panicMsg)
			}
		}()

		cliMu.RLock()
		// 如果 cli 为 nil，记录日志并安全退出 goroutine
		if cli == nil {
			goLogger(2, ">>> [API-goroutine] WriteTunPacket: ERROR - cli is nil.")
			cliMu.RUnlock()
			return
		}
		// cli 不是 nil，可以安全调用
		err := cli.WriteFromTun(p)
		cliMu.RUnlock() // 在完成调用后立即释放读锁

		if err != nil {
			goLogger(2, fmt.Sprintf(">>> [API-goroutine] WriteTunPacket: cli.WriteFromTun returned error: %v", err))
		} else {
			goLogger(1, ">>> [API-goroutine] WriteTunPacket: cli.WriteFromTun completed successfully.")
		}

	}(append([]byte(nil), pkt...)) // 关键：创建 pkt 的副本传递给 goroutine，防止数据竞争。

	return nil // 立即返回，不向 Swift 报告下游错误（错误在 Go 日志中处理）。
}

// =======================================================

func (a *API) ReadTunPacket() ([]byte, error) {
	goLogger(1, "[API] ReadTunPacket called. (User-provided version base)")
	cliMu.RLock()
	defer cliMu.RUnlock()
	if cli == nil {
		goLogger(2, "[API] ReadTunPacket: Error - cli is nil. (User-provided version base)")
		return nil, errors.New("client not started")
	}

	buf := make([]byte, 65535)
	n, err := cli.ReadToTun(buf)
	if err != nil {
		if err.Error() != "closed" {
			goLogger(2, fmt.Sprintf("[API] ReadTunPacket: ReadToTun returned error: %s (User-provided version base)", err.Error()))
		}
		return nil, err
	}
	if n == 0 {
		goLogger(1, "[API] ReadTunPacket: Read 0 bytes. (User-provided version base)")
		return nil, nil
	}

	goLogger(1, fmt.Sprintf("[API] ReadTunPacket: Successfully read %d bytes. (User-provided version base)", n))
	return buf[:n], nil
}

func (a *API) ReadTunPacketNonBlock() ([]byte, error) {
	cliMu.RLock()
	defer cliMu.RUnlock()
	if cli == nil {
		return nil, errors.New("client not started")
	}

	buf := make([]byte, 65535)
	n, err := cli.ReadToTun(buf)

	if err != nil {
		// ★ 把 gVisor / channel 的非致命状态统一映射成 ErrNoData
		// 注意：根据 tun_dev.go 的实现，"closed" 错误是致命的，不应映射为 ErrNoData。
		// 我们只处理那些可以被安全忽略的、代表“暂时无数据”的错误。
		// 鉴于 ReadToTun 的非阻塞实现，它在无数据时应返回 (0, nil)，
		// 所以这里的 err != nil 路径主要处理真实错误。
		// 但为了保险，我们保留对 "timeout" 的检查。
		if strings.Contains(err.Error(), "timeout") {
			return nil, ErrNoData
		}
		// 其他真实错误
		return nil, err
	}

	if n == 0 {
		// 无包可读
		return nil, ErrNoData
	}

	return buf[:n], nil
}

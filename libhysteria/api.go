package libhysteria

import (
	"errors"
	"fmt"
	"sync"
)

/* ---------- 日志缓冲 (严格遵循您提供的版本) ---------- */

var (
	logChan chan string
)

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
func (a *API) WriteTunPacket(pkt []byte) (errRet error) { // Named return for defer/recover
	// 使用您版本中的 goLogger，它有 logChan == nil 的保护
	goLogger(0, fmt.Sprintf(">>> [API] WriteTunPacket: ENTERED (api.go with TOP LEVEL RECOVER). Packet size: %d.", len(pkt)))

	defer func() {
		if r := recover(); r != nil {
			// 发生 Panic！
			panicMsg := fmt.Sprintf("!!!!!!!!!! PANIC RECOVERED IN LibhysteriaAPI.WriteTunPacket !!!!!!!!!! : %v", r)
			goLogger(2, panicMsg) // 尝试用 goLogger 记录
			fmt.Println(panicMsg) // 同时尝试用 fmt.Println，万一 goLogger 也失效了

			// 尝试将 panic 信息作为错误返回给 Swift
			// 注意：如果 panic 导致 gomobile 桥本身损坏，这个错误可能也无法成功传递
			errRet = errors.New("PANIC in Go WriteTunPacket: " + fmt.Sprint(r))
		}
	}()

	// --- 原有的逻辑 ---
	goLogger(1, ">>> [API] WriteTunPacket: Checking cli...")
	cliMu.RLock()
	defer cliMu.RUnlock() // RUnlock 会在函数返回前执行，即使发生 panic (在 cli.WriteFromTun 之后)

	if cli == nil {
		goLogger(2, ">>> [API] WriteTunPacket: ERROR - cli is nil.")
		return errors.New("client not started (API WriteTunPacket check)")
	}
	goLogger(1, ">>> [API] WriteTunPacket: cli is NOT nil. Calling cli.WriteFromTun...")

	// 调用下游函数，这里也可能 panic
	err := cli.WriteFromTun(pkt)
	if err != nil {
		goLogger(2, fmt.Sprintf(">>> [API] WriteTunPacket: cli.WriteFromTun returned error: %v", err))
		return err
	}

	goLogger(1, ">>> [API] WriteTunPacket: cli.WriteFromTun completed successfully.")
	return nil // 正常返回
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

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

// [PLAN STEP 1] 新增：用于工作队列模式的全局变量
var (
	// 使用 sync.Pool 复用数据包缓冲区，减少GC压力
	pktPool = sync.Pool{New: func() interface{} {
		// 预分配 2048 字节，足以容纳大多数MTU
		b := make([]byte, 2048)
		return &b
	}}
	// 数据包工作队列，限制缓冲大小以控制内存
	pktQueue = make(chan []byte, 1024)
	// 确保 writerLoop 只启动一次
	onceWriter sync.Once
)

// [PLAN STEP 1] 新增：处理数据包队列的常驻 goroutine
func writerLoop() {
	goLogger(0, "[writerLoop] Starting packet writer loop.")
	for p := range pktQueue {
		cliMu.RLock()
		c := cli
		cliMu.RUnlock()

		if c != nil {
			err := c.WriteFromTun(p)
			if err != nil {
				goLogger(2, fmt.Sprintf("[writerLoop] WriteFromTun failed: %v", err))
			}
		}
		// 将缓冲区放回池中以供复用
		// 我们需要从池中获取指向字节切片的指针，所以这里需要类型断言
		ptr := pktPool.Get().(*[]byte)
		*ptr = (*ptr)[:0] // 重置长度为0，但保留容量
		pktPool.Put(ptr)
	}
	goLogger(0, "[writerLoop] Packet writer loop stopped.")
}

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

// [PLAN STEP 1] 修正：在 Start 中启动 writerLoop
func (a *API) Start(jsonCfg string) error {
	goLogger(0, "[API] Start called.")

	// 确保 writerLoop goroutine 已经启动
	onceWriter.Do(func() {
		go writerLoop()
	})

	cliMu.Lock()
	defer cliMu.Unlock()

	if cli != nil {
		goLogger(1, "[API] Start: Existing client found, closing it first.")
		_ = cli.Close()
		cli = nil
	}

	goLogger(1, "[API] Start: Creating new client bridge.")
	cb, err := newClientBridge(jsonCfg)
	if err != nil {
		goLogger(2, "[API] Start: newClientBridge failed: "+err.Error())
		return err
	}

	cli = cb
	goLogger(1, "[API] Start: New client bridge created. Starting connection in background.")

	go cb.Connect()

	goLogger(0, "[API] Start finished. Connection process running in background.")
	return nil
}

func (a *API) GetState() string {
	cliMu.RLock()
	defer cliMu.RUnlock()
	if cli == nil {
		return "stopped"
	}
	return cli.GetState()
}

func (a *API) Stop() {
	goLogger(0, "[API] Stop called.")
	cliMu.Lock()
	defer cliMu.Unlock()
	if cli != nil {
		goLogger(1, "[API] Stop: Client found, calling Close().")
		_ = cli.Close()
		cli = nil
	}
}

// [PLAN STEP 1] 修正：重构 WriteTunPacket 以使用工作队列
func (a *API) WriteTunPacket(pkt []byte) error {
	// 从池中获取一个缓冲区
	bufPtr := pktPool.Get().(*[]byte)
	buf := *bufPtr

	// 确保缓冲区容量足够，如果不够则重新分配
	if cap(buf) < len(pkt) {
		buf = make([]byte, len(pkt))
	}
	// 设置正确的长度并拷贝数据
	buf = buf[:len(pkt)]
	copy(buf, pkt)

	// 使用非阻塞方式将数据包发送到队列
	select {
	case pktQueue <- buf:
		// 成功发送
		*bufPtr = buf // 更新指针指向的 slice
		return nil
	default:
		// 队列已满，丢弃数据包以防止阻塞
		goLogger(2, "[API] WriteTunPacket: pktQueue is full, dropping packet.")
		// 将未被发送的缓冲区立即归还给池
		*bufPtr = (*bufPtr)[:0]
		pktPool.Put(bufPtr)
		// 返回一个错误，向上游传递背压信号
		return ErrNoData
	}
}

// ReadTunPacket 保持不变，但通常不会被高频调用
func (a *API) ReadTunPacket() ([]byte, error) {
	cliMu.RLock()
	defer cliMu.RUnlock()
	if cli == nil {
		return nil, errors.New("client not started")
	}
	buf := make([]byte, 65535)
	n, err := cli.ReadToTun(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// ReadTunPacketNonBlock 保持不变
func (a *API) ReadTunPacketNonBlock() ([]byte, error) {
	cliMu.RLock()
	defer cliMu.RUnlock()
	if cli == nil {
		return nil, errors.New("client not started")
	}
	buf := make([]byte, 65535)
	n, err := cli.ReadToTun(buf)
	if err != nil {
		if strings.Contains(err.Error(), "timeout") {
			return nil, ErrNoData
		}
		return nil, err
	}
	if n == 0 {
		return nil, ErrNoData
	}
	return buf[:n], nil
}

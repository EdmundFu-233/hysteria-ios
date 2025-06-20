package libhysteria

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

/* ---------- 日志缓冲 ---------- */

var (
	logChan  chan string
	logLevel = 2 // 0=DEBUG, 1=INFO, 2=ERROR.
)

var ErrNoData = errors.New("no data")

func goLogger(level int, msg string) {
	if level < logLevel {
		return
	}
	if logChan == nil {
		return
	}
	prefix := [...]string{"DEBUG", "INFO", "ERROR"}[level%3]
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

// MARK: - CORRECTED
// 修正了 sync.Pool 的管理逻辑，这是解决内存泄漏和性能问题的核心。
var (
	// sync.Pool 用于复用数据包缓冲区，其类型为 *[]byte (指向字节切片的指针)
	pktPool = sync.Pool{New: func() interface{} {
		b := make([]byte, 2048) // 预分配 2048 字节
		return &b
	}}
	// 工作队列的类型必须与 Pool 的类型匹配，传递指针而不是切片本身。
	pktQueue = make(chan *[]byte, 1024)
	// 确保 writerLoop 只启动一次
	onceWriter sync.Once
)

// MARK: - CORRECTED
// writerLoop 现在正确地处理从池中获取并归还缓冲区的逻辑。
func writerLoop() {
	goLogger(0, "[writerLoop] Starting packet writer loop.")
	// 从队列中接收的是缓冲区的指针 (pPtr)
	for pPtr := range pktQueue {
		cliMu.RLock()
		c := cli
		cliMu.RUnlock()

		if c != nil {
			// 通过指针获取实际的数据切片 (*pPtr)
			err := c.WriteFromTun(*pPtr)
			if err != nil {
				goLogger(2, fmt.Sprintf("[writerLoop] WriteFromTun failed: %v", err))
			}
		}
		// **核心修复**：将使用完毕的缓冲区指针 pPtr 原封不动地放回池中。
		// 在放回之前，重置切片长度为0，但保留其底层数组容量，以便下次复用。
		*pPtr = (*pPtr)[:0]
		pktPool.Put(pPtr)
	}
	goLogger(0, "[writerLoop] Packet writer loop stopped.")
}

func NewAPI() *API {
	if logChan == nil {
		logChan = make(chan string, 100)
		logChan <- "[INFO] >>>>>> Log channel initialized successfully by NewAPI! <<<<<<"
	}
	goLogger(0, "[API] NewAPI instance created.")
	return &API{}
}

func (a *API) PollLogs() string {
	if logChan == nil {
		return "Log channel not initialized."
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
	goLogger(0, "[API] Start called.")
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

// MARK: - CORRECTED
// WriteTunPacket 现在将缓冲区的指针放入队列。
func (a *API) WriteTunPacket(pkt []byte) error {
	// 从池中获取一个缓冲区指针
	bufPtr := pktPool.Get().(*[]byte)
	buf := *bufPtr

	// 确保缓冲区容量足够
	if cap(buf) < len(pkt) {
		buf = make([]byte, len(pkt))
	}
	// 设置正确的长度并拷贝数据
	buf = buf[:len(pkt)]
	copy(buf, pkt)
	*bufPtr = buf // 将更新后的切片信息写回指针指向的位置

	// 使用非阻塞方式将缓冲区指针发送到队列
	select {
	case pktQueue <- bufPtr:
		// 成功发送
		return nil
	default:
		// 队列已满，丢弃数据包以防止阻塞
		goLogger(2, "[API] WriteTunPacket: pktQueue is full, dropping packet.")
		// **核心修复**：将未被发送的缓冲区指针立即归还给池
		pktPool.Put(bufPtr)
		return ErrNoData
	}
}

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

// api.go

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

const maxBufferSizeForPool = 4096

var (
	pktPool = sync.Pool{New: func() interface{} {
		b := make([]byte, 2048)
		return &b
	}}
	pktQueue   = make(chan *[]byte, 1024)
	onceWriter sync.Once
)

func writerLoop() {
	goLogger(0, "[writerLoop] Starting packet writer loop.")
	for pPtr := range pktQueue {
		cliMu.RLock()
		c := cli
		cliMu.RUnlock()

		if c != nil {
			err := c.WriteFromTun(*pPtr)
			if err != nil {
				goLogger(2, fmt.Sprintf("[writerLoop] WriteFromTun failed: %v", err))
			}
		}

		if cap(*pPtr) > maxBufferSizeForPool {
			goLogger(0, fmt.Sprintf("[writerLoop] Discarding oversized buffer (cap %d) instead of returning to pool.", cap(*pPtr)))
		} else {
			*pPtr = (*pPtr)[:0]
			pktPool.Put(pPtr)
		}
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

func (a *API) WriteTunPacket(pkt []byte) error {
	bufPtr := pktPool.Get().(*[]byte)
	buf := *bufPtr

	if cap(buf) < len(pkt) {
		buf = make([]byte, len(pkt))
	}
	buf = buf[:len(pkt)]
	copy(buf, pkt)
	*bufPtr = buf

	select {
	case pktQueue <- bufPtr:
		return nil
	default:
		goLogger(2, "[API] WriteTunPacket: pktQueue is full, dropping packet.")
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

// libhysteria/bridge.go
package libhysteria

/*
#include <stdint.h>
*/
import "C"
import (
    "unsafe"
    "sync"
)

var (
    cliMu sync.RWMutex
    cli   *ClientBridge
)

//export Start
func Start(jsonCfg *C.char) *C.char {
    cfgStr := C.GoString(jsonCfg)

    cb, err := newClientBridge(cfgStr)
    if err != nil {
        return C.CString(err.Error())
    }

    cliMu.Lock()
    if cli != nil {
        _ = cli.Close()
    }
    cli = cb
    cliMu.Unlock()
    return nil
}

//export Stop
func Stop() {
    cliMu.Lock()
    if cli != nil {
        _ = cli.Close()
        cli = nil
    }
    cliMu.Unlock()
}

//export WriteTunPacket
func WriteTunPacket(buf *C.uint8_t, length C.int) C.int {
    pkt := unsafe.Slice((*byte)(buf), int(length))

    cliMu.RLock()
    defer cliMu.RUnlock()
    if cli == nil {
        return -1
    }
    if err := cli.WriteFromTun(pkt); err != nil {
        return -1
    }
    return 0
}

//export ReadTunPacket
func ReadTunPacket(buf *C.uint8_t, cap C.int) C.int {
    out := unsafe.Slice((*byte)(buf), int(cap))

    cliMu.RLock()
    defer cliMu.RUnlock()
    if cli == nil {
        return -1
    }
    n, err := cli.ReadToTun(out)
    if err != nil {
        return -1
    }
    return C.int(n)
}


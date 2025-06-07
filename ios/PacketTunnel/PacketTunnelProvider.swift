import NetworkExtension

// Gomobile 自动产出的 swift module 名称 = Libhysteria
import Libhysteria

@main
class PacketTunnelProvider: NEPacketTunnelProvider {
    private var readTimer: DispatchSourceTimer?
    private var mtu = 1350

    // MARK: - 启动
    override func startTunnel(options: [String : NSObject]?,
                              completionHandler: @escaping (Error?) -> Void) {
        let cfg = [
            "server":"example.com:443",
            "auth":"base64-token",
            "psk":"mypsk",
            "fastOpen":true
        ]
        let json = try! JSONSerialization.data(withJSONObject: cfg).base64EncodedString()
        if let errPtr = Start(json) {     // CChar pointer -> swift String?
            let errStr = String(cString: errPtr)
            completionHandler(NSError(domain: "libhysteria", code: 1,
                                       userInfo: [NSLocalizedDescriptionKey: errStr]))
            return
        }

        // 设置虚拟接口
        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "10.0.0.1")
        let ipv4 = NEIPv4Settings(addresses: ["10.0.0.2"],
                                  subnetMasks: ["255.255.255.0"])
        ipv4.includedRoutes = [NEIPv4Route.default()]
        settings.ipv4Settings = ipv4
        settings.mtu = NSNumber(value: mtu)

        setTunnelNetworkSettings(settings) { err in
            if let err { completionHandler(err); return }
            self.startIO()
            completionHandler(nil)
        }
    }

    // MARK: - 停止
    override func stopTunnel(with reason: NEProviderStopReason,
                             completionHandler: @escaping () -> Void) {
        Stop()
        readTimer?.cancel()
        completionHandler()
    }

    // MARK: - TUN IO
    private func startIO() {
        // 上行：从 TUN 读 -> Go
        packetFlow.readPackets { [weak self] pkts, _ in
            guard let self else { return }
            for pkt in pkts {
                pkt.withUnsafeBytes { ptr in
                    _ = WriteTunPacket(UnsafeMutablePointer(mutating: ptr.baseAddress!.assumingMemoryBound(to: UInt8.self)),
                                       Int32(pkt.count))
                }
            }
            self.startIO()
        }

        // 下行：Go -> TUN（轮询）
        readTimer = DispatchSource.makeTimerSource()
        readTimer?.schedule(deadline: .now(), repeating: .milliseconds(10))
        readTimer?.setEventHandler {
            var buf = [UInt8](repeating: 0, count: 65535)
            let n = ReadTunPacket(&buf, Int32(buf.count))
            if n > 0 {
                let data = Data(bytes: buf, count: Int(n))
                self.packetFlow.writePackets([data], withProtocols: [AF_INET as NSNumber])
            }
        }
        readTimer?.resume()
    }
}


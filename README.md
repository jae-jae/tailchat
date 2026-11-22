# TailChat

跨平台多设备互联的文件传输助手（基于 **MyGo** Native UI + **Tailscale Tailcat** 纯用户态加密隧道）。

## 核心特性

- **单时间线“我的设备群”**：所有已配对设备共享同一条聊天流；文字即时同步，文件默认仅同步元信息 + 缩略图，按端阈值自动或按需拉取。
- **零账号、无中心控制面**：每台设备本地持久化 WireGuard + Ed25519 密钥对；新设备只需与群内任意一台已有设备扫码/粘贴配对码（`tailchat-pair://...`），签名成员事件（`MemberEvent`）自动扩散至其余设备并写入 `tailcat.KeySet` 白名单；任意设备可随时发起吊销踢出并强制断开隧道。
- **默认开箱即用 + 热切换自建 DERP**：体验期默认使用 `tailcat.dev` 引导并在打通 NAT/LAN 后自动升级直连；设置页随时可切换为自建 `derper` 节点或私有 `DERPMapURL`，设备永久身份（`DeviceID`）与可变路由（`RouteEpoch`）解耦，换 DERP 不掉配对。
- **大文件分块断点续传 & 多 Seeder 拉取**：1 MiB BLAKE3 内容寻址分块 + 持久化分块位图（`ChunkBitmap`），支持暂停/恢复/取消、限速与跨多台在线己方设备拉取（兼容部署一台常驻无头节点 `tailchat-headless` 充当永不离线成员）。
- **桌面 & 移动端系统集成**：
  - 桌面端（macOS / Windows / Linux）：MyGo GPU 自绘原生界面（零 WebView 内存）、系统托盘常驻、关闭隐藏到托盘、全局快捷键唤出（`Cmd/Ctrl+Shift+T`）、拖拽/粘贴文件、本地通知、SQLite WAL + FTS5 全文搜索。
  - Android：`plugins/mobilebridge/` 通过 `mygo-plugin.json` 注入 `FOREGROUND_SERVICE_DATA_SYNC` 传输保活服务与 `ShareReceiverActivity`（`ACTION_SEND` / `ACTION_SEND_MULTIPLE`）。
  - iOS：`ios/ShareExtension/` 提供纯 Swift、零 Go 依赖的 Share Extension（严守 iOS 120 MB 内存红线），通过 App Group 队列与 `tailchat://flush-outbox` 唤醒主 App 发送。

## 仓库结构

```text
.
├── cmd/
│   ├── tailchat/             # MyGo 桌面/移动主程序入口
│   └── tailchat-headless/    # 零 UI 常驻无头节点 CLI（适合 NAS/Mac mini/VPS）
├── internal/
│   ├── model/                # 领域模型、Ed25519/BLAKE3/ULID 工具与跨包接口契约
│   ├── store/                # SQLite WAL + FTS5 存储与 1 MiB BLAKE3 分块 CAS 库
│   ├── transport/            # Tailcat 1-Server + 公钥破环 Pairwise Client + yamux 多路复用 + MemNetwork
│   ├── syncproto/            # 签名成员 Gossip、配对协议、Vector Clock 同步、分块续传下载器与 AppAPI
│   ├── ui/                   # MyGo Native UI（聊天流、配对/设备/DERP 设置弹窗、桌面托盘与快捷键）
│   └── integration_test.go   # 全栈 3 节点端到端集成测试
├── plugins/mobilebridge/     # Android (Kotlin) & iOS (Swift) 原生桥接插件 + mygo-plugin.json
├── ios/ShareExtension/       # iOS 纯 Swift Share Extension 源码与 Xcode 打包指引
└── mygo.config.ts            # MyGo 五端构建配置
```

## 快速开始

### 前置条件
- Go `1.27.4+`（`tailcat` 与 `mygo` 当前要求）

### 1. 运行全仓测试
```bash
go test -race ./...
```

如需在本机实测真实 `TailcatMesh` 经公网 DERP 握手与 `yamux` 收发数据：
```bash
TAILCHAT_LIVE_TAILCAT_TEST=1 go test -v -timeout 60s ./internal/transport/... -run TestLiveTailcatMesh
```

### 2. 编译并本地双开测试桌面端
```bash
go build -o ./tailchat ./cmd/tailchat
go build -o ./tailchat-headless ./cmd/tailchat-headless

# 终端 1：启动设备 A
./tailchat -data-dir /tmp/tailchat-a -name "MacBook-A"

# 终端 2：启动设备 B（在同一台机器或另一台机器）
./tailchat -data-dir /tmp/tailchat-b -name "MacBook-B"
```
在设备 A 点击右上角 **Pair → Generate Pairing Code** 复制 `tailchat-pair://...`，粘贴到设备 B 的 **Pair → Join Existing Group** 即可完成配对并自动互联。

### 3. 运行常驻无头节点（可选）
把一台始终在线的 Mac mini / Linux 主机 / NAS 加入你的设备群作为离线消息与文件缓存节点：
```bash
./tailchat-headless \
  -data-dir ~/.tailchat-headless \
  -name "HomeNAS" \
  -join "tailchat-pair://..." \
  -auto-download-mb 200
```
如需自建 `derper`：
```bash
./tailchat-headless -data-dir ~/.tailchat-headless -name "HomeNAS" -derp-host derp.example.com
```

### 4. 移动端构建（Android / iOS）
安装 `mygo` CLI 后，参考 [mygo.config.ts](mygo.config.ts) 与 [ios/ShareExtension/README.md](ios/ShareExtension/README.md)：
```bash
mygo build -platform android
mygo build -platform ios
```

<div align="center">

<img src="docs/assets/icon.png" alt="TailChat" width="112" />

# TailChat

**零账号、纯 Go 原生界面的跨平台多设备 P2P 文件与文字传输助手**

基于 [**MyGo**](https://github.com/egoist/mygo) GPU 自绘原生界面 · [**Tailscale Tailcat**](https://github.com/tailscale/tailcat) 纯用户态 WireGuard 加密隧道

[![Release](https://img.shields.io/github/v/release/jae-jae/tailchat?color=0A84FF)](https://github.com/jae-jae/tailchat/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.27+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![UI](https://img.shields.io/badge/UI-MyGo%20GPU%20Native-2563EB)](#核心特性)
[![Transport](https://img.shields.io/badge/Transport-Tailscale%20Tailcat-0F172A)](#核心特性)
[![License: MIT](https://img.shields.io/badge/License-MIT-22C55E.svg)](LICENSE)

[English](README.md) | 简体中文

<br />

<img src="docs/screenshots/hero.png" alt="TailChat 日夜间主题界面总览" width="100%" />

</div>

---

## 核心特性

- **零账号全互联**：每台设备本地生成 WireGuard + Ed25519 密钥，无中心控制面；所有已配对设备共享同一条「我的设备群」时间线，文字秒同步，文件按需或按阈值自动拉取。
- **4 级智能直连与 DERP 热切**：自动按最优加密链路建连与热升级——**自定义指定 IP 直连 (TCP+mTLS)** → **局域网自动发现直连 (TCP+mTLS)** → **Tailcat UDP P2P 直连** → **DERP 中继**。支持在设备浮层内联配置逐设备直连 IP，也可随时在设置中热切换自建 `derper` 或自定义 `DERPMapURL`，**更换 DERP 不掉配对**。
- **扫码即配对，一次加入全群互通**：点击顶部 **配对** 生成高对比纯 Go 二维码（深色模式下保留纯白静默区）并支持一键复制配对码；签名成员事件自动 Gossip 扩散至群内所有设备，随时一键移除设备并即时断开隧道。
- **大文件分块断点续传 & 富媒体预览**：1 MiB BLAKE3 内容寻址分块 + 持久化位图与自动空间回收，支持暂停/恢复/取消、限速及多台在线设备并发拉取；内置图片缩略图与全屏灯箱预览、音频内联播放、自定义下载目录以及一键「打开所在目录」。
- **纯 Go GPU 原生客户端（日夜间主题 & 双语）**：零 Electron / 零 WebView 开销；支持 **Zen Popover（浅色）** 与 **Zen Slate（深色）** 主题及 `跟随系统 / 浅色 / 深色` 实时预览、系统语言自动识别（中英双语）、悬浮胶囊输入栏、4 分类设置弹窗、跨平台原生顶栏适配（macOS 单行交通灯；Windows & Linux 采用独立 `32px` 标题栏 + `48px` 全宽工具栏），并集成系统托盘、`Cmd/Ctrl+Shift+T` 全局唤出、拖拽/粘贴发送与 SQLite WAL + FTS5 全文检索。
- **签名自动更新 & 多平台安装包**：内置带签名校验与增量补丁的应用内更新（`设置 → 关于`），配合 GitHub Releases 自动发布全平台安装包。
- **无头节点 & 移动端扩展**：提供零 UI 的 `tailchat-headless` 常驻节点（适合 NAS / Mac mini / VPS），以及 Android 前台保活/分享插件与纯 Swift iOS Share Extension。

## 界面预览

<table>
  <tr>
    <td align="center" width="50%">
      <img src="docs/screenshots/pairing-qr.png" alt="扫码配对（深色主题）" width="100%" /><br />
      <sub><b>扫码配对 & 一键复制（Zen Slate 深色）</b></sub>
    </td>
    <td align="center" width="50%">
      <img src="docs/screenshots/devices.png" alt="已配对设备与自定义直连 IP" width="100%" /><br />
      <sub><b>4 级连接状态 & 浮层内联配置直连 IP</b></sub>
    </td>
  </tr>
  <tr>
    <td align="center" width="50%">
      <img src="docs/screenshots/settings-general.png" alt="4 分类设置：语言与日夜间主题" width="100%" /><br />
      <sub><b>4 分类设置：中英双语与日夜间主题实时切换</b></sub>
    </td>
    <td align="center" width="50%">
      <img src="docs/screenshots/settings-derp.png" alt="网络设置：局域网直连监听与 DERP 热切" width="100%" /><br />
      <sub><b>局域网直连监听 & 自建 DERP 热切换</b></sub>
    </td>
  </tr>
</table>

## 下载与快速开始

### 直接下载安装包

前往 [**GitHub Releases**](https://github.com/jae-jae/tailchat/releases/latest) 下载最新签名版本（附 `SHA256SUMS.txt` 校验）：

| 平台 | 架构 | 安装包格式 |
| --- | --- | --- |
| **macOS** | `arm64`, `amd64` | `.dmg`, `.tar.gz` |
| **Windows** | `amd64`, `arm64` | `-setup.exe` 安装程序, 绿色 `.zip` |
| **Linux** | `amd64`, `arm64` | `.deb`, `.rpm`, `.tar.gz` |
| **无头节点 CLI** | Linux / macOS / Windows | `tailchat-headless` `.tar.gz` / `.zip` |

在已有设备点击右上角 **配对**，用新设备扫码或粘贴配对码即可完成组网。

### 从源码编译

需要 **Go 1.27.4+**。

```bash
# 1. 编译桌面端与无头节点
go build -o ./tailchat ./cmd/tailchat
go build -o ./tailchat-headless ./cmd/tailchat-headless

# 2. 启动桌面端（本地双开可指定不同 -data-dir）
./tailchat -data-dir ~/.tailchat -name "MacBook Pro"

# 3. 运行全仓测试
go test -race ./...
```

<details>
<summary><b>可选：部署常驻无头节点（NAS / Linux / VPS）与移动端构建</b></summary>

```bash
# 加入设备群并自动缓存 ≤ 200 MB 的文件
./tailchat-headless \
  -data-dir ~/.tailchat-headless \
  -name "HomeNAS" \
  -join "tailchat-pair://..." \
  -auto-download-mb 200

# 或在无头节点上生成一次性配对码供其他设备加入
./tailchat-headless -data-dir ~/.tailchat-headless -name "HomeNAS" -pair

# 指定自建 derper 节点（也支持 -derp-map-url）
./tailchat-headless -data-dir ~/.tailchat-headless -name "HomeNAS" -derp-host derp.example.com
```

移动端（Android / iOS）参考 [`mygo.json`](mygo.json) 与 [`ios/ShareExtension/README.md`](ios/ShareExtension/README.md)：

```bash
go tool mygo build -platform android
go tool mygo build -platform ios
```

</details>

## 仓库结构

```text
cmd/
├── tailchat/             # MyGo 桌面/移动主程序
└── tailchat-headless/    # 无头常驻缓存节点 CLI
internal/
├── model/                # 领域模型、Ed25519 / BLAKE3 / ULID 与接口契约
├── store/                # SQLite WAL + FTS5 存储与 1 MiB BLAKE3 分块 CAS
├── transport/            # Tailcat 网格、局域网/自定义 IP TCP+mTLS 直连与 yamux 多路复用
├── syncproto/            # 签名成员 Gossip、配对、Vector Clock 同步与续传下载器
├── updater/              # 签名应用内自动更新、增量补丁与 Release Notes 解析
└── ui/                   # MyGo GPU 原生界面、日夜间主题、双语、二维码、媒体预览与系统托盘
plugins/mobilebridge/     # Android (Kotlin) / iOS (Swift) 原生桥接插件
ios/ShareExtension/       # 纯 Swift iOS Share Extension（零 Go 依赖）
```

## License

[MIT](LICENSE) © 2026 [jae-jae](https://github.com/jae-jae)

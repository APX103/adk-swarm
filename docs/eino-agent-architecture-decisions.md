# eino_agent 架构决策记录

> 状态：已采纳（现状）。本文记录 eino_agent 当前实现方式背后的权衡（trade-off），
> 以及曾经考虑过的替代方案为什么不选。
> 这是一个**事后整理**的决策文档，记录的是"为什么现在长这样"。

---

## 背景

eino_agent 是本项目的 Go 语言 agent，承担两个角色：
1. 演示**异构 agent 协作**（Go agent 能和 Python agent 通过 A2A 协议互通）
2. 作为**集群中的真实节点**：自注册到 registry、发现别的 agent、被别人调用

它当前基于 **CloudWeGo Eino** 框架实现，A2A server 和 Web UI 是**手写**的。

---

## 决策一：用 Eino 而不是 Google ADK Go SDK

### 背景：有两个 "adk"

这个名字撞车是理解问题的起点：

| | eino_agent 当前用的 | 官方 Google ADK Go |
|---|---|---|
| 包路径 | `github.com/cloudwego/eino/adk` | `google.golang.org/adk` |
| 来源 | CloudWeGo（字节跳动）| Google 官方 |
| 仓库 | [cloudwego/eino](https://github.com/cloudwego/eino) | [google/adk-go](https://github.com/google/adk-go) |
| 自带 Web UI | ❌ 无（我们手写了简陋的 `/ui`）| ✅ `adk web` 启动官方调试界面 |
| 自带 A2A server | ❌ 无（我们手写 JSON-RPC）| ✅ 内置 |
| 工具系统 | `utils.InferTool`（反射生成 schema）| 原生工具系统 |
| 国内模型适配 | Eino 对国内 LLM 接入较成熟 | 需验证（GLM 等）|

**结论**：eino_agent 里的 `adk` 是 Eino 的子包，**不是** Google ADK。

### 为什么当初选 Eino

从代码痕迹判断，原因大概是：
1. **adk-go 当时不够成熟**——它 1.0 稳定版是较近期才发布的，项目立项时可能还在早期
2. **Eino 对国内模型友好**——项目用 GLM，Eino（字节系）对这类模型的接入可能更顺手
3. **Eino 的 `adk` 子包够用**——提供了 Runner/Agent/Tool 抽象，能跑起来

这是一个**务实的当时之选**，不是错误。

### 选 Eino 的代价

- **没有官方 Web UI**：我们手写了一个极简 HTML 页面（`/ui`），只能发消息看回复，
  没有 session 管理、工具调用可视化、事件流展示
- **A2A 全靠手写**：`callA2AAgent` / `handleA2A` / `extractA2AText` 都是手写 JSON-RPC，
  包括双端点兜底（`/` 和 `/jsonrpc`）、多种返回格式兼容。能用，但要自己维护。
- **Agent card 手写**：`buildAgentCard` 手工拼 JSON，字段（skills/capabilities）要自己保证合规

---

## 决策二：为什么不"桥接" Eino 到 adk-go

### 曾设想的方案

```
Eino ADK（agent 引擎：Runner、Agent、Tool、编排）
       │
       ▼ 桥接层（adapter）
Google ADK Go（外壳：WebUI、A2A server、部署工具）
```

**意图**：Eino 负责"agent 怎么思考和执行"，adk-go 负责"agent 怎么被暴露和调试"。
白嫖 adk-go 的 WebUI/A2A/评估工具，而不用自己手写。

### 为什么没这么做（这是核心权衡）

**方向上对，但落到这两个具体框架上不划算。** 三层原因：

#### 第一层：接口形状不同构

Eino 的 `adk.Agent` 和 adk-go 的 `Agent` 接口不是一回事。它们各自定义了：
- agent 如何被构造（config 结构不同）
- 工具如何注册（`compose.ToolsNodeConfig` vs adk-go 的 tool 系统）
- 结果如何返回（`MessageVariant` 可能是流 vs adk-go 的事件模型）

桥接 = 写 adapter 让 Eino agent 实现 adk-go 的 Agent 接口。但 adk-go 接口里很可能有
Eino 不直接对应的概念（callback、session state 读写、sub-agent 转移），这些得模拟或空实现。
**一旦空实现，WebUI 展示的调试信息就会缺失或失真**——因为底层事件流对不上。

#### 第二层：WebUI 依赖整个事件协议（最容易被低估）

adk-go 的 WebUI 不是"发个 HTTP 请求拿回复"那么简单。它背后是整套：
- **session 管理**（创建/恢复/列举）
- **结构化事件流**（每步思考、工具调用、工具返回都作为 event 推送）
- **artifacts 管理**
- **评估/metrics**

WebUI 能展示得丰富，是因为 adk-go 的 agent 在执行时**吐出结构化事件**。
Eino 的事件模型是它自己的（`MessageVariant`、`schema.Message`），和 adk-go 期望的事件
schema 不一样。桥接层不仅要转 agent 接口，还得**翻译事件流**——这往往是最脆、最容易出
bug 的部分：
- 流式输出怎么对应？
- 工具调用的边界怎么对应？
- 错误/异常怎么对应？

#### 第三层：A2A 反而最不需要桥接

有意思的是，**A2A 恰恰是最不需要桥接的部分**——因为 A2A 是开放协议（JSON-RPC over HTTP），
谁实现都行。我们手写的 A2A handler 就是协议适配，和用什么 agent 框架无关。
**"为了 A2A 而桥接"这个理由不成立**——A2A 本来就解耦了。

### 桥接方案的成本/收益

| | 自己手写 UI（现状）| 桥接 Eino→adk-go | 直接用 adk-go（重写）|
|---|---|---|---|
| 工作量 | 已完成，简陋 | **大**（adapter + 事件翻译，脆）| 中（重写 agent 核心）|
| WebUI 质量 | 简陋但够 demo | 可能失真（事件对不上）| 完整、官方 |
| agent 引擎 | Eino（国内模型友好）| Eino（保留）| adk-go（需验证 GLM）|
| 长期维护 | 自己背 | 跟进两边升级 | 跟进官方 |

**桥接是三个选项里性价比最低的**——承担两边升级的维护成本，解决事件翻译脆点，
但 WebUI 效果还不一定好。

---

## 决策三：手写 A2A 和 Web UI（当前现状）

基于决策一、二的结论，采取的现状是：

### A2A：手写 JSON-RPC

- `callA2AAgent`：构造 `message/send` payload，双端点兜底（先 `/` 后 `/jsonrpc`）
- `handleA2A`：解析请求，只认 `message/send`，调 Eino Runner，返回 Task
- `extractA2AText`：兼容三种返回格式（status.message.parts / artifacts[].parts / 裸 parts）
- **已验证**：和 Python ADK 的 A2A（main_agent）、Node ADK（frontend_agent）互通无问题

### Web UI：手写极简 HTML

- 嵌入式单页 HTML（`devUIHTML`），支持发消息、显示回复和工具调用记录
- **够用但不丰富**：无 session 管理、无事件流可视化、无评估

### 取舍

- ✅ 优点：零额外依赖、完全可控、不绑死某个框架的 UI 协议
- ❌ 缺点：调试体验远不如官方 WebUI；每次加功能都得手写 HTML/JS

---

## 未来可能的演进（未采纳，仅记录）

### 选项 A：保持现状（Eino + 手写 UI）
适合：当前定位是"演示异构协作"，够用。

### 选项 B：迁移到 adk-go（重写 agent 部分）
适合：如果 eino 需要完整官方调试体验，且验证过 GLM 在 adk-go 下可用。
代价：放弃 Eino（及其国内模型适配优势），重写 agent 定义/工具/Runner。

### 选项 C：桥接 Eino→adk-go
适合：理论上最优雅（保留 Eino + 白嫖 adk-go UI）。
代价：本文决策二论证了它的脆性和维护成本。**不推荐**。

> 注意：以上三个选项都和 registry / MCP 服务发现**正交**——
> 无论 eino 用哪个 Go agent 框架，A2A 协议和 registry 接入方式都不受影响。

---

## 关键文件索引

| 文件 | 作用 |
|------|------|
| `eino_agent/main.go` | 全部实现（单文件），含 agent 定义、A2A、UI、registry 接入 |
| `eino_agent/go.mod` | 依赖：`cloudwego/eino` + `mark3labs/mcp-go`（MCP 客户端）|
| `eino_agent/Dockerfile` | Go 1.24 构建 |
| `agent_registry/INTEGRATION.md` | 接入 registry 的文档（MCP + REST 两种方式）|

## 参考资料

- [CloudWeGo Eino – GitHub](https://github.com/cloudwego/eino)
- [Google ADK Go (adk-go) – GitHub](https://github.com/google/adk-go)
- [ADK Web (官方 WebUI) – GitHub](https://github.com/google/adk-web)
- [Go Quickstart – adk.dev](https://adk.dev/get-started/go/)

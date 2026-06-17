# ADK Swarm — 多 Agent 协同平台

基于 Google ADK + A2A 的多 Agent 平台。一个面向用户的 **Main Agent（orchestrator）** 负责对话与调度，**根据任务性质自主判断**该把工作交给哪个专家子 Agent，调用后拿回结果继续推理，最终整理回复用户。

## 架构：orchestrator + delegate（不是 transfer，不是流水线）

```
                         ┌──────────────────────────────┐
   用户 ──CLI 对话──▶    │   Main Agent (orchestrator)   │
                         │   从 Registry 读取子 Agent    │
                         │   自己判断派给谁、什么顺序     │
                         │   调用后拿回结果，控制权不交出 │
                         └──────────┬───────────────────┘
            ┌─────────────┬─────────┴──────────┬──────────────┐
       前端工具        backend_agent       comedian_agent   critic_agent
   generate_frontend    (A2A :8002)         (A2A :8003)     (A2A :8004)
       _project           │                    │               │
            │              └─── 均为 RemoteA2aAgent，用 AgentTool 包成委派工具 ──┘
            ▼
   Frontend Agent (Node, :8001, Docker) ──产物──▶ File Server :8080

   eino_agent (:8005)  ──────双向 A2A──────▶  main_agent :8081

   agent_registry (:8006)  ◄─── 所有 orchestrator 从这里读取 endpoints
```

**为什么是这种架构（不是别的）：**

ADK 有三种多 Agent 模式，本平台用的是 **AgentTool delegate（模式 B）**：

| 模式 | 机制 | 控制权 | 本平台 |
|------|------|--------|--------|
| transfer（sub_agents） | LLM 把整个对话交给子 Agent | **交出去**，子 Agent 接管直到转回 | ✗ RemoteA2aAgent 跨进程拿不到 agent 树，转不回来 |
| **AgentTool delegate** | 子 Agent 包成函数工具，调用拿回结果 | **始终在 Main 手里**，可连续委派多个 | ✅ **采用** |
| Workflow Graph | 代码写死的图编排 | 代码决定 | ✗ 那是流水线，不是 orchestrator |

每个 A2A 子 Agent 用 `AgentTool(RemoteA2aAgent(...))` 包成工具，子 Agent 的 description 自动成为工具描述。Main 的 LLM 读这些描述，**自己判断**哪个专家擅长什么、该派给谁——调用顺序是临场判断的，不是写死的。这正是 orchestrator 的含义。

## 组件

- **Main Agent** (`main_agent/`)：总调度 orchestrator。
  - `cli.py`：**交互式入口**（主要使用方式），含 session 管理、思考/工具调用展示、自动上下文压缩。
  - `agent.py`：root agent，从 Agent Registry 动态加载 A2A 子 Agent 并包成 AgentTool delegate 工具。
  - `a2a_server.py`：A2A 服务端点（:8081），被 eino_agent 等外部 Agent 回调。
  - `session.py`：基于 SQLite 的持久化 session（可恢复对话）。
  - `compression.py`：上下文超长时自动摘要压缩（简单版）。
  - `test_orchestration.py`：orchestrator 多步委派验证（笑话链）。
- **Agent Registry** (`agent_registry/`)：轻量 HTTP 配置中心，集中管理所有 Agent 的 endpoints。新增 Agent 只需改 `endpoints.json` + `/reload`。
- **Frontend Agent** (`frontend_agent/`)：Node.js/ADK（Docker），生成可运行的 Vite + React 项目。
- **Mock Backend Agent** (`mock_agent/`)：A2A 后端生成器（:8002），联调用。
- **Demo Agents** (`demo_agents/`)：两个 A2A 子 Agent，用来验证 orchestrator 路由：
  - `comedian_server.py`（:8003）：讲笑话专家。
  - `critic_server.py`（:8004）：笑话评论员。
- **Eino Agent** (`eino_agent/`)：Go + CloudWeGo Eino Agent（:8005），支持天气查询和双向 A2A 调度。
- **File Service** (`main_agent/file_server.py`)：FastAPI 静态服务，:8080 提供产物下载。

## 前置要求

- Docker & Docker Compose
- Node.js 22 + npm
- Python 3.11 + `uv`

## 配置

在项目根创建 `.env`：

```env
OPENAI_BASE_URL=https://open.bigmodel.cn/api/coding/paas/v4
OPENAI_API_KEY=<your-key>
OPENAI_MODEL=glm-4.5-air
FRONTEND_AGENT_URL=http://localhost:8001
BACKEND_AGENT_URL=http://localhost:8002
COMEDIAN_AGENT_URL=http://localhost:8003
CRITIC_AGENT_URL=http://localhost:8004
EINO_AGENT_URL=http://localhost:8005
AGENT_REGISTRY_URL=http://localhost:8006
FILE_SERVER_PORT=8080
# 可选：接入 MCP 工具（JSON 数组）
# MCP_SERVERS='[{"transport":"stdio","command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","."]}]'
```

## 启动

按需起子 Agent（每个都是独立的 A2A 服务），最后起 Main Agent：

```bash
# 1) 后端 mock（:8002）
cd mock_agent && source ../main_agent/.venv/bin/activate && python server.py

# 2) demo 子 Agent（:8003 :8004，验证 orchestrator 路由用）
cd demo_agents && source ../main_agent/.venv/bin/activate
python comedian_server.py   # 新终端
python critic_server.py     # 新终端

# 3) 前端 Agent（:8001，Docker）
cd frontend_agent && npm install && npm run build && cd ..
docker compose up -d --build

# 4) Main Agent（交互式 CLI，主要入口）
cd main_agent
uv venv --python python3.11 .venv && source .venv/bin/activate
uv pip install -r requirements.txt
python cli.py
```

> 没起的子 Agent 不影响 Main 启动——它只在实际调用到该子 Agent 时才会报连不上。

## 使用

进入 CLI 直接对话。Main 会根据任务性质自主路由：
- `帮我做一个 TODO 页面` → 委派前端工具，返回下载链接。
- `帮我写一个 FastAPI 用户注册登录接口` → 委派 backend_agent，返回接口规格。
- `让喜剧演员讲个关于程序员的笑话，再让评论员评价` → Main 自主判断先调 comedian_agent 拿笑话、再调 critic_agent 评价，最后综合回复。
- `现在几点` → 调内置工具。

CLI 命令：`/sessions` `/resume <id>` `/new` `/history` `/compact` `/help` `/quit`。

## 验证 orchestrator 路由

```bash
# 先起 :8003 :8004 两个 demo 子 Agent，再跑：
cd main_agent && python test_orchestration.py
```

发送「讲笑话 + 评价」复合任务，打印 Main 实际调用了哪些子 Agent 工具，确认它是自主判断路由而非流水线。

## 接入真实第三方 Agent（把 mock/demo 换掉）

只要第三方 Agent 暴露 A2A 端点（agent card + `message/send`），在 `agent_registry/endpoints.json` 里加一行：

```json
{
  "name": "third_party_agent",
  "url": "http://third_party_agent:8007",
  "description": "它擅长做什么...",
  "type": "specialist"
}
```

然后调用 `curl -X POST http://localhost:8006/reload`。Main Agent 会在下次轮询时自动发现它，无需改代码、无需重启。

本地开发时如果没有 Registry，也可以直接在 `.env` 里设置 `*_AGENT_URL` 并改 `agent.py` 的 `_build_delegate_tools`（静态方式）。

## Project Layout

```
.
├── docker-compose.yml
├── .env
├── agent_registry/             # Agent 注册中心 (:8006)
│   ├── endpoints.json          # 所有 Agent endpoints 配置
│   ├── server.py
│   └── Dockerfile
├── frontend_agent/             # Node/ADK TS 前端子 Agent (Docker, :8001)
├── mock_agent/                 # Python mock 后端子 Agent (A2A, :8002)
├── demo_agents/                # 两个 A2A 子 Agent，验证 orchestrator 路由
│   ├── comedian_server.py      #   :8003 讲笑话
│   └── critic_server.py        #   :8004 笑话评价
├── eino_agent/                 # Go + CloudWeGo Eino Agent (:8005)
└── main_agent/                 # Python ADK 主 Agent (orchestrator, :8080/:8081)
    ├── cli.py                  # 交互式入口
    ├── agent.py                # root agent + 动态 Registry 加载
    ├── a2a_server.py           # A2A 服务端点
    ├── session.py              # session 持久化
    ├── compression.py          # 上下文压缩
    ├── file_server.py
    ├── test_orchestration.py   # orchestrator 多步委派验证
    └── requirements.txt
```

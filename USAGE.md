# 使用说明（详细版）

本文档介绍如何从零开始搭建、运行并验证 `adk_swarm` Phase 0 PoC：一个基于 Google ADK + A2A 的多 Agent 前端项目生成平台。

---

## 目录

1. [环境要求](#环境要求)
2. [项目结构](#项目结构)
3. [配置文件](#配置文件)
4. [启动前端子 Agent](#启动前端子-agent)
5. [启动主 Agent](#启动主-agent)
6. [交互使用](#交互使用)
7. [验证生成的项目](#验证生成的项目)
8. [常见问题排查](#常见问题排查)
9. [关闭与清理](#关闭与清理)

---

## 环境要求

- **Docker** 29+ 与 **Docker Compose**（用于运行前端子 Agent）
- **Node.js 22** + **npm 10**（本地 TypeScript 编译检查）
- **Python 3.11**（主 Agent 运行环境）
- **uv**（推荐，用于创建 Python 虚拟环境并安装依赖）
- 可用的 OpenAI 兼容 API Key（本项目默认使用 `https://open.bigmodel.cn/api/coding/paas/v4`）

---

## 项目结构

```
adk_swarm/
├── .env                          # 配置文件（需自行创建，已 gitignore）
├── docker-compose.yml            # 前端 Agent 容器编排
├── README.md                     # 项目总览
├── USAGE.md                      # 本文档
├── frontend_agent/               # Node.js / ADK TypeScript 前端子 Agent
│   ├── Dockerfile
│   ├── package.json
│   ├── tsconfig.json
│   └── src/                      # frontend_agent / generator / builder / packer / server
├── mock_agent/                   # Python mock 后端子 Agent（A2A :8002，联调用）
│   ├── agent.py
│   └── server.py
├── demo_agents/                  # 两个 A2A 子 Agent，验证 orchestrator 路由
│   ├── comedian_server.py        #   :8003 讲笑话专家
│   └── critic_server.py          #   :8004 笑话评论员
└── main_agent/                   # Python ADK 主 Agent（orchestrator）
    ├── cli.py                    # 交互式入口（session/思考/工具调用展示/压缩）
    ├── agent.py                  # root agent，用 AgentTool 把 A2A 子 Agent 包成委派工具
    ├── session.py                # 基于 SQLite 的 session 持久化
    ├── compression.py            # 上下文超长自动压缩（简单版）
    ├── file_server.py            # FastAPI 静态文件服务
    ├── test_orchestration.py     # orchestrator 多步委派验证（笑话链）
    ├── test_subagent.py          # 前端/后端路由分流验证
    └── requirements.txt          # Python 依赖
```

---

## 配置文件

在项目根目录创建 `.env`，内容如下：

```env
# OpenAI 兼容 API
OPENAI_BASE_URL=https://open.bigmodel.cn/api/coding/paas/v4
OPENAI_API_KEY=<your-key>
OPENAI_MODEL=glm-4.5-air

# A2A 子 Agent 地址（按需配置；没起的服务不影响主 Agent 启动）
FRONTEND_AGENT_URL=http://localhost:8001   # 前端 Agent（Node, Docker）
BACKEND_AGENT_URL=http://localhost:8002    # 后端 mock Agent
COMEDIAN_AGENT_URL=http://localhost:8003   # 讲笑话 demo Agent
CRITIC_AGENT_URL=http://localhost:8004     # 笑话评论 demo Agent

# 主 Agent 文件服务端口
FILE_SERVER_PORT=8080

# 可选：MCP 工具（JSON 数组，留空则不加载）
# MCP_SERVERS='[{"transport":"stdio","command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","."]}]'
```

> 注意：`.env` 已加入 `.gitignore`，不会进入版本控制。
>
> 架构说明：主 Agent 是 **orchestrator**。每个 A2A 子 Agent 在主 Agent 内被包成一个
> 委派工具（`AgentTool`），其 `description` 成为工具描述。主 Agent 的 LLM 读这些描述，
> **自主判断**该把任务派给谁、什么顺序，调用后拿回结果继续推理——不是流水线，不是写死的路由。

---

## 启动前端子 Agent

前端子 Agent 运行在 Docker 容器中，与主 Agent 通过 HTTP/A2A 通信。

### 1. 本地编译检查（可选但推荐）

```bash
cd frontend_agent
npm install
npm run build
```

如果 `npm run build` 没有报错，说明 TypeScript 类型检查通过。

### 2. 构建并启动容器

```bash
cd ..                          # 回到项目根目录
docker compose up -d --build
```

构建过程会执行 `npm ci` 与 `npm run build`，首次可能较慢。

### 3. 确认服务正常

```bash
curl http://localhost:8001/.well-known/agent-card.json
```

应返回 JSON 格式的 Agent Card，包含 `name: frontend_agent` 与 A2A 端点信息。

### 4. 查看日志

```bash
docker compose logs -f
```

---

## 启动 A2A 子 Agent（mock / demo）

主 Agent 是 orchestrator，下面挂的子 Agent 都是独立的 A2A 服务。按需启动（没起的不影响主 Agent 启动，只在实际调用到时才报连不上）。所有子 Agent 复用 `main_agent` 的虚拟环境。

### 1. 后端 mock Agent（:8002，联调用）

```bash
cd mock_agent
source ../main_agent/.venv/bin/activate
python server.py
```

验证：`curl http://localhost:8002/.well-known/agent-card.json` 应返回 `name: backend_agent`。

### 2. demo 子 Agent（:8003 :8004，验证 orchestrator 路由用）

这两个 Agent（讲笑话、笑话评价）专门用来验证主 Agent 的自主路由能力——比如「让喜剧演员讲个笑话，再让评论员评价」这类需要主 Agent 自己判断先调谁、后调谁的复合任务。

```bash
cd demo_agents
source ../main_agent/.venv/bin/activate
python comedian_server.py   # 终端 A，:8003 讲笑话专家
python critic_server.py     # 终端 B，:8004 笑话评论员
```

验证：`curl http://localhost:8003/.well-known/agent-card.json`、`:8004`。

> 这些 demo/mock 都是占位。等真实第三方 Agent 暴露 A2A 端点后，把 `.env` 里对应的
> `*_AGENT_URL` 指过去，再在 `main_agent/agent.py` 的 `_build_delegate_tools` 加一行 spec 即可，主 Agent 侧无需改路由逻辑。

---

## 启动主 Agent

### 1. 创建并激活 Python 虚拟环境

```bash
cd main_agent
uv venv --python python3.11 .venv
source .venv/bin/activate
```

### 2. 安装依赖

```bash
uv pip install -r requirements.txt
```

如果 `uv` 安装 `google-adk[extensions]` 超时，可以先单独安装 `litellm`：

```bash
uv pip install litellm
```

### 3. 运行方式一：交互式 CLI（推荐，主要使用方式）

```bash
python cli.py
```

这是项目的主要入口。它提供：
- **session 持久化**：对话存到本地 SQLite，可跨进程恢复（`/resume <id>`）。
- **思考展示**：实时显示 LLM 的推理过程（💭）。
- **工具调用展示**：实时显示每次工具调用（🔧）和返回（↩️），包括对子 Agent 的委派。
- **上下文压缩**：对话过长时提示并支持 `/compact` 摘要。
- **MCP 工具**：配置了 `MCP_SERVERS` 即自动加载。

CLI 内命令：`/sessions` `/resume <id>` `/new` `/history` `/compact` `/help` `/quit`。

> 注意：必须在 `main_agent` 目录下运行（`agent.py` 会 `from file_server import ...`，依赖当前工作目录的模块查找）。如需恢复某个 session：`python cli.py --session <id>`。

### 4. 运行方式二：ADK 自带 CLI / Web UI

```bash
adk run .      # ADK 交互式 CLI
adk web .      # Web UI，浏览器打开提示地址
```

这两种方式也能跑，但不会展示自定义的思考/工具渲染，也没有 session 持久化。推荐用方式一的 `cli.py`。

### 5. 运行方式三：非交互式验证脚本

```bash
python test_orchestration.py   # 验证 orchestrator 多步委派（笑话链）
python test_subagent.py        # 验证前端/后端路由分流
```

适合 CI 或快速回归。会打印主 Agent 实际调用了哪些子 Agent 工具，确认路由正确。

---

## 交互使用

启动 `python cli.py` 后，主 Agent 会根据任务性质**自主路由**。试这些：

```
帮我做一个 TODO 页面
```
→ 主 Agent 调用 `generate_frontend_project` 委派前端子 Agent，返回下载链接：
```
http://localhost:8080/artifacts/<session-id>/project.tar.gz
```

```
帮我写一个 FastAPI 的用户注册登录接口
```
→ 主 Agent 判断这是后端任务，委派 backend_agent，返回接口规格。

```
让喜剧演员讲个关于程序员的笑话，再让评论员评价
```
→ 主 Agent **自主判断**先调 comedian_agent 拿笑话、再调 critic_agent 评价，最后综合回复。调用顺序是它临场判断的，不是写死的。

```
现在几点
```
→ 调内置工具 `get_current_time`。

过程中你会看到 🔧 工具调用、↩️ 返回、💭 思考实时打印。下载链接可复制到浏览器或：

```bash
curl -O http://localhost:8080/artifacts/<session-id>/project.tar.gz
```

---

## 验证生成的项目

下载 `project.tar.gz` 后：

```bash
mkdir todo_app && cd todo_app
tar -xzf /path/to/project.tar.gz
npm install
npm run dev
```

浏览器访问 `http://localhost:5173`，应能看到 TODO 页面正常工作。

> 前端子 Agent 在容器内部已经完成了 `npm install`、`npm run build` 和 dev server 200 校验，因此解压后的项目应当是可直接运行的。

---

## 常见问题排查

### 1. 前端 Agent 返回 `package.json` 缺失

这是部分 LLM 会把 `package.json` 截断成 `package.` 的已知现象。代码中已做兜底：如果生成的文件里只有 `package.`，会自动重命名为 `package.json`。如果仍然失败，请检查 `frontend_agent/src/generator.ts` 的日志。

### 2. Dev server 验证失败

容器内 `localhost` 可能解析到 IPv6，而 Vite 默认只绑定 IPv4。代码中已强制使用 `--host 127.0.0.1` 并用 `http://127.0.0.1:5173` 轮询。如果仍然失败，检查：

```bash
docker compose logs -f
```

看是否有端口占用或 build 报错。

### 3. 主 Agent 文件服务无法访问

`agent.py` 在模块导入时会自动在后台线程启动 FastAPI 文件服务。只要主 Agent 进程在运行，`http://localhost:8080/artifacts/` 就应可访问。如果退出 `adk run`，文件服务也会停止，但已生成的文件仍保留在 `main_agent/artifacts/` 中。

### 4. A2A 调用返回 `Method not found`

本项目使用 `@a2a-js/sdk` 暴露的 A2A 端点，发送方法为 `message/send`，端点为 `/jsonrpc`。不要直接使用标准 A2A 草案中的 `tasks/send`。

### 5. Python 依赖安装慢或失败

如果 `uv pip install -r requirements.txt` 长时间无响应，建议：

```bash
uv pip install google-adk[a2a]
uv pip install litellm
uv pip install fastapi uvicorn[standard] python-dotenv requests
```

分步安装。

---

## 关闭与清理

### 停止前端 Agent 容器

```bash
cd /path/to/adk_swarm
docker compose down
```

### 删除已生成的前端项目（释放磁盘）

```bash
rm -rf main_agent/artifacts/*
```

### 删除虚拟环境（如需完全重装）

```bash
cd main_agent
rm -rf .venv
```

---

## 下一步建议

- 接入更多子 Agent（后端、数据库、测试等）。
- 为主 Agent 增加任务级日志与审计记录。
- 将主 Agent 也容器化，统一通过 `docker compose` 管理。
- 增加工作流模板，支持“生成前端 + 生成后端 + 联调”等组合任务。

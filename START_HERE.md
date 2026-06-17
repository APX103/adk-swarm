# 启动指南（明天早上直接用这个）

## 最快启动（90 秒，只要 CLI 聊天）

```bash
cd /Users/lijialun/work/adk_swarm/main_agent
source .venv/bin/activate
python cli.py
```

这样就能聊了。你可以：
- 直接打字聊天
- 输入 `查一下北京天气` → 看到 💭思考 → 🔧工具调用(get_weather) → ↩️结果 → 📝回复
- 输入 `现在几点` → 看到 🔧工具调用(get_current_time)
- 输入 `/quit` 退出

session 自动保存到 SQLite。下次想接着聊：
```bash
python cli.py                          # 新 session
/sessions                              # 列出历史 session
/resume <session-id>                   # 恢复之前的对话
```

## 完整启动（含子 Agent 调度演示）

每个终端跑一个服务，最后跑 CLI。如果直接用 Docker Compose，则 Registry 和所有子 Agent 都已编排好：

```bash
# 方式 A：Docker Compose 一键启动（推荐）
cd /Users/lijialun/work/adk_swarm
docker compose up -d --build

# 方式 B：手动逐个启动
# 终端 1：Agent Registry
cd /Users/lijialun/work/adk_swarm/agent_registry
source ../main_agent/.venv/bin/activate
python server.py

# 终端 2：讲笑话子 Agent
cd /Users/lijialun/work/adk_swarm/demo_agents
source ../main_agent/.venv/bin/activate
python comedian_server.py

# 终端 3：笑话评论子 Agent
cd /Users/lijialun/work/adk_swarm/demo_agents
source ../main_agent/.venv/bin/activate
python critic_server.py

# 终端 4：后端生成 mock 子 Agent
cd /Users/lijialun/work/adk_swarm/mock_agent
source ../main_agent/.venv/bin/activate
python server.py

# 终端 5：前端子 Agent（可选，需要 Docker）
cd /Users/lijialun/work/adk_swarm/frontend_agent
npm install && npm run build && cd ..
docker compose up -d --build frontend_agent

# 终端 6：主 Agent CLI
cd /Users/lijialun/work/adk_swarm/main_agent
source .venv/bin/activate
python cli.py
```

然后在 CLI 里试这些：
```
你好                              → 普通聊天，看 💭 思考过程
查一下上海天气                    → MCP 工具调用，看 🔧 ↩️ 全链路
现在几点                          → 内置工具调用
让喜剧演员讲个关于程序员的笑话     → Main 自动判断 → 委派 comedian_agent
让喜剧演员讲个笑话，然后让评论员评价 → 多步编排：Main → comedian → critic
帮我写一个 FastAPI 用户注册接口   → 委派 backend_agent
```

## 你能看到什么

每一轮对话，CLI 实时显示：
- **💭 思考**（灰色）：模型的推理过程
- **🔧 工具调用**（黄色）：调用了哪个工具、传了什么参数
- **↩️ 工具返回**（绿色）：工具返回的结果
- **📝 回复**（青色）：最终给用户的回答

## Session 管理

| 命令 | 作用 |
|------|------|
| `/sessions` | 列出所有历史 session |
| `/resume <id>` | 恢复某个 session（继续之前的对话） |
| `/new` | 开新 session |
| `/history` | 看当前 session 的历史记录 |
| `/compact` | 压缩上下文（对话太长时用） |
| `/help` | 帮助 |
| `/quit` | 退出 |

对话历史存在 `main_agent/sessions.db`（SQLite），重启后用 `/resume` 恢复。

## 前提

- `.env` 在项目根目录，里面有 `OPENAI_API_KEY` 和 `OPENAI_BASE_URL`（已配好）
- `AGENT_REGISTRY_URL` 已配置（手动启动时默认 `http://localhost:8006`）
- `MCP_SERVERS` 已配好天气查询工具（已配好）
- 子 Agent 没起也没关系——CLI 照常启动，只是调到那个子 Agent 时会报连不上。如果使用 Registry，未注册的 Agent 对 main_agent 不可见。

## 故障排查

| 问题 | 解决 |
|------|------|
| `ModuleNotFoundError: No module named 'agent'` | 确认 `cd main_agent` 后再跑 `python cli.py` |
| 天气查询没反应 | MCP 工具没加载，检查 `.env` 里 `MCP_SERVERS` 路径 |
| 子 Agent 连不上 | 对应的 `python xxx_server.py` 没启动 |
| `sqlite` 报错 | 确认装了 `aiosqlite`：`uv pip install aiosqlite greenlet` |

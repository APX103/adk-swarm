# 快速开始（30 秒上手）

## 1. 配置

在项目根目录创建 `.env`：

```env
OPENAI_BASE_URL=https://open.bigmodel.cn/api/coding/paas/v4
OPENAI_API_KEY=你的API_KEY
OPENAI_MODEL=glm-4.5-air
FRONTEND_AGENT_URL=http://localhost:8001
FILE_SERVER_PORT=8080
```

## 2. 启动前端 Agent

```bash
cd frontend_agent
npm install && npm run build
cd ..
docker compose up -d --build
```

## 3. 启动主 Agent

```bash
cd main_agent
uv venv --python python3.11 .venv
source .venv/bin/activate
uv pip install -r requirements.txt
adk run .
```

## 4. 使用

在终端输入：

```
帮我做一个 TODO 页面
```

复制返回的下载链接，解压即可得到可运行的 Vite + React 项目。

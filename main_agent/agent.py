"""Main (root) agent: an orchestrator that delegates to specialist A2A
sub-agents and tools.

Architecture: orchestrator + AgentTool delegate (NOT transfer, NOT pipeline).
Each remote A2A sub-agent is wrapped in an AgentTool; the agent's description
becomes the tool description, so the LLM reads those descriptions and decides
for itself which sub-agent to call (and in what order) based on who is best at
the task. Control always stays with this agent: it calls a sub-agent, gets the
result back as a tool response, and continues reasoning.

Capabilities:
  * Interactive entry with session persistence / thinking & tool-call rendering
    (see cli.py).
  * Real tools: generate_frontend_project, get_current_time.
  * Remote A2A sub-agents as delegate tools (AgentTool(RemoteA2aAgent)):
      backend_agent  -> mock_agent on :8002 (FastAPI 后端生成)
      comedian_agent -> demo_agents on :8003 (讲笑话)
      critic_agent   -> demo_agents on :8004 (笑话评价)
  * Optional MCP tools (McpToolset), configured via MCP_SERVERS env var.
  * Context compression for long conversations (see compression.py + cli.py).
"""

import os
import re
import threading
import uuid
from pathlib import Path
from typing import Optional

import requests
from dotenv import load_dotenv
from google.adk.agents import Agent
from google.adk.agents.remote_a2a_agent import RemoteA2aAgent
from google.adk.models.lite_llm import LiteLlm
from google.adk.tools.agent_tool import AgentTool
from google.adk.tools.tool_context import ToolContext

# file_server is a sibling module. With __init__.py present, the package import
# works whether run as `python cli.py` (cwd on path) or `adk web .` (package mode).
try:
    from file_server import ARTIFACTS_DIR, run_file_server
except ImportError:
    from main_agent.file_server import ARTIFACTS_DIR, run_file_server

load_dotenv()

# Configure OpenAI-compatible endpoint used by LiteLlm via litellm.
os.environ.setdefault("OPENAI_API_KEY", os.getenv("OPENAI_API_KEY", ""))
os.environ.setdefault("OPENAI_API_BASE", os.getenv("OPENAI_BASE_URL", ""))

MODEL_NAME = os.getenv("OPENAI_MODEL", "glm-4.5-air")
FRONTEND_AGENT_URL = os.getenv("FRONTEND_AGENT_URL", "http://localhost:8001")
BACKEND_AGENT_URL = os.getenv("BACKEND_AGENT_URL", "http://localhost:8002")
COMEDIAN_AGENT_URL = os.getenv("COMEDIAN_AGENT_URL", "http://localhost:8003")
CRITIC_AGENT_URL = os.getenv("CRITIC_AGENT_URL", "http://localhost:8004")
FILE_SERVER_PORT = int(os.getenv("FILE_SERVER_PORT", "8080"))


def _start_file_server():
    thread = threading.Thread(
        target=run_file_server,
        kwargs={"port": FILE_SERVER_PORT},
        daemon=True,
    )
    thread.start()


def _extract_url(text: str) -> Optional[str]:
    if not text:
        return None
    match = re.search(
        r'https?://[^\s">\')\]]+project\.tar\.gz',
        text,
        re.IGNORECASE,
    )
    return match.group(0) if match else None


def _download_artifact(url: str, save_path: str) -> bool:
    try:
        response = requests.get(url, stream=True, timeout=300)
        response.raise_for_status()
        Path(save_path).parent.mkdir(parents=True, exist_ok=True)
        with open(save_path, "wb") as f:
            for chunk in response.iter_content(chunk_size=8192):
                if chunk:
                    f.write(chunk)
        return True
    except Exception as e:
        print(f"[agent] artifact download failed from {url}: {e}")
        return False


def _task_text(result: dict) -> str:
    """Extract all text from an A2A task/message result."""
    parts = []
    # message/send returns a Message (or Task) at the top level.
    for key in ("message", "status", "result"):
        node = result.get(key) if isinstance(result, dict) else None
        if isinstance(node, dict):
            for part in node.get("parts", []):
                if part.get("text"):
                    parts.append(part["text"])
    for part in result.get("parts", []) or []:
        if isinstance(part, dict) and part.get("text"):
            parts.append(part["text"])
    for artifact in result.get("artifacts", []) or []:
        for part in artifact.get("parts", []) or []:
            if part.get("text"):
                parts.append(part["text"])
    return "\n".join(parts)


def _call_a2a_agent(base_url: str, message: str) -> str:
    """Send message/send to an A2A agent and return its concatenated text reply.

    Tries both the Node-style /jsonrpc path and the Python a2a sdk root "/" path
    so it works against either implementation of the remote agent. Only accepts
    responses that look like valid JSON-RPC (have result or error) to avoid
    silently treating an unrelated 200 as an A2A reply.
    """
    payload = {
        "jsonrpc": "2.0",
        "id": str(uuid.uuid4()),
        "method": "message/send",
        "params": {
            "message": {
                "messageId": str(uuid.uuid4()),
                "role": "user",
                "parts": [{"kind": "text", "text": message}],
            }
        },
    }
    endpoints = [f"{base_url}/jsonrpc", f"{base_url}/"]
    last_err = None
    data = None
    for url in endpoints:
        try:
            response = requests.post(url, json=payload, timeout=600)
            response.raise_for_status()
            candidate = response.json()
            # Reject responses that aren't JSON-RPC (no result/error keys) so the
            # "/" fallback doesn't silently accept a random server's 200.
            if not isinstance(candidate, dict) or ("result" not in candidate and "error" not in candidate):
                last_err = f"non-JSONRPC response from {url}"
                continue
            data = candidate
            break
        except Exception as e:
            last_err = e
            continue
    if data is None:
        raise RuntimeError(f"Could not reach A2A agent at {base_url}: {last_err}")
    if data.get("error"):
        raise RuntimeError(f"A2A agent error: {data['error']}")
    return _task_text(data.get("result", {}))


def generate_frontend_project(request: str, tool_context: ToolContext) -> str:
    """Call the remote frontend A2A agent and return a local download URL.

    The frontend agent (Node/ADK in Docker) generates a runnable Vite + React
    project, verifies it, packs it, and returns a download URL. We pull the
    archive into our local file server so the user gets a stable local link.
    """
    session_id = getattr(tool_context.session, "id", str(uuid.uuid4()))
    artifact_text = _call_a2a_agent(FRONTEND_AGENT_URL, request)
    artifact_url = _extract_url(artifact_text)
    if not artifact_url:
        raise RuntimeError(f"Frontend agent did not return a download URL. Response: {artifact_text}")

    save_dir = os.path.join(ARTIFACTS_DIR, session_id)
    save_path = os.path.join(save_dir, "project.tar.gz")
    if not _download_artifact(artifact_url, save_path):
        raise RuntimeError(f"Failed to download artifact from {artifact_url}")

    return f"http://localhost:{FILE_SERVER_PORT}/artifacts/{session_id}/project.tar.gz"


def get_current_time() -> str:
    """Return the current wall-clock time. Handy for the agent to answer '现在几点'."""
    from datetime import datetime

    return datetime.now().strftime("%Y-%m-%d %H:%M:%S")


def _build_delegate_tools() -> list:
    """Wrap each remote A2A sub-agent as an AgentTool (ADK native delegate mode).

    This is the orchestrator pattern: each sub-agent becomes a function tool whose
    description is the agent's own description. The main agent reads those
    descriptions and decides for itself which sub-agent to call (and in what
    order) based on who is best at the task — no hardcoded routing or pipeline.
    Control always stays with the main agent: it calls a sub-agent, gets the
    result back as a tool response, and continues reasoning (may call more).

    Card resolution is lazy, so a sub-agent server not up at import time won't
    crash; invoking it while down surfaces a clear error.
    """
    specs = [
        (
            "backend_agent",
            BACKEND_AGENT_URL,
            "后端服务生成专家：用 FastAPI / Python 设计 REST 接口、数据模型、可运行的后端项目骨架。"
            "遇到后端/服务端/接口 API 相关需求时调用本工具委派给它。",
        ),
        (
            "comedian_agent",
            COMEDIAN_AGENT_URL,
            "一个讲笑话的子 Agent。给它一个主题，它会讲一个简短的中文笑话。"
            "当需要有人讲笑话或创作幽默内容时调用本工具委派给它。",
        ),
        (
            "critic_agent",
            CRITIC_AGENT_URL,
            "一个笑话评论员子 Agent。给它一段笑话，它会给出简短评价（好不好笑、打分、理由）。"
            "当需要评价某个笑话时调用本工具委派给它。",
        ),
    ]

    tools: list = []
    for name, base_url, desc in specs:
        try:
            ra = RemoteA2aAgent(
                name=name,
                agent_card=base_url.rstrip("/"),
                description=desc,
            )
            # AgentTool turns the sub-agent into a callable tool; the LLM sees
            # `desc` as the tool description and routes to it autonomously.
            tools.append(AgentTool(agent=ra))
        except Exception as e:
            print(f"[agent] warning: could not init AgentTool for {name}: {e}")
    return tools


def _load_mcp_tools() -> list:
    """Load tools from MCP servers configured via MCP_SERVERS env var.

    Configure with a JSON array, e.g.:
      MCP_SERVERS='[{"transport":"stdio","command":"python","args":["tools/weather_mcp.py"]}]'

    Any MCP server you point at becomes a set of tools the agent can call.
    Uses asyncio to handle the async McpToolset initialization at import time.
    """
    import asyncio
    import json
    import sys

    from google.adk.tools.mcp_tool.mcp_session_manager import (
        SseServerParams,
        StdioConnectionParams,
        StdioServerParameters,
    )
    from google.adk.tools.mcp_tool.mcp_toolset import McpToolset

    raw = os.getenv("MCP_SERVERS", "").strip()
    if not raw:
        return []
    try:
        servers = json.loads(raw)
    except json.JSONDecodeError as e:
        print(f"[agent] MCP_SERVERS is not valid JSON, skipping MCP tools: {e}")
        return []

    async def _load_all():
        tools: list = []
        for idx, spec in enumerate(servers):
            try:
                transport = spec.get("transport", "stdio")
                if transport == "stdio":
                    # Use sys.executable if command is "python" (not on PATH reliably)
                    cmd = spec["command"]
                    if cmd in ("python", "python3"):
                        cmd = sys.executable
                    server_params = StdioServerParameters(
                        command=cmd,
                        args=list(spec.get("args", [])),
                        env=spec.get("env"),
                    )
                    conn_params = StdioConnectionParams(server_params=server_params)
                elif transport in ("sse", "streamable_http"):
                    conn_params = SseServerParams(url=spec["url"])
                else:
                    print(f"[agent] unknown MCP transport '{transport}', skipping")
                    continue
                prefix = spec.get("tool_name_prefix") or f"mcp{idx}"
                toolset = McpToolset(connection_params=conn_params, tool_name_prefix=prefix)
                mcp_tools = await toolset.get_tools()
                tools.extend(mcp_tools)
                name = spec.get("name", spec.get("command", spec.get("url", "")))
                print(f"[agent] loaded MCP server '{name}' -> {len(mcp_tools)} tools")
            except Exception as e:
                print(f"[agent] failed to load MCP server #{idx}: {e}")
        return tools

    try:
        loop = asyncio.get_event_loop()
        if loop.is_running():
            # Already in an event loop (shouldn't happen at import time, but guard)
            return []
    except RuntimeError:
        pass
    return asyncio.run(_load_all())


_DELEGATE_TOOLS = _build_delegate_tools()
_MCP_TOOLS = _load_mcp_tools()

root_agent = Agent(
    name="main_agent",
    model=LiteLlm(model=f"openai/{MODEL_NAME}"),
    description="面向用户的总调度 Agent（orchestrator），能聊天、调用工具，并把任务委派给各领域的专家子 Agent。",
    instruction="""你是面向用户的总调度 Agent（orchestrator）。你直接和用户对话，并根据任务需要调度你的能力。

## 调度原则（核心）

你是一个 orchestrator（调度员），不是一个流水线。你的工具列表里有一组专家子 Agent，每个子 Agent 的工具描述写明了它擅长什么。
你的职责是：听懂用户要什么，判断这件事该交给哪个（或哪几个）专家子 Agent 来做，然后调用对应的工具委派给它，
拿到结果后继续判断是否还需要调用别的专家，最后把结果整理给用户。

路由判断完全由你自己根据各子 Agent 的工具描述来做——不要假设固定的调用顺序，不要替专家做它们擅长的事。
如果一个任务需要多个专家配合（例如"讲个笑话并评价它"），你应当：先判断哪个专家负责第一部分、调用它拿结果；
再根据结果判断下一步交给哪个专家、调用它；最后综合呈现。调用顺序由你临场判断，不是写死的。

## 重要约束

- 每个子 Agent 产出的内容（笑话、评价、后端规格、前端项目等），必须通过实际调用对应工具来获得，**不允许自己编造**。
- 你是调度员，不自己写前端/后端代码，也不自己讲笑话/做评价——这些交给对应的专家。
- 子 Agent 返回后，用简洁的中文转述给用户；关键内容（笑话、评价、接口规格、下载链接）要清晰呈现。
- **如果子 Agent 调用失败、报错或返回空结果，必须如实告诉用户"该子 Agent 当前不可用或返回异常"，不要编造结果假装成功。**
- 通用问题（闲聊、查时间等）直接回答即可，不必每次都调工具。
""",
    tools=[generate_frontend_project, get_current_time, *_DELEGATE_TOOLS, *_MCP_TOOLS],
)

# Start the static file server on import so it is available for `adk web` as well.
_start_file_server()

if __name__ == "__main__":
    print(f"File server started at http://localhost:{FILE_SERVER_PORT}")
    print("Run 'python main_agent/cli.py' to start the interactive session.")

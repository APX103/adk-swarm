"""Mock A2A agent that pretends to be a FastAPI/Python backend generator.

This is a stand-in for a real third-party agent (one that currently only ships
as a CLI or MCP server) so we can develop and debug the A2A integration against
the main agent without waiting for the real agent to expose an A2A endpoint.

Protocol-wise it is identical to the real thing: it speaks A2A (message/send),
publishes an agent card, and returns text answers + a small "artifact" describing
the backend it "generated". Swap the URL on the main agent side when the real one
is ready.
"""

import os

from dotenv import load_dotenv
from google.adk.agents import Agent
from google.adk.models.lite_llm import LiteLlm
from google.adk.tools.tool_context import ToolContext

load_dotenv()

# Reuse the same OpenAI-compatible endpoint as the rest of the project.
os.environ.setdefault("OPENAI_API_KEY", os.getenv("OPENAI_API_KEY", ""))
os.environ.setdefault("OPENAI_API_BASE", os.getenv("OPENAI_BASE_URL", ""))

MODEL_NAME = os.getenv("OPENAI_MODEL", "glm-4.5-air")


def generate_backend_spec(request: str, tool_context: ToolContext) -> str:
    """Produce a structured FastAPI backend spec tailored to the request.

    Parses the request for common backend intents (auth/users, CRUD on a
    resource, search, etc.) and builds a matching route table. A real backend
    generator would scaffold files and return a download URL; here we return a
    compact text spec so the round-trip (request -> tool -> answer + artifact)
    can be verified end to end against the main agent.
    """
    import json
    import re
    import uuid

    text = (request or "").strip()
    low = text.lower()

    # 1. Guess the resource name from the request (default: "resource").
    resource = "resource"
    for cand in re.findall(r"[\u4e00-\u9fa5A-Za-z_]+", text):
        if cand.lower() in {"fastapi", "python", "接口", "服务", "后端", "backend", "api", "rest", "一个", "写", "帮我", "生成"}:
            continue
        resource = cand
        break

    # 2. Pick endpoints based on detected intent.
    endpoints = []
    if any(k in low for k in ("登录", "注册", "auth", "login", "register", "signup", "jwt", "token")):
        endpoints += [
            {"method": "POST", "path": "/api/v1/auth/register", "desc": "用户注册"},
            {"method": "POST", "path": "/api/v1/auth/login", "desc": "用户登录，返回 JWT"},
            {"method": "GET", "path": "/api/v1/auth/me", "desc": "获取当前用户信息"},
        ]
    crud_methods = [
        ("POST", f"/api/v1/{resource}s", f"创建 {resource}"),
        ("GET", f"/api/v1/{resource}s", f"列出 {resource}"),
        ("GET", f"/api/v1/{resource}s/{{id}}", f"获取单个 {resource}"),
        ("PUT", f"/api/v1/{resource}s/{{id}}", f"更新 {resource}"),
        ("DELETE", f"/api/v1/{resource}s/{{id}}", f"删除 {resource}"),
    ]
    endpoints += crud_methods
    if any(k in low for k in ("搜索", "查询", "search", "filter", "find")):
        endpoints.append({"method": "GET", "path": f"/api/v1/{resource}s/search", "desc": f"搜索 {resource}"})

    # 3. Data models roughly matching the endpoints.
    models = [f"{resource.capitalize()}(BaseModel): id: int; name: str; created_at: datetime"]
    if endpoints and "auth" in str(endpoints):
        models += ["UserCreate(BaseModel): username: str; email: EmailStr; password: str", "Token(BaseModel): access_token: str; token_type: str"]

    spec = {
        "framework": "FastAPI",
        "python": "3.11",
        "module": "app.main:app",
        "resource": resource,
        "endpoints": endpoints,
        "models": models,
        "task_id": str(uuid.uuid4()),
    }
    # Stash it in session state so it behaves like a produced artifact.
    tool_context.state["backend_spec"] = spec
    return json.dumps(spec, ensure_ascii=False, indent=2)


# A BaseAgent exposed via to_a2a() will auto-build its agent card from these.
backend_agent = Agent(
    name="backend_agent",
    model=LiteLlm(model=f"openai/{MODEL_NAME}"),
    description=(
        "A specialist agent that designs and generates FastAPI / Python backend "
        "services: REST APIs, data models, route definitions, and runnable backend "
        "project skeletons. Delegate backend/server-side work to this agent."
    ),
    instruction=(
        "你是一个后端服务生成专家，擅长用 FastAPI / Python 设计 REST 接口。"
        "收到后端相关需求时，调用工具 `generate_backend_spec` 生成一份结构化的后端规格"
        "（接口、数据模型、模块入口），然后把工具返回的规格用简洁的中文总结给用户，"
        "并附上 task_id。不要自己臆造代码，所有规格以工具结果为准。"
        "如果用户的请求与后端无关，请直接说明这一点。"
    ),
    tools=[generate_backend_spec],
)

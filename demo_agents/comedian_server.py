"""Joke-teller sub-agent #1, exposed over A2A on :8003.

This is a deliberately small, self-contained agent used to prove the
orchestration chain: Main -> comedian -> Main -> critic -> Main.
"""

import asyncio
import os

from dotenv import load_dotenv
from google.adk.a2a.utils.agent_to_a2a import AgentCardBuilder, to_a2a
from google.adk.agents import Agent
from google.adk.models.lite_llm import LiteLlm

from _register import register_self

load_dotenv()
os.environ.setdefault("OPENAI_API_KEY", os.getenv("OPENAI_API_KEY", ""))
os.environ.setdefault("OPENAI_API_BASE", os.getenv("OPENAI_BASE_URL", ""))
MODEL = os.getenv("OPENAI_MODEL", "glm-4.5-air")

HOST = os.getenv("COMEDIAN_HOST", "0.0.0.0")
PORT = int(os.getenv("COMEDIAN_PORT", "8003"))
SERVICE_NAME = os.getenv("SERVICE_NAME", "comedian")

comedian_agent = Agent(
    name="comedian_agent",
    model=LiteLlm(model=f"openai/{MODEL}"),
    description=(
        "一个讲笑话的子 Agent。给它一个主题，它会讲一个简短的中文笑话。"
        "当主 Agent 需要有人讲笑话时，应委派给它。"
    ),
    instruction=(
        "你是一个脱口秀演员，擅长讲简短好笑的中文笑话。"
        "收到请求后，根据用户给的主题讲一个 2-3 句的笑话，直接输出笑话本身，"
        "不要加多余的开场白。"
    ),
    tools=[],
)

_card = asyncio.run(
    AgentCardBuilder(agent=comedian_agent, rpc_url=f"http://{SERVICE_NAME}:{PORT}").build()
)

# Register self into the registry so main_agent/eino_agent can discover us.
register_self(
    name="comedian_agent",
    service_name=SERVICE_NAME,
    port=PORT,
    description=comedian_agent.description,
)

app = to_a2a(
    comedian_agent,
    host=HOST,
    port=PORT,
    protocol="http",
    agent_card=_card,
)

if __name__ == "__main__":
    import uvicorn

    print(f"[comedian] A2A server on http://{HOST}:{PORT}")
    print(f"[comedian] agent card: http://{HOST}:{PORT}/.well-known/agent-card.json")
    uvicorn.run(app, host=HOST, port=PORT, log_level="info")

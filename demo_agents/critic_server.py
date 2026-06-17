"""Joke-critic sub-agent #2, exposed over A2A on :8004.

Evaluates a joke handed to it. Used to prove the orchestration chain:
Main -> comedian -> Main -> critic -> Main.
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
os.environ.setdefault("OPENAI_BASE", os.getenv("OPENAI_BASE_URL", ""))
MODEL = os.getenv("OPENAI_MODEL", "glm-4.5-air")

HOST = os.getenv("CRITIC_HOST", "0.0.0.0")
PORT = int(os.getenv("CRITIC_PORT", "8004"))
SERVICE_NAME = os.getenv("SERVICE_NAME", "critic")

critic_agent = Agent(
    name="critic_agent",
    model=LiteLlm(model=f"openai/{MODEL}"),
    description=(
        "一个笑话评论员子 Agent。给它一段笑话，它会给出简短的评价（好不好笑、打分、理由）。"
        "当主 Agent 需要评价某个笑话时，应委派给它。"
    ),
    instruction=(
        "你是一个挑剔但公正的笑话评论员。用户会给你一段笑话，"
        "你要用 2-3 句话评价它：好不好笑（1-10 分）、亮点、不足。直接给评价，不要寒暄。"
    ),
    tools=[],
)

_card = asyncio.run(
    AgentCardBuilder(agent=critic_agent, rpc_url=f"http://{SERVICE_NAME}:{PORT}").build()
)

# Register self into the registry so main_agent/eino_agent can discover us.
register_self(
    name="critic_agent",
    service_name=SERVICE_NAME,
    port=PORT,
    description=critic_agent.description,
)

app = to_a2a(
    critic_agent,
    host=HOST,
    port=PORT,
    protocol="http",
    agent_card=_card,
)

if __name__ == "__main__":
    import uvicorn

    print(f"[critic] A2A server on http://{HOST}:{PORT}")
    print(f"[critic] agent card: http://{HOST}:{PORT}/.well-known/agent-card.json")
    uvicorn.run(app, host=HOST, port=PORT, log_level="info")

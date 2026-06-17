"""A2A server entry point for the mock backend agent.

Run with:  python mock_agent/server.py
It exposes the agent at http://localhost:<PORT>/ with an auto-generated agent
card at /.well-known/agent-card.json, speaking A2A (message/send).
"""

import asyncio
import os

from dotenv import load_dotenv
from google.adk.a2a.utils.agent_to_a2a import AgentCardBuilder, to_a2a

# Support both `python server.py` from mock_agent/ and `python mock_agent/server.py` from root.
try:
    from agent import backend_agent
except ImportError:
    from mock_agent.agent import backend_agent

from _register import register_self

load_dotenv()

HOST = os.getenv("MOCK_AGENT_HOST", "0.0.0.0")
PORT = int(os.getenv("MOCK_AGENT_PORT", "8002"))
# Service name for agent card URL (used by other agents to reach us).
SERVICE_NAME = os.getenv("SERVICE_NAME", "mock_agent")

# Build agent card with the correct RPC URL so other services can reach us
# inside the Docker network (not 0.0.0.0 or localhost).
_card = asyncio.run(
    AgentCardBuilder(agent=backend_agent, rpc_url=f"http://{SERVICE_NAME}:{PORT}").build()
)

# Register self into the registry so main_agent/eino_agent can discover us.
register_self(
    name="backend_agent",
    service_name=SERVICE_NAME,
    port=PORT,
    description=getattr(backend_agent, "description", "Backend specialist agent."),
)

app = to_a2a(
    backend_agent,
    host=HOST,
    port=PORT,
    protocol="http",
    agent_card=_card,
)

if __name__ == "__main__":
    import uvicorn

    print(f"[mock_agent] backend_agent A2A server on http://{HOST}:{PORT}")
    print(f"[mock_agent] agent card: http://{HOST}:{PORT}/.well-known/agent-card.json")
    uvicorn.run(app, host=HOST, port=PORT, log_level="info")

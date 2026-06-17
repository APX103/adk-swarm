"""A2A server entry point for the mock backend agent.

Run with:  python mock_agent/server.py
It exposes the agent at http://localhost:<PORT>/ with an auto-generated agent
card at /.well-known/agent-card.json, speaking A2A (message/send).
"""

import os

from dotenv import load_dotenv
from google.adk.a2a.utils.agent_to_a2a import to_a2a

# Support both `python server.py` from mock_agent/ and `python mock_agent/server.py` from root.
try:
    from agent import backend_agent
except ImportError:
    from mock_agent.agent import backend_agent

load_dotenv()

HOST = os.getenv("MOCK_AGENT_HOST", "0.0.0.0")
PORT = int(os.getenv("MOCK_AGENT_PORT", "8002"))

# One call wraps the BaseAgent in a full A2A Starlette app: agent card +
# /jsonrpc endpoint + everything the main agent's RemoteA2aAgent will talk to.
app = to_a2a(
    backend_agent,
    host=HOST,
    port=PORT,
    protocol="http",
)


if __name__ == "__main__":
    import uvicorn

    print(f"[mock_agent] backend_agent A2A server on http://{HOST}:{PORT}")
    print(f"[mock_agent] agent card: http://{HOST}:{PORT}/.well-known/agent-card.json")
    uvicorn.run(app, host=HOST, port=PORT, log_level="info")

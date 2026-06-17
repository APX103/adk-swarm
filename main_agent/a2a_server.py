"""A2A server for main_agent, runs alongside adk web on a separate port.

This allows other agents (like eino_agent) to call main_agent via A2A protocol.
Uses the same root_agent instance, just exposed as an A2A JSON-RPC endpoint.
"""

import os
import threading


def start_a2a_server(agent, port: int = 8081):
    """Start an A2A server wrapping *agent* in a daemon thread on *port*.

    All async/import work happens inside the thread to avoid event-loop
    conflicts with the caller (e.g. adk web's uvicorn).
    """

    def _run():
        import asyncio

        import uvicorn
        from google.adk.a2a.utils.agent_to_a2a import AgentCardBuilder, to_a2a

        service_name = os.getenv("SERVICE_NAME", "main_agent")
        card = asyncio.run(
            AgentCardBuilder(
                agent=agent, rpc_url=f"http://{service_name}:{port}"
            ).build()
        )
        app = to_a2a(
            agent,
            host="0.0.0.0",
            port=port,
            protocol="http",
            agent_card=card,
        )
        print(f"[main_agent] A2A server ready on http://0.0.0.0:{port}")
        print(
            f"[main_agent] A2A agent card: http://0.0.0.0:{port}/.well-known/agent-card.json"
        )
        uvicorn.run(app, host="0.0.0.0", port=port, log_level="info")

    thread = threading.Thread(target=_run, daemon=True, name="a2a-server")
    thread.start()
    print(f"[main_agent] A2A server thread started (port {port})")
    return thread

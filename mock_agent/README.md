# Mock Backend Agent (A2A)

A stand-in for a real third-party backend-generation agent so you can develop
and debug the A2A integration against the main agent without waiting for the
real agent to expose an A2A endpoint.

It pretends to be a **FastAPI / Python backend generator**. Protocol-wise it is
identical to a real A2A agent: it speaks `message/send`, publishes an agent card,
and returns text answers + a structured backend spec. When the real agent is
ready, point `BACKEND_AGENT_URL` at it and swap this out — nothing on the main
agent side needs to change.

## Run

From the repo root, using the main agent's venv:

```bash
cd mock_agent
source ../main_agent/.venv/bin/activate   # reuse the installed deps
python server.py
```

You should see:

```
[mock_agent] backend_agent A2A server on http://0.0.0.0:8002
[mock_agent] agent card: http://0.0.0.0:8002/.well-known/agent-card.json
```

## Verify on its own

```bash
# agent card
curl http://localhost:8002/.well-known/agent-card.json | jq .name   # -> "backend_agent"

# send a request (note: Python a2a SDK serves JSON-RPC at the root "/")
curl -X POST http://localhost:8002/ \
  -H 'content-type: application/json' \
  -d '{"jsonrpc":"2.0","id":"1","method":"message/send","params":{"message":{"messageId":"m1","role":"user","parts":[{"kind":"text","text":"帮我写一个 FastAPI 的用户注册登录接口"}]}}}'
```

## How it fits in

The main agent wraps this in an `AgentTool` (delegate mode, not transfer).
When you ask the main CLI for something backend-related
("帮我写一个 FastAPI 的用户注册登录接口"), the orchestrator reads this agent's
description, decides backend work should go here, calls it as a tool, and relays
the answer back. See `main_agent/test_subagent.py` for the routing check.

> Port 8002 is chosen to avoid the frontend agent (8001) and file server (8080).

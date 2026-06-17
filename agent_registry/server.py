"""Agent Registry: a tiny config-server for the multi-agent swarm.

All orchestrator agents read the endpoints list from here so new agents can
be added without code changes or restarts. Just edit endpoints.json and hit
POST /reload (or restart only this tiny registry container).

Endpoints:
  GET    /agents           -> list all registered agents
  GET    /agents/{name}    -> get a specific agent
  POST   /reload           -> reload endpoints.json from disk
  GET    /health           -> healthcheck for compose/k8s
"""

import json
import os
from pathlib import Path

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

APP_DIR = Path(__file__).parent
CONFIG_PATH = Path(os.getenv("REGISTRY_CONFIG_PATH", APP_DIR / "endpoints.json"))

app = FastAPI(title="Agent Registry", version="1.0.0")

# In-memory cache of the endpoints config.
_config: dict = {"agents": []}


class AgentSpec(BaseModel):
    name: str
    url: str
    description: str
    type: str = "specialist"


class RegistryConfig(BaseModel):
    agents: list[AgentSpec]


def _load_config() -> dict:
    if not CONFIG_PATH.exists():
        raise RuntimeError(f"Registry config not found: {CONFIG_PATH}")
    with open(CONFIG_PATH, "r", encoding="utf-8") as f:
        data = json.load(f)
    # Basic validation.
    if "agents" not in data:
        raise ValueError("Config must contain an 'agents' list")
    for item in data["agents"]:
        for key in ("name", "url", "description"):
            if key not in item:
                raise ValueError(f"Agent spec missing '{key}': {item}")
    return data


def reload_config() -> dict:
    global _config
    _config = _load_config()
    print(f"[agent_registry] loaded {len(_config['agents'])} agents from {CONFIG_PATH}")
    for a in _config["agents"]:
        print(f"[agent_registry]   - {a['name']}: {a['url']} ({a.get('type', 'specialist')})")
    return _config


@app.on_event("startup")
def startup():
    reload_config()


@app.get("/agents")
def list_agents():
    return _config


@app.get("/agents/{name}")
def get_agent(name: str):
    for agent in _config["agents"]:
        if agent["name"] == name:
            return agent
    raise HTTPException(status_code=404, detail=f"Agent '{name}' not found")


@app.post("/reload")
def reload():
    return {"ok": True, "agents": reload_config()["agents"]}


@app.get("/health")
def health():
    return {"status": "ok", "agents_count": len(_config["agents"])}


if __name__ == "__main__":
    import uvicorn

    host = os.getenv("REGISTRY_HOST", "0.0.0.0")
    port = int(os.getenv("REGISTRY_PORT", "8006"))
    uvicorn.run(app, host=host, port=port, log_level="info")

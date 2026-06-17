"""Shared self-registration helper for demo agents.

Each demo agent calls register_self() at startup so it appears in the registry
and can be discovered by main_agent / eino_agent. This mirrors the same
"agents register themselves into the phonebook" pattern used by main_agent and
eino_agent. Idempotent: a 409 (already exists) is treated as success.
"""

import os

import requests


def register_self(name: str, service_name: str, port: int, description: str, agent_type: str = "specialist") -> None:
    registry_url = os.getenv("AGENT_REGISTRY_URL", "")
    if not registry_url:
        return
    own_url = f"http://{service_name}:{port}"
    payload = {
        "name": name,
        "url": own_url,
        "description": description,
        "type": agent_type,
    }
    try:
        resp = requests.post(f"{registry_url.rstrip('/')}/agents", json=payload, timeout=10)
        if resp.status_code == 201:
            print(f"[{name}] registered self @ {own_url}")
        elif resp.status_code == 409:
            pass  # already registered (persistent DB across restarts)
        else:
            print(f"[{name}] self-registration status {resp.status_code}: {resp.text[:120]}")
    except Exception as e:
        print(f"[{name}] self-registration failed (non-fatal): {e}")

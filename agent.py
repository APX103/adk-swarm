"""Root-level agent entry point for `adk web .` and `adk run .`.

ADK discovers the agent by importing agent.py from the project root.
This module re-exports root_agent from main_agent.agent and starts the
file server (unless running under adk web which has its own server).
"""

import os
import sys

# Ensure /app is on sys.path so `main_agent` package is importable.
_project_root = os.path.dirname(os.path.abspath(__file__))
if _project_root not in sys.path:
    sys.path.insert(0, _project_root)

# Fix: LiteLlm.llm_client (LiteLLMClient) is not JSON-serializable, which
# breaks the dev UI's build_graph endpoint (Pydantic v2 model_dump -> json).
# Monkey-patch both model_dump and model_dump_json to always exclude it.
try:
    from google.adk.models.lite_llm import LiteLlm as _LiteLlm
    _orig_dump = _LiteLlm.model_dump
    _orig_dump_json = _LiteLlm.model_dump_json

    def _patched_dump(self, **kwargs):
        kwargs.setdefault("exclude", {"llm_client"})
        return _orig_dump(self, **kwargs)

    def _patched_dump_json(self, **kwargs):
        kwargs.setdefault("exclude", {"llm_client"})
        return _orig_dump_json(self, **kwargs)

    _LiteLlm.model_dump = _patched_dump
    _LiteLlm.model_dump_json = _patched_dump_json
except Exception:
    pass

from main_agent.agent import root_agent  # noqa: E402

# Start the static file server on import unless running under `adk web`.
if not os.environ.get("ADK_WEB"):
    from main_agent.file_server import run_file_server  # noqa: E402
    from main_agent.agent import FILE_SERVER_PORT  # noqa: E402

    thread = __import__("threading").Thread(
        target=run_file_server,
        kwargs={"port": FILE_SERVER_PORT},
        daemon=True,
    )
    thread.start()
    print(f"File server started at http://localhost:{FILE_SERVER_PORT}")

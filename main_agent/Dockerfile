FROM python:3.11-slim

WORKDIR /app

# Install system dependencies.
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Install Python dependencies first (cache-friendly).
COPY main_agent/requirements.txt ./requirements.txt
RUN pip install --no-cache-dir -r requirements.txt

# Copy the root-level agent.py (adk web entry point that patches LiteLlm
# serialization and re-exports root_agent from main_agent.agent).
COPY agent.py ./agent.py

# Copy main_agent package files.
COPY main_agent/agent.py ./main_agent/agent.py
COPY main_agent/cli.py ./main_agent/cli.py
COPY main_agent/compression.py ./main_agent/compression.py
COPY main_agent/file_server.py ./main_agent/file_server.py
COPY main_agent/session.py ./main_agent/session.py
COPY main_agent/requirements.txt ./main_agent/requirements.txt
COPY main_agent/__init__.py ./main_agent/__init__.py
COPY main_agent/artifacts/ ./main_agent/artifacts/
COPY main_agent/test_orchestration.py ./main_agent/test_orchestration.py
COPY main_agent/test_subagent.py ./main_agent/test_subagent.py

# Copy other packages.
COPY mock_agent/agent.py ./mock_agent/agent.py
COPY mock_agent/server.py ./mock_agent/server.py
COPY mock_agent/__init__.py ./mock_agent/__init__.py
COPY demo_agents/comedian_server.py ./demo_agents/comedian_server.py
COPY demo_agents/critic_server.py ./demo_agents/critic_server.py
COPY demo_agents/__init__.py ./demo_agents/__init__.py
COPY tools/weather_mcp.py ./tools/weather_mcp.py

# Ensure data dirs exist for volume mounts.
RUN mkdir -p /app/artifacts /app/data

# Default: keep container running for interactive exec.
CMD ["sleep", "infinity"]

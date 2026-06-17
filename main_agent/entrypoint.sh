#!/bin/bash
set -e

# Start the A2A server in background (port 8081) so other agents can call us.
python3 -c "
import sys, os, time
sys.path.insert(0, '/app')
os.environ.setdefault('OPENAI_API_KEY', os.getenv('OPENAI_API_KEY', ''))
os.environ.setdefault('OPENAI_API_BASE', os.getenv('OPENAI_BASE_URL', ''))
from main_agent.agent import get_root_agent
from main_agent.a2a_server import start_a2a_server
port = int(os.getenv('MAIN_AGENT_A2A_PORT', '8081'))
start_a2a_server(get_root_agent(), port=port)
while True:
    time.sleep(86400)
" &

# Start ADK web server (port 8080).
exec adk web . --host 0.0.0.0 --port 8080

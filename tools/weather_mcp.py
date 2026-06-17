#!/usr/bin/env python3
"""Minimal MCP server for weather queries (stdio transport).

Speaks the MCP JSON-RPC protocol over stdin/stdout — no external MCP SDK
dependency. ADK's McpToolset spawns this via StdioServerParameters and it
exposes one tool: get_weather(city).

Run standalone for testing:
    python tools/weather_mcp.py          (then type JSON-RPC on stdin)
Or let ADK spawn it automatically via MCP_SERVERS env var.
"""

import json
import sys
import random


def get_weather(city: str) -> str:
    """Return a (mock) weather report for the given city."""
    # Deterministic-ish mock: seed by city name so repeats are stable.
    seed = sum(ord(c) for c in city) % 4
    conditions = ["晴", "多云", "小雨", "雷阵雨"]
    temps = [28, 24, 19, 31]
    return f"{city}：{conditions[seed]}，气温 {temps[seed]}°C，湿度 {50 + seed * 10}%，风力 {seed + 2} 级。"


# --- Minimal MCP stdio server ---
def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            continue

        method = msg.get("method", "")
        msg_id = msg.get("id")
        result = {}

        if method == "initialize":
            result = {
                "protocolVersion": "2024-11-05",
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "weather-mcp", "version": "1.0.0"},
            }
        elif method == "initialized" or method == "notifications/initialized":
            # notification — no response needed
            continue
        elif method == "tools/list":
            result = {
                "tools": [
                    {
                        "name": "get_weather",
                        "description": "查询指定城市的天气（返回温度、天气状况、湿度、风力）。输入城市名称。",
                        "inputSchema": {
                            "type": "object",
                            "properties": {
                                "city": {"type": "string", "description": "城市名称，如 北京、上海"},
                            },
                            "required": ["city"],
                        },
                    }
                ]
            }
        elif method == "tools/call":
            params = msg.get("params", {})
            tool_name = params.get("name")
            args = params.get("arguments", {})
            if tool_name == "get_weather":
                weather = get_weather(args.get("city", "未知"))
                result = {
                    "content": [{"type": "text", "text": weather}],
                    "isError": False,
                }
            else:
                result = {"content": [{"type": "text", "text": f"未知工具: {tool_name}"}], "isError": True}
        else:
            result = {}

        response = {"jsonrpc": "2.0", "id": msg_id, "result": result}
        sys.stdout.write(json.dumps(response, ensure_ascii=False) + "\n")
        sys.stdout.flush()


if __name__ == "__main__":
    main()

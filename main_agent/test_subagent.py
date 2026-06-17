"""Non-interactive routing verification.

Sends two kinds of requests and prints which author responds, to confirm the
orchestrator routes frontend vs backend requests correctly:
  * a frontend request should be answered via generate_frontend_project
  * a backend request should be transferred to the backend_agent sub-agent

Run:  python main_agent/test_subagent.py
"""

import asyncio

from dotenv import load_dotenv
from google.adk.runners import Runner
from google.genai import types

from agent import root_agent
from session import APP_NAME, DEFAULT_USER, create_session, get_session_service

load_dotenv()

CASES = [
    ("帮我做一个 TODO 页面", "frontend"),
    ("帮我写一个 FastAPI 的用户注册登录接口", "backend"),
]


async def run_case(runner: Runner, session_id: str, text: str):
    print(f"\n{'='*60}\n[请求] {text}\n{'-'*60}")
    authors = []
    final_text = []
    async for event in runner.run_async(
        user_id=DEFAULT_USER,
        session_id=session_id,
        new_message=types.Content(role="user", parts=[types.Part(text=text)]),
    ):
        author = getattr(event, "author", "?")
        if author not in authors:
            authors.append(author)
        if event.content and event.content.parts:
            for part in event.content.parts:
                if getattr(part, "text", None) and not getattr(part, "thought", False):
                    final_text.append(part.text)
    print(f"[参与 authors] {authors}")
    if final_text:
        print(f"[最终回答] {final_text[-1][:400]}")
    return authors


async def main():
    service = get_session_service()
    session = await create_session(service)
    runner = Runner(app_name=APP_NAME, agent=root_agent, session_service=service)
    print(f"session: {session.id}")

    results = {}
    for text, expect in CASES:
        authors = await run_case(runner, session.id, text)
        routed_to_backend = "backend_agent" in authors
        results[expect] = routed_to_backend if expect == "backend" else ("main_agent" in authors)

    print(f"\n{'='*60}\n[路由结果]")
    for expect, ok in results.items():
        print(f"  {expect}: {'✓ 已正确路由' if ok else '✗ 未如预期'}")


if __name__ == "__main__":
    asyncio.run(main())

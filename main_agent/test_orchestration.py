"""Multi-step orchestration stress test.

Asks the main agent to do a chained task that requires it to call TWO remote
A2A sub-agents in sequence within a single turn:

    用户 -> Main -> comedian_agent (讲笑话) -> Main -> critic_agent (评价) -> Main -> 用户

This is the real test of whether the A2A-sub-agent architecture can do
genuine multi-step scheduling. It prints every author that participates and the
final answer, so success/failure is plain to see.

Run:  python main_agent/test_orchestration.py
"""

import asyncio
import sys

from dotenv import load_dotenv
from google.adk.runners import Runner
from google.genai import types

from agent import root_agent
from session import APP_NAME, DEFAULT_USER, create_session, get_session_service

load_dotenv()

# The chained request: comedian tells a joke, then critic rates it.
REQUEST = "让喜剧演员讲一个关于程序员的笑话，然后让评论员给这个笑话打分评价。"


async def main():
    service = get_session_service()
    session = await create_session(service)
    runner = Runner(app_name=APP_NAME, agent=root_agent, session_service=service)

    print(f"session: {session.id}")
    print(f"{'='*70}\n[用户] {REQUEST}\n{'='*70}")

    authors_seen = []
    transfers = []
    final_answer_parts = []

    tool_calls = []  # (tool_name, args)
    tool_responses = []  # (tool_name, result_excerpt)

    async for event in runner.run_async(
        user_id=DEFAULT_USER,
        session_id=session.id,
        new_message=types.Content(role="user", parts=[types.Part(text=REQUEST)]),
    ):
        author = getattr(event, "author", "?")
        if author not in authors_seen:
            authors_seen.append(author)

        actions = getattr(event, "actions", None)
        tgt = getattr(actions, "transfer_to_agent", None) if actions else None
        if tgt:
            transfers.append((author, tgt))
            print(f"  ➡️ {author} 委派给 {tgt}")

        if event.content and event.content.parts:
            for part in event.content.parts:
                # Track ALL tool calls/responses — sub-agents now appear as
                # AgentTools named after themselves (comedian_agent, critic_agent,
                # backend_agent), so we watch for those names.
                fc = getattr(part, "function_call", None)
                fr = getattr(part, "function_response", None)
                if fc:
                    tool_calls.append(fc.name)
                    args_str = str(fc.args)[:80]
                    print(f"  🔧 {fc.name}({args_str})")
                if fr:
                    tool_responses.append(fr.name)
                    resp = str(fr.response)[:100]
                    print(f"  ↩️  {fr.name} -> {resp}")
                txt = getattr(part, "text", None)
                if not txt:
                    continue
                if getattr(part, "thought", False):
                    print(f"  💭 [{author}] {txt.strip()[:120]}")
                else:
                    final_answer_parts.append((author, txt))

    print(f"\n{'='*70}")
    print(f"[参与的 authors] {authors_seen}")
    print(f"[委派链路(transfer)] {transfers}")
    print(f"[工具实际调用] {tool_calls}")
    # Success = both sub-agents were actually invoked (as AgentTools, named after
    # themselves). This proves the orchestrator delegated to real A2A sub-agents.
    comedian_engaged = "comedian_agent" in tool_calls
    critic_engaged = "critic_agent" in tool_calls
    print(f"\ncomedian_agent 是否被调用: {'✓' if comedian_engaged else '✗'}")
    print(f"critic_agent  是否被调用: {'✓' if critic_engaged else '✗'}")

    if final_answer_parts:
        last_author, last_text = final_answer_parts[-1]
        print(f"\n[最终回答 by {last_author}]\n{last_text}")

    print(f"\n{'='*70}")
    ok = comedian_engaged and critic_engaged
    if ok:
        print("✅ 多步编排成功：Main 在一轮里串行调用了两个 A2A 子 Agent。")
    elif comedian_engaged or critic_engaged:
        print("⚠️ 部分成功：只调到了部分子 Agent，编排未完整。")
    else:
        print("❌ 失败：Main 没有委派给任何子 Agent，架构或指令需排查。")

    # Real assertions — fail loud so CI catches regressions.
    assert comedian_engaged, "comedian_agent 没有被调用，orchestrator 路由失败"
    assert critic_engaged, "critic_agent 没有被调用，多步编排未完成"
    return ok


if __name__ == "__main__":
    ok = asyncio.run(main())
    sys.exit(0 if ok else 1)

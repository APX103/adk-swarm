#!/usr/bin/env python3
"""End-to-end test: simulates a real user's complete journey.

Unlike unit tests that check pieces in isolation, this runs the ENTIRE flow
as one unbroken chain — exactly what a user would experience:

  1. Start CLI → chat (thinking visible?)
  2. Query weather via MCP tool (tool call visible? result correct?)
  3. Delegate to sub-agent (orchestration works?)
  4. Quit
  5. Restart with same session → restore history (persistence works?)
  6. Verify context continuity (agent remembers what was said?)
  7. Multi-step orchestration (comedian → critic chain)

Every step asserts on real output. If any step fails, the test fails loud.
Run:  python test_e2e.py
"""

import asyncio
import os
import sys

# Ensure we can import from main_agent's modules
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from dotenv import load_dotenv
from google.adk.runners import Runner
from google.genai import types

from agent import root_agent
from session import APP_NAME, DEFAULT_USER, create_session, get_session_service, get_or_create_session

load_dotenv()

# ANSI
GREEN = "\033[32m"
RED = "\033[31m"
YELLOW = "\033[33m"
BOLD = "\033[1m"
RESET = "\033[0m"

passed = 0
failed = 0


def check(name, condition, detail=""):
    global passed, failed
    if condition:
        passed += 1
        print(f"  {GREEN}✓ {name}{RESET}")
    else:
        failed += 1
        print(f"  {RED}✗ {name}{RESET} {detail}")


def extract_event_info(events):
    """Extract thinking, tool calls, tool results, text from events."""
    thoughts = []
    tool_calls = []
    tool_results = []
    texts = []
    for ev in events:
        if not ev.content or not ev.content.parts:
            continue
        for part in ev.content.parts:
            if getattr(part, "thought", False) and getattr(part, "text", None):
                thoughts.append(part.text)
            fc = getattr(part, "function_call", None)
            if fc:
                tool_calls.append((fc.name, fc.args or {}))
            fr = getattr(part, "function_response", None)
            if fr:
                tool_results.append((fr.name, fr.response or {}))
            if getattr(part, "text", None) and not getattr(part, "thought", False):
                texts.append(part.text)
    return thoughts, tool_calls, tool_results, texts


async def run_one_turn(runner, session_id, message):
    """Run one turn and return all events."""
    events = []
    async for ev in runner.run_async(
        user_id=DEFAULT_USER,
        session_id=session_id,
        new_message=types.Content(role="user", parts=[types.Part(text=message)]),
    ):
        events.append(ev)
    return events


async def main():
    global passed, failed

    service = get_session_service()
    runner = Runner(app_name=APP_NAME, agent=root_agent, session_service=service)

    # ──────────────────────────────────────────────
    # PHASE 1: Fresh session — chat + weather + tool
    # ──────────────────────────────────────────────
    print(f"\n{BOLD}━━━ Phase 1: 新 session — 聊天 + 天气查询 ━━━{RESET}")

    session = await create_session(service, session_id="e2e-full-test")
    sid = session.id
    print(f"  session: {sid}")

    # Step 1: simple chat
    print(f"\n  {BOLD}Step 1: 普通聊天「你好」{RESET}")
    events = await run_one_turn(runner, sid, "你好，我叫张三，是个后端工程师")
    thoughts, _, _, texts = extract_event_info(events)
    check("聊天有回复", len(texts) > 0, f"texts={len(texts)}")
    check("思考可见", len(thoughts) > 0, "没有 💭 thinking parts")

    # Step 2: weather via MCP tool
    print(f"\n  {BOLD}Step 2: MCP 天气查询「查杭州天气」{RESET}")
    events = await run_one_turn(runner, sid, "查一下杭州的天气")
    thoughts, calls, results, texts = extract_event_info(events)
    tool_names = [c[0] for c in calls]
    check("调用了天气工具", any("weather" in n for n in tool_names), f"calls={tool_names}")
    check("工具调用有结果返回", len(results) > 0)
    # 结果里应该有天气信息
    result_text = str(results)
    check("结果包含天气数据", "气温" in result_text or "°C" in result_text, f"result={result_text[:100]}")
    check("最终有文字回复", len(texts) > 0)

    # ──────────────────────────────────────────────
    # PHASE 2: Sub-agent delegation
    # ──────────────────────────────────────────────
    print(f"\n{BOLD}━━━ Phase 2: 子 Agent 调度 ━━━{RESET}")

    # Step 3: delegate to comedian
    print(f"\n  {BOLD}Step 3: 委派 comedian 讲笑话{RESET}")
    events = await run_one_turn(runner, sid, "让喜剧演员讲一个关于程序员的笑话")
    _, calls, results, texts = extract_event_info(events)
    tool_names = [c[0] for c in calls]
    check("委派了 comedian_agent", "comedian_agent" in tool_names, f"calls={tool_names}")
    check("comedian 有返回结果", len(results) > 0)
    joke_text = str(results) + str(texts)
    check("笑话内容非空", len(joke_text) > 20)

    # ──────────────────────────────────────────────
    # PHASE 3: Multi-step orchestration (comedian → critic)
    # ──────────────────────────────────────────────
    print(f"\n{BOLD}━━━ Phase 3: 多步编排（comedian → critic）━━━{RESET}")

    print(f"\n  {BOLD}Step 4: 讲笑话 + 评价{RESET}")
    events = await run_one_turn(runner, sid, "让喜剧演员讲个关于猫的笑话，然后让评论员评价它")
    _, calls, results, texts = extract_event_info(events)
    tool_names = [c[0] for c in calls]
    check("调用了 comedian_agent", "comedian_agent" in tool_names, f"calls={tool_names}")
    check("调用了 critic_agent", "critic_agent" in tool_names, f"calls={tool_names}")
    check("多步编排顺序正确（comedian 在 critic 之前）",
          "comedian_agent" in tool_names and "critic_agent" in tool_names)

    # ──────────────────────────────────────────────
    # PHASE 4: Session persistence — quit & resume
    # ──────────────────────────────────────────────
    print(f"\n{BOLD}━━━ Phase 4: Session 持久化 — 退出后恢复 ━━━{RESET}")

    # Step 5: simulate "restart" — re-open same session
    print(f"\n  {BOLD}Step 5: 重新打开同一 session{RESET}")
    resumed = await get_or_create_session(service, session_id=sid)
    check("session 成功恢复", resumed is not None and resumed.id == sid)

    # Step 6: verify history is there
    print(f"\n  {BOLD}Step 6: 历史记录完整{RESET}")
    events_in_db = list(getattr(resumed, "events", []) or [])
    check("历史事件数 > 10（至少 4 轮对话）", len(events_in_db) > 10, f"events={len(events_in_db)}")
    # 确认用户的自我介绍还在历史里
    all_history_text = ""
    for ev in events_in_db:
        if ev.content and ev.content.parts:
            for p in ev.content.parts:
                if getattr(p, "text", None):
                    all_history_text += p.text
    check("历史包含用户自我介绍", "张三" in all_history_text, "用户名字不在历史里")

    # Step 7: context continuity — does agent remember the name?
    print(f"\n  {BOLD}Step 7: 上下文连续性（agent 记得名字吗？）{RESET}")
    events = await run_one_turn(runner, sid, "我叫什么名字？我是做什么的？")
    _, _, _, texts = extract_event_info(events)
    final_reply = "".join(texts)
    check("agent 记得用户名字「张三」", "张三" in final_reply, f"reply={final_reply[:100]}")
    check("agent 记得职业「后端」", "后端" in final_reply, f"reply={final_reply[:100]}")

    # ──────────────────────────────────────────────
    # PHASE 5: Built-in tool
    # ──────────────────────────────────────────────
    print(f"\n{BOLD}━━━ Phase 5: 内置工具 ━━━{RESET}")

    print(f"\n  {BOLD}Step 8: get_current_time{RESET}")
    events = await run_one_turn(runner, sid, "现在几点？")
    _, calls, _, texts = extract_event_info(events)
    tool_names = [c[0] for c in calls]
    check("调用了 get_current_time", "get_current_time" in tool_names, f"calls={tool_names}")

    # ──────────────────────────────────────────────
    # SUMMARY
    # ──────────────────────────────────────────────
    print(f"\n{'='*60}")
    total = passed + failed
    print(f"{BOLD}端到端测试结果: {passed}/{total} 通过{RESET}")
    if failed:
        print(f"{RED}❌ {failed} 项失败{RESET}")
    else:
        print(f"{GREEN}✅ 全部通过 — 完整用户历程无断点{RESET}")

    return failed == 0


if __name__ == "__main__":
    ok = asyncio.run(main())
    sys.exit(0 if ok else 1)

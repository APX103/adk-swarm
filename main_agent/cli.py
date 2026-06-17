#!/usr/bin/env python3
"""Interactive CLI entry point for the main agent.

This is the primary way to use the swarm: a chat loop with persistent sessions,
visible thinking, tool-call rendering, A2A sub-agent routing, and automatic
context compression when the conversation gets long.

Commands (typed instead of a message):
  /sessions   list your saved sessions
  /resume ID  resume a previous session
  /new        start a fresh session
  /history    show recent turns in the current session
  /compact    force a context compression now
  /help       show commands
  /quit       exit

Usage:
  python main_agent/cli.py                # new or resumed session
  python main_agent/cli.py --session ID   # start/resume a specific session
"""

from __future__ import annotations

import argparse
import asyncio
import datetime
import sys

from dotenv import load_dotenv
from google.adk.runners import Runner
from google.genai import types

try:
    from agent import MODEL_NAME, get_root_agent
    from compression import DEFAULT_MAX_TOKENS, compress_events, history_tokens
except ImportError:
    from main_agent.agent import MODEL_NAME, get_root_agent
    from main_agent.compression import DEFAULT_MAX_TOKENS, compress_events, history_tokens


def _event_text(ev) -> str:
    """Extract concatenated text from an event (ADK Event or SimpleNamespace)."""
    content = getattr(ev, "content", None)
    if not content:
        return ""
    parts = getattr(content, "parts", None) or []
    return " ".join(getattr(p, "text", "") or "" for p in parts)
try:
    from session import (
        APP_NAME,
        DEFAULT_USER,
        create_session,
        get_or_create_session,
        get_session_service,
        list_sessions,
    )
except ImportError:
    from main_agent.session import (
        APP_NAME,
        DEFAULT_USER,
        create_session,
        get_or_create_session,
        get_session_service,
        list_sessions,
    )

load_dotenv()

# --- ANSI colors (plain terminal, no extra deps) ---
DIM = "\033[2m"
BOLD = "\033[1m"
CYAN = "\033[36m"
YELLOW = "\033[33m"
GREEN = "\033[32m"
MAGENTA = "\033[35m"
RED = "\033[31m"
RESET = "\033[0m"


def _fmt(author: str, text: str, color: str = RESET) -> str:
    return f"{color}{BOLD}[{author}]{RESET} {color}{text}{RESET}"


def _render_event(event) -> list[str]:
    """Turn one ADK Event into printable lines.

    Handles: thinking parts, tool calls, tool responses, text answers, and
    sub-agent transfers/errors. Returns a list of already-formatted lines.
    """
    lines: list[str] = []
    author = getattr(event, "author", "?")

    # 1. Errors first — most actionable.
    err = getattr(event, "error_message", None)
    if err:
        lines.append(_fmt(author, f"⚠️ {err}", RED))

    content = getattr(event, "content", None)
    if not content or not getattr(content, "parts", None):
        return lines

    for part in content.parts:
        # a) Thinking / reasoning (Gemini-style thoughts).
        is_thought = bool(getattr(part, "thought", False))
        if is_thought and getattr(part, "text", None):
            lines.append(_fmt(author, "💭 " + part.text, DIM + MAGENTA))
            continue

        # b) Function call = a tool invocation in progress.
        fcall = getattr(part, "function_call", None)
        if fcall:
            name = getattr(fcall, "name", "tool")
            args = getattr(fcall, "args", {}) or {}
            import json as _json

            arg_str = _json.dumps(args, ensure_ascii=False)
            if len(arg_str) > 200:
                arg_str = arg_str[:200] + "…"
            lines.append(_fmt(author, f"🔧 调用工具 {name}({arg_str})", YELLOW))
            continue

        # c) Function response = tool result.
        fresp = getattr(part, "function_response", None)
        if fresp:
            name = getattr(fresp, "name", "tool")
            resp = getattr(fresp, "response", {}) or {}
            import json as _json

            resp_str = _json.dumps(resp, ensure_ascii=False)
            if len(resp_str) > 300:
                resp_str = resp_str[:300] + "…"
            lines.append(_fmt(author, f"↩️ {name} 返回: {resp_str}", DIM + GREEN))
            continue

        # d) Plain text answer.
        if getattr(part, "text", None):
            who = "🤖 " + author if author != "user" else "🧑 user"
            color = CYAN if author != "user" else RESET
            lines.append(_fmt(who, part.text, color))

    return lines


async def run_turn(runner: Runner, session_id: str, user_text: str):
    """Send one user message and stream events as they arrive."""
    new_message = types.Content(
        role="user",
        parts=[types.Part(text=user_text)],
    )
    async for event in runner.run_async(
        user_id=DEFAULT_USER,
        session_id=session_id,
        new_message=new_message,
    ):
        for line in _render_event(event):
            if line.strip():
                print(line)


async def cmd_list_sessions():
    service = get_session_service()
    sessions = await list_sessions(service)
    if not sessions:
        print(f"{DIM}(还没有历史 session){RESET}")
        return
    print(f"{BOLD}历史 sessions（用户 {DEFAULT_USER}）：{RESET}")
    for s in sessions:
        sid = getattr(s, "id", "?")
        upd_raw = getattr(s, "last_update_time", "") or ""
        # last_update_time may be an epoch float or ISO string; normalize to readable
        try:
            upd = datetime.datetime.fromtimestamp(float(upd_raw)).strftime("%Y-%m-%d %H:%M")
        except (ValueError, TypeError):
            upd = upd_raw if upd_raw else "未知"
        print(f"  {CYAN}{sid}{RESET}  {DIM}更新于 {upd}{RESET}")


async def cmd_show_history(service, session_id: str):
    sess = await service.get_session(app_name=APP_NAME, user_id=DEFAULT_USER, session_id=session_id)
    if not sess:
        print(f"{DIM}(session 不存在){RESET}")
        return
    events = list(getattr(sess, "events", []) or [])
    if not events:
        print(f"{DIM}(当前 session 还没有对话){RESET}")
        return
    print(f"{BOLD}最近 {len(events)} 条事件：{RESET}")
    for ev in events[-12:]:
        for line in _render_event(ev):
            if line.strip():
                print(line)


async def cmd_compact(service, session_id: str):
    """Compress the session's older history into a summary.

    ADK's DatabaseSessionService doesn't expose a public API to rewrite event
    history, so compression works at the level we control: we summarize the
    older turns and surface the compressed view. The full history is preserved
    in the DB; the summary is shown so the user knows what was folded away.
    """
    sess = await service.get_session(app_name=APP_NAME, user_id=DEFAULT_USER, session_id=session_id)
    events = list(getattr(sess, "events", []) or [])
    if not events:
        print(f"{DIM}(当前 session 没有对话，无需压缩){RESET}")
        return
    compressed, did = compress_events(events, MODEL_NAME)
    if did:
        tok_before = history_tokens(events)
        tok_after = history_tokens(compressed)
        summary_text = _event_text(compressed[0]) if compressed else ""
        print(f"{GREEN}✓ 上下文已压缩（约 {tok_before} → {tok_after} tokens）{RESET}")
        print(f"{DIM}{summary_text[:500]}{'…' if len(summary_text) > 500 else ''}{RESET}")
    else:
        tok = history_tokens(events)
        print(f"{DIM}当前约 {tok} tokens，未达阈值（{DEFAULT_MAX_TOKENS}），无需压缩。{RESET}")


def print_help():
    print(f"""{BOLD}命令：{RESET}
  {CYAN}/sessions{RESET}   列出历史 session
  {CYAN}/resume ID{RESET}  恢复某个 session
  {CYAN}/new{RESET}        开启新 session
  {CYAN}/history{RESET}    查看当前 session 最近对话
  {CYAN}/compact{RESET}    立即压缩上下文
  {CYAN}/help{RESET}       显示本帮助
  {CYAN}/quit{RESET}       退出
直接输入文字即可与 agent 对话。""")


async def main(start_session_id: str | None = None):
    service = get_session_service()
    session = await get_or_create_session(service, session_id=start_session_id)
    session_id = session.id

    runner = Runner(
        app_name=APP_NAME,
        agent=get_root_agent(),
        session_service=service,
    )

    print(f"{BOLD}ADK Swarm 交互式入口{RESET}")
    print(f"当前 session: {CYAN}{session_id}{RESET}  模型: {MODEL_NAME}")
    print(f"输入 {CYAN}/help{RESET} 查看命令，{CYAN}/quit{RESET} 退出。\n")

    while True:
        try:
            user_text = input(f"{BOLD}> {RESET}").strip()
        except (EOFError, KeyboardInterrupt):
            print(f"\n{DIM}bye{RESET}")
            break
        if not user_text:
            continue

        cmd, _, arg = user_text.partition(" ")
        if cmd == "/quit":
            print(f"{DIM}bye{RESET}")
            break
        if cmd == "/help":
            print_help()
            continue
        if cmd == "/sessions":
            await cmd_list_sessions()
            continue
        if cmd == "/resume":
            sid = arg.strip()
            if not sid:
                print(f"{RED}用法: /resume <session-id>{RESET}")
                continue
            # 先查在不在，给用户准确的反馈
            existing = None
            try:
                existing = await service.get_session(app_name=APP_NAME, user_id=DEFAULT_USER, session_id=sid)
            except Exception:
                pass
            session = await get_or_create_session(service, session_id=sid)
            session_id = session.id
            if existing:
                print(f"{GREEN}已恢复 session {session_id}{RESET}")
            else:
                print(f"{YELLOW}session {sid} 不存在，已新建{RESET}")
            continue
        if cmd == "/new":
            session = await create_session(service)
            session_id = session.id
            print(f"{GREEN}新 session: {session_id}{RESET}")
            continue
        if cmd == "/history":
            await cmd_show_history(service, session_id)
            continue
        if cmd == "/compact":
            await cmd_compact(service, session_id)
            continue

        try:
            await run_turn(runner, session_id, user_text)
        except Exception as e:
            err = str(e)
            # 友好化常见错误（子 Agent 掉线、A2A 解析失败）
            if "AgentCardResolutionError" in err or "Failed to resolve" in err:
                print(f"{RED}⚠ 子 Agent 连不上（可能服务没启动）。错误详情: {err[:200]}{RESET}")
            else:
                print(f"{RED}⚠ 本轮出错: {err[:200]}{RESET}")

        # Gentle auto-compression hint after each turn (non-blocking, conservative).
        try:
            sess = await service.get_session(app_name=APP_NAME, user_id=DEFAULT_USER, session_id=session_id)
            events = list(getattr(sess, "events", []) or [])
            tok = history_tokens(events)
            if tok > DEFAULT_MAX_TOKENS:
                print(f"{YELLOW}💡 当前约 {tok} tokens 已超过 {DEFAULT_MAX_TOKENS}，可输入 /compact 压缩上下文。{RESET}")
        except Exception:
            pass


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description="Interactive main agent CLI")
    ap.add_argument("--session", "-s", default=None, help="resume a specific session id")
    args = ap.parse_args()
    try:
        asyncio.run(main(start_session_id=args.session))
    except KeyboardInterrupt:
        print(f"\n{DIM}interrupted{RESET}")
        sys.exit(0)

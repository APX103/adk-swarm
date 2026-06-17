"""Simple context compression to keep long conversations under the token budget.

ADK's own agents can carry full history and blow past a model's context window.
This is a deliberately small, dependency-free safety net used by the CLI loop:
when the recent events in a session exceed a threshold, older events are folded
into a single summary text segment via the same LLM the agent uses. This is the
"上下文超长自动压缩（简单版）" requirement.

It is intentionally conservative: it only summarizes, never deletes, and it
leaves the most recent N turns intact so the active turn always has full detail.
"""

from __future__ import annotations

import os
from types import SimpleNamespace

import litellm

# Rough chars-per-token factor for mixed CJK/ASCII text. Conservative on purpose.
_CHARS_PER_TOKEN = 2.5

# Summarize once the estimated tokens of accumulated history pass this.
DEFAULT_MAX_TOKENS = int(os.getenv("CONTEXT_MAX_TOKENS", "24000"))

# Always keep the most recent turns untouched.
DEFAULT_KEEP_RECENT = 6


def estimate_tokens(text: str) -> int:
    if not text:
        return 0
    return max(1, int(len(text) / _CHARS_PER_TOKEN))


def _event_text(ev) -> str:
    """Extract concatenated text from a single ADK event (or SimpleNamespace stub)."""
    content = getattr(ev, "content", None)
    if not content:
        return ""
    parts = getattr(content, "parts", None) or []
    return " ".join(getattr(p, "text", "") or "" for p in parts)


def history_tokens(events) -> int:
    """Estimate tokens across all text in a list of ADK events."""
    total = 0
    for ev in events:
        total += estimate_tokens(_event_text(ev))
    return total


def _summarize(prompt: str, model: str) -> str:
    """Call the configured OpenAI-compatible model to summarize."""
    base_url = os.getenv("OPENAI_API_BASE") or os.getenv("OPENAI_BASE_URL") or ""
    api_key = os.getenv("OPENAI_API_KEY", "")
    try:
        resp = litellm.completion(
            model=f"openai/{model}" if not model.startswith(("openai/", "litellm_proxy/")) else model,
            messages=[
                {"role": "system", "content": "你是对话压缩助手。把下面这段多轮对话压缩成一段简洁的中文纪要，保留：用户目标、已做决定、关键产物/链接、未解决问题。不要新增信息。"},
                {"role": "user", "content": prompt},
            ],
            api_base=base_url,
            api_key=api_key,
            temperature=0.1,
        )
        return (resp["choices"][0]["message"]["content"] or "").strip()
    except Exception as e:  # compression must never break the main loop
        return f"[上下文压缩失败: {e}] 已保留原始历史，仅截断展示。"


def split_history(events, keep_recent: int = DEFAULT_KEEP_RECENT):
    """Split events into (older, recent). The last `keep_recent` are kept intact."""
    if len(events) <= keep_recent:
        return [], events
    return events[:-keep_recent], events[-keep_recent:]


def summarize_older(older_events, model: str) -> str:
    """Summarize the older portion of a conversation into a single text block."""
    if not older_events:
        return ""
    text = "\n".join(
        f"[{getattr(ev, 'author', '?')}] {_event_text(ev)}" for ev in older_events
    )
    return _summarize(text, model)


def compress_events(events, model: str):
    """If history is too long, compress older events into a summary.

    Returns (compressed_events, did_compress). When compression triggers, the
    older events are replaced by a single summary event and the most recent
    DEFAULT_KEEP_RECENT events are kept intact. If there aren't enough events
    to split (everything is "recent"), no compression happens — otherwise we'd
    duplicate content by adding a summary on top of events we're already keeping.
    """
    if history_tokens(events) < DEFAULT_MAX_TOKENS:
        return events, False

    older, recent = split_history(events)
    if not older:
        # Not enough history to summarize without duplicating recent turns.
        return events, False

    summary_text = summarize_older(older, model)
    summary_event = SimpleNamespace(
        author="system",
        content=SimpleNamespace(parts=[SimpleNamespace(text=f"[上下文纪要] {summary_text}")]),
    )
    return [summary_event] + list(recent), True

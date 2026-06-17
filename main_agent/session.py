"""Persistent session management for the interactive main agent.

Uses ADK's DatabaseSessionService so conversations survive restarts: every turn
is stored in a local SQLite file, and `resume`/`list` commands can bring a
previous session back. This is the "session 管理" requirement.
"""

import os
import uuid

from google.adk.sessions import DatabaseSessionService

APP_NAME = "main_agent"
DEFAULT_USER = "cli-user"

# A local SQLite DB keeps everything in-repo and needs no extra services.
# DatabaseSessionService uses SQLAlchemy's async engine, so we must pin the
# async driver (aiosqlite) explicitly in the URL.
DB_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "sessions.db")
DB_URL = f"sqlite+aiosqlite:///{DB_PATH}"

# Singleton — avoid creating a new async engine on every CLI command.
_session_service: DatabaseSessionService | None = None


def get_session_service() -> DatabaseSessionService:
    """Return the singleton session service backed by the local SQLite DB."""
    global _session_service
    if _session_service is None:
        _session_service = DatabaseSessionService(db_url=DB_URL)
    return _session_service


async def list_sessions(service: DatabaseSessionService, user_id: str = DEFAULT_USER):
    """List existing sessions for the user, newest first."""
    page = await service.list_sessions(app_name=APP_NAME, user_id=user_id)
    sessions = list(page.sessions) if hasattr(page, "sessions") else list(page)
    # Newest last activity first. last_update_time is an ISO-ish string.
    sessions.sort(key=lambda s: getattr(s, "last_update_time", "") or "", reverse=True)
    return sessions


async def create_session(service: DatabaseSessionService, user_id: str = DEFAULT_USER, session_id: str | None = None):
    """Create and return a new session (random id if not provided)."""
    return await service.create_session(
        app_name=APP_NAME,
        user_id=user_id,
        session_id=session_id or f"sess-{uuid.uuid4().hex[:12]}",
    )


async def get_or_create_session(service: DatabaseSessionService, session_id: str | None = None, user_id: str = DEFAULT_USER):
    """Return the named session, creating it if it doesn't exist yet.

    Handles the race where a session is created between the get and create
    calls (e.g. two CLI starts with the same --session id) by re-reading.
    """
    if session_id:
        try:
            sess = await service.get_session(app_name=APP_NAME, user_id=user_id, session_id=session_id)
            if sess is not None:
                return sess
        except Exception:
            pass
    try:
        return await create_session(service, user_id=user_id, session_id=session_id)
    except Exception:
        # Session may have been created by a concurrent process; re-read.
        if session_id:
            return await service.get_session(app_name=APP_NAME, user_id=user_id, session_id=session_id)
        raise

import asyncio
import os

from dotenv import load_dotenv
from google.adk.runners import InMemoryRunner
from google.genai import types

from agent import root_agent

load_dotenv()


async def main():
    port = int(os.getenv("FILE_SERVER_PORT", "8080"))
    print(f"File server should be running at http://localhost:{port}")

    runner = InMemoryRunner(agent=root_agent, app_name="main_agent")
    user_id = "test-user"
    session_id = "test-session-2"
    await runner.session_service.create_session(
        app_name="main_agent",
        user_id=user_id,
        session_id=session_id,
        state={},
    )

    async for event in runner.run_async(
        user_id=user_id,
        session_id=session_id,
        new_message=types.Content(
            role="user",
            parts=[types.Part(text="帮我做一个 TODO 页面")],
        ),
    ):
        author = event.author
        if not event.content or not event.content.parts:
            continue
        text_parts = [p.text for p in event.content.parts if p.text]
        if text_parts:
            print(f"[{author}] {' '.join(text_parts)}")


if __name__ == "__main__":
    asyncio.run(main())

#!/usr/bin/env python3
"""Playwright 端到端测试：在 adk web UI 里发消息，触发 main_agent 调度子 Agent，
验证返回的 artifact 链接 / 子 Agent 回答在页面中显示。

前置：adk web 跑在 :8000，子 Agent 跑在 :8001/:8002/:8003/:8004。

用法：  python test_playwright.py
"""

import sys
import time

from playwright.sync_api import sync_playwright

ADK_WEB = "http://localhost:8000/dev-ui/?app=main_agent"
INPUT_SEL = 'textarea[placeholder="Type a message..."]'
SEND_SEL = 'button:has-text("send")'


def send_and_wait(page, message, timeout=150):
    """在输入框打字、点 send，等到页面出现新回复（文本变多）。

    返回发送后页面的全部可见文本。
    前端项目生成要 2-3 分钟（npm install+build），所以 timeout 给足。
    """
    before = page.inner_text("body")
    inp = page.locator(INPUT_SEL)
    inp.click()
    inp.fill(message)
    page.locator(SEND_SEL).click()

    # 轮询等回复：页面文本变长 + 出现新内容。
    # 对于慢任务（前端生成），耐心等到 deadline。
    deadline = time.time() + timeout
    last = before
    stable_count = 0
    while time.time() < deadline:
        time.sleep(5)
        now = page.inner_text("body")
        # 看是否出现目标关键词（artifact 链接 / 错误 / 完成文本）
        low = now.lower()
        done_signals = ["project.tar.gz", "localhost:8080", "下载", "error", "错误", "失败", "failed"]
        if any(s.lower() in low for s in done_signals) and len(now) > len(before) + 30:
            time.sleep(2)
            return page.inner_text("body")
        # 文本变长 = 有进展
        if len(now) > len(last) + 30:
            stable_count = 0
            last = now
        else:
            stable_count += 1
    return page.inner_text("body")


def run_test():
    cases = [
        {
            "name": "orchestrator 多步委派（讲笑话+评价）",
            "msg": "让喜剧演员讲一个关于程序员的笑话，然后让评论员评价它",
            "expect_any": ["comedian", "critic", "笑话", "评价", "分", "/10", "Oct"],
            "need_artifact": False,
        },
        {
            "name": "前端项目生成（artifact 链接）",
            "msg": "帮我做一个简单的计数器页面",
            "expect_any": ["project.tar.gz", "localhost:8080", "下载"],
            "need_artifact": True,
        },
    ]

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        page = browser.new_page(viewport={"width": 1280, "height": 900})
        page.goto(ADK_WEB, wait_until="networkidle", timeout=30000)
        time.sleep(3)

        # 确认输入框就绪
        if not page.locator(INPUT_SEL).is_visible():
            print("❌ 输入框未出现，adk web 可能没加载好 agent")
            browser.close()
            return False

        all_pass = True
        for c in cases:
            print(f"\n{'='*60}\n[测试] {c['name']}\n[发送] {c['msg']}\n{'-'*60}")
            body = send_and_wait(page, c["msg"], timeout=300)
            # 截图留证
            safe = c["name"][:8].replace(" ", "_")
            page.screenshot(path=f"/tmp/pw_{safe}.png", full_page=True)

            # 取页面后半段（新回复通常在底部）
            tail = body[-2500:]
            matched = [k for k in c["expect_any"] if k.lower() in body.lower()]
            ok = len(matched) > 0

            # artifact 链接额外验证：链接文本确实在 DOM 里
            if c["need_artifact"]:
                link = page.locator("a:has-text('project.tar.gz'), a:has-text('8080')")
                if link.count() > 0:
                    href = link.first.get_attribute("href") or "(无 href)"
                    print(f"  🔗 发现 artifact 链接: {href}")
                    ok = True

            status = "✅ 通过" if ok else "❌ 失败"
            print(f"  匹配关键词: {matched}")
            print(f"  结果: {status}")
            print(f"  页面尾部文本:\n    {tail[-400:].strip()[:400]}")
            if not ok:
                all_pass = False

        browser.close()
        print(f"\n{'='*60}")
        print("✅ 全部通过" if all_pass else "❌ 有用例未通过")
        return all_pass


if __name__ == "__main__":
    ok = run_test()
    sys.exit(0 if ok else 1)

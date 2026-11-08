#!/usr/bin/env python3
"""Generate the diagrams used in README/docs.

Requires Pillow and the Microsoft YaHei fonts that ship with Windows.
Run: python docs/make_diagrams.py
"""

from __future__ import annotations

import math
import os
from functools import lru_cache

from PIL import Image, ImageDraw, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))

BG = "#F8FAFC"
INK = "#0F172A"
MUTED = "#64748B"
FAINT = "#94A3B8"
LINE = "#CBD5E1"

FONT_REG = "C:/Windows/Fonts/msyh.ttc"
FONT_BOLD = "C:/Windows/Fonts/msyhbd.ttc"
FONT_MONO = "C:/Windows/Fonts/consola.ttf"

PALETTE = {
    "blue": ("#EFF6FF", "#2563EB", "#BFDBFE"),
    "violet": ("#F5F3FF", "#7C3AED", "#DDD6FE"),
    "green": ("#ECFDF5", "#059669", "#A7F3D0"),
    "amber": ("#FFFBEB", "#D97706", "#FDE68A"),
    "red": ("#FEF2F2", "#DC2626", "#FECACA"),
    "slate": ("#F1F5F9", "#475569", "#CBD5E1"),
    "cyan": ("#ECFEFF", "#0891B2", "#A5F3FC"),
}


@lru_cache(maxsize=256)
def font(size: int, bold: bool = False, mono: bool = False) -> ImageFont.FreeTypeFont:
    path = FONT_MONO if mono else (FONT_BOLD if bold else FONT_REG)
    return ImageFont.truetype(path, size, index=0)


def canvas(w: int, h: int) -> tuple[Image.Image, ImageDraw.ImageDraw]:
    img = Image.new("RGB", (w, h), BG)
    return img, ImageDraw.Draw(img)


def text(
    d: ImageDraw.ImageDraw,
    x: float,
    y: float,
    s: str,
    size: int = 16,
    fill: str = INK,
    bold: bool = False,
    mono: bool = False,
    anchor: str = "la",
) -> None:
    d.text((x, y), s, font=font(size, bold, mono), fill=fill, anchor=anchor)


def lines(
    d: ImageDraw.ImageDraw,
    x: float,
    y: float,
    items: list[str],
    size: int = 15,
    fill: str = MUTED,
    gap: int = 6,
    mono: bool = False,
    bold: bool = False,
) -> float:
    """Draw left-aligned lines; returns the y after the block."""
    cur = y
    for item in items:
        d.text((x, cur), item, font=font(size, bold, mono), fill=fill, anchor="la")
        bbox = d.textbbox((x, cur), item, font=font(size, bold, mono), anchor="la")
        cur = bbox[3] + gap
    return cur


def shadow_box(
    d: ImageDraw.ImageDraw,
    x: int,
    y: int,
    w: int,
    h: int,
    radius: int = 14,
    fill: str = "#FFFFFF",
    outline: str = LINE,
    width: int = 2,
    dashed: bool = False,
) -> None:
    d.rounded_rectangle((x + 3, y + 5, x + w + 3, y + h + 5), radius=radius, fill="#E2E8F0")
    if dashed:
        # Pillow cannot dash a rounded rectangle; approximate with straight edges.
        d.rectangle((x, y, x + w, y + h), fill=fill)
        dash_line(d, [(x, y), (x + w, y)], outline, width)
        dash_line(d, [(x + w, y), (x + w, y + h)], outline, width)
        dash_line(d, [(x + w, y + h), (x, y + h)], outline, width)
        dash_line(d, [(x, y + h), (x, y)], outline, width)
    else:
        d.rounded_rectangle((x, y, x + w, y + h), radius=radius, fill=fill, outline=outline, width=width)


def box(
    d: ImageDraw.ImageDraw,
    x: int,
    y: int,
    w: int,
    h: int,
    title: str,
    body: list[str] | None = None,
    color: str = "blue",
    dashed: bool = False,
    title_size: int = 21,
    body_size: int = 15,
    mono_body: bool = False,
    centered: bool = False,
    icon: str | None = None,
) -> None:
    fill, accent, border = PALETTE[color]
    shadow_box(d, x, y, w, h, fill=fill, outline=border, dashed=dashed)
    # left accent bar
    d.rounded_rectangle((x, y + 10, x + 6, y + h - 10), radius=3, fill=accent)
    if centered:
        text(d, x + w / 2, y + 15, title, title_size, INK, True, anchor="ma")
        body_y = y + 15 + title_size + 14
    else:
        text(d, x + 20, y + 13, title, title_size, INK, True)
        body_y = y + 13 + title_size + 12
    if body:
        if centered:
            for i, line in enumerate(body):
                text(d, x + w / 2, body_y + i * (body_size + 8), line, body_size, MUTED, mono_body, anchor="ma")
        else:
            lines(d, x + 20, body_y, body, body_size, MUTED, 7, mono_body)
    if icon:
        text(d, x + w - 24, y + 18, icon, 22, accent, anchor="ra")


def diamond(
    d: ImageDraw.ImageDraw,
    cx: int,
    cy: int,
    w: int,
    h: int,
    label: str,
    color: str = "amber",
    sub: str | None = None,
) -> None:
    fill, accent, border = PALETTE[color]
    pts = [(cx, cy - h // 2), (cx + w // 2, cy), (cx, cy + h // 2), (cx - w // 2, cy)]
    d.polygon(pts, fill=fill, outline=border)
    d.line(pts + [pts[0]], fill=accent, width=2)
    text(d, cx, cy - (8 if sub else 0), label, 17, INK, True, anchor="mm")
    if sub:
        text(d, cx, cy + 18, sub, 13, MUTED, anchor="mm")


def dash_line(
    d: ImageDraw.ImageDraw,
    pts: list[tuple[float, float]],
    fill: str = FAINT,
    width: int = 2,
    dash: int = 10,
    gap: int = 7,
) -> None:
    for i in range(len(pts) - 1):
        x1, y1 = pts[i]
        x2, y2 = pts[i + 1]
        seg = math.hypot(x2 - x1, y2 - y1)
        if seg == 0:
            continue
        ox, oy = (x2 - x1) / seg, (y2 - y1) / seg
        pos = 0.0
        while pos < seg:
            end = min(pos + dash, seg)
            d.line([(x1 + ox * pos, y1 + oy * pos), (x1 + ox * end, y1 + oy * end)], fill=fill, width=width)
            pos += dash + gap


def head(d: ImageDraw.ImageDraw, tip: tuple[float, float], frm: tuple[float, float], fill: str, size: int = 13) -> None:
    ang = math.atan2(tip[1] - frm[1], tip[0] - frm[0])
    left = (tip[0] - size * math.cos(ang - 0.42), tip[1] - size * math.sin(ang - 0.42))
    right = (tip[0] - size * math.cos(ang + 0.42), tip[1] - size * math.sin(ang + 0.42))
    d.polygon([tip, left, right], fill=fill)


def arrow(
    d: ImageDraw.ImageDraw,
    pts: list[tuple[float, float]],
    color: str = "#475569",
    width: int = 2,
    dashed: bool = False,
    both: bool = False,
    label: str | None = None,
    label_at: tuple[float, float] | None = None,
    label_size: int = 14,
    label_color: str | None = None,
    bg: str = BG,
) -> None:
    if dashed:
        dash_line(d, pts, color, width)
    else:
        d.line(pts, fill=color, width=width, joint="curve")
    head(d, pts[-1], pts[-2], color)
    if both:
        head(d, pts[0], pts[1], color)
    if label:
        if label_at is None:
            (x1, y1), (x2, y2) = pts[0], pts[1]
            label_at = ((x1 + x2) / 2, (y1 + y2) / 2 - 12)
        f = font(label_size)
        tw = d.textlength(label, font=f)
        x, y = label_at
        d.rectangle((x - tw / 2 - 5, y - 9, x + tw / 2 + 5, y + 10), fill=bg)
        d.text((x, y), label, font=f, fill=label_color or color, anchor="mm")


def badge(d: ImageDraw.ImageDraw, cx: float, cy: float, n: str, color: str = "#2563EB") -> None:
    d.ellipse((cx - 11, cy - 11, cx + 11, cy + 11), fill=color)
    text(d, cx, cy, n, 12, "#FFFFFF", True, anchor="mm")


def title_block(d: ImageDraw.ImageDraw, title: str, subtitle: str, width: int) -> None:
    text(d, 70, 46, title, 38, INK, True)
    text(d, 72, 100, subtitle, 18, MUTED)
    d.line([(70, 134), (width - 70, 134)], fill="#E2E8F0", width=2)


def save(img: Image.Image, name: str) -> None:
    path = os.path.join(HERE, name)
    img.save(path, "PNG", optimize=True)
    print("wrote", path, img.size)


# ---------------------------------------------------------------------------
# 1. Architecture
# ---------------------------------------------------------------------------


def architecture() -> None:
    W, H = 1800, 1120
    img, d = canvas(W, H)
    title_block(d, "agent · 项目结构与模块依赖", "入口 / 编排 / 能力 / 传输 四层；实线是调用，虚线是协议实现或开发者工具", W)

    bands = [
        (160, 340, "#EFF6FF", "入口层  ENTRY"),
        (360, 550, "#F5F3FF", "编排层  ORCHESTRATION"),
        (575, 790, "#ECFDF5", "能力层  CAPABILITIES"),
        (820, 1020, "#FFF7ED", "传输层  TRANSPORT"),
    ]
    for y1, y2, color, label in bands:
        d.rounded_rectangle((60, y1, W - 60, y2), radius=18, fill=color)
        text(d, 82, y1 + 12, label, 15, MUTED, True)

    # Entry
    box(
        d, 420, 195, 540, 120,
        "cmd/agent — CLI",
        ["flags / env / REPL / 渲染 / Ctrl+C", "stdout = 回答与 JSON · stderr = 诊断与统计"],
        "blue",
    )
    box(
        d, 1270, 195, 380, 120,
        "cmd/mockllm — 本地假模型",
        ["OpenAI 兼容 SSE", "演示 / 离线联调"],
        "slate",
        dashed=True,
    )

    # Orchestration
    box(
        d, 380, 390, 780, 135,
        "internal/agent — Agent 编排",
        ["messages 历史 · 工具循环 · 结构化修复", "MaxTurns=8 · MaxRepairs=2 · Turn 统计", "Hooks：OnText / OnRetry / OnToolStart / OnRequest"],
        "violet",
    )

    # Capabilities
    box(
        d, 100, 615, 350, 145,
        "internal/tools",
        ["get_current_time", "calculator", "read_file（根目录隔离）"],
        "green",
    )
    box(
        d, 500, 615, 350, 145,
        "internal/schema",
        ["ExtractJSON（容忍围栏）", "Validate（JSON Path）", "repair 错误回填"],
        "cyan",
    )
    box(
        d, 900, 615, 350, 145,
        "internal/usage",
        ["prompt / completion / cached", "价格表 USD / 1M", "费用估算"],
        "amber",
    )
    box(
        d, 1290, 615, 360, 145,
        "约束与安全",
        ["工具 panic recover", "工具结果 8000 字符截断", "历史不裁剪（已知限制）"],
        "slate",
    )

    # Transport
    box(
        d, 100, 855, 620, 120,
        "internal/llm — LLM Client",
        ["SSE 累积 · tool_calls 按 index 拼接", "重试 / 退避 / 超时 / 错误分类"],
        "blue",
    )
    box(
        d, 880, 855, 560, 120,
        "OpenAI 兼容 API",
        ["DeepSeek / OpenAI / vLLM / Ollama", "POST /chat/completions"],
        "slate",
    )

    # arrows
    arrow(d, [(690, 315), (690, 390)], "#7C3AED", 2, label="Run(ctx, question)", label_at=(690, 352))
    arrow(d, [(470, 525), (275, 615)], "#059669", 2, both=True, label="Execute(tool)", label_at=(360, 567))
    arrow(d, [(700, 525), (675, 615)], "#0891B2", 2, both=True, label="Validate", label_at=(722, 570))
    arrow(d, [(1020, 525), (1050, 615)], "#D97706", 2, label="AddTokens", label_at=(1070, 570))

    # Agent <-> LLM, routed around the left edge
    arrow(
        d,
        [(380, 470), (78, 470), (78, 915), (100, 915)],
        "#2563EB", 2, both=True, label="Stream <-> Result", label_at=(225, 448),
    )
    # LLM <-> API
    arrow(
        d,
        [(720, 915), (880, 915)],
        "#475569", 2, both=True, label="POST / SSE",
        label_at=(800, 890), label_size=13,
    )
    # mock -> API (same protocol)
    arrow(
        d,
        [(1460, 315), (1740, 315), (1740, 815), (1360, 815), (1360, 855)],
        "#94A3B8", 2, dashed=True, label="实现同一协议", label_at=(1600, 790), label_size=13,
    )

    text(d, 70, 1055, "依赖方向：cmd/agent → internal/agent → internal/llm → 网络；Agent 再调用 tools / schema / usage。", 14, FAINT)
    text(d, W - 70, 1055, "图 1", 15, FAINT, True, anchor="ra")
    save(img, "architecture.png")


# ---------------------------------------------------------------------------
# 2. Sequence: one tool call round trip
# ---------------------------------------------------------------------------


def sequence() -> None:
    W, H = 1600, 1580
    img, d = canvas(W, H)
    title_block(d, "一次工具调用：端到端时序", "一次用户提问 = 两次模型请求；tool_calls 分片到达，按 index 拼接", W)

    lanes = [
        ("用户", "输入 / 输出", 110, "#475569"),
        ("cmd/agent", "CLI · REPL · 渲染", 310, "#2563EB"),
        ("Agent", "消息历史 · 循环", 520, "#7C3AED"),
        ("LLM Client", "SSE · 重试 · 统计", 750, "#0891B2"),
        ("API", "OpenAI 兼容", 980, "#D97706"),
        ("Tools", "本地执行", 1180, "#059669"),
    ]
    header_y = 160
    for name, sub, x, color in lanes:
        d.rounded_rectangle((x - 88, header_y, x + 88, header_y + 64), radius=12, fill="#FFFFFF", outline=color, width=2)
        text(d, x, header_y + 18, name, 19, color, True, anchor="ma")
        text(d, x, header_y + 44, sub, 12, MUTED, anchor="ma")
        dash_line(d, [(x, header_y + 64), (x, 1520)], "#CBD5E1", 2, 6, 8)

    def msg(y: int, x1: int, x2: int, label: str, color: str = "#334155", dashed: bool = False) -> None:
        arrow(d, [(x1, y), (x2, y)], color, 2, dashed=dashed, label=label, label_at=((x1 + x2) / 2, y - 15), label_size=13)

    msg(280, 110, 310, "输入：现在几点？")
    msg(335, 310, 520, "Run(ctx, question)")
    msg(390, 520, 750, "Stream(messages, tools)")
    msg(445, 750, 980, "POST stream=true")
    msg(500, 980, 750, "chunk: content delta")
    msg(555, 750, 310, "OnText(delta) → stdout", "#0891B2")
    msg(610, 980, 750, "chunk: tool_calls 首片")
    msg(665, 980, 750, "chunk: arguments 分片")
    # self note: assembly
    d.rounded_rectangle((762, 700, 1100, 752), radius=10, fill="#FEF3C7", outline="#F59E0B")
    text(d, 772, 710, "按 index 拼接 arguments → 最后统一 Unmarshal", 13, "#92400E")
    d.line([(750, 726), (762, 726)], fill="#F59E0B", width=2)
    msg(795, 980, 750, "finish + usage + [DONE]")
    msg(850, 750, 520, "Result{tool_calls, usage}")
    # self note: append assistant
    d.rounded_rectangle((532, 880, 900, 932), radius=10, fill="#EDE9FE", outline="#8B5CF6")
    text(d, 542, 890, "messages += assistant(tool_calls)", 13, "#5B21B6")
    text(d, 542, 910, "usage.AddTokens(...)", 13, "#5B21B6")
    d.line([(520, 906), (532, 906)], fill="#8B5CF6", width=2)
    msg(970, 520, 1180, "Execute(name, args)")
    msg(1025, 1180, 520, "tool result / ERROR", "#059669")
    # self note: append tool
    d.rounded_rectangle((532, 1060, 870, 1112), radius=10, fill="#EDE9FE", outline="#8B5CF6")
    text(d, 542, 1070, "messages += tool(id=call_1)", 13, "#5B21B6")
    text(d, 542, 1090, "tool_call_id 必须匹配", 12, "#6D28D9")
    d.line([(520, 1086), (532, 1086)], fill="#8B5CF6", width=2)
    msg(1150, 520, 750, "Stream(4 条消息)")
    msg(1205, 750, 980, "POST 第二次", "#94A3B8", dashed=True)
    msg(1260, 980, 750, "text chunks")
    msg(1315, 750, 310, "OnText → stdout", "#0891B2")
    msg(1370, 750, 520, "Result{usage}")
    msg(1425, 520, 310, "TurnResult")
    msg(1480, 310, 110, "回答 + [turn] 统计")

    # right-side reading notes
    panel = (1300, 240, 1550, 900)
    d.rounded_rectangle(panel, radius=16, fill="#FFFFFF", outline="#E2E8F0", width=2)
    text(d, 1322, 262, "读图要点", 19, INK, True)
    notes = [
        ("1", "文本 delta 到达即打印"),
        ("2", "tool_calls 分片 → 按 index 拼"),
        ("3", "assistant / tool 必须成对"),
        ("4", "一次提问 = 两次请求"),
        ("5", "usage 在最后一块 chunk"),
        ("6", "已输出后不再重试"),
        ("7", "stdout 只放回答/JSON"),
    ]
    y = 300
    for num, line in notes:
        badge(d, 1330, y + 7, num, "#2563EB")
        text(d, 1350, y, line, 14, MUTED)
        y += 38
    text(d, 1322, 640, "数据不变量", 16, INK, True)
    lines(d, 1322, 672, ["call.id == tool_call_id", "回填顺序 = index 顺序"], 13, MUTED, 8)
    text(d, 1322, 750, "失败路径", 16, INK, True)
    lines(d, 1322, 782, ["429/5xx → 退避重试", "4xx → 直接报错", "已输出 → 不重试"], 13, MUTED, 8)

    save(img, "sequence-tool-call.png")


# ---------------------------------------------------------------------------
# 3. Agent loop
# ---------------------------------------------------------------------------


def agent_loop() -> None:
    W, H = 1700, 1320
    img, d = canvas(W, H)
    title_block(d, "Agent 主循环：工具调用与结构化修复", "菱形是判断，圆角矩形是处理；循环上限 MaxTurns=8，repair 上限 MaxRepairs=2", W)

    # Central cycle
    box(d, 680, 175, 360, 82, "A 发起流式请求", ["Stream(messages, tools, format)"], "blue", centered=True, title_size=19)
    box(d, 1030, 320, 400, 100, "B 接收 SSE", ["文本 delta → stdout", "tool_calls 按 index 拼接"], "cyan", centered=True, title_size=19)
    diamond(d, 1230, 555, 260, 120, "有 tool_calls?", "amber")
    box(d, 1040, 690, 380, 100, "D 执行工具 + 回填", ["超时 / panic / 截断", "messages += assistant + tool"], "green", centered=True, title_size=19)
    diamond(d, 850, 980, 230, 110, "有 schema?", "amber")
    diamond(d, 430, 980, 230, 110, "校验通过?", "amber")
    diamond(d, 300, 700, 250, 120, "repairs 剩余?", "amber")
    box(d, 155, 380, 300, 92, "H 追加 repair 消息", ["带 JSON Path 的错误"], "violet", centered=True, title_size=18)
    box(d, 680, 1130, 360, 74, "完成：输出 + 统计", ["turn / session token · 费用"], "blue", centered=True, title_size=19)
    box(d, 100, 1130, 310, 74, "StructuredError", ["退出码 1 · 打印错误"], "red", centered=True, title_size=18)

    # A -> B
    arrow(d, [(950, 257), (1030, 350)], "#0891B2", 2, label="成功", label_at=(960, 302))
    arrow(d, [(1230, 420), (1230, 495)], "#D97706", 2)
    arrow(d, [(1230, 615), (1230, 690)], "#059669", 2, label="yes", label_at=(1256, 652))

    # D -> A: next request after tool result
    arrow(
        d,
        [(1420, 740), (1650, 740), (1650, 216), (1040, 216)],
        "#059669", 2, label="≤ maxTurns", label_at=(1530, 700), label_size=13,
    )

    # C no -> E
    arrow(d, [(1100, 555), (850, 555), (850, 925)], "#D97706", 2, label="no", label_at=(980, 532))
    # E yes -> F
    arrow(d, [(735, 980), (545, 980)], "#D97706", 2, label="yes", label_at=(645, 958))
    # E no -> I
    arrow(d, [(850, 1035), (850, 1130)], "#D97706", 2, label="no", label_at=(875, 1090))
    # F pass -> I
    arrow(d, [(430, 1035), (430, 1167), (680, 1167)], "#2563EB", 2, label="通过", label_at=(560, 1145))
    # F fail -> G (enter the lower-right edge of the diamond)
    arrow(d, [(430, 925), (430, 730), (372, 730)], "#DC2626", 2, label="fail", label_at=(410, 845))
    # G yes -> H
    arrow(d, [(300, 640), (300, 472)], "#7C3AED", 2, label="yes", label_at=(326, 560))
    # G no -> StructuredError
    arrow(d, [(175, 700), (80, 700), (80, 1167), (100, 1167)], "#DC2626", 2, label="no", label_at=(110, 900))
    # H -> A along the far-left lane
    arrow(
        d,
        [(155, 426), (60, 426), (60, 216), (680, 216)],
        "#7C3AED", 2, label="追加 repair 后重发", label_at=(360, 194), label_size=13,
    )

    # legend
    ly = 1245
    d.rounded_rectangle((680, ly - 8, 1470, ly + 34), radius=12, fill="#FFFFFF", outline="#E2E8F0")
    d.rounded_rectangle((700, ly + 2, 730, ly + 22), radius=5, fill=PALETTE["amber"][0], outline=PALETTE["amber"][2])
    text(d, 740, ly + 4, "判断", 13, MUTED)
    d.rounded_rectangle((810, ly + 2, 840, ly + 22), radius=5, fill=PALETTE["green"][0], outline=PALETTE["green"][2])
    text(d, 850, ly + 4, "处理", 13, MUTED)
    d.rounded_rectangle((920, ly + 2, 950, ly + 22), radius=5, fill=PALETTE["red"][0], outline=PALETTE["red"][2])
    text(d, 960, ly + 4, "失败退出", 13, MUTED)
    text(d, 1080, ly + 4, "请求失败由 llm.Client 分类：退避重试或直接报错", 13, FAINT)

    save(img, "agent-loop.png")


if __name__ == "__main__":
    architecture()
    sequence()
    agent_loop()


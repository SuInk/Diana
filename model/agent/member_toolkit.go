// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"os"
	"path/filepath"
)

const memberPythonLibSubdir = "_pylib"

// installMemberPythonToolkit 在成员沙盒的 .tmp/_pylib 下写入零依赖离线辅助库 diana_sandbox.py。
// 宿主机即使没装 Pillow、matplotlib 或音频库，沙盒里的 python3 也能直接 import diana_sandbox
// 生成 PNG 位图、SVG 统计图、WAV 合成音频、多轨 MIDI 和统计报表；放在 .tmp 下不会混进产物列表。
func installMemberPythonToolkit(tmpDir string) error {
	libDir := filepath.Join(tmpDir, memberPythonLibSubdir)
	if err := os.MkdirAll(libDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(libDir, "diana_sandbox.py"), []byte(dianaSandboxPythonSource), 0o600)
}

const dianaSandboxPythonSource = `"""Zero-dependency offline toolkit preloaded inside the Diana command sandbox.

Provides:
- Canvas: RGB/RGBA pixel drawing with built-in 5x7 ASCII bitmap font and PNG/BMP export
- SVG charts: svg_bar_chart, svg_line_chart, svg_pie_chart, save_svg
- Audio synthesis: write_wav, synth_notes (sine/square/triangle/saw + ADSR)
- MIDI composition: MIDIBuilder (multi-track standard MIDI file writer)
- Tabular & statistics helpers: describe_numbers, read_csv, write_csv
"""

from __future__ import annotations

import csv
import html
import math
import os
import shutil
import statistics
import struct
import subprocess
import wave
import zlib
from pathlib import Path
from typing import Iterable, Sequence

# 5x7 uppercase/symbol ASCII bitmap font (each row is 5 bits, MSB on left).
_FONT_5X7: dict[str, tuple[int, ...]] = {
    " ": (0, 0, 0, 0, 0, 0, 0),
    "!": (4, 4, 4, 4, 4, 0, 4),
    '"': (10, 10, 0, 0, 0, 0, 0),
    "#": (10, 31, 10, 10, 31, 10, 0),
    "$": (4, 15, 20, 14, 5, 30, 4),
    "%": (24, 25, 2, 4, 8, 19, 3),
    "&": (12, 18, 20, 8, 21, 18, 13),
    "'": (4, 4, 0, 0, 0, 0, 0),
    "(": (2, 4, 8, 8, 8, 4, 2),
    ")": (8, 4, 2, 2, 2, 4, 8),
    "*": (0, 4, 21, 14, 21, 4, 0),
    "+": (0, 4, 4, 31, 4, 4, 0),
    ",": (0, 0, 0, 0, 4, 4, 8),
    "-": (0, 0, 0, 31, 0, 0, 0),
    ".": (0, 0, 0, 0, 0, 12, 12),
    "/": (1, 1, 2, 4, 8, 16, 16),
    "0": (14, 17, 19, 21, 25, 17, 14),
    "1": (4, 12, 4, 4, 4, 4, 14),
    "2": (14, 17, 1, 2, 4, 8, 31),
    "3": (31, 2, 4, 2, 1, 17, 14),
    "4": (2, 6, 10, 18, 31, 2, 2),
    "5": (31, 16, 30, 1, 1, 17, 14),
    "6": (6, 8, 16, 30, 17, 17, 14),
    "7": (31, 1, 2, 4, 8, 8, 8),
    "8": (14, 17, 17, 14, 17, 17, 14),
    "9": (14, 17, 17, 15, 1, 2, 12),
    ":": (0, 12, 12, 0, 12, 12, 0),
    ";": (0, 12, 12, 0, 12, 4, 8),
    "<": (2, 4, 8, 16, 8, 4, 2),
    "=": (0, 0, 31, 0, 31, 0, 0),
    ">": (8, 4, 2, 1, 2, 4, 8),
    "?": (14, 17, 1, 2, 4, 0, 4),
    "@": (14, 17, 23, 21, 23, 16, 14),
    "A": (14, 17, 17, 31, 17, 17, 17),
    "B": (30, 17, 17, 30, 17, 17, 30),
    "C": (14, 17, 16, 16, 16, 17, 14),
    "D": (28, 18, 17, 17, 17, 18, 28),
    "E": (31, 16, 16, 30, 16, 16, 31),
    "F": (31, 16, 16, 30, 16, 16, 16),
    "G": (14, 17, 16, 23, 17, 17, 15),
    "H": (17, 17, 17, 31, 17, 17, 17),
    "I": (14, 4, 4, 4, 4, 4, 14),
    "J": (7, 2, 2, 2, 2, 18, 12),
    "K": (17, 18, 20, 24, 20, 18, 17),
    "L": (16, 16, 16, 16, 16, 16, 31),
    "M": (17, 27, 21, 21, 17, 17, 17),
    "N": (17, 25, 21, 19, 17, 17, 17),
    "O": (14, 17, 17, 17, 17, 17, 14),
    "P": (30, 17, 17, 30, 16, 16, 16),
    "Q": (14, 17, 17, 17, 21, 18, 13),
    "R": (30, 17, 17, 30, 20, 18, 17),
    "S": (15, 16, 16, 14, 1, 1, 30),
    "T": (31, 4, 4, 4, 4, 4, 4),
    "U": (17, 17, 17, 17, 17, 17, 14),
    "V": (17, 17, 17, 17, 17, 10, 4),
    "W": (17, 17, 17, 21, 21, 21, 10),
    "X": (17, 17, 10, 4, 10, 17, 17),
    "Y": (17, 17, 10, 4, 4, 4, 4),
    "Z": (31, 1, 2, 4, 8, 16, 31),
    "[": (14, 8, 8, 8, 8, 8, 14),
    "\\": (16, 16, 8, 4, 2, 1, 1),
    "]": (14, 2, 2, 2, 2, 2, 14),
    "^": (4, 10, 17, 0, 0, 0, 0),
    "_": (0, 0, 0, 0, 0, 0, 31),
}


def _rgba(color: Sequence[int]) -> tuple[int, int, int, int]:
    if len(color) == 4:
        return (int(color[0]) & 255, int(color[1]) & 255, int(color[2]) & 255, int(color[3]) & 255)
    return (int(color[0]) & 255, int(color[1]) & 255, int(color[2]) & 255, 255)


class Canvas:
    """Pure-Python RGBA pixel canvas with zero-dependency PNG and BMP export."""

    def __init__(self, width: int, height: int, bg: Sequence[int] = (255, 255, 255, 255)) -> None:
        if width <= 0 or height <= 0 or width > 4096 or height > 4096:
            raise ValueError("canvas dimensions must be in 1..4096")
        self.width = int(width)
        self.height = int(height)
        r, g, b, a = _rgba(bg)
        self.buf = bytearray([r, g, b, a]) * (self.width * self.height)

    def clear(self, color: Sequence[int] = (255, 255, 255, 255)) -> None:
        r, g, b, a = _rgba(color)
        self.buf[:] = bytes([r, g, b, a]) * (self.width * self.height)

    def set_pixel(self, x: int, y: int, color: Sequence[int]) -> None:
        if 0 <= x < self.width and 0 <= y < self.height:
            r, g, b, a = _rgba(color)
            idx = (y * self.width + x) * 4
            if a >= 255:
                self.buf[idx : idx + 4] = bytes((r, g, b, 255))
            elif a > 0:
                inv = 255 - a
                self.buf[idx] = (r * a + self.buf[idx] * inv) // 255
                self.buf[idx + 1] = (g * a + self.buf[idx + 1] * inv) // 255
                self.buf[idx + 2] = (b * a + self.buf[idx + 2] * inv) // 255
                self.buf[idx + 3] = min(255, a + (self.buf[idx + 3] * inv) // 255)

    def fill_rect(self, x: int, y: int, w: int, h: int, color: Sequence[int]) -> None:
        x0 = max(0, int(x))
        y0 = max(0, int(y))
        x1 = min(self.width, int(x + w))
        y1 = min(self.height, int(y + h))
        if x0 >= x1 or y0 >= y1:
            return
        r, g, b, a = _rgba(color)
        if a >= 255:
            row_bytes = bytes((r, g, b, 255)) * (x1 - x0)
            stride = self.width * 4
            for py in range(y0, y1):
                start = py * stride + x0 * 4
                self.buf[start : start + len(row_bytes)] = row_bytes
        else:
            for py in range(y0, y1):
                for px in range(x0, x1):
                    self.set_pixel(px, py, (r, g, b, a))

    def draw_rect(self, x: int, y: int, w: int, h: int, color: Sequence[int], thickness: int = 1) -> None:
        t = max(1, int(thickness))
        self.fill_rect(x, y, w, t, color)
        self.fill_rect(x, y + h - t, w, t, color)
        self.fill_rect(x, y, t, h, color)
        self.fill_rect(x + w - t, y, t, h, color)

    def draw_line(self, x0: int, y0: int, x1: int, y1: int, color: Sequence[int], thickness: int = 1) -> None:
        x0, y0, x1, y1 = int(x0), int(y0), int(x1), int(y1)
        dx = abs(x1 - x0)
        dy = -abs(y1 - y0)
        sx = 1 if x0 < x1 else -1
        sy = 1 if y0 < y1 else -1
        err = dx + dy
        rad = max(0, int(thickness) - 1)
        while True:
            if rad == 0:
                self.set_pixel(x0, y0, color)
            else:
                self.fill_rect(x0 - rad // 2, y0 - rad // 2, rad + 1, rad + 1, color)
            if x0 == x1 and y0 == y1:
                break
            e2 = 2 * err
            if e2 >= dy:
                err += dy
                x0 += sx
            if e2 <= dx:
                err += dx
                y0 += sy

    def fill_circle(self, cx: int, cy: int, radius: int, color: Sequence[int]) -> None:
        cx, cy, r = int(cx), int(cy), int(radius)
        if r <= 0:
            return
        r2 = r * r
        for dy in range(-r, r + 1):
            span = int(math.isqrt(r2 - dy * dy))
            self.fill_rect(cx - span, cy + dy, 2 * span + 1, 1, color)

    def draw_circle(self, cx: int, cy: int, radius: int, color: Sequence[int], thickness: int = 1) -> None:
        cx, cy, r = int(cx), int(cy), int(radius)
        if r <= 0:
            return
        inner = max(0, r - max(1, int(thickness)))
        r2, inner2 = r * r, inner * inner
        for dy in range(-r, r + 1):
            for dx in range(-r, r + 1):
                d2 = dx * dx + dy * dy
                if inner2 <= d2 <= r2:
                    self.set_pixel(cx + dx, cy + dy, color)

    def gradient_v(self, top: Sequence[int], bottom: Sequence[int]) -> None:
        t = _rgba(top)
        b = _rgba(bottom)
        denom = max(1, self.height - 1)
        for y in range(self.height):
            c = tuple(t[i] + (b[i] - t[i]) * y // denom for i in range(4))
            self.fill_rect(0, y, self.width, 1, c)

    def draw_text(self, x: int, y: int, text: str, color: Sequence[int] = (20, 20, 20), scale: int = 2) -> None:
        s = max(1, int(scale))
        cursor_x, cursor_y = int(x), int(y)
        for ch in str(text):
            if ch == "\n":
                cursor_x = int(x)
                cursor_y += 9 * s
                continue
            glyph = _FONT_5X7.get(ch.upper(), _FONT_5X7["?"])
            for row_idx, bits in enumerate(glyph):
                for col_idx in range(5):
                    if (bits >> (4 - col_idx)) & 1:
                        self.fill_rect(cursor_x + col_idx * s, cursor_y + row_idx * s, s, s, color)
            cursor_x += 6 * s

    def to_png_bytes(self) -> bytes:
        stride = self.width * 4
        raw = bytearray()
        for y in range(self.height):
            raw.append(0)  # filter type 0 (None)
            raw.extend(self.buf[y * stride : (y + 1) * stride])

        def chunk(tag: bytes, payload: bytes) -> bytes:
            crc = zlib.crc32(tag + payload) & 0xFFFFFFFF
            return struct.pack(">I", len(payload)) + tag + payload + struct.pack(">I", crc)

        ihdr = struct.pack(">IIBBBBB", self.width, self.height, 8, 6, 0, 0, 0)
        idat = zlib.compress(bytes(raw), level=6)
        return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", ihdr) + chunk(b"IDAT", idat) + chunk(b"IEND", b"")

    def save_png(self, path: str | Path) -> Path:
        out = Path(path)
        out.parent.mkdir(parents=True, exist_ok=True)
        out.write_bytes(self.to_png_bytes())
        return out

    def save_bmp(self, path: str | Path) -> Path:
        out = Path(path)
        out.parent.mkdir(parents=True, exist_ok=True)
        row_pad = (4 - (self.width * 3) % 4) % 4
        pixel_bytes = bytearray()
        stride = self.width * 4
        for y in range(self.height - 1, -1, -1):
            row = self.buf[y * stride : (y + 1) * stride]
            for x in range(self.width):
                r, g, b = row[x * 4], row[x * 4 + 1], row[x * 4 + 2]
                pixel_bytes.extend((b, g, r))
            pixel_bytes.extend(b"\x00" * row_pad)
        file_size = 54 + len(pixel_bytes)
        header = struct.pack("<2sIHHI", b"BM", file_size, 0, 0, 54)
        dib = struct.pack("<IiiHHIIiiII", 40, self.width, self.height, 1, 24, 0, len(pixel_bytes), 2835, 2835, 0, 0)
        out.write_bytes(header + dib + bytes(pixel_bytes))
        return out


_PALETTE = (
    "#3B82F6", "#10B981", "#F59E0B", "#EF4444", "#8B5CF6",
    "#EC4899", "#06B6D4", "#84CC16", "#F97316", "#6366F1",
)


def save_svg(path: str | Path, svg_content: str) -> Path:
    out = Path(path)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(svg_content, encoding="utf-8")
    return out


def svg_bar_chart(
    path: str | Path,
    labels: Sequence[str],
    values: Sequence[float],
    title: str = "",
    width: int = 720,
    height: int = 420,
) -> Path:
    if len(labels) != len(values) or not labels:
        raise ValueError("labels and values must be non-empty and have equal length")
    pad_l, pad_r, pad_t, pad_b = 64, 28, 56 if title else 28, 60
    plot_w = width - pad_l - pad_r
    plot_h = height - pad_t - pad_b
    max_v = max(max(float(v) for v in values), 0.0)
    min_v = min(min(float(v) for v in values), 0.0)
    span = (max_v - min_v) or 1.0
    zero_y = pad_t + plot_h * (max_v / span)
    slot = plot_w / len(labels)
    bar_w = max(8.0, slot * 0.62)

    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}">',
        f'<rect width="{width}" height="{height}" fill="#FFFFFF" rx="12"/>',
    ]
    if title:
        parts.append(
            f'<text x="{width / 2:.1f}" y="32" text-anchor="middle" font-family="sans-serif" font-size="18" font-weight="600" fill="#111827">{html.escape(str(title))}</text>'
        )
    for step in range(5):
        val = min_v + span * step / 4
        gy = pad_t + plot_h * (1 - step / 4)
        parts.append(f'<line x1="{pad_l}" y1="{gy:.1f}" x2="{width - pad_r}" y2="{gy:.1f}" stroke="#E5E7EB" stroke-width="1"/>')
        parts.append(
            f'<text x="{pad_l - 8}" y="{gy + 4:.1f}" text-anchor="end" font-family="sans-serif" font-size="11" fill="#6B7280">{val:g}</text>'
        )
    for idx, (label, raw_val) in enumerate(zip(labels, values)):
        v = float(raw_val)
        x = pad_l + idx * slot + (slot - bar_w) / 2
        y_val = pad_t + plot_h * ((max_v - v) / span)
        top = min(zero_y, y_val)
        bh = max(1.5, abs(zero_y - y_val))
        color = _PALETTE[idx % len(_PALETTE)]
        parts.append(f'<rect x="{x:.1f}" y="{top:.1f}" width="{bar_w:.1f}" height="{bh:.1f}" fill="{color}" rx="4"/>')
        parts.append(
            f'<text x="{x + bar_w / 2:.1f}" y="{top - 6:.1f}" text-anchor="middle" font-family="sans-serif" font-size="11" fill="#374151">{v:g}</text>'
        )
        parts.append(
            f'<text x="{x + bar_w / 2:.1f}" y="{height - pad_b + 20}" text-anchor="middle" font-family="sans-serif" font-size="12" fill="#374151">{html.escape(str(label))}</text>'
        )
    parts.append("</svg>")
    return save_svg(path, "\n".join(parts))


def svg_line_chart(
    path: str | Path,
    series: dict[str, Sequence[float]] | Sequence[float],
    labels: Sequence[str] | None = None,
    title: str = "",
    width: int = 720,
    height: int = 420,
) -> Path:
    named = {"Series": series} if not isinstance(series, dict) else dict(series)
    if not named:
        raise ValueError("series must not be empty")
    length = max(len(v) for v in named.values())
    if length == 0:
        raise ValueError("series values must not be empty")
    all_vals = [float(x) for seq in named.values() for x in seq]
    min_v, max_v = min(all_vals), max(all_vals)
    if min_v == max_v:
        min_v -= 1.0
        max_v += 1.0
    span = max_v - min_v
    pad_l, pad_r, pad_t, pad_b = 64, 28, 56 if title else 28, 64
    plot_w = width - pad_l - pad_r
    plot_h = height - pad_t - pad_b
    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}">',
        f'<rect width="{width}" height="{height}" fill="#FFFFFF" rx="12"/>',
    ]
    if title:
        parts.append(
            f'<text x="{width / 2:.1f}" y="32" text-anchor="middle" font-family="sans-serif" font-size="18" font-weight="600" fill="#111827">{html.escape(str(title))}</text>'
        )
    for step in range(5):
        val = min_v + span * step / 4
        gy = pad_t + plot_h * (1 - step / 4)
        parts.append(f'<line x1="{pad_l}" y1="{gy:.1f}" x2="{width - pad_r}" y2="{gy:.1f}" stroke="#E5E7EB" stroke-width="1"/>')
        parts.append(
            f'<text x="{pad_l - 8}" y="{gy + 4:.1f}" text-anchor="end" font-family="sans-serif" font-size="11" fill="#6B7280">{val:g}</text>'
        )
    if labels:
        for idx, lbl in enumerate(labels):
            px = pad_l + (plot_w * idx / max(1, len(labels) - 1))
            parts.append(
                f'<text x="{px:.1f}" y="{height - pad_b + 22}" text-anchor="middle" font-family="sans-serif" font-size="11" fill="#374151">{html.escape(str(lbl))}</text>'
            )
    for s_idx, (name, seq) in enumerate(named.items()):
        color = _PALETTE[s_idx % len(_PALETTE)]
        pts = []
        for idx, raw_val in enumerate(seq):
            px = pad_l + (plot_w * idx / max(1, len(seq) - 1))
            py = pad_t + plot_h * (1 - (float(raw_val) - min_v) / span)
            pts.append((px, py))
        poly = " ".join(f"{px:.1f},{py:.1f}" for px, py in pts)
        parts.append(f'<polyline fill="none" stroke="{color}" stroke-width="2.5" points="{poly}"/>')
        for px, py in pts:
            parts.append(f'<circle cx="{px:.1f}" cy="{py:.1f}" r="3.5" fill="{color}"/>')
        if len(named) > 1:
            lx = pad_l + s_idx * 120
            ly = height - 16
            parts.append(f'<rect x="{lx}" y="{ly - 9}" width="12" height="12" rx="2" fill="{color}"/>')
            parts.append(
                f'<text x="{lx + 16}" y="{ly + 1}" font-family="sans-serif" font-size="11" fill="#374151">{html.escape(str(name))}</text>'
            )
    parts.append("</svg>")
    return save_svg(path, "\n".join(parts))


def svg_pie_chart(
    path: str | Path,
    labels: Sequence[str],
    values: Sequence[float],
    title: str = "",
    width: int = 640,
    height: int = 400,
) -> Path:
    if len(labels) != len(values) or not labels:
        raise ValueError("labels and values must be non-empty and have equal length")
    nums = [max(0.0, float(v)) for v in values]
    total = sum(nums) or 1.0
    cx, cy, r = width * 0.36, height * 0.54, min(width, height) * 0.30
    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}">',
        f'<rect width="{width}" height="{height}" fill="#FFFFFF" rx="12"/>',
    ]
    if title:
        parts.append(
            f'<text x="{width / 2:.1f}" y="32" text-anchor="middle" font-family="sans-serif" font-size="18" font-weight="600" fill="#111827">{html.escape(str(title))}</text>'
        )
    angle = -math.pi / 2
    for idx, (label, val) in enumerate(zip(labels, nums)):
        frac = val / total
        sweep = frac * 2 * math.pi
        color = _PALETTE[idx % len(_PALETTE)]
        if frac >= 0.9999:
            parts.append(f'<circle cx="{cx:.1f}" cy="{cy:.1f}" r="{r:.1f}" fill="{color}"/>')
        elif frac > 0:
            x1, y1 = cx + r * math.cos(angle), cy + r * math.sin(angle)
            x2, y2 = cx + r * math.cos(angle + sweep), cy + r * math.sin(angle + sweep)
            large = 1 if sweep > math.pi else 0
            parts.append(
                f'<path d="M {cx:.2f} {cy:.2f} L {x1:.2f} {y1:.2f} A {r:.2f} {r:.2f} 0 {large} 1 {x2:.2f} {y2:.2f} Z" fill="{color}" stroke="#FFFFFF" stroke-width="1.5"/>'
            )
        angle += sweep
        ly = 72 + idx * 26
        lx = width * 0.68
        pct = frac * 100
        parts.append(f'<rect x="{lx:.1f}" y="{ly - 11}" width="12" height="12" rx="3" fill="{color}"/>')
        parts.append(
            f'<text x="{lx + 18:.1f}" y="{ly}" font-family="sans-serif" font-size="12" fill="#374151">{html.escape(str(label))} ({pct:.1f}%)</text>'
        )
    parts.append("</svg>")
    return save_svg(path, "\n".join(parts))


_NOTE_OFFSETS = {
    "C": 0, "C#": 1, "DB": 1, "D": 2, "D#": 3, "EB": 3, "E": 4,
    "F": 5, "F#": 6, "GB": 6, "G": 7, "G#": 8, "AB": 8, "A": 9, "A#": 10, "BB": 10, "B": 11,
}


def note_to_midi(note: str | int) -> int:
    if isinstance(note, int):
        return max(0, min(127, note))
    s = str(note).strip().upper()
    if s in ("R", "REST", "-"):
        return -1
    if len(s) >= 2 and s[1] in ("#", "B"):
        name, octave = s[:2], int(s[2:])
    else:
        name, octave = s[:1], int(s[1:])
    return max(0, min(127, (octave + 1) * 12 + _NOTE_OFFSETS[name]))


def write_wav(path: str | Path, samples: Iterable[float], sample_rate: int = 44100) -> Path:
    out = Path(path)
    out.parent.mkdir(parents=True, exist_ok=True)
    pcm = bytearray()
    for s in samples:
        clamped = max(-1.0, min(1.0, float(s)))
        pcm.extend(struct.pack("<h", int(clamped * 32767)))
    with wave.open(str(out), "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(2)
        wf.setframerate(int(sample_rate))
        wf.writeframes(bytes(pcm))
    return out


def synth_notes(
    path: str | Path,
    notes: Sequence[tuple[str | int, float]],
    bpm: float = 120.0,
    waveform: str = "sine",
    sample_rate: int = 44100,
    volume: float = 0.45,
) -> Path:
    beat_sec = 60.0 / max(1.0, float(bpm))
    samples: list[float] = []
    for pitch_spec, beats in notes:
        dur = max(0.01, float(beats) * beat_sec)
        count = int(dur * sample_rate)
        midi_num = note_to_midi(pitch_spec)
        if midi_num < 0:
            samples.extend([0.0] * count)
            continue
        freq = 440.0 * (2.0 ** ((midi_num - 69) / 12.0))
        attack = max(1, min(int(0.01 * sample_rate), count // 4))
        release = max(1, min(int(0.04 * sample_rate), count // 3))
        for i in range(count):
            phase = (i * freq / sample_rate) % 1.0
            if waveform == "square":
                val = 1.0 if phase < 0.5 else -1.0
            elif waveform == "triangle":
                val = 4.0 * abs(phase - 0.5) - 1.0
            elif waveform == "saw":
                val = 2.0 * phase - 1.0
            else:
                val = math.sin(2.0 * math.pi * phase)
            env = 1.0
            if i < attack:
                env = i / attack
            elif i > count - release:
                env = (count - i) / release
            samples.append(val * env * volume)
    return write_wav(path, samples, sample_rate=sample_rate)


def _vlq(value: int) -> bytes:
    value = max(0, int(value))
    out = [value & 0x7F]
    value >>= 7
    while value:
        out.append((value & 0x7F) | 0x80)
        value >>= 7
    out.reverse()
    return bytes(out)


class MIDIBuilder:
    """Multi-track standard MIDI file (.mid) builder."""

    def __init__(self, bpm: float = 120.0, ticks_per_beat: int = 480) -> None:
        self.bpm = float(bpm)
        self.tpb = int(ticks_per_beat)
        self._tracks: dict[int, list[tuple[int, int, bytes]]] = {}

    def set_instrument(self, track: int, channel: int, program: int, beat: float = 0.0) -> None:
        tick = int(round(beat * self.tpb))
        self._tracks.setdefault(track, []).append((tick, 0, bytes([0xC0 | (channel & 0x0F), program & 0x7F])))

    def add_note(
        self,
        track: int,
        pitch: str | int,
        start_beat: float,
        duration_beats: float,
        velocity: int = 96,
        channel: int = 0,
    ) -> None:
        midi_num = note_to_midi(pitch)
        if midi_num < 0:
            return
        t0 = max(0, int(round(start_beat * self.tpb)))
        t1 = max(t0 + 1, int(round((start_beat + duration_beats) * self.tpb)))
        ch = channel & 0x0F
        vel = max(1, min(127, int(velocity)))
        ev = self._tracks.setdefault(track, [])
        ev.append((t0, 2, bytes([0x90 | ch, midi_num, vel])))
        ev.append((t1, 1, bytes([0x80 | ch, midi_num, 0])))

    def add_chord(
        self,
        track: int,
        pitches: Sequence[str | int],
        start_beat: float,
        duration_beats: float,
        velocity: int = 90,
        channel: int = 0,
    ) -> None:
        for p in pitches:
            self.add_note(track, p, start_beat, duration_beats, velocity=velocity, channel=channel)

    def save(self, path: str | Path) -> Path:
        out = Path(path)
        out.parent.mkdir(parents=True, exist_ok=True)
        tempo_us = max(1, int(round(60_000_000 / max(1.0, self.bpm))))
        tempo_track = _vlq(0) + b"\xFF\x51\x03" + tempo_us.to_bytes(3, "big") + _vlq(0) + b"\xFF\x2F\x00"
        chunks = [b"MTrk" + struct.pack(">I", len(tempo_track)) + tempo_track]
        for _, events in sorted(self._tracks.items()):
            ordered = sorted(events, key=lambda item: (item[0], item[1]))
            buf = bytearray()
            last_tick = 0
            for tick, _, msg in ordered:
                buf.extend(_vlq(tick - last_tick))
                buf.extend(msg)
                last_tick = tick
            buf.extend(_vlq(0) + b"\xFF\x2F\x00")
            chunks.append(b"MTrk" + struct.pack(">I", len(buf)) + bytes(buf))
        header = b"MThd" + struct.pack(">IHHH", 6, 1, len(chunks), self.tpb)
        out.write_bytes(header + b"".join(chunks))
        return out


def describe_numbers(values: Iterable[float]) -> dict[str, float | int]:
    nums = sorted(float(v) for v in values if v is not None and not math.isnan(float(v)))
    if not nums:
        return {"count": 0}
    n = len(nums)

    def pct(p: float) -> float:
        pos = (n - 1) * p
        lo = int(math.floor(pos))
        hi = int(math.ceil(pos))
        return nums[lo] + (nums[hi] - nums[lo]) * (pos - lo)

    return {
        "count": n,
        "sum": sum(nums),
        "min": nums[0],
        "p25": pct(0.25),
        "median": statistics.median(nums),
        "mean": statistics.fmean(nums),
        "p75": pct(0.75),
        "p90": pct(0.90),
        "max": nums[-1],
        "stdev": statistics.pstdev(nums) if n > 1 else 0.0,
    }


def read_csv(path: str | Path) -> list[dict[str, str]]:
    with Path(path).open("r", encoding="utf-8-sig", newline="") as f:
        return list(csv.DictReader(f))


def write_csv(path: str | Path, rows: Sequence[dict[str, object]], fieldnames: Sequence[str] | None = None) -> Path:
    out = Path(path)
    out.parent.mkdir(parents=True, exist_ok=True)
    keys = list(fieldnames) if fieldnames else (list(rows[0].keys()) if rows else [])
    with out.open("w", encoding="utf-8", newline="") as f:
        writer = csv.DictWriter(f, fieldnames=keys)
        if keys:
            writer.writeheader()
            writer.writerows(rows)
    return out


def paper_html(
    title: str,
    authors: str = "",
    abstract: str = "",
    body_html: str = "",
    columns: int = 2,
    date: str = "",
) -> str:
    """Build a self-contained offline arXiv-style academic HTML document ready for PDF/PNG rendering."""
    col_css = f"column-count: {max(1, int(columns))}; column-gap: 7mm;" if int(columns) > 1 else ""
    meta_parts = []
    if authors:
        meta_parts.append(f'<div class="authors">{html.escape(authors)}</div>')
    if date:
        meta_parts.append(f'<div class="date">{html.escape(date)}</div>')
    abs_block = ""
    if abstract:
        abs_block = f'<div class="abstract"><strong>Abstract — </strong>{html.escape(abstract)}</div>'
    return f"""<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>{html.escape(title)}</title>
<style>
  @page {{ size: A4; margin: 16mm 14mm; }}
  * {{ box-sizing: border-box; }}
  body {{
    margin: 0;
    padding: 0;
    color: #111827;
    background: #ffffff;
    font-family: "Times New Roman", "Noto Serif CJK SC", "Songti SC", "PingFang SC", "Noto Sans CJK SC", serif;
    font-size: 10pt;
    line-height: 1.52;
  }}
  header.paper-header {{
    text-align: center;
    margin-bottom: 5mm;
    padding-bottom: 3mm;
    border-bottom: 0.6pt solid #d1d5db;
  }}
  h1 {{ font-size: 16.5pt; line-height: 1.25; margin: 0 0 2.5mm 0; font-weight: 700; }}
  .authors {{ font-size: 10.5pt; color: #1f2937; margin-bottom: 1mm; }}
  .date {{ font-size: 9pt; color: #6b7280; }}
  .abstract {{
    margin: 0 8mm 5mm 8mm;
    padding: 3mm 4mm;
    background: #f9fafb;
    border-left: 2.2pt solid #2563eb;
    font-size: 9.3pt;
    text-align: justify;
  }}
  main.paper-body {{ {col_css} text-align: justify; }}
  h2 {{ font-size: 11.5pt; margin: 4mm 0 1.8mm 0; break-after: avoid; }}
  h3 {{ font-size: 10.2pt; margin: 3mm 0 1.2mm 0; break-after: avoid; }}
  p {{ margin: 0 0 2.4mm 0; }}
  figure {{ margin: 3mm 0; break-inside: avoid; text-align: center; }}
  figure.wide {{ column-span: all; margin: 4mm 0; }}
  figcaption {{ font-size: 8.8pt; color: #4b5563; margin-top: 1.2mm; }}
  table {{
    width: 100%;
    border-collapse: collapse;
    font-size: 8.8pt;
    margin: 3mm 0;
    break-inside: avoid;
  }}
  thead th {{ border-top: 1.2pt solid #111827; border-bottom: 0.7pt solid #111827; padding: 1.5mm 2mm; text-align: left; }}
  tbody td {{ padding: 1.2mm 2mm; border-bottom: 0.3pt solid #e5e7eb; }}
  tbody tr:last-child td {{ border-bottom: 1.2pt solid #111827; }}
  pre, code {{ font-family: "SFMono-Regular", Menlo, Consolas, monospace; font-size: 8.5pt; }}
  pre {{ background: #f3f4f6; padding: 2.5mm; border-radius: 3pt; overflow-x: auto; break-inside: avoid; }}
  .eq {{ text-align: center; font-style: italic; margin: 2.5mm 0; }}
</style>
</head>
<body>
  <header class="paper-header">
    <h1>{html.escape(title)}</h1>
    {"".join(meta_parts)}
  </header>
  {abs_block}
  <main class="paper-body">{body_html}</main>
</body>
</html>"""


def _is_existing_file_path(value: str | Path) -> bool:
    if isinstance(value, Path):
        try:
            return value.is_file()
        except OSError:
            return False
    text = str(value)
    if not text or len(text) > 1024 or "\n" in text or "\r" in text or text.lstrip("\ufeff \t").startswith("<"):
        return False
    try:
        return Path(text).is_file()
    except OSError:
        return False


def _resolve_html_file(html_or_path: str | Path, scratch_dir: Path) -> Path:
    if _is_existing_file_path(html_or_path):
        return Path(html_or_path).resolve()
    scratch_dir.mkdir(parents=True, exist_ok=True)
    target = scratch_dir / "render_input.html"
    target.write_text(str(html_or_path), encoding="utf-8")
    return target.resolve()


def _find_browser() -> str:
    for name in ("chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "microsoft-edge", "msedge", "chrome"):
        found = shutil.which(name)
        if found:
            return found
    for candidate in (
        "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
        "/Applications/Chromium.app/Contents/MacOS/Chromium",
        "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
        r"C:\Program Files\Google\Chrome\Application\chrome.exe",
        r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
    ):
        try:
            if Path(candidate).is_file():
                return candidate
        except OSError:
            continue
    raise RuntimeError("headless chromium is not available in this sandbox; write HTML and pass its path to render_media(format='html', path=..., output='pdf') instead")


def _run_headless_browser(browser: str, profile: Path, extra_flags: list[str], target_uri: str) -> None:
    env = dict(os.environ)
    env.setdefault("DBUS_SESSION_BUS_ADDRESS", "/dev/null")
    last_err = ""
    for headless_flag in ("--headless=new", "--headless"):
        cmd = [
            browser,
            headless_flag,
            "--disable-gpu",
            "--no-sandbox",
            "--disable-setuid-sandbox",
            "--disable-dev-shm-usage",
            "--disable-crash-reporter",
            "--disable-breakpad",
            "--no-first-run",
            "--no-default-browser-check",
            "--disable-background-networking",
            "--proxy-server=http://127.0.0.1:9",
            f"--user-data-dir={profile}",
            *extra_flags,
            target_uri,
        ]
        try:
            proc = subprocess.run(cmd, capture_output=True, text=True, timeout=25, env=env)
        except OSError as exc:
            raise RuntimeError(
                f"subprocesses are restricted in this host sandbox ({exc}); write HTML to a file and call render_media(format='html', path=..., output='pdf') instead"
            ) from exc
        if proc.returncode == 0:
            return
        last_err = f"code {proc.returncode}: {proc.stderr.strip()}"
    raise RuntimeError(f"chromium headless render failed ({last_err})")


def render_pdf(html_or_path: str | Path, out_path: str | Path = "document.pdf") -> Path:
    """Offline HTML/SVG -> PDF renderer via headless Chromium inside the container sandbox."""
    browser = _find_browser()
    out = Path(out_path)
    out.parent.mkdir(parents=True, exist_ok=True)
    tmp_root = Path(os.environ.get("TMPDIR", ".tmp"))
    src = _resolve_html_file(html_or_path, tmp_root)
    profile = tmp_root / "_chrome_pdf_profile"
    profile.mkdir(parents=True, exist_ok=True)
    _run_headless_browser(
        browser,
        profile,
        ["--no-pdf-header-footer", f"--print-to-pdf={out.resolve()}"],
        src.as_uri(),
    )
    if not out.is_file():
        raise RuntimeError("chromium pdf completed without producing output file")
    return out


def render_png(
    html_or_path: str | Path,
    out_path: str | Path = "render.png",
    width: int = 1000,
    height: int = 1400,
) -> Path:
    """Offline HTML/SVG -> PNG renderer via headless Chromium inside the container sandbox."""
    browser = _find_browser()
    out = Path(out_path)
    out.parent.mkdir(parents=True, exist_ok=True)
    tmp_root = Path(os.environ.get("TMPDIR", ".tmp"))
    src = _resolve_html_file(html_or_path, tmp_root)
    profile = tmp_root / "_chrome_png_profile"
    profile.mkdir(parents=True, exist_ok=True)
    _run_headless_browser(
        browser,
        profile,
        [f"--window-size={int(width)},{int(height)}", f"--screenshot={out.resolve()}"],
        src.as_uri(),
    )
    if not out.is_file():
        raise RuntimeError("chromium screenshot completed without producing output file")
    return out


def write_text(path: str | Path, content: str) -> Path:
    """Write UTF-8 text to path, creating parent directories automatically."""
    target = Path(path)
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(str(content), encoding="utf-8")
    return target


def read_text(path: str | Path) -> str:
    """Read UTF-8 text from path."""
    return Path(path).read_text(encoding="utf-8")
`

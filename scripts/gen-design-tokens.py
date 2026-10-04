#!/usr/bin/env python3
"""Генератор CSS-переменных из дизайн-файла proto/auth-flow.pen (issue #88).

Дизайн-файл — единственный источник правды: скрипт читает variables
(цвета со значениями на light/dark, радиусы, шрифты) и выдаёт
services/parser-service/internal/mapview/tokens.gen.css:

  :root                        — light (по умолчанию)
  [data-theme="dark"]          — dark (ручной override)
  @media prefers-color-scheme  — dark для auto-режима (:root без override)

В конец файла добавляется секция «map extension» — цвета элементов карты,
которых нет в дизайн-файле (шапка, палитра диапазонов доходности, границы
маркеров). Они тоже живут только здесь (в переменных): компоненты карты
никогда не хардкодят цвета.

Запуск: python3 scripts/gen-design-tokens.py
"""

import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
PEN = ROOT / "proto" / "auth-flow.pen"
OUT = ROOT / "services" / "parser-service" / "internal" / "mapview" / "tokens.gen.css"

HEADER = (
    "/* СГЕНЕРИРОВАНО scripts/gen-design-tokens.py из proto/auth-flow.pen —\n"
    "   НЕ ПРАВИТЬ ВРУЧНУЮ. Дизайн-файл — единственный источник токенов. */\n\n"
)


def token_values(var: dict) -> dict:
    """{mode: value} из переменной pen-файла."""
    val = var.get("value")
    if isinstance(val, list):  # [{"value": ..., "theme": {"mode": ...}}]
        return {item["theme"]["mode"]: item["value"] for item in val}
    return {"light": val, "dark": val}


def css_name(name: str, kind: str) -> str:
    if kind == "string":  # font-body → --font-body (без дубля префикса)
        return f"--{name}"
    if kind == "number":  # radius-md → --radius-md (без дубля префикса)
        return f"--{name}"
    return f"--color-{name}"


def fmt_value(kind: str, value) -> str:
    if kind == "number":
        return f"{value}px"
    return str(value)


# Расширение для карты: цвета элементов, которых нет в дизайн-файле (#73).
# В dark-теме палитра диапазонов высветлена под тёмные тайлы; на ярких
# диапазонах (c2, c3) текст — тёмный (контраст), см. --map-bucket-fg-bright.
MAP_TOKENS = {
    "map-header-bg": {"light": "#232A35", "dark": "#0B1411"},
    "map-header-text": {"light": "#FFFFFF", "dark": "#F1F7F4"},
    "map-header-muted": {"light": "#9AA4B2", "dark": "#8FA39A"},
    "map-pin-border": {"light": "#FFFFFF", "dark": "#13201A"},
    "map-shadow": {"light": "rgba(0, 0, 0, .25)", "dark": "rgba(0, 0, 0, .6)"},
    "map-shadow-lg": {"light": "rgba(0, 0, 0, .25)", "dark": "rgba(0, 0, 0, .7)"},
    "map-bucket-0": {"light": "#b71c1c", "dark": "#e07b72"},
    "map-bucket-1": {"light": "#d84315", "dark": "#f08c5a"},
    "map-bucket-2": {"light": "#ea7600", "dark": "#ffab40"},
    "map-bucket-3": {"light": "#c79500", "dark": "#ffd54f"},
    "map-bucket-4": {"light": "#9e9d24", "dark": "#d4ce46"},
    "map-bucket-5": {"light": "#558b2f", "dark": "#8bc34a"},
    "map-bucket-6": {"light": "#1b5e20", "dark": "#66bb6a"},
    "map-bucket-gray": {"light": "#9e9e9e", "dark": "#7a8b83"},
    "map-bucket-fg": {"light": "#FFFFFF", "dark": "#F1F7F4"},
    "map-bucket-fg-bright": {"light": "#FFFFFF", "dark": "#13201A"},
}


def block(selector: str, mode: str) -> str:
    lines = [f"{selector} {{"]
    for name, var in sorted(VARIABLES.items()):
        vals = token_values(var)
        if mode in vals:
            lines.append(f"  {css_name(name, var['type'])}: {fmt_value(var['type'], vals[mode])};")
    for name, vals in sorted(MAP_TOKENS.items()):
        lines.append(f"  --{name}: {vals[mode]};")
    lines.append("}")
    return "\n".join(lines)


def main() -> int:
    global VARIABLES
    pen = json.loads(PEN.read_text(encoding="utf-8"))
    VARIABLES = pen.get("variables", {})
    if not VARIABLES:
        print("no variables in pen file", file=sys.stderr)
        return 1

    dark = block('[data-theme="dark"]', "dark")
    media = block('@media (prefers-color-scheme: dark) {\n:root:not([data-theme="light"])', "dark")

    out = [HEADER, block(":root", "light"), "", dark, "", media, ""]
    OUT.write_text("\n".join(out), encoding="utf-8")
    n = len(VARIABLES) + len(MAP_TOKENS)
    print(f"{OUT.relative_to(ROOT)}: {n} токенов ({len(VARIABLES)} из дизайн-файла + {len(MAP_TOKENS)} map)")
    return 0


if __name__ == "__main__":
    sys.exit(main())

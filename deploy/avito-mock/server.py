#!/usr/bin/env python3
"""Мок-сервер Авито для локальной проверки парсера (docker compose).

Отдаёт фикстуры из testdata по путям, имитирующим структуру URL Авито:
  /                                    -> простая страница (прогрев)
  .../prodam|sdam... (страница выдачи) -> search_mfe.html
  ..._<id>            (карточка)       -> item_mfe.html
  /img/*                               -> сгенерированный PNG-плейсхолдер
  иначе                               -> 404
"""
import http.server
import pathlib
import struct
import sys
import urllib.parse
import zlib

TD = pathlib.Path(sys.argv[2] if len(sys.argv) > 2 else "/srv/testdata")
PORT = int(sys.argv[1] if len(sys.argv) > 1 else "8080")

WARMUP = b"<!DOCTYPE html><html><head><title>Avito mock</title></head><body>ok</body></html>"


def make_png(w=64, h=64, rgb=(212, 176, 106)):
    """Минимальный валидный PNG-плейсхолдер (без внешних зависимостей)."""
    def chunk(tag, data):
        payload = tag + data
        return struct.pack(">I", len(data)) + payload + struct.pack(">I", zlib.crc32(payload) & 0xFFFFFFFF)
    ihdr = struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0)
    raw = b"".join(b"\x00" + bytes(rgb) * w for _ in range(h))
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", ihdr)
            + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b""))


PNG = make_png()


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        sys.stderr.write("[avito-mock] %s %s\n" % (self.command, self.path))

    def _send(self, body: bytes, code: int = 200, ctype: str = "text/html; charset=utf-8"):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = urllib.parse.urlparse(self.path).path
        if path in ("", "/"):
            self._send(WARMUP)
            return
        if path.startswith("/img/"):
            self._send(PNG, ctype="image/png")
            return
        # карточка объявления: ..._<числовой id>
        last = path.rstrip("/").rsplit("/", 1)[-1]
        if "_" in last and last.rsplit("_", 1)[-1].isdigit():
            self._send((TD / "item_mfe.html").read_bytes())
            return
        # страница выдачи: сегмент сделки в пути
        if any(s in path for s in ("prodam", "sdam", "kuplyu")):
            self._send((TD / "search_mfe.html").read_bytes())
            return
        self._send(b"<html><body>404</body></html>", code=404)


if __name__ == "__main__":
    server = http.server.ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    print(f"avito-mock listening on :{PORT}, fixtures: {TD}", flush=True)
    server.serve_forever()

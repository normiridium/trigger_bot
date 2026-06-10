#!/usr/bin/env python3
from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import tempfile
import urllib.parse
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ROLEPLAY_GO = ROOT / "internal" / "app" / "roleplay.go"
ENV_FILE = ROOT / ".env"
OUT_DIR = ROOT / "static" / "roleplay"
SIZE = 96


def read_env_key(path: Path, key: str) -> str:
    if not path.exists():
        return ""
    for line in path.read_text(errors="ignore").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        if k.strip() == key:
            return v.strip().strip('"').strip("'")
    return ""


def api(token: str, method: str, params: dict | None = None) -> dict:
    data = None
    if params:
        data = urllib.parse.urlencode(params).encode()
    req = urllib.request.Request(f"https://api.telegram.org/bot{token}/{method}", data=data)
    with urllib.request.urlopen(req, timeout=30) as r:
        payload = json.load(r)
    if not payload.get("ok"):
        raise RuntimeError(f"Telegram {method} failed: {payload!r}")
    return payload["result"]


def download(token: str, file_path: str, dest: Path) -> None:
    url = f"https://api.telegram.org/file/bot{token}/{file_path}"
    with urllib.request.urlopen(url, timeout=60) as r, dest.open("wb") as f:
        f.write(r.read())


def convert_to_jpeg(src: Path, dest: Path) -> bool:
    # Inline result thumbnails are most reliable as small JPEGs.
    cmd = [
        "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
        "-i", str(src),
        "-vf", f"scale={SIZE}:{SIZE}:force_original_aspect_ratio=decrease,pad={SIZE}:{SIZE}:(ow-iw)/2:(oh-ih)/2:color=#151827,setsar=1",
        "-frames:v", "1",
        "-q:v", "3",
        str(dest),
    ]
    return subprocess.run(cmd, cwd=str(ROOT)).returncode == 0 and dest.exists() and dest.stat().st_size > 0


def chunks(items: list[str], n: int):
    for i in range(0, len(items), n):
        yield items[i:i+n]


def main() -> int:
    token = read_env_key(ENV_FILE, "TELEGRAM_BOT_TOKEN") or os.getenv("TELEGRAM_BOT_TOKEN", "")
    if not token:
        print("TELEGRAM_BOT_TOKEN not found", file=sys.stderr)
        return 2
    text = ROLEPLAY_GO.read_text()
    ids = list(dict.fromkeys(re.findall(r'EmojiID:\s*"(\d+)"', text)))
    if not ids:
        print("No roleplay EmojiID values found", file=sys.stderr)
        return 2
    OUT_DIR.mkdir(parents=True, exist_ok=True)

    total = 0
    ok = 0
    skipped = 0
    failed: list[str] = []
    with tempfile.TemporaryDirectory(prefix="roleplay-previews-") as td:
        tmp = Path(td)
        for part in chunks(ids, 200):
            stickers = api(token, "getCustomEmojiStickers", {"custom_emoji_ids": json.dumps(part)})
            by_id = {str(s.get("custom_emoji_id")): s for s in stickers}
            for emoji_id in part:
                total += 1
                dest = OUT_DIR / f"{emoji_id}.jpg"
                if dest.exists() and dest.stat().st_size > 0:
                    skipped += 1
                    continue
                sticker = by_id.get(emoji_id)
                if not sticker:
                    failed.append(emoji_id)
                    continue
                thumb = sticker.get("thumbnail") or sticker.get("thumb") or {}
                file_id = thumb.get("file_id") or sticker.get("file_id")
                if not file_id:
                    failed.append(emoji_id)
                    continue
                try:
                    file_info = api(token, "getFile", {"file_id": file_id})
                    raw = tmp / f"{emoji_id}.raw"
                    download(token, file_info["file_path"], raw)
                    if convert_to_jpeg(raw, dest):
                        ok += 1
                    else:
                        failed.append(emoji_id)
                except Exception as e:
                    print(f"failed {emoji_id}: {e}", file=sys.stderr)
                    failed.append(emoji_id)
    print(f"roleplay previews: total={total} created={ok} skipped={skipped} failed={len(failed)} dir={OUT_DIR}")
    if failed:
        print("failed ids: " + ",".join(failed), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

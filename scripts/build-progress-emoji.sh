#!/usr/bin/env bash
# Пересобирает картинки для прогресс-виджета из кастомных эмодзи Telegram.
#
# Виджет (HTMLBubbles) не может ходить в сеть, поэтому эмодзи вшиваются в HTML
# как data:image/webp. Кастомные эмодзи — видеостикеры (WEBM 100x100): берём
# первый кадр, ужимаем до 48px с альфой, кладём в internal/app/progress_assets.
#
# Запуск на сервере от root: bash scripts/build-progress-emoji.sh
# Требует ffmpeg, cwebp и TELEGRAM_BOT_TOKEN в .env (только чтение).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/internal/app/progress_assets"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

TOKEN="$(sed -n 's/^TELEGRAM_BOT_TOKEN=//p' "$ROOT/.env" | head -1 | tr -d "\"'")"
[ -n "$TOKEN" ] || { echo "нет TELEGRAM_BOT_TOKEN в $ROOT/.env" >&2; exit 1; }
command -v ffmpeg >/dev/null || { echo "нет ffmpeg" >&2; exit 1; }
command -v cwebp >/dev/null || { echo "нет cwebp" >&2; exit 1; }

mkdir -p "$OUT"
IDS="$(grep -o 'emoji-id="[0-9]*"' "$ROOT/internal/app/media_progress.go" |
  sed 's/emoji-id="//; s/"//' | awk '!seen[$0]++' | paste -sd, -)"
[ -n "$IDS" ] || { echo "не нашёл emoji-id в media_progress.go" >&2; exit 1; }
echo "эмодзи: $IDS"

python3 - "$TOKEN" "$IDS" "$OUT" "$WORK" <<'PY'
import json
import os
import subprocess
import sys
import urllib.request

token, ids_arg, out_dir, work = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
ids = [i for i in ids_arg.split(",") if i]
api = f"https://api.telegram.org/bot{token}"


def call(method, payload):
    req = urllib.request.Request(f"{api}/{method}", data=json.dumps(payload).encode(),
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=40) as resp:
        body = json.loads(resp.read().decode())
    if not body.get("ok"):
        sys.exit(f"{method}: {body}")
    return body["result"]


stickers = {s["custom_emoji_id"]: s for s in call("getCustomEmojiStickers", {"custom_emoji_ids": ids})}
missing = [i for i in ids if i not in stickers]
if missing:
    sys.exit(f"нет стикеров для {missing}")

total = 0
for emoji_id in ids:
    sticker = stickers[emoji_id]
    file_info = call("getFile", {"file_id": sticker["file_id"]})
    url = f"https://api.telegram.org/file/bot{token}/{file_info['file_path']}"
    with urllib.request.urlopen(url, timeout=60) as resp:
        raw = resp.read()
    webm = os.path.join(work, f"{emoji_id}.webm")
    png = os.path.join(work, f"{emoji_id}.png")
    webp = os.path.join(out_dir, f"{emoji_id}.webp")
    with open(webm, "wb") as fh:
        fh.write(raw)
    subprocess.run(["ffmpeg", "-y", "-loglevel", "error", "-i", webm, "-frames:v", "1",
                    "-vf", "scale=48:48:flags=lanczos", png], check=True)
    subprocess.run(["cwebp", "-quiet", "-q", "70", "-m", "6", png, "-o", webp], check=True)
    size = os.path.getsize(webp)
    total += size
    print(f"  {sticker['emoji']} id={emoji_id}: webm {len(raw)} -> webp {size} байт")

print(f"итого webp: {total} байт (base64≈{total * 4 // 3}), лимит виджета 4096")
PY

chown -R faline:faline "$OUT" 2>/dev/null || true
echo "готово: $OUT"

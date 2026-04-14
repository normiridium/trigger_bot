# Trigger Admin Bot (Go)

Telegram-бот с web-админкой триггеров и поддержкой Spotify-аудио через `yt-dlp`.

## Что умеет
- Триггеры по `match_type` (`full`, `partial`, `regex`, `starts`, `ends`, `idle`, `new_member`)
- Действия: `send`, `delete`, `gpt_prompt`, `gpt_image`, `search_image`, `spotify_music_audio`
- Для `spotify_music_audio`:
  - поиск треков в Spotify
  - интерактивный список кнопок выбора
  - скачивание аудио через `yt-dlp` + `ffmpeg`
- Web-админка: список, создание, редактирование, reorder, import/export

## Зависимости
```bash
./scripts/install_deps.sh
```

Нужны: `ffmpeg`, `ffprobe`, `yt-dlp`, MongoDB.

## Переменные окружения
См. пример: `.env.example`.

Обязательные:
- `TELEGRAM_BOT_TOKEN`
- `MONGO_URI`
- `SPOTIPY_CLIENT_ID`
- `SPOTIPY_CLIENT_SECRET`

Ключевые для Spotify-аудио:
- `SPOTIFY_AUDIO_INTERACTIVE=true` — показывать список выбора
- `SPOTIFY_AUDIO_WORKERS=1` — число воркеров скачивания
- `SPOTIFY_AUDIO_QUEUE=8` — размер очереди задач скачивания
- `AUDIO_FORMAT=mp3`
- `AUDIO_QUALITY=320K`
- `YTDLP_BIN=/usr/local/bin/yt-dlp` (или оставить пустым, если есть в `PATH`)
- `FIXIE_SOCKS_HOST=` (опционально, SOCKS5 `host:port`)

Прочие важные:
- `ALLOWED_CHAT_IDS`
- `CHAT_ERROR_LOG`
- `DEBUG_TRIGGER_LOG`
- `DEBUG_GPT_LOG`
- `ADMIN_ENABLED`, `ADMIN_BIND`, `ADMIN_TOKEN`

## Запуск
```bash
cd /home/faline/trigger_admin_bot
set -a && source .env && set +a
/usr/local/go/bin/go build -o trigger_admin_bot .
./trigger_admin_bot
```

## Команды в чате
- `/start`
- `/help`
- `/emojiid`
- `/spsearch <запрос>`
- `/spfind <запрос>`

## Действие `spotify_music_audio`
В триггере укажи:
- `action_type = spotify_music_audio`
- `response_text` = шаблон поискового запроса (можно с переменными, например `{{message}}`)

Флаги триггера:
- `reply` — выбранный трек отправляется reply на исходное сообщение
- `delete_source_message` — исходное сообщение удаляется сразу после показа списка и после завершения выбора

## Проверка
```bash
cd /home/faline/trigger_admin_bot
/usr/local/go/bin/go test ./...
```

## Перезапуск systemd
```bash
sudo systemctl restart trigger-admin-bot.service
sudo systemctl status trigger-admin-bot.service --no-pager
```

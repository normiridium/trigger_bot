# 🤖 Trigger Admin Bot (Go)

Подробная документация (Wiki): https://github.com/normiridium/trigger_bot/wiki

Telegram-бот с web-админкой триггеров, GPT-интеграцией, музыкальными и медиа-сценариями.

## 🚀 Быстрый старт

### 1) Установить зависимости
```bash
cd /home/faline/trigger_admin_bot
./scripts/install_deps.sh
```

Базовые зависимости:
- `ffmpeg`, `ffprobe`
- `webp` (`img2webp`)
- `yt-dlp`
- `nodejs` + `npm` (нужны `yt-dlp` для части YouTube-ссылок)
- MongoDB

Дополнительно для превью `.tgs` в админке:
- `lottie_to_webp` или `lottie_to_webp.sh` в `PATH`
- https://github.com/ed-asriyan/lottie-converter/releases

### 2) Настроить `.env`
Скопируйте пример и заполните значения:
```bash
cp .env.example .env
```

Минимально обязательные:
- `TELEGRAM_BOT_TOKEN`
- `MONGO_URI`
- `OPENAI_API_KEY` (если используете GPT-функции)

Для Spotify:
- `SPOTIPY_CLIENT_ID`
- `SPOTIPY_CLIENT_SECRET`

### 3) Собрать и запустить
```bash
set -a && source .env && set +a
/usr/local/go/bin/go build -o trigger_admin_bot .
./trigger_admin_bot
```

## 🧩 Что умеет бот

### Триггеры
- Match-типы: `full`, `partial`, `regex`, `starts`, `ends`, `idle`, `new_member`
- Режимы триггера (`trigger_mode`):
  - `all`
  - `only_replies`
  - `only_replies_to_any_bot`
  - `only_replies_to_combot`
  - `only_replies_to_combot_no_media`
  - `never_on_replies`
  - `command_reply`
- Режимы админов (`admin_mode`):
  - `anybody`
  - `admins`
  - `not_admins`
- `chance` (вероятность срабатывания)
- `pass_through` (сквозное выполнение нескольких триггеров)

### Action-типы
- `send`
- `send_file`
- `send_gif`
- `send_sticker`
- `delete`
- `delete_user_portrait`
- `gpt_prompt`
- `gpt_image`
- `search_image`
- `spotify_music_audio`
- `music_audio`
- `yandex_music_audio`
- `media_link_audio`
- `media_tiktok_download`
- `media_x_download`
- `user_limit_low_warning` (системный)

### Web-админка
- URL: `/trigger_bot`
- CRUD триггеров
- CRUD шаблонов
- Reorder
- Импорт/экспорт JSON
- Настройки
- Перезапуск
- Авторизация в админке

## 🎵 Музыка и медиа

### Spotify (`spotify_music_audio`)
- Поиск треков через Spotify API
- Поддержка прямых ссылок `open.spotify.com/track/...` и `spotify:track:...`
- Интерактивный выбор
- Скачивание/отправка аудио

### Универсальный музыкальный сценарий (`music_audio`)
- Показывает выбор сервиса: `Spotify` / `Yandex Music`
- Работает и с текстовым запросом, и с ссылками соответствующих сервисов

### Yandex Music (`yandex_music_audio`)
- Ссылки на треки `music.yandex.ru/.../track/...`
- Встроенный загрузчик (без внешнего CLI)

### Медиа по ссылкам (`media_link_audio`)
- YouTube / Instagram / SoundCloud
- Для YouTube: интерактивный выбор `аудио` / `видео` (если включено)
- Для Instagram: автоопределение фото/видео
- Для SoundCloud: прямой аудиосценарий
- Ограничения по размеру и высоте, авто-транскод видео при превышении Telegram-лимита

### TikTok / X
- `media_tiktok_download`
- `media_x_download`

## ⚙️ Важные переменные окружения

Полный список: `.env.example`

### Основные
- `TELEGRAM_BOT_TOKEN`
- `MONGO_URI`
- `OPENAI_API_KEY`
- `OPENAI_MODEL`

### Админка
- `ADMIN_ENABLED`
- `ADMIN_BIND`
- `ADMIN_TOKEN`

### Ограничение чатов
- `ALLOWED_CHAT_IDS`

### GPT
- `GPT_PROMPT_DEBOUNCE_SEC`
- `USER_DAILY_BOT_MESSAGES_LIMIT`

### Музыка
- `SPOTIPY_CLIENT_ID`
- `SPOTIPY_CLIENT_SECRET`
- `SPOTIFY_AUDIO_INTERACTIVE`
- `SPOTIFY_AUDIO_WORKERS`
- `SPOTIFY_AUDIO_QUEUE`
- `YA_MUSIC_TOKEN`
- `YANDEX_MUSIC_QUALITY`
- `YANDEX_MUSIC_TIMEOUT_SEC`
- `YANDEX_MUSIC_TRIES`
- `YANDEX_MUSIC_RETRY_DELAY_SEC`
- `YANDEX_MUSIC_WORKERS`
- `YANDEX_MUSIC_QUEUE`

### Медиа / yt-dlp
- `MEDIA_DOWNLOAD_INTERACTIVE`
- `MEDIA_DOWNLOAD_WORKERS`
- `MEDIA_DOWNLOAD_QUEUE`
- `MEDIA_DOWNLOAD_MAX_MB`
- `MEDIA_DOWNLOAD_MAX_HEIGHT`
- `MEDIA_VIDEO_TRANSCODE_TIMEOUT_SEC`
- `TELEGRAM_UPLOAD_MAX_MB`
- `YTDLP_BIN`
- `YTDLP_EXTRACTOR_ARGS`
- `YTDLP_COOKIES_FILE`
- `YTDLP_COOKIES_FROM_BROWSER`
- `FIXIE_SOCKS_HOST`

## 🧪 Пример импорта (обезличенный)

Для быстрого старта используйте пример:
- `examples/import_starter_anonymized.json`

Импорт через web-админку:
1. Откройте `/trigger_bot/import`
2. Загрузите файл
3. Проверьте и адаптируйте `match_text`, `response_text`, `action_type`

## 🗂️ Правила удаления исходного сообщения

Флаг триггера:
- `delete_source_message`

Текущее поведение:
- source-сообщение удаляется **только после успешной отправки результата**
- при ошибках скачивания/отправки source остаётся
- при `Отменить` в интерактивных клавиатурах source не удаляется

## 🧰 Команды бота
- `/start`
- `/help`
- `/emojiid`
- `/stickerid`
- `/spsearch <запрос>`
- `/spfind <запрос>`

## 🔎 Диагностика и логи

Полезные флаги:
- `DEBUG_TRIGGER_LOG=true`
- `DEBUG_GPT_LOG=true`
- `CHAT_ERROR_LOG=true`

Проверка тестов:
```bash
/usr/local/go/bin/go test ./...
```

Если сервис работает через systemd:
```bash
sudo journalctl -u trigger-admin-bot.service -f -l
```

## 🔄 Перезапуск systemd
```bash
sudo systemctl restart trigger-admin-bot.service
sudo systemctl status trigger-admin-bot.service --no-pager
```

## 📤 Импорт/экспорт

- Экспорт: `/trigger_bot/export`
- Импорт: `/trigger_bot/import`

Формат импорта — bundle JSON c секциями:
- `triggers`
- `templates`

# Trigger Admin Bot (Go)

Подробная документация (Wiki): https://github.com/normiridium/trigger_bot/wiki

Telegram-бот с web-админкой триггеров, Spotify-аудио и скачиванием медиа по ссылкам (`YouTube`, `Instagram`, `SoundCloud`, `TikTok`, `X`) через `yt-dlp`.

## Что умеет
- Триггеры по `match_type` (`full`, `partial`, `regex`, `starts`, `ends`, `idle`, `new_member`)
- Действия: `send`, `send_sticker`, `delete`, `gpt_prompt`, `gpt_image`, `search_image`, `spotify_music_audio`, `yandex_music_audio`, `media_link_audio`, `media_tiktok_download`, `media_x_download`
- Флаг триггера `pass_through` (`Сквозная реакция`): триггер не останавливает дальнейшую обработку, после основного срабатывания выполняются все подходящие сквозные триггеры
- `gpt_prompt` принимает не только текст, но и изображение из сообщения (или из reply) как вход для модели
- Для `spotify_music_audio`:
  - поиск треков в Spotify
  - поддержка прямой Spotify-ссылки на трек (`open.spotify.com/track/...` и `spotify:track:...`)
  - интерактивный список кнопок выбора
  - скачивание аудио через `yt-dlp` + `ffmpeg`
- Для `media_link_audio`:
  - авто-обработка ссылок `YouTube` / `Instagram` / `SoundCloud`
  - интерактивные кнопки выбора: скачать `аудио` или `видео` (для `YouTube`)
  - для `SoundCloud` интерактив отключён: сразу скачивается аудио
  - для `Instagram` интерактив отключён: бот сам определяет, это фото или видео, и отправляет соответствующий тип
  - в подписи/тайтле добавляется исходная ссылка
  - статистика в формате `длительность | размер`
  - сервисные emoji:
    - YouTube видео: `<tg-emoji emoji-id="5463206079913533096">📹</tg-emoji>`
    - Instagram видео: `<tg-emoji emoji-id="5463238270693416950">📹</tg-emoji>`
    - SoundCloud: `<tg-emoji emoji-id="5359614685664523140">🎉</tg-emoji>`
  - лимит размера скачивания (`MEDIA_DOWNLOAD_MAX_MB`, автоматически ограничивается `TELEGRAM_UPLOAD_MAX_MB`)
  - лимит отправки в Telegram (`TELEGRAM_UPLOAD_MAX_MB`, по умолчанию 50 MB)
  - если видео не влезает в Telegram-лимит: локальный `ffmpeg`-транскод по лестнице `720 -> 480 -> 360`
  - лимит качества источника по высоте (по умолчанию 720)
- Для `media_tiktok_download`:
  - обработка ссылок TikTok
  - автоопределение типа медиа и отправка в Telegram
- Для `media_x_download`:
  - обработка ссылок X/Twitter
  - скачивание и отправка видео
- Для `yandex_music_audio`:
  - обработка только ссылок на трек `music.yandex.ru/.../track/...`
  - скачивание трека через встроенный API-клиент Яндекс.Музыки (без внешнего CLI)
  - отправка в Telegram как аудио
- Web-админка: список, создание, редактирование, reorder, import/export

## Зависимости
```bash
./scripts/install_deps.sh
```

Нужны: `ffmpeg`, `ffprobe`, `webp` (`img2webp`), `yt-dlp`, MongoDB.

Для анимированных превью кастом-эмодзи (`.tgs`) в web-админке дополнительно нужен:
- `lottie_to_webp` (или `lottie_to_webp.sh`) в `PATH`
- источник: `ed-asriyan/lottie-converter` releases
  - https://github.com/ed-asriyan/lottie-converter/releases

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
- `YA_MUSIC_TOKEN=` — OAuth токен Яндекс.Музыки
- `YANDEX_MUSIC_QUALITY=1`
- `YANDEX_MUSIC_TIMEOUT_SEC=20`
- `YANDEX_MUSIC_TRIES=6`
- `YANDEX_MUSIC_RETRY_DELAY_SEC=2`
- `YANDEX_MUSIC_WORKERS=1`
- `YANDEX_MUSIC_QUEUE=4`
- `AUDIO_FORMAT=mp3`
- `AUDIO_QUALITY=320K`
- `MEDIA_DOWNLOAD_MAX_MB=50` (не может быть выше `TELEGRAM_UPLOAD_MAX_MB`)
- `TELEGRAM_UPLOAD_MAX_MB=50` (жёсткий лимит отправки файла ботом в Telegram)
- `MEDIA_DOWNLOAD_MAX_HEIGHT=720`
- `MEDIA_DOWNLOAD_INTERACTIVE=true`
- `MEDIA_DOWNLOAD_WORKERS=1`
- `MEDIA_DOWNLOAD_QUEUE=8`
- `MEDIA_VIDEO_TRANSCODE_TIMEOUT_SEC=300` (таймаут локального пережатия видео)
- `YTDLP_BIN=/usr/local/bin/yt-dlp` (или оставить пустым, если есть в `PATH`)
- `YTDLP_EXTRACTOR_ARGS=youtube:player_client=android,web`
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
- `/stickerid`
- `/spsearch <запрос>`
- `/spfind <запрос>`

## Действие `spotify_music_audio`
В триггере укажи:
- `action_type = spotify_music_audio`
- `response_text` = шаблон поискового запроса (можно с переменными, например `{{message}}`)

Поддерживается вход:
- текстовый запрос (поиск через Spotify API)
- прямая ссылка на трек Spotify (берётся конкретный трек без шага поиска)

Флаги триггера:
- `reply` — выбранный трек отправляется reply на исходное сообщение
- `delete_source_message` — исходное сообщение удаляется сразу после показа списка и после завершения выбора

## Действие `yandex_music_audio`
- `action_type = yandex_music_audio`
- `match_type = regex`
- `match_text` = регулярка под ссылку на трек `music.yandex.ru/.../track/...`
- бот достаёт первую ссылку на трек Яндекс.Музыки из текста/подписи и ставит скачивание в очередь

## Действие `media_link_audio`
- `action_type = media_link_audio`
- `match_type = regex`
- `match_text` = регулярка на URL (YouTube / Instagram / SoundCloud)
- бот сам достаёт ссылку из текста сообщения
- при `MEDIA_DOWNLOAD_INTERACTIVE=true` показывает кнопки `Скачать аудио` / `Скачать видео` (YouTube)
- если выбран `видео`:
  - файл пытается отправиться как есть
  - если превышает Telegram-лимит, пережимается локально (`720 -> 480 -> 360`)
  - если после 360 всё равно больше лимита — отправка отменяется

## Действия `media_tiktok_download` и `media_x_download`
- `media_tiktok_download`: используй regex-триггер под ссылки TikTok
- `media_x_download`: используй regex-триггер под ссылки X/Twitter
- ссылки извлекаются из текста сообщения автоматически
- триггеры под ссылки настраиваются в админке/БД

## Проверка
```bash
cd /home/faline/trigger_admin_bot
/usr/local/go/bin/go test ./...
```

## Логи
Полезно включить подробную диагностику:
- `DEBUG_TRIGGER_LOG=true`

Смотреть live-логи сервиса:
```bash
sudo journalctl -u trigger-admin-bot.service -f -l
```

Ключевые строки для `media_link_audio`:
- `send media pick keyboard ...` — показаны кнопки выбора формата
- `media choice selected ...` — пользователь нажал кнопку
- `media worker=... start ...` — задача пошла в воркер
- `media video downloaded ...` / `media audio downloaded ...` — файл скачан
- `media transcode ...` — этапы пережатия видео
- `media worker=... success ...` — файл отправлен успешно
- `media queue send failed ...` — ошибка (с текстом причины)

## Перезапуск systemd
```bash
sudo systemctl restart trigger-admin-bot.service
sudo systemctl status trigger-admin-bot.service --no-pager
```

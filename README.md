# Trigger Admin Bot (Go)

Отдельный Telegram-бот с web-админкой триггеров.

## Что умеет

- Отвечает в чате по триггерам (full match / contains)
- HTML в ответах (ссылки `<a href=...>`)
- Опции: `reply`, `preview`, `chance`, `case-sensitive`, `enabled`
- Web-админка:
  - список, создание, редактирование
  - включить/выключить
  - удалить
  - импорт/экспорт JSON (триггеры + шаблоны в одном файле)
  - вкладка шаблонов ответов (MongoDB)
- Тип действия `search_image`: найти картинку через SerpAPI (Google Images) и отправить в чат
- Ограничение по чатам через whitelist (`ALLOWED_CHAT_IDS`)
- Учёт админов чата с автопрогревом:
  - при первом сообщении из чата бот прогревает полный список админов
  - хранит кэш в БД и в памяти (TTL через `ADMIN_CACHE_TTL_SEC`)
  - ручное обновление: команда `!reload_admins`
- HTML админки вынесен в шаблон `templates/trigger_list.html`

## Формат импорта/экспорта

JSON-файл со связкой триггеров и шаблонов:

```json
{
  "triggers": [
    {
      "uid": "some-uid",
      "priority": 100,
      "regex_bench_us": 0,
      "title": "Список СДВГ",
      "enabled": true,
      "trigger_mode": "all",
      "admin_mode": "anybody",
      "match_text": "СДВГ",
      "match_type": "partial",
      "case_sensitive": false,
      "action_type": "send",
      "response_text": [{"text": "..." }],
      "send_as_reply": true,
      "preview_first_link": false,
      "delete_source_message": false,
      "chance": 100
    }
  ],
  "templates": [
    {"key": "olenyam_base", "title": "Оле-ням: база", "text": "..."}
  ]
}
```

## System dependencies

Debian/Ubuntu:

```bash
./scripts/install_deps.sh
```

To also install MongoDB on Debian bullseye:

```bash
INSTALL_MONGODB=1 ./scripts/install_deps.sh
```

## Запуск

```bash
cd /home/faline/trigger_admin_bot
cp .env.example .env
# заполни TELEGRAM_BOT_TOKEN
set -a; source .env; set +a

/usr/local/go/bin/go mod tidy
/usr/local/go/bin/go build -o trigger_admin_bot .
./trigger_admin_bot
```

Админка по умолчанию: `http://<host>:8090/trigger_bot`

Если задан `ADMIN_TOKEN`, передавай его в URL: `?token=...`

## ENV-переменные

- `ALLOWED_CHAT_IDS` — список разрешённых `chat_id` через запятую/пробел.
  Если пусто, бот работает во всех чатах.
  Пример: `ALLOWED_CHAT_IDS=-1001234567890,-1009876543210`
- `ADMIN_CACHE_TTL_SEC` — TTL кэша админов чата (по умолчанию `120` сек)
  - при первом сообщении в чате бот автопрогревает список админов
  - повторный прогрев — после истечения TTL или по `!reload_admins`
- `USER_INDEX_MAX` — максимум пользователей в индексе чата (по умолчанию `800`)
- `CHAT_RECENT_MAX_MESSAGES` — сколько последних сообщений хранить для контекста (по умолчанию `8`)
- `CHAT_RECENT_MAX_AGE_SEC` — TTL контекста сообщений (по умолчанию `1800` сек)
- `OLENYAM_CONTEXT_MESSAGES` — сколько последних сообщений передавать в GPT-контекст (по умолчанию `4`)
- `DEBUG_TRIGGER_LOG` и `DEBUG_GPT_LOG` — подробные debug-логи (по умолчанию `false`)
- `CHAT_ERROR_LOG` — отправка ошибок в чат (`true/false`)
- `WEB_TEMPLATE_DIR` — каталог HTML-шаблонов админки (по умолчанию `./templates`)
- `WEB_STATIC_DIR` — каталог статики админки (по умолчанию `./static`)
- `SERPAPI_KEY` — обязательный ключ для `search_image`
- `SERPAPI_ENGINE` — движок SerpAPI (по умолчанию `google_images`)
- `GPT_PROMPT_DEBOUNCE_SEC` — debounce для `gpt_prompt` (если `>0`, бот отвечает только на последнее сообщение в окне времени, per chat)
- `match_type=idle` — специальный тип условия: в поле `match_text` указывается время простоя в минутах, после которого бот автоответит на первое следующее сообщение (для `gpt_prompt`)
- `VK_AUDIO_INTERACTIVE` — интерактивный режим выбора трека (по умолчанию `true`)
- `VK_AUDIO_MAX_MB` — лимит размера аудио (по умолчанию `60`)
- `VK_AUDIO_FFMPEG_TIMEOUT_SEC` — таймаут ffmpeg (по умолчанию `120`)
- `VK_AUDIO_RETRY_COUNT` — число ретраев загрузки аудио (по умолчанию `3`)
- `VK_AUDIO_DL_THREADS` — количество потоков скачивания (по умолчанию `1`)
- `VK_AUDIO_WORKERS` — количество воркеров очереди аудио (по умолчанию `1`)
- `VK_AUDIO_QUEUE` — размер очереди аудио (по умолчанию `8`)

## UI-правки без ребилда

Страница админки рендерится из файла `templates/trigger_list.html`.
Изменения шаблона применяются сразу на следующий HTTP-запрос к `/trigger_bot`
(достаточно обновить страницу в браузере, без `go build`).

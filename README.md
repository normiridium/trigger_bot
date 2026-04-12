# Trigger Admin Bot (Go)

Отдельный Telegram-бот с web-админкой триггеров.

## ✨ Что умеет

- Отвечает в чате по триггерам (full match / contains)
- HTML в ответах (ссылки `<a href=...>`)
- Опции: `reply`, `preview`, `chance`, `case-sensitive`, `enabled`
- Web-админка:
  - список, создание, редактирование
  - включить/выключить
  - удалить
  - импорт/экспорт JSON (триггеры + шаблоны в одном файле)
  - вкладка шаблонов ответов (MongoDB)
  - вставка шаблонов в ответы через `{{template "key"}}`
- Тип действия `search_image`: найти картинку через SerpAPI (Google Images) и отправить в чат
- Ограничение по чатам через whitelist (`ALLOWED_CHAT_IDS`)
- Учёт админов чата с автопрогревом:
  - при первом сообщении из чата бот прогревает полный список админов
  - хранит кэш в БД и в памяти (TTL через `ADMIN_CACHE_TTL_SEC`)
  - ручное обновление: команда `!reload_admins`
- HTML админки вынесен в шаблон `templates/trigger_list.html`

## 📦 Формат импорта/экспорта

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

Важно: старые форматы импорта не поддерживаются. Импорт — только этот bundle.

## 🧰 System dependencies

Debian/Ubuntu:

```bash
./scripts/install_deps.sh
```

To also install MongoDB on Debian bullseye:

```bash
INSTALL_MONGODB=1 ./scripts/install_deps.sh
```

## 🔑 Получение токенов

Telegram Bot Token

- Напишите `@BotFather`
- Создайте нового бота командой `/newbot`
- Скопируйте полученный токен в `.env`

VK Admin Token

- Перейдите на `vkhost.github.io`
- Выберите `VK Admin`
- Авторизуйтесь
- Скопируйте токен в `.env`

## 📁 Структура проекта

```
.
├── internal/                 # бизнес-логика
│   ├── engine/               # подбор триггеров
│   ├── gpt/                  # debounce/очереди GPT
│   ├── match/                # матчинг условий
│   ├── model/                # типы и перечисления
│   ├── trigger/              # idle/служебные триггеры
│   └── vk/                   # VK-интеграции
├── static/                   # CSS/JS админки
├── templates/                # HTML-шаблоны админки
├── scripts/                  # скрипты установки зависимостей
├── main.go                   # входная точка
├── web.go                    # web-админка и API
├── store.go                  # логика хранилища (Mongo)
├── store_mongo.go            # Mongo backend
├── types_alias.go            # алиасы типов
├── main_test.go              # тесты основного пакета
├── store_test.go             # тесты хранилища
├── store_cache_test.go       # тесты кэша
├── go.mod / go.sum           # зависимости Go
├── trigger-admin-bot.service # systemd unit
└── trigger_admin_bot         # собранный бинарник
```

## 🚀 Запуск

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

## 🗂️ Вкладки админки

- `Триггеры` — список, редактирование и управление триггерами.
- `Шаблоны` — список шаблонов (MongoDB). Шаблон вставляется в тексты ответов как `{{template "key"}}`.
- `Настройки` — импорт/экспорт JSON (один файл, включает триггеры и шаблоны).

## 🧪 HTTP API (JSON-only)

Все POST‑эндпоинты принимают **только JSON**.

- `POST /trigger_bot/save` — сохранить триггер.
- `POST /trigger_bot/delete` — удалить триггер.
- `POST /trigger_bot/toggle` — переключить триггер.
- `POST /trigger_bot/reorder` — сортировка (список `ids`).
- `POST /trigger_bot/template_save` — сохранить шаблон.
- `POST /trigger_bot/template_delete` — удалить шаблон (вернёт `409`, если используется в триггере).
- `POST /trigger_bot/import` — импорт bundle (JSON: `{"raw":"<string>"}`).

Экспорт:
- `GET /trigger_bot/export` — скачать bundle `trigger_bot_export.json`.

## 🔁 Перезапуск сервиса из админки

Кнопка «Перезапуск сервиса» использует `sudo systemctl restart trigger-admin-bot.service`.
Нужно разрешить это действие без пароля для пользователя сервиса:

```bash
echo 'faline ALL=(root) NOPASSWD: /usr/bin/systemctl restart trigger-admin-bot.service' | sudo tee /etc/sudoers.d/trigger-admin-bot
sudo chmod 440 /etc/sudoers.d/trigger-admin-bot
sudo visudo -cf /etc/sudoers.d/trigger-admin-bot
```

## 🧩 ENV-переменные

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

## 🖼️ UI-правки без ребилда

Страница админки рендерится из файла `templates/trigger_list.html`.
Изменения шаблона применяются сразу на следующий HTTP-запрос к `/trigger_bot`
(достаточно обновить страницу в браузере, без `go build`).

## 🛡️ Админ-команды в чате

- `!ban` / `!sban`
- `!unban` / `!sunban`
- `!mute` / `!smute`
- `!unmute`
- `!kick` / `!skick`
- `!readonly` / `!ro` / `!channelmode`
- `!reload_admins`

Описание и примеры:

- `!ban` — бан пользователя(ей). Можно ответом на сообщение или через `@username`/ID. Для нескольких — перечислять через пробел. Тихий вариант: `!sban`. Причину можно писать на следующей строке.
  - Пример: `!ban @user 2w`
- `!unban` — разбан. Можно ответом или через `@username`/ID. Тихий вариант: `!sunban`. Причину можно писать на следующей строке.
  - Пример: `!unban 12345`
- `!mute` — мут пользователя(ей). Можно ответом или через `@username`/ID. Тихий вариант: `!smute`. Причину можно писать на следующей строке.
  - Пример: `!mute @user 10h`
- `!unmute` — снять мут. Можно ответом или через `@username`/ID. Снимает ограничения согласно правам группы. Причину можно писать на следующей строке.
  - Пример: `!unmute @user`
- `!kick` — кик пользователя(ей). Можно ответом или через `@username`/ID. Тихий вариант: `!skick`. Причину можно писать на следующей строке.
  - Пример: `!kick @user`
- `!readonly` / `!ro` / `!channelmode` — режим “только чтение”: писать могут только админы. Повторная команда отключает режим. Можно указать длительность.
  - Пример: `!readonly 30m`
- `!reload_admins` — обновить список админов.

Формат длительности: `30d`, `2w`, `10h`, `2m`.

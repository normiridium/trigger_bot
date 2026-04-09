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
  - импорт/экспорт JSON
- Тип действия `search_image`: найти картинку через SerpAPI (Google Images) и отправить в чат
- Ограничение по чатам через whitelist (`ALLOWED_CHAT_IDS`)
- Учёт админов чата с автопрогревом:
  - при первом сообщении из чата бот прогревает полный список админов
  - хранит кэш в БД и в памяти (TTL через `ADMIN_CACHE_TTL_SEC`)
  - ручное обновление: команда `!reload_admins`
- HTML админки вынесен в шаблон `templates/trigger_list.html`

## Формат импорта/экспорта

Поддержан формат элементов вида:

```json
{"t":"Список СДВГ","cos":[{"mty":"0","tt":"Список СДВГ","ty":"0"}],"acs":[{"ty":"se","t":"...","sr":"1"}]}
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

Админка по умолчанию: `http://<host>:8090/admin/triggers`

Если задан `ADMIN_TOKEN`, передавай его в URL: `?token=...`

## ENV-переменные

- `ALLOWED_CHAT_IDS` — список разрешённых `chat_id` через запятую/пробел.
  Если пусто, бот работает во всех чатах.
  Пример: `ALLOWED_CHAT_IDS=-1001234567890,-1009876543210`
- `ADMIN_CACHE_TTL_SEC` — TTL кэша админов чата (по умолчанию `120` сек)
  - при первом сообщении в чате бот автопрогревает список админов
  - повторный прогрев — после истечения TTL или по `!reload_admins`
- `DEBUG_TRIGGER_LOG` и `DEBUG_GPT_LOG` — подробные debug-логи (по умолчанию `false`)
- `CHAT_ERROR_LOG` — отправка ошибок в чат (`true/false`)
- `WEB_TEMPLATE_DIR` — каталог HTML-шаблонов админки (по умолчанию `./templates`)
- `SERPAPI_KEY` — обязательный ключ для `search_image`
- `SERPAPI_ENGINE` — движок SerpAPI (по умолчанию `google_images`)
- `GPT_PROMPT_DEBOUNCE_SEC` — debounce для `gpt_prompt` (если `>0`, бот отвечает только на последнее сообщение в окне времени, per chat)
- `match_type=idle` — специальный тип условия: в поле `match_text` указывается время простоя в минутах, после которого бот автоответит на первое следующее сообщение (для `gpt_prompt`)

## UI-правки без ребилда

Страница админки рендерится из файла `templates/trigger_list.html`.
Изменения шаблона применяются сразу на следующий HTTP-запрос к `/trigger_bot`
(достаточно обновить страницу в браузере, без `go build`).

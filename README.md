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
- Кэш проверки админ-статуса (меньше запросов к Telegram API)
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
- `ADMIN_CACHE_TTL_SEC` — TTL кэша проверки админов (по умолчанию `120` сек)
- `DEBUG_TRIGGER_LOG` и `DEBUG_GPT_LOG` — подробные debug-логи (по умолчанию `false`)
- `CHAT_ERROR_LOG` — отправка ошибок в чат (`true/false`)
- `WEB_TEMPLATE_DIR` — каталог HTML-шаблонов админки (по умолчанию `./templates`)
- `SERPAPI_KEY` — обязательный ключ для `search_image`
- `SERPAPI_ENGINE` — движок SerpAPI (по умолчанию `google_images`)
- `GPT_PROMPT_DEBOUNCE_SEC` — debounce для `gpt_prompt` (если `>0`, бот отвечает только на последнее сообщение в окне времени, per chat)

## UI-правки без ребилда

Страница админки рендерится из файла `templates/trigger_list.html`.
Изменения шаблона применяются сразу на следующий HTTP-запрос к `/trigger_bot`
(достаточно обновить страницу в браузере, без `go build`).

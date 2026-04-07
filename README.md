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

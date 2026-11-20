package app

import (
	"encoding/json"
	"fmt"
	"strings"
)

// decodeResponseTextPayload собирает варианты ответа из того, что прислала
// админка: массив строк (текущий формат), массив объектов {"text": …} (старые
// клиенты) или одиночная строка. Структуру []ResponseTextItem — то есть JSON,
// который уходит в хранилище, — собирает сервер, а не браузер.
func decodeResponseTextPayload(raw json.RawMessage) ([]ResponseTextItem, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		var single string
		if errSingle := json.Unmarshal(raw, &single); errSingle == nil {
			return []ResponseTextItem{{Text: single}}, nil
		}
		return nil, fmt.Errorf("response_text: ожидался массив вариантов")
	}
	items := make([]ResponseTextItem, 0, len(entries))
	for idx, entry := range entries {
		var text string
		if err := json.Unmarshal(entry, &text); err == nil {
			items = append(items, ResponseTextItem{Text: text})
			continue
		}
		var obj struct {
			Text      string `json:"text"`
			TextUpper string `json:"Text"`
		}
		if err := json.Unmarshal(entry, &obj); err == nil {
			value := obj.Text
			if strings.TrimSpace(value) == "" {
				value = obj.TextUpper
			}
			items = append(items, ResponseTextItem{Text: value})
			continue
		}
		return nil, fmt.Errorf("response_text[%d]: ожидалась строка или объект с text", idx)
	}
	return items, nil
}

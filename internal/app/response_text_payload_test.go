package app

import (
	"encoding/json"
	"testing"
)

func TestDecodeResponseTextPayload(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{name: "массив строк — текущий формат админки", raw: `["раз","два"]`, want: []string{"раз", "два"}},
		{name: "массив объектов — старый клиент", raw: `[{"text":"раз"},{"Text":"два"}]`, want: []string{"раз", "два"}},
		{name: "одиночная строка", raw: `"один"`, want: []string{"один"}},
		{name: "пустой массив", raw: `[]`, want: nil},
		{name: "null", raw: `null`, want: nil},
		{name: "пустое тело", raw: ``, want: nil},
		{name: "смешанный массив", raw: `["a",{"text":"b"}]`, want: []string{"a", "b"}},
		{name: "мусор в массиве", raw: `["a",42]`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, err := decodeResponseTextPayload(json.RawMessage(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ожидалась ошибка, получено %#v", items)
				}
				return
			}
			if err != nil {
				t.Fatalf("неожиданная ошибка: %v", err)
			}
			got := make([]string, 0, len(items))
			for _, it := range items {
				got = append(got, it.Text)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("получено %#v, ожидалось %#v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("получено %#v, ожидалось %#v", got, tc.want)
				}
			}
		})
	}
}

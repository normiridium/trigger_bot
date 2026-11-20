package mediadl

import "strings"

// mediaTitleTrackingMarkers — куски query-строки, которые попадают в заголовок,
// когда название тянется из описания поста (Instagram/Pinterest/Reddit):
// «The Dachshund … “sausage dog,”rce=ig_web_copy_link&igsh=MzRlODBiNWFlZA==».
var mediaTitleTrackingMarkers = []string{
	"utm_source=", "utm_medium=", "utm_campaign=", "utm_content=", "utm_term=",
	"igsh=", "ig_web_copy_link", "ig_mid=",
	"fbclid=", "gclid=", "yclid=",
	"si=", "share_id=", "share_url=", "feature=share",
	"ref_src=", "ref_url=",
}

// sanitizeMediaTitle чистит заголовок от приклеенного хвоста ссылки: обрезает
// последний токен без пробелов, если в нём есть query-параметр из известных
// трекеров, и добирает назад «остаток» параметра (rce= от обрезанного
// utm_source=). Если от токена остаётся огрызок URL — токен выбрасывается.
func sanitizeMediaTitle(raw string) string {
	title := strings.TrimSpace(raw)
	if title == "" {
		return title
	}
	for {
		fields := strings.Split(title, " ")
		if len(fields) == 0 {
			return title
		}
		last := fields[len(fields)-1]
		cutAt := trackingMarkerIndex(last)
		if cutAt < 0 {
			return strings.TrimSpace(title)
		}
		// Отступаем назад по остатку имени параметра: «rce=» перед ig_web_copy_link.
		start := cutAt
		for start > 0 && (isParamRune(last[start-1]) || last[start-1] == '=') {
			start--
		}
		head := strings.TrimRight(last[:start], " \t&?=#/")
		if strings.Contains(head, "://") {
			head = "" // это была ссылка целиком — её место в подписи источника
		}
		head = strings.TrimRight(head, " \t,;:—-")
		fields = fields[:len(fields)-1]
		if head != "" {
			fields = append(fields, head)
		}
		title = strings.TrimSpace(strings.Join(fields, " "))
		if title == "" {
			return ""
		}
	}
}

// trackingMarkerIndex возвращает позицию самого раннего известного трекера
// в токене или -1, если трекеров нет.
func trackingMarkerIndex(token string) int {
	lower := strings.ToLower(token)
	best := -1
	for _, marker := range mediaTitleTrackingMarkers {
		if idx := strings.Index(lower, marker); idx >= 0 && (best == -1 || idx < best) {
			best = idx
		}
	}
	if best < 0 {
		return -1
	}
	// Без «=» или «&» рядом это скорее обычный текст, чем query-строка.
	if !strings.Contains(token[best:], "=") && !strings.Contains(token[best:], "&") {
		return -1
	}
	return best
}

func isParamRune(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '_' || c == '-' || c == '%':
		return true
	}
	return false
}

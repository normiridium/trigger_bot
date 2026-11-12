package app

// Cron-подобные расписания участников.
//
// Участник просит Оле-ням «напоминай мне по утрам пить воду», модель в ответе
// возвращает директиву в обратных кавычках:
//
//	`53 10 * * *|тегать @Anny_Wlne и желать хорошего настроения`
//
// Слева — cron-выражение (стандартные 5 полей), справа — что именно надо
// сделать. Разбор cron-выражения отдан библиотеке github.com/robfig/cron/v3.

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// scheduleDirectiveRe matches `cron|action` blocks in the model answer.
var scheduleDirectiveRe = regexp.MustCompile("`([^`\n]{3,120})\\|([^`\n]{1,400})`")

// ScheduleDirective is one parsed request to schedule an action.
type ScheduleDirective struct {
	Cron   string
	Action string
}

// cronSpec wraps a parsed schedule from robfig/cron.
type cronSpec struct {
	sched cron.Schedule
	expr  string
}

// parseCronSpec parses a standard 5-field expression. A leading seconds field
// (6 fields, which some models emit) is dropped: we schedule per minute.
func parseCronSpec(expr string) (*cronSpec, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) == 6 {
		fields = fields[1:]
	}
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron: ожидается 5 полей (минута час день месяц день_недели), получено %d", len(fields))
	}
	normalized := strings.Join(fields, " ")
	sched, err := cron.ParseStandard(normalized)
	if err != nil {
		return nil, fmt.Errorf("cron %q: %w", normalized, err)
	}
	return &cronSpec{sched: sched, expr: normalized}, nil
}

// Matches reports whether the schedule fires in the minute containing t.
func (c *cronSpec) Matches(t time.Time) bool {
	if c == nil || c.sched == nil {
		return false
	}
	minute := t.Truncate(time.Minute)
	return c.sched.Next(minute.Add(-time.Minute)).Equal(minute)
}

// ExtractScheduleDirectives pulls `cron|action` blocks out of the model answer.
// The returned text has valid directives removed; malformed blocks are left in
// place so nothing is silently swallowed.
func ExtractScheduleDirectives(answer string) (string, []ScheduleDirective) {
	if !strings.Contains(answer, "`") || !strings.Contains(answer, "|") {
		return answer, nil
	}
	var directives []ScheduleDirective
	cleaned := scheduleDirectiveRe.ReplaceAllStringFunc(answer, func(match string) string {
		sub := scheduleDirectiveRe.FindStringSubmatch(match)
		if len(sub) != 3 {
			return match
		}
		cronExpr := strings.TrimSpace(sub[1])
		action := strings.TrimSpace(sub[2])
		if action == "" {
			return match
		}
		if _, err := parseCronSpec(cronExpr); err != nil {
			return match
		}
		directives = append(directives, ScheduleDirective{Cron: cronExpr, Action: action})
		return ""
	})
	if len(directives) == 0 {
		return answer, nil
	}
	// Remove the leftover blank lines from the stripped blocks, but keep the
	// paragraph structure of the rest of the answer intact.
	cleaned = regexp.MustCompile(`[ \t]+\n`).ReplaceAllString(cleaned, "\n")
	cleaned = regexp.MustCompile(`\n{3,}`).ReplaceAllString(cleaned, "\n\n")
	cleaned = strings.TrimSpace(cleaned)
	return cleaned, directives
}

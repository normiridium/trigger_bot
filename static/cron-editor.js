/*
 * <cron-editor> — визуальный редактор cron-выражения.
 *
 * Источник: https://github.com/normiridium/cron-calendar (src/cron-editor.js).
 * Правки делать в отдельном репозитории и копировать сюда — иначе версии разойдутся.
 *
 * Один файл, без зависимостей: ни Bootstrap, ни jQuery, ни сборки.
 * Разметка живёт в shadow DOM (ничего не протекает наружу), оформление
 * настраивается CSS-переменными на самом теге:
 *
 *   --cron-border   цвет рамки контейнера
 *   --cron-accent   цвет выбранной ячейки
 *   --cron-label    цвет подписей полей
 *   --cron-mono     шрифт подписей и выражений
 *   --cron-cell-bg / --cron-cell-border / --cron-cell-color — ячейки
 *
 * API:
 *   <cron-editor value="[шаг5] 19-20,22-23 11,14,29 1,4,6 1-2"></cron-editor>   (вместо [шаг5] — звёздочка и /5)
 *   el.value            → строка выражения (5 сегментов через пробел)
 *   el.value = '0 9 * * *'
 *   el.values           → {min:[0,5,…], hour:[…], dom:[…], month:[…], dow:[…]}
 *   el.setValues({...}) → то же, что value, но наборами чисел
 *
 * Флаги компонента (без всякого UI):
 *   enumeration           — «при равной длине показывать перечисление».
 *                           Включён по умолчанию; `enumeration="off"` (или
 *                           `el.preferEnumeration = false`) — при равенстве
 *                           побеждает шаг: 2-10/4,4 вместо 2,4,6,10.
 *   mode                  — `always` (сетка видна) | `popover` (панель по фокусу).
 *   fields                — подмножество полей, напр. fields="min,hour".
 *   событие 'change'    → detail: {value, values}
 *
 * Поведение: клик по ячейке выбирает значение (закрашено), пусто = «*»,
 * подряд идущие значения схлопываются в диапазон (1-5), арифметическая
 * прогрессия — в шаг (звёздочка/5 или 0-10/5), остаток дописывается через запятую
 * (звёздочка/5,58). Рядом с каждой ячейкой-сеткой есть поле, куда выражение поля
 * можно вписать руками; по выходу из поля оно нормализуется.
 *
 * Чистые функции разбора/формата доступны как статические методы:
 *   CronEditor.parseToken(token, {min, max})
 *   CronEditor.formatToken(values, {min, max}, {preferEnumeration: false})
 */
(function () {
  'use strict';

  // Названия месяцев и дней недели берём из нативного Intl.DateTimeFormat —
  // он знает правильные формы и склонения для любого языка. Свои таблицы
  // держим только как fallback для сред без Intl и как компактный вариант
  // (русский Intl отдаёт «янв.» с точкой, нам нужна точка без «янв»).
  const INTL_LOCALES = {
    ru: {
      monthsShort: ['янв', 'фев', 'мар', 'апр', 'май', 'июн', 'июл', 'авг', 'сен', 'окт', 'ноя', 'дек'],
      monthsLong: ['январь', 'февраль', 'март', 'апрель', 'май', 'июнь', 'июль', 'август', 'сентябрь', 'октябрь', 'ноябрь', 'декабрь'],
      dowShort: ['вс', 'пн', 'вт', 'ср', 'чт', 'пт', 'сб'],
      dowLong: ['воскресенье', 'понедельник', 'вторник', 'среда', 'четверг', 'пятница', 'суббота']
    },
    en: {
      monthsShort: ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'],
      monthsLong: ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'],
      dowShort: ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'],
      dowLong: ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
    }
  };

  // Подписи полей и тексты подсказок: у Intl такого API нет, поэтому маленький
  // словарь. Незнакомый язык получает английские фразы, а месяцы/дни — всё равно
  // свои, из Intl.
  // Языковой пак: подписи полей, фразы подсказок и заголовок списка запусков.
  // Названия месяцев и дней сюда НЕ входят — их отдаёт Intl. Добавить язык =
  // добавить запись сюда, логику трогать не нужно.
  const MESSAGES = {
    ru: {
      fields: { min: 'минута', hour: 'час', dom: 'день', month: 'месяц', dow: 'день недели' },
      expressionAria: 'выражение',
      cronAria: 'cron-выражение',
      nextRunsTitle: 'Следующие запуски:',
      noRuns: 'В ближайшие годы запусков не будет',
      dayDead: function (day) {
        return day + '-го числа не бывает ни в одном из выбранных месяцев — этот день не сработает никогда';
      },
      dayPartial: function (day, months) {
        return day + '-го числа не бывает в: ' + months + ' — в этих месяцах день пропустится';
      },
      monthDead: function (name, list) {
        return 'в месяце «' + name + '» не бывает ' + list + ' числа — расписание не сработает никогда';
      },
      monthPartial: function (name, list) {
        return 'в месяце «' + name + '» не бывает ' + list + ' числа — эти дни пропустятся';
      }
    },
    en: {
      fields: { min: 'Minute', hour: 'Hour', dom: 'Day', month: 'Month', dow: 'Day of week' },
      expressionAria: 'expression',
      cronAria: 'cron expression',
      nextRunsTitle: 'Next runs:',
      noRuns: 'No runs in the coming years',
      dayDead: function (day) {
        return englishOrdinal(day) + ' never exists in the selected months — this day will never run';
      },
      dayPartial: function (day, months) {
        return englishOrdinal(day) + ' does not exist in ' + months + ' — skipped in those months';
      },
      monthDead: function (name, list) {
        return name + ' has no ' + list + ' — the schedule will never run';
      },
      monthPartial: function (name, list) {
        return name + ' has no ' + list + ' — those days will be skipped';
      }
    },
    de: {
      fields: { min: 'Minute', hour: 'Stunde', dom: 'Tag', month: 'Monat', dow: 'Wochentag' },
      expressionAria: 'Ausdruck',
      cronAria: 'Cron-Ausdruck',
      nextRunsTitle: 'Nächste Ausführungen:',
      noRuns: 'Keine Ausführungen in den nächsten Jahren',
      dayDead: function (day) {
        return 'Den ' + day + '. gibt es in keinem der gewählten Monate — dieser Tag läuft nie';
      },
      dayPartial: function (day, months) {
        return 'Den ' + day + '. gibt es nicht im ' + months + ' — dort wird der Tag übersprungen';
      },
      monthDead: function (name, list) {
        return 'Im Monat ' + name + ' gibt es keinen ' + list + '. — der Zeitplan läuft nie';
      },
      monthPartial: function (name, list) {
        return 'Im Monat ' + name + ' gibt es keinen ' + list + '. — diese Tage werden übersprungen';
      }
    },
    fr: {
      fields: { min: 'minute', hour: 'heure', dom: 'jour', month: 'mois', dow: 'jour de la semaine' },
      expressionAria: 'expression',
      cronAria: 'expression cron',
      nextRunsTitle: 'Prochaines exécutions :',
      noRuns: 'Aucune exécution dans les prochaines années',
      dayDead: function (day) {
        return 'Le ' + day + ' n’existe dans aucun des mois sélectionnés — ce jour ne s’exécutera jamais';
      },
      dayPartial: function (day, months) {
        return 'Le ' + day + ' n’existe pas en ' + months + ' — ignoré ces mois-là';
      },
      monthDead: function (name, list) {
        return name + ' n’a pas de ' + list + ' — la planification ne s’exécutera jamais';
      },
      monthPartial: function (name, list) {
        return name + ' n’a pas de ' + list + ' — ces jours seront ignorés';
      }
    },
    es: {
      fields: { min: 'minuto', hour: 'hora', dom: 'día', month: 'mes', dow: 'día de la semana' },
      expressionAria: 'expresión',
      cronAria: 'expresión cron',
      nextRunsTitle: 'Próximas ejecuciones:',
      noRuns: 'No hay ejecuciones en los próximos años',
      dayDead: function (day) {
        return 'El día ' + day + ' no existe en ninguno de los meses seleccionados — nunca se ejecutará';
      },
      dayPartial: function (day, months) {
        return 'El día ' + day + ' no existe en ' + months + ' — se omitirá en esos meses';
      },
      monthDead: function (name, list) {
        return name + ' no tiene día ' + list + ' — la programación nunca se ejecutará';
      },
      monthPartial: function (name, list) {
        return name + ' no tiene día ' + list + ' — esos días se omitirán';
      }
    },
    it: {
      fields: { min: 'minuto', hour: 'ora', dom: 'giorno', month: 'mese', dow: 'giorno della settimana' },
      expressionAria: 'espressione',
      cronAria: 'espressione cron',
      nextRunsTitle: 'Prossime esecuzioni:',
      noRuns: 'Nessuna esecuzione nei prossimi anni',
      dayDead: function (day) {
        return 'Il giorno ' + day + ' non esiste in nessuno dei mesi selezionati — non verrà mai eseguito';
      },
      dayPartial: function (day, months) {
        return 'Il giorno ' + day + ' non esiste in ' + months + ' — verrà saltato in quei mesi';
      },
      monthDead: function (name, list) {
        return name + ' non ha il giorno ' + list + ' — la pianificazione non verrà mai eseguita';
      },
      monthPartial: function (name, list) {
        return name + ' non ha il giorno ' + list + ' — quei giorni verranno saltati';
      }
    },
    pt: {
      fields: { min: 'minuto', hour: 'hora', dom: 'dia', month: 'mês', dow: 'dia da semana' },
      expressionAria: 'expressão',
      cronAria: 'expressão cron',
      nextRunsTitle: 'Próximas execuções:',
      noRuns: 'Nenhuma execução nos próximos anos',
      dayDead: function (day) {
        return 'O dia ' + day + ' não existe em nenhum dos meses selecionados — nunca será executado';
      },
      dayPartial: function (day, months) {
        return 'O dia ' + day + ' não existe em ' + months + ' — será ignorado nesses meses';
      },
      monthDead: function (name, list) {
        return name + ' não tem dia ' + list + ' — o agendamento nunca será executado';
      },
      monthPartial: function (name, list) {
        return name + ' não tem dia ' + list + ' — esses dias serão ignorados';
      }
    },
    pl: {
      fields: { min: 'minuta', hour: 'godzina', dom: 'dzień', month: 'miesiąc', dow: 'dzień tygodnia' },
      expressionAria: 'wyrażenie',
      cronAria: 'wyrażenie cron',
      nextRunsTitle: 'Następne uruchomienia:',
      noRuns: 'Brak uruchomień w najbliższych latach',
      dayDead: function (day) {
        return 'Dzień ' + day + '. nie istnieje w żadnym z wybranych miesięcy — nigdy się nie uruchomi';
      },
      dayPartial: function (day, months) {
        return 'Dzień ' + day + '. nie istnieje w ' + months + ' — w tych miesiącach zostanie pominięty';
      },
      monthDead: function (name, list) {
        return 'W miesiącu ' + name + ' nie ma dnia ' + list + ' — harmonogram nigdy się nie uruchomi';
      },
      monthPartial: function (name, list) {
        return 'W miesiącu ' + name + ' nie ma dnia ' + list + ' — te dni zostaną pominięte';
      }
    },
    tr: {
      fields: { min: 'dakika', hour: 'saat', dom: 'gün', month: 'ay', dow: 'haftanın günü' },
      expressionAria: 'ifade',
      cronAria: 'cron ifadesi',
      nextRunsTitle: 'Sonraki çalıştırmalar:',
      noRuns: 'Önümüzdeki yıllarda çalıştırma yok',
      dayDead: function (day) {
        return day + '. gün seçilen ayların hiçbirinde yok — bu gün hiç çalışmayacak';
      },
      dayPartial: function (day, months) {
        return day + '. gün ' + months + ' aylarında yok — o aylarda atlanacak';
      },
      monthDead: function (name, list) {
        return name + ' ayında ' + list + '. gün yok — zamanlama hiç çalışmayacak';
      },
      monthPartial: function (name, list) {
        return name + ' ayında ' + list + '. gün yok — o günler atlanacak';
      }
    },
    zh: {
      fields: { min: '分钟', hour: '小时', dom: '日', month: '月', dow: '星期' },
      expressionAria: '表达式',
      cronAria: 'cron 表达式',
      nextRunsTitle: '接下来运行：',
      noRuns: '未来几年内不会运行',
      dayDead: function (day) {
        return '所选月份中没有 ' + day + ' 日 — 这一天永远不会执行';
      },
      dayPartial: function (day, months) {
        return months + ' 中没有 ' + day + ' 日 — 这些月份会跳过';
      },
      monthDead: function (name, list) {
        return name + '没有 ' + list + ' 日 — 计划永远不会执行';
      },
      monthPartial: function (name, list) {
        return name + '没有 ' + list + ' 日 — 这些日期会被跳过';
      }
    }
  };

  const MESSAGE_LANGS = ['ru', 'en', 'de', 'fr', 'es', 'it', 'pt', 'pl', 'tr', 'zh'];

  // shortenNames подбирает минимальную длину префикса, при которой все названия
  // различимы: сначала 3 знака, при совпадениях — 4, потом 5.
  function shortenNames(names) {
    for (let length = 3; length <= 5; length += 1) {
      const cut = names.map(function (name) { return Array.from(name).slice(0, length).join(''); });
      const unique = new Set(cut);
      if (unique.size === cut.length) {
        return cut;
      }
    }
    return names.map(function (name) { return Array.from(name).slice(0, 5).join(''); });
  }

  function englishOrdinal(value) {
    const tail = value % 100;
    if (tail >= 11 && tail <= 13) {
      return value + 'th';
    }
    const last = value % 10;
    if (last === 1) return value + 'st';
    if (last === 2) return value + 'nd';
    if (last === 3) return value + 'rd';
    return value + 'th';
  }

  // Кэш имён: ключ — язык + вид + форма.
  const nameCache = new Map();

  function intlNames(locale, kind, style) {
    const key = locale + '|' + kind + '|' + style;
    if (nameCache.has(key)) {
      return nameCache.get(key);
    }
    let names = null;
    try {
      if (typeof Intl !== 'undefined' && typeof Intl.DateTimeFormat === 'function') {
        const options = { timeZone: 'UTC' };
        options[kind === 'month' ? 'month' : 'weekday'] = style;
        const format = new Intl.DateTimeFormat(locale, options);
        names = [];
        const start = kind === 'month' ? 0 : 7; // 7 июня 2020 — воскресенье
        for (let i = 0; i < (kind === 'month' ? 12 : 7); i += 1) {
          const date = kind === 'month' ? new Date(Date.UTC(2020, i, 15)) : new Date(Date.UTC(2020, 5, start + i));
          let text = format.format(date);
          if (style === 'short') {
            // Точку после сокращения убираем: «янв.» → «янв».
            text = text.replace(/\.$/, '').trim();
          }
          names.push(text);
        }
      }
    } catch (error) {
      names = null;
    }
    if (!names || names.length !== (kind === 'month' ? 12 : 7) || names.some(function (n) { return !n; })) {
      names = null;
    }
    if (names && style === 'short') {
      // Intl отдаёт неровные сокращения («февр», «сент», «janv.»). Подрезаем до
      // минимальной длины, при которой подписи внутри поля не совпадают:
      // ru — три знака, fr — четыре (иначе июнь и июль оба «jui»).
      names = shortenNames(names);
    }
    nameCache.set(key, names);
    return names;
  }

  function fallbackNames(lang, kind, style) {
    const table = INTL_LOCALES[lang === 'ru' ? 'ru' : 'en'];
    if (kind === 'month') {
      return style === 'short' ? table.monthsShort : table.monthsLong;
    }
    return style === 'short' ? table.dowShort : table.dowLong;
  }

  function resolveLang(locale) {
    const base = String(locale || '').toLowerCase().split(/[-_]/)[0];
    return MESSAGE_LANGS.indexOf(base) === -1 ? 'en' : base;
  }

  // Первый день недели по стандарту CLDR: Intl.Locale.weekInfo / getWeekInfo.
  // ru → понедельник, en-US → воскресенье.
  function intlWeekStart(locale) {
    try {
      if (typeof Intl !== 'undefined' && typeof Intl.Locale === 'function') {
        const loc = new Intl.Locale(locale);
        const info = typeof loc.getWeekInfo === 'function' ? loc.getWeekInfo() : loc.weekInfo;
        if (info && info.firstDay) {
          return info.firstDay === 1 ? 'mon' : 'sun';
        }
      }
    } catch (error) {
      /* язык без данных CLDR — остаётся значение по умолчанию */
    }
    return 'sun';
  }

  // FIELDS — только метаданные поля: границы и число колонок. Подписи — в локали.
  const FIELDS = {
    min: { key: 'min', min: 0, max: 59, cols: 12 },
    hour: { key: 'hour', min: 0, max: 23, cols: 6 },
    dom: { key: 'dom', min: 1, max: 31, cols: 8 },
    month: { key: 'month', min: 1, max: 12, cols: 4 },
    dow: { key: 'dow', min: 0, max: 6, cols: 4 }
  };

  const FIELD_ORDER = ['min', 'hour', 'dom', 'month', 'dow'];
  // Сколько дней реально бывает в месяце. Февраль — 29: 29-е существует
  // в високосный год, пугать пользователя незачем.
  const DAYS_IN_MONTH = { 1: 31, 2: 29, 3: 31, 4: 30, 5: 31, 6: 30, 7: 31, 8: 31, 9: 30, 10: 31, 11: 30, 12: 31 };
  // Сверху два крупных блока, снизу остальные: иначе браузер набивает первый
  // ряд тремя блоками и низ остаётся полупустым.
  const LINE_SIZES = [2, 3];

  // ---------------------------------------------------------------- parsing

  function parseToken(token, cfg) {
    const raw = String(token == null ? '' : token).trim();
    if (raw === '' || raw === '*') {
      return { star: true, values: [], raw: '' };
    }
    const found = new Set();
    const parts = raw.toLowerCase().split(',');
    for (let i = 0; i < parts.length; i += 1) {
      const piece = parts[i].trim();
      if (piece === '') {
        return { star: false, values: [], raw: raw };
      }
      if (piece.startsWith('*/')) {
        const step = Number(piece.slice(2));
        if (!Number.isInteger(step) || step <= 0) {
          return { star: false, values: [], raw: raw };
        }
        for (let v = cfg.min; v <= cfg.max; v += step) {
          found.add(v);
        }
        continue;
      }
      const range = piece.match(/^(\d+)-(\d+)(?:\/(\d+))?$/);
      if (range) {
        const from = Number(range[1]);
        const to = Number(range[2]);
        const step = range[3] ? Number(range[3]) : 1;
        if (!Number.isInteger(from) || !Number.isInteger(to) || step <= 0 || from > to || from < cfg.min || to > cfg.max) {
          return { star: false, values: [], raw: raw };
        }
        for (let v = from; v <= to; v += step) {
          found.add(v);
        }
        continue;
      }
      if (/^\d+$/.test(piece)) {
        const v = Number(piece);
        if (v < cfg.min || v > cfg.max) {
          return { star: false, values: [], raw: raw };
        }
        found.add(v);
        continue;
      }
      // Имена месяцев/дней и прочие формы ячейками не представить.
      return { star: false, values: [], raw: raw };
    }
    return {
      star: false,
      values: Array.from(found).sort(function (a, b) { return a - b; }),
      raw: ''
    };
  }

  function runsString(sorted) {
    if (!Array.isArray(sorted) || sorted.length === 0) {
      return '*';
    }
    const runs = [];
    let runStart = sorted[0];
    let prev = sorted[0];
    for (let i = 1; i < sorted.length; i += 1) {
      const v = sorted[i];
      if (v === prev + 1) {
        prev = v;
        continue;
      }
      runs.push([runStart, prev]);
      runStart = v;
      prev = v;
    }
    runs.push([runStart, prev]);
    return runs.map(function (pair) {
      return pair[0] === pair[1] ? String(pair[0]) : pair[0] + '-' + pair[1];
    }).join(',');
  }

  // progressions собирает все арифметические прогрессии из выбранных значений:
  // шаг s, старт из выбранного значения, дальше только значения из набора —
  // лишних чисел такая прогрессия подразумевать не может по построению.
  function progressions(sorted, cfg) {
    const present = new Set(sorted);
    const out = [];
    const maxStep = Math.max(1, cfg.max - cfg.min);
    for (let step = 1; step <= maxStep; step += 1) {
      sorted.forEach(function (start) {
        const seq = [];
        for (let v = start; v <= cfg.max; v += step) {
          if (!present.has(v)) {
            break;
          }
          seq.push(v);
        }
        if (seq.length < 3) {
          return;
        }
        const finish = seq[seq.length - 1];
        let token;
        if (start === cfg.min && finish + step > cfg.max) {
          token = '*/' + step;
        } else if (step === 1) {
          token = start + '-' + finish;
        } else {
          token = start + '-' + finish + '/' + step;
        }
        out.push({ token: token, values: seq, start: start });
      });
    }
    return out;
  }

  // coverCandidate жадно накрывает набор прогрессиями. Прогрессия берётся
  // только если она КОРОЧЕ перечисления того, что накрывает: иначе «7-53/23»
  // (9 символов) проигрывает простому «7,53» (4). Остаток дописывается
  // перечислением диапазонами.
  function coverCandidate(sorted, cfg, preferEnumeration) {
    const candidates = progressions(sorted, cfg);
    const uncovered = new Set(sorted);
    const picks = [];
    while (uncovered.size) {
      let best = null;
      let bestSaving = 0;
      let bestGain = 0;
      candidates.forEach(function (cand) {
        const gainValues = cand.values.filter(function (v) { return uncovered.has(v); });
        if (gainValues.length < 2) {
          return;
        }
        const asList = runsString(gainValues);
        const profitable = preferEnumeration
          ? cand.token.length < asList.length
          : cand.token.length <= asList.length;
        if (!profitable) {
          return;
        }
        const saving = asList.length - cand.token.length;
        const better = !best || saving > bestSaving || (saving === bestSaving && gainValues.length > bestGain);
        if (better) {
          best = cand;
          bestSaving = saving;
          bestGain = gainValues.length;
        }
      });
      if (!best) {
        break;
      }
      best.values.forEach(function (v) { uncovered.delete(v); });
      picks.push(best);
    }
    const rest = Array.from(uncovered).sort(function (a, b) { return a - b; });
    if (rest.length) {
      picks.push({ token: runsString(rest), start: rest[0] });
    }
    picks.sort(function (a, b) { return a.start - b.start; });
    return picks.map(function (item) { return item.token; }).join(',');
  }

  function formatToken(values, cfg, opts) {
    if (!Array.isArray(values) || values.length === 0) {
      return '*';
    }
    const preferEnumeration = !opts || opts.preferEnumeration !== false;
    const sorted = Array.from(new Set(values)).sort(function (a, b) { return a - b; });
    if (sorted.length === cfg.max - cfg.min + 1) {
      return '*';
    }
    const plain = runsString(sorted);
    const covered = coverCandidate(sorted, cfg, preferEnumeration);
    // preferEnumeration (по умолчанию включено): при равной длине выигрывает
    // перечисление — «2,4,6,10» читается лучше, чем «2-6/2,10».
    // Выключено: при равенстве выигрывает форма с шагом.
    const shorter = preferEnumeration
      ? covered.length < plain.length
      : covered.length <= plain.length;
    return shorter ? covered : plain;
  }

  // Смещение часового пояса: насколько «настенные» часы зоны ушли от UTC.
  // Считается один раз на старте — DST внутри горизонта не учитываем (для МСК,
  // где переходов нет, это точно; для других зон возможна ошибка в час после
  // перевода стрелок).
  function zoneOffsetMs(date, timeZone) {
    if (!timeZone) {
      return null;
    }
    try {
      const format = new Intl.DateTimeFormat('en-US', {
        timeZone: timeZone,
        hourCycle: 'h23',
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit'
      });
      const parts = {};
      format.formatToParts(date).forEach(function (part) {
        if (part.type !== 'literal') {
          parts[part.type] = part.value;
        }
      });
      const asUTC = Date.UTC(+parts.year, +parts.month - 1, +parts.day, +parts.hour, +parts.minute, +parts.second);
      // Date.getTime() содержит миллисекунды, а formatToParts их не отдаёт:
      // без выравнивания смещение получается «на 0.7 секунды меньше» и
      // wall-минута съезжает на предыдущую.
      const aligned = date.getTime() - date.getMilliseconds();
      return asUTC - aligned;
    } catch (error) {
      return null;
    }
  }

  // nextRuns считает ближайшие срабатывания выражения. Правило дня — как в
  // стандартном cron (robfig/cron): если ограничены и день месяца, и день
  // недели, достаточно совпадения любого из них.
  function nextRuns(tokens, opts) {
    const options = opts || {};
    const count = Math.max(1, options.count || 5);
    const from = options.from instanceof Date ? options.from : new Date();
    if (!Array.isArray(tokens) && String(tokens == null ? '' : tokens).trim() === '') {
      return [];
    }
    const list = Array.isArray(tokens) ? tokens : splitExpression(tokens, 5);
    if (list.length !== 5) {
      return [];
    }
    const parsed = list.map(function (token, index) {
      const cfg = FIELDS[FIELD_ORDER[index]];
      const result = parseToken(token, cfg);
      return { star: result.star, values: result.values };
    });
    if (parsed.some(function (field) { return !field.star && field.values.length === 0; })) {
      return [];
    }

    const offsetMs = zoneOffsetMs(from, options.timeZone);
    const useUTC = offsetMs !== null;
    const partsOf = function (ms) {
      const shifted = useUTC ? new Date(ms + offsetMs) : new Date(ms);
      return useUTC
        ? {
          year: shifted.getUTCFullYear(), month: shifted.getUTCMonth() + 1, day: shifted.getUTCDate(),
          hour: shifted.getUTCHours(), minute: shifted.getUTCMinutes(), weekday: shifted.getUTCDay()
        }
        : {
          year: shifted.getFullYear(), month: shifted.getMonth() + 1, day: shifted.getDate(),
          hour: shifted.getHours(), minute: shifted.getMinutes(), weekday: shifted.getDay()
        };
    };

    const monthOk = function (p) {
      return parsed[3].star || parsed[3].values.indexOf(p.month) !== -1;
    };
    const dayOk = function (p) {
      if (!monthOk(p)) {
        return false;
      }
      const dom = parsed[2];
      const dow = parsed[4];
      if (dom.star && dow.star) {
        return true;
      }
      const domHit = !dom.star && dom.values.indexOf(p.day) !== -1;
      const dowHit = !dow.star && dow.values.indexOf(p.weekday) !== -1;
      if (dom.star) {
        return dowHit;
      }
      if (dow.star) {
        return domHit;
      }
      return domHit || dowHit;
    };
    const hourOk = function (p) {
      return parsed[1].star || parsed[1].values.indexOf(p.hour) !== -1;
    };
    const minuteOk = function (p) {
      return parsed[0].star || parsed[0].values.indexOf(p.minute) !== -1;
    };

    const minuteMs = 60000;
    const limit = from.getTime() + 366 * 5 * 86400000;
    let cursor = Math.floor(from.getTime() / minuteMs) * minuteMs + minuteMs;
    const out = [];
    let guard = 0;

    while (cursor <= limit && out.length < count && guard < 500000) {
      guard += 1;
      const p = partsOf(cursor);
      if (!dayOk(p)) {
        cursor += ((24 * 60) - (p.hour * 60 + p.minute)) * minuteMs;
        continue;
      }
      if (!hourOk(p)) {
        cursor += (60 - p.minute) * minuteMs;
        continue;
      }
      if (!minuteOk(p)) {
        cursor += minuteMs;
        continue;
      }
      out.push(new Date(cursor));
      cursor += minuteMs;
    }
    return out;
  }

  // formatRun печатает дату запуска. Состав строки задаётся форматом:
  //   'day', 'month', 'time', 'weekday', 'year'
  // По умолчанию — 'day month time' (день недели не выводим: он лишний).
  // Между датой и временем ставится широкий пробел (U+2002), между днём и
  // месяцем — обычный; запятая на стыке дата→время не нужна.
  const RUN_FORMAT_DEFAULT = ['day', 'month', 'time'];
  const RUN_FORMAT_TOKENS = ['weekday', 'day', 'month', 'year', 'time'];

  function normalizeRunFormat(value) {
    if (value == null) {
      return RUN_FORMAT_DEFAULT.slice();
    }
    const list = Array.isArray(value) ? value : String(value).split(/[\s,|]+/);
    const picked = [];
    list.forEach(function (token) {
      const name = String(token || '').trim().toLowerCase();
      if (RUN_FORMAT_TOKENS.indexOf(name) !== -1 && picked.indexOf(name) === -1) {
        picked.push(name);
      }
    });
    return picked.length ? picked : RUN_FORMAT_DEFAULT.slice();
  }

  function formatRun(date, locale, timeZone, format) {
    const wanted = normalizeRunFormat(format);
    const takes = function (type) {
      if (type === 'hour' || type === 'minute' || type === 'second') {
        return wanted.indexOf('time') !== -1;
      }
      return wanted.indexOf(type) !== -1;
    };

    let formatter;
    try {
      const options = { weekday: 'short', day: '2-digit', month: 'long', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' };
      if (timeZone) {
        options.timeZone = timeZone;
      }
      formatter = new Intl.DateTimeFormat(locale, options);
    } catch (error) {
      formatter = null;
    }
    if (!formatter) {
      return date.toISOString();
    }

    const dateTypes = ['weekday', 'year', 'month', 'day'];
    const out = [];
    let gap = '';
    formatter.formatToParts(date).forEach(function (part) {
      if (part.type === 'literal') {
        gap += part.value;
        return;
      }
      if (!takes(part.type)) {
        gap = gap.replace(/,/g, '');
        return;
      }
      if (out.length) {
        out.push({ type: 'literal', value: gap });
      }
      gap = '';
      out.push({ type: part.type, value: part.value });
    });

    // Разделители: на стыке «дата → время» — широкий пробел, остальное как есть.
    for (let i = 0; i < out.length; i += 1) {
      if (out[i].type !== 'literal') {
        continue;
      }
      const before = out.slice(0, i).reverse().find(function (item) { return item.type !== 'literal'; });
      const after = out.slice(i + 1).find(function (item) { return item.type !== 'literal'; });
      const beforeDate = before && dateTypes.indexOf(before.type) !== -1;
      const afterTime = after && (after.type === 'hour' || after.type === 'minute' || after.type === 'second');
      if (beforeDate && afterTime) {
        const raw = out[i].value;
        // Если в стыке есть слово-связка («в», «at», «um», «日» как суффикс дня) —
        // оставляем его и обычные пробелы, убирая только запятую. Чистый
        // разделитель заменяем на широкий пробел.
        out[i].value = /[^\s,]/.test(raw) ? raw.replace(/,/g, ' ') : '\u2002';
      }
    }

    const text = out.map(function (item) { return item.value; }).join('');
    return text.replace(/ {2,}/g, ' ').replace(/^[\s,]+|[\s,]+$/g, '');
  }

  function splitExpression(expr, count) {
    const parts = String(expr == null ? '' : expr).trim().split(/\s+/).filter(Boolean);
    while (parts.length < count) {
      parts.push('*');
    }
    return parts.slice(0, count);
  }

  // -------------------------------------------------------------- component

  const STYLE = [
    ':host{display:block;border:1px solid var(--cron-border,#6c757d);border-radius:.375rem;padding:.5rem .75rem;color:inherit;font-size:inherit}',
    '.grid{display:flex;flex-direction:column;gap:.9rem}',
    /* Шаг сетки задаётся переменными, а зазор между блоками равен ровно одной
       ячейке с её отступом — тогда края рядов сходятся:
       минута(12) + зазор + час(6) = день(8) + зазор + месяц(4) + зазор + дни(5). */
    '.line{display:flex;flex-wrap:wrap;align-items:flex-start;column-gap:calc(var(--cron-cell-w,1.9rem) + 2 * var(--cron-cell-gap,.2rem));row-gap:.85rem}',
    '.block{display:grid;min-width:0;justify-items:stretch}',
    /* width:0 + min-width:100%: заголовок занимает ширину блока, но НЕ участвует
       в её расчёте — иначе длинное «день недели:» растягивало блок и ряд уезжал. */
    /* По центру по вертикали: подпись и число стоят на одной оси, число
       центрируется внутри своего поля, а не по базовой линии подписи. */
    '.head{display:flex;align-items:center;gap:.5rem;width:0;min-width:100%;margin-bottom:.25rem;overflow:hidden}',
    '.label{flex:0 1 auto;min-width:0;overflow:hidden;text-overflow:clip;font-family:var(--cron-mono,ui-monospace,SFMono-Regular,monospace);font-size:.78rem;color:var(--cron-label,#6c757d);white-space:nowrap}',
    '.label::after{content:":"}',
    /* Правый край числа = правый край ячеек: без горизонтального паддинга и без
       рамки в метрике (рамка нарисована inset-тенью, поэтому при фокусе число
       не сдвигается ни на пиксель). */
    '.token{flex:1 1 auto;width:0;min-width:0;height:auto;padding:.1rem 0;font-family:var(--cron-mono,ui-monospace,SFMono-Regular,monospace);font-size:.78rem;line-height:1.35;text-align:right;color:var(--cron-accent,#198754);background:transparent;border:0;border-radius:.25rem;box-shadow:inset 0 0 0 1px transparent}',
    '.token:hover{box-shadow:inset 0 0 0 1px var(--cron-cell-border,#495057)}',
    '.token:focus{outline:none;color:var(--cron-cell-color,#ced4da);background:var(--cron-input-bg,#000);box-shadow:inset 0 0 0 1px var(--cron-focus,#0d6efd)}',
    '.token.bad{color:var(--cron-error,#dc3545);box-shadow:inset 0 0 0 1px var(--cron-error,#dc3545)}',
    '.cells{display:grid;grid-template-columns:repeat(var(--cron-cols,12),var(--cron-cell-w,1.9rem));gap:var(--cron-cell-gap,.2rem);justify-content:start}',
    '.cell{box-sizing:border-box;width:var(--cron-cell-w,1.9rem);padding:.1rem 0;text-align:center;font-family:inherit;font-size:.74rem;line-height:1.35;color:var(--cron-cell-color,#ced4da);background:var(--cron-cell-bg,transparent);border:1px solid var(--cron-cell-border,#495057);border-radius:.25rem;cursor:pointer}',
    '.cell:hover{border-color:var(--cron-focus,#0dcaf0);color:var(--cron-focus,#0dcaf0)}',
    '.cell.on{background:var(--cron-accent,#198754);border-color:var(--cron-accent,#198754);color:var(--cron-accent-text,#fff)}',
    /* Жёлтый — часть комбинации невозможна (31 в феврале, но март есть),
       красный — невозможна вся комбинация (31 и только февраль). */
    '.cell.warn{border-color:var(--cron-warn,#ffc107);color:var(--cron-warn,#ffc107)}',
    '.cell.on.warn{background:var(--cron-warn,#ffc107);border-color:var(--cron-warn,#ffc107);color:#1b1b1b}',
    '.token.warn{color:var(--cron-warn,#ffc107)}',
    '.cell.error{border-color:var(--cron-error,#dc3545);color:var(--cron-error,#dc3545)}',
    '.cell.on.error{background:var(--cron-error,#dc3545);border-color:var(--cron-error,#dc3545);color:#fff}',
    '.token.error{color:var(--cron-error,#dc3545)}',
    /* mode="popover": сам тег без рамки, сетка живёт в выпадающей панели. */
    ':host([mode="popover"]){border:none;padding:0;display:block;position:relative}',
    '.trigger{width:100%;box-sizing:border-box;padding:.375rem .75rem;font-family:var(--cron-mono,ui-monospace,SFMono-Regular,monospace);font-size:.85rem;color:inherit;background:var(--cron-input-bg,#000);border:1px solid var(--cron-cell-border,#495057);border-radius:.375rem}',
    '.trigger:focus{outline:none;border-color:var(--cron-focus,#0d6efd)}',
    /* Панель шире своего инпута: сетке из 12 колонок нужно больше места.
       Ширина по содержимому, но не больше окна; если упирается в правый край —
       выравнивается по правому краю инпута (класс align-right). */
    '.panel{position:absolute;left:0;top:100%;margin-top:.25rem;z-index:1050;padding:.5rem .75rem;background:var(--cron-panel-bg,#16191d);border:1px solid var(--cron-border,#6c757d);border-radius:.375rem;box-shadow:0 10px 30px rgba(0,0,0,.45);width:max-content;min-width:100%;max-width:var(--cron-panel-max-width,calc(100vw - 3rem));overflow-x:auto;box-sizing:border-box}',
    '.panel.align-right{left:auto;right:0}',
    '.panel[hidden]{display:none}',
    /* Список ближайших запусков под сеткой (флаг next-runs). */
    '.next{margin-top:.7rem;padding-top:.55rem;border-top:1px solid var(--cron-border,#6c757d)}',
    '.next-title{display:block;font-family:var(--cron-mono,ui-monospace,SFMono-Regular,monospace);font-size:.78rem;color:var(--cron-label,#6c757d)}',
    '.next-list{display:flex;flex-wrap:wrap;gap:.15rem 1.1rem;margin:.3rem 0 0;padding:0;list-style:none}',
    '.next-list li{font-family:var(--cron-mono,ui-monospace,SFMono-Regular,monospace);font-size:.78rem;color:var(--cron-accent,#198754);white-space:nowrap}',
    '.next-empty{margin:.3rem 0 0;font-family:var(--cron-mono,ui-monospace,SFMono-Regular,monospace);font-size:.78rem;color:var(--cron-warn,#ffc107)}'
  ].join('\n');

  // В Node нет HTMLElement — берём пустую базу, чтобы файл импортировался ради
  // чистых функций (элемент в такой среде всё равно не создаётся).
  const HTMLElementBase = typeof HTMLElement !== 'undefined' ? HTMLElement : class {};

  class CronEditor extends HTMLElementBase {
    static parseToken(token, cfg) { return parseToken(token, cfg); }
    static nextRuns(tokens, opts) { return nextRuns(tokens, opts); }
    static zoneOffsetMs(date, timeZone) { return zoneOffsetMs(date, timeZone); }
    static formatToken(values, cfg, opts) { return formatToken(values, cfg, opts); }
    static get fields() { return FIELD_ORDER.slice(); }

    constructor() {
      super();
      this.attachShadow({ mode: 'open' });
      this._fields = FIELD_ORDER.slice();
      this._tokens = {};
      this._raw = {};
      this._built = false;
      FIELD_ORDER.forEach(function (key) {
        this._tokens[key] = '*';
        this._raw[key] = '';
      }, this);
    }

    connectedCallback() {
      const attr = this.getAttribute('fields');
      if (attr) {
        const wanted = attr.split(',').map(function (s) { return s.trim(); }).filter(function (k) { return FIELDS[k]; });
        if (wanted.length) {
          this._fields = wanted;
        }
      }
      this._build();
      const initial = this.getAttribute('value');
      if (initial != null) {
        this.value = initial;
      } else {
        this._syncUI();
      }
    }

    // nextRuns — сколько ближайших запусков показать под сеткой.
    // Атрибут next-runs: пустой или next-runs="5" → пять строк; "0" — выключено.
    get nextRuns() {
      if (!this.hasAttribute('next-runs')) {
        return 0;
      }
      const raw = this.getAttribute('next-runs');
      if (raw === '' || raw === null) {
        return 5;
      }
      const parsed = parseInt(raw, 10);
      return Number.isFinite(parsed) && parsed > 0 ? Math.min(parsed, 20) : 0;
    }

    set nextRuns(next) {
      const value = Number(next);
      if (!Number.isFinite(value) || value <= 0) {
        this.removeAttribute('next-runs');
        return;
      }
      this.setAttribute('next-runs', String(Math.min(Math.floor(value), 20)));
    }

    // runFormat — из чего собирать строку запуска: weekday, day, month, year, time.
    // Атрибут run-format="day month time" (по умолчанию), "time" — только время,
    // "weekday day month time" — вернуть день недели.
    get runFormat() {
      const raw = this.getAttribute('run-format');
      return normalizeRunFormat(raw == null ? null : raw);
    }

    set runFormat(next) {
      if (next == null || next === '') {
        this.removeAttribute('run-format');
        return;
      }
      const list = normalizeRunFormat(next);
      this.setAttribute('run-format', list.join(' '));
    }

    // timeZone — IANA-зона для расчёта и подписи запусков (например Europe/Moscow).
    // Не задана — берётся локальная зона браузера.
    get timeZone() {
      return this.getAttribute('timezone') || '';
    }

    set timeZone(next) {
      if (next) {
        this.setAttribute('timezone', String(next));
      } else {
        this.removeAttribute('timezone');
      }
    }

    // locale — язык подписей и названий. По умолчанию берётся из <html lang>,
    // иначе из navigator.language. Названия месяцев и дней идут из Intl,
    // фразы подсказок — из MESSAGES (незнакомый язык получает английские).
    get locale() {
      const own = this.getAttribute('locale');
      if (own) {
        return own;
      }
      const docLang = typeof document !== 'undefined' && document.documentElement
        ? document.documentElement.getAttribute('lang')
        : '';
      if (docLang) {
        return docLang;
      }
      if (typeof navigator !== 'undefined' && navigator.language) {
        return navigator.language;
      }
      return 'en';
    }

    set locale(next) {
      if (next) {
        this.setAttribute('locale', String(next));
      } else {
        this.removeAttribute('locale');
      }
    }

    // dict — всё, что нужно разметке: подписи полей, названия, фразы.
    get dict() {
      const locale = this.locale;
      const lang = resolveLang(locale);
      const messages = MESSAGES[lang];
      const pick = function (kind, style) {
        return intlNames(locale, kind, style) || fallbackNames(lang, kind, style);
      }.bind(this);
      const dict = {
        fields: messages.fields,
        expressionAria: messages.expressionAria,
        cronAria: messages.cronAria,
        nextRunsTitle: messages.nextRunsTitle,
        noRuns: messages.noRuns,
        dayDead: messages.dayDead,
        dayPartial: messages.dayPartial,
        monthDead: messages.monthDead,
        monthPartial: messages.monthPartial,
        monthsShort: pick('month', 'short'),
        monthsLong: pick('month', 'long'),
        dowShort: pick('dow', 'short'),
        dowLong: pick('dow', 'long')
      };
      return dict;
    }

    // weekStart — с какого дня рисовать неделю: 'sun' (по умолчанию) или 'mon'.
    // 'auto' — по данным CLDR для текущего языка (ru → mon, en-US → sun).
    // Порядок ячеек меняется, значения cron остаются прежними (0 = воскресенье).
    get weekStart() {
      const raw = String(this.getAttribute('week-start') || 'sun').toLowerCase();
      if (raw === 'auto') {
        return intlWeekStart(this.locale);
      }
      return raw === 'mon' ? 'mon' : 'sun';
    }

    set weekStart(next) {
      if (String(next || 'sun').toLowerCase() === 'mon') {
        this.setAttribute('week-start', 'mon');
      } else {
        this.removeAttribute('week-start');
      }
      if (this._built) {
        this._rebuild();
      }
    }

    _dowOrder() {
      return this.weekStart === 'mon' ? [1, 2, 3, 4, 5, 6, 0] : [0, 1, 2, 3, 4, 5, 6];
    }

    // preferEnumeration — флаг «при равной длине показывать перечисление».
    // Включён по умолчанию; выключается атрибутом enumeration="off".
    get preferEnumeration() {
      const raw = String(this.getAttribute('enumeration') || 'prefer').toLowerCase();
      return raw !== 'off' && raw !== 'false' && raw !== 'never';
    }

    set preferEnumeration(on) {
      if (on === false) {
        this.setAttribute('enumeration', 'off');
      } else {
        this.removeAttribute('enumeration');
      }
      if (this._built) {
        this._syncUI();
      }
    }

    get mode() {
      const value = String(this.getAttribute('mode') || 'always').toLowerCase();
      return value === 'popover' ? 'popover' : 'always';
    }

    set mode(next) {
      const value = String(next || 'always').toLowerCase();
      if (value === 'popover') {
        this.setAttribute('mode', 'popover');
      } else {
        this.removeAttribute('mode');
      }
    }

    _openPanel() {
      if (!this._panel || !this._panel.hidden) {
        return;
      }
      this._panel.hidden = false;
      this._placePanel();
      this.dispatchEvent(new CustomEvent('open', { bubbles: true, composed: true }));
      this._onDocumentDown = function (ev) {
        const path = ev.composedPath ? ev.composedPath() : [];
        if (path.indexOf(this) === -1) {
          this._closePanel();
        }
      }.bind(this);
      document.addEventListener('mousedown', this._onDocumentDown, true);
    }

    // _placePanel не даёт выпадашке уехать за правый край окна.
    _placePanel() {
      if (!this._panel) {
        return;
      }
      this._panel.classList.remove('align-right');
      const rect = this._panel.getBoundingClientRect();
      const gutter = 8;
      if (rect.right > window.innerWidth - gutter) {
        this._panel.classList.add('align-right');
      }
      const shifted = this._panel.getBoundingClientRect();
      if (shifted.left < gutter) {
        this._panel.classList.remove('align-right');
        this._panel.style.left = gutter - this.getBoundingClientRect().left + 'px';
      } else {
        this._panel.style.left = '';
      }
      if (window.innerHeight - shifted.top < shifted.height + gutter) {
        this._panel.style.maxHeight = Math.max(window.innerHeight - shifted.top - gutter * 2, 200) + 'px';
        this._panel.style.overflowY = 'auto';
      } else {
        this._panel.style.maxHeight = '';
        this._panel.style.overflowY = '';
      }
    }

    _closePanel() {
      if (!this._panel || this._panel.hidden) {
        return;
      }
      this._panel.hidden = true;
      if (this._onDocumentDown) {
        document.removeEventListener('mousedown', this._onDocumentDown, true);
        this._onDocumentDown = null;
      }
      if (this._trigger && this._trigger.value !== this.value) {
        this._trigger.value = this.value;
      }
      this.dispatchEvent(new CustomEvent('close', { bubbles: true, composed: true }));
    }

    get value() {
      return this._fields.map(function (key) { return this._tokenOf(key); }, this).join(' ');
    }

    set value(next) {
      const parts = splitExpression(next, this._fields.length);
      this._fields.forEach(function (key, index) {
        this._setToken(key, parts[index], { silent: true, fromInput: true });
      }, this);
      this._syncUI();
    }

    get values() {
      const out = {};
      this._fields.forEach(function (key) {
        out[key] = parseToken(this._tokenOf(key), FIELDS[key]).values;
      }, this);
      return out;
    }

    setValues(map) {
      if (!map || typeof map !== 'object') {
        return;
      }
      this._fields.forEach(function (key) {
        if (Array.isArray(map[key])) {
          this._setToken(key, formatToken(map[key], FIELDS[key], { preferEnumeration: this.preferEnumeration }), { silent: true });
        }
      }, this);
      this._syncUI();
    }

    // ------------------------------------------------------------- internal

    _tokenOf(key) {
      const raw = this._raw[key];
      if (raw) {
        return raw;
      }
      const parsed = parseToken(this._tokens[key], FIELDS[key]);
      if (parsed.star) {
        return '*';
      }
      return formatToken(parsed.values, FIELDS[key], { preferEnumeration: this.preferEnumeration });
    }

    _setToken(key, token, opts) {
      const cfg = FIELDS[key];
      const parsed = parseToken(token, cfg);
      this._raw[key] = (parsed.values.length === 0 && !parsed.star) ? String(token == null ? '' : token).trim() : '';
      this._tokens[key] = formatToken(parsed.values, cfg, { preferEnumeration: this.preferEnumeration });
      if (!opts || !opts.silent) {
        this._paint(key);
        this._emit();
      }
    }

    _emit() {
      this.dispatchEvent(new CustomEvent('change', {
        bubbles: true,
        composed: true,
        detail: { value: this.value, values: this.values }
      }));
    }

    _build() {
      if (this._built) {
        return;
      }
      this._nextBox = null;
      const style = document.createElement('style');
      style.textContent = STYLE;
      const grid = document.createElement('div');
      grid.className = 'grid';

      let offset = 0;
      const fields = this._fields;
      while (offset < fields.length) {
        const size = offset === 0 ? (LINE_SIZES[0] || 2) : (LINE_SIZES[1] || 3);
        const line = document.createElement('div');
        line.className = 'line';
        fields.slice(offset, offset + size).forEach(function (key) {
          line.appendChild(this._buildBlock(key));
        }, this);
        offset += size;
        grid.appendChild(line);
      }

      const root = document.createElement('div');
      root.appendChild(style);

      if (this.mode === 'popover') {
        const trigger = document.createElement('input');
        trigger.className = 'trigger';
        trigger.type = 'text';
        trigger.spellcheck = false;
        trigger.autocomplete = 'off';
        trigger.setAttribute('aria-label', this.dict.cronAria);
        trigger.addEventListener('focus', this._openPanel.bind(this));
        trigger.addEventListener('click', this._openPanel.bind(this));
        trigger.addEventListener('change', function () {
          this.value = trigger.value;
        }.bind(this));
        trigger.addEventListener('keydown', function (ev) {
          if (ev.key === 'Enter') {
            this.value = trigger.value;
            this._closePanel();
          }
          if (ev.key === 'Escape') {
            this._closePanel();
          }
        }.bind(this));

        const panel = document.createElement('div');
        panel.className = 'panel';
        panel.hidden = true;
        panel.appendChild(grid);

        root.appendChild(trigger);
        root.appendChild(panel);
        this._trigger = trigger;
        this._panel = panel;
      } else {
        root.appendChild(grid);
      }

      this.shadowRoot.appendChild(root);
      this._built = true;
      this._root = root;
    }

    _buildBlock(key) {
      const cfg = FIELDS[key];
      const self = this;

      const block = document.createElement('div');
      block.className = 'block';
      block.dataset.field = key;

      const head = document.createElement('div');
      head.className = 'head';
      const dict = this.dict;
      const label = document.createElement('span');
      label.className = 'label';
      label.textContent = dict.fields[key] || key;
      const token = document.createElement('input');
      token.className = 'token';
      token.type = 'text';
      token.spellcheck = false;
      token.autocomplete = 'off';
      token.setAttribute('aria-label', (dict.fields[key] || key) + ' — ' + dict.expressionAria);
      token.addEventListener('input', function () {
        block.dataset.userToken = '1';
        self._raw[key] = '';
        self._tokens[key] = token.value;
        self._paintCells(key, token.value);
        self._emit();
      });
      token.addEventListener('blur', function () {
        delete block.dataset.userToken;
        const parsed = parseToken(token.value, cfg);
        if (parsed.values.length === 0 && !parsed.star) {
          self._raw[key] = String(token.value || '').trim();
          self._paint(key);
          return;
        }
        self._setToken(key, formatToken(parsed.values, cfg, { preferEnumeration: self.preferEnumeration }), { silent: true });
        self._paint(key);
        self._emit();
      });
      head.appendChild(label);
      head.appendChild(token);

      const cells = document.createElement('div');
      cells.className = 'cells';
      // День недели рисуем в порядке week-start: с вс — 4 колонки (4+3),
      // с пн — 5 колонок (5+2, будни и выходные отдельными рядами).
      let values;
      let cols = cfg.cols || 12;
      if (key === 'dow') {
        values = this._dowOrder();
        cols = this.weekStart === 'mon' ? 5 : 4;
      } else {
        values = [];
        for (let v = cfg.min; v <= cfg.max; v += 1) {
          values.push(v);
        }
      }
      cells.style.setProperty('--cron-cols', String(cols));
      values.forEach((v) => {
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'cell';
        btn.dataset.value = String(v);
        btn.textContent = this._caption(cfg, v);
        btn.title = this._title(cfg, v);
        btn.addEventListener('click', function () {
          self._raw[key] = '';
          const parsed = parseToken(self._tokenOf(key), cfg);
          const set = new Set(parsed.values);
          if (set.has(v)) {
            set.delete(v);
          } else {
            set.add(v);
          }
          self._setToken(key, formatToken(Array.from(set), cfg, { preferEnumeration: self.preferEnumeration }), { silent: true });
          self._paint(key);
          self._emit();
        });
        cells.appendChild(btn);
      });

      block.appendChild(head);
      block.appendChild(cells);
      this._blocks = this._blocks || {};
      this._blocks[key] = { block: block, token: token, cells: cells };
      return block;
    }

    _caption(cfg, value) {
      const dict = this.dict;
      if (cfg.key === 'month') {
        return dict.monthsShort[value - 1] || String(value);
      }
      if (cfg.key === 'dow') {
        return dict.dowShort[value] || String(value);
      }
      return String(value);
    }

    _title(cfg, value) {
      const dict = this.dict;
      if (cfg.key === 'month') {
        return dict.monthsLong[value - 1] || String(value);
      }
      if (cfg.key === 'dow') {
        return dict.dowLong[value] || String(value);
      }
      return String(value);
    }

    _paintCells(key, token) {
      const entry = this._blocks && this._blocks[key];
      if (!entry) {
        return;
      }
      const cfg = FIELDS[key];
      const parsed = parseToken(token, cfg);
      const allowed = new Set(parsed.values);
      Array.prototype.forEach.call(entry.cells.children, function (btn) {
        btn.classList.toggle('on', allowed.has(Number(btn.dataset.value)));
      });
      entry.token.classList.toggle('bad', Boolean(this._raw[key]));
      this._paintWarnings();
    }

    // _fieldSet отдаёт выбранные числа поля; «*» разворачивается в полный диапазон.
    _fieldSet(key) {
      const cfg = FIELDS[key];
      const parsed = parseToken(this._tokenOf(key), cfg);
      if (parsed.star) {
        const all = [];
        for (let v = cfg.min; v <= cfg.max; v += 1) {
          all.push(v);
        }
        return all;
      }
      return parsed.values;
    }

    // _paintWarnings красит три состояния:
    //   красный — число не бывает ни в одном из выбранных месяцев (31 при
    //             одном феврале) — этот день не сработает никогда;
    //   жёлтый  — число не бывает в части месяцев (31 при фев+июл: в июле
    //             сработает, в феврале нет); месяц жёлтый, если в нём нет части
    //             выбранных чисел, но другие месяцы спасают комбинацию;
    //   без цвета — всё сходится.
    _paintWarnings() {
      if (!this._blocks || !this._blocks.dom || !this._blocks.month) {
        return;
      }
      const dict = this.dict;
      const days = this._fieldSet('dom');
      const months = this._fieldSet('month');
      const limitOf = function (month) { return DAYS_IN_MONTH[month] || 31; };

      const dayMarks = new Map();
      const monthMarks = new Map();
      if (days.length && months.length) {
        days.forEach(function (day) {
          const hosts = months.filter(function (month) { return day <= limitOf(month); });
          if (!hosts.length) {
            dayMarks.set(day, 'error');
          } else if (hosts.length < months.length) {
            dayMarks.set(day, 'warn');
          }
        });
        // Месяц красен, только если комбинация не сработает вообще нигде;
        // если он принимает лишь часть выбранных чисел — он жёлтый.
        const deadMonths = months.filter(function (month) {
          return !days.some(function (day) { return day <= limitOf(month); });
        });
        const everyMonthDead = deadMonths.length === months.length;
        months.forEach(function (month) {
          const limit = limitOf(month);
          const fits = days.filter(function (day) { return day <= limit; });
          if (!fits.length) {
            monthMarks.set(month, everyMonthDead ? 'error' : 'warn');
            return;
          }
          if (fits.length < days.length) {
            monthMarks.set(month, 'warn');
          }
        });
      }

      const paint = function (key, marks, hint) {
        const entry = this._blocks[key];
        if (!entry) {
          return;
        }
        Array.prototype.forEach.call(entry.cells.children, function (btn) {
          const value = Number(btn.dataset.value);
          btn.classList.remove('warn', 'error');
          const cls = marks.get(value);
          if (cls && btn.classList.contains('on')) {
            btn.classList.add(cls);
            btn.title = hint(value, cls);
          }
        });
        const hasError = Array.prototype.some.call(entry.cells.children, function (btn) {
          return btn.classList.contains('error');
        });
        const hasWarn = Array.prototype.some.call(entry.cells.children, function (btn) {
          return btn.classList.contains('warn');
        });
        entry.token.classList.remove('warn', 'error');
        if (hasError) {
          entry.token.classList.add('error');
        } else if (hasWarn) {
          entry.token.classList.add('warn');
        }
      }.bind(this);

      paint('dom', dayMarks, function (day, cls) {
        if (cls === 'error') {
          return dict.dayDead(day);
        }
        const missing = months.filter(function (month) { return day > limitOf(month); })
          .map(function (month) { return dict.monthsLong[month - 1] || month; });
        return dict.dayPartial(day, missing.join(', '));
      });
      paint('month', monthMarks, function (month, cls) {
        const name = dict.monthsLong[month - 1] || String(month);
        const limit = limitOf(month);
        const missing = days.filter(function (day) { return day > limit; });
        const list = missing.join(', ');
        return cls === 'error' ? dict.monthDead(name, list) : dict.monthPartial(name, list);
      });
    }

    _paint(key) {
      const entry = this._blocks && this._blocks[key];
      if (!entry) {
        return;
      }
      const value = this._tokenOf(key);
      if (entry.token.value !== value) {
        entry.token.value = value;
      }
      this._paintCells(key, value);
    }

    static get observedAttributes() {
      return ['locale', 'week-start', 'mode', 'fields', 'enumeration', 'next-runs', 'timezone', 'run-format'];
    }

    attributeChangedCallback(name, previous, next) {
      if (previous === next || !this._built) {
        return;
      }
      if (name === 'enumeration') {
        this._syncUI();
        return;
      }
      if (name === 'next-runs' || name === 'timezone' || name === 'run-format') {
        this._paintNextRuns();
        return;
      }
      this._rebuild();
    }

    _rebuild() {
      this.shadowRoot.innerHTML = '';
      this._blocks = {};
      this._built = false;
      this._build();
      this._syncUI();
    }

    // _paintNextRuns рисует (или убирает) блок «Следующие запуски».
    _paintNextRuns() {
      if (!this._built) {
        return;
      }
      const host = this._root || this.shadowRoot;
      const count = this.nextRuns;
      if (!count) {
        if (this._nextBox) {
          this._nextBox.remove();
          this._nextBox = null;
        }
        return;
      }
      const dict = this.dict;
      if (!this._nextBox) {
        this._nextBox = document.createElement('div');
        this._nextBox.className = 'next';
        host.appendChild(this._nextBox);
      }
      const box = this._nextBox;
      box.innerHTML = '';

      const title = document.createElement('span');
      title.className = 'next-title';
      title.textContent = dict.nextRunsTitle;
      box.appendChild(title);

      const timeZone = this.timeZone || undefined;
      const runs = nextRuns(splitExpression(this.value, 5), { count: count, timeZone: timeZone });
      if (!runs.length) {
        const empty = document.createElement('p');
        empty.className = 'next-empty';
        empty.textContent = dict.noRuns;
        box.appendChild(empty);
        return;
      }

      const locale = this.locale;
      const format = this.runFormat;

      const list = document.createElement('ol');
      list.className = 'next-list';
      runs.forEach(function (run) {
        const item = document.createElement('li');
        item.textContent = formatRun(run, locale, timeZone, format);
        list.appendChild(item);
      });
      box.appendChild(list);
    }

    _syncUI() {
      this._fields.forEach(function (key) {
        this._paint(key);
      }, this);
      this._paintWarnings();
      this._paintNextRuns();
      if (this._trigger && document.activeElement !== this._trigger) {
        this._trigger.value = this.value;
      }
    }

    disconnectedCallback() {
      if (this._onDocumentDown) {
        document.removeEventListener('mousedown', this._onDocumentDown, true);
        this._onDocumentDown = null;
      }
    }
  }

  // Регистрируем элемент только в браузере: файл должен грузиться и в Node,
  // где нет DOM — иначе тесты форматтера не смогут его импортировать.
  if (typeof customElements !== 'undefined' && !customElements.get('cron-editor')) {
    customElements.define('cron-editor', CronEditor);
  }
  if (typeof window !== 'undefined') {
    window.CronEditor = CronEditor;
  }
  if (typeof module === 'object' && module.exports) {
    module.exports = {
      CronEditor: CronEditor,
      parseToken: parseToken,
      formatToken: formatToken,
      runsString: runsString,
      progressions: progressions,
      coverCandidate: coverCandidate,
      splitExpression: splitExpression,
      DAYS_IN_MONTH: DAYS_IN_MONTH,
      FIELDS: FIELDS,
      FIELD_ORDER: FIELD_ORDER,
      LOCALES: Object.keys(MESSAGES),
      MESSAGES: MESSAGES,
      nextRuns: nextRuns,
      formatRun: formatRun,
      normalizeRunFormat: normalizeRunFormat,
      RUN_FORMAT_TOKENS: RUN_FORMAT_TOKENS,
      zoneOffsetMs: zoneOffsetMs,
      intlNames: intlNames,
      intlWeekStart: intlWeekStart,
      resolveLang: resolveLang
    };
  }
})();

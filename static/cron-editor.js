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

  const MONTH_SHORT_RU = ['янв', 'фев', 'мар', 'апр', 'май', 'июн', 'июл', 'авг', 'сен', 'окт', 'ноя', 'дек'];
  const MONTH_LONG_RU = ['январь', 'февраль', 'март', 'апрель', 'май', 'июнь', 'июль', 'август', 'сентябрь', 'октябрь', 'ноябрь', 'декабрь'];
  const DOW_SHORT_RU = ['вс', 'пн', 'вт', 'ср', 'чт', 'пт', 'сб'];
  const DOW_LONG_RU = ['воскресенье', 'понедельник', 'вторник', 'среда', 'четверг', 'пятница', 'суббота'];

  const FIELDS = {
    min: { key: 'min', label: 'минута', min: 0, max: 59, cols: 12 },
    hour: { key: 'hour', label: 'час', min: 0, max: 23, cols: 6 },
    dom: { key: 'dom', label: 'день', min: 1, max: 31, cols: 8 },
    month: {
      key: 'month', label: 'месяц', min: 1, max: 12, cols: 4,
      short: MONTH_SHORT_RU,
      long: MONTH_LONG_RU
    },
    dow: {
      key: 'dow', label: 'день недели', min: 0, max: 6, cols: 4,
      short: DOW_SHORT_RU,
      long: DOW_LONG_RU
    }
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
    '.cell{box-sizing:border-box;width:var(--cron-cell-w,1.9rem);padding:.1rem 0;text-align:center;font-family:inherit;font-size:.78rem;line-height:1.35;color:var(--cron-cell-color,#ced4da);background:var(--cron-cell-bg,transparent);border:1px solid var(--cron-cell-border,#495057);border-radius:.25rem;cursor:pointer}',
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
    '.panel[hidden]{display:none}'
  ].join('\n');

  // В Node нет HTMLElement — берём пустую базу, чтобы файл импортировался ради
  // чистых функций (элемент в такой среде всё равно не создаётся).
  const HTMLElementBase = typeof HTMLElement !== 'undefined' ? HTMLElement : class {};

  class CronEditor extends HTMLElementBase {
    static parseToken(token, cfg) { return parseToken(token, cfg); }
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

    // weekStart — с какого дня рисовать неделю: 'sun' (по умолчанию) или 'mon'.
    // Порядок ячеек меняется, значения cron остаются прежними (0 = воскресенье).
    get weekStart() {
      return String(this.getAttribute('week-start') || 'sun').toLowerCase() === 'mon' ? 'mon' : 'sun';
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
        trigger.setAttribute('aria-label', 'cron-выражение');
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
      const label = document.createElement('span');
      label.className = 'label';
      label.textContent = cfg.label;
      const token = document.createElement('input');
      token.className = 'token';
      token.type = 'text';
      token.spellcheck = false;
      token.autocomplete = 'off';
      token.setAttribute('aria-label', cfg.label + ' — выражение');
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
      if (cfg.short) {
        const index = cfg.key === 'month' ? value - 1 : value;
        return cfg.short[index] || String(value);
      }
      return String(value);
    }

    _title(cfg, value) {
      if (cfg.long) {
        const index = cfg.key === 'month' ? value - 1 : value;
        return cfg.long[index] || String(value);
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
          return day + '-го числа не бывает ни в одном из выбранных месяцев — этот день не сработает никогда';
        }
        const missing = months.filter(function (month) { return day > limitOf(month); })
          .map(function (month) { return FIELDS.month.long[month - 1] || month; });
        return day + '-го числа не бывает в: ' + missing.join(', ') + ' — в этих месяцах день пропустится';
      });
      paint('month', monthMarks, function (month, cls) {
        const name = FIELDS.month.long[month - 1] || String(month);
        const limit = limitOf(month);
        const missing = days.filter(function (day) { return day > limit; });
        const list = missing.join(', ');
        if (cls === 'error') {
          return 'в месяце «' + name + '» не бывает ' + list + ' числа — расписание не сработает никогда';
        }
        return 'в месяце «' + name + '» не бывает ' + list + ' числа — эти дни пропустятся';
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

    _rebuild() {
      this.shadowRoot.innerHTML = '';
      this._blocks = {};
      this._built = false;
      this._build();
      this._syncUI();
    }

    _syncUI() {
      this._fields.forEach(function (key) {
        this._paint(key);
      }, this);
      this._paintWarnings();
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
      FIELD_ORDER: FIELD_ORDER
    };
  }
})();

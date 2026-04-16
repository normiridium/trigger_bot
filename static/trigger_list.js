function triggerApp(){
  return {
    handleImportSubmit,
    updateImportFileName,
    openNew,
    openTemplateNew,
    submitTemplateForm,
    submitTriggerForm,
    saveSettings,
    syncSwitch,
    wrapResponseSelection,
    insertResponseLink,
    insertTgEmojiSnippet,
    showTelegramHtmlHelp,
    insertTemplateTagFromPicker,
    cloneCurrentTrigger,
  };
}

let __triggerPageInitialized = false;
let triggerSortable = null;
const rowActionBusy = new Set();
let editLoadInFlight = false;
let reorderSaving = false;
let responseEditorReady = false;
let responseVariants = [{text: ''}];
let activeResponseVariant = 0;
const enumCache = {};
let templatesCache = [];
let templatesLoadInFlight = false;
let settingsLoadInFlight = false;
let settingsCache = {fields: [], values: {}};

async function initTriggerPage(){
  if(__triggerPageInitialized){ return; }
  __triggerPageInitialized = true;
  window.__trgModal = new bootstrap.Modal(document.getElementById('triggerModal'));
  applyTokenToForms();
  await loadEnums();
  await loadTemplateTags();
  await loadTemplates();
  await loadSettings();
  applyMatchTypeUI();
  bindMatchTextToggle();
  bindMiniToolbarFallback();
  ensureResponseEditor();
  const form = document.getElementById('trigger_form');
  if(form && !form.dataset.boundSubmit){
    form.addEventListener('submit', submitTriggerForm);
    form.dataset.boundSubmit = '1';
  }
  const tplForm = document.getElementById('template_form');
  if(tplForm && !tplForm.dataset.boundSubmit){
    tplForm.addEventListener('submit', submitTemplateForm);
    tplForm.dataset.boundSubmit = '1';
  }
  const settingsForm = document.getElementById('settings_form');
  if(settingsForm && !settingsForm.dataset.boundSubmit){
    settingsForm.addEventListener('submit', saveSettings);
    settingsForm.dataset.boundSubmit = '1';
  }
  const restartBtn = document.getElementById('settings_restart_btn');
  if(restartBtn && !restartBtn.dataset.boundClick){
    restartBtn.addEventListener('click', restartService);
    restartBtn.dataset.boundClick = '1';
  }
  const cloneBtn = document.getElementById('f_clone_btn');
  if(cloneBtn && !cloneBtn.dataset.boundClick){
    cloneBtn.addEventListener('click', () => cloneCurrentTrigger());
    cloneBtn.dataset.boundClick = '1';
  }
  renderVariantControls();
  await loadTriggerList();
  initTriggerDragAndDrop();
}

async function loadEnums(){
  try{
    const r = await fetch(withToken('/trigger_bot/enums'));
    if(!r.ok){
      return;
    }
    const data = await r.json();
    applyEnumOptions('f_trigger_mode', data.trigger_modes, 'all');
    applyEnumOptions('f_admin_mode', data.admin_modes, 'anybody');
    applyEnumOptions('f_match_type', data.match_types, 'full');
    applyEnumOptions('f_action_type', data.action_types, 'send');
  } catch(err){
    // Keep static options as fallback.
  }
}

async function loadTemplateTags(){
  const pickers = [
    document.getElementById('f_template_tag_picker'),
    document.getElementById('tpl_template_tag_picker'),
  ].filter(Boolean);
  if(pickers.length === 0){
    return;
  }
  try{
    const r = await fetch(withToken('/trigger_bot/template_tags'));
    if(!r.ok){
      return;
    }
    const data = await r.json();
    if(!data || !Array.isArray(data.items)){
      return;
    }
    pickers.forEach((picker) => {
      const prev = String(picker.value || '');
      picker.innerHTML = '<option value=\"\">Вставить тег сообщения…</option>';
      data.items.forEach(it => {
        const opt = document.createElement('option');
        opt.value = String(it && it.value != null ? it.value : '');
        opt.textContent = String(it && it.label != null ? it.label : opt.value);
        picker.appendChild(opt);
      });
      if(prev){
        picker.value = prev;
      }
    });
  } catch(err){
    // Keep static options as fallback.
  }
}

async function loadTemplates(){
  const tbody = document.getElementById('templates_tbody');
  const picker = document.getElementById('f_template_picker');
  if(!tbody){
    return;
  }
  try{
    if(templatesLoadInFlight){ return; }
    templatesLoadInFlight = true;
    const r = await fetch(withToken('/trigger_bot/templates'));
    if(!r.ok){
      renderTemplatesError('Не удалось загрузить шаблоны');
      return;
    }
    const data = await r.json();
    templatesCache = Array.isArray(data?.items) ? data.items : [];
    renderTemplatesTable();
    renderTemplatesPicker(picker);
  } catch(err){
    renderTemplatesError('Не удалось загрузить шаблоны');
  } finally {
    templatesLoadInFlight = false;
  }
}

function renderTemplatesError(msg){
  const tbody = document.getElementById('templates_tbody');
  if(!tbody){ return; }
  tbody.innerHTML = `<tr><td colspan="4" class="text-center text-danger py-4">${escapeHtml(msg)}</td></tr>`;
}

function renderTemplatesTable(){
  const tbody = document.getElementById('templates_tbody');
  if(!tbody){ return; }
  if(!Array.isArray(templatesCache) || templatesCache.length === 0){
    tbody.innerHTML = '<tr><td colspan="4" class="text-center text-secondary py-4">Список пуст</td></tr>';
    return;
  }
  tbody.innerHTML = '';
  templatesCache.forEach((tpl) => {
    const id = String(pick(tpl, 'id', 'ID', '') ?? '').trim();
    const key = String(tpl?.key ?? '').trim();
    const title = String(tpl?.title ?? '').trim();
    const text = String(tpl?.text ?? '').trim();
    const preview = clipTemplatePreview(text, 80);
    const tr = document.createElement('tr');
    const insertDisabled = key ? '' : 'disabled';
    tr.innerHTML = `
      <td class="col-key"><code>${escapeHtml(key || id)}</code></td>
      <td class="col-title">${escapeHtml(title || '—')}</td>
      <td class="text-wrap col-text" title="${escapeHtml(text)}">${escapeHtml(preview)}</td>
      <td class="text-nowrap col-action">
        <div class="template-actions d-flex gap-2 flex-wrap">
          <button type="button" class="btn btn-outline-info btn-sm" data-template-edit="${escapeHtml(id)}">
            <i class="bi bi-pencil"></i>
          </button>
          <button type="button" class="btn btn-outline-danger btn-sm" data-template-delete="${escapeHtml(id)}" data-template-key="${escapeHtml(key)}">
            <i class="bi bi-trash"></i>
          </button>
        </div>
      </td>`;
    tbody.appendChild(tr);
  });
}

function renderTemplatesPicker(picker){
  if(!picker){
    return;
  }
  const prev = String(picker.value || '');
  picker.innerHTML = '<option value="">Вставить шаблон…</option>';
  if(!Array.isArray(templatesCache) || templatesCache.length === 0){
    return;
  }
  templatesCache.forEach((tpl) => {
    const key = String(tpl?.key ?? '').trim();
    const title = String(tpl?.title ?? '').trim();
    if(!key){ return; }
    const opt = document.createElement('option');
    opt.value = key;
    opt.textContent = title ? `${key} — ${title}` : key;
    picker.appendChild(opt);
  });
  if(prev){
    picker.value = prev;
  }
}

async function loadSettings(){
  if(settingsLoadInFlight){ return; }
  settingsLoadInFlight = true;
  try{
    const r = await fetch(withToken('/trigger_bot/settings_get'));
    if(!r.ok){ throw new Error('HTTP ' + r.status); }
    const data = await r.json();
    settingsCache.fields = Array.isArray(data?.fields) ? data.fields : [];
    settingsCache.values = data && typeof data.values === 'object' && data.values ? data.values : {};
    renderSettingsForm();
  } catch(err){
    renderSettingsError('Не удалось загрузить настройки');
  } finally {
    settingsLoadInFlight = false;
  }
}

function renderSettingsError(msg){
  const wrap = document.getElementById('settings_fields');
  if(!wrap){ return; }
  wrap.innerHTML = `<div class="text-danger">${escapeHtml(msg)}</div>`;
}

function renderSettingsForm(){
  const wrap = document.getElementById('settings_fields');
  if(!wrap){ return; }
  const fields = settingsCache.fields || [];
  if(fields.length === 0){
    wrap.innerHTML = '<div class="text-secondary">Настройки не найдены.</div>';
    return;
  }
  wrap.innerHTML = '';
  fields.forEach((f) => {
    const key = String(f?.key || '');
    const label = String(f?.label || key);
    const type = String(f?.type || 'string');
    const options = Array.isArray(f?.options) ? f.options.map(v => String(v ?? '')).filter(Boolean) : [];
    const val = settingsCache.values && settingsCache.values[key] != null ? String(settingsCache.values[key]) : '';
    const row = document.createElement('div');
    row.className = 'd-flex align-items-center gap-2 settings-row';
    const labelEl = document.createElement('label');
    labelEl.className = 'settings-label';
    labelEl.textContent = label;
    labelEl.setAttribute('for', 'settings_' + key);
    row.appendChild(labelEl);
    if(type === 'bool'){
      const wrapSwitch = document.createElement('div');
      wrapSwitch.className = 'form-check form-switch m-0';
      const input = document.createElement('input');
      input.className = 'form-check-input';
      input.type = 'checkbox';
      input.id = 'settings_' + key;
      input.dataset.key = key;
      input.checked = normalizeBool(val);
      wrapSwitch.appendChild(input);
      row.appendChild(wrapSwitch);
    } else if (options.length > 0) {
      const select = document.createElement('select');
      select.className = 'form-select form-select-sm bg-black text-light border-secondary settings-input';
      select.id = 'settings_' + key;
      select.dataset.key = key;
      options.forEach((optVal) => {
        const opt = document.createElement('option');
        opt.value = optVal;
        opt.textContent = optVal;
        select.appendChild(opt);
      });
      if(options.includes(val)){
        select.value = val;
      } else if (val) {
        const custom = document.createElement('option');
        custom.value = val;
        custom.textContent = val + ' (custom)';
        select.appendChild(custom);
        select.value = val;
      }
      row.appendChild(select);
    } else {
      const input = document.createElement('input');
      input.className = 'form-control form-control-sm bg-black text-light border-secondary settings-input';
      input.type = 'text';
      input.id = 'settings_' + key;
      input.dataset.key = key;
      input.value = val;
      row.appendChild(input);
    }
    wrap.appendChild(row);
  });
}

function normalizeBool(v){
  return ['1','true','yes','on'].includes(String(v || '').toLowerCase());
}

async function saveSettings(ev){
  if(ev){ ev.preventDefault(); }
  const btn = document.getElementById('settings_save_btn');
  const hint = document.getElementById('settings_save_hint');
  lockButton(btn);
  const payload = {};
  const inputs = document.querySelectorAll('#settings_fields [data-key]');
  inputs.forEach((el) => {
    const key = el.dataset.key;
    if(!key){ return; }
    if(el.type === 'checkbox'){
      payload[key] = el.checked ? 'true' : 'false';
    } else {
      payload[key] = String(el.value || '');
    }
  });
  try{
    const r = await fetch(withToken('/trigger_bot/settings_save'), {
      method: 'POST',
      headers: {'Content-Type': 'application/json', 'Accept': 'application/json'},
      body: JSON.stringify(payload),
    });
    if(!r.ok){
      const txt = await r.text();
      alert('Ошибка сохранения: ' + (txt || r.status));
      return false;
    }
    const data = await r.json().catch(() => ({}));
    if(hint){
      hint.textContent = data && data.message ? data.message : 'Сохранено. Нужен перезапуск.';
    }
  } finally {
    unlockButton(btn);
  }
  return false;
}

async function restartService(){
  if(!confirm('Перезапустить сервис?')){ return; }
  const btn = document.getElementById('settings_restart_btn');
  lockButton(btn);
  try{
    const r = await fetch(withToken('/trigger_bot/restart'), {
      method: 'POST',
      headers: {'Content-Type': 'application/json', 'Accept': 'application/json'},
      body: JSON.stringify({ok: true}),
    });
    if(!r.ok){
      const txt = await r.text();
      alert('Ошибка перезапуска: ' + (txt || r.status));
      return;
    }
  } finally {
    unlockButton(btn);
  }
}

function openTemplateNew(){
  document.getElementById('tpl_id').value = '';
  document.getElementById('tpl_key').value = '';
  document.getElementById('tpl_title').value = '';
  document.getElementById('tpl_text').value = '';
  ensureTemplateModal().show();
}

async function openTemplateEdit(id){
  if(!id){ return; }
  const r = await fetch(withToken('/trigger_bot/template_get?id=' + encodeURIComponent(id)));
  if(!r.ok){
    alert('Не удалось загрузить шаблон: ' + r.status);
    return;
  }
  const t = await r.json();
  document.getElementById('tpl_id').value = t.id || '';
  document.getElementById('tpl_key').value = t.key || '';
  document.getElementById('tpl_title').value = t.title || '';
  document.getElementById('tpl_text').value = t.text || '';
  ensureTemplateModal().show();
}

async function submitTemplateForm(ev){
  ev.preventDefault();
  const form = ev.currentTarget;
  const payload = {
    id: Number(document.getElementById('tpl_id')?.value || 0),
    key: String(document.getElementById('tpl_key')?.value || ''),
    title: String(document.getElementById('tpl_title')?.value || ''),
    text: String(document.getElementById('tpl_text')?.value || ''),
  };
  const r = await fetch(withToken('/trigger_bot/template_save'), {
    method: 'POST',
    headers: {'Content-Type': 'application/json', 'Accept': 'application/json'},
    body: JSON.stringify(payload),
  });
  if(!r.ok){
    const txt = await r.text();
    alert('Ошибка сохранения шаблона: ' + (txt || r.status));
    return false;
  }
  ensureTemplateModal().hide();
  await loadTemplates();
  return false;
}

async function deleteTemplate(id, key){
  id = String(id || '').trim();
  key = String(key || '').trim();
  if(!id && !key){ return; }
  if(!confirm('Удалить шаблон?')){ return; }
  const payload = {};
  if(id){ payload.id = Number(id); }
  if(key){ payload.key = key; }
  const r = await fetch(withToken('/trigger_bot/template_delete'), {
    method: 'POST',
    headers: {'Content-Type': 'application/json', 'Accept': 'application/json'},
    body: JSON.stringify(payload),
  });
  if(!r.ok){
    if(r.status === 409){
      try{
        const data = await r.json();
        const list = Array.isArray(data?.triggers) ? data.triggers : [];
        const names = list.map(t => t && t.title ? t.title : t && t.id ? `#${t.id}` : '').filter(Boolean);
        const suffix = names.length ? `\\nИспользуется в: ${names.join(', ')}` : '';
        alert((data && data.message ? data.message : 'Шаблон используется в триггерах') + suffix);
        return;
      }catch(_){}
    }
    const txt = await r.text();
    alert('Ошибка удаления шаблона: ' + (txt || r.status));
    return;
  }
  await loadTemplates();
}

function ensureTemplateModal(){
  if(!window.__tplModal){
    window.__tplModal = new bootstrap.Modal(document.getElementById('templateModal'));
  }
  return window.__tplModal;
}

function bindMatchTextToggle(){
  const btn = document.getElementById('match_text_toggle');
  if(!btn || btn.dataset.bound){
    return;
  }
  btn.addEventListener('click', () => toggleMatchTextArea());
  btn.dataset.bound = '1';
}

function clipTemplatePreview(text, maxLen){
  const src = String(text ?? '');
  if(maxLen <= 0){
    return src;
  }
  if(src.length <= maxLen){
    return src;
  }
  return src.slice(0, maxLen).trimEnd() + '…';
}

function applyEnumOptions(id, items, fallback){
  const input = document.getElementById(id);
  const menu = document.getElementById(id + '_menu');
  const btn = document.getElementById(id + '_btn');
  if(!input || !menu || !btn || !Array.isArray(items) || items.length === 0){
    return;
  }
  const prev = String(input.value || fallback || '');
  enumCache[id] = items.map(it => ({
    value: String(it && it.value != null ? it.value : ''),
    label: String(it && it.label != null ? it.label : ''),
    icon: String(it && it.icon != null ? it.icon : ''),
  }));
  menu.innerHTML = '';
  enumCache[id].forEach(it => {
    const li = document.createElement('li');
    const btnItem = document.createElement('button');
    btnItem.type = 'button';
    btnItem.className = 'dropdown-item d-flex align-items-center gap-2';
    btnItem.dataset.value = it.value;
    btnItem.dataset.label = it.label;
    btnItem.dataset.icon = it.icon;
    if(it.icon){
      const icon = document.createElement('i');
      icon.className = `bi ${it.icon}`;
      btnItem.appendChild(icon);
    }
    const span = document.createElement('span');
    span.textContent = it.label || it.value;
    btnItem.appendChild(span);
    btnItem.addEventListener('click', () => setSel(id, it.value));
    li.appendChild(btnItem);
    menu.appendChild(li);
  });
  input.value = prev || (enumCache[id][0]?.value ?? '');
  setSel(id, input.value || fallback);
}

function setSel(id,val){
  const el=document.getElementById(id);
  if(el && val!=null){ el.value=String(val); }
  const btn = document.getElementById(id + '_btn');
  if(btn){
    const info = findEnumInfo(id, String(val ?? ''));
    const label = info?.label || String(val ?? '');
    const icon = info?.icon || '';
    btn.innerHTML = icon
      ? `<i class=\"bi ${escapeHtml(icon)} me-2\"></i>${escapeHtml(label)}`
      : escapeHtml(label);
  }
}

function findEnumInfo(id, value){
  const items = enumCache[id];
  if(!Array.isArray(items)){
    return null;
  }
  return items.find(it => it.value === value) || null;
}

function escapeHtml(v){
  return String(v ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function ensureResponseEditor(){
  if(responseEditorReady){ return; }
  if(typeof window.initResponseEditor !== 'function'){ return; }
  const onEditorReady = (editor) => {
    if(!editor || responseEditorReady){ return; }
    const ta = document.getElementById('f_response_text');
    if(ta){ ta.classList.add('d-none'); }
    responseEditorReady = true;
    if(responseVariants.length > 0){
      setResponseValue(responseVariants[activeResponseVariant]?.text || '');
    }
  };
  const editorOrPromise = window.initResponseEditor('f_response_text', 'response_editor');
  if(editorOrPromise && typeof editorOrPromise.then === 'function'){
    editorOrPromise.then(onEditorReady).catch(() => {});
    return;
  }
  onEditorReady(editorOrPromise);
}

function getEditorView(){
  if(typeof window.getResponseEditor === 'function'){
    return window.getResponseEditor();
  }
  return null;
}

function setResponseValue(val){
  const editor = getEditorView();
  const text = String(val ?? '');
  if(editor){
    const docLen = editor.state.doc.length;
    editor.dispatch({changes: {from: 0, to: docLen, insert: text}});
    return;
  }
  const ta = document.getElementById('f_response_text');
  if(ta){ ta.value = text; }
}

function getResponseValue(){
  const editor = getEditorView();
  if(editor){ return editor.state.doc.toString(); }
  const ta = document.getElementById('f_response_text');
  return ta ? ta.value : '';
}

function normalizeResponseItems(raw){
  if(raw == null){ return [{text: ''}]; }
  if(Array.isArray(raw)){
    const out = [];
    raw.forEach((it)=>{
      if(it == null){ return; }
      if(typeof it === 'string'){
        out.push({text: it});
        return;
      }
      if(typeof it === 'object'){
        if(Object.prototype.hasOwnProperty.call(it, 'text')){
          out.push({text: String(it.text ?? '')});
          return;
        }
        if(Object.prototype.hasOwnProperty.call(it, 'Text')){
          out.push({text: String(it.Text ?? '')});
          return;
        }
      }
    });
    return out.length > 0 ? out : [{text: ''}];
  }
  if(typeof raw === 'string'){
    return [{text: raw}];
  }
  return [{text: ''}];
}

function commitActiveVariant(){
  if(!responseVariants.length){ responseVariants = [{text: ''}]; }
  if(activeResponseVariant < 0){ activeResponseVariant = 0; }
  if(activeResponseVariant >= responseVariants.length){
    activeResponseVariant = responseVariants.length - 1;
  }
  if(!responseVariants[activeResponseVariant]){
    responseVariants[activeResponseVariant] = {text: ''};
  }
  responseVariants[activeResponseVariant].text = getResponseValue();
}

function setActiveVariant(idx){
  commitActiveVariant();
  const next = Number(idx);
  if(!Number.isFinite(next)){ return; }
  if(next < 0 || next >= responseVariants.length){ return; }
  activeResponseVariant = next;
  setResponseValue(responseVariants[activeResponseVariant].text || '');
  renderVariantControls();
}

function addResponseVariant(){
  commitActiveVariant();
  responseVariants.push({text: ''});
  activeResponseVariant = responseVariants.length - 1;
  setResponseValue('');
  renderVariantControls();
}

function removeResponseVariant(){
  if(responseVariants.length <= 1){ return; }
  commitActiveVariant();
  responseVariants.pop();
  if(activeResponseVariant >= responseVariants.length){
    activeResponseVariant = responseVariants.length - 1;
  }
  setResponseValue(responseVariants[activeResponseVariant].text || '');
  renderVariantControls();
}

function renderVariantControls(){
  const wrap = document.getElementById('response_variant_controls');
  if(!wrap){ return; }
  wrap.innerHTML = '';
  const label = document.createElement('span');
  label.className = 'response-variant-label';
  label.textContent = 'Варианты ответа:';
  wrap.appendChild(label);
  const total = responseVariants.length || 1;
  for(let i = 0; i < total; i++){
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn btn-outline-secondary btn-sm';
    if(i === activeResponseVariant){
      btn.classList.add('btn-outline-success');
    }
    btn.textContent = String(i + 1);
    btn.title = 'Вариант ' + String(i + 1);
    btn.addEventListener('click', () => setActiveVariant(i));
    wrap.appendChild(btn);
  }
  const plus = document.createElement('button');
  plus.type = 'button';
  plus.className = 'btn btn-outline-info btn-sm';
  plus.textContent = '+';
  plus.title = 'Добавить вариант';
  plus.addEventListener('click', addResponseVariant);
  wrap.appendChild(plus);

  const minus = document.createElement('button');
  minus.type = 'button';
  minus.className = 'btn btn-outline-danger btn-sm';
  minus.textContent = '-';
  minus.disabled = total <= 1;
  minus.title = 'Удалить последний вариант';
  minus.addEventListener('click', removeResponseVariant);
  wrap.appendChild(minus);
}

function setResponseVariantsFromRaw(raw){
  responseVariants = normalizeResponseItems(raw);
  activeResponseVariant = 0;
  setResponseValue(responseVariants[0].text || '');
  renderVariantControls();
}

function syncResponseToTextarea(){
  commitActiveVariant();
  const ta = document.getElementById('f_response_text');
  if(!ta){ return; }
  const payload = responseVariants.map((it) => ({text: String(it.text ?? '')}));
  ta.value = JSON.stringify(payload);
}

function formatRegexBenchMS(us){
  const n = Number(us || 0);
  if(!Number.isFinite(n) || n <= 0){ return '—'; }
  return (n / 1000).toFixed(2) + ' ms';
}

function statusIcon(enabled){
  return enabled ? 'bi-eye' : 'bi-eye-slash';
}

function normalizeMatchType(v){
  return String(v || '').toLowerCase();
}

function triggerRowHTML(t){
  const id = Number(t.id || t.ID || 0);
  const title = escapeHtml(t.title ?? t.Title ?? '');
  const matchText = escapeHtml(t.match_text ?? t.MatchText ?? '');
  const matchType = normalizeMatchType(t.match_type ?? t.MatchType ?? 'full');
  const actionType = escapeHtml(t.action_type ?? t.ActionType ?? 'send');
  const adminModeRaw = String(t.admin_mode ?? t.AdminMode ?? '');
  const adminMode = escapeHtml(adminModeRaw);
  const enabled = !!(t.enabled ?? t.Enabled);
  const regexError = String(t.regex_error ?? t.RegexError ?? '').trim();
  const regexBenchUS = Number(t.regex_bench_us ?? t.RegexBenchUS ?? 0);

  const regexBench = matchType === 'regex'
    ? `<span class="regex-bench">${escapeHtml(formatRegexBenchMS(regexBenchUS))}</span> `
    : '';

  const regexWarn = regexError
    ? `<i class="bi bi-exclamation-triangle-fill text-danger mx-1" title="${escapeHtml(regexError)}"></i>`
    : '';

  const toggleClass = enabled ? 'btn-outline-success' : 'btn-outline-secondary';

  return `
<tr data-id="${id}">
  <td class="text-center"><i class="bi bi-grip-vertical drag-handle" title="Перетащить для изменения приоритета"></i></td>
  <td>${title}</td>
  <td>
    <code>${matchText}</code>
    ${regexWarn}
    <small>${regexBench}(${escapeHtml(matchType)})</small>
  </td>
  <td><small>${adminModeRaw === 'anybody' ? actionType : `${actionType} / ${adminMode}`}</small></td>
  <td class="text-nowrap">
    <button class="btn btn-sm action-mini action-visibility ${toggleClass}" type="button" data-id="${id}" title="Переключить статус" aria-label="Переключить статус" onclick="handleToggleClick(event, this)">
      <i class="bi ${statusIcon(enabled)}"></i>
    </button>
    <button class="btn btn-sm action-mini btn-outline-info ms-1" type="button" onclick="openEdit(${id}, this)" title="Редактировать" aria-label="Редактировать">
      <i class="bi bi-pencil"></i>
    </button>
    <button class="btn btn-sm action-mini btn-outline-danger ms-1" type="button" data-trigger-delete="${id}" title="Удалить" aria-label="Удалить">
      <i class="bi bi-trash"></i>
    </button>
  </td>
</tr>`;
}

function renderTriggerRows(items){
  const body = document.getElementById('triggers_tbody');
  if(!body){ return; }
  if(!Array.isArray(items) || items.length === 0){
    body.innerHTML = '<tr><td colspan="5" class="text-secondary text-center py-4">Триггеров пока нет</td></tr>';
    return;
  }
  body.innerHTML = items.map(triggerRowHTML).join('');
}

async function loadTriggerList(){
  const body = document.getElementById('triggers_tbody');
  if(body){
    body.innerHTML = '<tr><td colspan="5" class="text-secondary text-center py-4"><span class="spinner-border spinner-border-sm me-2"></span>Загрузка…</td></tr>';
  }
  try {
    const r = await fetch(withToken('/trigger_bot/list'), {credentials: 'same-origin'});
    if(!r.ok){ throw new Error('HTTP ' + r.status); }
    const payload = await r.json();
    renderTriggerRows(payload && payload.items ? payload.items : []);
  } catch (err) {
    if(body){
      body.innerHTML = `<tr><td colspan="5" class="text-danger text-center py-4">Не удалось загрузить список: ${escapeHtml(err && err.message ? err.message : String(err))}</td></tr>`;
    }
  }
}

function updateImportFileName(input){
  const outId = input && input.dataset ? input.dataset.output : '';
  const out = document.getElementById(outId || 'import_file_name');
  if(!out){ return; }
  const f = input && input.files && input.files[0];
  out.textContent = f ? f.name : 'Файл не выбран';
}

function lockButton(btn){
  if(!btn){ return null; }
  if(btn.dataset.locked === '1'){ return null; }
  btn.dataset.locked = '1';
  btn.disabled = true;
  btn.dataset.prevHtml = btn.innerHTML;
  btn.innerHTML = '<span class="spinner-border spinner-border-sm"></span>';
  return btn;
}

function unlockButton(btn){
  if(!btn){ return; }
  btn.disabled = false;
  if(btn.dataset.prevHtml){ btn.innerHTML = btn.dataset.prevHtml; }
  delete btn.dataset.prevHtml;
  delete btn.dataset.locked;
}

function triggerIdFromForm(form){
  const idInput = form && form.querySelector('input[name="id"]');
  return Number(idInput ? idInput.value : 0);
}

function handleRowFormSubmit(event, form){
  const id = triggerIdFromForm(form);
  if(id > 0 && rowActionBusy.has(id)){
    if(event){ event.preventDefault(); }
    return false;
  }
  const btn = form ? form.querySelector('button[type="submit"]') : null;
  lockButton(btn);
  if(id > 0){ rowActionBusy.add(id); }
  return true;
}

async function handleToggleClick(event, btn){
  if(event){ event.preventDefault(); }
  const id = Number(btn && btn.getAttribute('data-id') ? btn.getAttribute('data-id') : 0);
  if(id > 0 && rowActionBusy.has(id)){ return false; }
  lockButton(btn);
  if(id > 0){ rowActionBusy.add(id); }
  try{
    const url = withToken('/trigger_bot/toggle');
    const res = await fetch(url, {
      method: 'POST',
      headers: {
        'Accept': 'application/json',
        'Content-Type': 'application/json'
      },
      body: JSON.stringify({id}),
      credentials: 'same-origin'
    });
    if(!res.ok){
      const txt = await res.text();
      alert('Не удалось переключить: ' + (txt || res.status));
      return;
    }
    let nextEnabled = null;
    try {
      const data = await res.json();
      if(data && typeof data.enabled === 'boolean'){
        nextEnabled = data.enabled;
      }
    } catch (_) {}
    const icon = btn ? btn.querySelector('i') : null;
    const isEnabled = icon
      ? (icon.classList.contains('bi-eye') || icon.classList.contains('bi-eye-fill'))
      : false;
    if(nextEnabled === null){
      nextEnabled = !isEnabled;
    }
    if(btn){
      btn.classList.toggle('btn-outline-success', nextEnabled);
      btn.classList.toggle('btn-outline-secondary', !nextEnabled);
    }
    if(icon){
      icon.classList.remove('bi-eye-fill', 'bi-eye-slash-fill', 'bi-eye', 'bi-eye-slash');
      icon.classList.add(nextEnabled ? 'bi-eye' : 'bi-eye-slash');
    }
  } catch(err){
    alert('Не удалось переключить: ' + (err && err.message ? err.message : err));
    return;
  } finally {
    unlockButton(btn);
    if(id > 0){ rowActionBusy.delete(id); }
  }
}

function handleDeleteSubmit(event, form){
  if(event){ event.preventDefault(); }
  return false;
}

async function deleteTrigger(id, btn){
  id = Number(id || 0);
  if(id <= 0){ return; }
  if(!confirm('Удалить?')){ return; }
  const locked = lockButton(btn);
  try{
    const res = await fetch(withToken('/trigger_bot/delete'), {
      method: 'POST',
      headers: {'Content-Type': 'application/json', 'Accept': 'application/json'},
      body: JSON.stringify({id}),
    });
    if(!res.ok){
      const txt = await res.text();
      alert('Ошибка удаления: ' + (txt || res.status));
      return;
    }
    await loadTriggerList();
  } finally {
    unlockButton(locked);
  }
}

function handleImportSubmit(event, form){
  if(event){ event.preventDefault(); }
  const btnId = form && form.dataset ? form.dataset.submitBtn : '';
  const btn = document.getElementById(btnId || 'import_submit_btn');
  if(btn && btn.dataset.locked === '1'){
    return false;
  }
  const input = form ? form.querySelector('input[type=\"file\"]') : null;
  const file = input && input.files ? input.files[0] : null;
  if(!file){
    alert('Выберите файл для импорта');
    return false;
  }
  lockButton(btn);
  file.text().then((raw) => {
    return fetch(form.getAttribute('action') || withToken('/trigger_bot/import'), {
      method: 'POST',
      headers: {'Content-Type': 'application/json', 'Accept': 'application/json'},
      body: JSON.stringify({raw}),
    });
  }).then(async (res) => {
    if(!res.ok){
      const txt = await res.text();
      throw new Error(txt || res.status);
    }
    window.location.href = withToken('/trigger_bot');
  }).catch((err) => {
    alert('Ошибка импорта: ' + (err && err.message ? err.message : err));
  }).finally(() => {
    unlockButton(btn);
  });
  return false;
}

function setSaveBusy(busy){
  const saveBtn = document.getElementById('f_save_btn');
  const cloneBtn = document.getElementById('f_clone_btn');
  if(saveBtn){
    saveBtn.disabled = !!busy;
    saveBtn.innerHTML = busy ? '<span class="spinner-border spinner-border-sm me-1"></span>Сохраняю...' : '<i class="bi bi-floppy"></i> Сохранить';
  }
  if(cloneBtn){ cloneBtn.disabled = !!busy; }
}

async function submitTriggerForm(event){
  if(event){ event.preventDefault(); }
  const form = document.getElementById('trigger_form');
  if(!form){ return false; }
  commitActiveVariant();
  const prevResponseText = getResponseValue();
  syncResponseToTextarea();
  syncSwitch('f_enabled');
  syncSwitch('f_case_sensitive');
  syncSwitch('f_reply');
  syncSwitch('f_preview');
  syncSwitch('f_delete_source');

  const idEl = document.getElementById('f_id');
  const isNew = !idEl || !String(idEl.value || '').trim();
  const enabledEl = document.getElementById('f_enabled');
  const replyEl = document.getElementById('f_reply');
  if(isNew && enabledEl && !String(enabledEl.value || '').trim()){ enabledEl.value = '1'; }
  if(isNew && replyEl && !String(replyEl.value || '').trim()){ replyEl.value = '1'; }
  const inp = document.getElementById('f_match_text');
  if(inp){ inp.value = getMatchTextValue(); }

  setSaveBusy(true);
  try{
    const controller = new AbortController();
    const timeout = setTimeout(()=>controller.abort(), 15000);
    const payload = {
      id: Number(document.getElementById('f_id')?.value || 0),
      uid: String(document.getElementById('f_uid')?.value || ''),
      title: String(document.getElementById('f_title')?.value || ''),
      trigger_mode: String(document.getElementById('f_trigger_mode')?.value || 'all'),
      admin_mode: String(document.getElementById('f_admin_mode')?.value || 'anybody'),
      match_text: String(document.getElementById('f_match_text')?.value || ''),
      match_type: String(document.getElementById('f_match_type')?.value || 'full'),
      action_type: String(document.getElementById('f_action_type')?.value || 'send'),
      chance: Number(document.getElementById('f_chance')?.value || 100),
      enabled: document.getElementById('f_enabled')?.value === '1',
      case_sensitive: document.getElementById('f_case_sensitive')?.value === '1',
      reply: document.getElementById('f_reply')?.value === '1',
      preview: document.getElementById('f_preview')?.value === '1',
      delete_source: document.getElementById('f_delete_source')?.value === '1',
      response_text: responseVariants.map((it) => ({text: String(it.text ?? '')})),
    };
    const res = await fetch(form.getAttribute('action') || withToken('/trigger_bot/save'), {
      method: 'POST',
      headers: {'Content-Type': 'application/json', 'Accept': 'application/json'},
      body: JSON.stringify(payload),
      signal: controller.signal,
      credentials: 'same-origin'
    });
    clearTimeout(timeout);
    if(!res.ok){
      const txt = await res.text();
      alert('Ошибка сохранения: ' + (txt || res.status));
      setResponseValue(prevResponseText);
      setSaveBusy(false);
      return false;
    }
    window.location.href = withToken('/trigger_bot');
  }catch(err){
    alert('Сохранение не удалось: ' + (err && err.message ? err.message : err));
    setResponseValue(prevResponseText);
    setSaveBusy(false);
  }
  return false;
}

function getResponseTextArea(){
  return document.getElementById('f_response_text');
}

function getTemplateTextArea(){
  return document.getElementById('tpl_text');
}

function replaceTextAreaSelection(el, before, after){
  const editor = getEditorView();
  if(editor){
    const sel = editor.state.selection.main;
    const selected = editor.state.sliceDoc(sel.from, sel.to) || 'текст';
    const insert = String(before) + selected + String(after);
    editor.dispatch({
      changes: {from: sel.from, to: sel.to, insert},
      selection: {anchor: sel.from + insert.length}
    });
    editor.focus();
    return;
  }
  if(!el){ return; }
  const start = el.selectionStart ?? 0;
  const end = el.selectionEnd ?? 0;
  const val = el.value ?? '';
  const selected = val.slice(start, end) || 'текст';
  const next = val.slice(0, start) + before + selected + after + val.slice(end);
  el.value = next;
  const caret = start + before.length + selected.length + after.length;
  el.focus();
  el.setSelectionRange(caret, caret);
}

function insertTextAtCursor(el, text){
  const editor = getEditorView();
  if(editor && el && el.id === 'f_response_text'){
    const sel = editor.state.selection.main;
    editor.dispatch({
      changes: {from: sel.from, to: sel.to, insert: text},
      selection: {anchor: sel.from + String(text).length}
    });
    editor.focus();
    return;
  }
  if(!el){ return; }
  const start = el.selectionStart ?? 0;
  const end = el.selectionEnd ?? 0;
  const val = el.value ?? '';
  el.value = val.slice(0, start) + text + val.slice(end);
  const caret = start + text.length;
  el.focus();
  el.setSelectionRange(caret, caret);
}

function wrapResponseSelection(before, after){
  replaceTextAreaSelection(getResponseTextArea(), before, after);
}

function insertResponseLink(target){
  const url = prompt('URL ссылки', 'https://');
  if(!url){ return; }
  const el = target || getResponseTextArea();
  if(!el){ return; }
  const start = el.selectionStart ?? 0;
  const end = el.selectionEnd ?? 0;
  const val = el.value ?? '';
  const selected = val.slice(start, end) || '\u2060';
  const linked = `<a href="${url}">${selected}</a>`;
  el.value = val.slice(0, start) + linked + val.slice(end);
  const caret = start + linked.length;
  el.focus();
  el.setSelectionRange(caret, caret);
}

function insertTgEmojiSnippet(target){
  const id = prompt('ID кастомного emoji (из Telegram)', '12345');
  if(!id){ return; }
  const safeId = String(id).trim();
  if(!safeId){ return; }
  insertTextAtCursor(target || getResponseTextArea(), `<tg-emoji emoji-id="${safeId}">🙂</tg-emoji>`);
}

function showTelegramHtmlHelp(){
  alert(
    'Поддерживаемые HTML-теги Telegram:\n' +
    '<b> <strong>, <i> <em>, <u> <ins>, <s> <strike> <del>,\n' +
    '<code>, <pre>, <blockquote>, <a href="...">, <tg-spoiler>,\n' +
    '<tg-emoji emoji-id="...">🙂</tg-emoji>\n\n' +
    'Важно: не все обычные HTML-теги поддерживаются Telegram.'
  );
}

function insertTemplateTagFromPicker(sel){
  if(!sel || !sel.value){ return; }
  const isTemplate = sel.id === 'tpl_template_tag_picker';
  insertTextAtCursor(isTemplate ? getTemplateTextArea() : getResponseTextArea(), sel.value);
  sel.value = '';
}

function bindMiniToolbarFallback(){
  if(document.body.dataset.toolbarBound === '1'){ return; }
  document.body.dataset.toolbarBound = '1';
  document.addEventListener('click', (e) => {
    const btn = e.target && e.target.closest ? e.target.closest('button[data-action]') : null;
    if(!btn){ return; }
    const target = btn.closest('.mini-toolbar')?.getAttribute('data-target') || '';
    const targetEl = target === 'template' ? getTemplateTextArea() : getResponseTextArea();
    const action = btn.getAttribute('data-action');
    if(action === 'wrap'){
      const before = btn.getAttribute('data-before') || '';
      const after = btn.getAttribute('data-after') || '';
      replaceTextAreaSelection(targetEl, before, after);
      return;
    }
    if(action === 'link'){
      insertResponseLink(targetEl);
      return;
    }
    if(action === 'emoji'){
      insertTgEmojiSnippet(targetEl);
      return;
    }
    if(action === 'help'){
      showTelegramHtmlHelp();
      return;
    }
  });


  document.addEventListener('click', (e) => {
    const btn = e.target && e.target.closest ? e.target.closest('button[data-template-edit]') : null;
    if(!btn){ return; }
    const id = String(btn.getAttribute('data-template-edit') || '');
    if(!id){ return; }
    openTemplateEdit(id);
  });

  document.addEventListener('click', (e) => {
    const btn = e.target && e.target.closest ? e.target.closest('button[data-template-delete]') : null;
    if(!btn){ return; }
    const id = String(btn.getAttribute('data-template-delete') || '');
    const key = String(btn.getAttribute('data-template-key') || '');
    if(!id && !key){ return; }
    deleteTemplate(id, key);
  });

  document.addEventListener('click', (e) => {
    const btn = e.target && e.target.closest ? e.target.closest('button[data-trigger-delete]') : null;
    if(!btn){ return; }
    const id = Number(btn.getAttribute('data-trigger-delete') || 0);
    if(!id){ return; }
    deleteTrigger(id, btn);
  });

  document.addEventListener('change', (ev) => {
    const target = ev.target;
    if(!target || target.id !== 'f_template_tag_picker'){ return; }
    insertTemplateTagFromPicker(target);
  });

  document.addEventListener('change', (ev) => {
    const target = ev.target;
    if(!target || target.id !== 'f_template_picker'){ return; }
    if(!target.value){ return; }
    insertTextAtCursor(getResponseTextArea(), `{{template \"${String(target.value)}\"}}`);
    target.value = '';
  });
}

function applyMatchTypeUI(){
  const mt = document.getElementById('f_match_type');
  const lbl = document.getElementById('f_match_text_label');
  const inp = document.getElementById('f_match_text');
  const area = document.getElementById('f_match_text_area');
  const toggle = document.querySelector('.match-text-toggle');
  const cs = document.getElementById('f_case_sensitive_switch');
  const csHidden = document.getElementById('f_case_sensitive');
  if(!mt || !lbl || !inp){ return; }
  if(mt.value === 'idle'){
    lbl.textContent = 'Время простоя (мин)';
    inp.type = 'number';
    inp.min = '1';
    inp.step = '1';
    inp.placeholder = 'например, 120';
    if(area){ area.classList.add('d-none'); }
    if(toggle){ toggle.disabled = true; }
    if(cs){ cs.disabled = true; cs.checked = false; }
    if(csHidden){ csHidden.value = '0'; }
    inp.disabled = false;
  } else if(mt.value === 'new_member'){
    lbl.textContent = 'Текст триггера';
    inp.type = 'text';
    inp.removeAttribute('min');
    inp.removeAttribute('step');
    inp.placeholder = 'не используется';
    if(area){ area.classList.add('d-none'); }
    if(toggle){ toggle.disabled = true; }
    if(cs){ cs.disabled = true; cs.checked = false; }
    if(csHidden){ csHidden.value = '0'; }
    inp.disabled = true;
  } else {
    lbl.textContent = 'Текст триггера';
    inp.type = 'text';
    inp.removeAttribute('min');
    inp.removeAttribute('step');
    inp.placeholder = 'Текст триггера';
    if(toggle){ toggle.disabled = false; }
    if(cs){ cs.disabled = false; }
    inp.disabled = false;
  }
}

function toggleMatchTextArea(){
  const inp = document.getElementById('f_match_text');
  const area = document.getElementById('f_match_text_area');
  const textCol = document.getElementById('match_text_col');
  const typeCol = document.getElementById('match_type_col');
  if(!inp || !area){ return; }
  if(area.classList.contains('d-none')){
    area.value = inp.value || '';
    area.classList.remove('d-none');
    inp.classList.add('d-none');
    area.name = 'match_text';
    inp.removeAttribute('name');
    area.focus();
    if(textCol){
      textCol.classList.remove('col-md-6');
      textCol.classList.add('col-md-12');
    }
    if(typeCol){
      typeCol.classList.remove('col-md-6');
      typeCol.classList.add('col-md-12');
    }
  } else {
    inp.value = area.value || '';
    inp.classList.remove('d-none');
    area.classList.add('d-none');
    inp.name = 'match_text';
    area.removeAttribute('name');
    inp.focus();
    if(textCol){
      textCol.classList.remove('col-md-12');
      textCol.classList.add('col-md-6');
    }
    if(typeCol){
      typeCol.classList.remove('col-md-12');
      typeCol.classList.add('col-md-6');
    }
  }
}

function getMatchTextValue(){
  const inp = document.getElementById('f_match_text');
  const area = document.getElementById('f_match_text_area');
  if(area && !area.classList.contains('d-none')){ return area.value || ''; }
  return inp ? inp.value || '' : '';
}

function setMatchTextValue(val){
  const inp = document.getElementById('f_match_text');
  const area = document.getElementById('f_match_text_area');
  if(area && !area.classList.contains('d-none')){
    area.value = val || '';
    return;
  }
  if(inp){ inp.value = val || ''; }
}

function syncSwitch(id){
  const hidden = document.getElementById(id);
  const sw = document.getElementById(id + '_switch');
  if(!hidden || !sw) return;
  hidden.value = sw.checked ? '1' : '0';
}

function setBool(id,v){
  const hidden = document.getElementById(id);
  const sw = document.getElementById(id + '_switch');
  if(hidden && sw){
    sw.checked = !!v;
    hidden.value = sw.checked ? '1' : '0';
    return;
  }
  setSel(id, v ? '1' : '0');
}

function openModal(){ window.__trgModal.show(); }
function closeModal(){ window.__trgModal.hide(); }

function pick(o, a, b, d){
  if(o && o[a] !== undefined && o[a] !== null) return o[a];
  if(o && b && o[b] !== undefined && o[b] !== null) return o[b];
  return d;
}

function withToken(path){
  const token = new URLSearchParams(window.location.search).get('token');
  if(!token){ return path; }
  const u = new URL(path, window.location.origin);
  u.searchParams.set('token', token);
  return u.pathname + u.search;
}

function applyTokenToForms(){
  document.querySelectorAll('form[action]').forEach((f)=>{
    const action = f.getAttribute('action') || '';
    if(action.startsWith('/')){ f.setAttribute('action', withToken(action)); }
  });
  const ex = document.getElementById('export_link');
  if(ex){ ex.setAttribute('href', withToken('/trigger_bot/export')); }
}

async function persistTriggerOrder(){
  if(reorderSaving){ return; }
  reorderSaving = true;
  try{
    const body = document.getElementById('triggers_tbody');
    if(!body){ return; }
    const ids = Array.from(body.querySelectorAll('tr[data-id]'))
      .map((tr)=>Number(tr.getAttribute('data-id')))
      .filter((id)=>Number.isFinite(id) && id > 0);
    if(ids.length === 0){ return; }
    const res = await fetch(withToken('/trigger_bot/reorder'), {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      credentials: 'same-origin',
      body: JSON.stringify({ids})
    });
    if(!res.ok){
      const txt = await res.text();
      throw new Error(txt || ('HTTP '+res.status));
    }
  } finally {
    reorderSaving = false;
  }
}

function initTriggerDragAndDrop(){
  const body = document.getElementById('triggers_tbody');
  if(!body || typeof Sortable === 'undefined'){ return; }
  if(triggerSortable){
    triggerSortable.destroy();
    triggerSortable = null;
  }
  triggerSortable = new Sortable(body, {
    handle: '.drag-handle',
    animation: 140,
    ghostClass: 'dragging-row',
    onEnd: async function(){
      try{
        await persistTriggerOrder();
      }catch(err){
        alert('Не удалось сохранить порядок: ' + (err && err.message ? err.message : err));
        window.location.reload();
      }
    }
  });
}

function fillForm(t){
  document.getElementById('f_id').value=pick(t,'id','ID','');
  document.getElementById('f_uid').value=pick(t,'uid','UID','');
  document.getElementById('f_title').value=pick(t,'title','Title','');
  setResponseVariantsFromRaw(pick(t,'response_text','ResponseText',''));
  document.getElementById('f_chance').value=pick(t,'chance','Chance',100);
  setSel('f_trigger_mode', pick(t,'trigger_mode','TriggerMode','all'));
  setSel('f_admin_mode', pick(t,'admin_mode','AdminMode','anybody'));
  setSel('f_match_type', pick(t,'match_type','MatchType','full'));
  applyMatchTypeUI();
  setMatchTextValue(pick(t,'match_text','MatchText',''));
  setSel('f_action_type', pick(t,'action_type','ActionType','send'));
  setBool('f_enabled', !!pick(t,'enabled','Enabled',true));
  setBool('f_case_sensitive', !!pick(t,'case_sensitive','CaseSensitive',false));
  setBool('f_reply', !!pick(t,'reply','Reply',true));
  setBool('f_preview', !!pick(t,'preview','Preview',false));
  setBool('f_delete_source', !!pick(t,'delete_source','DeleteSource',false));
}

async function openNew(){
  fillForm({
    id: '',
    uid: '',
    title: '',
    enabled: true,
    trigger_mode: 'all',
    admin_mode: 'anybody',
    match_text: '',
    match_type: 'full',
    case_sensitive: false,
    action_type: 'send',
    response_text: '',
    reply: true,
    preview: false,
    delete_source: false,
    chance: 100
  });
  ensureResponseEditor();
  openModal();
}

async function openEdit(id, btn){
  if(editLoadInFlight){ return; }
  editLoadInFlight = true;
  const lockedBtn = lockButton(btn);
  try{
    const r=await fetch(withToken('/trigger_bot/get?id='+encodeURIComponent(id)));
    if(!r.ok){
      alert('Не удалось загрузить триггер: ' + r.status);
      return;
    }
    const t=await r.json();
    fillForm(t);
    ensureResponseEditor();
    openModal();
  } catch(err){
    alert('Не удалось загрузить триггер: ' + (err && err.message ? err.message : err));
  } finally {
    editLoadInFlight = false;
    unlockButton(lockedBtn);
  }
}

function cloneCurrentTrigger(){
  const titleEl = document.getElementById('f_title');
  const idEl = document.getElementById('f_id');
  const uidEl = document.getElementById('f_uid');
  if(idEl){ idEl.value = ''; }
  if(uidEl){ uidEl.value = ''; }
  if(titleEl){
    const title = String(titleEl.value || '').trim();
    titleEl.value = title ? `${title} (копия)` : 'Новый триггер (копия)';
  }
}

document.getElementById('f_match_type')?.addEventListener('change', applyMatchTypeUI);
window.addEventListener('codemirror-ready', () => {
  ensureResponseEditor();
});
if(!window.Alpine){
  initTriggerPage();
}

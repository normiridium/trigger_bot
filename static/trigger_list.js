function triggerApp(){
  return {
    handleImportSubmit,
    updateImportFileName,
    openNew,
    submitTriggerForm,
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

async function initTriggerPage(){
  if(__triggerPageInitialized){ return; }
  __triggerPageInitialized = true;
  window.__trgModal = new bootstrap.Modal(document.getElementById('triggerModal'));
  applyTokenToForms();
  applyMatchTypeUI();
  bindMiniToolbarFallback();
  ensureResponseEditor();
  await loadTriggerList();
  initTriggerDragAndDrop();
}

function setSel(id,val){
  const el=document.getElementById(id);
  if(el && val!=null){ el.value=String(val); }
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
  const editor = window.initResponseEditor('f_response_text', 'response_editor');
  if(editor){
    const ta = document.getElementById('f_response_text');
    if(ta){ ta.classList.add('d-none'); }
    responseEditorReady = true;
  }
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

function syncResponseToTextarea(){
  const ta = document.getElementById('f_response_text');
  if(ta){ ta.value = getResponseValue(); }
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
    <form class="d-inline" method="post" action="${withToken('/trigger_bot/toggle')}" onsubmit="handleToggleSubmit(event, this); return false;">
      <input type="hidden" name="id" value="${id}">
      <button class="btn btn-sm action-mini ${toggleClass}" type="submit" title="Переключить статус" aria-label="Переключить статус">
        <i class="bi ${statusIcon(enabled)}"></i>
      </button>
    </form>
    <button class="btn btn-sm action-mini btn-outline-primary ms-1" type="button" onclick="openEdit(${id}, this)" title="Редактировать" aria-label="Редактировать">
      <i class="bi bi-pencil"></i>
    </button>
    <form class="d-inline ms-1" method="post" action="${withToken('/trigger_bot/delete')}" onsubmit="return handleDeleteSubmit(event, this)">
      <input type="hidden" name="id" value="${id}">
      <button class="btn btn-sm action-mini btn-outline-danger" type="submit" title="Удалить" aria-label="Удалить">
        <i class="bi bi-x-lg"></i>
      </button>
    </form>
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
  const out = document.getElementById('import_file_name');
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

async function handleToggleSubmit(event, form){
  if(event){ event.preventDefault(); }
  const id = triggerIdFromForm(form);
  if(id > 0 && rowActionBusy.has(id)){ return false; }
  const btn = form ? form.querySelector('button[type="submit"]') : null;
  lockButton(btn);
  if(id > 0){ rowActionBusy.add(id); }
  try{
    const baseUrl = form.getAttribute('action') || withToken('/trigger_bot/toggle');
    const url = new URL(baseUrl, window.location.origin);
    if(id > 0){ url.searchParams.set('id', String(id)); }
    const res = await fetch(url.toString(), {
      method: 'POST',
      body: new FormData(form),
      headers: {
        'Accept': 'application/json'
      },
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
  if(!confirm('Удалить?')){
    if(event){ event.preventDefault(); }
    return false;
  }
  return handleRowFormSubmit(event, form);
}

function handleImportSubmit(event, form){
  const btn = document.getElementById('import_submit_btn');
  if(btn && btn.dataset.locked === '1'){
    if(event){ event.preventDefault(); }
    return false;
  }
  lockButton(btn);
  return true;
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
    const res = await fetch(form.getAttribute('action') || withToken('/trigger_bot/save'), {
      method: 'POST',
      body: new FormData(form),
      signal: controller.signal,
      credentials: 'same-origin'
    });
    clearTimeout(timeout);
    if(!res.ok){
      const txt = await res.text();
      alert('Ошибка сохранения: ' + (txt || res.status));
      setSaveBusy(false);
      return false;
    }
    window.location.href = withToken('/trigger_bot');
  }catch(err){
    alert('Сохранение не удалось: ' + (err && err.message ? err.message : err));
    setSaveBusy(false);
  }
  return false;
}

function getResponseTextArea(){
  return document.getElementById('f_response_text');
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
  if(editor){
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

function insertResponseLink(){
  const url = prompt('URL ссылки', 'https://');
  if(!url){ return; }
  const el = getResponseTextArea();
  if(!el){ return; }
  const start = el.selectionStart ?? 0;
  const end = el.selectionEnd ?? 0;
  const val = el.value ?? '';
  const selected = val.slice(start, end) || 'ссылка';
  const linked = `<a href="${url}">${selected}</a>`;
  el.value = val.slice(0, start) + linked + val.slice(end);
  const caret = start + linked.length;
  el.focus();
  el.setSelectionRange(caret, caret);
}

function insertTgEmojiSnippet(){
  const id = prompt('ID кастомного emoji (из Telegram)', '12345');
  if(!id){ return; }
  const safeId = String(id).trim();
  if(!safeId){ return; }
  insertTextAtCursor(getResponseTextArea(), `<tg-emoji emoji-id="${safeId}">🙂</tg-emoji>`);
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
  insertTextAtCursor(getResponseTextArea(), sel.value);
  sel.value = '';
}

function bindMiniToolbarFallback(){
  if(document.body.dataset.toolbarBound === '1'){ return; }
  document.body.dataset.toolbarBound = '1';
  document.addEventListener('click', (e) => {
    const btn = e.target && e.target.closest ? e.target.closest('button[data-action]') : null;
    if(!btn){ return; }
    const action = btn.getAttribute('data-action');
    if(action === 'wrap'){
      const before = btn.getAttribute('data-before') || '';
      const after = btn.getAttribute('data-after') || '';
      wrapResponseSelection(before, after);
      return;
    }
    if(action === 'link'){
      insertResponseLink();
      return;
    }
    if(action === 'emoji'){
      insertTgEmojiSnippet();
      return;
    }
    if(action === 'help'){
      showTelegramHtmlHelp();
      return;
    }
  });

  document.addEventListener('change', (ev) => {
    const target = ev.target;
    if(!target || target.id !== 'f_template_tag_picker'){ return; }
    insertTemplateTagFromPicker(target);
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
  } else {
    lbl.textContent = 'Текст триггера';
    inp.type = 'text';
    inp.removeAttribute('min');
    inp.removeAttribute('step');
    inp.placeholder = 'Текст триггера';
    if(toggle){ toggle.disabled = false; }
    if(cs){ cs.disabled = false; }
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
  setResponseValue(pick(t,'response_text','ResponseText',''));
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

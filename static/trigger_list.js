function triggerApp(){
  return {
    handleImportSubmit,
    updateImportFileName,
    openNew,
    openTemplateNew,
    openScheduleNew,
    submitScheduleForm,
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
    startMTProtoQR,
    submitMTProtoQRPassword,
    stopMTProtoQR,
  };
}
window.triggerApp = triggerApp;

let __triggerPageInitialized = false;
let __triggerAppInitialized = false;
let triggerSortable = null;
const rowActionBusy = new Set();
let editLoadInFlight = false;
let reorderSaving = false;
let responseVariants = [{key: 1, text: ''}];
let activeResponseVariant = 0;
let responseVariantSeq = 1;
const enumCache = {};
let templatesCache = [];
let templatesLoadInFlight = false;
let schedulesCache = [];
let schedulesLoadInFlight = false;
let settingsLoadInFlight = false;
let settingsCache = {fields: [], values: {}};
let emojiHelperTimer = null;
let emojiHelperLastID = '';
let emojiHelperSelectedID = '';
let emojiHelperForcedSetKey = '';
let stickerHelperLastSet = '';
let stickerHelperSelectedSet = '';
let stickerHelperSelectedCode = '';
let stickerHelperForcedSetKey = '';
let recentSetsLoadInFlight = false;
let recentSetsSaveTimer = null;
let recentEmojiSets = [];
let recentStickerSets = [];
const emojiSetCache = {};
const stickerSetCache = {};
const RECENT_SETS_LIMIT = 300;
const ADMIN_FETCH_TIMEOUT_MS = 20000;
let authState = 'login_required';
let csrfToken = '';

function fetchAdmin(path, options, timeoutMs){
  const opts = Object.assign({credentials: 'same-origin'}, options || {});
  const timeout = Number.isFinite(timeoutMs) && timeoutMs > 0 ? timeoutMs : ADMIN_FETCH_TIMEOUT_MS;
  if(typeof AbortController === 'undefined'){
    return fetch(path, opts);
  }
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeout);
  opts.signal = controller.signal;
  return fetch(path, opts).finally(() => clearTimeout(timer));
}

function getCookieValue(name){
  const key = String(name || '').trim();
  if(!key){ return ''; }
  const all = String(document.cookie || '').split(';');
  for(const part of all){
    const p = String(part || '').trim();
    if(!p){ continue; }
    const idx = p.indexOf('=');
    if(idx <= 0){ continue; }
    const k = p.slice(0, idx).trim();
    if(k !== key){ continue; }
    try{
      return decodeURIComponent(p.slice(idx + 1));
    }catch(_){
      return p.slice(idx + 1);
    }
  }
  return '';
}

function getCSRFToken(){
  if(String(csrfToken || '').trim()){
    return String(csrfToken).trim();
  }
  return String(getCookieValue('trigger_admin_csrf') || '').trim();
}

function setCSRFToken(token){
  csrfToken = String(token || '').trim();
}

function withCSRFHeaders(base){
  const h = Object.assign({}, base || {});
  const token = getCSRFToken();
  if(token){
    h['X-CSRF-Token'] = token;
  }
  return h;
}

async function initTriggerPage(){
  if(__triggerPageInitialized){ return; }
  __triggerPageInitialized = true;
  bindAuthUI();
  try{
    await refreshAuthState();
    if(authState === 'authenticated'){
      await initAuthenticatedApp();
    } else {
      showAuthGate(authState);
    }
  }catch(err){
    console.error('trigger admin init failed', err);
    showAuthGate('login_required', 'Админка не смогла загрузиться. Подробности записаны в консоль браузера и серверные логи.');
  }
}

function bindAuthUI(){
  const setupForm = document.getElementById('auth_setup_form');
  if(setupForm && !setupForm.dataset.boundSubmit){
    setupForm.addEventListener('submit', handleAuthSetup);
    setupForm.dataset.boundSubmit = '1';
  }
  const loginForm = document.getElementById('auth_login_form');
  if(loginForm && !loginForm.dataset.boundSubmit){
    loginForm.addEventListener('submit', handleAuthLogin);
    loginForm.dataset.boundSubmit = '1';
  }
  const logoutBtn = document.getElementById('auth_logout_btn');
  if(logoutBtn && !logoutBtn.dataset.boundClick){
    logoutBtn.addEventListener('click', handleAuthLogout);
    logoutBtn.dataset.boundClick = '1';
  }
  const changePasswordForm = document.getElementById('change_password_form');
  if(changePasswordForm && !changePasswordForm.dataset.boundSubmit){
    changePasswordForm.addEventListener('submit', handleChangePassword);
    changePasswordForm.dataset.boundSubmit = '1';
  }
}

async function refreshAuthState(){
  try{
    const r = await fetchAdmin('/trigger_bot/auth_state');
    if(!r.ok){
      authState = 'login_required';
      return authState;
    }
    const data = await r.json();
    authState = String(data && data.auth_state ? data.auth_state : 'login_required');
    if(data && data.csrf_token){
      setCSRFToken(data.csrf_token);
    } else {
      setCSRFToken(getCookieValue('trigger_admin_csrf'));
    }
    return authState;
  }catch(_){
    authState = 'login_required';
    return authState;
  }
}

function showAuthGate(state, hint){
  const gate = document.getElementById('auth_gate');
  const app = document.getElementById('admin_app_shell');
  const title = document.getElementById('auth_gate_title');
  const hintEl = document.getElementById('auth_gate_hint');
  const setupForm = document.getElementById('auth_setup_form');
  const loginForm = document.getElementById('auth_login_form');
  if(gate){ gate.classList.remove('d-none'); }
  if(app){ app.classList.add('d-none'); }
  if(setupForm){ setupForm.classList.add('d-none'); }
  if(loginForm){ loginForm.classList.add('d-none'); }
  const st = String(state || authState || 'login_required');
  if(st === 'setup_required'){
    if(title){ title.textContent = 'Первичная настройка'; }
    if(hintEl){ hintEl.textContent = hint || 'Пароль администратора ещё не установлен. Создайте его ниже.'; }
    if(setupForm){ setupForm.classList.remove('d-none'); }
  } else {
    if(title){ title.textContent = 'Вход в админку'; }
    if(hintEl){ hintEl.textContent = hint || 'Введите пароль администратора.'; }
    if(loginForm){ loginForm.classList.remove('d-none'); }
  }
}

function showAdminApp(){
  const gate = document.getElementById('auth_gate');
  const app = document.getElementById('admin_app_shell');
  if(gate){ gate.classList.add('d-none'); }
  if(app){ app.classList.remove('d-none'); }
}

async function initAuthenticatedApp(){
  if(__triggerAppInitialized){
    showAdminApp();
    return;
  }
  __triggerAppInitialized = true;
  showAdminApp();
  window.__trgModal = new bootstrap.Modal(document.getElementById('triggerModal'));
  applyMatchTypeUI();
  bindMatchTextToggle();
  bindMiniToolbarFallback();
  bindEmojiHelperListeners();
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
  const mtprotoQRStartBtn = document.getElementById('mtproto_qr_start_btn');
  if(mtprotoQRStartBtn && !mtprotoQRStartBtn.dataset.boundClick){
    mtprotoQRStartBtn.addEventListener('click', startMTProtoQR);
    mtprotoQRStartBtn.dataset.boundClick = '1';
  }
  const mtprotoQRStopBtn = document.getElementById('mtproto_qr_stop_btn');
  if(mtprotoQRStopBtn && !mtprotoQRStopBtn.dataset.boundClick){
    mtprotoQRStopBtn.addEventListener('click', stopMTProtoQR);
    mtprotoQRStopBtn.dataset.boundClick = '1';
  }
  const mtprotoQRPwBtn = document.getElementById('mtproto_qr_pw_btn');
  if(mtprotoQRPwBtn && !mtprotoQRPwBtn.dataset.boundClick){
    mtprotoQRPwBtn.addEventListener('click', submitMTProtoQRPassword);
    mtprotoQRPwBtn.dataset.boundClick = '1';
  }
  renderResponseVariants();
  const addVariantBtn = document.getElementById('response_add_variant');
  if(addVariantBtn && !addVariantBtn.dataset.boundClick){
    addVariantBtn.addEventListener('click', addResponseVariant);
    addVariantBtn.dataset.boundClick = '1';
  }
  void loadTriggerList().then(() => initTriggerDragAndDrop()).catch(() => {});
  void Promise.allSettled([
    loadEnums(),
    loadTemplateTags(),
    loadTemplates(),
    loadSchedules(),
    loadSettings(),
    loadMTProtoChatOptions(),
    loadRecentSetsHistory(),
  ]);
}

async function loadMTProtoChatOptions(){
  const sel = document.getElementById('mtproto_chat_id');
  if(!sel){ return; }
  try{
    const r = await fetchAdmin(withToken('/trigger_bot/mtproto_chat_options'));
    if(!r.ok){ return; }
    const data = await r.json();
    const items = Array.isArray(data?.items) ? data.items : [];
    const prev = String(sel.value || '');
    sel.innerHTML = '<option value="">Выберите чат…</option>';
    items.forEach((it) => {
      const id = Number(it?.chat_id);
      const title = String(it?.title || '').trim();
      if(!Number.isFinite(id) || id === 0 || !title){ return; }
      const opt = document.createElement('option');
      opt.value = String(id);
      opt.textContent = title;
      sel.appendChild(opt);
    });
    if(prev){
      sel.value = prev;
    }
  } catch(_){
    // noop
  }
}

async function handleAuthSetup(ev){
  if(ev){ ev.preventDefault(); }
  const input = document.getElementById('auth_setup_password');
  const pass = String(input?.value || '');
  const r = await fetch('/trigger_bot/auth_setup', {
    method: 'POST',
    headers: {'Content-Type': 'application/json', 'Accept': 'application/json'},
    body: JSON.stringify({password: pass}),
    credentials: 'same-origin'
  });
  if(!r.ok){
    const txt = await r.text();
    alert('Ошибка установки пароля: ' + (txt || r.status));
    return false;
  }
  if(input){ input.value = ''; }
  const data = await r.json().catch(() => ({}));
  if(data && data.csrf_token){
    setCSRFToken(data.csrf_token);
  }
  authState = 'authenticated';
  await initAuthenticatedApp();
  return false;
}

async function handleAuthLogin(ev){
  if(ev){ ev.preventDefault(); }
  const input = document.getElementById('auth_login_password');
  const pass = String(input?.value || '');
  const r = await fetch('/trigger_bot/auth_login', {
    method: 'POST',
    headers: {'Content-Type': 'application/json', 'Accept': 'application/json'},
    body: JSON.stringify({password: pass}),
    credentials: 'same-origin'
  });
  if(!r.ok){
    let msg = '';
    try{
      const data = await r.json();
      msg = String(data?.error || '');
    }catch(_){}
    if(!msg){
      msg = await r.text();
    }
    alert('Ошибка входа: ' + (msg || r.status));
    return false;
  }
  if(input){ input.value = ''; }
  const data = await r.json().catch(() => ({}));
  if(data && data.csrf_token){
    setCSRFToken(data.csrf_token);
  }
  authState = 'authenticated';
  await initAuthenticatedApp();
  return false;
}

async function handleAuthLogout(){
  await fetch('/trigger_bot/auth_logout', {
    method: 'POST',
    headers: withCSRFHeaders({'Accept': 'application/json'}),
    credentials: 'same-origin'
  });
  setCSRFToken('');
  authState = 'login_required';
  showAuthGate(authState, 'Сессия завершена.');
}

async function handleChangePassword(ev){
  if(ev){ ev.preventDefault(); }
  const current = String(document.getElementById('change_password_current')?.value || '');
  const next = String(document.getElementById('change_password_new')?.value || '');
  const btn = document.getElementById('change_password_btn');
  const hint = document.getElementById('change_password_hint');
  lockButton(btn);
  try{
    const r = await fetch('/trigger_bot/change_password', {
      method: 'POST',
      headers: withCSRFHeaders({'Content-Type': 'application/json', 'Accept': 'application/json'}),
      body: JSON.stringify({current_password: current, new_password: next}),
      credentials: 'same-origin'
    });
    if(!r.ok){
      const txt = await r.text();
      alert('Ошибка смены пароля: ' + (txt || r.status));
      return false;
    }
    if(hint){ hint.textContent = 'Пароль изменён. Войдите снова.'; }
    const curEl = document.getElementById('change_password_current');
    const newEl = document.getElementById('change_password_new');
    if(curEl){ curEl.value = ''; }
    if(newEl){ newEl.value = ''; }
    await handleAuthLogout();
  } finally {
    unlockButton(btn);
  }
  return false;
}

async function loadEnums(){
  try{
    const r = await fetchAdmin(withToken('/trigger_bot/enums'));
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
  const pickers = Array.from(document.querySelectorAll('.template-tag-picker'));
  if(pickers.length === 0){
    return;
  }
  try{
    const r = await fetchAdmin(withToken('/trigger_bot/template_tags'));
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
    const r = await fetchAdmin(withToken('/trigger_bot/templates'));
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
    const r = await fetchAdmin(withToken('/trigger_bot/settings_get'));
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
      headers: withCSRFHeaders({'Content-Type': 'application/json', 'Accept': 'application/json'}),
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
      headers: withCSRFHeaders({'Content-Type': 'application/json', 'Accept': 'application/json'}),
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
    headers: withCSRFHeaders({'Content-Type': 'application/json', 'Accept': 'application/json'}),
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
    headers: withCSRFHeaders({'Content-Type': 'application/json', 'Accept': 'application/json'}),
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

async function loadSchedules(){
  const tbody = document.getElementById('schedules_tbody');
  if(!tbody){ return; }
  try{
    if(schedulesLoadInFlight){ return; }
    schedulesLoadInFlight = true;
    const r = await fetchAdmin(withToken('/trigger_bot/schedules'));
    if(!r.ok){
      renderSchedulesError('Не удалось загрузить расписания');
      return;
    }
    const data = await r.json();
    schedulesCache = Array.isArray(data?.items) ? data.items : [];
    renderSchedulesTable();
  } catch(err){
    renderSchedulesError('Не удалось загрузить расписания');
  } finally {
    schedulesLoadInFlight = false;
  }
}

function renderSchedulesError(msg){
  const tbody = document.getElementById('schedules_tbody');
  if(!tbody){ return; }
  tbody.innerHTML = `<tr><td colspan="7" class="text-center text-danger py-4">${escapeHtml(msg)}</td></tr>`;
}

function formatScheduleTime(unix){
  const ts = Number(unix);
  if(!Number.isFinite(ts) || ts <= 0){ return '—'; }
  try{
    return new Date(ts * 1000).toLocaleString('ru-RU', {timeZone: 'Europe/Moscow'});
  } catch(_){
    return new Date(ts * 1000).toISOString();
  }
}

function renderSchedulesTable(){
  const tbody = document.getElementById('schedules_tbody');
  if(!tbody){ return; }
  if(!Array.isArray(schedulesCache) || schedulesCache.length === 0){
    tbody.innerHTML = '<tr><td colspan="7" class="text-center text-secondary py-4">Расписаний нет</td></tr>';
    return;
  }
  tbody.innerHTML = '';
  schedulesCache.forEach((sch) => {
    const id = String(sch?.ID ?? sch?.id ?? '').trim();
    const chatID = String(sch?.ChatID ?? sch?.chat_id ?? '').trim();
    const userID = String(sch?.UserID ?? sch?.user_id ?? '').trim();
    const username = String(sch?.Username ?? sch?.username ?? '').trim();
    const cron = String(sch?.Cron ?? sch?.cron ?? '').trim();
    const action = String(sch?.Action ?? sch?.action ?? '').trim();
    const enabled = Boolean(sch?.Enabled ?? sch?.enabled);
    const lastRun = sch?.LastRunAt ?? sch?.last_run_at ?? 0;
    const runsToday = Number(sch?.RunsToday ?? sch?.runs_today ?? 0);
    const userCell = username ? `@${escapeHtml(username)}` : (userID ? escapeHtml(userID) : '—');
    const tr = document.createElement('tr');
    tr.innerHTML = `
      <td class="col-chat"><code>${escapeHtml(chatID)}</code></td>
      <td class="col-user">${userCell}<div class="text-secondary small">${escapeHtml(userID)}</div></td>
      <td class="col-cron text-nowrap"><code>${escapeHtml(cron)}</code></td>
      <td class="text-wrap col-action-text" title="${escapeHtml(action)}">${escapeHtml(clipTemplatePreview(action, 90))}</td>
      <td class="col-state text-center"><small class="text-secondary">${runsToday}</small></td>
      <td class="col-last text-nowrap">${escapeHtml(formatScheduleTime(lastRun))}</td>
      <td class="text-nowrap col-actions">
        <button class="btn btn-sm action-mini action-visibility ${enabled ? 'btn-outline-success' : 'btn-outline-secondary'}" type="button" data-schedule-toggle="${escapeHtml(id)}" data-schedule-enabled="${enabled ? '1' : '0'}" title="Переключить статус" aria-label="Переключить статус">
          <i class="bi ${statusIcon(enabled)}"></i>
        </button>
        <button class="btn btn-sm action-mini btn-outline-info ms-1" type="button" data-schedule-edit="${escapeHtml(id)}" title="Редактировать" aria-label="Редактировать">
          <i class="bi bi-pencil"></i>
        </button>
        <button class="btn btn-sm action-mini btn-outline-danger ms-1" type="button" data-schedule-delete="${escapeHtml(id)}" title="Удалить" aria-label="Удалить">
          <i class="bi bi-trash"></i>
        </button>
      </td>`;
    tbody.appendChild(tr);
  });
}

function openScheduleNew(){
  document.getElementById('sch_id').value = '';
  const title = document.getElementById('schedule_modal_title');
  if(title){ title.textContent = 'Новое расписание'; }
  document.getElementById('sch_chat_id').value = '';
  document.getElementById('sch_user_id').value = '';
  document.getElementById('sch_username').value = '';
  bindCronEditor();
  writeCronExpression(CRON_DEFAULT);
  document.getElementById('sch_action').value = '';
  document.getElementById('sch_enabled').checked = true;
  ensureScheduleModal().show();
}

function openScheduleEdit(id){
  id = String(id || '').trim();
  if(!id){ return; }
  const sch = (Array.isArray(schedulesCache) ? schedulesCache : []).find((it) => String(it?.ID ?? it?.id ?? '') === id);
  if(!sch){
    alert('Расписание не найдено в списке — обнови страницу');
    return;
  }
  document.getElementById('sch_id').value = id;
  const title = document.getElementById('schedule_modal_title');
  if(title){ title.textContent = 'Правка расписания'; }
  document.getElementById('sch_chat_id').value = String(sch?.ChatID ?? sch?.chat_id ?? '');
  document.getElementById('sch_user_id').value = String(sch?.UserID ?? sch?.user_id ?? '');
  document.getElementById('sch_username').value = String(sch?.Username ?? sch?.username ?? '');
  bindCronEditor();
  writeCronExpression(String(sch?.Cron ?? sch?.cron ?? ''));
  document.getElementById('sch_action').value = String(sch?.Action ?? sch?.action ?? '');
  document.getElementById('sch_enabled').checked = Boolean(sch?.Enabled ?? sch?.enabled);
  ensureScheduleModal().show();
}

// Cron: штатное поле #sch_cron — видимое значение, <cron-editor> — визуальный
// редактор. Вся разметка, разбор и форматирование живут в компоненте
// static/cron-editor.js; здесь только связка двух элементов.
const CRON_FIELD_COUNT = 5;
const CRON_DEFAULT = '0 9 * * *';

function cronEditorEl(){
  return document.getElementById('sch_cron_grid');
}

function normalizeCronParts(expr){
  const parts = String(expr || '').trim().split(/\s+/).filter(Boolean);
  while(parts.length < CRON_FIELD_COUNT){ parts.push('*'); }
  return parts.slice(0, CRON_FIELD_COUNT).map((v) => v || '*');
}

function readCronExpression(){
  const input = document.getElementById('sch_cron');
  return normalizeCronParts(input ? input.value : '').join(' ');
}

function writeCronExpression(expr){
  const normalized = normalizeCronParts(expr).join(' ');
  const input = document.getElementById('sch_cron');
  if(input){ input.value = normalized; }
  const editor = cronEditorEl();
  if(editor){ editor.value = normalized; }
}

function bindCronEditor(){
  const editor = cronEditorEl();
  if(!editor || editor.dataset.boundChange === '1'){ return; }
  editor.dataset.boundChange = '1';
  editor.addEventListener('change', (ev) => {
    const input = document.getElementById('sch_cron');
    const next = ev && ev.detail && typeof ev.detail.value === 'string' ? ev.detail.value : '';
    if(input && next && input.value !== next){ input.value = next; }
  });
}

// cloneSchedule повторяет поведение кнопки «Клонировать» у триггеров: поля
// остаются, id сбрасывается — сохранение создаст новое расписание.
function cloneSchedule(){
  const idEl = document.getElementById('sch_id');
  if(!idEl){ return; }
  const hadID = String(idEl.value || '').trim() !== '';
  if(!hadID){ return; }
  idEl.value = '';
  const title = document.getElementById('schedule_modal_title');
  if(title){ title.textContent = 'Новое расписание (копия)'; }
  document.getElementById('sch_action')?.focus();
}

async function submitScheduleForm(ev){
  ev.preventDefault();
  const scheduleID = String(document.getElementById('sch_id')?.value || '').trim();
  const payload = {
    chat_id: Number(String(document.getElementById('sch_chat_id')?.value || '').trim()),
    user_id: Number(String(document.getElementById('sch_user_id')?.value || '').trim()),
    username: String(document.getElementById('sch_username')?.value || '').trim(),
    cron: readCronExpression(),
    action: String(document.getElementById('sch_action')?.value || '').trim(),
    enabled: Boolean(document.getElementById('sch_enabled')?.checked),
  };
  if(!Number.isFinite(payload.chat_id) || payload.chat_id === 0){
    alert('Укажи chat_id, например -1002329738424');
    return false;
  }
  if(!Number.isFinite(payload.user_id) || payload.user_id === 0){
    alert('Укажи user_id автора расписания');
    return false;
  }
  if(!payload.cron){
    alert('Укажи cron, например “0 9 * * *”');
    return false;
  }
  if(!payload.action){
    alert('Опиши задание для ИИ-агента');
    return false;
  }
  const endpoint = scheduleID ? '/trigger_bot/schedule_update' : '/trigger_bot/schedule_create';
  if(scheduleID){ payload.id = scheduleID; }
  const r = await fetch(withToken(endpoint), {
    method: 'POST',
    headers: withCSRFHeaders({'Content-Type': 'application/json', 'Accept': 'application/json'}),
    body: JSON.stringify(payload),
  });
  if(!r.ok){
    const txt = await r.text();
    alert('Ошибка сохранения расписания: ' + (txt || r.status));
    return false;
  }
  ensureScheduleModal().hide();
  await loadSchedules();
  return false;
}

async function toggleSchedule(id, enabled, btn){
  if(!id){ return; }
  const r = await fetch(withToken('/trigger_bot/schedule_toggle'), {
    method: 'POST',
    headers: withCSRFHeaders({'Content-Type': 'application/json', 'Accept': 'application/json'}),
    body: JSON.stringify({id, enabled: !enabled}),
  });
  if(!r.ok){
    const txt = await r.text();
    alert('Ошибка переключения расписания: ' + (txt || r.status));
    return;
  }
  // Как у триггеров: обновляем кнопку на месте, без перерисовки таблицы.
  const nextEnabled = !enabled;
  const item = (Array.isArray(schedulesCache) ? schedulesCache : [])
    .find((it) => String(it?.ID ?? it?.id ?? '') === String(id));
  if(item){
    if('Enabled' in item){ item.Enabled = nextEnabled; } else { item.enabled = nextEnabled; }
  }
  if(btn){
    btn.classList.toggle('btn-outline-success', nextEnabled);
    btn.classList.toggle('btn-outline-secondary', !nextEnabled);
    btn.setAttribute('data-schedule-enabled', nextEnabled ? '1' : '0');
    const icon = btn.querySelector('i');
    if(icon){ icon.className = 'bi ' + statusIcon(nextEnabled); }
  }
}

async function deleteSchedule(id){
  if(!id){ return; }
  if(!confirm('Удалить расписание?')){ return; }
  const r = await fetch(withToken('/trigger_bot/schedule_delete'), {
    method: 'POST',
    headers: withCSRFHeaders({'Content-Type': 'application/json', 'Accept': 'application/json'}),
    body: JSON.stringify({id}),
  });
  if(!r.ok){
    const txt = await r.text();
    alert('Ошибка удаления расписания: ' + (txt || r.status));
    return;
  }
  await loadSchedules();
}

function ensureScheduleModal(){
  if(!window.__schModal){
    window.__schModal = new bootstrap.Modal(document.getElementById('scheduleModal'));
  }
  return window.__schModal;
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
  if(id === 'f_match_type'){
    applyMatchTypeUI();
  }
  if(id === 'f_action_type'){
    applyActionTypeUI();
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

function nowUnix(){
  return Math.floor(Date.now() / 1000);
}

function normalizeRecentEmojiEntry(it){
  const emojiID = String(it?.emoji_id || it?.emojiID || '').trim();
  const setName = String(it?.set_name || '').trim();
  if(!setName){ return null; }
  return {
    emoji_id: emojiID,
    set_name: setName,
    title: String(it?.title || '').trim(),
    emoji: String(it?.emoji || '🙂').trim() || '🙂',
    preview_url: String(it?.preview_url || '').trim(),
    thumb_url: String(it?.thumb_url || '').trim(),
    updated_at: Number(it?.updated_at || 0) || nowUnix(),
  };
}

function normalizeRecentStickerEntry(it){
  const setName = String(it?.set_name || it?.setName || '').trim();
  if(!setName){ return null; }
  return {
    set_name: setName,
    title: String(it?.title || '').trim(),
    preview_url: String(it?.preview_url || '').trim(),
    thumb_url: String(it?.thumb_url || '').trim(),
    updated_at: Number(it?.updated_at || 0) || nowUnix(),
  };
}

function upsertRecentEmojiSet(entry, source){
  const n = normalizeRecentEmojiEntry(entry);
  if(!n){ return; }
  const key = String(n.set_name || '').trim();
  if(!key){ return; }
  const list = [n, ...recentEmojiSets.filter((x) => String(x?.set_name || '').trim() !== key)];
  recentEmojiSets = list.slice(0, RECENT_SETS_LIMIT);
  if(source !== 'server'){
    scheduleRecentSetsSave();
  }
}

function upsertRecentStickerSet(entry, source){
  const n = normalizeRecentStickerEntry(entry);
  if(!n){ return; }
  const key = n.set_name;
  const list = [n, ...recentStickerSets.filter((x) => String(x?.set_name || '') !== key)];
  recentStickerSets = list.slice(0, RECENT_SETS_LIMIT);
  if(source !== 'server'){
    scheduleRecentSetsSave();
  }
}

function readRecentSetsFromLocalStorage(){
  try{
    const raw = localStorage.getItem('trigger_recent_sets_v1');
    if(!raw){ return null; }
    const data = JSON.parse(raw);
    return {
      emoji_sets: Array.isArray(data?.emoji_sets) ? data.emoji_sets : [],
      sticker_sets: Array.isArray(data?.sticker_sets) ? data.sticker_sets : [],
    };
  }catch(_){
    return null;
  }
}

function writeRecentSetsToLocalStorage(){
  try{
    localStorage.setItem('trigger_recent_sets_v1', JSON.stringify({
      emoji_sets: recentEmojiSets,
      sticker_sets: recentStickerSets,
    }));
  }catch(_){
    // ignore storage failures
  }
}

async function loadRecentSetsHistory(){
  if(recentSetsLoadInFlight){ return; }
  recentSetsLoadInFlight = true;
  try{
    const local = readRecentSetsFromLocalStorage();
    if(local){
      (local.emoji_sets || []).forEach((it) => upsertRecentEmojiSet(it, 'server'));
      (local.sticker_sets || []).forEach((it) => upsertRecentStickerSet(it, 'server'));
    }
    const r = await fetchAdmin(withToken('/trigger_bot/recent_sets_get'));
    if(r.ok){
      const data = await r.json();
      const fromServerEmoji = Array.isArray(data?.emoji_sets) ? data.emoji_sets : [];
      const fromServerSticker = Array.isArray(data?.sticker_sets) ? data.sticker_sets : [];
      fromServerEmoji.forEach((it) => upsertRecentEmojiSet(it, 'server'));
      fromServerSticker.forEach((it) => upsertRecentStickerSet(it, 'server'));
      writeRecentSetsToLocalStorage();
      // Merge local additions back to Mongo if any.
      scheduleRecentSetsSave();
    }
  }catch(_){
    // keep local fallback silently
  }finally{
    recentSetsLoadInFlight = false;
  }
}

function scheduleRecentSetsSave(){
  writeRecentSetsToLocalStorage();
  if(recentSetsSaveTimer){ clearTimeout(recentSetsSaveTimer); }
  recentSetsSaveTimer = setTimeout(async () => {
    recentSetsSaveTimer = null;
    try{
      await fetch(withToken('/trigger_bot/recent_sets_save'), {
        method: 'POST',
        headers: withCSRFHeaders({'Content-Type': 'application/json'}),
        body: JSON.stringify({
          emoji_sets: recentEmojiSets,
          sticker_sets: recentStickerSets,
        }),
      });
    }catch(_){
      // best-effort sync
    }
  }, 500);
}

function extractCustomEmojiTagsFromText(text){
  const src = String(text || '');
  const re = /<\s*tg-emoji[^>]*emoji-id\s*=\s*["']?(\d{3,})["']?[^>]*>/ig;
  const out = [];
  let m;
  while((m = re.exec(src)) !== null){
    const id = String(m[1] || '').trim();
    if(!id){ continue; }
    out.push({id, start: Number(m.index || 0), end: Number(m.index || 0) + String(m[0] || '').length});
    if(out.length >= 64){ break; }
  }
  return out;
}

function extractStickerSetTagsFromText(text){
  const src = String(text || '');
  const re = /([A-Za-z0-9_-]{16,}):([A-Za-z][A-Za-z0-9_]{2,})/g;
  const out = [];
  let m;
  while((m = re.exec(src)) !== null){
    const fileID = String(m[1] || '').trim();
    const setName = String(m[2] || '').trim();
    if(!fileID || !setName){ continue; }
    const start = Number((m.index || 0) + fileID.length + 1);
    const end = start + setName.length;
    out.push({fileID, setName, start, end});
    if(out.length >= 64){ break; }
  }
  return out;
}

function getResponseCursorOffset(){
  const editor = getEditorView();
  if(editor && editor.state && editor.state.selection && editor.state.selection.main){
    return Number(editor.state.selection.main.head || 0);
  }
  const ta = getResponseTextArea();
  return ta ? Number(ta.selectionStart || 0) : 0;
}

function pickEmojiIDByCursor(tags, cursor){
  if(!Array.isArray(tags) || tags.length === 0){ return ''; }
  const pos = Number.isFinite(cursor) ? cursor : 0;
  for(const t of tags){
    if(pos >= Number(t.start || 0) && pos <= Number(t.end || 0)){
      return String(t.id || '');
    }
  }
  return String(tags[0].id || '');
}

function bindEmojiHelperListeners(){
  forEachResponseTextArea((ta) => {
    if(ta.dataset.emojiHelperBound){ return; }
    ta.addEventListener('input', scheduleEmojiSetHelperRefresh);
    ta.addEventListener('keyup', scheduleEmojiSetHelperRefresh);
    ta.addEventListener('click', scheduleEmojiSetHelperRefresh);
    ta.dataset.emojiHelperBound = '1';
  });
  if(!document.body.dataset.emojiHelperEditorBound){
    window.addEventListener('response-editor-change', scheduleEmojiSetHelperRefresh);
    window.addEventListener('response-editor-selection', scheduleEmojiSetHelperRefresh);
    document.body.dataset.emojiHelperEditorBound = '1';
  }
}

function scheduleEmojiSetHelperRefresh(){
  if(emojiHelperTimer){ clearTimeout(emojiHelperTimer); }
  emojiHelperTimer = setTimeout(() => {
    emojiHelperTimer = null;
    refreshEmojiSetHelper();
    refreshStickerSetHelper();
  }, 180);
}

function currentActionType(){
  return String(document.getElementById('f_action_type')?.value || 'send').toLowerCase();
}

function shouldShowStickerHelper(){
  return currentActionType() === 'send_sticker';
}

function shouldShowEmojiHelper(){
  return currentActionType() === 'send';
}

function hideEmojiSetHelper(){
  const box = document.getElementById('emoji_set_helper');
  if(!box){ return; }
  box.classList.add('d-none');
  box.innerHTML = '';
  emojiHelperLastID = '';
  emojiHelperSelectedID = '';
  emojiHelperForcedSetKey = '';
}

function emojiEntryKey(it){
  return String(it?.set_name || '').trim();
}

function stickerEntryKey(it){
  return String(it?.set_name || '').trim();
}

function renderEmojiSetHelperLoading(id){
  const box = document.getElementById('emoji_set_helper');
  if(!box){ return; }
  box.classList.remove('d-none');
  box.innerHTML = `<div class="emoji-helper-title">Набор эмодзи для <code>${escapeHtml(id)}</code>…</div>`;
}

function renderEmojiSetHelperError(msg){
  const box = document.getElementById('emoji_set_helper');
  if(!box){ return; }
  box.classList.remove('d-none');
  box.innerHTML = `<div class="emoji-helper-title text-danger">${escapeHtml(msg || 'Не удалось загрузить набор эмодзи')}</div>`;
}

function renderEmojiSetTabs(activeKey){
  const tabs = recentEmojiSets.map((it) => {
    const key = emojiEntryKey(it);
    if(!key){ return ''; }
    const activeClass = key === activeKey ? ' active' : '';
    const title = String(it?.title || it?.set_name || '').trim();
    const previewURL = String(it?.preview_url || it?.thumb_url || '').trim();
    const fallbackURL = String(it?.thumb_url || '').trim();
    const content = previewURL
      ? `<img alt="emoji" loading="lazy" src="${escapeHtml(withToken(previewURL))}" data-fallback-src="${escapeHtml(fallbackURL ? withToken(fallbackURL) : '')}">`
      : `<span class="emoji-set-tab-symbol">${escapeHtml(String(it?.emoji || '🙂').trim() || '🙂')}</span>`;
    return `<button type="button" class="emoji-set-tab${activeClass}" data-emoji-set-key="${escapeHtml(key)}" data-emoji-id="${escapeHtml(String(it?.emoji_id || '').trim())}" title="${escapeHtml(title)}">${content}</button>`;
  }).join('');
  return `<div class="emoji-set-tabs">${tabs}</div>`;
}

function renderStickerSetTabs(activeKey){
  const tabs = recentStickerSets.map((it) => {
    const key = stickerEntryKey(it);
    if(!key){ return ''; }
    const activeClass = key === activeKey ? ' active' : '';
    const title = String(it?.title || key).trim();
    const imgURL = String(it?.preview_url || it?.thumb_url || '').trim();
    const fallbackURL = String(it?.thumb_url || '').trim();
    const content = imgURL
      ? `<img alt="sticker" loading="lazy" src="${escapeHtml(withToken(imgURL))}" data-fallback-src="${escapeHtml(fallbackURL ? withToken(fallbackURL) : '')}">`
      : `<span class="emoji-set-tab-symbol">🎟</span>`;
    return `<button type="button" class="emoji-set-tab${activeClass}" data-sticker-set-key="${escapeHtml(key)}" title="${escapeHtml(title)}">${content}</button>`;
  }).join('');
  return `<div class="emoji-set-tabs">${tabs}</div>`;
}

function bindSetTabImageFallback(box){
  box.querySelectorAll('.emoji-set-tab img').forEach((img) => {
    const fallbackURL = String(img.getAttribute('data-fallback-src') || '').trim();
    if(!fallbackURL){ return; }
    img.addEventListener('error', () => {
      if(img.src !== fallbackURL){
        img.src = fallbackURL;
      }
    });
  });
}

function renderEmojiSetHelper(){
  const box = document.getElementById('emoji_set_helper');
  if(!box){ return; }
  if(recentEmojiSets.length === 0){
    hideEmojiSetHelper();
    return;
  }
  let activeKey = '';
  if(emojiHelperForcedSetKey){
    activeKey = emojiHelperForcedSetKey;
  } else if(emojiHelperSelectedID){
    const found = recentEmojiSets.find((x) => String(x?.emoji_id || '') === emojiHelperSelectedID);
    if(found){
      activeKey = emojiEntryKey(found);
    }
  }
  if(!activeKey){
    activeKey = emojiEntryKey(recentEmojiSets[0]);
  }
  const data = emojiSetCache[activeKey];
  const items = Array.isArray(data?.items) ? data.items : [];
  if(items.length === 0){
    box.classList.remove('d-none');
    box.innerHTML = `${renderEmojiSetTabs(activeKey)}<div class="emoji-helper-title mt-2">Набор ещё не загружен…</div>`;
    bindEmojiSetTabClicks(box);
    return;
  }
  const head = `<div class="emoji-helper-title">Нажми эмодзи, чтобы вставить</div>`;
  const chips = items.map((it) => {
    const id = String(it?.custom_emoji_id || '').trim();
    if(!id){ return ''; }
    const emoji = String(it?.emoji || '🙂');
    const preview = String(it?.preview_url || '').trim();
    const thumb = String(it?.thumb_url || '').trim();
    const previewURL = preview ? withToken(preview) : '';
    const thumbURL = thumb ? withToken(thumb) : previewURL;
    const selectedClass = id === emojiHelperSelectedID ? ' emoji-chip-selected' : '';
    return `<button type="button" class="emoji-chip${selectedClass}" data-emoji-id="${escapeHtml(id)}" data-emoji-fallback="${escapeHtml(emoji)}" data-preview-url="${escapeHtml(previewURL)}" data-thumb-url="${escapeHtml(thumbURL)}" title="${escapeHtml(id)}"><img alt="${escapeHtml(emoji)}" loading="lazy" src="${escapeHtml(previewURL)}"></button>`;
  }).join('');
  box.classList.remove('d-none');
  box.innerHTML = `${renderEmojiSetTabs(activeKey)}${head}<div class="emoji-helper-grid">${chips}</div>`;
  bindSetTabImageFallback(box);
  bindEmojiSetTabClicks(box);
  box.querySelectorAll('.emoji-chip[data-emoji-id]').forEach((btn) => {
    const img = btn.querySelector('img');
    const thumbURL = String(btn.getAttribute('data-thumb-url') || '').trim();
    if(img){
      img.addEventListener('error', () => {
        if(thumbURL && img.src !== thumbURL){
          img.src = thumbURL;
        }
      });
    }
    btn.addEventListener('click', () => {
      const id = String(btn.getAttribute('data-emoji-id') || '').trim();
      const fallback = String(btn.getAttribute('data-emoji-fallback') || '🙂').trim() || '🙂';
      if(!id){ return; }
      emojiHelperSelectedID = id;
      insertTextAtCursor(getResponseTextArea(), `<tg-emoji emoji-id="${id}">${fallback}</tg-emoji>`);
      scheduleEmojiSetHelperRefresh();
    });
  });
}

function hideStickerSetHelper(){
  const box = document.getElementById('sticker_set_helper');
  if(!box){ return; }
  box.classList.add('d-none');
  box.innerHTML = '';
  stickerHelperLastSet = '';
  stickerHelperSelectedSet = '';
  stickerHelperSelectedCode = '';
  stickerHelperForcedSetKey = '';
}

function renderStickerSetHelperLoading(setName){
  const box = document.getElementById('sticker_set_helper');
  if(!box){ return; }
  box.classList.remove('d-none');
  box.innerHTML = `<div class="emoji-helper-title">Набор стикеров <code>${escapeHtml(setName)}</code>…</div>`;
}

function renderStickerSetHelperError(msg){
  const box = document.getElementById('sticker_set_helper');
  if(!box){ return; }
  box.classList.remove('d-none');
  box.innerHTML = `<div class="emoji-helper-title text-danger">${escapeHtml(msg || 'Не удалось загрузить набор стикеров')}</div>`;
}

function bindEmojiSetTabClicks(box){
  box.querySelectorAll('.emoji-set-tab[data-emoji-set-key]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const key = String(btn.getAttribute('data-emoji-set-key') || '').trim();
      const emojiID = String(btn.getAttribute('data-emoji-id') || '').trim();
      if(!key){ return; }
      emojiHelperForcedSetKey = key;
      if(!emojiSetCache[key] && emojiID){
        await fetchEmojiSetByID(emojiID);
      }
      renderEmojiSetHelper();
    });
  });
}

function bindStickerSetTabClicks(box){
  box.querySelectorAll('.emoji-set-tab[data-sticker-set-key]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const key = String(btn.getAttribute('data-sticker-set-key') || '').trim();
      if(!key){ return; }
      stickerHelperForcedSetKey = key;
      stickerHelperSelectedSet = key;
      if(!stickerSetCache[key]){
        await fetchStickerSetByName(key);
      }
      renderStickerSetHelper();
    });
  });
}

function renderStickerSetHelper(){
  const box = document.getElementById('sticker_set_helper');
  if(!box){ return; }
  if(recentStickerSets.length === 0){
    hideStickerSetHelper();
    return;
  }
  let activeKey = stickerHelperForcedSetKey || stickerHelperSelectedSet;
  if(!activeKey){
    activeKey = stickerEntryKey(recentStickerSets[0]);
  }
  const data = stickerSetCache[activeKey];
  const items = Array.isArray(data?.items) ? data.items : [];
  if(items.length === 0){
    box.classList.remove('d-none');
    box.innerHTML = `${renderStickerSetTabs(activeKey)}<div class="emoji-helper-title mt-2">Набор ещё не загружен…</div>`;
    bindStickerSetTabClicks(box);
    return;
  }
  const setName = String(data?.set_name || activeKey).trim();
  const head = `<div class="emoji-helper-title">Нажми стикер, чтобы вставить код</div>`;
  const chips = items.map((it) => {
    const code = String(it?.code || '').trim();
    const preview = String(it?.preview_url || '').trim();
    const thumb = String(it?.thumb_url || '').trim();
    if(!code){ return ''; }
    const previewURL = preview ? withToken(preview) : '';
    const thumbURL = thumb ? withToken(thumb) : previewURL;
    const selectedClass = code === stickerHelperSelectedCode ? ' emoji-chip-selected' : '';
    return `<button type="button" class="emoji-chip${selectedClass}" data-sticker-code="${escapeHtml(code)}" data-preview-url="${escapeHtml(previewURL)}" data-thumb-url="${escapeHtml(thumbURL)}" title="${escapeHtml(code)}"><img alt="sticker" loading="lazy" src="${escapeHtml(previewURL)}"></button>`;
  }).join('');
  box.classList.remove('d-none');
  box.innerHTML = `${renderStickerSetTabs(activeKey)}${head}<div class="emoji-helper-grid">${chips}</div>`;
  bindSetTabImageFallback(box);
  bindStickerSetTabClicks(box);
  box.querySelectorAll('.emoji-chip[data-sticker-code]').forEach((btn) => {
    const img = btn.querySelector('img');
    const thumbURL = String(btn.getAttribute('data-thumb-url') || '').trim();
    if(img){
      img.addEventListener('error', () => {
        if(thumbURL && img.src !== thumbURL){
          img.src = thumbURL;
        }
      });
    }
    btn.addEventListener('click', () => {
      const code = String(btn.getAttribute('data-sticker-code') || '').trim();
      if(!code){ return; }
      stickerHelperSelectedCode = code;
      // For send_sticker action only one sticker code is meaningful:
      // replace the whole response instead of appending at cursor.
      setResponseValue(code);
    });
  });
}

async function fetchEmojiSetByID(selectedID){
  if(!selectedID){ return; }
  renderEmojiSetHelperLoading(selectedID);
  const r = await fetch(withToken('/trigger_bot/emoji_set?emoji_id=' + encodeURIComponent(selectedID)));
  if(!r.ok){
    const txt = await r.text();
    throw new Error(txt || ('HTTP ' + r.status));
  }
  const data = await r.json();
  const setName = String(data?.set_name || '').trim();
  const key = setName;
  if(!key){
    return;
  }
  emojiSetCache[key] = data;
  const firstItem = Array.isArray(data?.items) ? data.items[0] : null;
  upsertRecentEmojiSet({
    emoji_id: selectedID,
    set_name: setName,
    title: String(data?.title || setName || selectedID).trim(),
    emoji: String(firstItem?.emoji || '🙂').trim() || '🙂',
    preview_url: String(data?.thumbnail_url || firstItem?.preview_url || '').trim(),
    thumb_url: String(data?.thumbnail_url || firstItem?.thumb_url || '').trim(),
    updated_at: nowUnix(),
  });
}

async function fetchStickerSetByName(setName){
  if(!setName){ return; }
  renderStickerSetHelperLoading(setName);
  const r = await fetch(withToken('/trigger_bot/sticker_set?set_name=' + encodeURIComponent(setName)));
  if(!r.ok){
    const txt = await r.text();
    throw new Error(txt || ('HTTP ' + r.status));
  }
  const data = await r.json();
  const key = String(data?.set_name || setName).trim();
  stickerSetCache[key] = data;
  const firstItem = Array.isArray(data?.items) ? data.items[0] : null;
  upsertRecentStickerSet({
    set_name: key,
    title: String(data?.title || key).trim(),
    preview_url: String(firstItem?.preview_url || data?.thumbnail_url || '').trim(),
    thumb_url: String(data?.thumbnail_url || firstItem?.thumb_url || '').trim(),
    updated_at: nowUnix(),
  });
}

async function refreshEmojiSetHelper(){
  if(!shouldShowEmojiHelper()){
    hideEmojiSetHelper();
    return;
  }
  const text = getResponseValue();
  const tags = extractCustomEmojiTagsFromText(text);
  if(tags.length === 0 && recentEmojiSets.length === 0){
    hideEmojiSetHelper();
    return;
  }
  let selectedID = '';
  if(tags.length > 0){
    selectedID = pickEmojiIDByCursor(tags, getResponseCursorOffset());
    emojiHelperSelectedID = selectedID;
  } else if(recentEmojiSets.length > 0){
    if(emojiHelperForcedSetKey){
      const foundByKey = recentEmojiSets.find((x) => emojiEntryKey(x) === emojiHelperForcedSetKey);
      selectedID = String(foundByKey?.emoji_id || '').trim();
    } else {
      selectedID = String(recentEmojiSets[0]?.emoji_id || '').trim();
    }
    emojiHelperSelectedID = selectedID;
  }
  if(!selectedID){
    renderEmojiSetHelper();
    return;
  }
  if(selectedID !== emojiHelperLastID){
    emojiHelperLastID = selectedID;
    try{
      await fetchEmojiSetByID(selectedID);
    } catch(err){
      renderEmojiSetHelperError(err && err.message ? err.message : String(err));
      return;
    }
  }
  renderEmojiSetHelper();
}

async function refreshStickerSetHelper(){
  if(!shouldShowStickerHelper()){
    hideStickerSetHelper();
    return;
  }
  const text = getResponseValue();
  const tags = extractStickerSetTagsFromText(text);
  if(tags.length === 0 && recentStickerSets.length === 0){
    hideStickerSetHelper();
    return;
  }
  let setName = '';
  if(tags.length > 0){
    const pick = tags.find((t) => {
      const pos = getResponseCursorOffset();
      return pos >= Number(t.start || 0) && pos <= Number(t.end || 0);
    }) || tags[0];
    setName = String(pick?.setName || '').trim();
    stickerHelperSelectedCode = `${String(pick?.fileID || '').trim()}:${setName}`;
  } else if(recentStickerSets.length > 0){
    setName = String(recentStickerSets[0]?.set_name || '').trim();
  }
  if(!setName){
    renderStickerSetHelper();
    return;
  }
  stickerHelperSelectedSet = setName;
  if(setName !== stickerHelperLastSet){
    stickerHelperLastSet = setName;
    try{
      await fetchStickerSetByName(setName);
    } catch(err){
      renderStickerSetHelperError(err && err.message ? err.message : String(err));
      return;
    }
  }
  renderStickerSetHelper();
}

// ── Варианты ответа ─────────────────────────────────────────────────────────
// Каждый вариант — своё окно (свой редактор). Модель — массив текстов, на
// сервер уходит массив строк, а структуру [{text: …}] собирает сервер.

function responseVariantElementId(key){
  return 'f_response_text_' + String(key);
}

function nextResponseKey(){
  responseVariantSeq += 1;
  return responseVariantSeq;
}

function normalizeResponseItems(raw){
  const out = [];
  const pushText = (value) => { out.push({key: nextResponseKey(), text: String(value ?? '')}); };
  if(raw == null){ return [{key: nextResponseKey(), text: ''}]; }
  if(Array.isArray(raw)){
    raw.forEach((it) => {
      if(it == null){ return; }
      if(typeof it === 'string'){ pushText(it); return; }
      if(typeof it === 'object'){
        if(Object.prototype.hasOwnProperty.call(it, 'text')){ pushText(it.text); return; }
        if(Object.prototype.hasOwnProperty.call(it, 'Text')){ pushText(it.Text); return; }
      }
    });
    return out.length > 0 ? out : [{key: nextResponseKey(), text: ''}];
  }
  if(typeof raw === 'string'){ pushText(raw); return out; }
  return [{key: nextResponseKey(), text: ''}];
}

// readVariantText достаёт актуальный текст варианта из его textarea.
function readVariantText(variant){
  if(!variant){ return ''; }
  const ta = document.getElementById(responseVariantElementId(variant.key));
  return ta ? ta.value : String(variant.text ?? '');
}

function commitResponseVariants(){
  responseVariants.forEach((variant) => { variant.text = readVariantText(variant); });
}

function commitActiveVariant(){
  commitResponseVariants();
}

function activeResponseVariantItem(){
  return responseVariants[activeResponseVariant] || null;
}

function setActiveVariant(idx){
  const next = Number(idx);
  if(!Number.isFinite(next) || next < 0 || next >= responseVariants.length){ return; }
  activeResponseVariant = next;
  document.querySelectorAll('#response_variants .response-variant').forEach((row) => {
    const rowIdx = Number(row.getAttribute('data-variant-index'));
    row.classList.toggle('response-variant-active', rowIdx === next);
  });
  scheduleEmojiSetHelperRefresh();
}

function renderResponseVariants(){
  const wrap = document.getElementById('response_variants');
  if(!wrap){ return; }
  if(!Array.isArray(responseVariants) || responseVariants.length === 0){
    responseVariants = [{key: nextResponseKey(), text: ''}];
  }
  responseVariants.forEach((variant) => {
    if(!variant || typeof variant !== 'object'){ return; }
    if(!variant.key){ variant.key = nextResponseKey(); }
    if(typeof variant.text !== 'string'){ variant.text = String(variant.text ?? ''); }
  });
  if(activeResponseVariant < 0 || activeResponseVariant >= responseVariants.length){ activeResponseVariant = 0; }

  wrap.innerHTML = '';
  responseVariants.forEach((variant, idx) => {
    const row = document.createElement('div');
    row.className = 'response-variant';
    row.setAttribute('data-variant-index', String(idx));
    const textareaId = responseVariantElementId(variant.key);
    row.innerHTML = `
      <div class="response-variant-head">
        <span class="response-variant-title">Вариант ${idx + 1}</span>
        <button type="button" class="btn btn-sm btn-outline-danger response-variant-remove" title="Удалить вариант"${responseVariants.length <= 1 ? ' disabled' : ''} aria-label="Удалить вариант"><i class="bi bi-x-lg"></i></button>
      </div>
      <textarea class="form-control bg-black text-light border-secondary" rows="6" id="${textareaId}" data-editor-target="response" placeholder="Текст ответа / промпт"></textarea>`;
    wrap.appendChild(row);

    const area = row.querySelector('textarea');
    if(area){
      area.value = variant.text;
      area.addEventListener('input', () => {
        variant.text = area.value;
        scheduleEmojiSetHelperRefresh();
      });
      area.addEventListener('focus', () => setActiveVariant(idx));
      area.addEventListener('click', () => setActiveVariant(idx));
      area.addEventListener('keyup', () => scheduleEmojiSetHelperRefresh());
      area.addEventListener('keydown', () => setActiveVariant(idx));
    }
    row.addEventListener('focusin', () => setActiveVariant(idx));
    row.addEventListener('mousedown', () => setActiveVariant(idx));
    const removeBtn = row.querySelector('.response-variant-remove');
    if(removeBtn){ removeBtn.addEventListener('click', () => removeResponseVariant(idx)); }
  });
  setActiveVariant(activeResponseVariant);
  applyActionTypeUI();
}

function ensureResponseEditor(){
  // Раньше здесь поднимался CodeMirror; варианты живут в обычных textarea,
  // поэтому достаточно убедиться, что окна отрисованы.
  if(!document.getElementById('response_variants')){ return; }
  if(!document.querySelector('#response_variants textarea')){ renderResponseVariants(); }
}

function getEditorView(){
  return null;
}

function setResponseValue(val){
  const variant = activeResponseVariantItem();
  if(!variant){ return; }
  const text = String(val ?? '');
  variant.text = text;
  const ta = document.getElementById(responseVariantElementId(variant.key));
  if(ta){ ta.value = text; }
  scheduleEmojiSetHelperRefresh();
}

function getResponseValue(){
  const variant = activeResponseVariantItem();
  if(!variant){ return ''; }
  return readVariantText(variant);
}

function addResponseVariant(){
  commitResponseVariants();
  responseVariants.push({key: nextResponseKey(), text: ''});
  activeResponseVariant = responseVariants.length - 1;
  renderResponseVariants();
  const variant = activeResponseVariantItem();
  const ta = variant ? document.getElementById(responseVariantElementId(variant.key)) : null;
  if(ta){ ta.focus(); }
}

function removeResponseVariant(idx){
  if(responseVariants.length <= 1){ return; }
  const target = Number(idx);
  if(!Number.isFinite(target) || target < 0 || target >= responseVariants.length){ return; }
  commitResponseVariants();
  responseVariants.splice(target, 1);
  if(activeResponseVariant >= responseVariants.length){ activeResponseVariant = responseVariants.length - 1; }
  renderResponseVariants();
}

function setResponseVariantsFromRaw(raw){
  responseVariants = normalizeResponseItems(raw);
  activeResponseVariant = 0;
  renderResponseVariants();
}

function forEachResponseTextArea(fn){
  responseVariants.forEach((variant) => {
    const ta = document.getElementById(responseVariantElementId(variant.key));
    if(ta){ fn(ta, variant); }
  });
}

function setResponsePlaceholder(text){
  forEachResponseTextArea((ta) => { ta.placeholder = text; });
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
    const r = await fetchAdmin(withToken('/trigger_bot/list'));
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
      headers: withCSRFHeaders({
        'Accept': 'application/json',
        'Content-Type': 'application/json'
      }),
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
      headers: withCSRFHeaders({'Content-Type': 'application/json', 'Accept': 'application/json'}),
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
      headers: withCSRFHeaders({'Content-Type': 'application/json', 'Accept': 'application/json'}),
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
  commitResponseVariants();
  const responseSnapshot = responseVariants.map((item) => ({key: item.key, text: item.text}));
  syncSwitch('f_enabled');
  syncSwitch('f_pass_through');
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
      pass_through: document.getElementById('f_pass_through')?.value === '1',
      reply: document.getElementById('f_reply')?.value === '1',
      preview: document.getElementById('f_preview')?.value === '1',
      delete_source: document.getElementById('f_delete_source')?.value === '1',
      response_text: responseVariants.map((it) => String(it.text ?? '')),
    };
    const res = await fetch(form.getAttribute('action') || withToken('/trigger_bot/save'), {
      method: 'POST',
      headers: withCSRFHeaders({'Content-Type': 'application/json', 'Accept': 'application/json'}),
      body: JSON.stringify(payload),
      signal: controller.signal,
      credentials: 'same-origin'
    });
    clearTimeout(timeout);
    if(!res.ok){
      const txt = await res.text();
      alert('Ошибка сохранения: ' + (txt || res.status));
      setResponseVariantsFromRaw(responseSnapshot);
      setSaveBusy(false);
      return false;
    }
    window.location.href = withToken('/trigger_bot');
  }catch(err){
    alert('Сохранение не удалось: ' + (err && err.message ? err.message : err));
    setResponseVariantsFromRaw(responseSnapshot);
    setSaveBusy(false);
  }
  return false;
}

function getResponseTextArea(){
  const variant = activeResponseVariantItem();
  if(variant){
    const el = document.getElementById(responseVariantElementId(variant.key));
    if(el){ return el; }
  }
  return document.querySelector('#response_variants textarea');
}

function getTemplateTextArea(){
  return document.getElementById('tpl_text');
}

// resolveEditorTarget находит поле по имени из data-target: тулбары и списки
// тегов больше не зависят от конкретных id — новое поле достаточно пометить
// data-editor-target="<имя>".
function resolveEditorTarget(name){
  const key = String(name || '').trim();
  if(key){
    // У вариантов ответа несколько окон: вставляем в то, где сейчас курсор.
    const active = document.activeElement;
    const row = active && active.closest ? active.closest('.response-variant') : null;
    if(row && key === 'response'){
      const idx = Number(row.getAttribute('data-variant-index'));
      const variant = responseVariants[idx];
      if(variant){
        const el = document.getElementById(responseVariantElementId(variant.key));
        if(el){ return el; }
      }
    }
    const el = document.querySelector(`[data-editor-target="${key}"]`);
    if(el){ return el; }
  }
  return getResponseTextArea();
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
  if(!el){ return; }
  const start = el.selectionStart ?? 0;
  const end = el.selectionEnd ?? 0;
  const val = el.value ?? '';
  el.value = val.slice(0, start) + text + val.slice(end);
  const caret = start + text.length;
  el.focus();
  el.setSelectionRange(caret, caret);
  scheduleEmojiSetHelperRefresh();
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
  const target = sel.getAttribute('data-target') || '';
  insertTextAtCursor(resolveEditorTarget(target), sel.value);
  sel.value = '';
}

function bindMiniToolbarFallback(){
  if(document.body.dataset.toolbarBound === '1'){ return; }
  document.body.dataset.toolbarBound = '1';
  document.addEventListener('click', (e) => {
    const btn = e.target && e.target.closest ? e.target.closest('button[data-action]') : null;
    if(!btn){ return; }
    const target = btn.closest('.mini-toolbar')?.getAttribute('data-target') || '';
    const targetEl = resolveEditorTarget(target);
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
    const btn = e.target && e.target.closest ? e.target.closest('button[data-schedule-clone-current]') : null;
    if(!btn){ return; }
    cloneSchedule();
  });

  document.addEventListener('click', (e) => {
    const btn = e.target && e.target.closest ? e.target.closest('button[data-schedule-edit]') : null;
    if(!btn){ return; }
    const id = String(btn.getAttribute('data-schedule-edit') || '');
    if(!id){ return; }
    openScheduleEdit(id);
  });

  document.addEventListener('click', (e) => {
    const btn = e.target && e.target.closest ? e.target.closest('button[data-schedule-toggle]') : null;
    if(!btn){ return; }
    const id = String(btn.getAttribute('data-schedule-toggle') || '');
    const enabled = String(btn.getAttribute('data-schedule-enabled') || '1') === '1';
    if(!id){ return; }
    toggleSchedule(id, enabled, btn);
  });

  document.addEventListener('click', (e) => {
    const btn = e.target && e.target.closest ? e.target.closest('button[data-schedule-delete]') : null;
    if(!btn){ return; }
    const id = String(btn.getAttribute('data-schedule-delete') || '');
    if(!id){ return; }
    deleteSchedule(id);
  });

  document.addEventListener('click', (e) => {
    const btn = e.target && e.target.closest ? e.target.closest('button[data-trigger-delete]') : null;
    if(!btn){ return; }
    const id = Number(btn.getAttribute('data-trigger-delete') || 0);
    if(!id){ return; }
    deleteTrigger(id, btn);
  });

  document.addEventListener('input', (ev) => {
    const target = ev.target;
    if(!target || target.id !== 'sch_cron'){ return; }
    const editor = cronEditorEl();
    if(editor){
      const next = normalizeCronParts(target.value).join(' ');
      if(editor.value !== next){ editor.value = next; }
    }
  });

  document.addEventListener('change', (ev) => {
    const target = ev.target;
    if(!target || !target.classList || !target.classList.contains('template-tag-picker')){ return; }
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
  if(!mt || !lbl || !inp){ return; }
  if(mt.value === 'idle'){
    lbl.textContent = 'Время простоя (мин)';
    inp.type = 'number';
    inp.min = '1';
    inp.step = '1';
    inp.placeholder = 'например, 120';
    if(area){ area.classList.add('d-none'); }
    if(toggle){ toggle.disabled = true; }
    inp.disabled = false;
  } else if(mt.value === 'new_member'){
    lbl.textContent = 'Текст триггера';
    inp.type = 'text';
    inp.removeAttribute('min');
    inp.removeAttribute('step');
    inp.placeholder = 'не используется';
    if(area){ area.classList.add('d-none'); }
    if(toggle){ toggle.disabled = true; }
    inp.disabled = true;
  } else if(['support_reactions', 'hype_reactions', 'funny_reactions', 'sad_reactions', 'angry_reactions'].includes(mt.value)){
    lbl.textContent = 'Порог реакций';
    inp.type = 'number';
    inp.min = '0';
    inp.step = '1';
    inp.placeholder = 'например, 3';
    if(area){ area.classList.add('d-none'); }
    if(toggle){ toggle.disabled = true; }
    inp.disabled = false;
  } else {
    lbl.textContent = 'Текст триггера';
    inp.type = 'text';
    inp.removeAttribute('min');
    inp.removeAttribute('step');
    inp.placeholder = 'Текст триггера';
    if(toggle){ toggle.disabled = false; }
    inp.disabled = false;
  }
}

function applyActionTypeUI(){
  const action = String(document.getElementById('f_action_type')?.value || 'send').toLowerCase();
  if(!document.getElementById('response_variants')){ return; }
  if(action === 'send_sticker'){
    setResponsePlaceholder('Код стикера: file_id:set_id (или только file_id)');
    scheduleEmojiSetHelperRefresh();
    return;
  }
  if(action === 'send_voice'){
    setResponsePlaceholder('file_id, URL или путь к voice-файлу; со второй строки можно указать подпись');
    scheduleEmojiSetHelperRefresh();
    return;
  }
  if(action === 'gpt_prompt'){
    setResponsePlaceholder('Промпт для ChatGPT');
    scheduleEmojiSetHelperRefresh();
    return;
  }
  setResponsePlaceholder('Текст ответа / промпт');
  scheduleEmojiSetHelperRefresh();
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
  return path;
}

function applyTokenToForms(){
  // query-token auth is removed; keep function for backward compatibility.
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
      headers: withCSRFHeaders({'Content-Type': 'application/json'}),
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
  const chanceValue = Number(pick(t,'chance','Chance',100) || 100);
  ensureChanceOption(chanceValue);
  document.getElementById('f_chance').value=String(chanceValue);
  setSel('f_trigger_mode', pick(t,'trigger_mode','TriggerMode','all'));
  setSel('f_admin_mode', pick(t,'admin_mode','AdminMode','anybody'));
  setSel('f_match_type', pick(t,'match_type','MatchType','full'));
  applyMatchTypeUI();
  setMatchTextValue(pick(t,'match_text','MatchText',''));
  setSel('f_action_type', pick(t,'action_type','ActionType','send'));
  setBool('f_enabled', !!pick(t,'enabled','Enabled',true));
  setBool('f_pass_through', !!pick(t,'pass_through','PassThrough',false));
  setBool('f_reply', !!pick(t,'reply','Reply',true));
  setBool('f_preview', !!pick(t,'preview','Preview',false));
  setBool('f_delete_source', !!pick(t,'delete_source','DeleteSource',false));
}

function ensureChanceOption(value){
  const sel = document.getElementById('f_chance');
  if(!sel){ return; }
  const v = String(Number(value || 100));
  for(const opt of sel.options){
    if(String(opt.value) === v){ return; }
  }
  const opt = document.createElement('option');
  opt.value = v;
  if(Number(v) > 100){
    const div = Number(v) - 100;
    const hours = div > 0 ? (24 / div) : 0;
    const labelHours = Number.isFinite(hours) && hours > 0 ? hours : 0;
    opt.textContent = `${v} — раз в ${labelHours} ч`;
  }else{
    opt.textContent = `${v}%`;
  }
  sel.appendChild(opt);
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
    pass_through: false,
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
if(!window.Alpine){
  initTriggerPage();
}
document.addEventListener('DOMContentLoaded', () => {
  setTimeout(() => {
    const gate = document.getElementById('auth_gate');
    const app = document.getElementById('admin_app_shell');
    if(gate?.classList.contains('d-none') && app?.classList.contains('d-none')){
      console.warn('trigger admin init watchdog fired');
      initTriggerPage();
    }
  }, 500);
});

// --- MTProto QR login -------------------------------------------------------
let __mtprotoQRPollTimer = null;
let __mtprotoQRChatID = 0;

function mtprotoQRStopPolling(){
  if(__mtprotoQRPollTimer){
    clearInterval(__mtprotoQRPollTimer);
    __mtprotoQRPollTimer = null;
  }
}

function applyMTProtoQRState(data){
  const hint = document.getElementById('mtproto_qr_hint');
  const box = document.getElementById('mtproto_qr_box');
  const img = document.getElementById('mtproto_qr_img');
  const pwBox = document.getElementById('mtproto_qr_pw_box');
  const status = String(data?.status || '');
  if(hint){
    hint.classList.remove('text-danger');
    if(status === 'waiting_scan'){ hint.textContent = 'Сканируйте QR из Telegram: Настройки → Устройства → Подключить устройство.'; }
    else if(status === 'need_password'){ hint.textContent = 'Вход подтверждён, нужен облачный пароль (2FA) этого аккаунта.'; }
    else if(status === 'authorized'){ hint.textContent = 'Готово: сессия привязана к чату.'; }
    else if(status === 'stopped'){ hint.textContent = 'Ожидание снято.'; }
    else if(status === 'error'){ hint.textContent = 'Ошибка: ' + (data?.error || 'неизвестно'); hint.classList.add('text-danger'); }
    else { hint.textContent = String(data?.error || ''); }
  }
  if(img && data?.png_b64){
    img.src = 'data:image/png;base64,' + data.png_b64;
  }
  if(box){
    box.style.display = (status === 'waiting_scan' && data?.png_b64) ? '' : 'none';
  }
  if(pwBox){
    pwBox.style.display = (status === 'need_password') ? '' : 'none';
  }
  if(status === 'authorized' || status === 'error' || status === 'stopped' || status === 'need_password'){
    mtprotoQRStopPolling();
  }
}

async function pollMTProtoQRState(){
  if(!__mtprotoQRChatID){ mtprotoQRStopPolling(); return; }
  try{
    const r = await fetchAdmin(withToken('/trigger_bot/mtproto_qr_state?chat_id=' + encodeURIComponent(__mtprotoQRChatID)));
    const data = await r.json().catch(() => ({}));
    if(data?.ok){ applyMTProtoQRState(data); }
  } catch(_){ /* transient */ }
}

async function startMTProtoQR(){
  const chatID = Number(document.getElementById('mtproto_chat_id')?.value || 0);
  const hint = document.getElementById('mtproto_qr_hint');
  if(!chatID){
    if(hint){ hint.textContent = 'Сначала выберите чат.'; hint.classList.add('text-danger'); }
    return;
  }
  __mtprotoQRChatID = chatID;
  if(hint){ hint.textContent = 'Генерирую QR…'; hint.classList.remove('text-danger'); }
  const btn = document.getElementById('mtproto_qr_start_btn');
  lockButton(btn);
  try{
    const r = await fetch(withToken('/trigger_bot/mtproto_qr_start'), {
      method: 'POST',
      headers: withCSRFHeaders({'Content-Type': 'application/json', 'Accept': 'application/json'}),
      body: JSON.stringify({chat_id: chatID}),
      credentials: 'same-origin'
    });
    const data = await r.json().catch(() => ({}));
    if(!r.ok || !data?.ok){
      if(hint){ hint.textContent = 'Ошибка: ' + (data?.error || r.status); hint.classList.add('text-danger'); }
      return;
    }
    applyMTProtoQRState(data);
    mtprotoQRStopPolling();
    __mtprotoQRPollTimer = setInterval(pollMTProtoQRState, 2000);
  } finally {
    unlockButton(btn);
  }
}

async function submitMTProtoQRPassword(){
  const chatID = __mtprotoQRChatID || Number(document.getElementById('mtproto_chat_id')?.value || 0);
  const input = document.getElementById('mtproto_qr_password');
  const password = String(input?.value || '');
  const hint = document.getElementById('mtproto_qr_hint');
  if(!chatID || !password){
    if(hint){ hint.textContent = 'Нужны чат и пароль.'; hint.classList.add('text-danger'); }
    return;
  }
  const btn = document.getElementById('mtproto_qr_pw_btn');
  lockButton(btn);
  try{
    const r = await fetch(withToken('/trigger_bot/mtproto_qr_password'), {
      method: 'POST',
      headers: withCSRFHeaders({'Content-Type': 'application/json', 'Accept': 'application/json'}),
      body: JSON.stringify({chat_id: chatID, password}),
      credentials: 'same-origin'
    });
    const data = await r.json().catch(() => ({}));
    if(!r.ok || !data?.ok){
      if(hint){ hint.textContent = 'Ошибка: ' + (data?.error || r.status); hint.classList.add('text-danger'); }
      return;
    }
    if(input){ input.value = ''; }
    applyMTProtoQRState(data);
  } finally {
    unlockButton(btn);
  }
}

async function stopMTProtoQR(){
  mtprotoQRStopPolling();
  const chatID = __mtprotoQRChatID || Number(document.getElementById('mtproto_chat_id')?.value || 0);
  if(!chatID){ return; }
  try{
    await fetch(withToken('/trigger_bot/mtproto_qr_stop'), {
      method: 'POST',
      headers: withCSRFHeaders({'Content-Type': 'application/json', 'Accept': 'application/json'}),
      body: JSON.stringify({chat_id: chatID}),
      credentials: 'same-origin'
    });
  } catch(_){ /* ignore */ }
  applyMTProtoQRState({status: 'stopped'});
}
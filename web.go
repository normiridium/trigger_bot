package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type WebAdmin struct {
	store      *Store
	adminToken string
}

func NewWebAdmin(store *Store, adminToken string) *WebAdmin {
	return &WebAdmin{store: store, adminToken: strings.TrimSpace(adminToken)}
}

func (w *WebAdmin) authOK(r *http.Request) bool {
	if w.adminToken == "" {
		return true
	}
	if strings.TrimSpace(r.URL.Query().Get("token")) == w.adminToken {
		return true
	}
	if strings.TrimSpace(r.Header.Get("X-Admin-Token")) == w.adminToken {
		return true
	}
	return false
}

func (w *WebAdmin) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if !w.authOK(r) {
			rw.WriteHeader(http.StatusUnauthorized)
			_, _ = rw.Write([]byte("доступ запрещен"))
			return
		}
		next(rw, r)
	}
}

func (w *WebAdmin) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, "/trigger_bot", http.StatusFound)
	})
	mux.HandleFunc("/trigger_bot", w.withAuth(w.listPage))
	mux.HandleFunc("/trigger_bot/get", w.withAuth(w.getJSON))
	mux.HandleFunc("/trigger_bot/save", w.withAuth(w.savePost))
	mux.HandleFunc("/trigger_bot/toggle", w.withAuth(w.togglePost))
	mux.HandleFunc("/trigger_bot/delete", w.withAuth(w.deletePost))
	mux.HandleFunc("/trigger_bot/export", w.withAuth(w.exportGet))
	mux.HandleFunc("/trigger_bot/import", w.withAuth(w.importPost))
	// legacy route compatibility
	mux.HandleFunc("/trigger_bot/edit", w.withAuth(func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, "/trigger_bot", http.StatusFound)
	}))
	return mux
}

func (w *WebAdmin) listPage(rw http.ResponseWriter, r *http.Request) {
	items, err := w.store.ListTriggers()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = rw.Write([]byte(renderListHTML(items)))
}

func (w *WebAdmin) getJSON(rw http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	var item *Trigger
	if id > 0 {
		var err error
		item, err = w.store.GetTrigger(id)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if item == nil {
		item = &Trigger{Enabled: true, TriggerMode: "all", AdminMode: "anybody", MatchType: "full", ActionType: "send", Chance: 100, Reply: true}
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(item)
}

func (w *WebAdmin) savePost(rw http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	id, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	chance, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("chance")))
	t := Trigger{
		ID:            id,
		Title:         r.FormValue("title"),
		Enabled:       r.FormValue("enabled") == "1",
		TriggerMode:   r.FormValue("trigger_mode"),
		AdminMode:     r.FormValue("admin_mode"),
		MatchText:     r.FormValue("match_text"),
		MatchType:     r.FormValue("match_type"),
		CaseSensitive: r.FormValue("case_sensitive") == "1",
		ActionType:    r.FormValue("action_type"),
		ResponseText:  r.FormValue("response_text"),
		Reply:         r.FormValue("reply") == "1",
		Preview:       r.FormValue("preview") == "1",
		Chance:        chance,
	}
	if err := w.store.SaveTrigger(t); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(rw, r, "/trigger_bot", http.StatusFound)
}

func (w *WebAdmin) togglePost(rw http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	id, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if id > 0 {
		_ = w.store.ToggleTrigger(id)
	}
	http.Redirect(rw, r, "/trigger_bot", http.StatusFound)
}

func (w *WebAdmin) deletePost(rw http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	id, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if id > 0 {
		_ = w.store.DeleteTrigger(id)
	}
	http.Redirect(rw, r, "/trigger_bot", http.StatusFound)
}

func (w *WebAdmin) exportGet(rw http.ResponseWriter, r *http.Request) {
	body, err := w.store.ExportJSON()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.Header().Set("Content-Disposition", `attachment; filename="triggers.json"`)
	_, _ = rw.Write(body)
}

func (w *WebAdmin) importPost(rw http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		_ = r.ParseForm()
	}
	var raw string
	if file, _, err := r.FormFile("file"); err == nil {
		defer file.Close()
		body, _ := io.ReadAll(file)
		raw = string(body)
	}
	if strings.TrimSpace(raw) == "" {
		raw = r.FormValue("raw")
	}
	added, err := w.store.ImportJSON([]byte(raw))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = rw.Write([]byte(fmt.Sprintf("Импортировано: %d", added)))
}

func renderListHTML(items []Trigger) string {
	rows := make([]string, 0, len(items))
	for _, it := range items {
		status := `<span class="badge text-bg-secondary">ВЫКЛ</span>`
		if it.Enabled {
			status = `<span class="badge text-bg-success">ВКЛ</span>`
		}
		row := fmt.Sprintf(`<tr>
<td>%d</td>
<td>%s</td>
<td><code>%s</code> <small>(%s)</small></td>
<td><small>%s / %s</small></td>
<td>%s</td>
<td class="text-nowrap">
<button class="btn btn-sm btn-outline-primary" type="button" onclick="openEdit(%d)" title="Редактировать" aria-label="Редактировать"><i class="bi bi-pencil"></i></button>
<form class="d-inline ms-1" method="post" action="/trigger_bot/toggle"><input type="hidden" name="id" value="%d"><button class="btn btn-sm btn-outline-warning" type="submit" title="Вкл/Выкл" aria-label="Вкл/Выкл"><i class="bi bi-power"></i></button></form>
<form class="d-inline ms-1" method="post" action="/trigger_bot/delete" onsubmit="return confirm('Удалить?')"><input type="hidden" name="id" value="%d"><button class="btn btn-sm btn-outline-danger" type="submit" title="Удалить" aria-label="Удалить"><i class="bi bi-x-lg"></i></button></form>
</td>
</tr>`,
			it.ID, htmlEsc(it.Title), htmlEsc(it.MatchText), htmlEsc(it.MatchType), htmlEsc(it.ActionType), htmlEsc(it.AdminMode), status, it.ID, it.ID, it.ID)
		rows = append(rows, row)
	}

	return `<!doctype html>
<html lang="ru">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Триггеры</title>
  <link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/css/bootstrap.min.css" rel="stylesheet">
  <link href="https://cdn.jsdelivr.net/npm/bootstrap-icons@1.11.3/font/bootstrap-icons.min.css" rel="stylesheet">
  <style>
    body { background: #111827; }
    .app { max-width: 1280px; }
    .table code { font-size: .9rem; }
  </style>
</head>
<body class="text-light">
<div class="container-fluid py-4 app">
  <div class="d-flex flex-wrap align-items-center gap-2 mb-3">
    <h3 class="m-0 me-auto">Триггеры</h3>
    <button class="btn btn-success" type="button" onclick="openNew()"><i class="bi bi-plus-lg"></i> Новый</button>
    <a class="btn btn-primary" id="export_link" href="/trigger_bot/export"><i class="bi bi-download"></i> Экспорт</a>
  </div>

  <form method="post" action="/trigger_bot/import" enctype="multipart/form-data" class="row g-2 mb-3">
    <div class="col-auto"><input class="form-control" type="file" name="file" accept="application/json"></div>
    <div class="col-auto"><button class="btn btn-secondary" type="submit"><i class="bi bi-upload"></i> Импорт JSON</button></div>
  </form>

  <div class="table-responsive rounded border border-secondary-subtle">
    <table class="table table-dark table-striped table-hover align-middle mb-0">
      <thead>
        <tr><th>ID</th><th>Название</th><th>Условие</th><th>Режим</th><th>Статус</th><th class="text-nowrap">Управление</th></tr>
      </thead>
      <tbody>` + strings.Join(rows, "\n") + `</tbody>
    </table>
  </div>
</div>

<div class="modal fade" id="triggerModal" tabindex="-1" aria-hidden="true">
  <div class="modal-dialog modal-xl modal-dialog-scrollable">
    <div class="modal-content bg-dark text-light border-secondary">
      <div class="modal-header border-secondary">
        <h5 class="modal-title">Триггер</h5>
        <button type="button" class="btn-close btn-close-white" data-bs-dismiss="modal"></button>
      </div>
      <form method="post" action="/trigger_bot/save">
        <div class="modal-body">
          <input type="hidden" name="id" id="f_id">

          <div class="mb-3">
            <label class="form-label">Название</label>
            <input class="form-control bg-black text-light border-secondary" type="text" name="title" id="f_title">
          </div>

          <div class="row g-3">
            <div class="col-md-6">
              <label class="form-label">Режим триггера</label>
              <select class="form-select bg-black text-light border-secondary" name="trigger_mode" id="f_trigger_mode">
                <option value="all">Все сообщения</option>
                <option value="only_replies">Только реплаи</option>
                <option value="only_replies_to_any_bot">Только реплаи на любого бота</option>
                <option value="only_replies_to_combot">Реплай к боту триггера</option>
                <option value="never_on_replies">Не срабатывать на реплаи</option>
                <option value="command_reply">Режим команд (действие в ответ)</option>
              </select>
            </div>
            <div class="col-md-6">
              <label class="form-label">Режим админов</label>
              <select class="form-select bg-black text-light border-secondary" name="admin_mode" id="f_admin_mode">
                <option value="anybody">Любой участник</option>
                <option value="admins">Только админы</option>
                <option value="not_admins">Кроме админов</option>
              </select>
            </div>

            <div class="col-md-6">
              <label class="form-label">Текст триггера</label>
              <input class="form-control bg-black text-light border-secondary" type="text" name="match_text" id="f_match_text">
            </div>
            <div class="col-md-6">
              <label class="form-label">Тип условия</label>
              <select class="form-select bg-black text-light border-secondary" name="match_type" id="f_match_type">
                <option value="full">Полное совпадение</option>
                <option value="partial">Частичное совпадение</option>
                <option value="regex">Регулярное выражение</option>
                <option value="starts">Начинается с</option>
                <option value="ends">Заканчивается на</option>
              </select>
            </div>

            <div class="col-md-6">
              <label class="form-label">Тип действия</label>
              <select class="form-select bg-black text-light border-secondary" name="action_type" id="f_action_type">
                <option value="send">Отправить сообщение</option>
                <option value="delete">Удалить сообщение</option>
                <option value="gpt_prompt">Промпт в ChatGPT</option>
              </select>
            </div>
            <div class="col-md-6">
              <label class="form-label">Шанс (1-100)</label>
              <input class="form-control bg-black text-light border-secondary" type="number" name="chance" min="1" max="100" id="f_chance">
            </div>

            <div class="col-md-6">
              <label class="form-label">Включен</label>
              <select class="form-select bg-black text-light border-secondary" name="enabled" id="f_enabled"><option value="1">Да</option><option value="0">Нет</option></select>
            </div>
            <div class="col-md-6">
              <label class="form-label">Учитывать регистр</label>
              <select class="form-select bg-black text-light border-secondary" name="case_sensitive" id="f_case_sensitive"><option value="0">Нет</option><option value="1">Да</option></select>
            </div>

            <div class="col-md-6">
              <label class="form-label">Отправлять reply</label>
              <select class="form-select bg-black text-light border-secondary" name="reply" id="f_reply"><option value="1">Да</option><option value="0">Нет</option></select>
            </div>
            <div class="col-md-6">
              <label class="form-label">Показывать превью ссылки</label>
              <select class="form-select bg-black text-light border-secondary" name="preview" id="f_preview"><option value="0">Нет</option><option value="1">Да</option></select>
            </div>

            <div class="col-12">
              <label class="form-label">Текст ответа / промпт (для ChatGPT можно использовать {{message}})</label>
              <textarea class="form-control bg-black text-light border-secondary" rows="9" name="response_text" id="f_response_text"></textarea>
            </div>
          </div>
        </div>
        <div class="modal-footer border-secondary">
          <button class="btn btn-success" type="submit"><i class="bi bi-floppy"></i> Сохранить</button>
          <button class="btn btn-outline-light" type="button" data-bs-dismiss="modal">Закрыть</button>
        </div>
      </form>
    </div>
  </div>
</div>

<script src="https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/js/bootstrap.bundle.min.js"></script>
<script>
function setSel(id,val){const el=document.getElementById(id); if(el && val!=null){el.value=String(val)}}
function setBool(id,v){setSel(id, v ? '1' : '0')}
function openModal(){window.__trgModal.show()}
function closeModal(){window.__trgModal.hide()}
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
function fillForm(t){
  document.getElementById('f_id').value=pick(t,'id','ID','');
  document.getElementById('f_title').value=pick(t,'title','Title','');
  document.getElementById('f_match_text').value=pick(t,'match_text','MatchText','');
  document.getElementById('f_response_text').value=pick(t,'response_text','ResponseText','');
  document.getElementById('f_chance').value=pick(t,'chance','Chance',100);
  setSel('f_trigger_mode', pick(t,'trigger_mode','TriggerMode','all'));
  setSel('f_admin_mode', pick(t,'admin_mode','AdminMode','anybody'));
  setSel('f_match_type', pick(t,'match_type','MatchType','full'));
  setSel('f_action_type', pick(t,'action_type','ActionType','send'));
  setBool('f_enabled', !!pick(t,'enabled','Enabled',true));
  setBool('f_case_sensitive', !!pick(t,'case_sensitive','CaseSensitive',false));
  setBool('f_reply', !!pick(t,'reply','Reply',true));
  setBool('f_preview', !!pick(t,'preview','Preview',false));
}
async function openNew(){
  const r=await fetch(withToken('/trigger_bot/get'));
  if(!r.ok){ alert('Не удалось открыть форму: ' + r.status); return; }
  const t=await r.json();
  fillForm(t); openModal();
}
async function openEdit(id){
  const r=await fetch(withToken('/trigger_bot/get?id='+encodeURIComponent(id)));
  if(!r.ok){ alert('Не удалось загрузить триггер: ' + r.status); return; }
  const t=await r.json();
  fillForm(t); openModal();
}
window.__trgModal = new bootstrap.Modal(document.getElementById('triggerModal'));
applyTokenToForms();
</script>
</body></html>`
}

func htmlEsc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

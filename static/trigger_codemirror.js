const CDN = {
  state: [
    "https://cdn.jsdelivr.net/npm/@codemirror/state@6.4.1/+esm",
    "https://unpkg.com/@codemirror/state@6.4.1?module",
  ],
  view: [
    "https://cdn.jsdelivr.net/npm/@codemirror/view@6.23.0/+esm",
    "https://unpkg.com/@codemirror/view@6.23.0?module",
  ],
  commands: [
    "https://cdn.jsdelivr.net/npm/@codemirror/commands@6.5.0/+esm",
    "https://unpkg.com/@codemirror/commands@6.5.0?module",
  ],
  langHtml: [
    "https://cdn.jsdelivr.net/npm/@codemirror/lang-html@6.4.11/+esm",
    "https://unpkg.com/@codemirror/lang-html@6.4.11?module",
  ],
};

const editors = new Map(); // id textarea → EditorView
let activeEditorId = '';
let cm = null;
const CDN_IMPORT_TIMEOUT_MS = 5000;

function importModule(url){
  let timer = null;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(`module import timeout: ${url}`)), CDN_IMPORT_TIMEOUT_MS);
  });
  return Promise.race([import(url), timeout]).finally(() => {
    if(timer){ clearTimeout(timer); }
  });
}

async function importWithFallback(urls){
  let lastErr = null;
  for(const url of urls){
    try{
      return await importModule(url);
    } catch(err){
      lastErr = err;
    }
  }
  throw lastErr || new Error("module import failed");
}

async function loadCodeMirror(){
  if(cm){ return cm; }
  const [stateMod, viewMod, commandsMod, htmlMod] = await Promise.all([
    importWithFallback(CDN.state),
    importWithFallback(CDN.view),
    importWithFallback(CDN.commands),
    importWithFallback(CDN.langHtml),
  ]);
  cm = {
    EditorState: stateMod.EditorState,
    EditorView: viewMod.EditorView,
    keymap: viewMod.keymap,
    defaultKeymap: commandsMod.defaultKeymap,
    history: commandsMod.history,
    historyKeymap: commandsMod.historyKeymap,
    html: htmlMod.html,
  };
  return cm;
}

function createEditor(textarea, container){
  const startDoc = textarea.value || '';
  const changeListener = cm.EditorView.updateListener.of((vu) => {
    if(vu.docChanged){
      window.dispatchEvent(new Event("response-editor-change"));
    }
    if(vu.selectionSet){
      window.dispatchEvent(new Event("response-editor-selection"));
    }
  });
  const state = cm.EditorState.create({
    doc: startDoc,
    extensions: [
      cm.history(),
      cm.keymap.of([...cm.defaultKeymap, ...cm.historyKeymap]),
      cm.html(),
      changeListener,
      cm.EditorView.lineWrapping,
      cm.EditorView.theme({
        "&": {height: "100%"},
        ".cm-content": {fontFamily: "inherit", fontSize: "0.95rem"},
        ".cm-scroller": {overflow: "auto"},
      }),
    ],
  });
  const view = new cm.EditorView({
    state,
    parent: container,
  });
  const id = String(textarea.id || '');
  if(id){
    editors.set(id, view);
    activeEditorId = id;
    const mark = () => { activeEditorId = id; };
    container.addEventListener('focusin', mark);
    container.addEventListener('mousedown', mark);
  }
  return view;
}

window.initResponseEditor = async function initResponseEditor(textareaId, containerId){
  const id = String(textareaId || '');
  if(!id){ return null; }
  if(editors.has(id)){ return editors.get(id); }
  const textarea = document.getElementById(id);
  const container = document.getElementById(containerId);
  if(!textarea || !container){ return null; }
  try{
    await loadCodeMirror();
    return createEditor(textarea, container);
  } catch(err){
    console.warn("CodeMirror unavailable, fallback to textarea:", err);
    return null;
  }
};

// getResponseEditor(id) — редактор конкретной textarea; без id — тот, где
// сейчас курсор (последний сфокусированный), иначе первый созданный.
window.getResponseEditor = function getResponseEditor(id){
  const key = String(id || '');
  if(key && editors.has(key)){ return editors.get(key); }
  if(activeEditorId && editors.has(activeEditorId)){ return editors.get(activeEditorId); }
  const first = editors.values().next();
  return first.done ? null : first.value;
};

window.destroyResponseEditor = function destroyResponseEditor(id){
  const key = String(id || '');
  const view = editors.get(key);
  if(!view){ return; }
  view.destroy();
  editors.delete(key);
  if(activeEditorId === key){ activeEditorId = ''; }
};

window.dispatchEvent(new Event("codemirror-ready"));

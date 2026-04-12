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
    "https://cdn.jsdelivr.net/npm/@codemirror/lang-html@6.10.2/+esm",
    "https://unpkg.com/@codemirror/lang-html@6.10.2?module",
  ],
};

let responseEditor = null;
let cm = null;

async function importWithFallback(urls){
  let lastErr = null;
  for(const url of urls){
    try{
      return await import(url);
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
  const state = cm.EditorState.create({
    doc: startDoc,
    extensions: [
      cm.history(),
      cm.keymap.of([...cm.defaultKeymap, ...cm.historyKeymap]),
      cm.html(),
      cm.EditorView.lineWrapping,
      cm.EditorView.theme({
        "&": {height: "100%"},
        ".cm-content": {fontFamily: "inherit", fontSize: "0.95rem"},
        ".cm-scroller": {overflow: "auto"},
      }),
    ],
  });
  responseEditor = new cm.EditorView({
    state,
    parent: container,
  });
  return responseEditor;
}

window.initResponseEditor = async function initResponseEditor(textareaId, containerId){
  const textarea = document.getElementById(textareaId);
  const container = document.getElementById(containerId);
  if(!textarea || !container){ return null; }
  if(responseEditor){ return responseEditor; }
  try{
    await loadCodeMirror();
    return createEditor(textarea, container);
  } catch(err){
    console.warn("CodeMirror unavailable, fallback to textarea:", err);
    return null;
  }
};

window.getResponseEditor = function getResponseEditor(){
  return responseEditor;
};

window.dispatchEvent(new Event("codemirror-ready"));

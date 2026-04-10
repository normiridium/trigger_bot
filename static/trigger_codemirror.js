import {EditorState} from "https://cdn.jsdelivr.net/npm/@codemirror/state@6.4.1/+esm";
import {EditorView, keymap} from "https://cdn.jsdelivr.net/npm/@codemirror/view@6.23.0/+esm";
import {defaultKeymap, history, historyKeymap} from "https://cdn.jsdelivr.net/npm/@codemirror/commands@6.5.0/+esm";
import {html} from "https://cdn.jsdelivr.net/npm/@codemirror/lang-html@6.10.2/+esm";

let responseEditor = null;

function createEditor(textarea, container){
  const startDoc = textarea.value || '';
  const state = EditorState.create({
    doc: startDoc,
    extensions: [
      history(),
      keymap.of([...defaultKeymap, ...historyKeymap]),
      html(),
      EditorView.lineWrapping,
      EditorView.theme({
        "&": {height: "100%"},
        ".cm-content": {fontFamily: "inherit", fontSize: "0.95rem"},
        ".cm-scroller": {overflow: "auto"},
      }),
    ],
  });
  responseEditor = new EditorView({
    state,
    parent: container,
  });
  return responseEditor;
}

window.initResponseEditor = function initResponseEditor(textareaId, containerId){
  const textarea = document.getElementById(textareaId);
  const container = document.getElementById(containerId);
  if(!textarea || !container){ return null; }
  if(responseEditor){ return responseEditor; }
  const editor = createEditor(textarea, container);
  return editor;
};

window.getResponseEditor = function getResponseEditor(){
  return responseEditor;
};

window.dispatchEvent(new Event('codemirror-ready'));

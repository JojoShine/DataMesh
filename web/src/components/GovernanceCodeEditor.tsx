import {useEffect,useRef} from "react";
import {EditorState} from "@codemirror/state";
import {EditorView,keymap,lineNumbers,highlightActiveLine,drawSelection} from "@codemirror/view";
import {defaultKeymap,history,historyKeymap} from "@codemirror/commands";
import {autocompletion} from "@codemirror/autocomplete";
import {syntaxHighlighting,defaultHighlightStyle} from "@codemirror/language";
import {python} from "@codemirror/lang-python";
import {javascript} from "@codemirror/lang-javascript";

export default function GovernanceCodeEditor({language,value,inputFields,errorLocation,readOnly=false,onChange}:{language:"python"|"javascript";value:string;inputFields:string[];errorLocation?:{line:number;column:number};readOnly?:boolean;onChange:(value:string)=>void}){
 const parent=useRef<HTMLDivElement>(null);const view=useRef<EditorView|null>(null);const change=useRef(onChange);change.current=onChange;const fields=inputFields.join(",");
 useEffect(()=>{if(!parent.current)return;const editor=new EditorView({parent:parent.current,state:EditorState.create({doc:value,extensions:[lineNumbers(),syntaxHighlighting(defaultHighlightStyle),drawSelection(),highlightActiveLine(),history(),keymap.of([...defaultKeymap,...historyKeymap]),language==="python"?python():javascript(),EditorState.readOnly.of(readOnly),EditorView.editable.of(!readOnly),EditorView.contentAttributes.of({"aria-label":"自定义处理代码"}),autocompletion({override:[context=>{const access=context.matchBefore(/row(?:\.[A-Za-z_0-9]*|\[(?:"[^"]*|'[^']*))$/);const word=context.matchBefore(/\w*/);if(!access&&!context.explicit)return null;const match=access??word;if(!match)return null;const tail=context.state.doc.sliceString(context.pos,context.pos+2);return {from:match.from,to:access&&/^["']\]$/.test(tail)?context.pos+2:context.pos,options:inputFields.map(label=>({label,type:"property",apply:`row[${JSON.stringify(label)}]`}))}}]}),EditorView.updateListener.of(update=>{if(update.docChanged)change.current(update.state.doc.toString())}),EditorView.theme({"&":{height:"320px",border:"1px solid #d9e4f3",borderRadius:"8px",fontSize:"15px"},".cm-scroller":{overflow:"auto"},".cm-content":{fontFamily:"monospace"}})]})});view.current=editor;return()=>{editor.destroy();view.current=null}},[language,readOnly,fields]);
 useEffect(()=>{const editor=view.current;if(editor&&editor.state.doc.toString()!==value)editor.dispatch({changes:{from:0,to:editor.state.doc.length,insert:value}})},[value]);
 useEffect(()=>{const editor=view.current;if(!editor||!errorLocation?.line)return;const line=editor.state.doc.line(Math.min(errorLocation.line,editor.state.doc.lines));const position=Math.min(line.to,line.from+Math.max(0,errorLocation.column-1));editor.dispatch({selection:{anchor:position},effects:EditorView.scrollIntoView(position,{y:"center"})})},[errorLocation]);
 return <div ref={parent} className="governance-code-editor"/>;
}

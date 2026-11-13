import {applicationURL} from "../app/basePath";
import {Button,Popover,Tag} from "antd";
import {useEffect,useId,useRef,useState} from "react";
import "./FloatingAgent.css";

function AgentMark(){
 return <img src={applicationURL("/static/design/robot-digital.png")} alt="" aria-hidden="true"/>;
}

export function FloatingAgent({workbench=false}:{workbench?:boolean}){
 const [open,setOpen]=useState(false),[blocked,setBlocked]=useState(false),[bottom,setBottom]=useState(workbench?96:24);
 const trigger=useRef<HTMLButtonElement>(null),closeButton=useRef<HTMLButtonElement>(null);
 const id=useId();
 function close(){setOpen(false);trigger.current?.focus()}
 useEffect(()=>{
  let frame=0;const observed=new Set<Element>();
  const schedule=()=>{if(!frame)frame=requestAnimationFrame(()=>{frame=0;measure()})};
  const resize=new ResizeObserver(schedule);
  function measure(){
   const modalOpen=Array.from(document.querySelectorAll(".ant-modal-wrap, .ant-drawer-open")).some(element=>getComputedStyle(element).display!=="none");
   setBlocked(modalOpen);if(modalOpen)setOpen(false);
   let clearance=workbench?96:24;
   for(const footer of document.querySelectorAll(".governance-flow-page-footer, .lightweight-actions")){
    if(!observed.has(footer)){observed.add(footer);resize.observe(footer)}
    const rect=footer.getBoundingClientRect();
    if(rect.height>0&&rect.bottom>=window.innerHeight-8&&rect.top<window.innerHeight)
     clearance=Math.max(clearance,window.innerHeight-rect.top+16);
   }
   setBottom(Math.min(clearance,Math.max(24,window.innerHeight-88)));
  }
  // Dialogs and sticky command bars are mounted after the page itself.
  const mutations=new MutationObserver(schedule);
  mutations.observe(document.body,{subtree:true,childList:true,attributes:true,attributeFilter:["class","style"]});
  resize.observe(document.body);measure();
  window.addEventListener("resize",schedule);window.addEventListener("scroll",schedule,true);
  return()=>{cancelAnimationFrame(frame);resize.disconnect();mutations.disconnect();window.removeEventListener("resize",schedule);window.removeEventListener("scroll",schedule,true)};
 },[workbench]);
 useEffect(()=>{
  if(!open)return;
  const frame=requestAnimationFrame(()=>closeButton.current?.focus());
  const escape=(event:KeyboardEvent)=>{if(event.key==="Escape"){event.preventDefault();setOpen(false);trigger.current?.focus()}};
  document.addEventListener("keydown",escape);
  return()=>{cancelAnimationFrame(frame);document.removeEventListener("keydown",escape)};
 },[open]);
 if(blocked)return null;
 return <Popover open={open} onOpenChange={setOpen} trigger="click" placement="topRight" arrow={false} fresh destroyOnHidden classNames={{root:"agent-popover"}} content={
  <section id={id} role="dialog" aria-hidden={!open} inert={!open} aria-modal="false" aria-labelledby={`${id}-title`} className="agent-panel">
   <div className="agent-panel-heading"><span className="agent-panel-mark"><AgentMark/></span><div><h2 id={`${id}-title`}>AI智能问数</h2><Tag>待接入</Tag></div></div>
   <div className="agent-panel-copy"><p>用自然语言探索数据</p><p>智能问数正在准备中。后续可在这里查询已授权的数据，查看分析结果与数据来源。</p><span className="helper-text">例如：今年各街道的招生报名人数是多少？</span></div>
   <div className="agent-panel-footer"><Button ref={closeButton} onClick={close}>关闭</Button></div>
  </section>
 }>
  <button ref={trigger} type="button" className="agent-launcher" aria-label="打开AI智能问数" title="AI智能问数" aria-haspopup="dialog" aria-expanded={open} aria-controls={open?id:undefined} style={{bottom:`calc(${bottom}px + env(safe-area-inset-bottom, 0px))`}}><AgentMark/></button>
 </Popover>;
}

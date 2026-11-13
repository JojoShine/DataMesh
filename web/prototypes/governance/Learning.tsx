import {Alert,Tag,Steps} from "antd";
import {useEffect,useRef,useState} from "react";
import {Workbench,FlowSurface,ResultSurface} from "./Surfaces";
import {initialGraph,lessons,topics} from "./data";
import type {View,Topic,RunEvidence} from "./data";

const demonstration:RunEvidence={version:3,sourceVersion:1,state:"succeeded",processed:1000,rejected:0,output:1000,id:"教学运行 · 不对应生产记录"};
export function Learning({view,step}:{view:View;step:number}){
 const lesson=lessons[view][step];
 const [graph,setGraph]=useState(()=>structuredClone(initialGraph));
 const [selected,setSelected]=useState("clean_name");
 const [name,setName]=useState("客户基础清洗 · 教学副本");
 const [notice,setNotice]=useState("");
 const [flowTab,setFlowTab]=useState("design");
 const [resultTab,setResultTab]=useState("verification");
 const preview=useRef<HTMLDivElement>(null);
 useEffect(()=>{
  setFlowTab(lesson.target==="flow-conditions"?"conditions":lesson.target==="flow-evidence"?"execution":"design");
  setResultTab(lesson.target==="sharing"?"sharing":"verification");
  setNotice("");
 },[lesson.target]);
 useEffect(()=>{const frame=requestAnimationFrame(()=>preview.current?.querySelector(`[data-tour="${lesson.target}"]`)?.scrollIntoView({block:"nearest",inline:"nearest"}));return()=>cancelAnimationFrame(frame)},[lesson.target,flowTab,resultTab]);
 const explain=(t:Topic)=>setNotice(`${topics[t].title}：${topics[t].purpose} 开始前：${topics[t].before} 完成后：${topics[t].after}`);
 return <div className="proto-learning"><Alert showIcon type="info" title="先在教学副本里认识流程，再进入操作" description="虚线框是本步的学习区域，可点选节点、切换页签和修改示例。这里的修改不带入正式操作，也不代表完成业务核验。"/><div className="proto-learning-grid"><aside><Tag color="blue">第 {step+1} / {lessons[view].length} 步</Tag><h2>{lesson.title}</h2><p>{lesson.why}</p><dl><dt>开始之前</dt><dd>{lesson.before}</dd><dt>完成之后</dt><dd>{lesson.after}</dd></dl><Steps orientation="vertical" size="small" current={step} items={lessons[view].map(l=>({title:l.title}))}/></aside><div className="proto-learning-preview" ref={preview}>
 {view==="home"&&<Workbench active={lesson.target} flowVersion={3} published={false} hasRun={false} onTopic={explain} onDesign={()=>setNotice("从待办进入数据流设计，保留客户来源与模型上下文；随后保存草稿、发布定义、完整运行。下一章可学习具体配置。 ")} onResult={()=>setNotice("从对应成果进入核验，继续查看固定版本、质量与完整性证据；第三章可学习发布条件。")}/>}
 {view==="flow"&&<FlowSurface active={lesson.target} graph={graph} onChange={setGraph} selected={selected} onSelect={setSelected} onTopic={explain} tab={flowTab} onTab={setFlowTab} version={3} published={false} dirty={false} sourceReady sourceVersion={1} run={demonstration} scenario="ready" name={name} onName={setName}/>}
 {view==="result"&&<ResultSurface active={lesson.target} scenario="ready" run={demonstration} published={false} checked={false} sourceVersion={1} sourceReady onTopic={explain} tab={resultTab} onTab={setResultTab} designVersion={3}/>}
 </div></div>{notice&&<Alert className="section-gap" type="info" showIcon title="所选功能的办理关系" description={notice}/>}</div>
}

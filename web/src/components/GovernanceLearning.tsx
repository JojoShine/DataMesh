import {GovernanceHelpScope} from "./GovernanceWorkspaceContext";
import {Alert,Button,Descriptions,Form,Input,Select,Steps,Table,Tabs,Tag,Typography} from "antd";
import {useEffect,useRef,useState} from "react";
import {useSession} from "../stores/session";
import type {GovernanceDataflow} from "../api/governance";
import {FlowGraph} from "../pages/GovernanceFlowGraph";
import {GovernanceDialog} from "./GovernanceDialog";
import {GovernancePurpose} from "./GovernanceContext";
import {journeyTitles,lessons,topics,type GovernanceJourneyPage} from "./governanceJourney";

const pages:GovernanceJourneyPage[]=["home","flow","result"];
const isPage=(value:unknown):value is GovernanceJourneyPage=>value==="home"||value==="flow"||value==="result";
interface LearningProgress {seen:boolean;completed:GovernanceJourneyPage[];steps:Record<GovernanceJourneyPage,number>}
function readProgress(key:string):{value:LearningProgress;warning:string}{
 const empty:LearningProgress={seen:false,completed:[],steps:{home:0,flow:0,result:0}};
 try{
  const raw=localStorage.getItem(key);if(!raw)return {value:empty,warning:""};
  const parsed:unknown=JSON.parse(raw);
  if(typeof parsed!=="object"||parsed===null||!("completed" in parsed)||!Array.isArray(parsed.completed))return {value:empty,warning:"旧学习记录格式已变化，将从本版引导重新开始。"};
  const steps={...empty.steps};if("steps" in parsed&&typeof parsed.steps==="object"&&parsed.steps!==null)for(const [page,step] of Object.entries(parsed.steps))if(isPage(page)&&Number.isInteger(step)&&typeof step==="number"&&step>=0&&step<lessons[page].length)steps[page]=step;
  return {value:{seen:"seen" in parsed&&parsed.seen===true,completed:parsed.completed.filter(isPage),steps},warning:""};
 }catch{return {value:empty,warning:"浏览器无法读取学习记录。本次仍可学习，但刷新后可能需要重新开始。"}}
}

export function useGovernanceLearning(page:GovernanceJourneyPage,_offerOnEntry=false){
 const userID=useSession(s=>s.user?.user_id??"");const key=`datamesh:governance-learning:v1:${userID}`;
 const [initial]=useState(()=>readProgress(key));const [progress,setProgress]=useState(initial.value);const [warning,setWarning]=useState(initial.warning);
 const [stage,setStage]=useState<"intro"|"lesson"|null>(null);
 const [chapter,setChapter]=useState(page);const [step,setStep]=useState(initial.value.steps[page]);
 function persist(next:LearningProgress){setProgress(next);try{localStorage.setItem(key,JSON.stringify(next))}catch{setWarning("浏览器无法保存学习进度，本轮仍可继续；刷新后学习记录可能丢失。")}}
 function start(){setChapter(page);setStep(progress.steps[page]);setStage("lesson")}
 function close(){persist({...progress,seen:true,steps:{...progress.steps,[chapter]:step}});setStage(null)}
 function selectChapter(next:GovernanceJourneyPage){persist({...progress,seen:true,steps:{...progress.steps,[chapter]:step}});setChapter(next);setStep(progress.steps[next]);setStage("lesson")}
 function advance(){
  if(step<lessons[chapter].length-1){const next=step+1;setStep(next);persist({...progress,seen:true,steps:{...progress.steps,[chapter]:next}});return}
  const next={...progress,seen:true,completed:[...new Set([...progress.completed,chapter])],steps:{...progress.steps,[chapter]:0}};persist(next);
  const index=pages.indexOf(chapter);if(index<pages.length-1){setChapter(pages[index+1]);setStep(next.steps[pages[index+1]])}else setStage(null);
 }
 return {active:stage!==null,stage,chapter,step,completed:progress.completed,warning,start,close,selectChapter,advance,back:()=>setStep(n=>Math.max(0,n-1))};
}
export type GovernanceLearningController=ReturnType<typeof useGovernanceLearning>;

const lessonGraph:GovernanceDataflow={nodes:[{id:"lesson_source",kind:"source",inputs:[],source:{resource:{resource_type:"table_resource",resource_id:"lesson-only",contract_version:1},fields:["id","name","amount"]}},{id:"lesson_clean",kind:"transform",inputs:[{node:"lesson_source"}],columns:[{source:"id",target:"id",rule:"copy"},{source:"name",target:"name",rule:"trim"},{source:"amount",target:"amount",rule:"copy"}]},{id:"lesson_quality",kind:"quality",inputs:[{node:"lesson_clean"}],checks:[{field:"id",kind:"required",blocking:true}]},{id:"lesson_output",kind:"output",inputs:[{node:"lesson_quality",port:"good"}],model:{id:"lesson-model",version:2}}]};
function LearningPreview({chapter,step}:{chapter:GovernanceJourneyPage;step:number}){
 const lesson=lessons[chapter][step];const ref=useRef<HTMLDivElement>(null);const [selected,setSelected]=useState("lesson_clean");const [name,setName]=useState("客户基础清洗 · 教学副本");const [rule,setRule]=useState("trim");const [note,setNote]=useState("");
 const [tab,setTab]=useState("design");
 useEffect(()=>{setTab(lesson.target==="flow-conditions"?"conditions":lesson.target==="flow-evidence"?"execution":lesson.target==="sharing"?"sharing":"design");setNote("")},[lesson.target]);
 useEffect(()=>{const id=requestAnimationFrame(()=>ref.current?.querySelector(`[data-learn="${lesson.target}"]`)?.scrollIntoView({block:"nearest",inline:"nearest"}));return()=>cancelAnimationFrame(id)},[lesson.target,tab]);
 const highlight=(target:string)=>lesson.target===target?" governance-learning-highlight":"";
 const explain=(key:"domain"|"term"|"standard")=>setNote(`${topics[key].purpose} ${topics[key].after}`);
 const sharing=<div data-learn="sharing" className={highlight("sharing")}><GovernancePurpose purpose="按目录、范围和条件将成果提供给获准的使用者。" before="先核验发布；共享需求成立时，再办理目录、挂接和审核。" after="使用方申请获准后才能查询、下载或调用。" requirement="需要共享时条件必选；成果发布不会自动授权。"/><Steps orientation="vertical" current={0} items={[{title:"核验发布成果",content:"发布时重核质量、完整性、来源和当前职责。"},{title:"目录维护与资源挂接",content:"明确字段、使用条件与限制。"},{title:"审核与使用申请",content:"各职责分别办理，保留原审批流程。"},{title:"审批、授权与交付",content:"使用方仅在获准范围内使用。"}]}/></div>;
 return <div className="governance-learning-preview" ref={ref}>
 {chapter==="home"&&<>
 <section data-learn="goal" className={"governance-goal"+highlight("goal")}><Tag>教学示例 · 独立副本</Tag><h3>治理工作台是一张综合看板</h3><p>从资源、业务规范、执行、质量和成果几方面了解治理情况。这里的数字只用于讲解，不是当前平台统计。</p><Descriptions column={{xs:1,md:2}} items={[{key:"resources",label:"可治理资源",children:"12份（示例）"},{key:"outputs",label:"已发布成果",children:"6份（示例）"},{key:"success",label:"运行成功率",children:"80%：成功8次，失败2次（示例）"},{key:"quality",label:"质量合格率",children:"98%：合格980 / 检查1000行次（示例）"}]}/></section>
 <section data-learn="relation" className={"governance-business-panel"+highlight("relation")}><h3>趋势和分布分别回答什么</h3><Table rowKey="name" pagination={false} dataSource={[{name:"运行趋势与状态",meaning:"选定周期内有多少次执行，当前成功、失败及在途状态怎样分布"},{name:"质量检查趋势",meaning:"检查、合格和隔离行次随日期怎样变化；多节点分别计次"},{name:"成果发布分布",meaning:"累计成果中有多少已发布、草稿或下架；不能代替当前可交付核验"}]} columns={[{title:"图表",dataIndex:"name"},{title:"业务含义",dataIndex:"meaning"}]}/><p className="helper-text">看板数字按业务可见范围统计；没有检查记录时显示—，不会假定全部合格。</p></section>
 <section data-learn="foundation" className={"governance-business-panel"+highlight("foundation")}><h3>全部功能指标与业务关系</h3><p>按业务范围与规范、认识数据与约定结果、开发编排、质量成果、血缘运维、发布生命周期分组；数字入口通向对应功能。</p>{(["domain","term","standard"] as const).map(key=><p key={key}><Typography.Link onClick={()=>explain(key)}>{topics[key].title}</Typography.Link>：{topics[key].purpose}<br/><span className="helper-text">{topics[key].required}</span></p>)}</section>
 <section data-learn="tasks" className={"governance-business-panel"+highlight("tasks")}><h3>不同异常进入不同业务</h3><Table rowKey="name" pagination={false} dataSource={[{name:"客户数据质量问题逾期",next:"到质量整改核对责任人、进展及原版本复测"},{name:"客户数据流运行失败",next:"到运行运维查看实际诊断，处理来源、权限或规则"},{name:"告警通知失败",next:"到治理告警检查通知渠道，运行问题另行处理"},{name:"客户成果迁移未完成",next:"到成果生命周期处理旧引用和新成果交接"}]} columns={[{title:"教学示例",dataIndex:"name"},{title:"对应业务",dataIndex:"next"}]}/></section>
 </>}
 {chapter==="flow"&&<><div data-learn="flow-purpose" className={highlight("flow-purpose")}><GovernancePurpose purpose="读取授权输入，经清洗和质量检查产出符合模型的成果。" before="选择来源，配置处理步骤；可使用已有模型或运行后生成模型。" after="配置 → 保存并运行 → 查看结果 → 成果核验。" requirement="来源与输出必选，各算子按业务需要配置。"/></div>{tab==="execution"&&<Tabs activeKey={tab} onChange={setTab} items={[{key:"design",label:"数据编排"},{key:"execution",label:"运行结果"}]}/>}{tab==="design"&&<><Form layout="vertical"><Form.Item label="数据流名称（只修改教学副本）"><Input placeholder="请输入教学任务名称" value={name} onChange={e=>setName(e.target.value)}/></Form.Item></Form><div className="governance-learning-designer"><div data-learn="canvas" className={highlight("canvas")}><h3>处理关系</h3><FlowGraph graph={lessonGraph} selected={selected} onSelect={setSelected}/><p className="helper-text">点选节点，配置当前输入与输出；窄屏画布可左右滚动。</p></div><div data-learn="properties" className={"governance-node-help"+highlight("properties")}><h3>当前节点的配置</h3><p>{selected=== "lesson_clean"?"文本转换：统一姓名格式，保持含义。":selected==="lesson_quality"?"质量检查：识别不符合要求的记录，合格分支进入成果。":selected==="lesson_source"?"来源：固定资源、契约版本与读取字段。":"输出：固定已发布模型，检查字段和要求是否相容。"}</p><Form layout="vertical"><Form.Item label="姓名的处理方式（教学）"><Select aria-label="教学姓名处理方式" placeholder="请选择处理方式" value={rule} options={[{value:"trim",label:"去除首尾空白"},{value:"copy",label:"保持原值"}]} onChange={setRule}/></Form.Item></Form><p className="helper-text">只显示当前节点配置。添加、移除、保存及运行在操作区办理。</p></div></div></>}{tab==="conditions"&&<div data-learn="flow-conditions" className={highlight("flow-conditions")}><Alert showIcon type="warning" title="教学示例：尚未选择来源数据" description="点击保存并运行时自动检查；有问题就定位到对应节点，修正后再次运行。"/><Button onClick={()=>{setSelected("lesson_source");setTab("design")}}>定位数据来源</Button><p className="helper-text">教学说明不代表以上业务条件已满足。</p></div>}{tab==="execution"&&<div data-learn="flow-evidence" className={highlight("flow-evidence")}><Alert showIcon type="info" title="教学示例：一次完整运行" description="读取1000行，输出1000行，隔离0行。这是预设教学数据，实际平台显示真实运行结果。"/><Descriptions className="detail-section" column={1} items={[{key:"flow",label:"固定处理版本",children:"教学数据流 v3"},{key:"model",label:"输出模型",children:"教学模型 v2"},{key:"scope",label:"检查范围",children:"完整运行；采样不能代替"}]}/><p>历史 v2 的成功结果只证明 v2 的执行，新版仍需独立运行。</p></div>}</>}
 {chapter==="result"&&<><section data-learn="result-object" className={"governance-goal"+highlight("result-object")}><Tag>教学示例成果</Tag><h3>客户基础数据</h3><Descriptions column={1} items={[{key:"source",label:"来源",children:"教学客户表 · 契约v1"},{key:"model",label:"结果要求",children:"教学客户模型v2"},{key:"flow",label:"实际处理",children:"教学数据流v3 · 独立运行"}]}/></section>{lesson.target==="sharing"?sharing:<><section data-learn="gates" className={"governance-business-panel"+highlight("gates")}><h3>发布需要满足什么条件</h3><Table rowKey="name" pagination={false} dataSource={["实际运行与登记证据","质量与完整性","当前来源及加工依据","结果有效期与使用限制","成果维护与发布职责"].map(name=>({name,state:"以实际核验为准"}))} columns={[{title:"必要条件",dataIndex:"name"},{title:"判断依据",dataIndex:"state"}]}/></section><section data-learn="diagnostics" className={"governance-business-panel"+highlight("diagnostics")}><Alert showIcon type="warning" title="教学示例：质量未通过" description="18条姓名为空、2条金额小于0。先核对规则与来源、登记整改，再按原版本复测。失败运行不能当成可发布成果。"/><p className="helper-text">实际平台会显示真实原因和对应办理入口；完成学习不会消除异常。</p></section></>}</>}
 {note&&<Alert type="info" showIcon title={note}/>}</div>;
}

export function GovernanceLearningDialog({controller}:{controller:GovernanceLearningController}){
 const c=controller;const lesson=lessons[c.chapter][c.step];
 return <GovernanceHelpScope><GovernanceDialog header={c.stage==="intro"?"先认识流程，再开始工作":`学习模式 · ${journeyTitles[c.chapter]}`} width={1680} onClose={c.close} closeLabel={c.stage==="intro"?"稍后学习，进入工作台":"暂停学习并返回"} actions={c.stage==="intro"?<Button type="primary" onClick={c.start}>开始虚线框学习</Button>:<><Button disabled={c.step===0} onClick={c.back}>上一步</Button><Button type="primary" onClick={c.advance}>{c.step<lessons[c.chapter].length-1?"下一步":c.chapter==="result"?"完成学习，返回操作":"完成本章，学习下一页"}</Button></>}>
 {c.warning&&<Alert showIcon type="warning" title={c.warning}/>}<Alert showIcon type="info" title="教学副本与真实业务隔离" description="学习区使用明确标记的虚拟数据，不读取客户记录、不调用写接口，也不改变模型、运行、质量、权限或发布状态。完成学习后再按实际条件操作。"/>
 {c.stage==="intro"?<div className="governance-learning-chapters">{pages.map((page,i)=><article key={page}><span>0{i+1}</span><h3>{journeyTitles[page]}</h3><p>{page==="home"?"先了解目标、责任、业务规范与当前环节。":page==="flow"?"点选节点配置，将前置、版本和真实运行分开查看。":"看懂发布条件，异常知道原因和处理路径。"}</p></article>)}</div>:<><Tabs activeKey={c.chapter} onChange={key=>{if(isPage(key))c.selectChapter(key)}} items={pages.map(page=>({key:page,label:`${journeyTitles[page]}${c.completed.includes(page)?" · 已学习":""}`}))}/><div className="governance-learning-grid"><aside><Tag color="blue">第{c.step+1} / {lessons[c.chapter].length}步</Tag><h3>{lesson.title}</h3><p>{lesson.why}</p><dl><dt>开始之前</dt><dd>{lesson.before}</dd><dt>完成之后</dt><dd>{lesson.after}</dd></dl><Steps size="small" orientation="vertical" current={c.step} items={lessons[c.chapter].map(l=>({title:l.title}))}/></aside><LearningPreview chapter={c.chapter} step={c.step}/></div></>}
 </GovernanceDialog></GovernanceHelpScope>;
}

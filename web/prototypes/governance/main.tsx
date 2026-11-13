import {useEffect,useRef,useState,type ReactNode} from "react";
import {createRoot} from "react-dom/client";
import {Alert,App as AntApp,Button,ConfigProvider,Descriptions,Form,Input,Menu,Select,Space,Table,Tabs,Tag} from "antd";
import zhCN from "antd/locale/zh_CN";
import {ApartmentOutlined,AuditOutlined,BookOutlined,CheckCircleOutlined,DatabaseOutlined,ExperimentOutlined,HomeOutlined,NodeIndexOutlined,ReadOutlined,ScheduleOutlined,ShareAltOutlined,TeamOutlined} from "@ant-design/icons";
import {appTheme} from "../../src/app/theme";
import {governanceCheckNames} from "../../src/components/GovernanceChecksEditor";
import {GovernanceDialog} from "../../src/components/GovernanceDialog";
import {defaultFlowNode,flowEditorFields} from "../../src/pages/GovernanceFlowGraph";
import {governanceFlowNames,type GovernanceDataflow,type GovernanceFlowKind} from "../../src/api/governance";
import {FieldTable,FlowSurface,Purpose,ResultSurface,SharingSurface,Workbench} from "./Surfaces";
import {Learning} from "./Learning";
import {fields,initialGraph,lessons,palette,resource,topics,type RunEvidence,type Scenario,type Topic,type View} from "./data";
import "../../src/style.css";
import "../../src/pages/GovernanceDataflows.css";
import "./prototype.css";

type Panel="welcome"|"flow"|"result"|"learn"|"info"|"relations"|null;
const titles:Record<View,string>={home:"治理工作台",flow:"数据流设计",result:"成果核验"};
const initialRun:RunEvidence={version:2,sourceVersion:1,state:"succeeded",processed:1000,rejected:0,output:1000,id:"demo-run-v2"};
const signature=(graph:GovernanceDataflow,name:string)=>JSON.stringify({graph,name});

function validate(graph:GovernanceDataflow,name:string,sourceReady:boolean,sourceVersion:number):string[]{
 const errors:string[]=[];const ids=graph.nodes.map(n=>n.id);
 if(!name.trim())errors.push("请填写数据流名称。");
 if(new Set(ids).size!==ids.length)errors.push("节点编号必须唯一。");
 if(!graph.nodes.some(n=>n.kind==="source")||graph.nodes.filter(n=>n.kind==="output").length!==1)errors.push("必须保留来源和一个模型输出节点。");
 if(!sourceReady)errors.push("来源变更尚未核对，请先在来源说明中明确新契约和字段权限。");
 const visit=(id:string,path:Set<string>):boolean=>{if(path.has(id))return false;const node=graph.nodes.find(n=>n.id===id);return !!node&&(node.inputs??[]).every(e=>visit(e.node,new Set([...path,id])))};
 if(!graph.nodes.every(n=>visit(n.id,new Set())))errors.push("连线存在循环或缺少前置节点，请调整输入关系。");
 for(const n of graph.nodes){
  const available=flowEditorFields(graph,n.inputs?.[0],{[resource.object_id]:resource});
  const names=available.map(f=>f.name);
  if(n.kind==="source"&&(!n.source?.fields.length||n.source.resource.contract_version!==sourceVersion))errors.push(`${n.id}：请选择字段并引用当前契约 v${sourceVersion}。`);
  if(n.kind!=="source"&&!n.inputs?.length)errors.push(`${n.id}：缺少前置节点。`);
  if((n.kind==="project"||n.kind==="transform")&&(!n.columns?.length||n.columns.some(c=>!c.target.trim()||!names.includes(c.source))))errors.push(`${n.id}：字段映射不完整或输入字段已变化。`);
  if(n.kind==="deduplicate"&&(!n.keys?.length||n.keys.some(k=>!names.includes(k))))errors.push(`${n.id}：请设置有效去重键。`);
  if(n.kind==="filter"&&(!n.condition||!names.includes(n.condition.field)))errors.push(`${n.id}：请核对筛选字段。`);
  if(n.kind==="aggregate"&&(!n.aggregates?.length||n.aggregates.some(a=>!names.includes(a.source)||!a.target.trim())))errors.push(`${n.id}：请补全汇总字段与结果名。`);
  if(n.kind==="join"&&(n.inputs?.length!==2||!n.join?.left_keys.length||!n.join.right_keys.length||!n.join.columns.length))errors.push(`${n.id}：需两个输入、匹配的关联键及结果字段。本稿可查看配置，复杂关联请在完整业务页办理。`);
  if(n.kind==="code_map"&&(!n.code||!names.includes(n.code.field)||n.code.values.some(v=>!v.from||!v.to)))errors.push(`${n.id}：请填写映射字段、来源值及标准值。`);
  if(n.kind==="quality"&&(!n.checks?.length||n.checks.some(c=>!names.includes(c.field)||c.kind==="reference_exists"&&(n.inputs?.length!==2))))errors.push(`${n.id}：检查字段须来自输入；跨表存在检查还需第二输入。`);
  if(n.kind==="output"){
   if(n.model?.id!=="demo-model")errors.push("请选择已发布输出模型。");
   if(n.inputs?.[0]?.port==="bad")errors.push("正式成果不能连接质量不合格分支。");
   if(fields.some(f=>!available.some(a=>a.name===f.name&&a.logical_type===f.logical_type&&a.precision===f.precision&&a.scale===f.scale)))errors.push("模型要求 id、name、amount 及对应类型和金额精度；当前输出不相容。");
  }
 }
 return [...new Set(errors)];
}

function Prototype(){
 const {message}=AntApp.useApp();
 const [view,setView]=useState<View>("home");
 const [panel,setPanel]=useState<Panel>("welcome");
 const [back,setBack]=useState<Panel>(null);
 const [topic,setTopic]=useState<Topic>("domain");
 const [lessonView,setLessonView]=useState<View>("home");
 const [step,setStep]=useState(0);
 const [completed,setCompleted]=useState<View[]>([]);
 const [graph,setGraph]=useState(()=>structuredClone(initialGraph));
 const [name,setName]=useState("客户基础清洗");
 const [saved,setSaved]=useState(()=>signature(initialGraph,"客户基础清洗"));
 const [selected,setSelected]=useState("clean_name");
 const [version,setVersion]=useState(3);
 const [flowPublished,setFlowPublished]=useState(false);
 const [flowTab,setFlowTab]=useState("design");
 const [resultTab,setResultTab]=useState("verification");
 const [run,setRun]=useState<RunEvidence|null>(initialRun);
 const [history,setHistory]=useState<RunEvidence[]>([initialRun]);
 const sequence=useRef(0);
 const [published,setPublished]=useState(false);
 const [checked,setChecked]=useState(false);
 const [scenario,setScenario]=useState<Scenario>("ready");
 const [sourceReady,setSourceReady]=useState(true);
 const [sourceVersion,setSourceVersion]=useState(1);
 const [reviewing,setReviewing]=useState(false);
 const [migration,setMigration]=useState("本次调整客户清洗定义；保留 v2 及既有成果。新版本完整运行并核验后，再单独办理下游切换。");
 const [feedback,setFeedback]=useState("");
 const [phase,setPhase]=useState("");
 const timers=useRef<ReturnType<typeof setTimeout>[]>([]);
 useEffect(()=>()=>timers.current.forEach(clearTimeout),[]);
 const dirty=saved!==signature(graph,name);
 const errors=validate(graph,name,sourceReady,sourceVersion);
 const publishable=run?.state==="succeeded"&&scenario!=="quality"&&sourceReady&&run.sourceVersion===sourceVersion;
 const openFlow=()=>{setView("flow");setPanel("flow");setFeedback("")};
 const openResult=()=>{setView("result");setPanel("result");setResultTab("verification");setFeedback("")};
 const openTopic=(t:Topic)=>{setBack(panel==="info"?back:panel);setTopic(t);setPanel("info");setFeedback("")};
 const startLearning=(v:View)=>{setBack(panel);setLessonView(v);setStep(0);setPanel("learn");setFeedback("")};
 const close=()=>{setFeedback("");setPanel(panel==="learn"||panel==="info"||panel==="relations"?back:null);setBack(null)};
 const route=(key:string)=>{if(key==="home"||key==="flow"||key==="result"){setView(key);setPanel(null)}else openTopic(key as Topic)};
 const switchScenario=(s:Scenario)=>{
  setScenario(s);setPublished(false);setChecked(false);setFeedback("");setFlowPublished(false);setVersion(3);setReviewing(false);
  const next=structuredClone(initialGraph);setGraph(next);setSaved(signature(next,name));setSelected("clean_name");
  setSourceVersion(s==="source"?2:1);setSourceReady(s!=="source");
  const evidence:RunEvidence=s==="quality"?{version:2,sourceVersion:1,state:"failed",processed:1000,rejected:20,output:0,id:"demo-quality-failed-v2"}:{...initialRun};setRun(evidence);setHistory([evidence]);
 };
 const save=()=>{if(errors.length){setFeedback(errors.join(" "));setFlowTab("conditions");return}setSaved(signature(graph,name));setFeedback("");message.success("已保存交互稿中的草稿。")};
 const publishDefinition=()=>{
  setFlowTab("conditions");
  if(errors.length||dirty){setFeedback([...errors,...(dirty?["请先保存当前草稿，再发布同一份配置。"]:[])].join(" "));return}
  if(!reviewing){setReviewing(true);setFeedback("");return}
  if(!migration.trim()){setFeedback("请说明本次变更及旧版本的保留、下游迁移安排。");return}
  setFlowPublished(true);setReviewing(false);setFeedback("定义已在交互稿发布，尚未执行数据处理。请继续完整运行。");message.success(`示例定义 v${version} 已发布`);
 };
 const execute=()=>{
  if(!flowPublished||dirty||errors.length){setFeedback("请先保存并发布有效定义，再完整运行。");return}
  setPhase("排队并核对来源");setFlowTab("execution");setFeedback("");
  timers.current.push(setTimeout(()=>setPhase("演示读取与逐项质量检查"),450));
  timers.current.push(setTimeout(()=>{
   const evidence:RunEvidence={version,sourceVersion,state:scenario==="quality"?"failed":"succeeded",processed:1000,rejected:scenario==="quality"?20:0,output:scenario==="quality"?0:1000,id:`demo-run-v${version}-${++sequence.current}`};setRun(evidence);setHistory(h=>[evidence,...h]);
   setPhase("");setPublished(false);setChecked(false);setFeedback(scenario==="quality"?"示例运行未通过质量要求，保留失败证据，当前没有可发布成果。":"示例完整运行已结束。1000 行是预设演示计数，请继续核验这一次成果。");
  },1200));
 };
 const addNode=(kind:GovernanceFlowKind)=>{
  const current=graph.nodes.find(n=>n.id===selected)??graph.nodes[0];
  if(current.kind==="output"&&kind!=="source"){setFeedback("请先选中输出之前的节点，在其后插入处理算子。");return}
  if(kind==="output"&&graph.nodes.some(n=>n.kind==="output")){setFeedback("本例保留一个模型输出。请点选已有输出节点调整配置。");return}
  const edge={node:current.id,...(current.kind==="quality"?{port:"good" as const}:{})};
  const available=flowEditorFields(graph,edge,{[resource.object_id]:resource});
  const id=`${kind}_${graph.nodes.length+1}`;const node=defaultFlowNode(kind,id,edge,available);
  if(kind==="source")node.source={resource:{resource_type:"table_resource",resource_id:resource.object_id,contract_version:sourceVersion},fields:fields.map(f=>f.name)};
  if(kind==="output")node.model={id:"demo-model",version:2};
  if(node.code)node.code.standard={id:"demo-standard",version:1};
  const nodes=graph.nodes.map(n=>kind==="source"?n:{...n,inputs:n.inputs?.map(e=>e.node===current.id?{node:id,...(kind==="quality"?{port:"good" as const}:{})}:e)??null});
  setGraph({nodes:[...nodes,node]});setSelected(id);setFlowTab("design");setFeedback("已在示例草稿中添加节点；新配置仍须保存、发布和完整运行。");
 };
 const adoptSource=()=>{
  setGraph(g=>({nodes:g.nodes.map(n=>n.source?{...n,source:{...n.source,resource:{...n.source.resource,contract_version:sourceVersion}}}:n)}));
  setSourceReady(true);if(flowPublished){setVersion(v=>v+1);setFlowPublished(false)}setSaved("");setPanel("flow");setBack(null);setView("flow");setFlowTab("conditions");setFeedback("已在示例中确认新契约和权限范围。旧成果仍引用旧契约，请保存新定义、发布、完整运行，再核验新成果。");
 };
 const historyTable=<section className="section-gap"><h3>历史运行证据（当前演示场景）</h3><Table size="small" rowKey="id" pagination={false} scroll={{x:700}} dataSource={history} columns={[{title:"运行编号",dataIndex:"id"},{title:"定义 / 契约版本",render:(_,r)=>`v${r.version} / v${r.sourceVersion}`},{title:"状态",render:(_,r)=><Tag color={r.state==="succeeded"?"success":"error"}>{r.state==="succeeded"?"成功产出":"质量阻断"}</Tag>},{title:"读取 / 成果 / 隔离",render:(_,r)=>`${r.processed} / ${r.output} / ${r.rejected}`} ]}/></section>;
 let content:ReactNode;let actions:ReactNode;let header:ReactNode;let width=1480;let closeLabel="返回列表";
 if(panel==="welcome"){
  header="先认识流程，再开始操作";width=960;closeLabel="稍后学习，进入交互稿";
  content=<><Alert showIcon type="info" title="这是三页可点击交互稿，全部使用示例数据" description="不连接主环境、不实际读取或发布数据。教学副本与操作副本相互独立；业务前置、质量门禁和后续审批关系完整保留。"/><div className="proto-welcome-chapters">{(["home","flow","result"] as View[]).map((v,i)=><article key={v}><span>0{i+1}</span><h2>{titles[v]}</h2><p>{v==="home"?"先认识目标、责任、业务规范与完整功能关系。":v==="flow"?"点选节点配置，分开查看前置、版本和实际运行。":"逐项看懂发布条件，知道异常找谁、发布后做什么。"}</p></article>)}</div><Purpose purpose="每个功能都解释存在的必要性，并说明什么情况下才需要办理。" before="可先通过虚线框逐步学习，也可稍后从任意页面重新学习。" after="学习完成后回到交互稿；实际业务仍按真实状态办理。"/></>;
  actions=<Button type="primary" onClick={()=>startLearning("home")}>开始虚线框学习</Button>;
 }else if(panel==="learn"){
  header=`学习模式 · ${titles[lessonView]}`;width=1680;closeLabel="暂停学习并返回";
  content=<><Tabs activeKey={lessonView} onChange={k=>{setLessonView(k as View);setStep(0)}} items={(["home","flow","result"] as View[]).map(v=>({key:v,label:`${titles[v]}${completed.includes(v)?" · 已学习":""}`}))}/><Learning key={lessonView} view={lessonView} step={step}/></>;
  actions=<><Button disabled={step===0} onClick={()=>setStep(s=>s-1)}>上一步</Button><Button type="primary" onClick={()=>{if(step<lessons[lessonView].length-1)setStep(s=>s+1);else{setCompleted(c=>[...new Set([...c,lessonView])]);const next=lessonView==="home"?"flow":lessonView==="flow"?"result":null;if(next){setLessonView(next);setStep(0)}else{setView("home");setPanel(null);setBack(null);message.success("学习完成，可以在交互稿中操作。学习没有改变任何业务状态。")}}}}>{step<lessons[lessonView].length-1?"下一步":lessonView==="result"?"完成学习，进入交互稿":"完成本章，学习下一页"}</Button></>;
 }else if(panel==="flow"){
  header=<Space wrap><span>数据流设计 · 客户基础清洗</span><Tag>示例操作</Tag></Space>;width=1560;
  content=<>{phase&&<Alert showIcon type="info" title={`模拟执行中 · ${phase}`} description="演示使用固定场景计数，不执行数据库查询。"/>}{feedback&&<Alert className="section-gap" showIcon type={errors.length?"warning":"info"} title={feedback}/>}<FlowSurface graph={graph} onChange={setGraph} selected={selected} onSelect={setSelected} onTopic={openTopic} tab={flowTab} onTab={setFlowTab} version={version} published={flowPublished} dirty={dirty} sourceReady={sourceReady} sourceVersion={sourceVersion} run={run} scenario={scenario} name={name} onName={setName}/>{flowTab==="execution"&&historyTable}{reviewing&&<div className="proto-review"><h3>确认发布这个定义版本</h3><p>发布不会执行数据处理。既有 v2 成果和下游引用继续保留，新成果核验后再另行迁移。</p><Form layout="vertical"><Form.Item label="本次变更与迁移安排（示例）" required><Input.TextArea value={migration} onChange={e=>setMigration(e.target.value)} rows={2} placeholder="说明为何修订、如何保留旧版本、何时切换下游"/></Form.Item></Form></div>}</>;
  actions=<><Button disabled={!!phase} onClick={()=>startLearning("flow")}>学习本页</Button>{!flowPublished?<>{graph.nodes.find(n=>n.id===selected)?.kind==="quality"&&<><Button onClick={()=>setGraph(g=>({nodes:g.nodes.map(n=>n.id===selected?{...n,checks:[...(n.checks??[]),{field:"id",kind:"required" as const,blocking:true,parameters:{}}]}:n)}))}>添加质量规则</Button><Select aria-label="移除质量规则" value={null} placeholder="移除质量规则" style={{width:170}} options={(graph.nodes.find(n=>n.id===selected)?.checks??[]).map((c,i)=>({value:i,label:`${i+1}. ${c.field} · ${governanceCheckNames[c.kind]}`}))} onChange={i=>setGraph(g=>({nodes:g.nodes.map(n=>n.id===selected?{...n,checks:n.checks?.filter((_,j)=>j!==i)}:n)}))}/></>}<Select aria-label="添加算子" placeholder="添加处理算子" value={null} style={{width:160}} options={palette.map(k=>({value:k,label:governanceFlowNames[k]}))} onChange={addNode}/>{graph.nodes.length>4&&<Button onClick={()=>{const node=graph.nodes.find(n=>n.id===selected);if(!node||initialGraph.nodes.some(n=>n.id===node.id)){setFeedback("请先选中新添加的节点。基础示例链保留，以便完整演示。");return}setGraph(g=>({nodes:g.nodes.filter(n=>n.id!==selected).map(n=>({...n,inputs:n.inputs?.map(e=>e.node===selected?node.inputs?.[0]??e:e)??null}))}));setSelected("clean_name")}}>移除所选新增节点</Button>}<Button onClick={save}>保存草稿</Button><Button type="primary" onClick={publishDefinition}>{reviewing?"确认发布本版本（演示）":"核对并发布定义"}</Button></>:<><Button disabled={!!phase} onClick={()=>{setFlowPublished(false);setVersion(v=>v+1);setSaved("");setFlowTab("design");setFeedback("新修订保留旧运行与成果，修改后须重新保存、发布并运行。")}}>创建修订</Button><Button type="primary" loading={!!phase} onClick={execute}>完整运行（演示）</Button></>}{run?.version===version&&<Button disabled={!!phase} onClick={openResult}>前往成果核验</Button>}</>;
 }else if(panel==="result"){
  header=<Space wrap><span>成果核验 · 客户基础数据</span><Tag>示例操作</Tag></Space>;
  content=<>{feedback&&<Alert showIcon type={publishable?"info":"warning"} title={feedback}/>}<ResultSurface scenario={scenario} run={run} published={published} checked={checked} sourceVersion={sourceVersion} sourceReady={sourceReady} onTopic={openTopic} tab={resultTab} onTab={setResultTab} designVersion={version}/>{resultTab==="evidence"&&historyTable}</>;
  actions=<><Button onClick={()=>startLearning("result")}>学习本页</Button><Button onClick={()=>{setChecked(true);setFeedback(publishable?"本示例的发布条件满足，发布动作仍会再次核对。":"存在未满足条件，请按表格中的原因处理。点击核验不会改变异常事实。")}}>重新核验条件</Button>{!publishable&&<Button onClick={()=>openTopic(scenario==="source"?"source":"issues")}>查看原因并继续处理</Button>}{published?<><Button onClick={()=>{setResultTab("sharing");setFeedback("")}}>查看后续共享办理</Button><Button type="primary" onClick={()=>{setView("home");setPanel(null)}}>回到治理工作台</Button></>:<Button type="primary" disabled={!publishable} onClick={()=>{setChecked(true);setPublished(true);setResultTab("sharing");setFeedback("成果已在交互稿发布。后续目录、挂接、审核及授权须分别办理。");message.success("示例成果发布完成")}}>核验并发布（演示）</Button>}</>;
 }else if(panel==="info"){
  const info=topics[topic];header=info.title;width=1100;closeLabel="返回原位置";
  content=<><Tag color="blue">关联功能说明 · 保留完整办理关系</Tag><Purpose purpose={info.purpose} before={info.before} after={info.after} requirement={info.required}/><Descriptions column={1} bordered items={[{key:"when",label:"什么时候用",children:info.when},{key:"relation",label:"与其他功能的关系",children:info.relation},{key:"scope",label:"本次交互稿范围",children:"完整菜单和关系保留。本页说明关联功能，实际配置在对应业务功能办理。"}]}/>{topic==="model"&&<div className="section-gap"><h3>客户基础模型 · v2（示例已发布）</h3><FieldTable/></div>}{topic==="source"&&<div className="section-gap"><Alert showIcon type={sourceReady?"info":"warning"} title={sourceReady?`当前使用契约 v${sourceVersion} · 示例权限有效`:`来源已变为契约 v${sourceVersion}，旧运行仍使用 v1`} description="采用新契约前需核对结构与读取、派生范围；采用后须修订处理定义并重新运行。"/><FieldTable/></div>}{topic==="issues"&&scenario==="quality"&&<Alert className="section-gap" showIcon type="error" title="示例：18 条姓名为空，2 条金额小于 0" description="失败证据保留；整改完成后先按原发布版本 v2 复测。本稿提供一次整改演示，随后仍需手动完整运行并核验。"/>}{topic==="sharing"&&<SharingSurface published={published} onTopic={t=>{setTopic(t)}}/>}</>;
  actions=<>{topic==="source"&&!sourceReady&&<Button type="primary" onClick={adoptSource}>核对并采用示例契约 v2</Button>}{topic==="issues"&&scenario==="quality"&&<Button type="primary" onClick={()=>{setScenario("ready");setFlowPublished(true);setVersion(run?.version??2);setSaved(signature(graph,name));setView("flow");setPanel("flow");setBack(null);setFlowTab("execution");setFeedback("示例来源整改已完成；失败证据仍保留。请按原版本 v2 完整复测，成功后再核验新成果。")}}>演示：来源整改完成，去原版复测</Button>}{topic==="quality"&&<Button type="primary" onClick={openResult}>查看本次成果核验</Button>}</>;
 }else if(panel==="relations"){
  header="完整功能关系 · 按职责拆分菜单";closeLabel="返回原位置";width=1480;
  content=<><Alert showIcon type="info" title="按功能职责分开办理，按前后关系衔接" description="数据流负责数据处理，工作流负责任务依赖，调度负责自动触发。业务规范、执行证据、发布与共享各有独立作用；完整流程不省略。"/><Table className="section-gap" rowKey="key" pagination={false} scroll={{x:1100}} dataSource={(Object.entries(topics) as [Topic,typeof topics[Topic]][]).map(([key,t])=>({key,...t}))} columns={[{title:"功能 / 菜单",dataIndex:"title",width:170},{title:"必要性 / 适用场景",width:290,render:(_,r)=><><p>{r.purpose}</p><span className="proto-small">{r.when}</span></>},{title:"前置条件",dataIndex:"before"},{title:"完成后的关系",dataIndex:"after"},{title:"必选 / 可选",dataIndex:"required",width:210}]}/></>;
 }
 const menus=[{key:"home",icon:<HomeOutlined/>,label:"治理工作台"},{type:"group" as const,label:"业务范围与规范",children:[{key:"domain",icon:<TeamOutlined/>,label:"主题域与责任"},{key:"standard",icon:<BookOutlined/>,label:"数据标准"},{key:"term",icon:<ReadOutlined/>,label:"业务术语"}]},{type:"group" as const,label:"认识数据与约定结果",children:[{key:"source",icon:<DatabaseOutlined/>,label:"元数据与来源剖析"},{key:"model",icon:<ApartmentOutlined/>,label:"模型与物理建表"}]},{type:"group" as const,label:"开发与自动运行",children:[{key:"flow",icon:<NodeIndexOutlined/>,label:"数据流设计"},{key:"workflow",icon:<ApartmentOutlined/>,label:"工作流编排"},{key:"schedule",icon:<ScheduleOutlined/>,label:"调度计划"}]},{type:"group" as const,label:"核验与持续治理",children:[{key:"result",icon:<CheckCircleOutlined/>,label:"成果核验"},{key:"quality",icon:<ExperimentOutlined/>,label:"质量规则与趋势"},{key:"issues",icon:<AuditOutlined/>,label:"质量整改与告警"},{key:"lineage",icon:<ApartmentOutlined/>,label:"血缘与变更影响"},{key:"lifecycle",icon:<DatabaseOutlined/>,label:"成果生命周期"},{key:"sharing",icon:<ShareAltOutlined/>,label:"发布与共享衔接"}]}];
 return <div className="proto-shell"><aside className="proto-sidebar"><div className="proto-brand"><img src="/static/design/brand-mark.png" alt="DataMesh"/><strong>DataMesh <small>数据治理</small></strong></div><Menu theme="dark" mode="inline" items={menus} selectedKeys={[view]} onClick={({key})=>route(key)}/><div className="proto-sidebar-foot">三页交互稿 · 示例数据</div></aside><div className="proto-main"><header className="proto-topbar"><Space wrap><span>数据治理</span><Tag color="blue">交互稿</Tag><span className="proto-small">全部为示例数据 · 不连接主环境</span></Space><Space><span className="proto-small">治理人员</span><span className="proto-avatar">治</span></Space></header><main className="proto-page"><div className="proto-page-heading"><div><span className="proto-small">数据治理 / {titles[view]}</span><h1>{titles[view]}</h1><p>{view==="home"?"从要完成的工作出发，看清当前状态、所需条件与下一步。":view==="flow"?"配置如何处理数据；定义发布和实际运行分别办理。":"根据本次运行证据确认成果能否发布，异常有原因也有处理入口。"}</p></div><Space wrap><Button icon={<ReadOutlined/>} onClick={()=>startLearning(view)}>学习当前页</Button><Button onClick={()=>{setBack(panel);setPanel("relations")}}>完整功能关系</Button></Space></div>
 {view==="home"?<Workbench onTopic={openTopic} onDesign={openFlow} onResult={openResult} flowVersion={version} published={published} hasRun={!!run&&run.state==="succeeded"&&run.version===version} flowPublished={flowPublished} sourceReady={sourceReady} resultBlocked={!publishable}/>:<><Purpose purpose={view==="flow"?"用可复用的处理步骤，将授权数据形成符合模型的成果。":"依据质量、完整性和权限要求，发布可用于后续共享办理的成果。"} before={view==="flow"?"有效来源及加工权限，已发布模型。":"实际运行证据和固定的来源、模型及处理版本。"} after={view==="flow"?"保存 → 发布定义 → 完整运行 → 成果核验。":"核验发布 → 按需办理目录挂接、审核与使用授权。"}/><section className="proto-panel section-gap"><div className="proto-section-heading"><h2>{view==="flow"?"处理定义与运行":"待核验成果与运行记录"}</h2><Space wrap><span className="proto-small">演示场景</span><Select aria-label="演示场景" value={scenario} style={{width:210}} onChange={switchScenario} options={[{value:"ready",label:"条件满足 · 正常路径"},{value:"quality",label:"质量异常 · 整改复测"},{value:"source",label:"来源变更 · 修订后重跑"}]}/></Space></div><Table rowKey="id" pagination={false} scroll={{x:720}} dataSource={[{id:"customer",name:view==="flow"?name:"客户基础数据",version:view==="flow"?`v${version}`:`数据流 v${run?.version??"—"}`,state:view==="flow"?(flowPublished?"定义已发布":"草稿待发布"):(published?"示例成果已发布":publishable?"待核验发布":"存在阻断条件")}]} columns={[{title:view==="flow"?"数据流":"成果 / 对应运行",dataIndex:"name",render:value=><Button type="link" onClick={view==="flow"?openFlow:openResult}>{value}</Button>},{title:"固定版本",dataIndex:"version"},{title:"当前状态",dataIndex:"state"},{title:"下一步",render:()=><Button type="primary" onClick={view==="flow"?openFlow:openResult}>{view==="flow"?"打开设计 Dialog":"打开核验 Dialog"}</Button>}]}/><p className="proto-small proto-table-note">场景切换会重置交互稿中的示例状态。运行计数、来源与权限均为预设演示，不代表主环境核验结果。</p></section></>}
 </main></div>{panel&&<GovernanceDialog header={header} width={width} onClose={close} closeLabel={closeLabel} busy={!!phase} actions={actions}>{content}</GovernanceDialog>}</div>
}

const prototypeRoot=createRoot(document.getElementById("root")!);
prototypeRoot.render(<ConfigProvider theme={appTheme} locale={zhCN}><AntApp><Prototype/></AntApp></ConfigProvider>);

if(import.meta.hot)import.meta.hot.dispose(()=>prototypeRoot.unmount());

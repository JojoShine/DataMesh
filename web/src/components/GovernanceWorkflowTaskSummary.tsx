import type {GovernanceWorkflowTask,GovernanceRecord} from "../api/governance";
import type {GovernanceNodeContext} from "../api/governanceNodeInfo";
import {useQuery} from "../hooks/useQuery";
import {useGovernanceFlowNodeContext} from "../hooks/useGovernanceRunNodeContext";
import {GovernanceNodeLabel} from "./GovernanceNodeLabel";
import {Failure} from "./common";

export const governanceTaskNames={dataflow:"数据流计算",quality_check:"独立质量检查",publish_approval:"成果发布审批"};
export function governanceTaskChoice(task:GovernanceWorkflowTask){return `${governanceTaskNames[task.kind??"dataflow"]} · ${task.id||"待填写任务编码"}${(task.kind??"dataflow")==="dataflow"&&task.dataflow.id?` · ${task.dataflow.id} · v${task.dataflow.version}`:""}`;}
export function GovernanceWorkflowFlowDetails({task,tasks,context}:{task:GovernanceWorkflowTask;tasks:GovernanceWorkflowTask[];context:GovernanceNodeContext}){
 return context.graph.nodes.length>0?<details className="governance-node-rules"><summary>查看选择的数据、字段与处理规则</summary><div className="governance-detail-stack">{context.graph.nodes.map(node=>{const replacement=task.inputs?.[node.id];const upstream=tasks.find(other=>other.id===replacement);return <div key={node.id}><GovernanceNodeLabel id={node.id} context={context}/>{replacement&&<p className="helper-text">本次替换为前置成果：{upstream?governanceTaskChoice(upstream):replacement}；上方为数据流声明的原来源。</p>}</div>})}</div></details>:null;
}
function FlowDetails({record,task,tasks}:{record:GovernanceRecord|null;task:GovernanceWorkflowTask;tasks:GovernanceWorkflowTask[]}){
 const {context,errors}=useGovernanceFlowNodeContext(record);
 return <>{errors.length>0&&<Failure error={errors[0]}/>}<GovernanceWorkflowFlowDetails task={task} tasks={tasks} context={context}/></>;
}
export function GovernanceWorkflowTaskSummary({task,tasks,compact=false}:{task:GovernanceWorkflowTask;tasks:GovernanceWorkflowTask[];compact?:boolean}){
 const input=tasks.find(item=>item.id===task.input_task);
 const target=(task.kind??"dataflow")==="dataflow"?task:input;
 const flow=useQuery<GovernanceRecord>(target?.dataflow.id?`/api/v1/governance/dataflows/${encodeURIComponent(target.dataflow.id)}/versions/${target.dataflow.version}`:null);
 return <div className="governance-node-description"><strong>{governanceTaskNames[task.kind??"dataflow"]}{flow.data?` · ${flow.data.definition.name} · v${flow.data.version}`:""}</strong><span className="helper-text">{task.id||"待填写任务编码"}{!flow.data&&target?.dataflow.id?` · ${target.dataflow.id} · v${target.dataflow.version}`:""}</span>{!compact&&<>{flow.error&&<Failure error={flow.error}/>} {(task.kind??"dataflow")==="dataflow"&&!task.dataflow.id?<span>尚未选择数据流</span>:flow.loading?<span>正在读取固定版本配置</span>:null}{task.input_task&&<span>前置成果：{input?governanceTaskChoice(input):task.input_task}</span>}{task.kind==="quality_check"&&<span>允许隔离比例：{task.max_failure_rate??0}；按前置成果固定模型核验</span>}{task.kind==="publish_approval"&&<span>等待有成果发布职责的人员核验来源、质量与共享依据</span>}<FlowDetails record={flow.data} task={target??task} tasks={tasks}/></>}</div>;
}

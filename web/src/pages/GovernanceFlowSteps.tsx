import {governanceNodeInfo,type GovernanceNodeContext} from "../api/governanceNodeInfo";
import {Button,Steps} from "antd";
import {useEffect,useState} from "react";
import type {GovernanceDataflow,GovernanceFlowKind,GovernanceFlowNode,MetadataResource} from "../api/governance";
import {businessFlowSequence} from "./governanceFlowEditing";
import {flowEditorFields} from "./GovernanceFlowGraph";
import {governanceFieldLabel} from "../api/governanceFieldLabels";

export const businessFlowNames:Record<GovernanceFlowKind,string>={source:"选择数据",project:"选择保留的数据项",transform:"清理文字",filter:"筛选需要的记录",deduplicate:"去掉重复记录",join:"关联数据表",aggregate:"分组统计",quality:"检查数据质量",code_map:"统一分类值",script:"自定义处理",output:"确认输出结果"};
export const businessFlowDescriptions:Record<GovernanceFlowKind,string>={source:"选择要整理的数据，再勾选需要使用的数据项。",project:"只保留业务需要的数据项，也可以调整它们的名称。",transform:"去除多余空格，或统一文字的大小写。",filter:"设置条件，只保留符合条件的记录。",deduplicate:"选择判断重复的依据，相同记录只保留第一条。",join:"按相同编号等信息，把两份数据补充到一起。",aggregate:"按部门、地区等分类，计算数量或合计。",quality:"设置必填、格式等要求，检查哪些记录符合要求。",code_map:"把不同写法的分类值统一为约定的标准值。",script:"由技术人员维护的自定义规则，可在高级编排中查看。",output:"确认结果如何组织；运行后可以查看实际处理结果。"};
export const businessStepKinds:GovernanceFlowKind[]=["project","transform","filter","deduplicate","code_map","aggregate","quality"];
export function businessFlowTitle(node:GovernanceFlowNode,resources:Record<string,MetadataResource>,graph?:GovernanceDataflow):string{
 if(node.kind==="join"&&graph){const right=graph.nodes.find(n=>n.id===node.inputs?.[1]?.node),resource=right?.source&&resources[right.source.resource.resource_id];const name=resource?.display_name||resource?.qualified_name;if(name)return `${businessFlowNames.join} · ${name}`;}
 const source=node.source&&resources[node.source.resource.resource_id];
 return source?.display_name||source?.qualified_name||businessFlowNames[node.kind];
}
function summary(node:GovernanceFlowNode,graph:GovernanceDataflow,resources:Record<string,MetadataResource>,pinned:GovernanceNodeContext["pinned"],lightweight=false):string{
 const fields=flowEditorFields(graph,node.inputs?.[0],resources);
 const fieldName=(name:string)=>governanceFieldLabel(fields.find(f=>f.name===name)??{name});
 switch(node.kind){
  case "source":return governanceNodeInfo(node.id,{graph,resources,pinned}).description;
  case "project":return governanceNodeInfo(node.id,{graph,resources,pinned}).description;
  case "transform":{const changed=node.columns?.filter(c=>c.rule&&c.rule!=="copy")??[];return changed.length?changed.map(c=>`${fieldName(c.source)}：${c.rule==="trim"?"去除首尾空白":c.rule==="upper"?"转为大写":"转为小写"}`).join("；"):"选择要清理的文字和处理方式";}
  case "filter":{const condition=node.condition;if(!condition?.field)return "请设置需要保留哪些记录";const operator=({eq:"等于",ne:"不等于",gt:"大于",gte:"大于等于",lt:"小于",lte:"小于等于",is_null:"为空",not_null:"不为空"})[condition.operator];return `只保留${fieldName(condition.field)}${operator}${["is_null","not_null"].includes(condition.operator)?"":condition.parameter?"本次运行填写的值":String(condition.value??"待填写")}的记录`;}
  case "deduplicate":return node.keys?.length?`${node.keys.map(fieldName).join("、")}相同视为重复，保留第一条`:"请选择判断重复的依据";
  case "join":{
   const right=graph.nodes.find(n=>n.id===node.inputs?.[1]?.node),resource=right?.source&&resources[right.source.resource.resource_id];
   const name=resource?.display_name||resource?.qualified_name||"待选择的数据表";
   const rightFields=flowEditorFields(graph,node.inputs?.[1],resources);
   const keys=node.join&&node.join.left_keys.length===node.join.right_keys.length?node.join.left_keys.map((key,i)=>`${fieldName(key)}对应${governanceFieldLabel(rightFields.find(f=>f.name===node.join?.right_keys[i])??{name:node.join?.right_keys[i]??""})}`).join("、"):"";
   return `补充「${name}」；${keys||"请选择两份数据的对应字段"}；${node.join?.kind==="left"?"保留原有全部记录":"只保留匹配的记录"}`;
  }
  case "script":if(lightweight)return `${node.script?.language==="javascript"?"JavaScript":"Python"} · ${node.script?.fields.length??0} 个输出字段`;return governanceNodeInfo(node.id,{graph,resources,pinned}).description;
  case "aggregate":return governanceNodeInfo(node.id,{graph,resources,pinned}).description;
  case "quality":return governanceNodeInfo(node.id,{graph,resources,pinned}).description;
  case "code_map":return governanceNodeInfo(node.id,{graph,resources,pinned}).description;
  case "output":return governanceNodeInfo(node.id,{graph,resources,pinned,lightweight}).description;
  default:return governanceNodeInfo(node.id,{graph,resources,pinned}).description;
 }
}
export function GovernanceFlowSteps({graph,selected,onSelect,resources,editing,onAddSource,pinned,lightweight=false}:{lightweight?:boolean;graph:GovernanceDataflow;selected:string;onSelect:(id:string)=>void;resources:Record<string,MetadataResource>;editing:boolean;onAddSource?:()=>void;pinned?:GovernanceNodeContext["pinned"]}){
 const sequence=businessFlowSequence(graph),nodes=sequence??graph.nodes;
 const selectedNode=graph.nodes.find(node=>node.id===selected);
 const selectedStage=selectedNode?.kind==="output"?2:selectedNode&&selectedNode.kind!=="source"?1:0;
 const [stage,setStage]=useState<number>(selectedStage);
 useEffect(()=>{if(selectedNode)setStage(selectedStage)},[selected,selectedNode?.kind,selectedStage]);
 const groups=[{title:"选数据",description:"先选主表，再逐张添加需要关联的数据表。",nodes:graph.nodes.filter(n=>n.kind==="source")},{title:"定规则",description:"按照业务需要，依次整理数据。",nodes:nodes.filter(n=>n.kind!=="source"&&n.kind!=="output")},{title:"确认输出",description:"处理后的数据要整理成什么样？",nodes:nodes.filter(n=>n.kind==="output")}];
 const group=groups[stage];
 function changeStage(index:number){setStage(index);const next=groups[index].nodes.find(node=>node.id===selected)??groups[index].nodes[0];onSelect(next?.id??"")}
 return <section className="governance-business-flow" aria-label="业务处理步骤">
  <nav className="governance-business-stepper" aria-label="业务阶段"><Steps size="small" responsive={false} current={stage} onChange={changeStage} items={groups.map((item,index)=>({title:item.title,status:index===stage?"process" as const:"wait" as const}))}/></nav>
  <p className="governance-business-intro">{editing?"点选一步填写要求，完成后保存并运行。关联表会补充到当前处理结果；新增整理步骤接在选中步骤之后，或放在输出之前。":"点选一步查看处理要求；需要调整时，点击下方“基于此版本修订”。"}</p>
  {!sequence&&<p className="helper-text governance-business-complex">这份方案包含多路处理或待完善的关系。这里按来源、规则和输出展示；调整处理顺序或数据关联请使用高级编排。</p>}
  <section className="governance-business-stage" aria-label="当前业务阶段" key={group.title}>
   <div className="governance-business-stage-heading"><h3>{group.title}</h3>{stage===0&&onAddSource&&<Button onClick={onAddSource}>添加关联表</Button>}<p className="helper-text">{group.description}</p></div>
   <ol>{group.nodes.map(node=><li key={node.id}><button type="button" className={`governance-business-step${selected===node.id?" selected":""}`} aria-pressed={selected===node.id} onClick={()=>onSelect(node.id)}><strong>{businessFlowTitle(node,resources,graph)}</strong>{node.kind==="source"&&sequence&&<small>{sequence[0].id===node.id?"主表":"关联表"}</small>}<span title={summary(node,graph,resources,pinned,lightweight)}>{summary(node,graph,resources,pinned,lightweight)}</span>{!sequence&&node.inputs?.length? <small>使用：{node.inputs.map(edge=>{const input=graph.nodes.find(n=>n.id===edge.node);return input?`${businessFlowTitle(input,resources,graph)}${edge.port==="bad"?"（不合格记录）":edge.port==="good"?"（合格记录）":""}`:"尚未选择数据"}).join("、")}</small>:null}</button></li>)}</ol>
   {!group.nodes.length&&<p className="helper-text governance-business-empty">{stage===1?editing?"暂不需要整理也可以直接输出。需要时，点击下方“添加整理步骤”。":"当前方案直接使用来源数据。":"尚未配置，请在高级编排中补充。"}</p>}
  </section>
 </section>;
}

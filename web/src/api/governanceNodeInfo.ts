import type {GovernanceDataflow,GovernanceFlowExecution,GovernanceFlowNode,MetadataResource} from "./governance";
import {governanceFlowNames} from "./governance";
import {flowEditorFields} from "./governanceFlowFields";
import {governanceQualityParameters} from "./governanceQuality";

export interface GovernanceNodeContext {lightweight?:boolean;graph:GovernanceDataflow;resources?:Record<string,MetadataResource>;pinned?:GovernanceFlowExecution["pinned"];inputs?:GovernanceFlowExecution["inputs"]}
export function governanceRunNodeContext(execution:Pick<GovernanceFlowExecution,"flow"|"pinned"|"inputs"|"lightweight">):GovernanceNodeContext{
 const definition=execution.pinned.find(item=>item.kind==="dataflow"&&item.ref?.id===execution.flow.id&&item.ref?.version===execution.flow.version)?.definition;
 return {lightweight:Boolean(execution.lightweight),graph:definition?.dataflow??{nodes:[]},pinned:execution.pinned,inputs:execution.inputs};
}
const checkNames={format:"格式检查",required:"必填",business_key:"唯一业务主键",type:"类型",length:"文本长度",range:"数值范围",enum:"枚举值",compare_fields:"跨字段比较",reference_exists:"跨表参照存在"};
const operators:Record<string,string>={eq:"等于",ne:"不等于",gt:"大于",gte:"大于等于",lt:"小于",lte:"小于等于",is_null:"为空",not_null:"非空"};
const operations={count:"计数",sum:"求和",min:"取最小值",max:"取最大值"};
export function governanceNodeInfo(id:string,context:GovernanceNodeContext):{name:string;description:string;reference:string}{
 const node=context.graph.nodes.find(item=>item.id===id);
 if(!node)return {name:id?"未保存的节点":"整体运行",description:id?"本次记录未保存此节点的配置":"运行整体状态与进度",reference:id};
 const resources=Object.fromEntries(Object.entries(context.resources??{}).filter(([resourceID,resource])=>context.graph.nodes.filter(item=>item.source?.resource.resource_id===resourceID).every(item=>item.source?.resource.contract_version===resource.schema_version)));
 const fields=flowEditorFields(context.graph,node.inputs?.[0],resources,new Set(),context.inputs);
 const field=(name:string,side=0)=>{const known=(side===0?fields:flowEditorFields(context.graph,node.inputs?.[side],resources,new Set(),context.inputs)).find(item=>item.name===name);return known?.business_name?`${known.business_name}（${name}）`:name;};
 const reference=(kind:"model"|"standard"|"quality_template",ref:{id:string;version:number}|undefined)=>ref?`${context.pinned?.find(item=>item.kind===kind&&item.ref?.id===ref.id&&item.ref.version===ref.version)?.definition.name??({model:"模型",standard:"标准",quality_template:"检查方案"})[kind]+" "+ref.id} · v${ref.version}`:"尚未选择";
 const selected=(values:string[]|undefined)=>values?.length?values.map(value=>field(value)).join("、"):"尚未选择";
 let description:string;
 switch(node.kind){
  case "source":{
   const source=node.source;const resource=resources[source?.resource.resource_id??""];const known=resource?.fields??context.inputs?.[node.id]?.fields??[];
   const names=source?.fields.map(name=>{const label=resource?.field_labels?.[name]??known.find(item=>item.name===name)?.business_name;return label?`${label}（${name}）`:name;}).join("、");
   const name=resource?.display_name||resource?.qualified_name||context.inputs?.[node.id]?.source_name||source?.resource.resource_id;
   description=name?`${name} · v${source?.resource.contract_version}；选取：${names||"尚未选择字段"}`:"尚未选择来源数据与字段";break;
  }
  case "project":description=node.columns?.length?`保留：${node.columns.map(column=>`${field(column.source)}${column.target!==column.source?` → ${column.target}`:""}`).join("；")}`:"尚未选择保留的字段";break;
  case "transform":description=node.columns?.length?node.columns.map(column=>`${field(column.source)}：${({trim:"去除首尾空白",upper:"转换为大写",lower:"转换为小写",copy:"保持原值"})[column.rule||"copy"]}${column.target!==column.source?` → ${column.target}`:""}`).join("；"):"尚未选择清洗字段与方式";break;
  case "filter":{const value=node.condition;description=value?.field?`${field(value.field)}${operators[value.operator]}${["is_null","not_null"].includes(value.operator)?"":value.parameter?` 运行参数 ${value.parameter}`:` ${value.value===null?"空值":value.value===undefined?"待填写":String(value.value)}`}`:"尚未选择筛选字段与条件";break;}
  case "deduplicate":description=`去重依据：${selected(node.keys)}；保留第一条`;break;
  case "aggregate":description=`${node.group_by?.length?`按${selected(node.group_by)}分组`:"不分组，汇总全部记录"}；${node.aggregates?.length?node.aggregates.map(item=>`${field(item.source)}${operations[item.operation]} → ${item.target}`).join("；"):"尚未选择计算字段与方式"}`;break;
  case "join":{const join=node.join;description=join?`${join.kind==="left"?"保留左侧全部记录":"只保留两边匹配记录"}；${join.left_keys.length?join.left_keys.map((key,index)=>`${field(key)} = ${field(join.right_keys[index]??"待选择",1)}`).join("、"):"尚未选择对应字段"}；保留：${join.columns.map(column=>`${column.side==="left"?"左表":"右表"}.${field(column.source,column.side==="left"?0:1)} → ${column.target}`).join("、")||"尚未选择输出字段"}` :"尚未选择关联条件";break;}
  case "quality":{
   const template=node.template&&context.pinned?.find(item=>item.kind==="quality_template"&&item.ref?.id===node.template?.id&&item.ref.version===node.template.version)?.definition.quality_template;
   const checks=node.template?template?.checks:node.checks;
   description=`${node.template?reference("quality_template",node.template)+"；":""}${checks?.length?checks.map(check=>`${field(check.field)}：${checkNames[check.kind]}${governanceQualityParameters(check)?`（${governanceQualityParameters(check)}）`:""}`).join("；"):node.template?"按固定版本方案检查":"尚未配置检查要求"}`;break;
  }
  case "code_map":description=node.code?`${field(node.code.field)}；${reference("standard",node.code.standard)}；${node.code.values.length?node.code.values.map(value=>`${value.from} → ${value.to}`).join("、"):"尚未设置分类值对应关系"}；未匹配时${({keep:"保留原值",null:"置为空值",reject:"隔离记录"})[node.code.unmatched]}`:"尚未选择分类字段与标准";break;
  case "script":description=node.script?`${node.script.language==="python"?"Python":"JavaScript"} · ${node.script.runtime_version}；输出字段：${node.script.fields.map(item=>item.description?`${item.description}（${item.name}）`:item.name).join("、")||"尚未声明"}`:"尚未配置自定义处理";break;
  case "output":description=node.infer_model?`${context.lightweight?"按结果自动确定字段":"根据结果生成模型"}；结果字段：${fields.map(item=>field(item.name)).join("、")||"尚未确定"}`:`输出模型：${reference("model",node.model)}；结果字段：${fields.map(item=>field(item.name)).join("、")||"尚未确定"}`;break;
 }
 return {name:governanceFlowNames[node.kind],description,reference:node.id};
}
export function governanceNodeChoice(node:GovernanceFlowNode,context:GovernanceNodeContext){const info=governanceNodeInfo(node.id,context);return `${info.name} · ${info.description} · ${node.id}`;}

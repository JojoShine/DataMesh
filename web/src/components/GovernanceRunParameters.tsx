import {governanceFieldTooltip} from "./GovernanceFieldHelp";
import {Form,Input,Select} from "antd";
import type {GovernanceDataflow} from "../api/governance";

export type GovernanceParameterValues=Record<string,string|number|boolean>;
export function governanceParameterDefaults(graph:GovernanceDataflow):GovernanceParameterValues{
 const values:GovernanceParameterValues={};for(const node of graph.nodes){Object.assign(values,node.script?.parameters??{});if(node.condition?.parameter){const value=node.condition.value;if(typeof value==="string"||typeof value==="number"||typeof value==="boolean")values[node.condition.parameter]=value}if(node.source?.parameter_bindings)for(const [name,parameter] of Object.entries(node.source.parameter_bindings))values[parameter]=node.source.parameters?.[name]??""}return values;
}
export function GovernanceRunParameters({defaults,value={},onChange}:{defaults:GovernanceParameterValues;value:GovernanceParameterValues;onChange:(value:GovernanceParameterValues)=>void}){
 if(!Object.keys(defaults).length)return null;
 return <div className="governance-editor-panel"><h3>本次运行参数</h3><p className="helper-text">仅替换已声明的筛选常量或API查询参数，使用固定定义及模型版本，不执行表达式。</p><div>{Object.entries(defaults).map(([name,fallback])=><Form.Item layout="vertical" key={name} label={name} tooltip={governanceFieldTooltip("运行参数")}>{typeof fallback==="boolean"?<Select aria-label={`运行参数 ${name}`} placeholder="请选择布尔参数" value={value[name]??fallback} options={[{value:true,label:"是"},{value:false,label:"否"}]} onChange={next=>onChange({...value,[name]:next})}/>:<Input aria-label={`运行参数 ${name}`} placeholder="请输入本次运行值，留空也会作为参数值提交" maxLength={2048} value={String(value[name]??fallback)} onChange={e=>onChange({...value,[name]:e.target.value})}/>}</Form.Item>)}</div></div>;
}

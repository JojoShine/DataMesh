import type {Field} from "./types";
import type {GovernanceDataflow,GovernanceFlowEdge,GovernanceFlowExecution,MetadataResource} from "./governance";

// These inferred fields only populate editor choices. The server checks every graph and output contract.
export function flowEditorFields(graph:GovernanceDataflow,edge:GovernanceFlowEdge|undefined,resources:Record<string,MetadataResource>,visited=new Set<string>(),inputs:GovernanceFlowExecution["inputs"]={}):Field[]{
 if(!edge||visited.has(edge.node))return [];visited.add(edge.node);
 const node=graph.nodes.find(n=>n.id===edge.node);if(!node)return [];
 if(node.kind==="source")return (inputs[node.id]?.fields??resources[node.source?.resource.resource_id??""]?.fields??[]).filter(f=>node.source?.fields.includes(f.name)).map(f=>({...f,business_name:resources[node.source?.resource.resource_id??""]?.field_labels?.[f.name]??f.business_name}));
 const input=flowEditorFields(graph,node.inputs?.[0],resources,new Set(visited),inputs);
 if(node.kind==="script")return (node.script?.fields??[]).map(f=>({name:f.name,business_name:f.description,logical_type:f.logical_type,precision:f.logical_type==="decimal"?f.precision:null,scale:f.logical_type==="decimal"?f.scale:null,nullable:f.nullable,primary_key:false,sensitive:f.sensitive||input.some(v=>v.sensitive)}));
 if(node.kind==="project"||node.kind==="transform")return (node.columns??[]).flatMap(column=>{const field=input.find(f=>f.name===column.source);return field?[{...field,name:column.target}]:[]});
 if(node.kind==="join"){
  const right=flowEditorFields(graph,node.inputs?.[1],resources,new Set(visited),inputs);
  return (node.join?.columns??[]).flatMap(column=>{const field=(column.side==="left"?input:right).find(f=>f.name===column.source);return field?[{...field,name:column.target,primary_key:false,nullable:field.nullable||column.side==="right"&&node.join?.kind==="left"}]:[]});
 }
 if(node.kind==="aggregate")return [...input.filter(f=>node.group_by?.includes(f.name)).map(f=>({...f,primary_key:false})),...(node.aggregates??[]).flatMap(aggregate=>{const field=input.find(f=>f.name===aggregate.source);return field?[{...field,name:aggregate.target,business_name:field.business_name?`${({sum:"合计",count:"数量",min:"最小值",max:"最大值",avg:"平均值"})[aggregate.operation]} · ${field.business_name}`:undefined,primary_key:false,logical_type:aggregate.operation==="count"?"integer":field.logical_type,nullable:aggregate.operation!=="count"}]:[]})];
 if(node.kind==="quality"&&edge.port==="good")return input.map(field=>({...field,nullable:field.nullable&&!(node.checks??[]).some(check=>check.field===field.name&&check.blocking&&(check.kind==="required"||check.kind==="business_key"))}));
 if(node.kind==="filter"&&node.condition?.operator!=="is_null")return input.map(field=>({...field,nullable:field.name===node.condition?.field?false:field.nullable}));
 if(node.kind==="code_map"&&node.code?.unmatched==="null")return input.map(field=>({...field,nullable:field.nullable||field.name===node.code?.field}));
 return input;
}

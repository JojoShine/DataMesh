import type {GovernanceDataflow,GovernanceFlowEdge,GovernanceFlowNode} from "../api/governance";
export interface FlowPosition {x:number;y:number}
export interface GovernanceFlowLayout {revision:number;positions:Record<string,FlowPosition>;viewport:{x:number;y:number;zoom:number}}
export interface FlowConnection {source:string;sourcePort?:string|null;target:string;targetIndex:number}
export const emptyFlowLayout=():GovernanceFlowLayout=>({revision:0,positions:{},viewport:{x:0,y:0,zoom:1}});
export function automaticFlowPositions(graph:GovernanceDataflow):Record<string,FlowPosition>{
 const depth=(id:string,seen=new Set<string>()):number=>{if(seen.has(id))return 0;seen.add(id);const node=graph.nodes.find(n=>n.id===id);return node?.inputs?.some(e=>e.node)?Math.min(40,1+Math.max(...node.inputs.map(e=>depth(e.node,new Set(seen))))):0};
 const used:Record<number,number>={};return Object.fromEntries(graph.nodes.map(node=>{const col=depth(node.id),row=used[col]??0;used[col]=row+1;return [node.id,{x:col*280,y:row*160}]}));
}
function hasBadInput(graph:GovernanceDataflow,id:string,seen=new Set<string>()):boolean{
 if(seen.has(id))return false;seen.add(id);return graph.nodes.find(n=>n.id===id)?.inputs?.some(e=>e.port==="bad"||hasBadInput(graph,e.node,seen))??false;
}
export function validateFlowConnection(graph:GovernanceDataflow,c:FlowConnection):string|null{
 const source=graph.nodes.find(n=>n.id===c.source),target=graph.nodes.find(n=>n.id===c.target);
 if(!source||!target)return "请选择已有的处理节点";
 if(source.id===target.id)return "节点不能连接到自身";
 if(source.kind==="output"||target.kind==="source")return "来源只提供输出，模型输出只接收输入";
 const count=target.kind==="join"||target.kind==="quality"&&(target.inputs?.length??0)>1?2:1;
 if(c.targetIndex<0||c.targetIndex>=count)return "该节点没有这个输入端口";
 if(source.kind==="quality"?!["good","bad"].includes(c.sourcePort??""):Boolean(c.sourcePort))return "请选择正确的输出分支";
 if(target.inputs?.[c.targetIndex]?.node)return "此输入已连接，请先断开或拖动原连线重新连接";
 if(target.kind==="output"&&(c.sourcePort==="bad"||hasBadInput(graph,source.id)))return "不合格数据分支不能进入模型成果";
 const reaches=(id:string,seen=new Set<string>()):boolean=>{if(id===target.id)return true;if(seen.has(id))return false;seen.add(id);return graph.nodes.find(n=>n.id===id)?.inputs?.some(e=>reaches(e.node,seen))??false};
 if(reaches(source.id))return "这条连线会形成循环，请调整处理顺序";
 return null;
}
export function connectFlow(graph:GovernanceDataflow,c:FlowConnection):GovernanceDataflow{
 const reason=validateFlowConnection(graph,c);if(reason)throw new Error(reason);
 return {nodes:graph.nodes.map(node=>{if(node.id!==c.target)return node;const inputs=[...(node.inputs??[])];while(inputs.length<=c.targetIndex)inputs.push({node:""});inputs[c.targetIndex]={node:c.source,...(c.sourcePort?{port:c.sourcePort==="good"?"good":"bad"}:{})};return {...node,inputs}})};
}
export function disconnectFlow(graph:GovernanceDataflow,target:string,index:number):GovernanceDataflow{
 return {nodes:graph.nodes.map(node=>node.id===target?{...node,inputs:node.inputs?.map((edge,i)=>i===index?{node:""}:edge)??null}:node)};
}
export function removeFlowNode(graph:GovernanceDataflow,id:string):GovernanceDataflow{return {nodes:graph.nodes.filter(n=>n.id!==id).map(n=>({...n,inputs:n.inputs?.map(e=>e.node===id?{node:""}:e)??null}))}}
export function nextFlowNodeID(graph:GovernanceDataflow,kind:GovernanceFlowNode["kind"]):string{let i=1;while(graph.nodes.some(n=>n.id===`${kind}_${i}`))i++;return `${kind}_${i}`}
export function flowEdgeID(target:string,index:number){return `${target}:${index}`}
export function flowEdgeAt(graph:GovernanceDataflow,id:string):{target:string;index:number;edge:GovernanceFlowEdge}|null{
 for(const node of graph.nodes)for(const [index,edge] of (node.inputs??[]).entries())if(flowEdgeID(node.id,index)===id)return {target:node.id,index,edge};return null;
}

// Automatic edits support one main path with exclusive source leaves for joins.
export function businessFlowSequence(graph:GovernanceDataflow):GovernanceFlowNode[]|null{
 const outputs=graph.nodes.filter(n=>n.kind==="output");
 if(outputs.length!==1||new Set(graph.nodes.map(n=>n.id)).size!==graph.nodes.length)return null;
 const sequence:GovernanceFlowNode[]=[],visited=new Set<string>();
 let current:GovernanceFlowNode|undefined=outputs[0];
 while(current){
  if(visited.has(current.id))return null;
  visited.add(current.id);sequence.unshift(current);
  if(current.kind==="source")return !current.inputs?.length&&visited.size===graph.nodes.length?sequence:null;
  const inputs:GovernanceFlowEdge[]=current.inputs??[];
  if(current.kind==="join"){
   if(inputs.length!==2)return null;
   const right=graph.nodes.find(n=>n.id===inputs[1].node);
   if(!right||right.kind!=="source"||right.inputs?.length||inputs[1].port||visited.has(right.id))return null;
   visited.add(right.id);
  }else if(inputs.length!==1)return null;
  const previous:GovernanceFlowNode|undefined=graph.nodes.find(n=>n.id===inputs[0].node);
  if(!previous||previous.kind==="output"||(previous.kind==="quality"?inputs[0].port!=="good":Boolean(inputs[0].port)))return null;
  current=previous;
 }
 return null;
}
export function insertBusinessFlowStep(graph:GovernanceDataflow,node:GovernanceFlowNode,after:string):GovernanceDataflow{
 const sequence=businessFlowSequence(graph),index=sequence?.findIndex(n=>n.id===after)??-1;
 if(!sequence||index<0||index>=sequence.length-1||["source","output","join"].includes(node.kind)||graph.nodes.some(n=>n.id===node.id))throw new Error("请在高级编排中调整这份数据的处理关系。");
 const following=sequence[index+1],incoming=following.inputs?.[0];
 if(!incoming)throw new Error("请先完善处理关系。");
 const added={...node,inputs:[{...incoming}]};
 return {nodes:graph.nodes.flatMap(n=>n.id===following.id?[added,{...n,inputs:n.inputs?.map((edge,i)=>i===0?{node:node.id,...(node.kind==="quality"?{port:"good" as const}:{})}:edge)??null}]:[n])};
}
export function insertBusinessFlowJoin(graph:GovernanceDataflow,source:GovernanceFlowNode,node:GovernanceFlowNode,after:string):GovernanceDataflow{
 const sequence=businessFlowSequence(graph),index=sequence?.findIndex(n=>n.id===after)??-1;
 if(!sequence||index<0||index>=sequence.length-1||graph.nodes.length>38||source.kind!=="source"||source.inputs?.length||node.kind!=="join"||source.id===node.id||graph.nodes.some(n=>n.id===node.id||n.id===source.id))throw new Error("请核对关联表和处理关系；复杂关系请使用高级编排。");
 const following=sequence[index+1],incoming=following.inputs?.[0];
 if(!incoming)throw new Error("请先完善处理关系。");
 const added={...node,inputs:[{...incoming},{node:source.id}]};
 return {nodes:graph.nodes.flatMap(n=>n.id===following.id?[source,added,{...n,inputs:n.inputs?.map((edge,i)=>i===0?{node:node.id}:edge)??null}]:[n])};
}
export function removeBusinessFlowStep(graph:GovernanceDataflow,id:string):GovernanceDataflow{
 const sequence=businessFlowSequence(graph),index=sequence?.findIndex(n=>n.id===id)??-1;
 if(!sequence||index<=0||index>=sequence.length-1)throw new Error("请在高级编排中调整这份数据的处理关系。");
 const removed=sequence[index],following=sequence[index+1],incoming=removed.inputs?.[0];
 if(!incoming)throw new Error("请先完善处理关系。");
 const removedIDs=new Set([id,...(removed.kind==="join"?[removed.inputs?.[1]?.node??""]:[])]);
 return {nodes:graph.nodes.filter(n=>!removedIDs.has(n.id)).map(n=>n.id===following.id?{...n,inputs:n.inputs?.map((edge,i)=>i===0?{...incoming}:edge)??null}:n)};
}

export function flowRunIssues(graph:GovernanceDataflow):{node:string;message:string}[]{
 const issues:{node:string;message:string}[]=[];
 if(!graph.nodes.some(node=>node.kind==="source"))issues.push({node:"",message:"请添加数据来源。"});
 if(!graph.nodes.some(node=>node.kind==="output"))issues.push({node:"",message:"请配置结果输出。"});
 for(const node of graph.nodes){
  if(node.kind==="source"){
   if(!node.source?.resource.resource_id)issues.push({node:node.id,message:"请选择要读取的数据表。"});
   else if(!node.source.fields.length)issues.push({node:node.id,message:"请至少选择一个读取字段。"});
  }else{
   const expected=node.kind==="join"?2:1;
   if((node.inputs?.length??0)<expected||node.inputs?.some(input=>!graph.nodes.some(source=>source.id===input.node)))issues.push({node:node.id,message:node.kind==="join"?"请连接左、右两份数据。":"请连接上一步数据。"});
  }
  if(node.kind==="output"&&!node.infer_model&&!node.model)issues.push({node:node.id,message:"请选择已有模型，或选择根据结果生成模型。"});
  if(node.kind==="join"){
   const join=node.join;
   if(!join||!["inner","left"].includes(join.kind))issues.push({node:node.id,message:"请选择两份数据的关联方式。"});
   if(!join?.left_keys.length||!join?.right_keys.length)issues.push({node:node.id,message:"请选择左、右两侧互相对应的字段，例如两张表的业务主体编号。"});
   else if(join.left_keys.length!==join.right_keys.length)issues.push({node:node.id,message:"左、右两侧对应字段的数量需要一致，并按相同顺序配对。"});
   else if(join.left_keys.length>10)issues.push({node:node.id,message:"每次关联最多使用 10 组对应字段。"});
   if(!join?.columns.length)issues.push({node:node.id,message:"请选择关联结果要保留的字段：添加左侧或右侧输出字段。"});
   else if(join.columns.length>100)issues.push({node:node.id,message:"每次关联最多保留 100 个输出字段，请移除不需要的字段。"});
   else if(join.columns.some(c=>!c.target.trim())||new Set(join.columns.map(c=>c.target)).size!==join.columns.length)issues.push({node:node.id,message:"关联输出的字段名称不能为空或重复，请修改输出名称。"});
  }
 }
 return issues;
}

import {governanceNodeInfo,type GovernanceNodeContext} from "../api/governanceNodeInfo";
import {flowEditorFields} from "../api/governanceFlowFields";
export {flowEditorFields} from "../api/governanceFlowFields";
import {useEffect,useMemo,useRef,useState} from "react";
import {Button,Dropdown,Space,Tooltip} from "antd";
import {ReactFlow,Background,Controls,Handle,Position,MarkerType,applyNodeChanges,type Node,type NodeProps,type ReactFlowInstance,type Connection,type Edge} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {GovernanceDialogActions} from "../components/GovernanceDialog";
import {automaticFlowPositions,connectFlow,disconnectFlow,emptyFlowLayout,flowEdgeAt,flowEdgeID,removeFlowNode,validateFlowConnection,type FlowPosition,type GovernanceFlowLayout} from "./governanceFlowEditing";
import type {Field} from "../api/types";
import type {GovernanceDataflow,GovernanceFlowEdge,GovernanceFlowNode,MetadataResource} from "../api/governance";
import {scriptExamples,scriptFields} from "../components/GovernanceScriptConfig";
import {governanceFlowNames} from "../api/governance";

export function flowNodeIdentity(node:GovernanceFlowNode,resources:Record<string,MetadataResource>){
 const resource=node.kind==="source"?resources[node.source?.resource.resource_id??""]:undefined;
 return {label:resource?.display_name||resource?.qualified_name||governanceFlowNames[node.kind],reference:resource?.qualified_name||node.id};
}

// Populate only unconfigured downstream field mappings after an explicit source/input edit.
// Existing mappings, renames and conversion rules remain unchanged.
export function initializeFlowColumns(graph:GovernanceDataflow,from:string,resources:Record<string,MetadataResource>):GovernanceDataflow{
 const affected=new Set([from]);
 for(let pass=0;pass<graph.nodes.length;pass++){
  let expanded=false;
  for(const node of graph.nodes)if(!affected.has(node.id)&&node.inputs?.some(edge=>affected.has(edge.node))){affected.add(node.id);expanded=true;}
  if(!expanded)break;
 }
 let next=graph;
 for(let pass=0;pass<graph.nodes.length;pass++){
  let initialized=false;
  const nodes=next.nodes.map<GovernanceFlowNode>(node=>{
   if(!affected.has(node.id))return node;
   if(node.kind==="join"&&node.join&&!node.join.columns.length){
    const left=flowEditorFields(next,node.inputs?.[0],resources),right=flowEditorFields(next,node.inputs?.[1],resources);
    if(!left.length||!right.length)return node;
    const used=new Set<string>();
    const columns=(["left","right"] as const).flatMap(side=>(side==="left"?left:right).map(field=>{
     const base=side==="right"?`right_${field.name}`:field.name;let target=base,index=2;
     while(used.has(target))target=`${base}_${index++}`;
     used.add(target);return {side,source:field.name,target};
    }));
    if(columns.length>100)return node;
    initialized=true;return {...node,join:{...node.join,columns}};
   }
   if((node.kind!=="project"&&node.kind!=="transform")||node.columns?.length)return node;
   const fields=flowEditorFields(next,node.inputs?.[0],resources);
   if(!fields.length)return node;
   initialized=true;
   return {...node,columns:fields.map(field=>({source:field.name,target:field.name,rule:"copy"}))};
  });
  if(!initialized)break;
  next={...next,nodes};
 }
 return next;
}
type CanvasNodeData={kind:GovernanceFlowNode["kind"];label:string;reference:string;description:string;selected:boolean;secondInput:boolean;pick:()=>void};
function ProcessingNode({data}:NodeProps<Node<CanvasNodeData>>){return <>
 {data.kind!=="source"&&<Handle type="target" position={Position.Left} id="0" style={{top:data.secondInput?"35%":"50%"}} aria-label={data.secondInput?"左侧输入":"输入"}/>}
 {data.secondInput&&<Handle type="target" position={Position.Left} id="1" style={{top:"75%"}} aria-label="右侧输入"/>}
 <button type="button" aria-label={`${data.label} ${data.reference}`} aria-description={data.description} aria-pressed={data.selected} className={`governance-flow-node${data.selected?" selected":""}${data.kind==="quality"?" quality":""}`} onClick={data.pick}><strong title={data.label}>{data.label}</strong><span className="governance-flow-node-summary" title={data.description}>{data.description}</span><small title={data.reference}>{data.reference}</small></button>
 {data.kind!=="output"&&(data.kind==="quality"?<><Handle type="source" position={Position.Right} id="good" style={{top:"35%"}} aria-label="合格输出"/><Handle type="source" position={Position.Right} id="bad" style={{top:"75%",background:"#ff4d4f"}} aria-label="不合格输出"/></>:<Handle type="source" position={Position.Right} id="main" aria-label="输出"/>)}
 </>}
const nodeTypes={processing:ProcessingNode};
const emptyResources:Record<string,MetadataResource>={};
const paletteGroups:{label:string;kinds:GovernanceFlowNode["kind"][]}[]=[{label:"来源",kinds:["source"]},{label:"字段处理",kinds:["project","transform","code_map"]},{label:"条件与记录",kinds:["filter","deduplicate"]},{label:"关联与汇总",kinds:["join","aggregate"]},{label:"质量",kinds:["quality"]},{label:"自定义代码",kinds:["script"]}];
const palette:GovernanceFlowNode["kind"][]=["source","project","transform","script","filter","deduplicate","join","aggregate","quality","code_map"];
export function FlowGraph({graph,selected,onSelect,editable=false,layout,onLayoutChange,onGraphChange,onAdd,onError,onSaveLayout,resources=emptyResources,pinned}:{graph:GovernanceDataflow;selected:string;onSelect:(id:string)=>void;editable?:boolean;layout?:GovernanceFlowLayout;onLayoutChange?:(layout:GovernanceFlowLayout)=>void;onGraphChange?:(graph:GovernanceDataflow,initializeFrom?:string)=>void;onAdd?:(kind:GovernanceFlowNode["kind"],position?:FlowPosition,insertEdge?:string)=>void;onError?:(message:string)=>void;onSaveLayout?:()=>void;resources?:Record<string,MetadataResource>;pinned?:GovernanceNodeContext["pinned"]}){
 const [fallback,setFallback]=useState(emptyFlowLayout);const value=layout??fallback;const instance=useRef<ReactFlowInstance<Node<CanvasNodeData>>|null>(null);
 const [edgeID,setEdgeID]=useState<string|null>(null);const [localNodes,setLocalNodes]=useState<Node<CanvasNodeData>[]>([]);const [,refreshHistory]=useState(0);
 const selectRef=useRef(onSelect);useEffect(()=>{selectRef.current=onSelect},[onSelect]);
 const automatic=useMemo(()=>automaticFlowPositions(graph),[graph]);
 const initialPositions=useRef<Record<string,FlowPosition>>({});
 for(const node of graph.nodes)if(!initialPositions.current[node.id])initialPositions.current[node.id]=value.positions[node.id]??automatic[node.id];
 const stablePositions=Object.fromEntries(graph.nodes.map(node=>[node.id,value.positions[node.id]??initialPositions.current[node.id]]));
 const generated=useMemo<Node<CanvasNodeData>[]>(()=>graph.nodes.map(node=>({id:node.id,type:"processing",ariaRole:"group",width:216,height:136,position:value.positions[node.id]??initialPositions.current[node.id],data:{kind:node.kind,...flowNodeIdentity(node,resources),description:governanceNodeInfo(node.id,{graph,resources,pinned}).description,selected:node.id===selected,secondInput:node.kind==="join"||node.kind==="quality"&&(node.inputs?.length??0)>1,pick:()=>selectRef.current(node.id)}})),[graph,selected,value.positions,resources,pinned]);
 // React Flow clears handle bounds when measured dimensions disappear. Keep them
 // across configuration/selection updates so existing edges stay mounted.
 useEffect(()=>setLocalNodes(previous=>generated.map(node=>{
  const old=previous.find(item=>item.id===node.id);
  return old&&old.data.kind===node.data.kind&&old.data.secondInput===node.data.secondInput?{...node,measured:old.measured}:node;
 })),[generated]);
 const snapshot={graph,layout:value};const present=useRef(snapshot);const undo=useRef<typeof snapshot[]>([]),redo=useRef<typeof snapshot[]>([]);const replay=useRef(false);
 useEffect(()=>{if(JSON.stringify(present.current)!==JSON.stringify(snapshot)){if(!replay.current){undo.current=[...undo.current.slice(-49),structuredClone(present.current)];redo.current=[]}present.current=structuredClone(snapshot);refreshHistory(n=>n+1)}replay.current=false},[graph,value]);
 useEffect(()=>{
  const flow=instance.current;if(!flow||!Object.keys(value.positions).length)return;
  const current=flow.getViewport(),next=value.viewport;
  if(current.x!==next.x||current.y!==next.y||current.zoom!==next.zoom)void flow.setViewport(next);
 },[value.viewport,value.positions]);
 const setLayout=(next:GovernanceFlowLayout)=>{setFallback(next);onLayoutChange?.(next)};
 function history(back:boolean){const from=back?undo:redo,to=back?redo:undo,next=from.current.pop();if(!next)return;to.current.push(structuredClone(present.current));replay.current=true;present.current=structuredClone(next);onGraphChange?.(next.graph);setLayout(next.layout);refreshHistory(n=>n+1)}
 const edges=useMemo<Edge[]>(()=>graph.nodes.flatMap(node=>(node.inputs??[]).flatMap((edge,index)=>edge.node?[{id:flowEdgeID(node.id,index),source:edge.node,target:node.id,sourceHandle:edge.port||"main",targetHandle:String(index),label:edge.port==="good"?"合格":edge.port==="bad"?"不合格":undefined,selected:edgeID===flowEdgeID(node.id,index),style:{stroke:edge.port==="bad"?"#ff4d4f":"#60749A"},markerEnd:{type:MarkerType.ArrowClosed,color:edge.port==="bad"?"#ff4d4f":"#60749A"}}]:[])),[graph,edgeID]);
 const connection=(c:Connection)=>({source:c.source,target:c.target,sourcePort:c.sourceHandle==="main"?null:c.sourceHandle,targetIndex:Number(c.targetHandle??0)});
 function connect(c:Connection,base=graph){const input=connection(c),reason=validateFlowConnection(base,input);if(reason){onError?.(reason);return}onGraphChange?.(connectFlow(base,input),input.target)}
 return <>
 {editable&&<details className="governance-flow-toolbox"><summary>处理算子库 · 可拖入画布或点击添加</summary>{paletteGroups.map(group=><div className="governance-flow-palette-group" key={group.label}><span className="helper-text">{group.label}</span><Space size={16} wrap>{group.kinds.map(kind=><Tooltip key={kind} title={`添加${governanceFlowNames[kind]}`}><Button draggable onDragStart={e=>{e.dataTransfer.setData("application/datamesh-governance-kind",kind);e.dataTransfer.effectAllowed="copy"}} onClick={()=>onAdd?.(kind)}>{governanceFlowNames[kind]}</Button></Tooltip>)}</Space></div>)}</details>}
 <div className="governance-flow-canvas" role="group" aria-label="处理关系画布"><span role="img" aria-label="治理数据流图" className="governance-flow-description"/>
 <ReactFlow nodes={localNodes} edges={edges} nodeTypes={nodeTypes} onInit={flow=>{instance.current=flow;if(Object.keys(value.positions).length)void flow.setViewport(value.viewport)}} onNodesChange={changes=>setLocalNodes(nodes=>applyNodeChanges(changes,nodes))} onNodeDragStop={(_,node)=>setLayout({...value,positions:{...stablePositions,[node.id]:node.position}})} onMoveEnd={(event,viewport)=>{if((event||Object.keys(value.positions).length)&&(viewport.x!==value.viewport.x||viewport.y!==value.viewport.y||viewport.zoom!==value.viewport.zoom))setLayout({...value,viewport})}} onNodeClick={(_,node)=>{onSelect(node.id);setEdgeID(null)}} onEdgeClick={(_,edge)=>setEdgeID(edge.id)} onPaneClick={()=>setEdgeID(null)} onConnect={editable?connect:undefined} onReconnect={editable?(edge,c)=>{const old=flowEdgeAt(graph,edge.id);if(old)connect(c,disconnectFlow(graph,old.target,old.index))}:undefined} onEdgesDelete={editable?deleted=>{let next=graph;for(const edge of deleted){const at=flowEdgeAt(next,edge.id);if(at)next=disconnectFlow(next,at.target,at.index)}onGraphChange?.(next);setEdgeID(null)}:undefined} onNodesDelete={editable?deleted=>{let next=graph;for(const node of deleted)next=removeFlowNode(next,node.id);onGraphChange?.(next)}:undefined} nodesConnectable={editable} edgesReconnectable={editable} nodesDraggable={Boolean(onLayoutChange)} deleteKeyCode={editable?["Backspace","Delete"]:null} minZoom={.1} maxZoom={4} fitView={!Object.keys(value.positions).length} fitViewOptions={{padding:.2,maxZoom:1}} onDragOver={e=>{if(editable){e.preventDefault();e.dataTransfer.dropEffect="copy"}}} onDrop={e=>{e.preventDefault();if(!editable)return;const kind=e.dataTransfer.getData("application/datamesh-governance-kind");const found=palette.find(item=>item===kind);if(found)onAdd?.(found,instance.current?.screenToFlowPosition({x:e.clientX,y:e.clientY}))}} aria-label="可编排的数据治理流程"><Background gap={20}/><Controls position="bottom-right" showInteractive={false}/></ReactFlow>
 </div>
 <GovernanceDialogActions><Button onClick={()=>setLayout({...value,positions:automatic})}>自动排列</Button>{onSaveLayout&&<Button onClick={onSaveLayout}>保存画布布局</Button>}{editable&&<><Button disabled={!undo.current.length} onClick={()=>history(true)}>撤销</Button><Button disabled={!redo.current.length} onClick={()=>history(false)}>重做</Button>{edgeID&&<><Dropdown menu={{items:palette.filter(k=>k!=="source"&&k!=="join").map(kind=>({key:kind,label:`插入${governanceFlowNames[kind]}`})),onClick:({key})=>{const kind=palette.find(k=>k===key);if(kind)onAdd?.(kind,undefined,edgeID)}}}><Button>在选中连线中插入</Button></Dropdown><Button onClick={()=>{const at=flowEdgeAt(graph,edgeID);if(at)onGraphChange?.(disconnectFlow(graph,at.target,at.index));setEdgeID(null)}}>断开选中连线</Button></>}</>}</GovernanceDialogActions>
 </>
}
export function defaultFlowNode(kind:GovernanceFlowNode["kind"],id:string,input:GovernanceFlowEdge|undefined,fields:Field[]):GovernanceFlowNode{
 const node:GovernanceFlowNode={id,kind,inputs:input?[input]:[]};
 switch(kind){
  case "script":node.script={language:"python",runtime_version:"",source:scriptExamples.python,fields:scriptFields(fields)};break;
  case "source":node.inputs=[];node.source={resource:{resource_type:"table_resource",resource_id:"",contract_version:1},fields:[]};break;
  case "project":case "transform":node.columns=fields.map(f=>({source:f.name,target:f.name,rule:"copy"}));break;
  case "filter":node.condition={field:fields[0]?.name??"",operator:"not_null",value:null};break;
  case "deduplicate":node.keys=fields.filter(f=>f.primary_key).map(f=>f.name);if(!node.keys.length&&fields[0])node.keys=[fields[0].name];break;
  case "aggregate":node.group_by=fields.filter(f=>f.primary_key).map(f=>f.name);node.aggregates=[{operation:"sum",source:fields.find(f=>f.logical_type==="decimal")?.name??fields[0]?.name??"",target:"total_value"}];break;
  case "quality":node.checks=[{kind:"required",field:fields[0]?.name??"",blocking:true,parameters:{}}];break;
  case "code_map":node.code={field:fields.find(f=>f.logical_type==="string")?.name??"",standard:{id:"",version:1},values:[{from:"",to:""}],unmatched:"reject"};break;
  case "join":node.join={kind:"inner",left_keys:[],right_keys:[],columns:[]};break;
 }
 return node;
}

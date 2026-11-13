import {useEffect,useMemo,useState} from "react";
import type {GovernanceCodePreviewRun,GovernanceFlowRun,GovernanceRecord,MetadataResource} from "../api/governance";
import {governanceRunNodeContext,type GovernanceNodeContext} from "../api/governanceNodeInfo";
import {request} from "../api/client";
import {usePath} from "../app/navigation";
import {useSession} from "../stores/session";

export function useGovernanceRunNodeContext(run:GovernanceFlowRun|GovernanceCodePreviewRun|null){
 return useGovernanceNodeContext(run?governanceRunNodeContext(run.definition):{graph:{nodes:[]}},[run?.workspace_id,run?.id]);
}

export function useGovernanceFlowNodeContext(record:GovernanceRecord|null|undefined){
 return useGovernanceNodeContext({graph:record?.definition.dataflow??{nodes:[]}},[record?.id,record?.version],true);
}

export function useGovernanceNodeContext(base:GovernanceNodeContext,identity:unknown,resolveReferences=false){
 const epoch=useSession(state=>state.epoch);
 const path=usePath();const workspace=new URLSearchParams(path.split("?")[1]).get("workspace_id");
 const sources=base.graph.nodes.flatMap(node=>node.source?[node.source.resource]:[]).filter(ref=>base.resources?.[ref.resource_id]?.schema_version!==ref.contract_version);
 const refs=Array.from(new Map(sources.map(ref=>[`${ref.resource_id}:${ref.contract_version}`,ref])).values());
 const dependencies=(resolveReferences?base.graph.nodes:[]).flatMap(node=>[...(node.model?[{kind:"model" as const,ref:node.model}]:[]),...(node.template?[{kind:"quality_template" as const,ref:node.template}]:[]),...(node.code?[{kind:"standard" as const,ref:node.code.standard}]:[])]).filter((item,index,items)=>!base.pinned?.some(pin=>pin.kind===item.kind&&pin.ref?.id===item.ref.id&&pin.ref.version===item.ref.version)&&items.findIndex(other=>other.kind===item.kind&&other.ref.id===item.ref.id&&other.ref.version===item.ref.version)===index);
 const key=JSON.stringify([epoch,workspace,identity,refs,dependencies]);
 const [state,setState]=useState<{key:string;resources:Record<string,MetadataResource>;pinned:NonNullable<GovernanceNodeContext["pinned"]>;errors:unknown[];loading:boolean}>({key:"",resources:{},pinned:[],errors:[],loading:false});
 useEffect(()=>{
  const controller=new AbortController();setState({key,resources:{},pinned:[],errors:[],loading:refs.length+dependencies.length>0});
  void Promise.all([Promise.allSettled(refs.map(async ref=>{
   const response=await request<{object:MetadataResource}>(`/api/v1/governance/metadata/${encodeURIComponent(ref.resource_id)}/observations`,{signal:controller.signal});
   const object=response.object;
   if(!object||object.object_id!==ref.resource_id)throw new Error("来源说明未返回，已保留原运行字段与资源版本");
   // Labels may aid recognition; a newer schema must never replace this run's input contract.
   return refs.filter(item=>item.resource_id===ref.resource_id).every(item=>item.contract_version===object.schema_version)?object:null;
  })),Promise.allSettled(dependencies.map(async item=>{
   const path={model:"models",quality_template:"quality-templates",standard:"standards"}[item.kind];
   const record=await request<GovernanceRecord>(`/api/v1/governance/${path}/${encodeURIComponent(item.ref.id)}/versions/${item.ref.version}`,{signal:controller.signal});
   if(record.id!==item.ref.id||record.version!==item.ref.version||!record.definition)throw new Error("固定版本规则说明不可用，已保留原引用");
   return {...item,definition:record.definition};
  }))]).then(([results,pins])=>{
   if(controller.signal.aborted)return;
   const resources=Object.fromEntries(results.flatMap(result=>result.status==="fulfilled"&&result.value?[[result.value.object_id,result.value]]:[]));
   const pinned=pins.flatMap(result=>result.status==="fulfilled"?[result.value]:[]);
   const errors=[...results,...pins].flatMap(result=>result.status==="rejected"?[result.reason]:[]);
   setState({key,resources,pinned,errors,loading:false});
  });
  return()=>controller.abort();
 },[key]);
 const visible=state.key===key;
 const pinned=useMemo(()=>[...(base.pinned??[]),...(visible?state.pinned:[])],[base.pinned,state.pinned,visible]);
 return {context:{...base,resources:{...base.resources,...(visible?state.resources:{})},pinned},errors:state.key===key?state.errors:[],loading:state.key===key?state.loading:refs.length+dependencies.length>0};
}

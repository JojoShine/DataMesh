import {Table} from "antd";
import type {Field,Resource} from "../api/types";
import type {GovernanceMasterSource,GovernanceRecord,GovernanceRef,MetadataResource} from "../api/governance";
import {governanceFieldLabel} from "../api/governanceFieldLabels";
import {useQuery} from "../hooks/useQuery";
import {Failure} from "./common";

function useContract(resource:Resource|undefined){
 const query=useQuery<MetadataResource>(resource?.resource_id?`/api/v1/governance/metadata/${encodeURIComponent(resource.resource_id)}/contract`:null);
 const fields=query.data?.schema_version===resource?.contract_version?query.data?.fields:undefined;
 return {...query,fields,label:(name:string)=>governanceFieldLabel(fields?.find(field=>field.name===name)??{name})};
}
function MasterMapping({source,record}:{source:GovernanceMasterSource;record:GovernanceRecord}) {
 const contract=useContract(source.resource);const ref=record.definition.master?.model_ref;
 const labels=useQuery<{field_labels:Record<string,string>}>(ref?`/api/v1/governance/models/${ref.id}/versions/${ref.version}/field-labels`:null);
 const label=(name:string)=>governanceFieldLabel({...record.definition.master?.model.fields.find(field=>field.name===name),name,business_name:record.definition.master?.model.fields.find(field=>field.name===name)?.business_name||labels.data?.field_labels?.[name]});
 return <>{contract.error&&<Failure error={contract.error} retry={contract.reload}/ >}{labels.error&&<Failure error={labels.error} retry={labels.reload}/>}<p>{source.id} · 内部编号：{contract.label(source.record_id)}</p>{Object.entries(source.mapping).map(([target,original])=><p key={target}>{label(target)} ← {contract.label(original)}</p>)}</>;
}

export function ScenarioContractLabels({input,reference}:{input:{task_id?:string;node_id:string;fields:Field[]};reference?:{kind:"dataflow"|"workflow";reference:GovernanceRef}}){
 const parent=useQuery<GovernanceRecord>(reference?`/api/v1/governance/${reference.kind==="workflow"?"workflows":"dataflows"}/${reference.reference.id}/versions/${reference.reference.version}`:null);
 const task=parent.data?.definition.workflow?.tasks.find(task=>task.id===input.task_id);
 const child=useQuery<GovernanceRecord>(task?.dataflow?`/api/v1/governance/dataflows/${task.dataflow.id}/versions/${task.dataflow.version}`:null);
 const source=(reference?.kind==="workflow"?child.data:parent.data)?.definition.dataflow?.nodes.find(node=>node.id===input.node_id)?.source;
 const contract=useContract(source?.resource);
 return <>{parent.error&&<Failure error={parent.error} retry={parent.reload}/ >}{child.error&&<Failure error={child.error} retry={child.reload}/ >}{contract.error&&<Failure error={contract.error} retry={contract.reload}/ >}{input.fields.map(field=>`${governanceFieldLabel(contract.fields?.find(actual=>actual.name===field.name)??field)} · ${field.logical_type}`).join("、")}</>;
}

export function CapabilityDefinition({record}:{record:GovernanceRecord}) {
 const metric=record.definition.metric;const contract=useContract(metric?.source.resource);
 if(record.definition.master)return <Table rowKey="id" dataSource={record.definition.master.sources} pagination={false} columns={[{title:"来源与字段映射",render:(_,source)=><MasterMapping source={source} record={record}/>}]} />;
 if(metric)return <>{contract.error&&<Failure error={contract.error} retry={contract.reload}/ >}<Table rowKey="target" dataSource={metric.aggregates} pagination={false} columns={[{title:"结果字段",render:(_,aggregate)=>`${({count:"数量",sum:"合计",min:"最小值",max:"最大值"})[aggregate.operation]} · ${contract.label(aggregate.source)} → ${aggregate.target}`},{title:"来源字段",render:(_,aggregate)=>contract.label(aggregate.source)},{title:"计算方式",render:(_,aggregate)=>({count:"非空计数",sum:"求和",min:"最小值",max:"最大值"})[aggregate.operation]}]}/><p className="helper-text">{metric.period==="range"?`${metric.from} 至 ${metric.until}（不含）`:"来源完整范围"}</p></>;
 const scenario=record.definition.scenario;
 return <Table rowKey={row=>`${row.task_id}/${row.node_id}`} dataSource={scenario?.inputs} pagination={false} columns={[{title:"输入槽位",render:(_,row)=>`${row.task_id?row.task_id+" / ":""}${row.node_id}`},{title:"字段契约",render:(_,row)=><ScenarioContractLabels input={row} reference={scenario}/>}]} />;
}

export function CapabilityKeys({record}:{record:GovernanceRecord}) {
 const master=record.definition.master;const metric=record.definition.metric;const ref=master?.model_ref;
 const labels=useQuery<{field_labels:Record<string,string>}>(ref?`/api/v1/governance/models/${ref.id}/versions/${ref.version}/field-labels`:null);
 const contract=useContract(metric?.source.resource);
 if(master)return <>{labels.error&&<Failure error={labels.error} retry={labels.reload}/ >}{master.keys.map(name=>governanceFieldLabel({...master.model.fields.find(field=>field.name===name),name,business_name:master.model.fields.find(field=>field.name===name)?.business_name||labels.data?.field_labels?.[name]})).join("、")}</>;
 if(metric)return <>{contract.error&&<Failure error={contract.error} retry={contract.reload}/ >}{metric.dimensions.map(contract.label).join("、")||"完整范围汇总"}</>;
 return <>{record.definition.scenario?.acceptance}</>;
}

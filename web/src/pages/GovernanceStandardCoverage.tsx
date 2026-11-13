import {GovernanceSourceSelect} from "../components/GovernanceSourceSelect";
import "./GovernanceCapabilities.css";
import {GovernanceGuide} from "../components/GovernanceGuide";
import {App,Alert,Button,Descriptions,Form,Input,Space,Table,Tag} from "antd";
import {useState} from "react";
import type {Page,Resource} from "../api/types";
import type {GovernanceStandardAudit as Audit,GovernanceRef,GovernanceRun,MetadataResource} from "../api/governance";
import {useQuery} from "../hooks/useQuery";
import {useAction} from "../hooks/useAction";
import {usePagination} from "../hooks/usePagination";
import {useGovernanceWorkspace} from "../components/GovernanceWorkspaceContext";
import {errorMessage} from "../api/client";
import {PlatformDialog} from "../components/PlatformDialog";
import {Failure,Title,Status} from "../components/common";
interface CoverageField{name:string;standard:GovernanceRef|null;state:"mapped"|"unmapped"|"unavailable"}
interface Coverage{id:string;version:number;name:string;kind:"model"|"metadata";fields:CoverageField[];covered:number;total:number;resource:Resource|null}

export function GovernanceStandardCoverage(){
 const {message}=App.useApp(),action=useAction();const{navigation:navigate}=useGovernanceWorkspace();const[page,,pagination]=usePagination();const list=useQuery<Page<Coverage>>(`/api/v1/governance/standard-coverage?page=${page}&page_size=${pagination.pageSize}`);
 const[selected,setSelected]=useState<Coverage|null>(null);const[resource,setResource]=useState<Resource>();const[parameters,setParameters]=useState<Record<string,string>>({});const[runID,setRunID]=useState<string|null>(null);
 const reference=selected?.resource??resource;const source=useQuery<MetadataResource>(reference?`/api/v1/governance/metadata/${encodeURIComponent(reference.resource_id)}/contract`:null);
 const run=useQuery<GovernanceRun&{result?:{standard_audit?:Audit}}>(runID?`/api/v1/governance/runs/${runID}`:null);const report=run.data?.result?.standard_audit;

 const bindings=selected?.fields.filter(f=>f.state==="mapped"&&f.standard).map(f=>({field:f.name,standard:f.standard}))??[];
 async function audit(){if(!reference||!bindings.length)return;try{const result=await action.run<GovernanceRun>("/api/v1/governance/standard-audits",{resource:reference,bindings,parameters});setRunID(result.id);message.success("已固定标准版本并安排完整核验")}catch(e){message.error(errorMessage(e))}}
 function open(row:Coverage){setSelected(row);setResource(undefined);setParameters({});setRunID(null)}
 return <><Title title="标准落标" description="查看已发布模型与业务说明中标准的覆盖情况，并对授权来源执行真实约束核验。"/><GovernanceGuide topic="coverage"/><Alert showIcon type="info" title="关联标准与符合标准分别核对" description="覆盖率只计算有效的已发布标准引用，不代表实际数据合格。执行落标核验会固定来源与标准版本，检查完整读取范围；报告保留违规条数及行号，不展示原始字段值。"/>
 {list.error?<Failure error={list.error} retry={list.reload}/>:<Table<Coverage> rowKey="id" loading={list.loading} dataSource={list.data?.items} pagination={{...pagination,total:list.data?.total}} scroll={{x:800}} expandable={{expandedRowRender:row=><Table rowKey="name" dataSource={row.fields} pagination={false} columns={[{title:"字段",dataIndex:"name"},{title:"标准版本",render:(_,field:CoverageField)=>field.standard?`${field.standard.id} · v${field.standard.version}`:"未关联"},{title:"关联状态",render:(_,field:CoverageField)=><Tag>{field.state==="mapped"?"有效关联":field.state==="unavailable"?"标准待确认":"尚未落标"}</Tag>}]}/>}} columns={[{title:"定义",render:(_,row)=><Button type="link" onClick={()=>open(row)}>{row.name}</Button>},{title:"类型",render:(_,row)=>row.kind==="model"?"数据模型":"业务说明"},{title:"版本",dataIndex:"version"},{title:"已落标字段",render:(_,row)=>`${row.covered} / ${row.total}`},{title:"覆盖率",render:(_,row)=>row.total?`${(row.covered/row.total*100).toFixed(1)}%`:"无字段"},{title:"操作",render:(_,row)=><Space size={16} wrap><Button disabled={!row.covered} onClick={()=>open(row)}>核验数据</Button><Button onClick={()=>navigate(row.kind==="model"?`/platform/governance/models?model_id=${encodeURIComponent(row.id)}&version=${row.version}`:`/platform/governance/metadata?resource_id=${encodeURIComponent(row.resource?.resource_id??"")}`)}>维护关联</Button></Space>} ]}/>}
 {selected&&<PlatformDialog className="governance-expansion-form" open title={`${selected.name} · 落标核验`} width={1100} onCancel={()=>setSelected(null)} footer={<Space size={16}>{Boolean(report?.rejected_rows)&&runID&&<Button onClick={()=>navigate(`/platform/governance/issues?action=create&run_id=${encodeURIComponent(runID)}`)}>登记整改</Button>}<Button disabled={!runID} onClick={run.reload}>刷新结果</Button><Button type="primary" disabled={!reference||!bindings.length||Boolean(run.data&&['queued','running'].includes(run.data.state))} loading={action.busy} onClick={()=>void audit()}>完整核验</Button></Space>}><Descriptions items={[{key:"definition",label:"固定定义",children:`v${selected.version}`},{key:"fields",label:"有效标准关联",children:bindings.length}]}/>{!selected.resource&&<><p>选择本次需要核验的来源；其技术字段必须覆盖已落标字段。</p><GovernanceSourceSelect value={resource} onChange={value=>{setResource(value);setParameters({})}}/></>}
 {source.data?.input_parameters?.map(parameter=><Form.Item key={parameter.name} label={`来源参数：${parameter.name}`} required={parameter.required}><Input value={parameters[parameter.name]??""} maxLength={4000} onChange={e=>setParameters({...parameters,[parameter.name]:e.target.value})}/></Form.Item>)}
 {run.error?<Failure error={run.error} retry={run.reload}/>:run.data&&<><p className="detail-section">本次运行：{run.data.id} <Status value={run.data.state}/></p>{run.data.error_code&&<Alert showIcon type="warning" title={run.data.error_code}/>}</>}
 {report&&<><Descriptions className="detail-section" items={[{key:"rows",label:"实际核验行数",children:report.rows},{key:"bad",label:"不符合标准记录",children:report.rejected_rows}]}/><Table rowKey="field" dataSource={report.fields} pagination={false} columns={[{title:"字段",dataIndex:"field"},{title:"固定标准",render:(_,row)=>`${row.name} · v${row.standard.version}`},{title:"核验状态",render:(_,row)=>row.status==="redacted"?"敏感字段无法明文核实":"已核验"},{title:"违规条数",render:(_,row)=>row.status==="redacted"?"无法核实":row.violations},{title:"示例行号（最多20个）",render:(_,row)=>row.ordinals.join("、")||"无"}]}/></>}
 </PlatformDialog>}
 </>;
}

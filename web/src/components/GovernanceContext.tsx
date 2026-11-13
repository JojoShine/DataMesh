import {governanceFieldLabel} from "../api/governanceFieldLabels";
import {GovernanceHelpScope,useGovernanceWorkspace} from "./GovernanceWorkspaceContext";
import {Descriptions,Table,Tag,Typography} from "antd";
import {InfoCircleOutlined} from "@ant-design/icons";
import type {GovernanceKind,GovernanceRecord,GovernanceRef} from "../api/governance";
import {governancePaths,governanceTypes} from "../api/governance";
import {useQuery} from "../hooks/useQuery";
import {Failure,Status} from "./common";
import {GovernanceGuide} from "./GovernanceGuide";
import {topics,type GovernanceJourneyTopic} from "./governanceJourney";
import "./GovernanceJourney.css";

export function GovernancePurpose({purpose,before,after,requirement}:{purpose:string;before:string;after:string;requirement?:string}){
 if(!useGovernanceWorkspace().helpOpen)return null;
 return <div className="governance-purpose"><div><InfoCircleOutlined/><strong>为什么需要</strong><span>{purpose}</span></div><p><b>开始前</b>{before}</p><p><b>完成后</b>{after}</p>{requirement&&<p><b>当前要求</b>{requirement}</p>}</div>
}

export function GovernanceRelationInfo({topic,reference}:{topic:GovernanceJourneyTopic;reference?:{kind:GovernanceKind;ref:GovernanceRef}}){
 const info=topics[topic];
 const record=useQuery<GovernanceRecord>(reference?`/api/v1/governance/${governancePaths[reference.kind]}/${encodeURIComponent(reference.ref.id)}/versions/${reference.ref.version}`:null);
 return <GovernanceHelpScope><Tag color="blue">功能用途与办理关系</Tag><GovernancePurpose purpose={info.purpose} before={info.before} after={info.after} requirement={info.required}/><Descriptions bordered column={1} items={[{key:"when",label:"什么时候用",children:info.when},{key:"relation",label:"与其他功能的关系",children:info.relation}]}/>{(topic==="domain"||topic==="standard"||topic==="term")&&<GovernanceGuide topic={topic}/>} {record.error?<Failure error={record.error}/>:record.data&&<div className="detail-section"><h3>{record.data.definition.name} · v{record.data.version}</h3><Status value={record.data.state}/><p>{record.data.definition.description}</p>{record.data.definition.model&&<Table rowKey="name" pagination={false} scroll={{x:600}} dataSource={record.data.definition.model.fields} columns={[{title:"字段（中文 / 英文）",render:(_,f)=>governanceFieldLabel(f)},{title:"业务含义",dataIndex:"description"},{title:"类型",render:(_,f)=>`${governanceTypes.find(t=>t.value===f.logical_type)?.label??f.logical_type}${f.logical_type==="decimal"?`（${f.precision},${f.scale}）`:""}`},{title:"要求",render:(_,f)=><>{f.primary_key&&<Tag>主键</Tag>}{!f.nullable&&<Tag>非空</Tag>}{f.sensitive&&<Tag>敏感</Tag>}</>} ]}/>}</div>}</GovernanceHelpScope>;
}

export function GovernanceRelationsTable({onTopic}:{onTopic:(topic:GovernanceJourneyTopic)=>void}){
 return <Table className="detail-section" rowKey="key" pagination={false} scroll={{x:1000}} dataSource={(Object.entries(topics) as [GovernanceJourneyTopic,typeof topics[GovernanceJourneyTopic]][]).map(([key,info])=>({key,...info}))} columns={[{title:"功能",width:180,render:(_,r)=><Typography.Link onClick={()=>onTopic(r.key)}>{r.title}</Typography.Link>},{title:"必要性与适用场景",width:280,render:(_,r)=><><p>{r.purpose}</p><span className="helper-text">{r.when}</span></>},{title:"前置条件",dataIndex:"before"},{title:"后续关系",dataIndex:"after"},{title:"必选与可选",dataIndex:"required",width:200}]}/>;
}

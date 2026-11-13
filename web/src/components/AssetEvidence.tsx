import {ResourceSecurityContext} from "./ResourceSecurityContext";
import {Alert,Descriptions,Space,Spin,Table} from "antd";
import type {Resource} from "../api/types";
import {query} from "../api/client";
import {useQuery} from "../hooks/useQuery";
import {Failure,Stamp,Status} from "./common";
interface Evidence {
 name:string;description:string;provider:string;owner:string;update_frequency:string;usage_restrictions:string;
 fields:{name:string;business_name:string;description:string;logical_type:string;sensitive:boolean}[];
 parameters:{name:string;type:string;required:boolean}[];
 delivery_mode:string;generated_at:string|null;archived_at:string|null;registered_at:string|null;data_cutoff:string|null;
 quality_checks:number;output_validated:boolean;output_rows:number|null;rejected_rows:number|null;snapshot_scope:string;
 upstream_state:string;upstream_attempt_at:string|null;notice:string;
}
export function AssetEvidence({resource}:{resource:Resource}){
 const detail=useQuery<Evidence>("/api/v1/assets/resource-detail?"+query({...resource}));const d=detail.data;
 if(detail.error)return <Failure error={detail.error} retry={detail.reload}/>;
 if(!d)return <Spin/>;
 return <Space orientation="vertical" size={24} style={{width:"100%"}}>
 <ResourceSecurityContext resource={resource}/>
 {d.notice&&<Alert type="warning" showIcon title={d.notice}/>}
 <Descriptions title="业务说明" column={1} items={[
 {key:"description",label:"说明",children:d.description||"未提供"},
 {key:"provider",label:"提供单位",children:d.provider||"未提供"},
 {key:"owner",label:"责任人",children:d.owner||"未提供"},
 {key:"usage",label:"使用限制",children:d.usage_restrictions||"以申请审批条件为准"},
 ]}/>
 <section style={{width:"100%"}}><h3>共享字段与口径</h3><Table rowKey="name" dataSource={d.fields} size="small" scroll={{x:650}} pagination={false} locale={{emptyText:"按完整文件交付，未提供字段说明"}} columns={[
 {title:"字段",dataIndex:"name"},{title:"业务名称",render:(_,r)=>r.business_name||"未说明"},{title:"类型",dataIndex:"logical_type"},{title:"含义与口径",render:(_,r)=>r.description||"未说明"},{title:"保护方式",render:(_,r)=>r.sensitive?"按授权脱敏":"按授权访问"},
 ]}/></section>
 <Descriptions title="质量检查证据" column={1} items={[
 {key:"checks",label:"业务检查",children:d.quality_checks?`本次运行配置 ${d.quality_checks} 个检查步骤；结论仅覆盖已配置规则`:"未提供业务质量检查证据"},
 {key:"model",label:"输出格式校验",children:d.output_validated?"已通过绑定模型校验，不代表全部业务质量要求通过":"未提供模型校验证据"},
 {key:"rows",label:"输出记录",children:d.output_rows??"未提供"},
 {key:"rejected",label:"运行隔离记录",children:d.rejected_rows??"未提供"},
 ]}/>
 <Descriptions title="更新与来源证据" column={1} items={[
 {key:"mode",label:"交付依据",children:({latest:"接口使用最新已发布且契约兼容的成果",snapshot:"固定版本成果或文件快照",source_query:"请求时查询已接入的来源"})[d.delivery_mode]??d.delivery_mode},
 {key:"generated",label:"当前成果生成时间",children:d.generated_at?<Stamp value={d.generated_at}/>:"未提供"},
 {key:"cutoff",label:"数据截止时间",children:d.data_cutoff?<Stamp value={d.data_cutoff}/>:"未知；生成时间不能代替源数据截止时间"},
 {key:"frequency",label:"声明更新周期",children:d.update_frequency||"未说明"},
 {key:"upstream",label:"上游最近运行",children:d.upstream_state?<Space wrap><Status value={d.upstream_state}/><Stamp value={d.upstream_attempt_at??""}/></Space>:"暂无可核验记录"},
 {key:"archive",label:"成果文件归档时间",children:d.archived_at?<Stamp value={d.archived_at}/>:"未提供归档证据"},
 ...(d.registered_at?[{key:"registered",label:"文件登记时间",children:<Stamp value={d.registered_at}/>}]:[]),
 ]}/>
 <Alert showIcon title="上游运行与当前共享版本分别记录" description="较新的上游运行不代表当前共享成果已替换；文件归档时间也不代表源数据实时性。实际使用仍以当前有效授权和交付结果为准。"/>
 {resource.resource_type==="api_resource"&&<section style={{width:"100%"}}><h3>接口参数</h3><Table rowKey="name" dataSource={d.parameters} pagination={false} scroll={{x:450}} locale={{emptyText:"此接口无需输入参数"}} columns={[{title:"参数",dataIndex:"name"},{title:"类型",dataIndex:"type"},{title:"必填",render:(_,r)=>r.required?"是":"否"}]}/><p className="helper-text">申请通过后，在“我的资产”查看授权调用入口和令牌办理说明。</p></section>}
 </Space>;
}

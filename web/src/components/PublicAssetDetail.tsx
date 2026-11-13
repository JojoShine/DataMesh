import {Alert,Button,Descriptions,Spin,Table,Tabs} from "antd";
import type {Asset,PublicAssetDetail as Detail} from "../api/types";
import {useQuery} from "../hooks/useQuery";
import {useSession} from "../stores/session";
import {navigate} from "../app/navigation";
import {Failure} from "./common";
import {PlatformDialog} from "./PlatformDialog";
import "./PublicAssetDetail.css";

const classifications:Record<string,string>={base:"基础信息",theme:"主题信息",department:"部门信息"};
const methods:Record<string,string>={query:"查询数据",download:"下载文件",api:"调用API",invoke:"调用API"};
const formats:Record<string,string>={database:"数据库",file:"文件",api:"API"};
export function PublicAssetDetail({asset,onClose,initialTab="intro"}:{asset:Asset;onClose:()=>void;initialTab?:"intro"|"fields"|"sharing"}){
 const user=useSession(s=>s.user);
 const detail=useQuery<Detail>(`/api/public/assets/${encodeURIComponent(asset.dataset_id)}`);
 const data=detail.data,d=data?.details;
 const missing=(value:string|undefined)=>value||"未提供";
 return <PlatformDialog open title={data?.name??asset.name} width={1000} onCancel={onClose} destroyOnHidden footer={<Button type="primary" disabled={Boolean(user?.public_test)||!data||Boolean(detail.error)} onClick={()=>navigate(user?`/platform/assets/catalog?dataset_id=${encodeURIComponent(asset.dataset_id)}`:"/login")}>{user?.public_test?"体验账号仅可浏览":user?"进入目录申请使用":"登录后申请使用"}</Button>}>
  {detail.error?<Failure error={detail.error} retry={detail.reload}/>:!data||!d?<Spin description="正在读取公开资产详情"/>:<div className="public-asset-detail-content">
   <Descriptions size="small" column={{xs:1,sm:2}} items={[{key:"version",label:"资产版本",children:`治理成果 · V${data.contract_version}`},{key:"code",label:"资源编码",children:missing(d.resource_code)},{key:"provider",label:"提供单位",children:missing(d.provider_name)},{key:"category",label:"资产分类",children:classifications[d.classification]??missing(d.classification)},{key:"updated",label:"成果更新时间",children:new Date(data.updated_at).toLocaleString("zh-CN")},{key:"frequency",label:"声明更新频率",children:missing(d.update_frequency)}]}/>
   <Tabs defaultActiveKey={initialTab} items={[
    {key:"intro",label:"资产介绍",children:<div className="public-asset-detail-intro"><section><h3>资产说明</h3><p>{missing(data.description)}</p></section><Descriptions column={1} items={[{key:"format",label:"资源格式",children:formats[d.format]??missing(d.format)},{key:"level",label:"安全分类说明",children:missing(d.security_classification)},{key:"restriction",label:"使用限制",children:missing(data.usage_restrictions)}]}/><Alert showIcon type="info" title="质量与更新说明" description="处理与质量说明以已审核的资产介绍为准；当前未公开独立质量报告。声明更新频率不代表最近运行成功，成果更新时间也不等于源数据截止时间。"/></div>},
    {key:"fields",label:`字段说明（${d.items.length}）`,children:<Table rowKey="name" size="small" dataSource={d.items} pagination={false} scroll={{x:650}} columns={[{title:"字段名称",dataIndex:"name"},{title:"业务名称",render:(_,r)=>missing(r.business_name)},{title:"数据类型",dataIndex:"logical_type"},{title:"允许为空",render:(_,r)=>r.nullable?"是":"否"},{title:"敏感标记",render:(_,r)=>r.sensitive?"是":"否"},{title:"字段说明",render:(_,r)=>missing(r.description)}]}/>},
    {key:"sharing",label:"共享与使用",children:<Descriptions column={1} items={[{key:"type",label:"共享类型",children:({conditional:"有条件共享",unconditional:"无条件共享",not_shared:"不予共享"} as Record<string,string>)[d.sharing_type]??missing(d.sharing_type)},{key:"method",label:"交付方式",children:methods[d.sharing_method]??missing(d.sharing_method)},{key:"conditions",label:"共享条件",children:missing(d.sharing_conditions)},{key:"open",label:"公开条件",children:missing(d.open_conditions)},{key:"legal",label:"使用依据",children:missing(d.legal_basis)},{key:"usage",label:"使用限制",children:missing(data.usage_restrictions)}]}/>},
   ]}/>
   <p className="helper-text">以上为已审核公开的资产目录信息，无需登录即可浏览。申请使用时需登录，实际记录仍按审批授权范围交付。</p>
  </div>}
 </PlatformDialog>;
}

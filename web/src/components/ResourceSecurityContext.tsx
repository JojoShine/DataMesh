import {Alert,Button,Descriptions,Space} from "antd";
import type {Resource} from "../api/types";
import type {SecurityContextData} from "../api/security";
import {securityLevelNames} from "../api/security";
import {useQuery} from "../hooks/useQuery";
import {useSession} from "../stores/session";
import {query} from "../api/client";
import {navigate} from "../app/navigation";
import {Failure} from "./common";
export function ResourceSecurityContext({resource,security=false}:{resource:Resource;security?:boolean}){
 const user=useSession(s=>s.user);const result=useQuery<SecurityContextData>((security?"/api/v1/security/resource-context?":"/api/v1/assets/security-context?")+query({...resource}));const d=result.data;
 if(result.error)return <Failure error={result.error} retry={result.reload}/>;if(!d?.resource)return null;
 return <Space orientation="vertical" size={16} style={{width:"100%"}}><Descriptions title="安全与使用要求" column={1} items={[{key:"level",label:"已复核安全级别",children:securityLevelNames[d.level]??"未分级"},{key:"mask",label:"额外保护字段",children:d.mask_fields?.join("、")||"仍执行原有字段保护"},{key:"mark",label:"交付要求",children:[d.visible?"页面可见标记":"",d.file_mark?"文件内容标记":"",d.require_proof?"签名交付凭据":""].filter(Boolean).join("、")||"按已有授权与审计交付"}]}/>{d.blocked&&<Alert showIcon type="warning" title={d.reason}/ >}{user?.platforms.includes("exchange")&&user.roles.some(r=>r==="provider"||r==="admin")&&<Button onClick={()=>navigate("/platform/exchange/security-labels?"+query({...resource}))}>维护此资源安全标签</Button>}{(user?.roles.includes("admin")||(user?.platforms.includes("security")&&user.roles.includes("auditor")))&&<Button onClick={()=>navigate("/platform/security/overview?"+query({...resource}))}>查看安全工作台</Button>}</Space>;
}
export function DeliveryWatermark({text,deliveryID}:{text:string;deliveryID:string}){return <Space orientation="vertical" size={8} className="delivery-security-watermark" style={{width:"100%"}}>{text&&<span>{text}</span>}{deliveryID&&<small>交付溯源编号：{deliveryID}</small>}</Space>}

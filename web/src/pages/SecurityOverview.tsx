import {Alert,Button,Card,Col,Row,Space,Spin} from "antd";
import {ResourceSecurityContext} from "../components/ResourceSecurityContext";
import {usePath} from "../app/navigation";
import type {Resource} from "../api/types";
import type {SecurityOverviewData} from "../api/security";
import {useQuery} from "../hooks/useQuery";
import {navigate} from "../app/navigation";
import {Failure,Stamp,Title} from "../components/common";
export function securityResourceFromPath(path:string):Resource|null{const q=new URLSearchParams(path.split("?")[1]);const type=q.get("resource_type");const id=q.get("resource_id");const version=Number(q.get("contract_version"));if(!id||!Number.isInteger(version)||version<1)return null;if(type!=="raw_object"&&type!=="governed_dataset"&&type!=="table_resource"&&type!=="api_resource"&&type!=="file_resource")return null;return {resource_type:type,resource_id:id,contract_version:version}}
export function SecurityOverview(){
 const resource=securityResourceFromPath(usePath());
 const result=useQuery<SecurityOverviewData>("/api/v1/security/overview");const d=result.data;
 return <>{resource&&<Card title="当前定位资源" style={{marginBottom:24}}><p>{resource.resource_id} · V{resource.contract_version}</p><ResourceSecurityContext resource={resource} security/></Card>}<Title title="安全总览" description="核对已发布防护、审批待办及实际安全事件，定位共享、治理与资产相关资源。" actions={<Button onClick={result.reload}>刷新</Button>}/>{result.error?<Failure error={result.error} retry={result.reload}/>:!d?<Spin/>:<Space orientation="vertical" size={24} style={{width:"100%"}}><Alert showIcon title={d.scope} description={<>观测时间：<Stamp value={d.checked_at}/></>}/><Row gutter={[24,24]} style={{width:"100%"}}>{[{key:"pending",name:"待复核版本",path:"policies"},{key:"classification",name:"已发布分级",path:"classification"},{key:"policy",name:"已发布策略",path:"policies"},{key:"watermark",name:"已发布水印模板",path:"watermarks"},{key:"open_incidents",name:"未结束安全事件",path:"incidents"},{key:"frozen",name:"冻结资源事件",path:"incidents"},{key:"proofs",name:"交付凭据",path:"proofs"},{key:"expired_grants",name:"已到期授权记录",path:"grants"}].map(item=><Col xs={24} md={12} xl={6} key={item.key}><Card title={item.name}><Space orientation="vertical" size={16}><strong>{d.counts[item.key]??"—"}</strong><Button onClick={()=>navigate(`/platform/security/${item.path}`)}>查看</Button></Space></Card></Col>)}</Row><Alert showIcon type={d.signing_available?"info":"warning"} title={d.signing_available?`签名密钥可用 · ${d.signing_key_id}`:"签名密钥尚未配置"} description="必需签名的防护策略在配置有效签名密钥后才可发布；旧业务继续执行原有授权与审计。"/><Card title="当前水印支持范围"><p>页面可见标记、Excel说明页及打印页眉、PDF逐页内容水印，以及PDF/Excel可提取的隐式溯源标识。API与ETL保留签名交付凭据。</p><p className="helper-text">行级过滤支持原生数据库表查询与ETL、Doris治理成果查询及未映射的数据服务API。其余载体在要求行过滤时阻止交付。隐式标识用于追溯，编辑或重新保存可能移除。</p></Card></Space>}</>;
}

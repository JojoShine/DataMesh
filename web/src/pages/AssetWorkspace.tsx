import {Alert,Button,Card,Col,Row,Space,Spin} from "antd";
import type {Page} from "../api/types";
import {navigate} from "../app/navigation";
import {useQuery} from "../hooks/useQuery";
import {Failure,Title} from "../components/common";
import {AssetFeedbackList} from "../components/AssetFeedback";
import {classificationNames} from "./ResourceDirectory";

function AssetSummary({title,endpoint,path,action,description}:{title:string;endpoint:string;path:string;action:string;description:string}){
 const result=useQuery<Page<unknown>>(`/api/v1/assets/${endpoint}${endpoint.includes("?")?"&":"?"}page_size=1`);
 return <Card title={title} style={{height:"100%"}}>
  <Space orientation="vertical" size={16} style={{width:"100%"}}>
   {result.error?<Failure error={result.error} retry={result.reload}/>:result.loading?<Spin/>:<strong style={{fontSize:18}}>{result.data?.total??"—"} 项</strong>}
   <span>{description}</span>
   <Button onClick={()=>navigate(path)}>{action}</Button>
  </Space>
 </Card>;
}
export function AssetOverview(){
 return <>
  <Title title="资产总览" description="从发现、核验到申请与使用，集中查看当前账号可见的资产和个人工作。"/>
  <Row gutter={[24,24]}>
   <Col xs={24} md={12} xl={6}><AssetSummary title="共享资产目录" endpoint="directories" path="/platform/assets/catalog" action="发现资产" description="当前可见的已共享目录，不等同于资源版本数量。"/></Col>
   <Col xs={24} md={12} xl={6}><AssetSummary title="我的申请" endpoint="applications" path="/platform/assets/applications" action="查看申请" description="本人提交的全部申请，包含审批中和已结束的流程。"/></Col>
   <Col xs={24} md={12} xl={6}><AssetSummary title="我的授权记录" endpoint="grants" path="/platform/assets/grants" action="使用我的资产" description="本人授权记录总数，实际可用性以授权状态和共享条件为准。"/></Col>
   <Col xs={24} md={12} xl={6}><AssetSummary title="我的使用反馈" endpoint="feedback" path="/platform/assets/feedback" action="跟进反馈" description="本人反馈及其处理进展，可查看提供部门的公开回复。"/></Col>
  </Row>
  <Card title="使用前核对" style={{marginTop:24}}>
   <p>先确认业务含义、共享字段与使用限制，再核对质量和更新证据。收藏、关注及查看证据不会授予访问权限。</p>
   <Space size={16} wrap><Button onClick={()=>navigate("/platform/assets/categories")}>按分类查找</Button><Button onClick={()=>navigate("/platform/assets/trust")}>核验可信证据</Button><Button onClick={()=>navigate("/platform/assets/notices")}>查看变化与到期提醒</Button></Space>
  </Card>
 </>;
}
const categoryDescriptions:Record<string,string>={base:"查找纳入基础信息分类的共享目录。",theme:"按现有主题信息分类查找业务相关数据。",department:"查找纳入部门信息分类的共享目录。"};
export function AssetCategories(){
 return <>
  <Title title="分类浏览" description="沿用资源目录的基础信息、主题信息和部门信息分类，进入分类后可继续搜索并查看共享条件。"/>
  <Row gutter={[24,24]}>{Object.entries(classificationNames).map(([key,label])=><Col xs={24} md={8} key={key}><AssetSummary title={label} endpoint={`directories?classification=${key}`} path={`/platform/assets/catalog?classification=${key}`} action={`浏览${label}`} description={categoryDescriptions[key]}/></Col>)}</Row>
  <Alert style={{marginTop:24}} showIcon title="分类来自已发布目录" description="分类只组织查找入口，不改变目录、共享版本或授权范围。"/>
 </>;
}
export function AssetFeedbackWorkspace(){
 return <>
  <Title title="我的反馈" description="集中跟进数据质量、更新时效、字段口径和访问交付问题，查看公开回复与处理历史。" actions={<Button onClick={()=>navigate("/platform/assets/grants")}>从我的资产提交反馈</Button>}/>
  <AssetFeedbackList/>
 </>;
}

import {usePagination} from "../hooks/usePagination";
import {Alert,Button,Pagination,Space,Spin,Steps} from "antd";
import {} from "react";
import type {ResourceDirectory,ResourceMount} from "../api/types";
import {useQuery} from "../hooks/useQuery";
import {Status} from "./common";
import {navigate} from "../app/navigation";

function MountProgress({id}:{id:string}){
 const mount=useQuery<ResourceMount>(`/api/v1/resource-mounts/${encodeURIComponent(id)}`);
 const directory=useQuery<ResourceDirectory>(mount.data?`/api/v1/resource-directories/${encodeURIComponent(mount.data.directory_id)}`:null);
 if(mount.error)return <p className="helper-text">挂载证据暂不可读，需有权人员核对。<Button onClick={mount.reload}>重新读取</Button></p>;
 if(mount.loading)return <Spin/>;
 if(!mount.data)return <p className="helper-text">挂载状态待核对。</p>;
 return <div className="detail-section"><Space size={16} wrap><strong>{directory.data?.definition.name??"关联目录"}</strong><span>目录：{directory.data?<Status value={directory.data.state}/>:directory.error?"待有权人员核对":"正在读取"}</span><span>挂载：<Status value={mount.data.state}/></span><span>当前修订：<Status value={mount.data.revision_state}/></span></Space>{mount.data.active_revision_id&&mount.data.revision_id!==mount.data.active_revision_id&&<p className="helper-text">当前另有草稿修订或待审核版本；既有启用版本与本次修订分别核对。</p>}<p className="helper-text">挂载状态表示目录中的资源状态，使用申请与授权需按具体使用方核对。</p><Button onClick={()=>navigate(`/platform/exchange/directory?view=manage&directory_id=${encodeURIComponent(mount.data!.directory_id)}`)}>查看目录与挂载</Button></div>;
}
export function GovernanceSharingProgress({published,canExchange,mountIDs,evidenceError=false,loading=false}:{published:boolean;canExchange:boolean;mountIDs?:string[];evidenceError?:boolean;loading?:boolean}){
 const [page,setPage,pagePagination]=usePagination();const safePage=Math.min(page,Math.max(1,Math.ceil((mountIDs?.length??0)/pagePagination.pageSize)));
 return <><Steps orientation="vertical" current={published?1:0} items={[{title:"成果核验发布",content:published?"已发布；使用时仍须满足结果有效期与当前授权。":"待核验发布"},{title:"目录关联、挂载审核与启用",content:loading?<Spin/>:evidenceError||!mountIDs?<span>挂载证据待核对；不能据此认定已共享。</span>:!mountIDs.length?"尚未关联目录":canExchange?<>{mountIDs.slice((safePage-1)*pagePagination.pageSize,safePage*pagePagination.pageSize).map(id=><MountProgress key={id} id={id}/>)}{mountIDs.length>0&&<Pagination {...pagePagination} size="small" total={mountIDs.length} current={safePage}/>}</>:"已登记挂载关系，具体状态需由有共享交换权限的人员核对。"},{title:"使用申请、审批与授权",content:"按具体使用方和用途办理；挂载启用不等于任何人都已获准使用。授权状态需在共享交换核对。"}]}/>{!canExchange&&<Alert type="info" showIcon title="请交由目录维护或提供方人员继续办理共享。"/>}</>;
}

import {applicationURL} from "../app/basePath";
import {useEffect,useState} from "react";
import {App,Button,Descriptions,Typography} from "antd";
import {useAction} from "../hooks/useAction";
import {useQuery} from "../hooks/useQuery";
import {errorMessage} from "../api/client";
import {GovernanceDialogActions} from "./GovernanceDialog";
import {Failure,Stamp} from "./common";
interface ResultArchive{id:string;state:"queued"|"running"|"ready"|"failed";error_code?:string;manifest:{rows?:number;sha256?:string;created_at?:string}}
export function GovernanceResultArchive({runID}:{runID:string}){
 const action=useAction();const {message}=App.useApp();const [id,setID]=useState<string>();const result=useQuery<ResultArchive>(id?`/api/v1/governance/exports/${id}`:null,true);
 useEffect(()=>{if(!id||result.data?.state==="ready"||result.data?.state==="failed")return;const timer=window.setInterval(result.reload,2000);return()=>window.clearInterval(timer)},[id,result.data?.state]);
 async function create(){try{const next=await action.run<ResultArchive>(`/api/v1/governance/runs/${runID}/archive`,{});setID(next.id)}catch(e){message.error(errorMessage(e))}}
 return <><GovernanceDialogActions><Button disabled={action.busy||action.readOnly} onClick={create}>保存 RustFS 快照</Button>{result.data?.state==="ready"&&<Button href={applicationURL(`/api/v1/governance/exports/${id}/download`)}>下载成果快照</Button>}</GovernanceDialogActions>{Boolean(result.error)&&<Failure error={result.error}/>} {result.data&&<Descriptions title="RustFS 版本快照" items={[{key:"state",label:"状态",children:{queued:"等待存档",running:"正在写入并校验",ready:"已保存并校验",failed:"存档失败"}[result.data.state]},{key:"rows",label:"行数",children:result.data.manifest.rows??"—"},{key:"time",label:"快照时间",children:<Stamp value={result.data.manifest.created_at}/>},{key:"hash",label:"内容校验值",children:<Typography.Text copyable>{result.data.manifest.sha256??result.data.error_code??"生成后显示"}</Typography.Text>} ]}/>}</>
}

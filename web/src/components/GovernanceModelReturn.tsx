import {Alert,Button} from "antd";
import type {GovernanceFlowRun,GovernanceRecord} from "../api/governance";
import {navigate} from "../app/navigation";
import {useQuery} from "../hooks/useQuery";
import {Failure} from "./common";

export function GovernanceModelReturn({runID,record,command=false}:{runID:string;record:GovernanceRecord;command?:boolean}){
 const source=useQuery<GovernanceFlowRun>(`/api/v1/governance/runs/${encodeURIComponent(runID)}`);
 const run=source.data;
 const origin=run?.result?.generated_model??(!run?.result?.unmodeled?run?.result?.model:undefined);
 const eligible=run?.kind==="dataflow"&&run.state==="succeeded"&&origin?.id===record.id&&run.workspace_id===record.workspace_id;
 if(command)return eligible&&record.state==="published"?<Button onClick={()=>navigate(`/platform/governance/dataflows?definition_id=${encodeURIComponent(run.definition.flow.id)}&version=${run.definition.flow.version}&from_run=${encodeURIComponent(run.id)}&bind_model=${encodeURIComponent(record.id)}&model_version=${record.version}&workspace_id=${encodeURIComponent(run.workspace_id)}`)}>返回数据流并绑定此版本</Button>:null;
 if(source.error)return <Failure error={source.error} retry={source.reload}/>;
 return eligible?<Alert className="detail-section" type="info" showIcon title={`来源运行 ${run.id} · 数据流 v${run.definition.flow.version}`} description={record.state==="published"?"返回后以数据流最新版本创建待复核修改；确认保存后才能运行。":"确认模型并发布后，可返回来源数据流绑定此版本。"}/>:null;
}

import {usePagination} from "../hooks/usePagination";
import {AssetFeedbackList} from "../components/AssetFeedback";
import {Alert,Button,Empty,Input,Select,Space,Table,Tabs} from "antd";
import {useState} from "react";
import type {Page} from "../api/types";
import type {GovernanceFlowRun} from "../api/governance";
import {governanceRunCodes} from "../api/governance";
import {governanceQualityFlow,governanceQualityNodes} from "../api/governanceQuality";
import {navigate,usePath} from "../app/navigation";
import {Failure,Stamp,Status,Title} from "../components/common";
import {GovernanceDialog} from "../components/GovernanceDialog";
import {useQuery} from "../hooks/useQuery";
import {GovernanceCatalog} from "./GovernanceCatalog";
import {GovernanceFlowRunView} from "./GovernanceDataflows";
import {GovernanceQualityIssues} from "./GovernanceQualityIssues";
import {GovernanceQualityTrends} from "./GovernanceQualityTrends";

function QualityRecords(){
 const path=usePath();const params=new URLSearchParams(path.split("?")[1]);const requestedRun=params.get("run_id");
 const [page,setPage,pagePagination]=usePagination();const [state,setState]=useState("");const [flow,setFlow]=useState("");const [selected,setSelected]=useState<string|null>(null);const [dismissed,setDismissed]=useState<string|null>(null);
 const runID=selected??(requestedRun!==dismissed?requestedRun:null);
 const list=useQuery<Page<GovernanceFlowRun>>(`/api/v1/governance/runs?kind=dataflow&page=${page}&page_size=${pagePagination.pageSize}&state=${state}&definition_id=${encodeURIComponent(flow)}`,true);
 return <><Title title="质量管理" description="在数据流中设置检查要求，运行后查看结果；发现问题后整改复查，通过核验的成果继续发布。" actions={<Space size={16} wrap><Button onClick={list.reload}>刷新</Button><Button type="primary" onClick={()=>navigate("/platform/governance/dataflows?quality_setup=true")}>配置检查与治理</Button></Space>}/><p className="helper-text">选择数据 → 设置检查和清洗步骤 → 运行 → 查看问题 → 整改复查 → 成果核验。单个数据流可直接完成。</p><Space size={16} wrap className="governance-filter-bar"><Select aria-label="检查记录运行状态" placeholder="全部运行状态" value={state} onChange={value=>{setState(value);setPage(1)}} options={[{value:"",label:"全部运行状态"},{value:"succeeded",label:"运行完成"},{value:"failed",label:"运行失败"},{value:"running",label:"运行中"},{value:"queued",label:"排队中"},{value:"cancelled",label:"已取消"},{value:"interrupted",label:"已中断"}]}/><Input.Search aria-label="按数据流编号筛选检查记录" placeholder="按数据流编号筛选，可留空" allowClear onSearch={value=>{setFlow(value);setPage(1)}}/></Space><p className="helper-text">以下按实际数据流运行列出，包含未配置检查的记录。运行完成不代表质量通过，打开详情后读取有权查看的检查证据。</p>{list.error?<Failure error={list.error} retry={list.reload}/>:<Table<GovernanceFlowRun> rowKey="id" dataSource={list.data?.items} loading={list.loading} scroll={{x:900}} locale={{emptyText:<Empty description={state||flow?"没有匹配的检查记录":"还没有数据流检查记录"}><Button onClick={()=>navigate("/platform/governance/dataflows?quality_setup=true")}>前往数据流配置检查</Button></Empty>}} pagination={{...pagePagination,total:list.data?.total}} columns={[{title:"数据流 / 固定版本",render:(_,row)=><><Button type="link" onClick={()=>setSelected(row.id)}>{governanceQualityFlow(row)?.name??row.definition.flow.id} · v{row.definition.flow.version}</Button><div className="helper-text">运行 {row.id}</div></>},{title:"运行状态",render:(_,row)=><Status value={row.state}/>},{title:"检查配置",render:(_,row)=>governanceQualityNodes(row).length?`${governanceQualityNodes(row).length} 个检查步骤 · 结论见详情`:"未配置检查 · 可回数据流补充"},{title:"诊断",render:(_,row)=>(governanceRunCodes[row.error_code]??row.error_code)||"—"},{title:"运行时间",render:(_,row)=><Stamp value={row.created_at}/>},{title:"下一步",render:(_,row)=><Button onClick={()=>setSelected(row.id)}>查看检查与处理</Button>}]}/>}<details className="detail-section"><summary>历史成果办理</summary><p className="helper-text">已在外部处理完成的历史成果，可沿用来源规则、实际结果绑定及完整性核验；新治理从数据流配置检查。</p><Space size={16} wrap><Button onClick={()=>navigate("/platform/governance/rules")}>维护历史来源规则</Button><Button onClick={()=>navigate("/platform/governance/results")}>前往历史成果核验</Button></Space></details>{runID&&<GovernanceDialog header="检查与治理记录" closeLabel="返回检查记录" onClose={()=>{setSelected(null);setDismissed(requestedRun);list.reload()}} width={1200}><GovernanceFlowRunView key={runID} id={runID} initialTab="summary"/></GovernanceDialog>}</>;
}

export function GovernanceQualityWorkspace(){
 const path=usePath();const page=path.split("?")[0].split("/").at(-1);
 const active=new URLSearchParams(path.split("?")[1]).get("tab")==="feedback"?"feedback":page==="quality-templates"?"plans":page==="issues"?"issues":page==="quality-trends"?"trends":"records";
 const routes:Record<string,string>= {feedback:"quality?tab=feedback",records:"quality",plans:"quality-templates",issues:"issues",trends:"quality-trends"};
 return <><Tabs aria-label="质量管理" activeKey={active} onChange={key=>navigate(`/platform/governance/${routes[key]}`)} items={[{key:"records",label:"检查记录"},{key:"plans",label:"检查方案"},{key:"issues",label:"问题整改"},{key:"feedback",label:"使用反馈"},{key:"trends",label:"质量趋势"}]}/>{active==="feedback"?<><Title title="使用反馈" description="跟进共享资产的质量、更新、口径和访问问题。"/><AssetFeedbackList governance/></>:active==="records"?<QualityRecords/>:active==="plans"?<><Alert showIcon type="info" title="检查方案用于复用，发布后到数据流中使用" description="单次检查可直接在数据流填写要求，并从该步骤保存为方案，自动带入字段。保存或发布方案不会自动执行检查。"/><div className="detail-section"><GovernanceCatalog kind="quality_template"/></div></>:active==="issues"?<GovernanceQualityIssues/>:<GovernanceQualityTrends/>}</>;
}

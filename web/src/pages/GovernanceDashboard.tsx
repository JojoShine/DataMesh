import {GovernanceMasterRunView} from "./GovernanceCapabilities";
import {Button,Empty,Select,Skeleton,Space,Table,Tag,Typography} from "antd";
import {useEffect,useRef,useState} from "react";
import type {ReactNode} from "react";
import type {GovernanceRun} from "../api/governance";
import type {GovernanceDashboardReport,GovernanceDashboardState,GovernanceOperationsReport} from "../api/governanceOperations";
import {useQuery} from "../hooks/useQuery";
import {navigate} from "../app/navigation";
import {Failure,Stamp,Status,Title} from "../components/common";
import {useGovernanceWorkspace} from "../components/GovernanceWorkspaceContext";
import {GovernanceGuide} from "../components/GovernanceGuide";
import {GovernanceDialog} from "../components/GovernanceDialog";
import {GovernancePurpose,GovernanceRelationInfo,GovernanceRelationsTable} from "../components/GovernanceContext";
import {GovernanceLearningDialog,useGovernanceLearning} from "../components/GovernanceLearning";
import {topics,journeyRoutes,type GovernanceJourneyTopic} from "../components/governanceJourney";
import {GovernanceFlowRunView} from "./GovernanceDataflows";
import {GovernanceWorkflowRunView} from "./GovernanceWorkflows";
import {ProfileResult} from "./GovernanceProfiles";
import "./GovernanceDashboard.css";

const colors={success:"#239973",failure:"#D9534F",primary:"#1677FF",other:"#91A7C5",warning:"#D28B24"};
const number=(value:number)=>value.toLocaleString("zh-CN");
const total=(items:readonly {count:number}[])=>items.reduce((sum,item)=>sum+item.count,0);
const stateCount=(items:readonly GovernanceDashboardState[],state:string)=>total(items.filter(item=>item.state===state));
const definitionCount=(data:GovernanceDashboardReport,kind:string)=>total(data.definitions.filter(item=>item.kind===kind));
const routes={monitoring:"monitoring",coverage:"standard-coverage",master:"masters",metrics:"metrics",scenarios:"scenarios",releases:"releases",sources:"space-sources",lightweight:"lightweight",metadata:"metadata",domain:"domains",standard:"standards",term:"terms",model:"models",dataflow:"dataflows",workflow:"workflows",schedule:"schedules",results:"results",rules:"rules",template:"quality-templates",quality:"quality",trends:"quality-trends",issues:"issues",lineage:"lineage",operations:"operations",alerts:"alerts",lifecycle:"lifecycle"};
type Route=keyof typeof routes;
function go(route:Route){navigate(`/platform/governance/${routes[route]}`)}

function daysInReport(data:GovernanceDashboardReport){
 const parts=new Intl.DateTimeFormat("en",{timeZone:data.timezone,year:"numeric",month:"2-digit",day:"2-digit"}).formatToParts(new Date(data.started_at));
 const part=(type:string)=>Number(parts.find(item=>item.type===type)?.value);
 return Array.from({length:data.days},(_,i)=>new Date(Date.UTC(part("year"),part("month")-1,part("day")+i)).toISOString().slice(0,10));
}
function ChartPanel({title,note,route,children}:{title:string;note:string;route:Route;children:ReactNode}){
 return <section className="governance-dashboard-panel"><div className="governance-dashboard-heading"><h3>{title}</h3><Typography.Link onClick={()=>go(route)}>查看明细</Typography.Link></div><p className="helper-text">{note}</p><div className="governance-chart-scroll">{children}</div></section>;
}
function Legend({items}:{items:{label:string;color:string}[]}){
 return <div className="governance-chart-legend">{items.map(item=><span key={item.label}><i style={{background:item.color}}/>{item.label}</span>)}</div>;
}
function useChartWidth(){
 const ref=useRef<SVGSVGElement>(null);const [width,setWidth]=useState(720);
 useEffect(()=>{
  const element=ref.current;if(!element)return;
  const resize=()=>setWidth(Math.max(720,Math.round(element.clientWidth)));
  resize();const observer=new ResizeObserver(resize);observer.observe(element);return()=>observer.disconnect();
 },[]);
 return {ref,width};
}
function RunTrend({data}:{data:GovernanceDashboardReport}){
 const {ref,width}=useChartWidth();
 const rows=daysInReport(data).map(day=>data.run_trend.find(row=>row.day===day)??{day,succeeded:0,failed:0,other:0});
 const max=Math.max(2,Math.ceil(Math.max(0,...rows.map(row=>row.succeeded+row.failed+row.other))/2)*2);const height=220,left=46,bottom=182,plotWidth=width-60,plotHeight=160;
 const step=plotWidth/rows.length;const barWidth=Math.max(2,step*0.62);
 return <><Legend items={[{label:"成功",color:colors.success},{label:"失败 / 中断",color:colors.failure},{label:"排队 / 运行 / 取消",color:colors.other}]}/><svg ref={ref} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={`最近${data.days}天治理运行趋势，按创建日期统计执行次数`} className="governance-dashboard-chart">
 {[0,0.5,1].map(ratio=><g key={ratio}><line className="chart-grid" x1={left} x2={left+plotWidth} y1={bottom-plotHeight*ratio} y2={bottom-plotHeight*ratio}/><text x={left-8} y={bottom-plotHeight*ratio+5} textAnchor="end">{number(Math.ceil(max*ratio))}</text></g>)}
 {rows.map((row,index)=>{const x=left+step*index+(step-barWidth)/2;let y=bottom;return <g key={row.day}><title>{row.day}：成功{row.succeeded}，失败或中断{row.failed}，其他{row.other}</title>{([{key:"succeeded",color:colors.success},{key:"failed",color:colors.failure},{key:"other",color:colors.other}] as const).map(series=>{const barHeight=row[series.key]/max*plotHeight;y-=barHeight;return <rect key={series.key} x={x} y={y} width={barWidth} height={barHeight} fill={series.color}/>})}{(index%Math.ceil(rows.length/6)===0||index===rows.length-1)&&<text x={left+step*(index+0.5)} y={bottom+24} textAnchor="middle">{row.day.slice(5)}</text>}</g>})}
 </svg>{!data.runs.length&&<p className="governance-chart-empty">本期暂无运行记录</p>}</>;
}
function QualityTrend({data}:{data:GovernanceDashboardReport}){
 const {ref,width}=useChartWidth();
 const rows=daysInReport(data).map(day=>data.quality_trend.find(row=>row.day===day)??{day,checked:0,passed:0,rejected:0,runs:0});
 const max=Math.max(2,Math.ceil(Math.max(0,...rows.map(row=>row.checked))/2)*2);const left=58,bottom=182,plotWidth=width-76,plotHeight=160;const x=(i:number)=>left+i/Math.max(1,rows.length-1)*plotWidth;const y=(v:number)=>bottom-v/max*plotHeight;
 return <><Legend items={[{label:"检查行次",color:colors.primary},{label:"合格行次",color:colors.success},{label:"隔离行次",color:colors.failure}]}/><svg ref={ref} viewBox={`0 0 ${width} 220`} role="img" aria-label={`最近${data.days}天质量检查、合格和隔离行次趋势`} className="governance-dashboard-chart">
 {[0,0.5,1].map(ratio=><g key={ratio}><line className="chart-grid" x1={left} x2={left+plotWidth} y1={bottom-plotHeight*ratio} y2={bottom-plotHeight*ratio}/><text x={left-8} y={bottom-plotHeight*ratio+5} textAnchor="end">{number(Math.ceil(max*ratio))}</text></g>)}
 {([{key:"checked",color:colors.primary},{key:"passed",color:colors.success},{key:"rejected",color:colors.failure}] as const).map(series=><polyline key={series.key} points={rows.map((row,i)=>`${x(i)},${y(row[series.key])}`).join(" ")} fill="none" stroke={series.color} strokeWidth="2.5" strokeDasharray={series.key==="checked"?"5 4":undefined}/>)}
 {rows.map((row,i)=><g key={row.day}><title>{row.day}：检查{number(row.checked)}，合格{number(row.passed)}，隔离{number(row.rejected)}行次</title>{row.checked>0&&<circle cx={x(i)} cy={y(row.checked)} r="3" fill={colors.primary}/>} {(i%Math.ceil(rows.length/6)===0||i===rows.length-1)&&<text x={x(i)} y={bottom+24} textAnchor="middle">{row.day.slice(5)}</text>}</g>)}
 </svg>{!data.quality_trend.length&&<p className="governance-chart-empty">本期暂无质量节点检查记录</p>}</>;
}
function RunStates({data}:{data:GovernanceDashboardReport}){
 const items=[{label:"成功",count:stateCount(data.runs,"succeeded"),color:colors.success},{label:"失败 / 中断",count:stateCount(data.runs,"failed")+stateCount(data.runs,"interrupted"),color:colors.failure},{label:"排队 / 运行中",count:stateCount(data.runs,"queued")+stateCount(data.runs,"running"),color:colors.primary},{label:"已取消",count:stateCount(data.runs,"cancelled"),color:colors.other}];
 const sum=total(data.runs),length=2*Math.PI*62;let offset=0;
 return <div className="governance-donut-layout"><svg viewBox="0 0 180 180" role="img" aria-label={`本期共${sum}次运行，各状态次数见右侧图例`}><circle cx="90" cy="90" r="62" fill="none" stroke="#EDF2F8" strokeWidth="18"/>{items.filter(item=>item.count>0).map(item=>{const start=offset;offset+=item.count;return <circle key={item.label} cx="90" cy="90" r="62" fill="none" stroke={item.color} strokeWidth="18" strokeDasharray={`${item.count/sum*length} ${length}`} strokeDashoffset={-start/sum*length} transform="rotate(-90 90 90)"><title>{item.label}：{number(item.count)}次</title></circle>})}<text className="chart-total" x="90" y="87" textAnchor="middle">{number(sum)}</text><text x="90" y="111" textAnchor="middle">运行次数</text></svg><dl>{items.map(item=><div key={item.label}><dt><i style={{background:item.color}}/>{item.label}</dt><dd>{number(item.count)}</dd></div>)}</dl></div>;
}
function OutputStates({data}:{data:GovernanceDashboardReport}){
 const count=total(data.outputs);const items=[{label:"已发布",count:stateCount(data.outputs,"published"),color:colors.success},{label:"草稿",count:stateCount(data.outputs,"draft"),color:colors.primary},{label:"已下架",count:total(data.outputs.filter(row=>row.state!=="draft"&&row.state!=="published")),color:colors.other}];
 return <div className="governance-output-bars">{items.map(item=><div key={item.label}><div><span>{item.label}</span><strong>{number(item.count)}<small> / {number(count)}</small></strong></div><div className="governance-meter" role="meter" aria-label={item.label} aria-valuenow={item.count} aria-valuemin={0} aria-valuemax={Math.max(1,count)}><span style={{width:`${count?item.count/count*100:0}%`,background:item.color}}/></div></div>)}<p className="helper-text">状态为已登记事实。当前能否发布或交付，仍以成果核验的实时条件为准。</p></div>;
}
function ModuleMetric({label,value,note,route}:{label:string;value:number|null;note:string;route:Route}){
 return <div className="governance-module-metric"><div><Typography.Link onClick={()=>go(route)}>{label}</Typography.Link><span>{note}</span></div><strong>{value===null?"—":number(value)}</strong></div>;
}
function Modules({data}:{data:GovernanceDashboardReport}){
 const def=(kind:string)=>definitionCount(data,kind);const published=(kind:string)=>total(data.definitions.filter(row=>row.kind===kind&&row.state==="published"));
 const open=total(data.issues.filter(row=>row.state!=="closed"));const light=data.lightweight;
 const modes=(kind:string)=>total((data.flow_modes??[]).filter(row=>row.kind===kind));
 const targets=(kind:string)=>total((data.schedule_targets??[]).filter(row=>row.kind===kind));
 const caps=data.capabilities;
 const groups:{title:string;note:string;items:{label:string;value:number|null;note:string;route:Route}[]}[]=[
  {title:"准备数据",note:"确认来源、认识字段，按需约定结果结构。",items:[
   {label:"来源数据",value:total(data.resources.filter(row=>["table_resource","file_resource","api_resource"].includes(row.kind))),note:"当前范围已登记的库表、文件与 API；可用性和加工授权进入后核对",route:"sources"},
   {label:"元数据管理",value:total(data.resources),note:`可见资源 · ${data.observations}份结构快照 · ${data.metadata_pending??0}项变化待核查`,route:"metadata"},
   {label:"数据建模",value:def("model"),note:`模型对象 · ${published("model")}份最新版本已发布 · 建表成功${stateCount(data.model_deployments,"succeeded")}次`,route:"model"}]},
  {title:"处理数据",note:"单个数据流可直接运行，工作流和调度按需使用。",items:[
   {label:"轻量治理",value:light?.units??null,note:light?`仅本人 · 跨空间 · ${light.files}份 CSV；近${data.days}天运行${total(light.runs)}次，成功${stateCount(light.runs,"succeeded")}次；检查${number(light.checked)}行次，隔离${number(light.rejected)}行次`:"仅本人 · 跨空间 · 暂未取得统计",route:"lightweight"},
   {label:"数据流设计",value:def("dataflow"),note:`数据流对象 · ${published("dataflow")}份最新版本已发布${data.flow_modes?`；批处理${modes("batch")}份，持续更新${modes("continuous")}份`:""}${data.streams?`；持续任务运行中${stateCount(data.streams,"running")}个、失败${stateCount(data.streams,"failed")}个`:""}`,route:"dataflow"},
   {label:"工作流编排",value:def("workflow"),note:`工作流对象 · ${published("workflow")}份最新版本已发布`,route:"workflow"},
   {label:"调度计划",value:total(data.schedules),note:`计划数 · 启用${stateCount(data.schedules,"active")} · 暂停${stateCount(data.schedules,"paused")}${data.schedule_targets?`；数据流${targets("dataflow")}项，工作流${targets("workflow")}项`:""}`,route:"schedule"}]},
  {title:"检查与交付",note:"检查记录、复用方案和整改均归入质量管理。",items:[
   {label:"质量管理",value:data.quality_runs??null,note:`近${data.days}天实际产生质量检查的运行次数；不等于通过成果数`,route:"quality"},
   {label:"检查方案",value:def("quality_template"),note:`复用方案 · ${published("quality_template")}份最新版本已发布；保存方案不等于执行检查`,route:"template"},
   {label:"问题整改",value:open,note:`未关闭问题 · 逾期${data.overdue_issues}项`,route:"issues"},
   {label:"成果核验",value:total(data.outputs),note:`登记成果 · 已发布${stateCount(data.outputs,"published")}份 · 历史质量通过${total(data.outputs.filter(row=>row.quality==="passed"))}份`,route:"results"},
   {label:"成果生命周期",value:data.lifecycle_outputs,note:`自动产出成果 · 保留策略${data.retention_policies}项 · 已清理快照${data.purged_snapshots}份 · 进行中迁移${stateCount(data.migrations,"open")}项`,route:"lifecycle"}]},
  {title:"治理业务闭环",note:"按当前权限与空间统计，历史核验违规按行次累计，不代表当前未整改记录数。",items:[
 {label:"持续监测",value:caps?.monitors??null,note:`未关闭观察异常${caps?.open_observations??0}项`,route:"monitoring"},
 {label:"标准落标",value:caps?.audits??null,note:`成功核验${caps?.audits??0}次，历史违规${caps?.audit_violations??0}行次；覆盖明细进入后查看`,route:"coverage"},
 {label:"主数据管理",value:def("master"),note:`已确认${caps?.confirmed_entities??0}个实体，待确认冲突候选${caps?.master_conflicts??0}个`,route:"master"},
 {label:"指标口径",value:caps?.metrics??null,note:"含草稿及已发布口径；计算结果进入成果核验",route:"metrics"},
 {label:"场景方案",value:caps?.scenarios??null,note:"固定处理版本，实例重新绑定授权来源",route:"scenarios"},
 {label:"变更发布",value:caps?.pending_releases??null,note:"等待独立审核的变更申请",route:"releases"}]},
 {title:"标准与规范",note:"按权限可见，跨空间复用。",items:[
   {label:"主题域与责任",value:def("domain"),note:`主题域对象 · ${published("domain")}份最新版本已发布`,route:"domain"},
   {label:"业务术语",value:def("term"),note:`术语对象 · ${published("term")}份最新版本已发布`,route:"term"},
   {label:"数据标准",value:def("standard"),note:`标准对象 · ${published("standard")}份最新版本已发布`,route:"standard"}]},
  {title:"监控与追溯",note:"运行次数、通知记录和血缘依据分别统计。",items:[
   {label:"运行运维",value:total(data.runs),note:`近${data.days}天空间剖析、数据流及工作流执行次数；不含代码试运行和私人轻量治理`,route:"operations"},
   {label:"治理告警",value:total(data.alerts),note:`通知记录 · 告警规则${data.alert_rules}项 · 发送失败${stateCount(data.alerts,"failed")}条`,route:"alerts"},
   {label:"血缘与影响",value:data.lineage_runs,note:"累计成功数据流运行次数，保留血缘依据；不是数据表数量",route:"lineage"}]},
 ];
 return <div className="governance-modules-grid">{groups.map(group=><section className="governance-dashboard-panel" key={group.title}><h3>{group.title}</h3><p className="helper-text">{group.note}</p>{group.items.map(item=><ModuleMetric key={item.label} {...item}/>)}</section>)}</div>;
}

function DashboardRunDetail({id}:{id:string}){
 const detail=useQuery<GovernanceRun>(`/api/v1/governance/runs/${encodeURIComponent(id)}`);
 if(detail.error)return <Failure error={detail.error} retry={detail.reload}/>;
 if(!detail.data)return <Skeleton active/>;
 const run=detail.data;
 return (run.kind==="dataflow"||run.kind==="dataflow_preview")?<GovernanceFlowRunView id={id}/>:run.kind==="workflow"?<GovernanceWorkflowRunView id={id}/>:run.kind==="master"?<GovernanceMasterRunView id={id}/>:<><Space size={16}><Status value={run.state}/><span>已处理 {number(run.processed)} 行</span></Space><ProfileResult run={run}/></>;
}

export function GovernanceDashboard(){
 const {workspace,workspaceID,helpOpen}=useGovernanceWorkspace();
 const learning=useGovernanceLearning("home",true);const [days,setDays]=useState(30);const zone=Intl.DateTimeFormat().resolvedOptions().timeZone||"UTC";
 const report=useQuery<GovernanceDashboardReport>(`/api/v1/governance/dashboard?days=${days}&timezone=${encodeURIComponent(zone)}`);
 const recent=useQuery<GovernanceOperationsReport>("/api/v1/governance/operations");
 const [info,setInfo]=useState<GovernanceJourneyTopic|null>(null);const [panel,setPanel]=useState<"relations"|"metrics"|null>(null);
 const [runId,setRunId]=useState<string|null>(null);
 const data=report.data;const runs=data?total(data.runs):0;const succeeded=data?stateCount(data.runs,"succeeded"):0;const failed=data?stateCount(data.runs,"failed")+stateCount(data.runs,"interrupted"):0;
 const checked=data?.quality_trend.reduce((sum,row)=>sum+row.checked,0)??0;const passed=data?.quality_trend.reduce((sum,row)=>sum+row.passed,0)??0;
 const openIssues=data?total(data.issues.filter(row=>row.state!=="closed")):0;
 const metrics=data?[
  {label:"可见资源",value:number(total(data.resources)),note:"按当前权限和空间范围统计；加工授权另行核对",route:"metadata" as const},
  {label:"标准与规范",value:number(total(data.definitions.filter(row=>["domain","standard","term"].includes(row.kind)))),note:"按现有权限可见，跨空间复用",route:"domain" as const},
  {label:"运行成功率",value:succeeded+failed?`${(succeeded/(succeeded+failed)*100).toFixed(1)}%`:"—",note:`本期空间运行${number(runs)}次 · 成功${number(succeeded)}次`,route:"operations" as const},
  {label:"质量合格率",value:checked?`${(passed/checked*100).toFixed(1)}%`:"—",note:checked?`合格${number(passed)} / 检查${number(checked)}行次`:"本期暂无质量节点检查",route:"trends" as const},
  {label:"已发布成果",value:number(stateCount(data.outputs,"published")),note:`累计成果${number(total(data.outputs))}份`,route:"results" as const},
  {label:"未关闭质量问题",value:number(openIssues),note:`其中逾期${number(data.overdue_issues)}项`,route:"issues" as const},
 ]:[];
 return <><Title title="治理看板" description="按业务流程查看当前空间的治理情况；私人轻量治理单独统计。" actions={<Space size={16} wrap>{helpOpen&&<><Button onClick={learning.start}>学习当前页</Button><Button onClick={()=>setPanel("relations")}>完整功能关系</Button></>}<Button onClick={()=>setPanel("metrics")}>指标说明</Button><Button loading={report.loading} onClick={()=>{report.reload();recent.reload()}}>刷新</Button></Space>}/>
 <div className="governance-dashboard-toolbar"><Space size={16} wrap><label htmlFor="governance-dashboard-days">运行与质量统计周期</label><Select id="governance-dashboard-days" aria-label="看板统计周期" value={days} onChange={setDays} options={[{value:7,label:"最近7天"},{value:30,label:"最近30天"},{value:90,label:"最近90天"}]}/><Tag>{workspace?`${workspace.name} · 当前空间`:"当前账号可见范围"}</Tag></Space><span className="helper-text">{data?<>截至 <Stamp value={data.observed_at}/> · {data.timezone}</>:"正在读取真实统计"}</span></div>
 {report.error?<Failure error={report.error} retry={report.reload}/>:!data?<Skeleton active paragraph={{rows:10}}/>:<div className="governance-dashboard">
 <div className="governance-kpi-grid">{metrics.map(metric=><section className="governance-kpi" key={metric.label}><Typography.Link onClick={()=>go(metric.route)}>{metric.label}</Typography.Link><strong>{metric.value}</strong><span>{metric.note}</span></section>)}</div>
 <div className="governance-chart-grid"><ChartPanel title="治理运行趋势" note={`近${data.days}天空间运行，按创建日期统计；重跑单独计次，不含私人轻量治理。`} route="operations"><RunTrend data={data}/></ChartPanel><ChartPanel title="运行状态分布" note="与左侧趋势采用相同周期；成功率仅计算成功、失败和中断。" route="operations"><RunStates data={data}/></ChartPanel><ChartPanel title="质量检查趋势" note={`近${data.days}天空间质量节点检查行次；同一行经过多个节点分别计次，不含私人轻量治理。`} route="trends"><QualityTrend data={data}/></ChartPanel><ChartPanel title="成果发布分布" note="累计可见成果；发布、质量通过和当前可交付分别核验。" route="results"><OutputStates data={data}/></ChartPanel></div>
 <section className="governance-dashboard-panel governance-dashboard-attention"><div><h3>需要关注</h3><p className="helper-text">异常次数帮助定位，整改和告警是否办结以对应模块为准。</p></div><Space size={16} wrap><Button danger={data.overdue_issues>0} onClick={()=>go("issues")}>逾期整改 {number(data.overdue_issues)}</Button><Button danger={failed>0} onClick={()=>go("operations")}>本期失败 / 中断 {number(failed)}</Button><Button onClick={()=>go("alerts")}>通知失败 {number(stateCount(data.alerts,"failed"))}</Button><Button onClick={()=>go("lifecycle")}>进行中迁移 {number(stateCount(data.migrations,"open"))}</Button></Space></section>
 <div className="governance-dashboard-section"><h3>按业务流程查看指标</h3><span className="helper-text">与当前菜单保持一致；大数字为对象数或注明的运行次数，轻量治理仅统计本人且不受空间切换影响。</span></div><Modules data={data}/>
 <section className="governance-dashboard-panel"><div className="governance-dashboard-heading"><h3>最近运行</h3><Typography.Link onClick={()=>go("operations")}>全部运行</Typography.Link></div>{recent.error?<Failure error={recent.error} retry={recent.reload}/>:recent.loading?<Skeleton active paragraph={{rows:2}}/>:recent.data?.recent_runs.length?<Table rowKey="id" dataSource={recent.data.recent_runs.slice(0,6)} pagination={false} scroll={{x:720}} columns={[{title:"运行",render:(_,row)=><Typography.Link onClick={()=>setRunId(row.id)}>{row.id}</Typography.Link>},{title:"类型",render:(_,row)=>({master:"主数据匹配",profile:"数据剖析",dataflow:"数据流",dataflow_preview:"代码试运行",workflow:"工作流"})[row.kind]??row.kind},{title:"状态",render:(_,row)=><Status value={row.state}/>},{title:"处理行数",render:(_,row)=>number(row.processed)},{title:"创建时间",render:(_,row)=><Stamp value={row.created_at}/>} ]}/>:<Empty description="暂无可见运行"/>}</section>
 </div>}
 <GovernanceGuide topic="overview"/>
 {learning.active?<GovernanceLearningDialog controller={learning}/>:info||panel?<GovernanceDialog header={info?topics[info].title:panel==="relations"?"完整功能关系与菜单划分":"看板指标口径"} width={info?1100:panel==="relations"?1480:1000} onClose={()=>{if(info)setInfo(null);else setPanel(null)}} closeLabel={info&&panel?"返回功能关系":"返回工作台"} actions={info?<Button type="primary" onClick={()=>navigate(journeyRoutes[info])}>前往对应业务功能</Button>:undefined}>{info?<GovernanceRelationInfo topic={info}/>:panel==="relations"?<><GovernancePurpose purpose="按职责组织功能，按前后关系衔接完整流程。" before="明确本次工作目标；已有来源、模型与规范可复用。" after="每个环节依赖真实业务状态，查看看板不会代替业务校验。"/><GovernanceRelationsTable onTopic={setInfo}/></>:<><p>空间指标遵循当前空间筛选；共享规范跨空间复用，私人轻量治理始终只统计本人且不受空间筛选影响。未选择空间时，管理员按可见范围汇总；普通账号的运行、调度、告警及策略按本人范围，定义按部门范围，整改按创建或指派范围，资源按治理职责范围，成果沿用成果列表可见范围。</p><Table pagination={false} rowKey="name" columns={[{title:"指标",dataIndex:"name"},{title:"含义与边界",dataIndex:"meaning"}]} dataSource={[{name:"时间周期",meaning:"运行按创建日期归组，质量按节点检查发生日期归组；从所选时区首日零点至统计时刻，包含今天。最近运行独立展示最新6条，不随周期筛选。"},{name:"运行成功率",meaning:"成功次数 ÷（成功 + 失败 + 中断次数）。排队、运行中、取消不进入分母；没有已结束运行显示—，不显示100%。工作流及其子任务各自属于独立运行。"},{name:"质量合格率",meaning:"质量节点合格输出行次 ÷ 检查输入行次。多个节点分别计次，包含质量失败的运行，不代表去重后的数据行数；没有检查显示—。"},{name:"轻量治理",meaning:"处理单元和 CSV 文件按本人累计计数，运行次数采用当前周期；检查和隔离按节点事件计行次。管理员也不能汇总其他人的私有数据，不计入空间运行成功率、质量趋势或成果发布。"},{name:"批处理、持续更新与调度",meaning:"数据流按最新版本的执行方式分别计数；持续任务展示当前登记状态，运行中不代表数据已追平。调度计划按数据流和工作流目标分别计数，持续任务不算定时计划。"},{name:"质量管理",meaning:"按周期内实际产生质量检查事件的运行去重计数，不等于检查方案数量或质量通过的成果数量。保存检查方案不会执行检查。"},{name:"定义与版本",meaning:"每个主题域、标准、术语、模型、数据流、工作流、模板只计1份；已发布数量指最新版本已发布，旧发布版本仍可能在用。"},{name:"成果发布与检查",meaning:"展示登记状态和历史检查事实。已发布不代表永久有效；结果有效期、来源、完整性、权限及当前职责仍由真实业务核验。"},{name:"血缘与生命周期",meaning:"血缘统计成功数据流运行次数；生命周期统计自动产出成果数，不代表当前都可清理。保留策略、迁移保护和下游依赖继续生效。"}]}/></>}</GovernanceDialog>:runId?<GovernanceDialog guide="operations" header="治理运行详情" width={1200} closeLabel="返回工作台" onClose={()=>setRunId(null)}><DashboardRunDetail id={runId}/></GovernanceDialog>:null}
 </>;
}

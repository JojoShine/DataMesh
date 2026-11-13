import {GovernanceGuide} from "../components/GovernanceGuide";
import {Button,Input,Select,Space,Table} from "antd";
import {useState} from "react";
import {useQuery} from "../hooks/useQuery";
import {Failure,Title} from "../components/common";

interface QualityTrend{items:{day:string;checked:number;passed:number;rejected:number;runs:number}[];timezone:string;unit:string}
export function GovernanceQualityTrends(){
 const [days,setDays]=useState(30);const [flow,setFlow]=useState("");const zone=Intl.DateTimeFormat().resolvedOptions().timeZone||"UTC";
 const report=useQuery<QualityTrend>(`/api/v1/governance/quality-trends?days=${days}&timezone=${encodeURIComponent(zone)}&definition_id=${encodeURIComponent(flow)}`);
 return <><Title title="质量趋势" description="按实际质量节点记录汇总检查行次，包含超过模型门槛而未产出成果的检查；多个质量节点分别计次，不表示唯一数据行数。" actions={<Button onClick={report.reload}>刷新</Button>}/><GovernanceGuide topic="trends"/><Space size={16} wrap className="governance-filter-bar"><Select aria-label="质量趋势时间范围" placeholder="请选择时间范围" value={days} options={[{value:7,label:"最近7天"},{value:30,label:"最近30天"},{value:90,label:"最近90天"},{value:366,label:"最近一年"}]} onChange={setDays}/><Input.Search placeholder="按数据流编号筛选，可留空" allowClear onSearch={setFlow}/></Space>{report.error?<Failure error={report.error}/>:<Table rowKey="day" dataSource={report.data?.items} loading={report.loading} scroll={{x:700}} columns={[{title:`日期（${zone}）`,dataIndex:"day"},{title:"运行数",dataIndex:"runs"},{title:"检查行次",dataIndex:"checked"},{title:"合格行次",dataIndex:"passed"},{title:"隔离行次",dataIndex:"rejected"},{title:"合格比例",render:(_,r)=>r.checked?`${(r.passed/r.checked*100).toFixed(2)}%`:"无检查数据"}]} pagination={false}/>}</>;
}

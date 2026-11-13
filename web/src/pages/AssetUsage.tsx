import {localPagination} from "../hooks/usePagination";
import {Alert,Button,Card,Select,Space,Table} from "antd";
import {useState} from "react";
import type {AssetUsageSummary} from "../api/assetProducts";
import {useQuery} from "../hooks/useQuery";
import {Failure,Title} from "../components/common";
const labels:Record<string,string>={deliver_raw_object:"原始对象查询",deliver_governed_dataset:"治理成果查询",deliver_table:"库表查询",download_file:"文件下载",invoke_api:"API 调用","delivery.query":"查询交付","delivery.download":"文件下载","delivery.invoke":"API 调用","grant.query":"查询交付","grant.download":"文件下载","grant.invoke":"API 调用"};
export function AssetUsage(){
 const [days,setDays]=useState(30);const summary=useQuery<AssetUsageSummary>(`/api/v1/assets/usage?days=${days}`);const data=summary.data;
 return <><Title title="我的使用统计" description="按本人成功交付审计汇总，次数不代表数据行数、字节数或人数。"/>
 <Space size={16} wrap style={{marginBottom:24}}><Select aria-label="统计时间范围" placeholder="请选择统计时间范围" value={days} onChange={setDays} options={[{value:7,label:"最近7天"},{value:30,label:"最近30天"},{value:90,label:"最近90天"}]}/><Button onClick={summary.reload}>刷新</Button></Space>
 {summary.error?<Failure error={summary.error} retry={summary.reload}/>:<Space orientation="vertical" size={24} style={{width:"100%"}}><Card title="成功交付次数" loading={summary.loading}>{data?<><strong>{data.total} 次</strong><p>统计区间（UTC）：{data.from} 至 {data.to}</p><p>{data.scope}</p></>:null}</Card><Alert showIcon title="成本与价值：暂无可核实的数据" description="本页仅汇总本人成功交付，不推算费用或业务收益。"/><Table rowKey="action" loading={summary.loading} dataSource={data?.items} pagination={false} scroll={{x:480}} columns={[{title:"交付操作",render:(_,r)=>labels[r.action]??r.action},{title:"次数",dataIndex:"count"}]}/><Table rowKey="date" loading={summary.loading} dataSource={data?.daily} pagination={localPagination} scroll={{x:480}} columns={[{title:"日期（UTC）",dataIndex:"date"},{title:"成功交付次数",dataIndex:"count"}]}/></Space>}</>;
}

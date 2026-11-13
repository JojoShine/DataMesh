import {PlatformDialog} from "../components/PlatformDialog";
import {Alert,Button,Grid,Space,Table,Tag,Typography} from "antd";
import type {Preflight} from "../api/types";
type Check=Preflight["checks"][number];
const states={passed:{label:"已通过",color:"success"},failed:{label:"未满足",color:"error"},unknown:{label:"无法检测",color:"warning"},unsupported:{label:"平台暂不支持",color:"warning"},skipped:{label:"等待检查",color:"default"}};
const labels:Record<string,string>={authentication:"数据库连接",logging:"日志配置",mapping:"采集作业",network:"网络连接",permission:"账号权限",primary_key:"表主键"};
export function SourceCheckReport({report,sourceName,busy,onClose,onRetry,closeLabel="关闭"}:{closeLabel?:string;report:Preflight|null;sourceName?:string;busy:boolean;onClose:()=>void;onRetry:()=>void}) {
 const compact=Grid.useBreakpoint().md===false;
 return <PlatformDialog closeLabel={closeLabel} busy={busy} centered title={`${report?.kind==="cdc"?"CDC 条件诊断":"连接检查结果"}${sourceName?` · ${sourceName}`:""}`} open={Boolean(report)} onCancel={busy?undefined:onClose} width="min(1100px, calc(100vw - 32px))" styles={{body:{overflowY:"auto"}}} footer={<Space size={16}><Button type="primary" loading={busy} onClick={onRetry}>重新检查</Button></Space>}>
  {report&&<Space orientation="vertical" size={16} style={{width:"100%"}}>
   <Alert showIcon type={report.passed?"success":"warning"} title={report.passed?"检查范围已通过":"仍有条件未满足或尚未确认"} description={report.kind==="cdc"?"逐项查看当前值、所需条件及处理负责人。预检读取配置，不执行下方处理步骤；完成处理后重新检查。平台能力限制由平台管理员处理。":"连接成功不代表已具备 CDC 条件。"}/>
   <Table<Check> rowKey="check" dataSource={report.checks} pagination={false} scroll={compact?undefined:{x:640}} columns={[
    {title:"检查项",width:170,responsive:["md"],render:(_,c)=>c.label??labels[c.check]??c.check},
    {title:"结果",width:140,responsive:["md"],render:(_,c)=>{const state=states[c.status??(c.passed?"passed":"failed")];return <Tag color={state.color}>{state.label}</Tag>}},
    {title:compact?"检查结果与处理":"诊断与处理",render:(_,c)=><Space orientation="vertical" size={8} style={{width:"100%"}}>
     {compact&&<Space wrap><Typography.Text strong>{c.label??labels[c.check]??c.check}</Typography.Text>{(()=>{const state=states[c.status??(c.passed?"passed":"failed")];return <Tag color={state.color}>{state.label}</Tag>})()}</Space>}
     <span>{c.reason}</span>
     {(c.actual||c.expected)&&<span>当前：{c.actual??"未获取"}；要求：{c.expected??"请按说明核对"}</span>}
     {c.owner&&<span>处理负责人：{c.owner}</span>}
     {!c.passed&&<>
      {c.suggestion&&<Typography.Text strong>{c.suggestion}</Typography.Text>}
      {Boolean(c.steps?.length)&&<ol style={{margin:0,paddingInlineStart:20}}>{c.steps?.map((step,index)=><li key={index}>{step}</li>)}</ol>}
      {c.verify_sql&&<><Typography.Text type="secondary">只读验证命令（使用采集账号或由 DBA 执行）</Typography.Text><Typography.Paragraph code copyable style={{whiteSpace:"pre-wrap",margin:0}}>{c.verify_sql}</Typography.Paragraph></>}
     </>}
    </Space>},
   ]}/>
  </Space>}
 </PlatformDialog>;
}

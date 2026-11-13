import {sourceNames,sourcePorts,sourceOptions,sourceDefinition} from "../api/sourceTypes";
import {localPagination} from "../hooks/usePagination";
import {PlatformDialog} from "../components/PlatformDialog";
import {App, Alert, Button, Form, Input, InputNumber, Select, Space, Table, Tag} from "antd";
import {useState} from "react";
import {errorMessage} from "../api/client";
import type {SourceAllowedTarget, SourceTarget} from "../api/types";
import {Failure} from "../components/common";
import {useAction} from "../hooks/useAction";
import {useQuery} from "../hooks/useQuery";

const names=sourceNames;
const ports=sourcePorts;
const origins = {platform:"平台配置",environment:"环境配置",builtin:"内置样例"};
export function SourceAllowlist({open,onClose}:{open:boolean;onClose:()=>void}) {
 const {message,modal}=App.useApp();
 const [adding,setAdding]=useState(false);
 const [form]=Form.useForm<SourceTarget>();
 const kind=Form.useWatch("kind",form)??"postgres";
 const action=useAction();
 const list=useQuery<{items:SourceAllowedTarget[];total:number}>(open?"/api/v1/source-allowed-targets":null);
 function closeAdd(){setAdding(false);form.resetFields()}
 async function save(value:SourceTarget){
  try {await action.run("/api/v1/source-allowed-targets",value);closeAdd();list.reload();message.success("白名单已新增，新的连接测试立即生效")}catch(error){message.error(errorMessage(error))}
 }
 function remove(row:SourceAllowedTarget){
  modal.confirm({title:"删除可信数据源",content:`${names[row.kind]} · ${row.host}:${row.port} / ${row.database}。删除后若无其他白名单放行，将拒绝新的连接，不删除数据源记录，也不会中断已建立的数据库连接。`,okText:"确认删除",okButtonProps:{danger:true},cancelText:"取消",onOk:async()=>{
   try {await action.run(`/api/v1/source-allowed-targets/${row.target_id}/remove`);list.reload();message.success("白名单已删除")}catch(error){message.error(errorMessage(error));throw error}
  }});
 }
 return <>
  <PlatformDialog title="可信数据源" open={open&&!adding} onCancel={onClose} centered width="min(1080px, calc(100vw - 32px))" styles={{body:{overflowY:"auto"}}} footer={null}>
   <Space orientation="vertical" size={16} style={{width:"100%"}}>
    <Alert type="info" showIcon title="按数据库类型、地址、端口及数据库名称精确放行" description="平台配置保存后立即生效，不需要重启。环境配置和内置样例只读展示；白名单不包含账号密码，也不代替数据库权限。"/>
    <Space size={16} wrap><Button type="primary" onClick={()=>setAdding(true)}>新增白名单</Button><Button onClick={list.reload} loading={list.loading}>刷新</Button><span className="helper-text">共 {list.data?.total??0} 条</span></Space>
    {list.error?<Failure error={list.error} retry={list.reload}/>:<Table<SourceAllowedTarget> rowKey="target_id" loading={list.loading} dataSource={list.data?.items} scroll={{x:850}} pagination={localPagination} columns={[
     {title:"数据源类型",dataIndex:"kind",render:(value:SourceTarget["kind"])=>names[value]},
     {title:"数据库地址",dataIndex:"host"},
     {title:"端口",dataIndex:"port"},
     {title:"数据库 / 服务名",dataIndex:"database"},
     {title:"配置来源",dataIndex:"origin",render:(origin:SourceAllowedTarget["origin"])=><Tag color={origin==="platform"?"blue":undefined}>{origins[origin]}</Tag>},
     {title:"操作",render:(_,row)=>row.origin==="platform"?<Button danger disabled={action.busy} onClick={()=>remove(row)}>删除</Button>:<span className="helper-text">只读</span>},
    ]}/>}
   </Space>
  </PlatformDialog>
  <PlatformDialog closeLabel="取消" title="新增可信数据源" open={open&&adding} centered width="min(640px, calc(100vw - 32px))" onCancel={action.busy?undefined:closeAdd} destroyOnHidden footer={<Space size={16}><Button type="primary" loading={action.busy} onClick={()=>form.submit()}>保存</Button></Space>}>
   <p className="helper-text">填写将要连接的准确目标。不支持通配符；Oracle 填写 Service Name。</p>
   <Form form={form} layout="vertical" disabled={action.busy} onFinish={save} initialValues={{kind:"postgres",port:5432}}>
    <Form.Item name="kind" label="数据源类型" rules={[{required:true}]}><Select placeholder="请选择数据库类型" options={sourceOptions} onChange={(value:SourceTarget["kind"])=>form.setFieldValue("port",ports[value])}/></Form.Item>
    <Form.Item name="host" label="数据库地址" rules={[{required:true,message:"请输入数据库 IP 或域名"}]}><Input maxLength={253} placeholder="请输入数据库 IP 或域名，不含协议和端口"/></Form.Item>
    <Form.Item name="port" label="端口" rules={[{required:true,message:"请输入端口"}]}><InputNumber min={1} max={65535} precision={0} style={{width:"100%"}} placeholder="请输入数据库端口"/></Form.Item>
    <Form.Item name="database" label={sourceDefinition(kind).database_label} rules={[{required:true,message:"请输入数据库名称或服务名"}]}><Input maxLength={128} placeholder={kind==="oracle"?"例如 ORCLPDB1":"请输入准确的数据库名称"}/></Form.Item>
   </Form>
  </PlatformDialog>
 </>;
}

import {useState} from "react";
import {Alert,App,Button,Descriptions,Form,Input,InputNumber,Table,Tag} from "antd";
import {PlatformDialog} from "../components/PlatformDialog";
import {Failure,Title} from "../components/common";
import {useQuery} from "../hooks/useQuery";
import {useAction} from "../hooks/useAction";
import {errorMessage} from "../api/client";
import type {DirectoryDictionary} from "../api/types";

type CodingValues={code_template:string;sequence_width:number;reason:string};
export function DirectoryCoding(){
 const list=useQuery<{items:DirectoryDictionary[]}>("/api/v1/directory-dictionaries");
 const access=useQuery<Record<string,boolean>>("/api/v1/resource-access");
 const [selected,setSelected]=useState<DirectoryDictionary|null>(null);const [form]=Form.useForm<CodingValues>();const action=useAction();const {message}=App.useApp();
 async function save(values:CodingValues){if(!selected)return;try{
  await action.run(`/api/v1/directory-dictionaries/${selected.id}/coding`,{...values,sequence_width:values.code_template?values.sequence_width:0,expected_version:selected.version});message.success("编码生成规则已保存");setSelected(null);list.reload();
 }catch(e){message.error(errorMessage(e))}}
 return <><Title title="目录编码配置" description="由目录主管维护已核验字典的生成规则，系统在保存目录草稿时分配唯一编码。" actions={<Button onClick={list.reload}>刷新</Button>}/>
 <Alert type="info" showIcon title="按实际编目规范配置" description="生成模板必须符合字典已核验的编码规则。平台不预设或冒充国家标准；缺少模板时可先保存未编码草稿。修改模板不改变既有目录编码。" style={{marginBottom:24}}/>
 {list.error?<Failure error={list.error}/>:<Table<DirectoryDictionary> rowKey="id" loading={list.loading} dataSource={list.data?.items} scroll={{x:900}} pagination={false} columns={[
 {title:"编码字典",render:(_,row)=>`${row.definition.name} · ${row.definition.version}`},
 {title:"来源依据",render:(_,row)=>row.definition.source},
 {title:"生成模板",render:(_,row)=>row.definition.code_template||"尚未配置"},
 {title:"状态",width:120,render:(_,row)=><Tag color={row.definition.test_only?"orange":row.definition.verified?"green":"default"}>{row.definition.test_only?"测试字典":row.definition.verified?"已核验":"待核验"}</Tag>},
 {title:"操作",width:150,render:(_,row)=><Button disabled={!row.definition.verified||!access.data?.authority_review} onClick={()=>{form.resetFields();form.setFieldsValue({code_template:row.definition.code_template??"",sequence_width:row.definition.sequence_width??6,reason:""});setSelected(row)}}>配置编码</Button>}
 ]}/>}
 <PlatformDialog title="配置编码生成规则" open={Boolean(selected)} onCancel={()=>setSelected(null)} busy={action.busy} width={720} footer={<Button form="directory-coding" type="primary" htmlType="submit" loading={action.busy}>保存配置</Button>}>
 <Descriptions column={1} size="small" items={[{key:"name",label:"当前字典",children:selected?.definition.name},{key:"pattern",label:"编码校验规则",children:selected?.definition.code_pattern},{key:"ref",label:"核验依据",children:selected?.definition.verification_ref}]}/>
 <Form id="directory-coding" form={form} layout="vertical" onFinish={save} style={{marginTop:24}}>
 <Form.Item name="code_template" label="生成模板" extra="必须包含一个 {sequence}；可用 {provider_code} 引用所属组织正式代码。例如内部规则可配置为 ORG-{sequence}，须与实际字典一致。留空可关闭自动编码。"><Input maxLength={200} placeholder="请输入经核验的编码模板"/></Form.Item>
 <Form.Item name="sequence_width" label="流水号位数" rules={[{required:true}]} extra="支持 1–12 位，不足补零。流水号由服务端分配，已占用编码不会复用。"><InputNumber min={1} max={12} placeholder="请选择流水号位数"/></Form.Item>
 <Form.Item name="reason" label="配置原因" rules={[{required:true,whitespace:true,message:"请填写配置原因"}]}><Input.TextArea rows={2} maxLength={1000} placeholder="请说明配置依据及变更原因"/></Form.Item>
 </Form></PlatformDialog></>;
}

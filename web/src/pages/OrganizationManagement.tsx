import {useState} from "react";
import {Alert,App,Button,Form,Input,Select,Space,Switch,Table,Tag,TreeSelect} from "antd";
import {PlatformDialog} from "../components/PlatformDialog";
import {Failure,Title} from "../components/common";
import {useQuery} from "../hooks/useQuery";
import {useAction} from "../hooks/useAction";
import {errorMessage} from "../api/client";
import type {OrganizationUnit} from "../api/types";

type UnitValues={name:string;code:string;kind:"organization"|"department";parent_id?:string|null;active:boolean;reason:string};
export function organizationTreeOptions(units:OrganizationUnit[]){
 const map=new Map(units.map(unit=>[unit.id,unit]));
 return units.map(unit=>{
  let current:OrganizationUnit|undefined=unit,active=true;
  for(let i=0;current&&i<65;i++){active=active&&current.active;current=current.parent_id?map.get(current.parent_id):undefined}
  return {id:unit.id,pId:unit.parent_id??undefined,value:unit.id,title:unit.name,disabled:!active};
 });
}
export function OrganizationManagement(){
 const list=useQuery<{items:OrganizationUnit[]}>("/api/v1/organization-units");
 const [editor,setEditor]=useState<{row:OrganizationUnit|null;kind:"organization"|"department"}|null>(null);
 const [form]=Form.useForm<UnitValues>();const action=useAction();const {message}=App.useApp();
 const units=list.data?.items??[];const kind=Form.useWatch("kind",form);
 function edit(row:OrganizationUnit|null,type:"organization"|"department"){
  form.resetFields();form.setFieldsValue(row?{...row,reason:""}:{kind:type,active:true,name:"",code:"",reason:""});setEditor({row,kind:type});
 }
 async function save(values:UnitValues){if(!editor)return;try{
  await action.run(editor.row?`/api/v1/organization-units/${editor.row.id}/revisions`:"/api/v1/organization-units",{...values,parent_id:values.kind==="organization"?null:values.parent_id,expected_version:editor.row?.version??0});
  message.success("组织信息已保存");setEditor(null);list.reload();
 }catch(e){message.error(errorMessage(e))}}
 type UnitRow=OrganizationUnit&{children?:UnitRow[]};
 function tree(parent:string|null):UnitRow[]{return units.filter(u=>u.parent_id===parent).map(u=>{const children=tree(u.id);return children.length?{...u,children}:u})}
 return <><Title title="组织与部门" description="维护提供单位、下属部门与正式代码，目录信息按人员所属组织自动带入。" actions={<Space size={16} wrap><Button onClick={list.reload}>刷新</Button><Button onClick={()=>edit(null,"department")}>新增部门</Button><Button type="primary" onClick={()=>edit(null,"organization")}>新增组织</Button></Space>}/>
 <Alert type="info" showIcon title="历史单位已保留原标识" description="请核实名称并补录正式单位代码。组织代码用于目录提供单位信息；修改组织资料不会直接改写已发布目录。" style={{marginBottom:24}}/>
 {list.error?<Failure error={list.error}/>:<Table<UnitRow> rowKey="id" loading={list.loading} dataSource={tree(null)} pagination={false} scroll={{x:850}} columns={[
 {title:"组织 / 部门",dataIndex:"name",width:280},
 {title:"类型",dataIndex:"kind",width:100,render:(v:string)=>v==="organization"?"组织":"部门"},
 {title:"正式代码",dataIndex:"code",render:(v:string)=>v||<Tag>待补录</Tag>},
 {title:"状态",dataIndex:"active",width:100,render:(v:boolean)=><Tag color={v?"green":"default"}>{v?"在用":"停用"}</Tag>},
 {title:"操作",width:140,render:(_,row)=><Button onClick={()=>edit(row,row.kind)}>维护信息</Button>}
 ]}/>}
 <PlatformDialog title={editor?.row?"维护组织信息":editor?.kind==="department"?"新增部门":"新增组织"} open={Boolean(editor)} onCancel={()=>setEditor(null)} busy={action.busy} footer={<Button type="primary" form="organization-edit" htmlType="submit" loading={action.busy}>保存信息</Button>}>
 <Form id="organization-edit" form={form} layout="vertical" onFinish={save}>
 <Form.Item name="kind" label="单位类型"><Select disabled options={[{value:"organization",label:"组织"},{value:"department",label:"部门"}]}/></Form.Item>
 {kind==="department"&&<Form.Item name="parent_id" label="上级组织或部门" rules={[{required:true,message:"请选择上级组织或部门"}]} extra="部门可多级。所属层级创建后固定，人员调岗通过人员管理办理。"><TreeSelect treeDataSimpleMode treeData={organizationTreeOptions(units)} disabled={Boolean(editor?.row)} placeholder="请选择上级组织或部门" treeDefaultExpandAll showSearch treeNodeFilterProp="title"/></Form.Item>}
 <Form.Item name="name" label="名称" rules={[{required:true,whitespace:true,message:"请填写名称"}]}><Input maxLength={200} placeholder="请输入组织或部门的完整名称"/></Form.Item>
 <Form.Item name="code" label={kind==="organization"?"提供单位代码":"部门代码"} rules={[{pattern:/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/,message:"请使用字母、数字、点、下划线或短横线"}]} extra={kind==="organization"?"填写编目要求的正式单位代码；未核实时可留空，系统不会编造。":"部门内部代码。目录提供单位代码取所属组织的正式代码。"}><Input maxLength={128} placeholder="请输入经核实的正式代码"/></Form.Item>
 <Form.Item name="active" label="在用状态" valuePropName="checked" extra="有在用人员或下级部门时不能停用。"><Switch checkedChildren="在用" unCheckedChildren="停用"/></Form.Item>
 <Form.Item name="reason" label="维护原因" rules={[{required:true,whitespace:true,message:"请填写维护原因"}]}><Input.TextArea rows={2} maxLength={1000} placeholder="请说明新增、修改或停用的原因"/></Form.Item>
 </Form></PlatformDialog></>;
}

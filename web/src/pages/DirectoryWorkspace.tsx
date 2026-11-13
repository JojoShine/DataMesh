import {usePagination} from "../hooks/usePagination";
import {PlatformDialog} from "../components/PlatformDialog";
import { resourceLabel } from "../api/types";
import {Modal, Alert, App, Button, Checkbox, Col, Descriptions, Dropdown, Form, Grid, Input, InputNumber, Row, Select, Space, Steps, Table, Tooltip, Typography } from "antd";
import { useEffect, useState } from "react";
import type { Dataset, DirectoryDefinition, DirectoryDictionary, DirectoryProvider, Field, Page, ResourceDirectory, ResourceMount, SourceObject } from "../api/types";
import { errorMessage, query, request } from "../api/client";
import { useAction } from "../hooks/useAction";
import { useQuery } from "../hooks/useQuery";
import { useSession } from "../stores/session";
import { Failure, Stamp, Status, Title } from "../components/common";
import { DirectoryDetails, classificationNames, sharingNames } from "./ResourceDirectory";

import { ResourceMountEditor as MountEditor } from "./ResourceMountEditor";
import { DownOutlined, PlusOutlined } from "@ant-design/icons";
type DirectoryRowAction={key:string;label:string;onClick:()=>void;primary?:boolean;danger?:boolean;disabled?:boolean;loading?:boolean};
function DirectoryRowActions({actions}:{actions:DirectoryRowAction[]}){
 const visible=actions.slice(0,3),more=actions.slice(3);
 return visible.length?<Space size={16}>{visible.map(a=><Button key={a.key} type={a.primary?"primary":"default"} danger={a.danger} disabled={a.disabled} loading={a.loading} onClick={a.onClick}>{a.label}</Button>)}{more.length>0&&<Dropdown trigger={["click"]} menu={{items:more.map(a=>({key:a.key,label:a.label,danger:a.danger,disabled:a.disabled||a.loading,onClick:a.onClick}))}}><Button>更多 <DownOutlined/></Button></Dropdown>}</Space>:<>—</>;
}
const textFields: {key:keyof DirectoryDefinition;label:string;placeholder:string;area?:boolean}[][]=[
 [{key:"name",placeholder:"请输入资源名称，例如：客户基础信息",label:"资源名称"},{key:"description",placeholder:"请说明资源内容、来源及适用业务场景",label:"资源说明",area:true},{key:"classification_path",placeholder:"请输入目录分类路径，与所选编码字典保持一致",label:"分类路径"},{key:"resource_code",placeholder:"请输入符合所选编码字典规则的资源编码",label:"资源编码"},{key:"provider_name",placeholder:"请输入数据提供单位的完整名称",label:"提供单位名称"},{key:"provider_code",placeholder:"请输入数据提供单位代码，与编目规则保持一致",label:"提供单位代码"},{key:"update_frequency",placeholder:"请输入更新周期，例如：每日、每月或实时",label:"更新周期"}],
 [],
 [{key:"sharing_conditions",placeholder:"请说明可申请的对象、使用范围及申请条件",label:"共享范围与使用条件",area:true},{key:"usage_restrictions",placeholder:"请说明用途、转供及保存期限等使用限制",label:"使用限制",area:true},{key:"non_sharing_reason",placeholder:"不予共享时，请说明具体原因",label:"不予共享理由",area:true},{key:"legal_basis",placeholder:"不予共享时，请填写法律法规名称及具体条款",label:"不予共享法律依据",area:true},{key:"security_classification",placeholder:"请输入数据分类及安全级别，例如：一般数据",label:"数据分类分级"},{key:"open_conditions",placeholder:"允许公开摘要时，请说明公开范围及限制",label:"摘要公开条件",area:true}],
 [{key:"department_approval_ref",placeholder:"请输入部门负责人审核记录编号",label:"部门负责人审核记录编号"},{key:"assessment_ref",placeholder:"请输入保密及个人信息保护评估记录编号",label:"保密及个人信息保护评估记录编号"}],
];
const fieldHelp:Partial<Record<keyof DirectoryDefinition,string>>={
 name:"填写便于业务人员发现和理解的名称，不必使用数据库表名。",
 description:"说明包含哪些业务数据、来自哪里、适用于什么场景；经审核的介绍可用于资产展示。",
 classification_path:"先选择编码字典，再从该字典的已核验分类中选择；用于目录归类与检索。",
 resource_code:"按主管配置的编码规则，在保存草稿时自动分配。已发布编码保持不变。",
 provider_name:"根据该目录所属部门的上级组织自动带入，组织信息由管理员维护。",
 provider_code:"取所属组织经核实的正式单位代码；缺失时请管理员补录。",
 update_frequency:"约定的数据更新频率，不代表系统已配置自动采集或调度任务。",
 sharing_conditions:"说明哪些对象可以申请，以及业务范围、使用目的和申请条件。",
 usage_restrictions:"说明获批后的用途、保存期限、转供及传播限制。",
 non_sharing_reason:"选择不予共享时必填，说明具体原因。",
 legal_basis:"不予共享时填写所依据的法律法规名称和具体条款。",
 security_classification:"填写经确认的数据分类及安全级别，与安全标签和保护要求保持一致。",
 open_conditions:"允许门户展示摘要时，说明可公开的范围及限制。实际记录仍按授权交付。",
 department_approval_ref:"引用实际负责人审核材料的编号；填写编号本身不代表完成平台审核。",
 assessment_ref:"引用保密、个人信息保护等实际评估材料编号，平台不代替实际评估。"
};
const updateFrequencies=["实时","每小时","每日","每周","每月","每季度","每半年","每年","不定期","不再更新"];
function itemFromField(f:Field){return {name:f.name,business_name:f.name,logical_type:f.logical_type,nullable:f.nullable,sensitive:f.sensitive,precision:f.precision,scale:f.scale,length:null,description:""}}
function DirectoryEditor({row,seed,onClose,onDone}:{row:ResourceDirectory|null;seed:Dataset|null;onClose:()=>void;onDone:()=>void}){
 const user=useSession(s=>s.user)!;const [step,setStep]=useState(0);const [form]=Form.useForm<DirectoryDefinition>();const action=useAction();const {message}=App.useApp();
 const dictionaries=useQuery<{items:DirectoryDictionary[]}>("/api/v1/directory-dictionaries");
 const users=useQuery<{items:{user_id:string;username:string;display_name?:string;department_id?:string}[]}>("/api/v1/directory-responsibles");
 const provider=useQuery<DirectoryProvider>("/api/v1/directory-provider"+(row?"?directory_id="+encodeURIComponent(row.directory_id):""));
 const frequency=Form.useWatch("update_frequency",form);
 const dictionaryID=Form.useWatch("dictionary_id",form);const dictionary=dictionaries.data?.items.find(d=>d.id===dictionaryID);
 useEffect(()=>{form.setFieldsValue(row?.definition??{name:seed?.definition.name??"",description:seed?.definition.description??"",classification:"department",classification_path:"",resource_code:"",dictionary_id:"",provider_name:user.department_id,provider_code:"",responsible_user_id:user.user_id,update_frequency:"每日",format:"database",data_layer:seed?"governed_dataset":"raw_object",items:seed?.definition.fields.map(itemFromField)??[],sharing_type:"conditional",sharing_conditions:"",non_sharing_reason:"",legal_basis:"",sharing_method:"query",usage_restrictions:seed?.definition.usage_restrictions??"",security_classification:"一般数据",open_to_public:false,open_conditions:"",department_approval_ref:"",assessment_ref:"",related_resource_codes:[]})},[form,row,seed,user]);
 useEffect(()=>{if(!provider.data)return;if(provider.data.provider_code||!row){form.setFieldsValue({provider_name:provider.data.provider_name,provider_code:provider.data.provider_code})}},[form,provider.data,row]);
 async function changeStep(next:number){
  if(next===step)return;
  const names=step===1?["items"]:textFields[step].map(f=>f.key);
  try{await form.validateFields(names,{recursive:true});setStep(next)}catch(e){
   const invalid=form.getFieldsError().find(f=>names.includes(String(f.name[0]))&&f.errors.length);
   if(invalid){form.scrollToField(invalid.name,{block:"nearest",focus:true});message.warning("请先填写当前步骤的必填项")}else{message.error(errorMessage(e))}
  }
 }
 async function save(v:DirectoryDefinition){try{await action.run(row?`/api/v1/resource-directories/${row.directory_id}/revisions`:"/api/v1/resource-directories",row?{expected_version:row.version,definition:v}:v);message.success("目录草稿已保存，补全信息后提交审核");onDone();onClose()}catch(e){message.error(errorMessage(e))}}
 return <PlatformDialog centered className="directory-editor" styles={{body:{display:"flex",flexDirection:"column",overflow:"hidden"}}} title={row?"修改目录草稿":"编制资源目录"} open onCancel={onClose} width={step===1?1280:880} footer={<Space wrap>{step>0&&<Button onClick={()=>void changeStep(step-1)}>上一步</Button>}{step<3&&<Button onClick={()=>void changeStep(step+1)}>下一步</Button>}<Button type="primary" loading={action.busy} onClick={()=>form.submit()}>保存草稿</Button></Space>}>
  <div className="directory-editor-steps"><Steps current={step} onChange={next=>void changeStep(next)} items={["基本信息","数据项","共享规则","审核依据"].map(title=>({title}))}/></div>
  <div className="directory-editor-content">
   {dictionaries.error?<Failure error={dictionaries.error}/>:!dictionaries.loading&&!dictionaries.data?.items.length?<Alert type="info" showIcon title="编码字典尚未配置" description="可以先保存草稿；正式发布前须由目录主管核验并配置编码字典。"/>:dictionary?.definition.test_only?<Alert type="warning" showIcon title="当前为本地测试字典，不能作为正式国家资源编码"/>:null}
   {provider.error?<Failure error={provider.error}/>:provider.data&&!provider.data.provider_code?<Alert type="info" showIcon title="所属组织尚未维护正式单位代码" description="可以先保存草稿；请管理员在组织与部门中核实并补录代码后，再完善目录并提交审核。" style={{marginBottom:24}}/>:null}
   <Form form={form} layout="vertical" onFinish={save}>
    {textFields.map((fields,index)=><div key={index} hidden={step!==index}>
     {index===0&&<Row gutter={24}><Col xs={24} md={12}><Form.Item name="classification" label="目录分类" tooltip="基础信息、主题信息和部门信息是业务编目分类，具体路径由编码字典定义。"><Select placeholder="请选择基础、主题或部门信息分类" options={[{value:"base",label:"基础信息"},{value:"theme",label:"主题信息"},{value:"department",label:"部门信息"}]}/></Form.Item></Col><Col xs={24} md={12}><Form.Item name="data_layer" label="数据层" tooltip="原始数据指直接登记的数据资源；治理成果指经过处理形成的数据集，用于核对挂载资源类型。"><Select placeholder="请选择原始数据或治理成果" options={[{value:"raw_object",label:"原始数据"},{value:"governed_dataset",label:"治理成果"}]}/></Form.Item></Col><Col xs={24} md={12}><Form.Item name="format" label="数据载体" tooltip="数据以数据库、API 或文件形式交付；需要与实际挂载资源及共享方式一致。"><Select placeholder="请选择数据库、API 或文件" onChange={value=>form.setFieldValue("sharing_method",value==="file"?"download":value==="api"?"invoke":"query")} options={[{value:"database",label:"数据库"},{value:"api",label:"API"},{value:"file",label:"文件"}]}/></Form.Item></Col><Col xs={24} md={12}><Form.Item name="dictionary_id" label="编码字典" tooltip="规定目录分类和资源编码规则；正式发布须使用已核验且适用的字典。"><Select placeholder="请选择已配置的资源编码字典" disabled={Boolean(row?.published_revision_id)} onChange={value=>{const next=dictionaries.data?.items.find(d=>d.id===value);if(!next?.definition.classifications.includes(form.getFieldValue("classification_path")))form.setFieldValue("classification_path",undefined);if(!row?.published_revision_id)form.setFieldValue("resource_code","")}} allowClear loading={dictionaries.loading} options={dictionaries.data?.items.map(d=>({value:d.id,label:d.definition.name+" · "+d.definition.version}))}/></Form.Item></Col><Col span={24}><Form.Item name="responsible_user_id" label="业务责任人" extra="负责解释业务含义、协调质量问题和数据使用事项；提交审核时须为本部门有效人员。"><Select placeholder="请选择资源所属部门的业务责任人" loading={users.loading} options={users.data?.items.filter(u=>!u.department_id||u.department_id===(row?.department_id??user.department_id)).map(u=>({value:u.user_id,label:u.display_name?`${u.display_name}（${u.username}）`:u.username}))}/></Form.Item></Col></Row>}
     {fields.map(f=><Form.Item key={f.key} name={f.key} label={f.label} extra={fieldHelp[f.key]} rules={f.key==="name"?[{required:true,whitespace:true,message:"请填写资源名称"}]:undefined}>{f.key==="classification_path"?<Select disabled={!dictionary} placeholder={dictionary?"请选择资源所属分类路径":"请先选择编码字典"} options={dictionary?.definition.classifications.map(value=>({value,label:value}))}/>:f.key==="update_frequency"?<Select placeholder="请选择更新周期" options={[...updateFrequencies,...(frequency&&!updateFrequencies.includes(frequency)?[frequency]:[])].map(value=>({value,label:value}))}/>:f.area?<Input.TextArea placeholder={f.placeholder} rows={3} maxLength={2000}/>:<Input readOnly={["resource_code","provider_name","provider_code"].includes(f.key)} placeholder={f.key==="resource_code"?"保存草稿时按字典规则自动生成":f.key==="provider_name"?"根据所属组织自动带入":f.key==="provider_code"?"根据所属组织正式代码自动带入":f.placeholder} maxLength={f.key==="name"?200:1000}/>}</Form.Item>)}
     {index===0&&dictionary&&!dictionary.definition.code_template&&!row?.definition.resource_code&&<Alert type="info" showIcon title="当前字典尚未配置编码生成规则" description="可先保存草稿；请目录主管在目录编码配置中维护生成模板，完成后再次保存即可获得编码。" style={{marginBottom:24}}/>}
     {index===1&&<><Alert title="每行一个数据项，业务名称用于展示，技术字段与挂载资源对应。" type="info" showIcon style={{marginBottom:16}}/>
      <Form.List name="items">{(items,{add,remove})=><>
       <div className="directory-item-list">{items.map(({key,name,...rest})=><div key={key} className="directory-item-row">
        <Form.Item {...rest} name={[name,"business_name"]} label="数据项名称" tooltip="面向业务人员展示的名称，例如客户姓名。" rules={[{required:true,whitespace:true,message:"请输入数据项名称"}]}><Input placeholder="如：客户姓名" maxLength={200}/></Form.Item>
        <Form.Item {...rest} name={[name,"name"]} label="技术字段" tooltip="与资源映射使用的字段标识，例如 customer_name。" rules={[{required:true,whitespace:true,message:"请输入技术字段"}]}><Input placeholder="如：customer_name" maxLength={128}/></Form.Item>
        <Form.Item {...rest} name={[name,"logical_type"]} label="类型" tooltip="字段的逻辑数据类型，用于挂载映射及数据校验。"><Select placeholder="请选择类型" options={["integer","string","decimal","boolean","date","datetime"].map(value=>({value,label:value}))}/></Form.Item>
        <Form.Item {...rest} name={[name,"length"]} label="长度" tooltip="字符串允许的最大长度；不适用时留空。"><InputNumber placeholder="长度" min={1} max={1000000}/></Form.Item>
        <Form.Item {...rest} name={[name,"precision"]} label="精度" tooltip="小数总位数，包含整数和小数部分；适用于 decimal。"><InputNumber placeholder="1–38" min={1} max={38}/></Form.Item>
        <Form.Item {...rest} name={[name,"scale"]} label="小数位" tooltip="小数点后位数，不得大于精度。"><InputNumber placeholder="0–38" min={0} max={38}/></Form.Item>
        <Form.Item {...rest} name={[name,"description"]} label="说明" tooltip="说明字段含义、计量单位及取值范围。"><Input placeholder="字段含义、单位" maxLength={1000}/></Form.Item>
        <div className="directory-item-flags"><Form.Item {...rest} name={[name,"sensitive"]} valuePropName="checked"><Checkbox><Tooltip title="该项包含敏感信息，后续需要结合安全标签、脱敏和授权要求处理。">敏感</Tooltip></Checkbox></Form.Item><Form.Item {...rest} name={[name,"nullable"]} valuePropName="checked"><Checkbox><Tooltip title="允许该字段为空；不勾选表示必须有值，挂载时会核对可空性。">可空</Tooltip></Checkbox></Form.Item></div>
        <Button danger className="directory-item-remove" onClick={()=>remove(name)}>移除</Button>
       </div>)}</div>
       <Button style={{marginTop:16}} onClick={()=>add({name:"",business_name:"",logical_type:"string",length:null,precision:null,scale:null,nullable:true,sensitive:false,description:""})}>添加数据项</Button>
      </>}</Form.List></>}

     {index===2&&<><Form.Item name="sharing_type" label="共享属性" tooltip="区分无条件共享、有条件共享和不予共享；数据使用仍受平台审批与授权控制。"><Select placeholder="请选择共享属性" options={Object.entries(sharingNames).map(([value,label])=>({value,label}))}/></Form.Item><Form.Item name="sharing_method" label="共享方式" tooltip="选择受控查询、授权下载或 API 调用，需要与载体和挂载资源一致。"><Select placeholder="请选择共享交付方式" options={[{value:"query",label:"受控查询"},{value:"download",label:"授权下载"},{value:"invoke",label:"API 调用"}]}/></Form.Item><Form.Item name="open_to_public" valuePropName="checked" extra="审核后允许未登录访客查看目录摘要与字段说明；不公开实际数据记录。"><Checkbox>经审核后允许门户展示资源摘要</Checkbox></Form.Item></>}
     {index===3&&<Alert title="提交审核前需补齐负责人审核与评估记录。本系统保存记录引用，不代替实际评估。" type="info" showIcon/>}
    </div>)}
   </Form>
  </div>
 </PlatformDialog>;
}
export function DirectoryWorkspace(){
 const screens=Grid.useBreakpoint();
 const [mountContext,setMountContext]=useState<ResourceDirectory|null>(null);
 const [mountPage,setMountPage,mountPagePagination]=usePagination();
 const [page,setPage,pagePagination]=usePagination(),[search,setSearch]=useState(""),[editor,setEditor]=useState<{row:ResourceDirectory|null}|null>(null),[detail,setDetail]=useState<ResourceDirectory|null>(null),[mountDirectory,setMountDirectory]=useState<ResourceDirectory|null>(null),[mountDetail,setMountDetail]=useState<ResourceMount|null>(null);
 const [editingMount,setEditingMount]=useState<ResourceMount|null>(null);
 const action=useAction();const {message}=App.useApp();const params=new URLSearchParams(window.location.search);const targetID=params.get("dataset_id");
 const directoryID=params.get("directory_id");
 const selectedDirectory=useQuery<ResourceDirectory>(directoryID?"/api/v1/resource-directories/"+encodeURIComponent(directoryID)+(params.get("action")==="mount"?"":"?scope=mine"):null);
 const seed=useQuery<Dataset>(targetID?"/api/v1/datasets/"+encodeURIComponent(targetID):null);const access=useQuery<Record<string,boolean>>("/api/v1/resource-access");
 const directories=useQuery<Page<ResourceDirectory>>("/api/v1/resource-directories?"+query({scope:"mine",page,search,page_size:pagePagination.pageSize}));const mounts=useQuery<Page<ResourceMount>>(mountContext?"/api/v1/resource-mounts?"+query({page:mountPage,directory_id:mountContext.directory_id,page_size:mountPagePagination.pageSize}):null);
 useEffect(()=>{
  if(params.get("action")==="create"&&access.data?.manage_resource&&(!targetID||seed.data))setEditor({row:null});
 },[access.data,seed.data,targetID]);
 useEffect(()=>{
  const d=selectedDirectory.data;if(!d||(targetID&&!seed.data))return;
  if(params.get("action")==="mount"&&d.permissions.operate_resource){setEditingMount(null);setMountDirectory(d)}
  else if(d.permissions.manage_resource)setEditor({row:d});
 },[selectedDirectory.data,seed.data,targetID]);
 function reload(){directories.reload();mounts.reload()}
 async function run(row:ResourceDirectory|ResourceMount,kind:string,reason?:string){const isMount="mount_id" in row;const id=isMount?row.mount_id:row.directory_id;try{await action.run(`/api/v1/${isMount?"resource-mounts":"resource-directories"}/${id}/${kind}`,{expected_version:row.version,...(reason?{reason}:{})});reload();setMountDetail(null);message.success("操作已完成，请核对最新状态")}catch(e){message.error(errorMessage(e))}}
 function withReason(row:ResourceDirectory|ResourceMount,kind:string){let value="";Modal.confirm({title:kind==="withdraw"?"下架目录":"暂停、卸载或撤回",content:<Input.TextArea rows={3} maxLength={1000} placeholder="请填写原因" onChange={e=>{value=e.target.value}}/>,okText:"确认",cancelText:"取消",onOk:async()=>{if(!value.trim())throw new Error("请填写原因");await run(row,kind,value)}})}
 return <><Title title="资源目录" description="登记目录信息，在每条目录后直接挂载和维护资源，审核通过后启用共享。" actions={<><Button onClick={reload}>刷新</Button><Tooltip title={access.data?.manage_resource?undefined:"需由管理员分配本部门资源管理职责"}><Button type="primary" icon={<PlusOutlined/>} disabled={!access.data?.manage_resource} onClick={()=>setEditor({row:null})}>新增目录</Button></Tooltip></>}/>
  {selectedDirectory.error&&<Failure error={selectedDirectory.error}/>}
  {!access.loading&&!access.error&&!access.data?.manage_resource&&<Alert style={{marginBottom:16}} type="info" showIcon title="当前账号未获本部门资源管理职责" description="可查看职责范围内的目录；新增目录需要管理员在数据安全 → 资源权限中配置角色和资源职责。"/>}
  {access.error&&<Failure error={access.error}/>} {seed.error&&<Failure error={seed.error}/>} {seed.data&&<Alert style={{marginBottom:24}} title={"治理成果："+seed.data.definition.name} description="请优先关联已有目录；没有对应条目时编制目录，字段会从治理成果带入。" type="info" showIcon/>}
  <section className="business-panel"><div className="table-toolbar"><Input.Search allowClear placeholder="搜索目录名称或编码" onSearch={v=>{setSearch(v);setPage(1)}}/></div>{directories.error?<Failure error={directories.error} retry={directories.reload}/>:<Table<ResourceDirectory> rowKey="directory_id" dataSource={directories.data?.items} loading={directories.loading} scroll={{x:1632}} pagination={{...pagePagination,total:directories.data?.total}} columns={[
   {title:"资源名称",width:220,fixed:screens.md?"left":undefined,render:(_,r)=><Typography.Link onClick={()=>setDetail(r)}>{r.definition.name}</Typography.Link>},
   {title:"资源编码",width:160,ellipsis:true,render:(_,r)=>r.definition.resource_code||"待编目"},
   {title:"目录分类",width:100,render:(_,r)=>classificationNames[r.definition.classification]||"—"},
   {title:"数据层",width:100,render:(_,r)=>r.definition.data_layer==="raw_object"?"原始数据":"治理成果"},
   {title:"提供单位",width:120,ellipsis:true,render:(_,r)=>r.definition.provider_name||r.department_id||"—"},
   {title:"共享属性",width:120,render:(_,r)=>sharingNames[r.definition.sharing_type]||"—"},
   {title:"数据项数",width:80,align:"right",render:(_,r)=>r.definition.items.length},
   {title:"更新周期",width:100,render:(_,r)=>r.definition.update_frequency||"—"},
   {title:"目录状态",width:100,render:(_,r)=><Status value={r.state}/>},
   {title:"修订状态",width:100,render:(_,r)=><Status value={r.revision_state==="pending"?"review_pending":r.revision_state}/>},
   {title:"操作",width:432,fixed:screens.md?"right":undefined,render:(_,r)=>{
    const actions:DirectoryRowAction[]=[];
    if(r.permissions.manage_resource){
     if(!["department_pending","authority_pending"].includes(r.revision_state))actions.push({key:"edit",label:"修改草稿",onClick:()=>setEditor({row:r})});
     if(r.revision_state==="draft")actions.push({key:"submit",label:"提交审核",primary:true,loading:action.busy,onClick:()=>void run(r,"submit")});
     if(["department_pending","authority_pending"].includes(r.revision_state))actions.push({key:"withdraw-submission",label:"撤回提交",onClick:()=>withReason(r,"withdraw-submission")});
    }
    if(r.permissions.operate_resource){
     actions.push({key:"mount",label:"挂载资源",onClick:()=>{setEditingMount(null);setMountDirectory(r)}});
     actions.push({key:"records",label:"挂载记录",onClick:()=>{setMountPage(1);setMountContext(r)}});
    }
    if(r.permissions.manage_resource&&r.state==="published")actions.push({key:"withdraw",label:"下架",danger:true,onClick:()=>withReason(r,"withdraw")});
    return <DirectoryRowActions actions={actions}/>;
   }},
  ]}/>}</section>
  <PlatformDialog centered width={1100} title={mountContext?.definition.name+" · 挂载记录"} open={Boolean(mountContext)&&!mountDirectory&&!mountDetail} onCancel={()=>setMountContext(null)} footer={null} styles={{body:{overflowY:"auto"}}}>
   <Space orientation="vertical" size={24} style={{width:"100%"}}>
    <Space size={16}><Button type="primary" icon={<PlusOutlined/>} onClick={()=>{setEditingMount(null);setMountDirectory(mountContext)}}>新增挂载</Button><Button onClick={mounts.reload}>刷新</Button></Space>
    {mountContext?.state!=="published"&&<Alert type="info" showIcon title="可先保存资源挂载，目录发布后再校验、审核和启用。"/>}
    {mounts.error?<Failure error={mounts.error} retry={mounts.reload}/>:<Table<ResourceMount> rowKey="mount_id" dataSource={mounts.data?.items} loading={mounts.loading} scroll={{x:1000}} pagination={{...mountPagePagination,total:mounts.data?.total}} columns={[
   {title:"挂载编号",render:(_,r)=><Button type="link" onClick={()=>setMountDetail(r)}>{r.mount_id.slice(0,12)}</Button>},{title:"数据层",render:(_,r)=>resourceLabel(r.definition.resource)},{title:"服务状态",render:(_,r)=><Status value={r.state}/>},{title:"修订状态",render:(_,r)=><Status value={r.revision_state==="pending"?"review_pending":r.revision_state}/>},
   {title:"操作",width:432,render:(_,r)=>{
    const actions:DirectoryRowAction[]=[];
    if(r.can_operate){
     if(r.revision_state!=="pending")actions.push({key:"edit",label:"修改修订",onClick:()=>{void request<ResourceDirectory>("/api/v1/resource-directories/"+r.directory_id).then(d=>{setEditingMount(r);setMountDirectory(d)}).catch(e=>message.error(errorMessage(e)))}});
     if(r.revision_state==="pending")actions.push({key:"withdraw-submission",label:"撤回提交",onClick:()=>withReason(r,"withdraw-submission")});
     if(["draft","validated"].includes(r.revision_state))actions.push({key:"validate",label:"校验",disabled:action.busy,onClick:()=>void run(r,"validate")});
     if(r.revision_state==="validated")actions.push({key:"submit",label:"提交审核",primary:true,disabled:action.busy,onClick:()=>void run(r,"submit")});
     if(r.revision_state==="approved"&&r.state!=="active")actions.push({key:"activate",label:"启用",primary:true,disabled:action.busy,onClick:()=>void run(r,"activate")});
     if(r.state==="active")actions.push({key:"suspend",label:"暂停",danger:true,onClick:()=>withReason(r,"suspend")});
     if(["active","suspended"].includes(r.state))actions.push({key:"unmount",label:"卸载",danger:true,onClick:()=>withReason(r,"unmount")});
    }
    return <DirectoryRowActions actions={actions}/>;
   }},
  ]}/>}
   </Space>
  </PlatformDialog>
  {editor&&<DirectoryEditor row={editor.row} seed={seed.data} onClose={()=>setEditor(null)} onDone={reload}/>}
  {mountDirectory&&<MountEditor returnLabel={mountContext?"返回挂载记录":undefined} directory={mountDirectory} seed={seed.data} row={editingMount} onClose={()=>setMountDirectory(null)} onDone={reload}/>}
  <PlatformDialog footer={null} centered styles={{body:{overflowY:"auto"}}} title={detail?.definition.name} open={Boolean(detail)} onCancel={()=>setDetail(null)} width={880}>{detail&&<DirectoryDetails row={detail}/>}</PlatformDialog>
  <PlatformDialog closeLabel="返回挂载记录" footer={null} centered styles={{body:{overflowY:"auto"}}} title="挂载详情与校验" open={Boolean(mountDetail)} onCancel={()=>setMountDetail(null)} width={880}>{mountDetail&&<Space orientation="vertical" size={24} style={{width:"100%"}}><Descriptions column={1} items={[{key:"state",label:"服务状态",children:<Status value={mountDetail.state}/>},{key:"rev",label:"修订状态",children:<Status value={mountDetail.revision_state==="pending"?"review_pending":mountDetail.revision_state}/>},{key:"date",label:"最近校验",children:mountDetail.definition.validation?<Stamp value={mountDetail.definition.validation.checked_at}/>:"待校验"}]}/><Table pagination={false} rowKey="name" dataSource={mountDetail.definition.validation?.checks??[]} columns={[{title:"检查项",dataIndex:"name"},{title:"结果",render:(_,r)=><Status value={r.passed?"passed":"blocked"}/>},{title:"说明",dataIndex:"message"}]}/><Typography.Title level={5}>字段映射</Typography.Title><Table pagination={false} rowKey="name" dataSource={Object.entries(mountDetail.definition.mapping).map(([name,target])=>({name,target}))} columns={[{title:"目录字段",dataIndex:"name"},{title:"资源字段",dataIndex:"target"}]}/>{mountDetail.definition.reason&&<Alert title="处理意见" description={mountDetail.definition.reason} type="info" showIcon/>}</Space>}</PlatformDialog>
 </>;
}

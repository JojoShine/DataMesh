import {governanceFieldTooltip} from "./GovernanceFieldHelp";
import {Alert,App,Button,Form,Input,InputNumber,Space} from "antd";
import type {GovernanceRef,MetadataResource} from "../api/governance";
import type {Field,Resource} from "../api/types";
import {errorMessage} from "../api/client";
import {useAction} from "../hooks/useAction";
import {useSession} from "../stores/session";
import {GovernanceDialog} from "./GovernanceDialog";
import {GovernanceReferenceSelect} from "./GovernanceReferenceSelect";
import {Title} from "./common";

type FileInput={model:GovernanceRef;sheet:string;header_row:number};
export function GovernanceFileContract({object,onClose,onSaved}:{object:MetadataResource;onClose:()=>void;onSaved:(object:MetadataResource)=>void}){
 const [form]=Form.useForm<FileInput>();const {message}=App.useApp();const action=useAction();const user=useSession(s=>s.user)!;
 async function save(value:FileInput){try{const result=await action.run<{resource:Resource;fields:Field[];structured_input:FileInput}>(`/api/v1/governance/metadata/${object.object_id}/structured-contract`,{...value,sheet:value.sheet??"",expected_version:object.schema_version});onSaved({...object,schema_version:result.resource.contract_version,fields:result.fields,structured_input:result.structured_input});message.success("文件结构已完整核验，字段读取约定已保存为新版本")}catch(e){message.error(errorMessage(e))}}
 return <GovernanceDialog guide="file" header={<Title title="设置文件读取方式" description="告诉平台哪一行是列名、每列是什么类型，文件才能参与数据检查和数据流处理。" />} actions={<Space size={16}><Button type="primary" loading={action.busy} onClick={()=>form.submit()}>检查文件并保存</Button></Space>} closeLabel="返回元数据" onClose={onClose} busy={action.busy} width={800}>
  <Alert showIcon type="warning" title="保存会更新共享交换登记版本" description="仅支持UTF-8 CSV和XLSX，最多10000行/20MiB。表头须与模型技术字段同名；保存后请核对相关目录、字段授权和数据流是否需要采用新版本，原有授权不会自动扩大。需要该资源的维护权限。"/>
  <Form form={form} layout="vertical" className="detail-section" initialValues={object.structured_input??{header_row:1,sheet:""}} onFinish={save}>
   <Form.Item name="model" label="已发布模型版本" tooltip={governanceFieldTooltip("已发布模型版本")} rules={[{required:true}]}><GovernanceReferenceSelect kind="model" departmentId={user.department_id}/></Form.Item>
   <Form.Item name="header_row" label="表头行（从1开始）" tooltip={governanceFieldTooltip("表头行（从1开始）")} rules={[{required:true,type:"number",min:1,max:1000}]}><InputNumber min={1} max={1000} placeholder="请输入表头行号"/></Form.Item>
   <Form.Item name="sheet" label="XLSX工作表（CSV留空）" tooltip={governanceFieldTooltip("XLSX工作表（CSV留空）")}><Input placeholder="请输入文件中实际的工作表名称" maxLength={128}/></Form.Item>
  </Form>
 </GovernanceDialog>;
}

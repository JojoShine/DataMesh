import {App,Alert,Button,Form,Input} from "antd";
import {useState} from "react";
import type {Field} from "../api/types";
import type {GovernanceFlowCheck} from "../api/governance";
import {errorMessage} from "../api/client";
import {useAction} from "../hooks/useAction";
import {useSession} from "../stores/session";
import {GovernanceDialog,GovernanceDialogActions} from "./GovernanceDialog";

export function GovernanceQualityPlanSave({fields,referenceFields,checks,departmentID}:{fields:Field[];referenceFields:Field[];checks:GovernanceFlowCheck[];departmentID:string}){
 const [open,setOpen]=useState(false);const action=useAction();const {message}=App.useApp();const user=useSession(s=>s.user)!;
 // Definitions belong to the creator's department; do not silently copy another department's draft.
 const allowed=departmentID===user.department_id;
 const contractFields=(values:Field[])=>values.map(({name,logical_type,nullable,primary_key,sensitive,precision,scale})=>({name,logical_type,nullable,primary_key,sensitive,precision:logical_type==="decimal"?precision:null,scale:logical_type==="decimal"?scale:null}));
 async function save(values:{name:string}){
  try{await action.run("/api/v1/governance/quality-templates",{definition:{name:values.name.trim(),description:"从数据流检查步骤保存，可在字段兼容的数据流中复用。",owner_id:user.user_id,steward_id:user.user_id,quality_template:{fields:contractFields(fields),reference_fields:contractFields(referenceFields),checks}},base_version:0,change_note:"从数据流检查步骤保存"});setOpen(false);message.success("检查方案草稿已保存，请到质量管理的检查方案中核对并发布；当前数据流配置保持不变。")}catch(error){message.error(errorMessage(error))}
 }
 return <><GovernanceDialogActions><Button disabled={action.readOnly||!allowed||!fields.length||!checks.length} onClick={()=>setOpen(true)}>保存为检查方案</Button></GovernanceDialogActions>{open&&<GovernanceDialog header="保存为检查方案" onClose={()=>setOpen(false)} busy={action.busy} width={700} actions={<Button form="quality-plan-save" htmlType="submit" type="primary" loading={action.busy}>保存方案草稿</Button>}><Alert showIcon type="info" title="字段和检查要求已从当前步骤带入" description="只需命名。保存为草稿后，在质量管理中核对并发布，其他数据流才能引用；当前检查继续使用原配置。"/><p className="helper-text detail-section">检查字段 {fields.length} 个，参考字段 {referenceFields.length} 个，检查要求 {checks.length} 项。</p><Form id="quality-plan-save" layout="vertical" onFinish={save}><Form.Item name="name" label="方案名称" rules={[{required:true,whitespace:true,message:"请输入方案名称"},{max:200}]}><Input placeholder="请输入便于复用的业务名称" maxLength={200}/></Form.Item></Form></GovernanceDialog>}</>;
}

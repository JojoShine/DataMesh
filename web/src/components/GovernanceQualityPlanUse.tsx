import {Alert,Button} from "antd";
import type {Field} from "../api/types";
import type {GovernanceFlowNode,GovernanceRecord,GovernanceRef} from "../api/governance";
import {useQuery} from "../hooks/useQuery";
import {Failure} from "./common";
import {GovernanceDialogActions} from "./GovernanceDialog";

export function GovernanceQualityPlanUse({planID,version,node,fields,referenceFields,editable,onApply}:{planID:string;version:string;node?:GovernanceFlowNode;fields:Field[];referenceFields:Field[];editable:boolean;onApply:(ref:GovernanceRef)=>void}){
 const plan=useQuery<GovernanceRecord>(`/api/v1/governance/quality-templates/${encodeURIComponent(planID)}/versions/${encodeURIComponent(version)}`);
 const template=plan.data?.definition.quality_template;
 const compatible=Boolean(template)&&[template?.fields??[],template?.reference_fields??[]].every((expected,index)=>expected.every(field=>(index===0?fields:referenceFields).some(actual=>actual.name===field.name&&actual.logical_type===field.logical_type)));
 const applied=node?.template?.id===plan.data?.id&&node?.template?.version===plan.data?.version;
 return <div className="governance-flow-feedback">{plan.error?<Failure error={plan.error} retry={plan.reload}/>:<Alert showIcon type="info" title={plan.data?`使用检查方案：${plan.data.definition.name} · v${plan.data.version}`:"正在读取检查方案"} description={plan.data?.state!=="published"?"只有已发布的方案版本可用于数据流。":applied?"方案已带入当前步骤，请核对后保存并运行。":"选择或新建数据流，进入修订并选择“检查数据质量”步骤，再应用此方案。跨表检查还需连接真实参考数据；字段不匹配时请先核对输入。"}/>}<GovernanceDialogActions><Button disabled={!editable||node?.kind!=="quality"||!compatible||plan.data?.state!=="published"||applied} onClick={()=>plan.data&&onApply({id:plan.data.id,version:plan.data.version})}>应用到当前检查步骤</Button></GovernanceDialogActions></div>;
}

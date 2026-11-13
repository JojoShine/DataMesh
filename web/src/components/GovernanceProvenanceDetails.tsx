import { Descriptions, Space } from "antd";
import type { GovernanceProvenance } from "../api/types";

export function GovernanceProvenanceDetails({governance,compact=false}:{governance:GovernanceProvenance;compact?:boolean}) {
 return <Space orientation="vertical" size={16} style={{width:"100%"}}>
  {!compact&&<Descriptions column={1} items={[
   ...(governance.source_name?[{key:"source",label:"原始数据",children:governance.source_name}]:[]),
   ...(governance.original_directory_name?[{key:"directory",label:"原目录",children:governance.original_directory_name}]:[]),
   ...(governance.provider_department_id?[{key:"provider",label:"提供部门",children:governance.provider_department_id}]:[]),
  ]}/>}
  <div>{!compact&&<strong>治理内容</strong>}{governance.steps.length?<ul>{governance.steps.map((step,index)=><li key={`${step.kind}-${index}`}><strong>{step.name}</strong>：{step.description}</li>)}</ul>:<p className="helper-text">未记录具体治理步骤，请向数据提供部门核实。</p>}</div>
 </Space>;
}

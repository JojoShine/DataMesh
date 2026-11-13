import {Alert,Form,Radio} from "antd";
import type {Dataset} from "../api/types";
import {GovernanceProvenanceDetails} from "./GovernanceProvenanceDetails";

export type GovernanceDirectoryChoice="original"|"new";
export function governanceResultDirectoryPath(data:Dataset,choice:GovernanceDirectoryChoice):string {
 const params=new URLSearchParams({view:"manage",dataset_id:data.dataset_id});
 if(choice==="original"&&data.governance?.original_directory_id){params.set("directory_id",data.governance.original_directory_id);params.set("action","mount")}
 else params.set("action","create");
 return `/platform/exchange/directory?${params}`;
}
export function GovernanceResultDirectoryChoice({data,value,onChange}:{data:Dataset;value:GovernanceDirectoryChoice;onChange:(value:GovernanceDirectoryChoice)=>void}){
 const governance=data.governance;if(!governance)return null;
 const original=governance.single_source&&Boolean(governance.original_directory_id);
 return <section className="detail-section" aria-label="治理内容与目录归属">
  <h3>治理内容与目录归属</h3>
  <GovernanceProvenanceDetails governance={governance}/>
  <Form layout="vertical" className="detail-section"><Form.Item label="治理完成后关联到哪里">
   <Radio.Group value={original?value:"new"} onChange={event=>onChange(event.target.value==="original"?"original":"new")}>
    <Radio value="original" disabled={!original}>关联回原目录{governance.original_directory_name?` · ${governance.original_directory_name}`:""}（默认）</Radio>
    <Radio value="new">新建目录并关联治理结果</Radio>
   </Radio.Group>
  </Form.Item></Form>
  <Alert showIcon type="info" title={original?"原始数据保留，治理后的数据作为独立资源供申请":"来源尚无可直接关联的原目录，请在共享交换中补充目录"} description="选择后点击“前往共享办理”继续填写。目录关联仍需完成字段核对、挂载审核与启用；治理后的数据使用独立申请和授权。"/>
 </section>;
}

import {governanceFieldTooltip,GovernanceFieldHelp} from "./GovernanceFieldHelp";
import {Button,Checkbox,Form,Input,InputNumber,Select,Space} from "antd";
import type {GovernanceDefinition} from "../api/governance";
import {governanceTypes} from "../api/governance";
import {GovernanceChecksEditor} from "./GovernanceChecksEditor";

function TemplateFields({name}:{name:"fields"|"reference_fields"}){
 const form=Form.useFormInstance<GovernanceDefinition>();const values=Form.useWatch(["quality_template",name],form)??[];
 return <Form.List name={["quality_template",name]}>{(fields,{add,remove})=><>{fields.map(({key,name:index,...rest})=><div className="governance-field-editor" key={key}><Space size={16} wrap className="governance-form-grid">
  <Form.Item {...rest} name={[index,"name"]} label="字段技术名称" tooltip={governanceFieldTooltip("字段技术名称")} rules={[{required:true},{pattern:/^[A-Za-z_][A-Za-z0-9_]{0,62}$/}]}><Input placeholder="请输入字母或下划线开头的名称" maxLength={63}/></Form.Item>
  <Form.Item {...rest} name={[index,"logical_type"]} label="字段类型" tooltip={governanceFieldTooltip("字段类型")} rules={[{required:true}]}><Select placeholder="请选择字段类型" options={governanceTypes} onChange={type=>{form.setFieldValue(["quality_template",name,index,"precision"],type==="decimal"?38:null);form.setFieldValue(["quality_template",name,index,"scale"],type==="decimal"?0:null)}}/></Form.Item>
  {values[index]?.logical_type==="decimal"&&<><Form.Item {...rest} name={[index,"precision"]} label="数值精度" tooltip={governanceFieldTooltip("数值精度")} rules={[{required:true}]}><InputNumber min={1} max={38} placeholder="请输入1–38"/></Form.Item><Form.Item {...rest} name={[index,"scale"]} label="小数位数" tooltip={governanceFieldTooltip("小数位数")} rules={[{required:true}]}><InputNumber min={0} max={38} placeholder="请输入0至精度"/></Form.Item></>}
  <Form.Item {...rest} name={[index,"nullable"]} valuePropName="checked"><Checkbox>可空<GovernanceFieldHelp label="可空"/></Checkbox></Form.Item><Button danger onClick={()=>remove(index)}>移除字段</Button>
 </Space></div>)}<Button disabled={fields.length>=100} onClick={()=>add({name:"",logical_type:"string",nullable:true,primary_key:false,sensitive:false,precision:null,scale:null})}>添加{name==="fields"?"检查":"参考表"}字段</Button></>}</Form.List>;
}

export function GovernanceQualityTemplateEditor(){
 const form=Form.useFormInstance<GovernanceDefinition>();const fields=Form.useWatch(["quality_template","fields"],form)??[];const reference=Form.useWatch(["quality_template","reference_fields"],form)??[];
 return <><div className="governance-editor-panel"><h3>检查输入契约<GovernanceFieldHelp label="检查输入契约"/></h3><TemplateFields name="fields"/></div><div className="governance-editor-panel"><h3>参考表契约（跨表检查时填写）<GovernanceFieldHelp label="参考表契约（跨表检查时填写）"/></h3><TemplateFields name="reference_fields"/></div><div className="governance-editor-panel"><h3>模板规则</h3><Form.Item name={["quality_template","checks"]} rules={[{required:true,type:"array",min:1}]}><GovernanceChecksEditor fields={fields} referenceFields={reference}/></Form.Item></div></>;
}

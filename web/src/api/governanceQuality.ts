export const qualityFormatNames:Record<string,string>={mobile:"手机号格式（大陆）",email:"邮箱格式",date:"日期格式（YYYY-MM-DD）",integer:"整数格式",decimal:"小数格式",no_whitespace:"不含空白字符"};
import type {GovernanceFlowCheck,GovernanceFlowNode,GovernanceFlowRun} from "./governance";

export function governanceQualityFlow(run:GovernanceFlowRun){
 return run.definition.pinned.find(item=>item.kind==="dataflow"&&item.ref.id===run.definition.flow.id&&item.ref.version===run.definition.flow.version)?.definition;
}
export function governanceQualityNodes(run:GovernanceFlowRun){
 return governanceQualityFlow(run)?.dataflow?.nodes.filter(node=>node.kind==="quality")??[];
}
export function governanceQualityPlan(run:GovernanceFlowRun,node:GovernanceFlowNode){
 return node.template?run.definition.pinned.find(item=>item.kind==="quality_template"&&item.ref.id===node.template?.id&&item.ref.version===node.template.version)?.definition:undefined;
}
export function governanceQualityParameters(check:GovernanceFlowCheck){
 const values=check.parameters??{};
 if(check.kind==="format")return qualityFormatNames[String(values.format)]??"未选择格式";
 if(check.kind==="range"||check.kind==="length")return [values.min!==undefined?`下限 ${String(values.min)}`:"",values.max!==undefined?`上限 ${String(values.max)}`:""].filter(Boolean).join("，");
 if(check.kind==="enum")return Array.isArray(values.values)?`允许 ${values.values.map(String).join("、")}`:"";
 if(check.kind==="type")return String(values.type??"");
 if(check.kind==="reference_exists")return `参照字段 ${String(values.other_field??"")}`;
 if(check.kind==="compare_fields"){const operator=Object.entries({eq:"等于",ne:"不等于",gt:"大于",gte:"大于等于",lt:"小于",lte:"小于等于"}).find(([key])=>key===values.operator)?.[1]??"比较";return `${operator} ${String(values.other_field??"")}`;}
 return "";
}
// Match the server's issue eligibility; creation still validates permission and retained snapshots.
export function governanceQualityIssueEligible(run:GovernanceFlowRun){
 return run.state==="succeeded"?Boolean(run.result&&run.result.rejected_rows>0):run.state==="failed"&&["GOVERNANCE_QUALITY_BLOCKED","GOVERNANCE_OUTPUT_DUPLICATE_KEY","GOVERNANCE_OUTPUT_NULL","GOVERNANCE_OUTPUT_TYPE","GOVERNANCE_STANDARD_FAILED","GOVERNANCE_CODE_UNMATCHED"].includes(run.error_code);
}

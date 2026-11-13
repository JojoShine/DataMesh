import {applicationURL} from "../app/basePath";
import type {MouseEvent} from "react";
import {navigate} from "../app/navigation";
import {useSession} from "../stores/session";
import "./GovernanceQuickStart.css";

const recipes=[
 {title:"单表清洗与质量检查",steps:["在来源数据中确认可用资源与字段加工权限；到元数据管理查看字段或做数据剖析。","在数据流设计中选来源，按需去空白、去重、筛选，添加质量检查并确认输出字段或结果模型。","确认后点击“保存并运行”（已保存草稿用“检查并运行”），完整运行后到成果核验查看质量与完整性；若选择根据结果生成模型，先在数据建模确认并发布草稿、绑定后重新运行。"],links:[{page:"dataflows",label:"设计单表清洗"},{page:"results",label:"核验并发布成果"}]},
 {title:"多来源关联与汇总",steps:["确认各来源均可读取及加工，核对关联键类型、字段含义与数据范围。","在数据流设计的业务步骤中选择主表，点击“添加关联表”逐张补充数据，选择对应字段和关联方式，再按需汇总和检查质量；分支或复杂关系进入高级编排。","确认关联后的输出字段与模型，完整运行后核对匹配结果、隔离记录和成果；异常回到数据流修正。"],links:[{page:"dataflows",label:"设计多来源关联"},{page:"models",label:"查看结果模型"}]},
 {title:"定时执行与异常处理",steps:["先保存并发布所需数据流版本，完整运行验证后，在工作流编排中安排数据流、质量门禁等任务及前后依赖。","保存并发布工作流版本，手动运行确认结果，再在调度计划中选择该版本、配置周期与参数并启用。","到运行运维查看每次执行；失败时查看诊断，质量异常进入质量整改，修复后复测。需要发布审批时另配审批任务。"],links:[{page:"workflows",label:"编排定时工作流"},{page:"schedules",label:"设置调度计划"},{page:"operations",label:"查看运行记录"}]},
];
function followLink(event:MouseEvent<HTMLAnchorElement>){
 if(event.button!==0||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;
 event.preventDefault();navigate(event.currentTarget.pathname+event.currentTarget.search);
}
export function GovernanceQuickStart({workspaceID}:{workspaceID?:string|null}){
 const user=useSession(s=>s.user);
 const canGovern=Boolean(user&&(user.roles.includes("admin")||(user.platforms.includes("governance")&&user.roles.includes("governor"))));
 const path=(page:string)=>`/platform/governance/${page}${workspaceID?`?${new URLSearchParams({workspace_id:workspaceID})}`:""}`;
 return <section className="governance-quick-start" aria-label="常用治理工作流">
  <div className="governance-quick-start-heading"><div><h2>常用治理工作流</h2><p>先确认来源与加工权限，再按任务选择处理路径。进入治理平台后先选择治理空间。</p></div>{canGovern&&<a href={applicationURL(path("space-sources"))} onClick={followLink}>查看来源数据</a>}</div>
  <div className="governance-recipe-grid">{recipes.map(recipe=><article className="governance-recipe" key={recipe.title}><h3>{recipe.title}</h3><ol>{recipe.steps.map(step=><li key={step}>{step}</li>)}</ol>{canGovern&&<div className="governance-recipe-links">{recipe.links.map(link=><a key={link.page} href={applicationURL(path(link.page))} onClick={followLink}>{link.label}</a>)}</div>}</article>)}</div>
  <p className="governance-quick-start-note">保存保留方案；定义发布后才可正式运行；样本试运行用于验证代码；结果模型草稿须确认发布并绑定后，完整运行才能产出可发布成果。成果核验通过后，由具备资产发布职责的人员办理成果发布；需要共享时，再到共享交换维护目录、挂载并审核，由使用方申请、获批授权后使用。</p>
  {!user?<a href={applicationURL("/login")} onClick={followLink}>登录后开始治理</a>:!canGovern?<p className="governance-quick-start-note">需开通数据治理平台及治理职责后，才能使用以上处理入口；数据来源与字段加工仍须单独授权。</p>:null}
 </section>;
}

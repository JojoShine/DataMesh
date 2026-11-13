// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor,within} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import type {GovernanceFlowRun} from "../src/api/governance";
import {GovernanceQualityWorkspace} from "../src/pages/GovernanceQualityWorkspace";
import {GovernanceQualityPlanSave} from "../src/components/GovernanceQualityPlanSave";
import {GovernanceQualityPlanUse} from "../src/components/GovernanceQualityPlanUse";
import {GovernanceQualityIssueCreate} from "../src/components/GovernanceQualityIssueCreate";
import {GovernanceQualityRunSummary} from "../src/components/GovernanceQualityRunSummary";
import {GovernanceQualityIssues} from "../src/pages/GovernanceQualityIssues";
import {GovernanceFlowRunView} from "../src/pages/GovernanceDataflows";
import {governanceQualityFlow,governanceQualityIssueEligible,governanceQualityNodes,governanceQualityParameters,governanceQualityPlan} from "../src/api/governanceQuality";
import {useSession} from "../src/stores/session";
const field={name:"amount",logical_type:"decimal",precision:20,scale:6,nullable:false,primary_key:false,sensitive:false};
const flow={id:"flow",version:3};
const run:GovernanceFlowRun={workspace_id:"space",id:"run",kind:"dataflow",state:"succeeded",processed:12,error_code:"",created_at:"2026-10-28T01:00:00Z",started_at:null,finished_at:null,retry_of:null,cancel_requested:false,definition:{flow,pinned:[{kind:"dataflow",ref:flow,definition:{name:"商品整理",description:"",owner_id:"owner",steward_id:"owner",dataflow:{nodes:[{id:"check",kind:"quality",inputs:[],checks:[{field:"amount",kind:"required",blocking:true}]}]}}}],inputs:{}},result:null};
vi.setConfig({testTimeout:20000});
beforeEach(()=>{history.replaceState(null,"","/platform/governance/quality?workspace_id=space");useSession.setState({user:{user_id:"owner",username:"治理人员",department_id:"dept",roles:["governor"],platforms:["governance"],csrf_token:"test"},ready:true,epoch:0,identityError:null});Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
function show(child:React.ReactNode){render(<ConfigProvider theme={{token:{motion:false}}}><AntApp>{child}</AntApp></ConfigProvider>)}
function fetchRuns(items:GovernanceFlowRun[]){vi.stubGlobal("fetch",vi.fn(async()=>Response.json({items,total:items.length,page:1,page_size:20})))}
it("质量工作区包含使用反馈业务页签，空记录引导配置检查，切换保留空间",async()=>{
 fetchRuns([]);show(<GovernanceQualityWorkspace/>);
 expect(screen.getAllByRole("tab").map(tab=>tab.textContent)).toEqual(["检查记录","检查方案","问题整改","使用反馈","质量趋势"]);
 expect(await screen.findByText("还没有数据流检查记录")).toBeTruthy();
 await userEvent.click(screen.getByRole("tab",{name:"检查方案"}));
 expect(location.pathname+location.search).toBe("/platform/governance/quality-templates?workspace_id=space");
 expect(await screen.findByRole("button",{name:"新建检查方案"})).toBeTruthy();
});
it("运行完成和检查结论分开，来源规则收进历史成果办理",async()=>{
 fetchRuns([run]);show(<GovernanceQualityWorkspace/>);
 expect(await screen.findByRole("button",{name:"商品整理 · v3"})).toBeTruthy();
 expect(screen.getByText("1 个检查步骤 · 结论见详情")).toBeTruthy();
 expect(screen.queryByText("质量通过")).toBeNull();
 expect(screen.getByText("历史成果办理")).toBeTruthy();
 expect(screen.queryByRole("button",{name:"新增规则"})).toBeNull();
});
it("未配置检查的执行不能显示为已检查",async()=>{
 fetchRuns([{...run,definition:{...run.definition,pinned:[]}}]);show(<GovernanceQualityWorkspace/>);
 expect(await screen.findByText("未配置检查 · 可回数据流补充")).toBeTruthy();
});
it("固定检查取对应的数据流快照，不能取任意第一个引用",()=>{
 const reordered={...run,definition:{...run.definition,pinned:[{kind:"model" as const,ref:{id:"model",version:1},definition:{name:"结果模型",description:"",owner_id:"owner",steward_id:"owner"}},...run.definition.pinned]}};
 expect(governanceQualityFlow(reordered)?.name).toBe("商品整理");expect(governanceQualityNodes(reordered).map(node=>node.id)).toEqual(["check"]);
});
it("检查摘要保留精确边界、允许值及固定方案名称",()=>{
 expect(governanceQualityParameters({field:"amount",kind:"range",blocking:true,parameters:{min:"0.000001",max:"99999999999999.999999"}})).toBe("下限 0.000001，上限 99999999999999.999999");
 expect(governanceQualityParameters({field:"state",kind:"enum",blocking:true,parameters:{values:["启用","停用"]}})).toBe("允许 启用、停用");
 const withPlan={...run,definition:{...run.definition,pinned:[...run.definition.pinned,{kind:"quality_template" as const,ref:{id:"plan",version:2},definition:{name:"金额校验",description:"",owner_id:"owner",steward_id:"owner"}}]}};
 expect(governanceQualityPlan(withPlan,{id:"check",kind:"quality",inputs:[],template:{id:"plan",version:2}})?.name).toBe("金额校验");expect(governanceQualityPlan(withPlan,{id:"check",kind:"quality",inputs:[],template:{id:"plan",version:1}})).toBeUndefined();
});
it("只有真实质量失败或成功且隔离记录的运行允许登记整改",()=>{
 expect(governanceQualityIssueEligible(run)).toBe(false);
 expect(governanceQualityIssueEligible({...run,state:"failed",error_code:"GOVERNANCE_TIMEOUT"})).toBe(false);
 expect(governanceQualityIssueEligible({...run,state:"failed",error_code:"GOVERNANCE_QUALITY_BLOCKED"})).toBe(true);
 expect(governanceQualityIssueEligible({...run,result:{flow,model:{id:"model",version:1},output_node:"output",output_rows:10,rejected_rows:2,snapshot_scope:"per_source_snapshot",nodes:[],lineage:{}}})).toBe(true);
});
it("从当前检查保存方案，保留精确字段和参考字段，只生成草稿",async()=>{
 const posts:{path:string;body:unknown}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST")posts.push({path,body:JSON.parse(String(init.body))});return Response.json({id:"plan",version:1,state:"draft"})}));
 show(<GovernanceQualityPlanSave fields={[{...field,business_name:"金额"}]} referenceFields={[{...field,name:"reference_amount"}]} checks={[{field:"amount",kind:"range",blocking:true,parameters:{min:"0.000001"}}]} departmentID="dept"/>);
 await userEvent.click(screen.getByRole("button",{name:"保存为检查方案"}));const dialog=screen.getByRole("dialog");
 expect(within(dialog).getByText(/字段和检查要求已从当前步骤带入/)).toBeTruthy();
 await userEvent.type(within(dialog).getByLabelText("方案名称"),"金额要求");await userEvent.click(within(dialog).getByRole("button",{name:"保存方案草稿"}));
 await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0]).toMatchObject({path:"/api/v1/governance/quality-templates?workspace_id=space",body:{base_version:0,definition:{name:"金额要求",owner_id:"owner",steward_id:"owner",quality_template:{fields:[field],reference_fields:[{...field,name:"reference_amount"}],checks:[{field:"amount",kind:"range",blocking:true,parameters:{min:"0.000001"}}]}}}});
 expect(screen.queryByRole("dialog")).toBeNull();
});
it("已发布检查方案明确应用到当前步骤，不自行运行或提交数据流",async()=>{
 const apply=vi.fn();const fetcher=vi.fn(async()=>Response.json({id:"plan",version:2,state:"published",definition:{name:"金额要求",quality_template:{fields:[field],reference_fields:[],checks:[]}}}));vi.stubGlobal("fetch",fetcher);
 show(<GovernanceQualityPlanUse planID="plan" version="2" node={{id:"check",kind:"quality",inputs:[]}} fields={[field]} referenceFields={[]} editable onApply={apply}/>);
 await screen.findByText("使用检查方案：金额要求 · v2");await userEvent.click(screen.getByRole("button",{name:"应用到当前检查步骤"}));expect(apply).toHaveBeenCalledExactlyOnceWith({id:"plan",version:2});expect(fetcher.mock.calls).toHaveLength(1);
});
it("草稿方案和字段不匹配均不能应用",async()=>{
 const apply=vi.fn();vi.stubGlobal("fetch",vi.fn(async()=>Response.json({id:"plan",version:1,state:"draft",definition:{name:"金额要求",quality_template:{fields:[field],reference_fields:[],checks:[]}}})));
 show(<GovernanceQualityPlanUse planID="plan" version="1" node={{id:"check",kind:"quality",inputs:[]}} fields={[]} referenceFields={[]} editable onApply={apply}/>);
 await screen.findByText("使用检查方案：金额要求 · v1");expect(screen.getByRole("button",{name:"应用到当前检查步骤"}).hasAttribute("disabled")).toBe(true);expect(apply).not.toHaveBeenCalled();
});
it("检查详情直接关联原版本整改和数据流，保留空间与节点",async()=>{
 const failed={...run,state:"failed" as const,error_code:"GOVERNANCE_QUALITY_BLOCKED"};show(<GovernanceQualityRunSummary run={failed}/>);
 expect(screen.getByText("本次存在可整改的质量异常")).toBeTruthy();await userEvent.click(screen.getByRole("button",{name:"返回数据流检查与清洗"}));
 expect(location.search).toContain("definition_id=flow");expect(location.search).toContain("version=3");expect(location.search).toContain("node_id=check");
 await userEvent.click(screen.getByRole("button",{name:"登记质量问题"}));expect(location.pathname+location.search).toBe("/platform/governance/issues?action=create&run_id=run&workspace_id=space");
});
it("从异常运行登记只填写责任人与期限，错误运行不可提交",async()=>{
 const create=vi.fn();vi.stubGlobal("fetch",vi.fn(async()=>Response.json({...run,state:"failed",error_code:"GOVERNANCE_TIMEOUT"})));
 show(<GovernanceQualityIssueCreate runID="run" members={[]} busy={false} readOnly={false} onClose={()=>undefined} onCreate={create}/>);
 expect(await screen.findByText("此运行没有可登记的质量异常")).toBeTruthy();expect(screen.getByRole("button",{name:"登记问题"}).hasAttribute("disabled")).toBe(true);expect(screen.queryByLabelText("问题运行")).toBeNull();expect(create).not.toHaveBeenCalled();
});
it("整改读取无权限错误可见且不能登记",async()=>{
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({code:"FORBIDDEN",message:"无权读取此运行"},{status:403})));
 show(<GovernanceQualityIssueCreate runID="run" members={[]} busy={false} readOnly={false} onClose={()=>undefined} onCreate={vi.fn()}/>);
 await screen.findByText("无权读取此运行");expect(screen.getByRole("button",{name:"登记问题"}).hasAttribute("disabled")).toBe(true);
});
it("无监测或落标异常的剖析运行不能登记整改",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path.includes("issue-context")?{eligible:false,origin_kind:"standard",summary:"",existing_issue_id:""}:{id:"profile",kind:"profile",state:"succeeded",definition:{mode:"full"},result:null})));
 show(<GovernanceQualityIssueCreate runID="profile" members={[]} busy={false} readOnly={false} onClose={()=>undefined} onCreate={vi.fn()}/>);
 expect(await screen.findByText("此运行没有可登记的完整检查异常")).toBeTruthy();expect(screen.getByRole("button",{name:"登记问题"}).hasAttribute("disabled")).toBe(true);
});
it("检查记录误带工作流运行时提示选择数据流运行",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path.includes("/events")?{items:[],total:0}:{id:"workflow",kind:"workflow",state:"succeeded",definition:{workflow:{id:"work",version:1}},result:{}})));
 show(<GovernanceFlowRunView id="workflow" initialTab="summary"/>);expect(await screen.findByText("此记录不是数据流运行，请从检查记录选择数据流")).toBeTruthy();
});
it("从异常运行打开整改表单后，提交仍携带原运行且在同层查看问题",async()=>{
 history.replaceState(null,"","/platform/governance/issues?action=create&run_id=run&workspace_id=space");const posts:{path:string;body:unknown}[]=[];
 const issue={id:"issue",state:"open",version:1,run_id:"run",flow_id:"flow",flow_version:3,department_id:"dept",assignee_id:"owner",due_at:"2030-01-01T01:00:00Z",events:[]};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return Response.json(issue)}return Response.json(path.includes("/runs/run")?{...run,state:"failed",error_code:"GOVERNANCE_QUALITY_BLOCKED"}:path.includes("/members")?{items:[{user_id:"owner",username:"治理人员",department_id:"dept"}]}:path.includes("/quality-issues/issue")?issue:{items:[],total:0})}));
 show(<GovernanceQualityIssues/>);await screen.findByText("商品整理 · v3");const dialog=screen.getByRole("dialog");expect(within(dialog).queryByLabelText("问题运行")).toBeNull();
 await userEvent.click(within(dialog).getByLabelText("整改责任人"));await userEvent.click(await screen.findByText("治理人员",{selector:".ant-select-item-option-content"}));
 await userEvent.type(within(dialog).getByLabelText("整改期限（本地时间）"),"2030-01-01 09:00{Enter}");await userEvent.click(within(dialog).getByRole("button",{name:"登记问题"}));
 await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0]).toEqual({path:"/api/v1/governance/quality-issues?workspace_id=space",body:{run_id:"run",assignee_id:"owner",due_at:new Date("2030-01-01T09:00").toISOString()}});await screen.findByText("处理历史");expect(screen.getAllByRole("dialog")).toHaveLength(1);
});

it("完整标准违规可登记统一整改，按钮受服务端证据核对控制",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path.includes("issue-context")?{eligible:true,origin_kind:"standard",summary:"完整标准核验存在违规记录",existing_issue_id:""}:{id:"profile",kind:"profile",department_id:"dept",state:"succeeded",definition:{mode:"full"},result:null})));
 show(<GovernanceQualityIssueCreate runID="profile" members={[{user_id:"owner",username:"治理人员",department_id:"dept"}]} busy={false} readOnly={false} onClose={()=>undefined} onCreate={vi.fn()}/>);
 expect(await screen.findByText("完整标准核验存在违规记录")).toBeTruthy();expect(screen.getByRole("button",{name:"登记问题"}).hasAttribute("disabled")).toBe(false);
});

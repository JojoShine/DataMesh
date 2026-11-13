// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor,within} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {GovernanceWorkflows} from "../src/pages/GovernanceWorkflows";
import {GovernanceLifecycle} from "../src/pages/GovernanceLifecycle";
import {useSession} from "../src/stores/session";
vi.setConfig({testTimeout:20000});
beforeEach(()=>{useSession.setState({user:{user_id:"owner",username:"治理人员",department_id:"dept",roles:["governor"],platforms:["governance"],csrf_token:"test"},ready:true,epoch:0,identityError:null});Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
function show(child:React.ReactNode){render(<ConfigProvider theme={{token:{motion:false}}}><AntApp>{child}</AntApp></ConfigProvider>)}
const workflows=[{id:"first",department_id:"dept",version:1,state:"published",definition:{name:"首个工作流"}},{id:"second",department_id:"dept",version:2,state:"published",definition:{name:"第二工作流"}}];
it("调度保留参数输入，切换工作流清除旧覆盖值，指南在同层弹窗内可读",async()=>{
 const posts:{path:string;body:unknown}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return Response.json({id:"schedule"})}return Response.json(path.includes("/schedules/preview?")?{items:Array.from({length:5},(_,i)=>`2030-01-01T0${i+1}:00:00Z`),timezone:"Asia/Shanghai"}:path.endsWith("/parameters")?{defaults:{day:path.includes("first")?"2026-10-21":"2026-10-22"}}:path.startsWith("/api/v1/governance/workflows?")?{items:workflows,total:2}:path.includes("/versions/2")?workflows[1]:path.includes("/versions/1")?workflows[0]:{items:[],total:0})}));
 show(<GovernanceWorkflows/>);await userEvent.click(screen.getByRole("tab",{name:"调度计划"}));await userEvent.click(screen.getByRole("button",{name:"新建调度"}));
 const dialog=screen.getByRole("dialog");await userEvent.click(within(dialog).getByText("使用指南 · 固定间隔与日历调度"));expect(within(dialog).getByText(/先发布并手动验证批量数据流或工作流/)).toBeTruthy();
 await userEvent.click(within(dialog).getByLabelText("工作流版本"));await userEvent.click(await screen.findByText("首个工作流 · v1",{selector:".ant-select-item-option-content"}));
 const parameter=await screen.findByLabelText("运行参数 day");await userEvent.clear(parameter);await userEvent.type(parameter,"old-value");
 await userEvent.click(within(dialog).getByLabelText("工作流版本"));await userEvent.click(await screen.findByText("第二工作流 · v2",{selector:".ant-select-item-option-content"}));
 await waitFor(()=>expect((screen.getByLabelText("运行参数 day") as HTMLInputElement).value).toBe("2026-10-22"));await userEvent.clear(screen.getByLabelText("运行参数 day"));await userEvent.type(screen.getByLabelText("运行参数 day"),"2026-11-01");
 await userEvent.type(within(dialog).getByLabelText("首次触发（所选时区）"),"2030-01-01 09:00{Enter}");expect(screen.getAllByRole("dialog")).toHaveLength(1);
 await waitFor(()=>expect(within(dialog).getByRole("button",{name:"保存并启用调度"}).hasAttribute("disabled")).toBe(false));await userEvent.click(within(dialog).getByRole("button",{name:"保存并启用调度"}));await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0]).toMatchObject({path:"/api/v1/governance/schedules",body:{workflow:{id:"second",version:2},parameters:{day:"2026-11-01"},interval_seconds:0,cron:"0 * * * *",concurrency_policy:"forbid",missed_policy:"skip"}});
});
it("从成果行发起迁移自动带入原成果，按名称选择新成果后提交编号",async()=>{
 const outputs=[{id:"old",name:"原始治理成果",flow_id:"flow",state:"draft",created_at:"2026-10-21T00:00:00Z"},{id:"new",name:"替代治理成果",flow_id:"flow",state:"draft",created_at:"2026-10-22T00:00:00Z"}];const posts:unknown[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST"){posts.push(JSON.parse(String(init.body)));return Response.json({id:"plan",state:"open"})}return Response.json({items:path.includes("/lifecycle?")?outputs:[],total:path.includes("/lifecycle?")?2:0})}));
 show(<GovernanceLifecycle/>);await screen.findByRole("button",{name:"原始治理成果"});await userEvent.click(screen.getAllByRole("button",{name:"迁移此成果"})[0]);const dialog=screen.getByRole("dialog");
 await userEvent.click(within(dialog).getByText("使用指南 · 下游成果迁移"));expect(within(dialog).getByText(/审批和授权不会自动复制/)).toBeTruthy();expect(within(dialog).getByRole("link",{name:"发布迁移草稿"}).getAttribute("target")).toBe("_blank");
 await userEvent.click(within(dialog).getByLabelText("新成果编号"));await userEvent.click(await screen.findByText("替代治理成果 · new",{selector:".ant-select-item-option-content"}));await userEvent.click(within(dialog).getByRole("button",{name:"核验并登记"}));await waitFor(()=>expect(posts).toEqual([{source_id:"old",target_id:"new"}]));expect(screen.queryByRole("dialog")).toBeNull();
});
it("质量规则先在Dialog核对，确认后才提交发布",async()=>{
 const {Rules}=await import("../src/pages/Governance");const posts:string[]=[];const rule={rule_id:"rule",version:1,state:"draft",definition:{name:"客户非空规则",object_id:"source",checks:[{kind:"required",field:"id",blocking:true,parameters:{}}]}};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST"){posts.push(path);return Response.json({...rule,state:"published"})}return Response.json({items:path.startsWith("/api/v1/rules?")?[rule]:[],total:1})}));
 show(<Rules/>);await userEvent.click(await screen.findByRole("button",{name:/^发\s*布$/}));expect(posts).toHaveLength(0);expect(screen.getAllByRole("dialog")).toHaveLength(1);await userEvent.click(within(screen.getByRole("dialog")).getByRole("button",{name:"确认发布规则"}));await waitFor(()=>expect(posts).toEqual(["/api/v1/rules/rule/publish"]));
});
it("成果检查先打开结果Dialog，再由Dialog内操作执行",async()=>{
 const {QualityPage}=await import("../src/pages/Governance");const posts:string[]=[];const dataset={dataset_id:"result",governance_run_id:"run",definition:{name:"客户成果"},quality_status:"passed",completeness_status:"passed",publication_status:"draft"};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST"){posts.push(path);return Response.json({})}return Response.json(path.endsWith("/quality")?{status:"passed",failure_reasons:[],checked_range:{},passed_count:1,failed_count:0,rule_version:1}:{items:[dataset],total:1})}));
 show(<QualityPage/>);await userEvent.click(await screen.findByRole("button",{name:"运行检查"}));expect(posts).toHaveLength(0);expect(screen.getAllByRole("dialog")).toHaveLength(1);await userEvent.click(within(screen.getByRole("dialog")).getByRole("button",{name:"运行检查"}));await waitFor(()=>expect(posts).toEqual(["/api/v1/datasets/result/check"]));
});
it("调度状态只能在管理Dialog中变更，并使用返回的新版本继续操作",async()=>{
 const posts:unknown[]=[];let schedule={id:"schedule",workflow:{id:"first",version:1},state:"active",version:3,timezone:"Asia/Shanghai",next_run_at:"2030-01-01T01:00:00Z",interval_seconds:3600,parameters:{day:"2026-11-01"}};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});schedule={...schedule,state:path.endsWith("/pause")?"paused":"active",version:schedule.version+1};return Response.json(schedule)}return Response.json({items:path.startsWith("/api/v1/governance/schedules?")?[schedule]:[],total:1})}));
 show(<GovernanceWorkflows/>);await userEvent.click(screen.getByRole("tab",{name:"调度计划"}));await userEvent.click(await screen.findByRole("button",{name:"管理调度"}));expect(posts).toHaveLength(0);const dialog=screen.getByRole("dialog");expect(within(dialog).getByText("day = 2026-11-01")).toBeTruthy();await userEvent.click(within(dialog).getByRole("button",{name:"暂停调度"}));await userEvent.click(await within(dialog).findByRole("button",{name:"启用调度"}));await waitFor(()=>expect(posts).toEqual([{path:"/api/v1/governance/schedules/schedule/pause",body:{base_version:3}},{path:"/api/v1/governance/schedules/schedule/resume",body:{base_version:4}}]));expect(screen.getAllByRole("dialog")).toHaveLength(1);
});
it("普通成果发布指南与工作流审批分开，发布只能在成果Dialog确认",async()=>{
 const {Catalog}=await import("../src/pages/Catalog");useSession.setState({user:{user_id:"owner",username:"管理员",department_id:"dept",roles:["admin"],platforms:["assets","governance"],csrf_token:"test"}});
 const posts:string[]=[];let dataset={dataset_id:"result",contract_version:1,provider_department_id:"dept",governance_run_id:"run",definition:{name:"待发布治理成果",owner_id:"owner",description:"已核验成果",usage_restrictions:"按授权使用",fields:[{name:"id",logical_type:"integer",sensitive:false}]},publication_status:"draft",quality_status:"passed",completeness_status:"complete"};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST"){posts.push(path);dataset={...dataset,publication_status:"published"};return Response.json(dataset)}return Response.json(path==="/api/v1/datasets/result"?dataset:{items:path.startsWith("/api/v1/datasets?")?[dataset]:[],total:1})}));
 show(<Catalog publications/>);expect(screen.getByText("使用指南 · 治理成果核验与发布")).toBeTruthy();await userEvent.click(await screen.findByRole("button",{name:/^发\s*布$/}));expect(posts).toHaveLength(0);const dialog=screen.getByRole("dialog");await userEvent.click(within(dialog).getByText("使用指南 · 治理成果核验与发布"));expect(within(dialog).getByText(/只有工作流任务处于待发布审批/)).toBeTruthy();expect(screen.getAllByRole("dialog")).toHaveLength(1);await userEvent.click(within(dialog).getByRole("button",{name:"核验并发布"}));await waitFor(()=>expect(posts).toEqual(["/api/v1/datasets/result/publish"]));await waitFor(()=>expect(screen.queryByRole("dialog")).toBeNull());
});
it("工作流定时入口保留固定目标的默认参数及覆盖值",async()=>{
 const {GovernanceSchedules}=await import("../src/components/GovernanceSchedules");
 history.replaceState(null,"","/platform/governance/schedules?target_kind=workflow&target_id=first&target_version=1");
 const posts:{path:string;body:unknown}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return Response.json({id:"schedule"})}
  return Response.json(path.includes("/schedules/preview?")?{items:Array.from({length:5},(_,i)=>`2030-01-01T0${i+1}:00:00Z`),timezone:"Asia/Shanghai"}:path.endsWith("/parameters")?{defaults:{day:"2026-10-27"}}:path.includes("/workflows/first/versions/1")?{...workflows[0],kind:"workflow"}:{items:[],total:0});
 }));show(<GovernanceSchedules/>);
 await waitFor(()=>expect(globalThis.fetch).toHaveBeenCalledWith(expect.stringContaining("/workflows/first/versions/1"),expect.anything()));
 await userEvent.click(screen.getByRole("button",{name:"新建调度"}));
 const dialog=screen.getByRole("dialog");const input=await within(dialog).findByLabelText("运行参数 day");
 expect((input as HTMLInputElement).value).toBe("2026-10-27");
 await userEvent.clear(input);await userEvent.type(input,"2026-10-28");
 await userEvent.type(within(dialog).getByLabelText("首次触发（所选时区）"),"2030-01-01 09:00{Enter}");
 await waitFor(()=>expect(within(dialog).getByRole("button",{name:"保存并启用调度"}).hasAttribute("disabled")).toBe(false));await userEvent.click(within(dialog).getByRole("button",{name:"保存并启用调度"}));
 await waitFor(()=>expect(posts).toHaveLength(1));
 expect(posts[0]).toMatchObject({body:{workflow:{id:"first",version:1},parameters:{day:"2026-10-28"}}});
 history.replaceState(null,"","/");
});

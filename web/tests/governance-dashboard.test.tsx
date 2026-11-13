// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor,within} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {GovernanceDashboard} from "../src/pages/GovernanceDashboard";
import type {GovernanceDashboardReport} from "../src/api/governanceOperations";
import {useSession} from "../src/stores/session";

const report:GovernanceDashboardReport={capabilities:{monitors:0,open_observations:0,audits:0,audit_violations:0,master_conflicts:0,confirmed_entities:0,metrics:0,scenarios:0,pending_releases:0},days:30,timezone:"Asia/Shanghai",scope:"visible",started_at:"2026-09-22T16:00:00Z",observed_at:"2026-10-22T03:00:00Z",definitions:[],resources:[],observations:0,rules:[],outputs:[],runs:[],run_trend:[],quality_trend:[],schedules:[],issues:[],overdue_issues:0,alerts:[],alert_rules:0,retention_policies:0,purged_snapshots:0,lifecycle_outputs:0,migrations:[],model_deployments:[],lineage_runs:0,quality_runs:0,flow_modes:[],streams:[],schedule_targets:[],lightweight:{units:0,files:0,runs:[],checked:0,passed:0,rejected:0}};
beforeEach(()=>{localStorage.setItem("datamesh:governance-learning:v1:owner",JSON.stringify({seen:true,completed:[],steps:{home:0,flow:0,result:0}}));useSession.setState({user:{user_id:"owner",username:"治理人员",department_id:"dept",roles:["governor"],platforms:["governance"],csrf_token:"test"},ready:true,epoch:0,identityError:null});Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals();localStorage.clear()});
function show(){render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><GovernanceDashboard/></AntApp></ConfigProvider>)}
it("无运行和无检查时不伪造成功率，保留所有业务分类和可读图表",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path.includes("/dashboard?")?report:{recent_runs:[]})));
 show();await screen.findByRole("heading",{name:"治理运行趋势"});expect(screen.getAllByText("—")).toHaveLength(2);
 expect(screen.getByRole("img",{name:/治理运行趋势，按创建日期/})).toBeTruthy();expect(screen.getByRole("img",{name:/质量检查、合格和隔离行次/})).toBeTruthy();
 expect(screen.getByText("本期暂无运行记录")).toBeTruthy();expect(screen.getByText("本期暂无质量节点检查记录")).toBeTruthy();expect(document.querySelector("svg")?.outerHTML).not.toContain("NaN");
 for(const name of ["准备数据","处理数据","检查与交付","标准与规范","监控与追溯"])expect(screen.getByRole("heading",{name})).toBeTruthy();
});
it("成功率排除未结束及取消运行，切换周期重新请求真实统计，口径说明为单层dialog",async()=>{
 const requests:string[]=[];const data={...report,runs:[{kind:"dataflow",state:"succeeded",count:8},{kind:"dataflow",state:"failed",count:2},{kind:"workflow",state:"running",count:3},{kind:"profile",state:"cancelled",count:4}],quality_trend:[{day:"2026-10-22",checked:1000,passed:980,rejected:20,runs:2}]};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>{requests.push(path);return Response.json(path.includes("/dashboard?")?{...data,days:Number(new URL(path,"http://localhost").searchParams.get("days"))}:{recent_runs:[]})}));
 show();await screen.findByText("80.0%");expect(screen.getByText("98.0%")).toBeTruthy();
 await userEvent.click(screen.getByRole("combobox",{name:"看板统计周期"}));await userEvent.click(screen.getByText("最近7天",{selector:".ant-select-item-option-content"}));await waitFor(()=>expect(requests.some(path=>path.includes("days=7&"))).toBe(true));
 await userEvent.click(screen.getByRole("button",{name:"指标说明"}));expect(screen.getAllByRole("dialog")).toHaveLength(1);const dialog=screen.getByRole("dialog");expect(within(dialog).getByText(/排队、运行中、取消不进入分母/)).toBeTruthy();expect(within(dialog.querySelector(".ant-modal-footer") as HTMLElement).getByRole("button",{name:"返回工作台"})).toBeTruthy();
});
it("聚合接口失败时不展示零值看板",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>path.includes("/dashboard?")?Response.json({code:"UNAVAILABLE",message:"统计暂时不可用"},{status:503}):Response.json({recent_runs:[]})));
 show();await screen.findByText("统计暂时不可用");expect(screen.queryByRole("heading",{name:"治理运行趋势"})).toBeNull();expect(document.querySelector(".governance-kpi")).toBeNull();
});

it("当前业务菜单指标区分私人治理、批流和检查次数",async()=>{
 const data={...report,quality_runs:7,flow_modes:[{kind:"batch",count:2},{kind:"continuous",count:1}],streams:[{state:"running",count:1}],schedule_targets:[{kind:"dataflow",state:"active",count:2}],lightweight:{units:3,files:4,runs:[{state:"succeeded",count:9}],checked:20,passed:18,rejected:2}};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path.includes("/dashboard?")?data:{recent_runs:[]})));
 show();await screen.findByRole("heading",{name:"处理数据"});
 expect(screen.getByText(/仅本人 · 跨空间 · 4份 CSV/)).toBeTruthy();expect(screen.getByText(/批处理2份，持续更新1份/)).toBeTruthy();expect(screen.getByText(/数据流2项，工作流0项/)).toBeTruthy();
 expect(screen.getByText(/不等于通过成果数/)).toBeTruthy();expect(screen.queryByText("质量规则 / 模板")).toBeNull();
 const metric=screen.getByText("质量管理").closest(".governance-module-metric");expect(metric?.querySelector("strong")?.textContent).toBe("7");
});

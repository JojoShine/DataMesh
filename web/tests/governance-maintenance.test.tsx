// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {GovernanceAlerts} from "../src/pages/GovernanceAlerts";
import {GovernanceLifecycle} from "../src/pages/GovernanceLifecycle";
import {useSession} from "../src/stores/session";
vi.setConfig({testTimeout:15000});
beforeEach(()=>{useSession.setState({user:{user_id:"owner",username:"治理人员",department_id:"dept",roles:["governor"],platforms:["governance"],csrf_token:"test"},ready:true,epoch:0,identityError:null});Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
function show(child:React.ReactNode){render(<ConfigProvider theme={{token:{motion:false}}}><AntApp>{child}</AntApp></ConfigProvider>)}
it("告警配置使用单层Dialog并默认关闭，保存明确的阈值和版本",async()=>{
 const posts:{path:string;body:unknown}[]=[];vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return Response.json({id:"rule",version:1})}return Response.json({items:[],total:0,page:1,page_size:20})}));
 show(<GovernanceAlerts/>);await userEvent.click(screen.getByRole("button",{name:"新增告警规则"}));expect(screen.getAllByRole("dialog")).toHaveLength(1);expect(screen.getByRole("switch",{name:"启用规则"}).getAttribute("aria-checked")).toBe("false");await userEvent.type(screen.getByLabelText("规则名称"),"日常失败检查");await userEvent.click(screen.getByRole("button",{name:"保存规则"}));await waitFor(()=>expect(posts).toEqual([{path:"/api/v1/governance/alert-rules",body:{name:"日常失败检查",enabled:false,failure:true,quality_rate:0,webhook:"",expected_version:0}}]));
});
it("有真实依赖阻断时，清理Dialog显示原因且不提供清理操作",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path.endsWith("/preview")?{dataset_id:"result",rows:10,blockers:["DOWNSTREAM_IN_USE"],dependencies:[{kind:"run",id:"child",version:1,state:"succeeded"}],restricted_dependencies:0,token:"proof"}:path.includes("lifecycle?")?{items:[{id:"result",name:"保留成果",state:"draft",flow_id:"flow",created_at:"2026-10-21T00:00:00Z"}],total:1,page:1,page_size:20}:{items:[]})));
 show(<GovernanceLifecycle/>);await userEvent.click(await screen.findByRole("button",{name:"保留成果"}));expect(await screen.findByText("存在下游引用")).toBeTruthy();expect(screen.getAllByRole("dialog")).toHaveLength(1);expect(screen.getByText("child")).toBeTruthy();expect(screen.queryByRole("button",{name:"确认清理快照"})).toBeNull();
});
it("新保留策略在Dialog中默认关闭并保留最新份数",async()=>{
 const posts:unknown[]=[];vi.stubGlobal("fetch",vi.fn(async(_path:string,init?:RequestInit)=>{if(init?.method==="POST"){posts.push(JSON.parse(String(init.body)));return Response.json({id:"policy",version:1})}return Response.json(_path.includes("/dataflows?")?{items:[{id:"flow",definition:{name:"日常治理"}}],total:1,page:1,page_size:20}:{items:[],total:0,page:1,page_size:20})}));show(<GovernanceLifecycle/>);await userEvent.click(screen.getByRole("button",{name:"新增保留策略"}));expect(screen.getAllByRole("dialog")).toHaveLength(1);expect(screen.getByRole("switch",{name:"自动清理"}).getAttribute("aria-checked")).toBe("false");await userEvent.click(screen.getByLabelText("数据流编号"));await userEvent.click(await screen.findByText("日常治理 · flow",{selector:".ant-select-item-option-content"}));await userEvent.click(screen.getByRole("button",{name:"保存策略"}));await waitFor(()=>expect(posts).toEqual([{flow_id:"flow",days:30,keep_last:3,enabled:false,expected_version:0,confirmation:""}]));
});

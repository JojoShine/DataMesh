// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor,within} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {GovernanceResults} from "../src/pages/GovernanceResults";
import {useSession} from "../src/stores/session";

vi.setConfig({testTimeout:15000});
const dataset={dataset_id:"result",contract_version:1,governance_run_id:"run",provider_department_id:"dept",publication_status:"draft",quality_status:"passed",completeness_status:"complete",definition:{name:"客户治理成果",description:"实际执行成果",owner_id:"owner",source_object_id:"",usage_restrictions:"按业务授权使用",fields:[{name:"id",logical_type:"integer",primary_key:true,nullable:false,sensitive:false,precision:null,scale:null}]}};
const run={id:"run",kind:"dataflow",state:"succeeded",processed:1000,definition:{flow:{id:"flow",version:2},pinned:[],inputs:{}},result:{dataset_id:"result",model:{id:"model",version:2},output_rows:1000,rejected_rows:0,nodes:[],lineage:{}},finished_at:"2026-10-22T02:00:00Z"};
const historical={dataset_id:"result",contract_version:1,rule_version:2,status:"passed",passed_count:1000,failed_count:0,checked_range:{},failure_reasons:[]};
beforeEach(()=>{window.history.replaceState(null,"","/platform/governance/results");localStorage.clear();Object.defineProperty(Element.prototype,"scrollIntoView",{configurable:true,value:vi.fn()});useSession.setState({user:{user_id:"owner",username:"治理人员",department_id:"dept",roles:["governor"],platforms:["governance"],csrf_token:"test"},ready:true,epoch:0,identityError:null});Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();Reflect.deleteProperty(Element.prototype,"scrollIntoView");vi.unstubAllGlobals()});
function fixture(posts:string[]){vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
 if(init?.method==="POST"){posts.push(path);return Response.json({code:"GOVERNANCE_OUTPUT_STALE",message:"治理成果已超过模型鲜度期限",trace_id:"test-trace"},{status:409})}
 return Response.json(path==="/api/v1/governance/datasets/result"?dataset:path==="/api/v1/governance/runs/run"?run:path.endsWith("/quality")?historical:path.startsWith("/api/v1/governance/datasets?")?{items:[dataset],total:1,page:1,page_size:20}:{items:[],total:0});
}))}
function show(){render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><GovernanceResults/></AntApp></ConfigProvider>)}
it("历史通过与当前鲜度阻断分开，教学副本不会提交业务写入或扩大发布权限",async()=>{
 const posts:string[]=[];fixture(posts);show();await userEvent.click(await screen.findByRole("button",{name:"打开成果核验"}));
 const dialog=screen.getByRole("dialog");expect(within(dialog).getByRole("button",{name:"核验并发布"}).hasAttribute("disabled")).toBe(true);
 expect(screen.getAllByText("待当前核验").length).toBeGreaterThan(0);await userEvent.click(within(dialog).getByRole("button",{name:"重新核验条件"}));
 await screen.findByText(/这份数据已超过约定的使用时间/);expect(screen.queryByText(/test-trace|模型鲜度期限/)).toBeNull();expect(screen.getAllByText("核验受阻").length).toBeGreaterThan(0);
 await userEvent.click(screen.getByRole("button",{name:"学习本页"}));expect(screen.getAllByRole("dialog")).toHaveLength(1);await userEvent.click(screen.getByRole("tab",{name:"数据流设计"}));
 await userEvent.clear(screen.getByPlaceholderText("请输入教学任务名称"));await userEvent.type(screen.getByPlaceholderText("请输入教学任务名称"),"教学改名不提交");await userEvent.click(screen.getByRole("button",{name:"暂停学习并返回"}));
 expect(screen.getAllByRole("dialog")).toHaveLength(1);expect(screen.getAllByText("核验受阻").length).toBeGreaterThan(0);expect(screen.getByRole("button",{name:"核验并发布"}).hasAttribute("disabled")).toBe(true);expect(posts).toEqual(["/api/v1/datasets/result/check"]);
});
it("发布仍调用真实服务端门禁，失败不会把历史通过变成发布成功",async()=>{
 useSession.setState({user:{user_id:"owner",username:"管理员",department_id:"dept",roles:["admin"],platforms:["governance","assets"],csrf_token:"test"}});
 const posts:string[]=[];fixture(posts);show();await userEvent.click(await screen.findByRole("button",{name:"打开成果核验"}));
 await userEvent.click(screen.getByRole("button",{name:"核验并发布"}));await screen.findByText(/这份数据已超过约定的使用时间/);expect(screen.queryByText(/test-trace|模型鲜度期限/)).toBeNull();
 await waitFor(()=>expect(posts).toEqual(["/api/v1/datasets/result/publish"]));expect(screen.getAllByRole("dialog")).toHaveLength(1);expect(screen.getAllByText("核验受阻").length).toBeGreaterThan(0);expect(screen.getByRole("button",{name:"核验并发布"})).toBeTruthy();expect(screen.queryByText("成果已发布。")).toBeNull();
 const footer=screen.getByRole("dialog").querySelector(".ant-modal-footer")!;expect(within(footer as HTMLElement).getByRole("button",{name:"核验并发布"})).toBeTruthy();
});
it("单表成果默认回到原目录，也可以选择新建目录且显示实际治理内容",async()=>{
 useSession.setState({user:{user_id:"owner",username:"管理员",department_id:"dept",roles:["admin"],platforms:["governance","assets","exchange"],csrf_token:"test"}});
 const posts:string[]=[];fixture(posts);const original=globalThis.fetch;
 const governed={...dataset,publication_status:"published",governance:{single_source:true,source:{resource_type:"table_resource",resource_id:"original",contract_version:1},source_name:"客户原始数据",provider_department_id:"dept",original_directory_id:"original-directory",original_directory_name:"客户资料目录",steps:[{kind:"transform",name:"清理文字",description:"去除姓名首尾空白。"}]}};
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>path==="/api/v1/governance/datasets/result"?Promise.resolve(Response.json(governed)):original(path,init)));
 show();await userEvent.click(await screen.findByRole("button",{name:"打开成果核验"}));
 expect(await screen.findByText(/去除姓名首尾空白。/)).toBeTruthy();
 expect(screen.getByRole("radio",{name:/关联回原目录/})).toHaveProperty("checked",true);
 await userEvent.click(screen.getByRole("button",{name:"前往共享办理"}));
 expect(location.pathname+location.search).toBe("/platform/exchange/directory?view=manage&dataset_id=result&directory_id=original-directory&action=mount");
 await userEvent.click(screen.getByRole("radio",{name:/新建目录/}));await userEvent.click(screen.getByRole("button",{name:"前往共享办理"}));
 expect(location.pathname+location.search).toBe("/platform/exchange/directory?view=manage&dataset_id=result&action=create");expect(posts).toHaveLength(0);
});
it("成果空状态指向数据流并保留当前空间",async()=>{
 window.history.replaceState({},"","/platform/governance/results?workspace_id=space");
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({items:[],total:0})));
 show();await screen.findByText("当前空间尚无治理成果");
 expect(screen.getByRole("searchbox",{name:"搜索治理成果"})).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"前往数据流处理"}));
 expect(location.pathname+location.search).toBe("/platform/governance/dataflows?workspace_id=space");
});

it("搜索无结果可清空条件，不误导为从未产出成果",async()=>{
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({items:[],total:0})));show();
 const search=await screen.findByRole("searchbox",{name:"搜索治理成果"});
 await userEvent.type(search,"业务{Enter}");await screen.findByText("没有匹配的治理成果");
 expect(screen.queryByRole("button",{name:"前往数据流处理"})).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:"显示全部成果"}));
 expect(search).toHaveProperty("value","");await screen.findByText("当前空间尚无治理成果");
});

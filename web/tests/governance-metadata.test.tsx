// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor,within} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {MetadataWorkspace} from "../src/pages/GovernanceMetadata";
import {useSession} from "../src/stores/session";
vi.mock("../src/components/GovernanceWorkspaceContext",async importOriginal=>({...await importOriginal<typeof import("../src/components/GovernanceWorkspaceContext")>(),useGovernanceWorkspace:()=>({workspaceID:"space",workspace:null,helpOpen:true,navigation:vi.fn(),toggleHelp:vi.fn(),refreshWorkspace:vi.fn()})}));
vi.setConfig({testTimeout:15000});
const fields=[{name:"customer_id",logical_type:"integer",primary_key:true,nullable:false,sensitive:false,precision:null,scale:null}];
const object={object_id:"object",source_id:"source",qualified_name:"public.customers",schema_version:1,fields};
const observation={id:"observation",object_id:"object",version:2,source_id:"source",source_version:3,object_version:1,schema:"public",table:"customers",fields,changes:[],matches_active:true,observed_by:"governor",created_at:"2026-10-21T01:00:00Z"};
beforeEach(()=>{
 useSession.setState({user:{user_id:"governor",username:"治理人员",department_id:"test",roles:["governor"],platforms:["governance"],csrf_token:"test"},ready:true,epoch:0,identityError:null});
 Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});
 vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});
});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
function show(){render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><MetadataWorkspace/></AntApp></ConfigProvider>)}
function mock(posts:{path:string;body:unknown}[]){vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
 if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return new Response(JSON.stringify(path.endsWith("refresh")?observation:{id:"definition",kind:"metadata",version:1,state:"draft"}),{status:201})}
 return new Response(JSON.stringify(path.includes("/collection?")?{policy:null,items:[],total:0,pending_reviews:[],pending_total:0}:path.includes("/observations")?{items:[observation],total:1,page:1,page_size:20,object,object_version:1,source_version:3,department_id:"test"}:path.endsWith("members")?{items:[{user_id:"governor",username:"治理人员",department_id:"test"}]}:{items:path.startsWith("/api/v1/governance/metadata?")?[object]:[],total:path.startsWith("/api/v1/governance/metadata?")?1:0,page:1,page_size:20}));
}));}
it("刷新读取真实结构时提交来源和对象版本，不启动CDC或重置任务",async()=>{
 const posts:{path:string;body:unknown}[]=[];mock(posts);show();await userEvent.click(await screen.findByRole("button",{name:"public.customers"}));await userEvent.click(await screen.findByRole("button",{name:"检查最新结构"}));
 await waitFor(()=>expect(posts).toEqual([{path:"/api/v1/governance/metadata/object/refresh",body:{source_version:3,object_version:1}}]));
 expect(screen.getAllByRole("dialog")).toHaveLength(1);
});
it("业务注释绑定明确结构快照并在同一层弹窗保存",async()=>{
 const posts:{path:string;body:unknown}[]=[];mock(posts);show();await userEvent.click(await screen.findByRole("button",{name:"public.customers"}));await userEvent.click(screen.getByRole("tab",{name:"业务说明"}));await userEvent.click(await screen.findByRole("button",{name:"补充业务说明"}));
 await userEvent.type(screen.getByLabelText("中文名称待补充（customer_id） · 业务含义"),"客户唯一标识");await userEvent.click(screen.getByRole("button",{name:"保存说明草稿"}));
 await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0].path).toBe("/api/v1/governance/metadata-definitions");expect(posts[0].body).toMatchObject({base_version:0,definition:{owner_id:"governor",steward_id:"governor",metadata:{object_id:"object",observation_id:"observation",fields:[{name:"customer_id",description:"客户唯一标识"}]}}});
 expect(screen.getAllByRole("dialog")).toHaveLength(1);
});
it("统一目录中的文件展示登记契约，不声称已探测文件实时结构",async()=>{
 const file={...object,object_id:"file",qualified_name:"年度报告.pdf",resource_type:"file_resource",observation_kind:"registered_contract",adapter:"upload",source_id:"",fields:[]};
 const contract={...observation,object_id:"file",resource_type:"file_resource",observation_kind:"registered_contract",fields:[]};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify(path.includes("/observations")?{items:[contract],total:1,page:1,page_size:20,object:file,object_version:1,source_version:1,department_id:"test"}:path.startsWith("/api/v1/governance/metadata?")?{items:[file],total:1,page:1,page_size:20}:{items:[],total:0,page:1,page_size:20}))));
 show();await userEvent.click(await screen.findByRole("button",{name:"年度报告.pdf"}));
 expect(await screen.findByText("这里显示登记的字段结构")).toBeTruthy();
 expect(screen.getByText(/不读取文件内容.*不调用 API/)).toBeTruthy();
 expect(screen.getByRole("button",{name:"保存登记结构版本"})).toBeTruthy();
 expect(screen.queryByRole("button",{name:"检查最新结构"})).toBeNull();
});
it("采集设置复用单层弹窗，保存周期与版本，不修改资源契约",async()=>{
 const posts:{path:string;body:unknown}[]=[];
 const policy={object_id:"object",department_id:"test",actor_id:"governor",enabled:true,requested:false,interval_minutes:1440,version:4,next_at:"2026-10-23T01:00:00Z",last_attempt_at:null,last_success_at:null,failures:0,error_code:"",source_version:3,object_version:1,observation_id:"observation"};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return Response.json(policy)}
  return Response.json(path.includes("/collection?page=1")?{policy,items:[],total:0,page:1,page_size:20,pending_reviews:[]}:path.includes("/observations")?{items:[observation],total:1,page:1,page_size:20,object,object_version:1,source_version:3,department_id:"test"}:path.startsWith("/api/v1/governance/metadata?")?{items:[object],total:1,page:1,page_size:20}:{items:[],total:0});
 }));
 show();await userEvent.click(await screen.findByRole("button",{name:"public.customers"}));await userEvent.click(screen.getByRole("tab",{name:"更新与变化"}));await userEvent.click(await screen.findByRole("button",{name:"设置自动更新"}));
 expect(screen.getAllByRole("dialog")).toHaveLength(1);const interval=screen.getByRole("spinbutton",{name:/每隔多久检查/});await userEvent.clear(interval);await userEvent.type(interval,"30");await userEvent.click(screen.getByRole("button",{name:"保存更新设置"}));
 await waitFor(()=>expect(posts).toEqual([{path:"/api/v1/governance/metadata/object/collection",body:{expected_version:4,enabled:true,interval_minutes:30}}]));expect(screen.getAllByRole("dialog")).toHaveLength(1);
});
it("暂停计划仍可申请单次采集，提交时不隐式开启定时任务",async()=>{
 const posts:{path:string;body:unknown}[]=[];
 const policy={enabled:false,requested:false,interval_minutes:1440,version:2,next_at:"2026-10-23T01:00:00Z",last_success_at:null,failures:0};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return Response.json(policy,{status:202})}
  return Response.json(path.includes("/collection?")?{policy,items:[],total:0,pending_reviews:[]}:path.includes("/observations")?{items:[observation],total:1,object,object_version:1,source_version:3,department_id:"test"}:path.startsWith("/api/v1/governance/metadata?")?{items:[object],total:1}:{items:[],total:0});
 }));
 show();await userEvent.click(await screen.findByRole("button",{name:"public.customers"}));await userEvent.click(screen.getByRole("tab",{name:"更新与变化"}));await userEvent.click(await screen.findByRole("button",{name:"立即检查一次"}));
 await waitFor(()=>expect(posts).toEqual([{path:"/api/v1/governance/metadata/object/collect",body:{expected_version:2}}]));expect(screen.getAllByRole("dialog")).toHaveLength(1);
});
function footerButtons(){const footer=screen.getByRole("dialog").querySelector(".governance-dialog-footer");if(!(footer instanceof HTMLElement))throw new Error("缺少固定底栏");return within(footer).getAllByRole("button").map(button=>button.textContent)}
it("四个用途各有说明，底部只显示当前页操作，切换后不遗留其他页按钮",async()=>{
 mock([]);show();await userEvent.click(await screen.findByRole("button",{name:"public.customers"}));
 expect(screen.getAllByRole("tab").map(tab=>tab.textContent)).toEqual(["数据结构","业务说明","更新与变化","数据情况"]);
 expect(footerButtons()).toEqual(["返回列表","检查最新结构"]);
 await userEvent.click(screen.getByRole("tab",{name:"业务说明"}));
 expect(footerButtons()).toEqual(["返回列表","补充业务说明"]);expect(screen.getByText(/客户表 customer_id 是CRM客户编号/)).toBeTruthy();
 await userEvent.click(screen.getByRole("tab",{name:"数据情况"}));
 expect(footerButtons()).toEqual(["返回列表","检查数据情况"]);
 const rows=screen.getByRole("spinbutton",{name:"本次检查行数"});await userEvent.clear(rows);await userEvent.type(rows,"500");
 await userEvent.click(screen.getByRole("tab",{name:"更新与变化"}));
 await waitFor(()=>expect(footerButtons()).toEqual(["返回列表","设置自动更新","立即检查一次"]));
 await userEvent.click(screen.getByRole("tab",{name:"数据结构"}));expect(footerButtons()).toEqual(["返回列表","检查最新结构"]);
 await userEvent.click(screen.getByRole("tab",{name:"数据情况"}));expect(screen.getByRole("spinbutton",{name:"本次检查行数"})).toHaveProperty("value","500");
 expect(screen.getAllByRole("dialog")).toHaveLength(1);
});
it("自动更新设置在同一弹窗中编辑，切换页签不显示其他页命令且保留输入",async()=>{
 mock([]);show();await userEvent.click(await screen.findByRole("button",{name:"public.customers"}));await userEvent.click(screen.getByRole("tab",{name:"更新与变化"}));await userEvent.click(await screen.findByRole("button",{name:"设置自动更新"}));
 const interval=screen.getByRole("spinbutton",{name:/每隔多久检查/});await userEvent.clear(interval);await userEvent.type(interval,"60");expect(footerButtons()).toEqual(["返回列表","返回更新记录","保存更新设置"]);
 await userEvent.click(screen.getByRole("tab",{name:"业务说明"}));expect(footerButtons()).toEqual(["返回列表","补充业务说明"]);
 await userEvent.click(screen.getByRole("tab",{name:"更新与变化"}));expect(screen.getByRole("spinbutton",{name:/每隔多久检查/})).toHaveProperty("value","60");expect(screen.getAllByRole("dialog")).toHaveLength(1);
});
it("变化核查保留影响确认和处理说明，提交后返回当前更新页且不叠加弹窗",async()=>{
 const posts:{path:string;body:unknown}[]=[];
 const review={observation_id:"observation",object_id:"object",state:"pending",note:"",reviewed_by:null,reviewed_at:null,created_at:"2026-10-22T01:00:00Z"};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return Response.json({...review,state:"deferred"})}
  if(path.includes("/impact?"))return Response.json({root:"resource:object:1",nodes:[],edges:[],fields:null,transformations:null,truncated:false,restricted_dependencies:0});
  if(path.endsWith("/observations/observation"))return Response.json({...observation,changes:[{code:"field_added",field:"customer_level",breaking:false}]});
  if(path.includes("/collection?"))return Response.json({policy:null,items:[],total:0,pending_reviews:[review],pending_total:1});
  return Response.json(path.includes("/observations")?{items:[observation],total:1,object,object_version:1,source_version:3,department_id:"test"}:path.startsWith("/api/v1/governance/metadata?")?{items:[object],total:1}:{items:[],total:0});
 }));
 show();await userEvent.click(await screen.findByRole("button",{name:"public.customers"}));await userEvent.click(screen.getByRole("tab",{name:"更新与变化"}));await userEvent.click(await screen.findByRole("button",{name:/变化记录 ·/}));
 expect(screen.getAllByRole("dialog")).toHaveLength(1);await userEvent.type(screen.getByLabelText("处理说明"),"客户等级新增为可空字段，待销售部确认含义");
 await userEvent.click(screen.getByRole("checkbox",{name:/已核查当前可见的模型/}));await userEvent.click(screen.getByRole("button",{name:"保存处理意见"}));
 await waitFor(()=>expect(posts).toEqual([{path:"/api/v1/governance/metadata/object/review",body:{observation_id:"observation",state:"deferred",note:"客户等级新增为可空字段，待销售部确认含义",impact_checked:true}}]));
 await waitFor(()=>expect(footerButtons()).toEqual(["返回列表","设置自动更新","立即检查一次"]));
});

it("未关联来源时提示先维护来源，不展示全平台资源",async()=>{
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({items:[],total:0,page:1,page_size:20})));show();
 await screen.findByText("请先维护当前空间的来源数据");expect(screen.getByRole("button",{name:"选择来源数据"})).toBeTruthy();
 expect(vi.mocked(fetch).mock.calls.some(([path])=>String(path).includes("workspace_sources_only=true"))).toBe(true);
});

it("补充业务说明复用原弹窗和底栏，取消后回到业务说明",async()=>{
 const posts:{path:string;body:unknown}[]=[];mock(posts);show();
 await userEvent.click(await screen.findByRole("button",{name:"public.customers"}));
 await userEvent.click(screen.getByRole("tab",{name:"业务说明"}));const dialog=screen.getByRole("dialog");
 await userEvent.click(screen.getByRole("button",{name:"补充业务说明"}));
 expect(screen.getByRole("dialog")).toBe(dialog);expect(screen.getAllByRole("dialog")).toHaveLength(1);
 expect(footerButtons()).toEqual(["取消并返回业务说明","保存说明草稿"]);
 await userEvent.type(screen.getByLabelText("中文名称待补充（customer_id） · 业务含义"),"取消的输入");
 await userEvent.click(screen.getByRole("button",{name:"取消并返回业务说明"}));
 expect(screen.getByRole("dialog")).toBe(dialog);expect(screen.getByRole("tab",{name:"业务说明"}).getAttribute("aria-selected")).toBe("true");expect(posts).toHaveLength(0);
});

// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {GovernanceCatalog} from "../src/pages/GovernanceCatalog";
import {useSession} from "../src/stores/session";
vi.setConfig({testTimeout:15000});
beforeEach(()=>{
 useSession.setState({user:{user_id:"governor",username:"治理人员",department_id:"test",roles:["governor"],platforms:["governance"],csrf_token:"test"},ready:true,epoch:0,identityError:null});
 Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});
 vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});
});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
function show(kind:"domain"|"model"|"standard"){render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><GovernanceCatalog kind={kind}/></AntApp></ConfigProvider>)}
it("主题域在居中弹窗建立，保存真实责任人与说明，无嵌套弹窗",async()=>{
 const bodies:unknown[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){bodies.push(JSON.parse(String(init.body)));return new Response(JSON.stringify({id:"domain",version:1,state:"draft"}),{status:201})}
  return new Response(JSON.stringify({items:path.endsWith("members")?[{user_id:"governor",username:"治理人员",department_id:"test"}]:[],total:0}));
 }));
 show("domain");await userEvent.click(await screen.findByRole("button",{name:"新建主题域"}));
 expect(screen.getAllByRole("dialog")).toHaveLength(1);await userEvent.type(screen.getByLabelText("名称"),"客户域");await userEvent.type(screen.getByLabelText("业务说明"),"客户治理责任");
 await userEvent.click(screen.getAllByRole("button",{name:"保存草稿"})[0]);
 await waitFor(()=>expect(bodies).toEqual([{definition:{name:"客户域",description:"客户治理责任",owner_id:"governor",steward_id:"governor",domain_scope:"department"},change_note:"首次建立",base_version:0}]));
 expect(screen.queryByRole("dialog")).toBeNull();
});
it("从已发布模型创建新版本，提交基准版本，不覆盖原版",async()=>{
 const definition={name:"客户模型",description:"",owner_id:"governor",steward_id:"governor",model:{layer:"logical",schema:"existing_schema",table:"existing_table",fields:[{name:"id",description:"",logical_type:"integer",length:0,precision:0,scale:0,nullable:false,primary_key:true,sensitive:false}],relations:[],contract:{freshness_seconds:3600,max_failure_rate:0,usage_restrictions:""}}};
 const record={id:"model",kind:"model",department_id:"test",latest_version:3,version:3,state:"published",definition,change_note:"v3",migration_note:"",created_at:"2026-10-21T01:00:00Z",published_at:"2026-10-21T01:00:00Z"};
 const posts:{path:string;body:unknown}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return new Response(JSON.stringify({...record,version:4,state:"draft"}),{status:201})}
  return new Response(JSON.stringify(path.endsWith("members")?{items:[{user_id:"governor",username:"治理人员",department_id:"test"}]}:{items:path.startsWith("/api/v1/governance/models?")?[record]:[],total:path.startsWith("/api/v1/governance/models?")?1:0}));
 }));
 show("model");await userEvent.click(await screen.findByRole("button",{name:"客户模型"}));await userEvent.click(screen.getByRole("button",{name:"创建新版本"}));
 expect(screen.getAllByRole("dialog")).toHaveLength(1);expect(screen.queryByLabelText("目标库 / Schema")).toBeNull();await userEvent.clear(screen.getByLabelText("名称"));await userEvent.type(screen.getByLabelText("名称"),"客户模型修订");await userEvent.click(screen.getByLabelText("业务用途"));await userEvent.click(await screen.findByText("整理统一的业务明细",{selector:".ant-select-item-option-content"}));await userEvent.click(screen.getAllByRole("button",{name:"保存草稿"})[0]);
 await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0].path).toBe("/api/v1/governance/models/model/versions");expect(posts[0].body).toEqual({base_version:3,definition:{...definition,name:"客户模型修订",model:{...definition.model,data_layer:"dwd"}},change_note:"基于 v3 修订"});
 expect(screen.queryByRole("dialog")).toBeNull();
});
it("物理模型选择主键后自动设置非空，避免保存矛盾约束",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify({items:path.endsWith("members")?[{user_id:"governor",username:"治理人员",department_id:"test"}]:[],total:0}))));
 show("model");await userEvent.click(await screen.findByRole("button",{name:"新建数据模型"}));await userEvent.click(screen.getByRole("button",{name:"添加字段"}));
 expect((screen.getByLabelText("可空") as HTMLInputElement).checked).toBe(true);
 await userEvent.click(screen.getByLabelText("主键"));
 await waitFor(()=>expect((screen.getByLabelText("可空") as HTMLInputElement).checked).toBe(false));
 expect(screen.getByLabelText("可空").hasAttribute("disabled")).toBe(true);
});
it("企业主题域只读展示，部门可在同一层弹窗登记本部门责任",async()=>{
 const enterprise={id:"enterprise",kind:"domain",department_id:"head-office",latest_version:1,version:1,state:"published",definition:{name:"统一客户",description:"企业客户业务边界",owner_id:"central-admin",steward_id:"central-admin",domain_scope:"enterprise"},created_at:"2026-10-22T00:00:00Z"};
 const posts:unknown[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push(JSON.parse(String(init.body)));return Response.json({id:"local",version:1,state:"draft"},{status:201})}
  if(path.endsWith("members"))return Response.json({items:[{user_id:"governor",username:"治理人员",department_id:"test"}]});
  if(path.includes("enterprise_domain_id="))return Response.json({items:[],total:0});
  if(path.includes("/versions/1"))return Response.json(enterprise);
  return Response.json({items:[enterprise],total:1});
 }));
 show("domain");await userEvent.click(await screen.findByRole("button",{name:"统一客户"}));
 expect(screen.queryByRole("button",{name:"创建新版本"})).toBeNull();expect(screen.queryByRole("button",{name:"停用此版本"})).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:"登记本部门责任"}));expect(screen.getAllByRole("dialog")).toHaveLength(1);
 await userEvent.type(screen.getByLabelText("业务说明"),"本部门负责客户联系资料，不负责交易记录");
 await userEvent.click(screen.getByRole("button",{name:"保存草稿"}));await waitFor(()=>expect(posts).toHaveLength(1));
 expect(posts[0]).toMatchObject({base_version:0,definition:{name:"统一客户 · 本部门责任",domain_scope:"department",enterprise_domain:{id:"enterprise",version:1},owner_id:"governor",steward_id:"governor",description:"本部门负责客户联系资料，不负责交易记录"}});
});
it("码表逐值填写业务含义，提交兼容允许值和含义映射，不绑定表",async()=>{
 const posts:unknown[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push(JSON.parse(String(init.body)));return Response.json({id:"standard",version:1,state:"draft"},{status:201})}
  return Response.json({items:path.endsWith("members")?[{user_id:"governor",username:"治理人员",department_id:"test"}]:[],total:0});
 }));
 show("standard");await userEvent.click(await screen.findByRole("button",{name:"新建数据标准"}));
 await userEvent.type(screen.getByLabelText("名称"),"启停标志");await userEvent.type(screen.getByLabelText("标准编码"),"common.enable_flag");
 await userEvent.click(screen.getByRole("button",{name:"添加码值"}));await userEvent.type(screen.getByLabelText("码值",{exact:true}),"0");
 await userEvent.click(screen.getByRole("button",{name:"保存草稿"}));await screen.findByText(/请输入码值含义|Please enter/);expect(posts).toHaveLength(0);
 await userEvent.type(screen.getByLabelText("码值含义"),"停用");await userEvent.click(screen.getByRole("button",{name:"添加码值"}));
 await userEvent.type(screen.getAllByLabelText("码值",{exact:true})[1],"1");await userEvent.type(screen.getAllByLabelText("码值含义")[1],"启用");
 await userEvent.click(screen.getByRole("button",{name:"保存草稿"}));await waitFor(()=>expect(posts).toHaveLength(1));
 expect(posts[0]).toMatchObject({definition:{standard:{code:"common.enable_flag",values:["0","1"],value_meanings:{"0":"停用","1":"启用"}}}});
 expect(JSON.stringify(posts[0])).not.toContain("code_entries");
});
it("旧标准读取时明确提示未登记含义，不改写历史定义",async()=>{
 const standard={id:"legacy",kind:"standard",department_id:"test",latest_version:1,version:1,state:"published",definition:{name:"旧启停标志",description:"",owner_id:"governor",steward_id:"governor",standard:{code:"common.old_flag",logical_type:"integer",values:["0","1"]}},created_at:"2026-10-22T00:00:00Z"};
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({items:[standard],total:1})));
 show("standard");await userEvent.click(await screen.findByRole("button",{name:"旧启停标志"}));
 expect(screen.getAllByText("历史版本未登记，请创建新版本补充")).toHaveLength(2);
});
it("历史完整运行显式保存结果模型，再查看真实草稿",async()=>{
 window.history.replaceState({},"","/governance/models?from_run=completed");
 const definition={name:"招生结果模型",description:"每行代表一个儿童的报名资料",owner_id:"governor",steward_id:"governor",model:{layer:"logical",fields:[{name:"child_id",logical_type:"string",nullable:true,primary_key:false,sensitive:true}],relations:[],contract:{freshness_seconds:0,max_failure_rate:0,usage_restrictions:""}}};
 const record={id:"generated",kind:"model",department_id:"test",latest_version:1,version:1,state:"draft",definition,change_note:"来自完整运行",migration_note:"",created_at:"2026-10-26T00:00:00Z"};
 const posts:{path:string;body:unknown}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return Response.json(record,{status:201})}
  if(path.includes("model-proposal"))return Response.json({definition,change_note:"来自完整运行"});
  return Response.json({items:[],total:0});
 }));
 try{
  show("model");await screen.findByRole("button",{name:"保存结果模型草稿"});
  expect(screen.getAllByRole("dialog")).toHaveLength(1);expect(screen.getByText("敏感")).toBeTruthy();
  expect(posts).toHaveLength(0);
  await userEvent.click(screen.getByRole("button",{name:"保存结果模型草稿"}));
  await screen.findByText("招生结果模型 · v1");
  expect(posts).toEqual([{path:"/api/v1/governance/runs/completed/model-draft",body:{}}]);
  expect(screen.getByText("generated")).toBeTruthy();
  expect(screen.queryByRole("button",{name:"保存结果模型草稿"})).toBeNull();
 }finally{window.history.replaceState({},"","/")}
});
it("根据运行打开已保存模型，直接查看真实草稿且不重复创建",async()=>{
 window.history.replaceState({},"","/governance/models?from_run=completed");
 const definition={name:"已生成招生结果模型",description:"来自完整运行 completed",owner_id:"governor",steward_id:"governor",model:{layer:"logical",fields:[{name:"child_id",description:"儿童编号",logical_type:"string",nullable:true,primary_key:false,sensitive:true}],relations:[],contract:{freshness_seconds:0,max_failure_rate:0,usage_restrictions:""}}};
 const record={id:"persisted",kind:"model",department_id:"test",workspace_id:"space",latest_version:1,version:1,state:"draft",definition,change_note:"来自完整运行",migration_note:"",created_at:"2026-10-26T00:00:00Z"};
 const posts:unknown[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push(path);return Response.json({},{status:500})}
  if(path.includes("model-proposal"))return Response.json({definition,change_note:"来自完整运行",generated_model:{id:"persisted",version:1}});
  if(path.includes("/models/persisted/versions/1"))return Response.json(record);
  return Response.json({items:[],total:0});
 }));
 try{
  show("model");await screen.findByText("已生成招生结果模型 · v1");
  expect(screen.getAllByRole("dialog")).toHaveLength(1);
  expect(screen.getByText("persisted")).toBeTruthy();
  expect(screen.getByText("儿童编号（child_id）")).toBeTruthy();
  expect(screen.queryByRole("button",{name:"保存草稿"})).toBeNull();
  expect(posts).toHaveLength(0);
 }finally{window.history.replaceState({},"","/")}
});

it("新模型按业务用途对应分层，概念模型可以暂不确定用途",async()=>{
 const posts:unknown[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push(JSON.parse(String(init.body)));return Response.json({id:"model",version:1,state:"draft"},{status:201})}
  return Response.json({items:path.endsWith("members")?[{user_id:"governor",username:"治理人员",department_id:"test"}]:[],total:0});
 }));
 show("model");await userEvent.click(await screen.findByRole("button",{name:"新建数据模型"}));
 expect(screen.getByLabelText("业务用途")).toBeTruthy();
 expect(screen.getByText("对应分层：待确定")).toBeTruthy();
 await userEvent.type(screen.getByLabelText("名称"),"报名统计");
 await userEvent.click(screen.getByRole("button",{name:"保存草稿"}));
 await screen.findByText("请选择业务用途");expect(posts).toHaveLength(0);
 await userEvent.click(screen.getByLabelText("业务用途"));
 await userEvent.click(await screen.findByText("汇总可复用的统计指标",{selector:".ant-select-item-option-content"}));
 expect(screen.getByText("对应分层：主题汇总 · DWS")).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"保存草稿"}));
 await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0]).toMatchObject({definition:{model:{data_layer:"dws"}}});
 await userEvent.click(screen.getByRole("button",{name:"新建数据模型"}));
 await userEvent.type(screen.getByLabelText("名称"),"业务概念");
 await userEvent.click(screen.getByLabelText("模型层级"));
 await userEvent.click(await screen.findByText("概念模型",{selector:".ant-select-item-option-content"}));
 await userEvent.click(screen.getByRole("button",{name:"保存草稿"}));
 await waitFor(()=>expect(posts).toHaveLength(2));
 expect(posts[1]).toMatchObject({definition:{model:{layer:"conceptual"}}});
 expect(JSON.stringify(posts[1])).not.toContain("data_layer");
});

it("逻辑模型隐藏物理配置，切换物理模型后展示目标库表和主键说明",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json({items:path.endsWith("members")?[{user_id:"governor",username:"治理人员",department_id:"test"}]:[],total:0})));
 show("model");await userEvent.click(await screen.findByRole("button",{name:"新建数据模型"}));
 expect(screen.queryByLabelText("目标库 / Schema")).toBeNull();expect(screen.queryByText(/物理模型需要非空主键/)).toBeNull();
 await userEvent.click(screen.getByLabelText("模型层级"));await userEvent.click(await screen.findByText("物理模型",{selector:".ant-select-item-option-content"}));
 expect(screen.getByLabelText("目标库 / Schema")).toBeTruthy();expect(screen.getByLabelText("目标表")).toBeTruthy();expect(screen.getByText(/物理模型需要非空主键/)).toBeTruthy();
});

it("来源运行上下文在模型发布后提供回绑入口，保留明确模型版本",async()=>{
 history.replaceState(null,"","/platform/governance/models?model_id=generated&version=2&from_run=source-run&workspace_id=space");
 const definition={name:"已确认模型",owner_id:"governor",steward_id:"governor",model:{layer:"logical",data_layer:"dws",schema:"kept_schema",table:"kept_table",fields:[],relations:[],contract:{freshness_seconds:0,max_failure_rate:0,usage_restrictions:""}}};
 const record={id:"generated",kind:"model",department_id:"test",workspace_id:"space",latest_version:2,version:2,state:"draft",definition,created_at:"2026-10-28T00:00:00Z"};
 const posts:string[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push(path.split("?")[0]);return Response.json({...record,state:"published"})}
  if(path.split("?")[0]==="/api/v1/governance/runs/source-run")return Response.json({id:"source-run",kind:"dataflow",state:"succeeded",workspace_id:"space",definition:{flow:{id:"flow",version:1}},result:{generated_model:{id:"generated",version:1},unmodeled:true}});
  if(path.includes("model-proposal"))return Response.json({definition,generated_model:{id:"generated",version:1}});
  if(path.includes("/models/generated/versions/"))return Response.json(record);
  return Response.json({items:[],total:0});
 }));
 try{
  show("model");await screen.findByText("来源运行 source-run · 数据流 v1",{}, {timeout:10000});
  expect(screen.queryByRole("button",{name:"返回数据流并绑定此版本"})).toBeNull();
  await userEvent.click(screen.getByRole("button",{name:"发布此版本"}));
  await userEvent.click(await screen.findByRole("button",{name:"返回数据流并绑定此版本"}));
  expect(new URLSearchParams(location.search).get("model_version")).toBe("2");expect(new URLSearchParams(location.search).get("from_run")).toBe("source-run");
  expect(posts).toEqual(["/api/v1/governance/models/generated/versions/2/publish"]);
 }finally{history.replaceState(null,"","/")}
});

it("历史模型中文名沿用同版本成果说明，保留原定义与技术字段",async()=>{
 const field={name:"death_record_id",description:"",logical_type:"string",length:0,precision:0,scale:0,nullable:false,primary_key:true,sensitive:false};
 const record={id:"deceased-model",kind:"model",department_id:"test",latest_version:1,version:1,state:"published",definition:{name:"死亡人口核验专项库结果模型",owner_id:"governor",steward_id:"governor",model:{layer:"logical",data_layer:"dwd",fields:[field],relations:[],contract:{freshness_seconds:0,max_failure_rate:0,usage_restrictions:""}}},created_at:"2026-11-09T00:00:00Z"};
 const calls:string[]=[];vi.stubGlobal("fetch",vi.fn(async(path:string)=>{calls.push(path);return Response.json(path.endsWith("/field-labels")?{field_labels:{death_record_id:"死亡登记编号"}}:{items:path.startsWith("/api/v1/governance/models?")?[record]:[],total:1})}));
 show("model");await userEvent.click(await screen.findByRole("button",{name:record.definition.name}));expect(await screen.findByText("死亡登记编号（death_record_id）")).toBeTruthy();expect(record.definition.model.fields[0]).toEqual(field);expect(calls.some(path=>path.includes("/deceased-model/versions/1/field-labels"))).toBe(true);
});

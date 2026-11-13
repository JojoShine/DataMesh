// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor,within} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider,Modal} from "antd";
import zhCN from "antd/locale/zh_CN";
import {Tasks} from "../src/pages/Tasks";
import {useSession} from "../src/stores/session";
vi.setConfig({testTimeout:15000});
const remote={source_id:"remote",name:"远程业务库",source_version:1,capture_template:false,kind:"postgres",cdc_status:"untested",connection_status:"passed"};
const local={...remote,source_id:"local",name:"已验证采集库",capture_template:true,cdc_status:"passed"};
const object={object_id:"cdc-customers",source_id:"local",qualified_name:"public.customers",schema_version:3,fields:[]};
beforeEach(()=>{
 useSession.setState({user:{user_id:"admin",username:"admin",department_id:"test",roles:["admin"],platforms:["exchange"],csrf_token:"test"},ready:true,epoch:0,identityError:null});
 Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});
 vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});
});
afterEach(()=>{cleanup();Modal.destroyAll();vi.unstubAllGlobals()});
function dialog(){return within(screen.getByRole("dialog",{name:"建立接入任务"}))}
function show(){render(<ConfigProvider locale={zhCN} theme={{token:{motion:false}}}><AntApp><Tasks/></AntApp></ConfigProvider>)}
async function chooseSource(name:string){await userEvent.click(await screen.findByRole("button",{name:"建立任务"}));await userEvent.click(screen.getByLabelText("数据源"));await userEvent.click(await screen.findByText(name));}
it("采集对象读取实际库表，远程表可配置独立作业，未填写发布名不能保存",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify({items:path.startsWith("/api/v1/sources?")?[remote]:path.includes("/tables")?[{schema:"public",name:"parking_application",kind:"BASE TABLE"}]:[],total:1}))));
 show();await chooseSource(remote.name);
 await userEvent.click(screen.getByLabelText("采集对象"));await userEvent.click(await screen.findByText("public.parking_application"));
 expect(await screen.findByText(/新建独立采集作业/)).toBeTruthy();
 expect(dialog().getByRole("button",{name:"保存采集草稿"}).hasAttribute("disabled")).toBe(true);
 expect(screen.getByRole("button",{name:"前往 ETL 数据交换"})).toBeTruthy();
});
it("获取表失败显示真实错误和重试，恢复后能重新选择",async()=>{
 let failed=true;
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>path.includes("/tables")&&failed?new Response(JSON.stringify({code:"SOURCE_UNAVAILABLE",message:"数据库连接失败",trace_id:"tables-test"}),{status:503}):new Response(JSON.stringify({items:path.startsWith("/api/v1/sources?")?[remote]:path.includes("/tables")?[{schema:"public",name:"parking_application",kind:"BASE TABLE"}]:[],total:1}))));
 show();await chooseSource(remote.name);await screen.findByText(/数据库连接失败/);failed=false;
 await userEvent.click(screen.getByRole("button",{name:/^重\s*试$/}));
 await userEvent.click(screen.getByLabelText("采集对象"));expect(await screen.findByText("public.parking_application")).toBeTruthy();
});
it("已验证CDC对象仍按原对象编号和结构版本创建，不把库表名称当对象编号",async()=>{
 const bodies:unknown[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
 if(init?.method==="POST"){bodies.push(JSON.parse(String(init.body)));return new Response(JSON.stringify({task_id:"new-task"}),{status:201})}
 return new Response(JSON.stringify({items:path.startsWith("/api/v1/sources?")?[local]:path.includes("/tables")?[{schema:"public",name:"customers",kind:"BASE TABLE"}]:path.includes("/objects")?[object]:[],total:1}));
 }));
 show();await chooseSource(local.name);await userEvent.click(screen.getByLabelText("采集对象"));await userEvent.click(await screen.findByText("public.customers"));
 await waitFor(()=>expect(dialog().getByRole("button",{name:"建立任务"}).hasAttribute("disabled")).toBe(false));
 await userEvent.click(dialog().getByRole("button",{name:"建立任务"}));
 await waitFor(()=>expect(bodies).toEqual([{source_id:"local",object_id:"cdc-customers",mode:"cdc",dataset_id:null,schema_version:3,rule_version:null}]));
});
it("刷新库表会读取新建表并清空过期选择",async()=>{
 let refreshed=false;
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify({items:path.startsWith("/api/v1/sources?")?[local]:path.includes("/tables")?[{schema:"public",name:refreshed?"new_table":"customers",kind:"BASE TABLE"}]:path.includes("/objects")?[object]:[],total:1}))));
 show();await chooseSource(local.name);await userEvent.click(screen.getByLabelText("采集对象"));await userEvent.click(await screen.findByText("public.customers"));refreshed=true;
 await userEvent.click(await screen.findByRole("button",{name:/刷新库表$/}));
 expect(dialog().getByRole("button",{name:"保存采集草稿"}).hasAttribute("disabled")).toBe(true);
 await waitFor(()=>expect(screen.getByLabelText("采集对象").hasAttribute("disabled")).toBe(false));
 await userEvent.click(screen.getByLabelText("采集对象"));expect(await screen.findByText("public.new_table")).toBeTruthy();
});
it("从所选库表直接检查CDC条件，提交准确schema和表名",async()=>{
 const bodies:unknown[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
 if(init?.method==="POST"){bodies.push(JSON.parse(String(init.body)));return new Response(JSON.stringify({source_id:"remote",kind:"cdc",passed:false,checked_at:"2026-10-21T01:00:00Z",checks:[{check:"platform_cdc",label:"平台采集能力",passed:false,status:"unsupported",owner:"平台管理员",reason:"平台尚未支持此远程源的CDC作业创建",suggestion:"联系平台管理员",steps:["完成远程采集接入后重新检查"]}]}))}
 return new Response(JSON.stringify({items:path.startsWith("/api/v1/sources?")?[remote]:path.includes("/tables")?[{schema:"public",name:"parking_application",kind:"BASE TABLE"}]:[],total:1}));
 }));
 show();await chooseSource(remote.name);await userEvent.click(screen.getByLabelText("采集对象"));await userEvent.click(await screen.findByText("public.parking_application"));
 await userEvent.type(screen.getByLabelText("发布名称"),"parking_publication");
 await userEvent.click(screen.getByRole("button",{name:"检查 CDC 条件"}));await screen.findByText("平台暂不支持");expect(bodies).toEqual([{source_id:"remote",source_version:1,schema:"public",table:"parking_application",publication:"parking_publication"}]);
 await waitFor(()=>expect(screen.getAllByRole("dialog")).toHaveLength(1));
 await userEvent.click(screen.getByRole("button",{name:"返回接入任务"}));
 await waitFor(()=>expect(screen.getAllByRole("dialog")).toHaveLength(1));
 expect(dialog().getByText("远程业务库")).toBeTruthy();
 expect(dialog().getByText("public.parking_application")).toBeTruthy();
},15000);
it("远程库表可建立独立采集草稿，诊断返回保留发布名称",async()=>{
 const bodies:{path:string;body:unknown}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){
   bodies.push({path,body:JSON.parse(String(init.body))});
   return new Response(JSON.stringify(path.endsWith("/validate")?{source_id:"remote",kind:"cdc",passed:true,checks:[]}:{task_id:"new-task",state:"draft"}),{status:path.endsWith("/validate")?200:201});
  }
  return new Response(JSON.stringify({items:path.startsWith("/api/v1/sources?")?[{...remote,source_version:1,capture_template:false}]:path.includes("/tables")?[{schema:"public",name:"parking_application",kind:"BASE TABLE"}]:[],total:1}));
 }));
 show();await chooseSource(remote.name);
 await userEvent.click(screen.getByLabelText("采集对象"));await userEvent.click(await screen.findByText("public.parking_application"));
 await userEvent.type(await screen.findByLabelText("发布名称"),"parking_publication");
 await userEvent.click(screen.getByRole("button",{name:"检查 CDC 条件"}));
 await userEvent.click(await screen.findByRole("button",{name:"返回接入任务"}));
 expect(screen.getByDisplayValue("parking_publication")).toBeTruthy();
 await userEvent.click(dialog().getByRole("button",{name:"保存采集草稿"}));
 await waitFor(()=>expect(bodies.some(c=>c.path==="/api/v1/cdc-tasks")).toBe(true));
 expect(bodies.find(c=>c.path==="/api/v1/cdc-tasks")?.body).toEqual({source_id:"remote",source_version:1,schema:"public",table:"parking_application",publication:"parking_publication"});
},15000);
it("Oracle 先填写目标 PDB 再刷新库表，不把 CDB 的表当成采集对象",async()=>{
 const calls:string[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>{calls.push(path);return new Response(JSON.stringify({items:path.startsWith("/api/v1/sources?")?[{...remote,kind:"oracle",name:"Oracle业务库"}]:path.includes("/tables?pdb=FREEPDB1")?[{schema:"APP",name:"ORDERS",kind:"TABLE"}]:[],total:1}))}));
 show();await chooseSource("Oracle业务库");
 await userEvent.type(await screen.findByLabelText("目标 PDB"),"FREEPDB1");
 await userEvent.click(screen.getByRole("button",{name:"刷新库表"}));
 await waitFor(()=>expect(calls.some(p=>p.includes("/tables?pdb=FREEPDB1"))).toBe(true));
 await userEvent.click(screen.getByLabelText("采集对象"));expect(await screen.findByText("APP.ORDERS")).toBeTruthy();
},15000);

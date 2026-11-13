// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor,within,fireEvent} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider,Modal} from "antd";
import zhCN from "antd/locale/zh_CN";
import {Sources} from "../src/pages/Sources";
import {useSession} from "../src/stores/session";
beforeEach(()=>{
 useSession.setState({user:{user_id:"admin",username:"admin",department_id:"test",roles:["admin"],platforms:["exchange"],csrf_token:"test"},ready:true,epoch:0,identityError:null});
 Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});
 vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});
});
afterEach(()=>{cleanup();Modal.destroyAll();vi.unstubAllGlobals()});
function show(){render(<ConfigProvider locale={zhCN}><AntApp><Sources/></AntApp></ConfigProvider>)}
const target={target_id:"platform-one",kind:"postgres",host:"db.internal",port:5432,database:"business",origin:"platform",created_at:"2026-10-21T00:00:00Z"};
it("白名单仅管理员可见，普通运维不请求配置接口",async()=>{
 useSession.setState(s=>({user:s.user?{...s.user,roles:["operator"]}:null}));
 const fetcher=vi.fn(async(_path:string)=>new Response(JSON.stringify({items:[],total:0})));vi.stubGlobal("fetch",fetcher);
 show();await screen.findByText("数据源列表");expect(screen.queryByRole("button",{name:"可信数据源"})).toBeNull();expect(fetcher.mock.calls.some(call=>String(call[0]).includes("source-allowed-targets"))).toBe(false);
});
it("显示白名单来源，删除须确认且不能删除环境配置",async()=>{
 const calls:string[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
 if(init?.method==="POST"){calls.push(path);return new Response(JSON.stringify({target_id:target.target_id,removed:true}))}
 return new Response(JSON.stringify({items:path.includes("source-allowed-targets")?[target,{...target,target_id:"environment-one",host:"env.internal",origin:"environment"}]:[],total:2}));
 }));
 show();await userEvent.click(screen.getByRole("button",{name:"可信数据源"}));
 await screen.findByText("env.internal");expect(screen.getByText("环境配置")).toBeTruthy();
 expect(screen.getAllByRole("button",{name:/^删\s*除$/})).toHaveLength(1);
 await userEvent.click(screen.getByRole("button",{name:/^删\s*除$/}));expect(calls).toHaveLength(0);
 await userEvent.click(await screen.findByRole("button",{name:"确认删除"}));
 await waitFor(()=>expect(calls).toEqual(["/api/v1/source-allowed-targets/platform-one/remove"]));
});
it("新增白名单不收集账号密码，失败时保留填写内容",async()=>{
 const bodies:unknown[]=[];
 vi.stubGlobal("fetch",vi.fn(async(_path:string,init?:RequestInit)=>{
 if(init?.method==="POST"){bodies.push(JSON.parse(String(init.body)));return new Response(JSON.stringify({code:"STATE_CONFLICT",message:"该目标已在白名单中",trace_id:"test"}),{status:409})}
 return new Response(JSON.stringify({items:[],total:0}));
 }));
 show();await userEvent.click(screen.getByRole("button",{name:"可信数据源"}));await userEvent.click(await screen.findByRole("button",{name:"新增白名单"}));
 const form=(await screen.findByText("新增可信数据源")).closest('[role="dialog"]');
 if(!(form instanceof HTMLElement))throw new Error("新增表单未展示");
 await userEvent.type(within(form).getByLabelText("数据库地址"),"db.internal");
 await userEvent.type(within(form).getByLabelText("数据库名称"),"business");
 expect(within(form).queryByLabelText("数据库密码")).toBeNull();
 await userEvent.click(within(form).getByRole("button",{name:/^保\s*存$/}));
 await screen.findByText(/该目标已在白名单中/);expect(screen.getByDisplayValue("db.internal")).toBeTruthy();
 expect(bodies).toEqual([{kind:"postgres",host:"db.internal",port:5432,database:"business"}]);
});
it("CDC预检展示具体缺项、负责人、操作步骤与只读验证命令，并可重新检查",async()=>{
 let runs=0;
 const source={source_id:"s-cdc",name:"远程业务库",kind:"postgres",department_id:"test",connection_status:"passed",cdc_status:"failed",credential_configured:true};
 vi.stubGlobal("fetch",vi.fn(async(_path:string,init?:RequestInit)=>{
 if(init?.method==="POST"){runs++;return new Response(JSON.stringify({source_id:"s-cdc",kind:"cdc",passed:false,checked_at:"2026-10-21T01:00:00Z",checks:[{check:"wal_level",label:"逻辑复制日志",passed:runs>1,status:runs>1?"passed":"failed",owner:"数据库管理员",actual:runs>1?"logical":"replica",expected:"logical",reason:runs>1?"已开启逻辑复制":"未开启逻辑复制",suggestion:"调整实例参数后重新检查",steps:["在数据库实例参数中将 wal_level 设置为 logical；需要重启时安排维护窗口"],verify_sql:"SHOW wal_level;"},{check:"platform_cdc",label:"平台采集能力",passed:false,status:"unsupported",owner:"平台管理员",reason:"平台尚未支持此远程源的CDC作业创建",suggestion:"联系平台管理员安排远程CDC接入",steps:["数据库条件满足后仍需完成平台采集能力接入"]}]}))}
 return new Response(JSON.stringify({items:[source],total:1}));
 }));
 show();await userEvent.click(await screen.findByRole("button",{name:"CDC 预检"}));
 await screen.findByText("未开启逻辑复制");expect(screen.getByText("处理负责人：数据库管理员")).toBeTruthy();expect(screen.getByText(/在数据库实例参数中将 wal_level/)).toBeTruthy();expect(screen.getByText("SHOW wal_level;")).toBeTruthy();expect(screen.getByText("平台暂不支持")).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"重新检查"}));await screen.findByText("已开启逻辑复制");expect(runs).toBe(2);
});

it("新增来源按四类展示，切换MongoDB保留认证库及TLS配置",async()=>{
 const {sourceDefinitions}=await import("../src/api/sourceTypes");
 const bodies:Record<string,unknown>[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){bodies.push(JSON.parse(String(init.body)));return new Response(JSON.stringify({passed:true,checks:[]}))}
  return new Response(JSON.stringify({items:path.includes("source-types")?sourceDefinitions.map(t=>({...t,reader:!["postgres","mysql","oracle"].includes(t.kind),http:["clickhouse","elasticsearch","hbase"].includes(t.kind),status:t.kind==="kingbase"?"driver_missing":"ready"})):[],total:0}));
 }));
 show();await userEvent.click(screen.getByRole("button",{name:/登记数据源/}));
 const kind=screen.getByLabelText("数据源类型");await userEvent.click(kind);
 expect(await screen.findByText("国产数据库")).toBeTruthy();
 for(let i=0;i<4;i++)fireEvent.keyDown(kind,{key:"ArrowUp",code:"ArrowUp",keyCode:38,which:38});
 fireEvent.keyDown(kind,{key:"Enter",code:"Enter",keyCode:13,which:13});
 expect((screen.getByLabelText("端口") as HTMLInputElement).value).toBe("27017");
 await userEvent.type(screen.getByLabelText("数据库地址"),"mongo.internal");
 await userEvent.type(screen.getByLabelText("数据库名称"),"business");
 await userEvent.type(screen.getByLabelText("认证库（选填）"),"admin");
 await userEvent.click(screen.getByRole("button",{name:"测试连接"}));
 await waitFor(()=>expect(bodies).toHaveLength(1));
 expect(bodies[0]).toMatchObject({kind:"mongodb",host:"mongo.internal",port:27017,database:"business",auth_database:"admin",scheme:"https"});
 expect(screen.getByText(/使用稳定记录键及完整JSON/)).toBeTruthy();
});

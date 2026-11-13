// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider,Modal} from "antd";
import zhCN from "antd/locale/zh_CN";
import {ETL} from "../src/pages/ETL";
import {useSession} from "../src/stores/session";
const definition={name:"客户交换",source:{source_id:"s1",schema:"public",table:"source"},target:{source_id:"s2",schema:"public",table:"target"},mapping:[{source:"name",target:"name",rule:"trim"}]};
const task={id:"task-1",state:"ready",data:definition,created_at:"2026-10-19T00:00:00Z",updated_at:"2026-10-19T00:00:00Z"};
beforeEach(()=>{
 useSession.setState({user:{user_id:"admin",username:"admin",department_id:"test",roles:["admin"],platforms:["exchange"],csrf_token:"test"},ready:true,epoch:0,identityError:null});
 Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});
 vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});
});
afterEach(()=>{cleanup();Modal.destroyAll();vi.unstubAllGlobals()});
function show(){render(<ConfigProvider locale={zhCN} theme={{token:{motion:false}}}><AntApp><ETL/></AntApp></ConfigProvider>)}
it("执行 ETL 使用真实任务接口，未提交的批次不计为提交行数",async()=>{
 const calls:string[]=[];
 const run={id:"run-1",state:"loading",created_at:task.created_at,updated_at:task.updated_at,data:{definition,read:100,written:0,message:"正在写入目标事务，尚未提交",timings:{prepare_ms:1230,read_transform_ms:100,write_ms:100,index_ms:100,before_check_ms:0,commit_ms:0,total_ms:1530},batches:[{first:1,last:100,state:"pending_commit"}]}};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST"){calls.push(path);return new Response(JSON.stringify(run),{status:201})}return new Response(JSON.stringify({items:path.includes("/runs")?[run]:[task],total:1}),{status:200})}));
 show();await userEvent.click(await screen.findByRole("button",{name:"执行 ETL"}));
 expect(calls).toHaveLength(0);await userEvent.click(await screen.findByRole("button",{name:"确认执行"}));
 await screen.findByText("正在写入目标事务，尚未提交");expect(calls).toEqual(["/api/v1/etl-tasks/task-1/run"]);expect(screen.getByText("1.23 秒")).toBeTruthy();expect(await screen.findByText("待提交")).toBeTruthy();expect(screen.getAllByText("提交行数")[0]).toBeTruthy();
},15000);
it("运行接口拒绝时显示错误且不宣称任务已启动",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(_path:string,init?:RequestInit)=>init?.method==="POST"?new Response(JSON.stringify({code:"ETL_RUN_PENDING",message:"已有运行未结束",trace_id:"test"}),{status:409}):new Response(JSON.stringify({items:[task],total:1}),{status:200})));
 show();await userEvent.click(await screen.findByRole("button",{name:"执行 ETL"}));await userEvent.click(await screen.findByRole("button",{name:"确认执行"}));await screen.findByText(/已有运行未结束/);expect(screen.queryByText("运行已创建，可在交换明细中查看实际进度")).toBeNull();
});
it("新建任务必须选择来源目标后才能保存",async()=>{
 vi.stubGlobal("fetch",vi.fn(async()=>new Response(JSON.stringify({items:[],total:0}),{status:200})));
 show();await userEvent.click(screen.getByRole("button",{name:"新建 ETL 任务"}));
 await waitFor(()=>expect(screen.getByRole("button",{name:"保存任务"}).hasAttribute("disabled")).toBe(true));
 expect(screen.getByText("来源数据类型")).toBeTruthy();expect(screen.getByText("来源资源")).toBeTruthy();expect(screen.getByText("目标数据源")).toBeTruthy();
});
it("原始数据和治理数据在任务内切换，复用同一个资源选择与申请入口",async()=>{
 const calls:string[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>{calls.push(path);return new Response(JSON.stringify({items:[],total:0}),{status:200})}));
 show();await userEvent.click(screen.getByRole("button",{name:"新建 ETL 任务"}));
 await waitFor(()=>expect(calls.some(p=>p.includes("data_layer=raw_object"))).toBe(true));
 await userEvent.click(screen.getByText("原始数据"));await userEvent.click(await screen.findByText("治理数据"));
 await waitFor(()=>expect(calls.some(p=>p.includes("data_layer=governed_dataset"))).toBe(true));
 expect(screen.queryByText("来源选择方式")).toBeNull();
 expect(screen.getByRole("button",{name:"申请使用所选数据"}).hasAttribute("disabled")).toBe(true);
});
it("停用任务不能执行，并可从页面启用",async()=>{
 const calls:string[]=[];const disabled={...task,state:"disabled",version:2};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST"){calls.push(path);return new Response(JSON.stringify({...disabled,state:"ready",version:3}),{status:200})}return new Response(JSON.stringify({items:path.includes("etl-alerts")?[]:[disabled],total:1}),{status:200})}));
 show();await screen.findByRole("button",{name:/^启\s*用$/});expect(screen.getByRole("button",{name:"执行 ETL"}).hasAttribute("disabled")).toBe(true);
 await userEvent.click(screen.getByRole("button",{name:/^启\s*用$/}));await waitFor(()=>expect(calls).toContain("/api/v1/etl-tasks/task-1/enable"));
});
it("unknown运行保留核实入口且禁止直接重试",async()=>{
 const run={id:"run-unknown",state:"unknown",created_at:task.created_at,updated_at:task.updated_at,data:{definition,read:10,written:0,batches:[],message:"等待核对目标事务",attempt:1}};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify({items:path.includes("/runs?page=1&")?[run]:path.includes("etl-tasks")?[task]:[],total:1}),{status:200})));
 show();await userEvent.click(await screen.findByRole("button",{name:"运行与交换明细"}));await screen.findByText("等待核对目标事务");
 expect(screen.getByRole("button",{name:"核实结果"})).toBeTruthy();expect(screen.queryByRole("button",{name:"重试运行"})).toBeNull();expect(screen.getByText("运行日志")).toBeTruthy();
});
it("自动刷新运行进度不会清空正在填写的核实依据",async()=>{
 const run={id:"run-stable",state:"unknown",created_at:task.created_at,updated_at:task.updated_at,data:{definition,read:10,written:0,batches:[],message:"等待核对目标事务",attempt:1}};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>{if(path.includes("/runs?"))await new Promise(resolve=>setTimeout(resolve,80));return new Response(JSON.stringify({items:path.includes("/runs?")?[run]:path.includes("etl-tasks")?[task]:[],total:1}),{status:200})}));
 show();await userEvent.click(await screen.findByRole("button",{name:"运行与交换明细"}));await screen.findByRole("button",{name:"核实结果"});const dialogCount=screen.getAllByRole("dialog").length;await userEvent.click(screen.getByRole("button",{name:"核实结果"}));
 expect(screen.getAllByRole("dialog")).toHaveLength(dialogCount);
 const placeholder="请输入事务核对记录、工单或其他可追溯依据";
 await userEvent.type(screen.getByPlaceholderText(placeholder),"已检查目标事务记录");
 await new Promise(resolve=>setTimeout(resolve,3500));
 expect(screen.getByDisplayValue("已检查目标事务记录")).toBeTruthy();
},15000);

it("告警在当前窗口内处理，不叠加弹窗",async()=>{
 const alert={id:"alert-1",run_id:"run-1",code:"FAILED",severity:"error",message:"测试运行告警",state:"open",reason:"",created_at:task.created_at};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify({items:path.includes("etl-alerts")?[alert]:[],total:1}))));
 show();await userEvent.click(await screen.findByRole("button",{name:/运行告警/}));
 const dialogCount=screen.getAllByRole("dialog").length;
 await userEvent.click(await screen.findByRole("button",{name:"确认处理"}));
 expect(screen.getAllByRole("dialog")).toHaveLength(dialogCount);
 await userEvent.type(screen.getByPlaceholderText("请说明已采取的处理措施或关闭依据"),"已检查");
 await userEvent.click(screen.getByRole("button",{name:"返回告警列表"}));
 expect(screen.queryByPlaceholderText("请说明已采取的处理措施或关闭依据")).toBeNull();
 expect(screen.getAllByRole("dialog")).toHaveLength(dialogCount);
});

it("按选中资源显示真实批量容量，不把所有资源统一标成一万行",async()=>{
 const calls:string[]=[];
 const directory={directory_id:"directory",available:true,resource:{resource_type:"table_resource",resource_id:"bulk-resource",contract_version:1},fields:[{name:"id",logical_type:"integer",primary_key:true}],definition:{name:"授权大表",data_layer:"raw_object"}};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>{calls.push(path);if(path.startsWith("/api/v1/etl-capabilities"))return new Response(JSON.stringify({direct:{max_rows:2000000,max_bytes:1073741824,timeout_seconds:1800},platform_resource:{max_rows:path.includes("resource_id=")?2000000:10000,max_bytes:path.includes("resource_id=")?1073741824:20971520,timeout_seconds:1800,adapter:path.includes("resource_id=")?"database_postgres":"legacy_resource"}}));return new Response(JSON.stringify({items:path.includes("resource-directories")?[directory]:[],total:1}))}));
 show();await userEvent.click(screen.getByRole("button",{name:"新建 ETL 任务"}));
 await userEvent.click(screen.getByRole("combobox",{name:"来源资源"}));await userEvent.click(await screen.findByText("授权大表"));
 await waitFor(()=>expect(calls.some(p=>p.includes("etl-capabilities?")&&p.includes("resource_id=bulk-resource"))).toBe(true));
 expect(await screen.findByText(/2,000,000 行/)).toBeTruthy();
});

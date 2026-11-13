// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {GovernanceProfiles} from "../src/pages/GovernanceProfiles";
import {useSession} from "../src/stores/session";
const object={object_id:"mounted",resource_type:"table_resource" as const,adapter:"database",source_id:"source",qualified_name:"public.orders",schema_version:3,fields:[{name:"id",logical_type:"integer",primary_key:true,nullable:false,sensitive:false,precision:null,scale:null},{name:"secret",logical_type:"string",nullable:true,sensitive:true,primary_key:false,precision:null,scale:null}]};
vi.setConfig({testTimeout:15000});
beforeEach(()=>{
 useSession.setState({user:{user_id:"governor",username:"治理人员",department_id:"test",roles:["governor"],platforms:["governance"],csrf_token:"test"},ready:true,epoch:0,identityError:null});
 Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});
 vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});
});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
function show(){render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><GovernanceProfiles object={object}/></AntApp></ConfigProvider>)}
it("创建剖析固定资源契约和所选字段，采样报告不冒充全量",async()=>{
 const posts:{path:string;body:unknown}[]=[];
 const report={mode:"sample",coverage:"sample",sampling:"first_rows_not_random",rows:2,total_rows:10,fields:[{name:"secret",logical_type:"string",null_count:1,distinct_count:null,duplicate_count:null,min:null,max:null,redacted:true,frequencies:[]}],anomalies:[]};
 const run={id:"run",kind:"profile",state:"succeeded",processed:2,created_at:"2026-10-21T01:00:00Z",definition:{mode:"sample"},result:report};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return new Response(JSON.stringify({...run,state:"queued"}),{status:201})}
  return new Response(JSON.stringify(path==="/api/v1/governance/runs/run"?run:{items:path.includes("/events")?[]:[run],total:1,page:1,page_size:20}));
 }));
 show();await userEvent.click(screen.getByRole("button",{name:"检查数据情况"}));
 await waitFor(()=>expect(posts).toEqual([{path:"/api/v1/governance/profiles",body:{resource:{resource_type:"table_resource",resource_id:"mounted",contract_version:3},fields:["id","secret"],mode:"sample",sample_rows:1000}}]));
 expect(await screen.findByText("采样覆盖：2 / 10 行")).toBeTruthy();
 expect(screen.getByText("敏感统计已隐藏")).toBeTruthy();
 expect(screen.queryByRole("dialog")).toBeNull();
});
it("取消任务使用持久运行编号，页面内返回历史，不叠加弹窗",async()=>{
 const posts:string[]=[];const run={id:"pending",kind:"profile",state:"running",processed:12,definition:{mode:"full"}};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push(path);return new Response(JSON.stringify({...run,state:"cancelled"}))}
  return new Response(JSON.stringify(path==="/api/v1/governance/runs/pending"?run:{items:path.includes("/events")?[]:[run],total:1,page:1,page_size:20}));
 }));
 show();await userEvent.click(await screen.findByRole("button",{name:"查看检查报告"}));await userEvent.click(await screen.findByRole("button",{name:"取消本次检查"}));
 await waitFor(()=>expect(posts).toEqual(["/api/v1/governance/runs/pending/cancel"]));expect(screen.queryByRole("dialog")).toBeNull();
});

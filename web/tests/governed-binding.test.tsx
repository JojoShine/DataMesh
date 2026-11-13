// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider,Modal} from "antd";
import {GovernedResultBindingEditor} from "../src/components/GovernedResultBindingEditor";
import {useSession} from "../src/stores/session";
import type {Dataset} from "../src/api/types";
vi.setConfig({testTimeout:15000});
const fields=[{name:"id",logical_type:"integer",primary_key:true,nullable:false,sensitive:false,precision:null,scale:null},{name:"name",logical_type:"string",primary_key:false,nullable:true,sensitive:true,precision:null,scale:null}];
const dataset:Dataset={dataset_id:"dataset",contract_version:2,provider_department_id:"provider",definition:{name:"客户成果",description:"",source_object_id:"object",owner_id:"owner",fields,usage_restrictions:""},publication_status:"draft",quality_status:"not_checked",completeness_status:"pending",latest_watermark:null};
beforeEach(()=>{
 useSession.setState({user:{user_id:"admin",username:"admin",department_id:"provider",roles:["admin"],platforms:["governance"],csrf_token:"csrf"},ready:true,epoch:0,identityError:null});
 Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});
 vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});
});
afterEach(()=>{cleanup();Modal.destroyAll();vi.unstubAllGlobals()});
it("绑定当前完整归档与真实结果库表，后台排队时保留单层配置窗口",async()=>{
 const bodies:unknown[]=[];
 const fetcher=vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){bodies.push(JSON.parse(String(init.body)));return new Response(JSON.stringify({operation_id:"op",status:"accepted",resource_id:"dataset",result:{governed_check_id:"check"}}),{status:202})}
  const body=path.includes("/result-binding")?{binding:null,rule_version:3,contract_version:2,input_archive_id:"archive",checks:[]}:path.includes("/tables")?{items:[{schema:"public",name:"customers_result",kind:"BASE TABLE"}]}:{items:[{source_id:"result-source",name:"结果库",kind:"postgres",source_version:4}],total:1};
  return new Response(JSON.stringify(body));
 });vi.stubGlobal("fetch",fetcher);
 render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><GovernedResultBindingEditor dataset={dataset} onClose={()=>{}} onQueued={()=>{}}/></AntApp></ConfigProvider>);
 await userEvent.click(await screen.findByLabelText("结果数据源"));await userEvent.click(await screen.findByText("结果库"));
 await userEvent.click(screen.getByLabelText("结果库表"));await userEvent.click(await screen.findByText("public.customers_result"));
 await userEvent.click(screen.getByLabelText("name 的转换规则"));await userEvent.click(await screen.findByText("去除首尾空白"));
 await userEvent.click(screen.getByRole("button",{name:"保存并核验"}));
 await waitFor(()=>expect(bodies).toEqual([{source_id:"result-source",source_version:4,schema:"public",table:"customers_result",input_archive_id:"archive",contract_version:2,rule_version:3,mapping:[{source:"id",target:"id",rule:"copy"},{source:"name",target:"name",rule:"trim"}]}]));
 expect(await screen.findByText("核验已在后台排队，窗口内会自动更新进度")).toBeTruthy();
 expect(screen.getAllByRole("dialog")).toHaveLength(1);
 expect(fetcher.mock.calls.some(([path])=>path.includes("/etl-tasks"))).toBe(false);
});
it("没有完整归档时提示先归档并禁止提交",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify(path.includes("result-binding")?{binding:null,rule_version:3,contract_version:2,input_archive_id:"",checks:[]}:{items:[],total:0}))));
 render(<AntApp><GovernedResultBindingEditor dataset={dataset} onClose={()=>{}} onQueued={()=>{}}/></AntApp>);
 expect(await screen.findByText(/尚无完整原始归档/)).toBeTruthy();
 expect(screen.getByRole("button",{name:"保存并核验"}).hasAttribute("disabled")).toBe(true);
});

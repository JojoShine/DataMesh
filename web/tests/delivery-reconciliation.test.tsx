// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider,Modal} from "antd";
import {DeliveryReconciliation} from "../src/components/DeliveryReconciliation";
import {useSession} from "../src/stores/session";
import type {Run} from "../src/pages/ETL";
vi.setConfig({testTimeout:15000});
const run:Run={id:"run",state:"succeeded",created_at:"2026-10-21T00:00:00Z",updated_at:"2026-10-21T00:00:00Z",data:{definition:{name:"授权交付",source_resource:{resource_type:"table_resource",resource_id:"resource",contract_version:1},source:{source_id:"resource",schema:"raw_object",table:"source"},target:{source_id:"target",schema:"public",table:"target"},mapping:[]},read:10001,written:10001,message:"目标事务已提交",batches:[]}};
const report={id:"report-1",run_id:"run",version:1,state:"passed",input_snapshot_id:"snapshot",input_scope:null,task_version:1,read_rows:10001,processed_rows:10001,key_count:10000,checked_rows:10000,mismatch_count:0,checked_at:run.updated_at,created_at:run.created_at,updated_at:run.updated_at};
beforeEach(()=>{
 useSession.setState({user:{user_id:"recipient",username:"recipient",department_id:"consumer",roles:["operator"],platforms:["exchange"],csrf_token:"csrf"},ready:true,epoch:0,identityError:null});
 Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});
 vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});
});
afterEach(()=>{cleanup();Modal.destroyAll();vi.unstubAllGlobals()});
function show(value=run){render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><Modal open footer={null}><DeliveryReconciliation run={value}/></Modal></AntApp></ConfigProvider>)}
function body(path:string,latest:typeof report|null=report){return path.includes("receipts")?{version:1,state:"pending",items:[],total:0,can_accept:latest?.state==="passed",can_reject:true,delegate:false,recipient_id:"recipient"}:{latest,history:latest?[latest]:[],total:latest?1:0,can_retry:true,recipient_id:"recipient",execution_state:"succeeded"}}
it("执行成功和对账失败分别显示，重新核对不发送交换写入请求",async()=>{
 const calls:string[]=[];const failed={...report,state:"failed",error_code:"SOURCE_UNAVAILABLE"};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST"){calls.push(path);return new Response(JSON.stringify({operation_id:"operation",status:"succeeded",resource_id:"run",result:{}}),{status:202})};return new Response(JSON.stringify(body(path,failed)))}));
 show();expect((await screen.findAllByText("对账失败")).length).toBeGreaterThan(0);expect(screen.getByText("已提交")).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"重核本次交付"}));await waitFor(()=>expect(calls).toEqual(["/api/v1/etl-runs/run/reconciliation"]));
 expect(screen.getByRole("button",{name:"确认接收"}).hasAttribute("disabled")).toBe(true);
});
it("验收表单在原窗口填写，刷新后保留说明且不偷偷替换所选报告版本",async()=>{
 const posted:unknown[]=[];let refreshed=false;
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posted.push(JSON.parse(String(init.body)));return new Response(JSON.stringify({code:"RECONCILIATION_VERSION_CHANGED",message:"对账报告已更新，请刷新并核对最新报告"}),{status:409})}
  const latest=refreshed?{...report,id:"report-2",version:2}:report;
  return new Response(JSON.stringify(body(path,latest)));
 }));
 show();await userEvent.click(await screen.findByRole("button",{name:"确认接收"}));
 await userEvent.type(screen.getByLabelText("验收说明"),"按交付范围完成核验");refreshed=true;
 await userEvent.click(screen.getByRole("button",{name:"刷新对账"}));
 await waitFor(()=>expect(screen.getByText("V2")).toBeTruthy());
 expect(screen.getByDisplayValue("按交付范围完成核验")).toBeTruthy();expect(screen.getAllByRole("dialog")).toHaveLength(1);
 await userEvent.click(screen.getByRole("button",{name:"保存验收回执"}));
 await waitFor(()=>expect(posted).toEqual([{decision:"accepted",reason:"按交付范围完成核验",expected_version:1,reconciliation_id:"report-1"}]));
 expect(await screen.findByText(/对账报告已更新，请刷新并核对最新报告/)).toBeTruthy();
});
it("未知执行结果和历史未核对运行不能接收",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify(body(path,null)))));
 show({...run,state:"unknown"});expect(await screen.findByText("未核对")).toBeTruthy();
 expect(screen.getByRole("button",{name:"确认接收"}).hasAttribute("disabled")).toBe(true);
 expect(screen.getByRole("button",{name:"重核本次交付"}).hasAttribute("disabled")).toBe(true);
});

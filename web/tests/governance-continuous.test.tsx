// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App,ConfigProvider} from "antd";
import {GovernanceContinuousFlow} from "../src/components/GovernanceContinuousFlow";
import type {GovernanceRecord} from "../src/api/governance";
import {useSession} from "../src/stores/session";
const record:GovernanceRecord={id:"flow",kind:"dataflow",department_id:"test",latest_version:1,version:1,state:"published",change_note:"",migration_note:"",published_at:"2026-10-27T00:00:00Z",definition:{name:"治理",description:"",owner_id:"governor",steward_id:"governor",dataflow:{nodes:[{id:"output",kind:"output",data_layer:"dws",inputs:[],infer_model:true}]}},created_at:"2026-10-27T00:00:00Z"};
beforeEach(()=>{useSession.setState({user:{user_id:"governor",username:"治理人员",department_id:"test",roles:["governor"],platforms:["governance"],csrf_token:"test"},ready:true,epoch:0,identityError:null});Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
it("先校验持续执行方案并传递业务参数，校验失败不会启动",async()=>{
 const posts:string[]=[];vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST"){posts.push(path);expect(JSON.parse(String(init.body)).parameters).toEqual({region:"A"});return Response.json({code:"STREAM_CDC_REQUIRED",message:"请先完成CDC采集"},{status:409})}return Response.json({items:[],total:0,page:1,page_size:20})}));
 render(<ConfigProvider theme={{token:{motion:false}}}><App><GovernanceContinuousFlow record={record} parameters={{region:"A"}} onSnapshot={()=>{}}/></App></ConfigProvider>);
 expect(screen.getByText("主题汇总 · DWS")).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"启动持续运行"}));
 await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0]).toContain("/execution-plan?mode=continuous");
 expect(await screen.findByText("请先完成CDC采集")).toBeTruthy();
});
it("停止持续运行后等待实际停止确认，不再提供重复结束按钮",async()=>{
 let ending=false;const posts:string[]=[];
 const stream=()=>({id:"stream",flow_id:"flow",flow_version:1,state:"running",desired_state:ending?"failed":"running",input_events:2,output_rows:2,rejected_rows:0,output_offset:2,updated_at:"2026-10-27T00:00:00Z"});
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push(path);ending=true;return Response.json(stream())}
  if(path.includes("definition_id"))return Response.json({items:[stream()],total:1,page:1,page_size:20});
  if(path.includes("/records"))return Response.json({items:[],total:0,page:1,page_size:20});
  return Response.json(stream());
 }));
 render(<ConfigProvider theme={{token:{motion:false}}}><App><GovernanceContinuousFlow record={record} parameters={{}} onSnapshot={()=>{}}/></App></ConfigProvider>);
 await userEvent.click(await screen.findByRole("button",{name:"停止持续运行"}));
 expect(posts).toEqual(["/api/v1/governance/streams/stream/stop"]);
 expect(await screen.findByText("正在结束任务")).toBeTruthy();
 expect(screen.queryByRole("button",{name:"停止持续运行"})).toBeNull();
});
it("流式启动前展示绑定模型的分层，不误报默认DWD",async()=>{
 const bound:GovernanceRecord={...record,definition:{...record.definition,dataflow:{nodes:[{id:"output",kind:"output",inputs:[],model:{id:"model",version:2}}]}}};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path.includes("/models/model/versions/2")?{definition:{model:{data_layer:"ads"}}}:{items:[],total:0})));
 render(<ConfigProvider theme={{token:{motion:false}}}><App><GovernanceContinuousFlow record={bound} parameters={{}} onSnapshot={()=>{}}/></App></ConfigProvider>);
 expect(await screen.findByText("应用服务 · ADS")).toBeTruthy();
 expect(screen.queryByText("标准明细 · DWD")).toBeNull();
});
it("采集、计算和落库分别展示证据，轮询时间不能冒充数据时效",async()=>{
 const stream={id:"stream",flow_id:"flow",flow_version:1,state:"running",desired_state:"running",input_events:2,output_rows:2,rejected_rows:0,output_offset:2,caught_up:true,updated_at:"2026-10-28T09:00:00Z",freshness:{source_capture:{state:"running",observed_at:"2026-10-28T08:59:00Z"},flink_compute:{state:"running",observed_at:"2026-10-28T08:59:00Z"},doris_write:{state:"readable",observed_at:"2026-10-28T08:59:00Z"},sources:[{node_id:"source",state:"running",observed_at:"2026-10-28T08:59:00Z",latest_event_at:"2026-10-28T08:00:00Z",forwarded_to_at:"2026-10-28T07:30:00Z",backlog_offsets:0}],processed_to_at:null,end_to_end_latency_ms:null,doris_output_offset:2}};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path.includes("definition_id")?{items:[stream],total:1}:path.includes("/records")?{items:[],total:0}:stream)));
 render(<ConfigProvider theme={{token:{motion:false}}}><App><GovernanceContinuousFlow record={record} parameters={{}} onSnapshot={()=>{}}/></App></ConfigProvider>);
 expect(await screen.findByText("来源采集")).toBeTruthy();
 expect(screen.getByText("Flink 计算")).toBeTruthy();expect(screen.getByText("Doris 写入")).toBeTruthy();
 expect(screen.getByText("已提交位点可读取")).toBeTruthy();
 expect(screen.getByText("已处理到的来源时间").parentElement?.textContent).toContain("未知");
 expect(screen.getByText("端到端数据延迟").parentElement?.textContent).toContain("未知");
 expect(screen.getByText("待转入计算的日志位点数").parentElement?.textContent).toContain("0");
 expect(screen.getByText(/落库追平不代表来源端到端已同步/)).toBeTruthy();
});
it("旧接口没有时效证据时显示未知",async()=>{
 const stream={id:"legacy",flow_id:"flow",flow_version:1,state:"running",desired_state:"running",input_events:2,output_rows:2,rejected_rows:0,output_offset:2,updated_at:"2026-10-28T09:00:00Z"};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path.includes("definition_id")?{items:[stream],total:1}:path.includes("/records")?{items:[],total:0}:stream)));
 render(<ConfigProvider theme={{token:{motion:false}}}><App><GovernanceContinuousFlow record={record} parameters={{}} onSnapshot={()=>{}}/></App></ConfigProvider>);
 expect(await screen.findByText("来源采集")).toBeTruthy();
 expect(screen.getByText("已处理到的来源时间").parentElement?.textContent).toContain("未知");
 expect(screen.getByText("端到端数据延迟").parentElement?.textContent).toContain("未知");
});

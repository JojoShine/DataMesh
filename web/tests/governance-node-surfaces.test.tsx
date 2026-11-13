// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App,ConfigProvider} from "antd";
import {GovernanceWorkflowTaskSummary} from "../src/components/GovernanceWorkflowTaskSummary";
import {GovernanceWorkflowTaskConfig} from "../src/components/GovernanceWorkflowTaskConfig";
import {GovernanceImpact} from "../src/components/GovernanceImpact";
import {GovernanceContinuousFlow} from "../src/components/GovernanceContinuousFlow";
import type {GovernanceRecord,GovernanceWorkflowTask,MetadataResource} from "../src/api/governance";
import {useSession} from "../src/stores/session";
const resource={resource_type:"table_resource" as const,resource_id:"table",contract_version:2};
const metadata:MetadataResource={object_id:"table",source_id:"source",qualified_name:"public.orders",display_name:"订单明细",resource_type:"table_resource",schema_version:2,fields:[{name:"region",logical_type:"string",nullable:false,primary_key:false,sensitive:false,precision:null,scale:null,business_name:"地区"}]};
const flow:GovernanceRecord={id:"flow",kind:"dataflow",version:3,latest_version:3,state:"published",department_id:"test",change_note:"",migration_note:"",published_at:"",created_at:"",definition:{name:"地区整理",description:"",owner_id:"owner",steward_id:"owner",dataflow:{nodes:[{id:"source_1",kind:"source",inputs:[],source:{resource,fields:["region"]}},{id:"trim_1",kind:"transform",inputs:[{node:"source_1"}],columns:[{source:"region",target:"region",rule:"trim"}]},{id:"quality_1",kind:"quality",inputs:[{node:"trim_1"}],template:{id:"plan",version:2}},{id:"output_1",kind:"output",inputs:[{node:"quality_1"}],infer_model:true}]}}};
const task:GovernanceWorkflowTask={id:"prepare",dataflow:{id:"flow",version:3},depends_on:[],condition:"all_success"};
beforeEach(()=>{useSession.setState({user:{user_id:"owner",username:"owner",department_id:"test",roles:["governor"],platforms:["governance"],csrf_token:"test"},ready:true,epoch:0,identityError:null});Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
function respond(path:string){
 if(path.includes("/dataflows/flow/versions/3"))return Response.json(flow);
 if(path.includes("/metadata/table/observations"))return Response.json({object:metadata});
 if(path.includes("/quality-templates/plan/versions/2"))return Response.json({...flow,id:"plan",kind:"quality_template",version:2,definition:{...flow.definition,name:"地区完整性",quality_template:{checks:[{field:"region",kind:"required",blocking:true}]}}});
 return Response.json({items:[],total:0});
}
function mount(content:React.ReactNode){return render(<ConfigProvider theme={{token:{motion:false}}}><App>{content}</App></ConfigProvider>)}
it("工作流任务展示固定数据流的来源、字段、清洗和检查规则",async()=>{
 const paths:string[]=[];vi.stubGlobal("fetch",vi.fn(async(path:string)=>{paths.push(path);return respond(path)}));
 mount(<GovernanceWorkflowTaskSummary task={task} tasks={[task]}/>);
 expect(await screen.findByText("数据流计算 · 地区整理 · v3")).toBeTruthy();
 await userEvent.click(screen.getByText("查看选择的数据、字段与处理规则"));
 expect(await screen.findByText(/订单明细 · v2；选取：地区（region）/)).toBeTruthy();
 expect(screen.getByText(/地区（region）：去除首尾空白/)).toBeTruthy();
 expect(await screen.findByText(/地区完整性 · v2；地区（region）：必填/)).toBeTruthy();
 expect(paths.some(path=>path.includes("/versions/1"))).toBe(false);
});
it("前置成果映射标签展示声明来源与字段",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>respond(path)));
 mount(<GovernanceWorkflowTaskConfig task={task} tasks={[task]} department="test" parameters={{}} onParameters={()=>{}} onChange={()=>{}}/>);
 expect((await screen.findAllByText(/订单明细 · v2；选取：地区（region）/)).some(element=>element.closest(".ant-form-item-label"))).toBe(true);
 expect(screen.queryByText("source_1 · 前置成果输入（可选）")).toBeNull();
});
it("持续运行显示来源的具体选择，结构版本变化不会混用新标签",async()=>{
 const stream={id:"stream",flow_id:"flow",flow_version:3,state:"running",desired_state:"running",freshness:{sources:[{node_id:"source_1",state:"running",backlog_offsets:0}]}};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>path.includes("definition_id")?Response.json({items:[stream],total:1}):path.includes("/streams/stream/records")?Response.json({items:[],total:0}):path.includes("/streams/stream")?Response.json(stream):respond(path)));
 mount(<GovernanceContinuousFlow record={flow} parameters={{}} onSnapshot={()=>{}}/>);
 expect(await screen.findByText(/订单明细 · v2；选取：地区（region）/)).toBeTruthy();
 expect(screen.queryByText("来源 1")).toBeNull();
});
it("工作流使用前置成果时明确展示替换关系",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>respond(path)));
 const replacement={...task,id:"later",inputs:{source_1:"prepare"}};
 mount(<GovernanceWorkflowTaskSummary task={replacement} tasks={[task,replacement]}/>);
 await userEvent.click(await screen.findByText("查看选择的数据、字段与处理规则"));
 expect(screen.getByText(/本次替换为前置成果：数据流计算 · prepare/)).toBeTruthy();
});

it("影响分析显示来源业务信息与规则，隐藏的比较值不能冒充空值或未配置",async()=>{
 const transformations={nodes:[...flow.definition.dataflow!.nodes,{id:"filter_1",kind:"filter",inputs:[{node:"source_1"}],condition:{field:"region",operator:"eq",value:null}}]};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>path.includes("/impact?")?Response.json({root:"run:run:1",nodes:[],edges:[],fields:{region:[{resource,field:"region"}]},transformations,truncated:false,restricted_dependencies:0}):respond(path)));
 mount(<GovernanceImpact id="run" kind="run"/>);
 await userEvent.click(await screen.findByRole("tab",{name:"字段与转换"}));
 expect(await screen.findByText(/地区（region）等于 比较值已隐藏/)).toBeTruthy();
 expect(screen.getByText("订单明细 · v2 · 地区（region）")).toBeTruthy();
 expect(screen.queryByText(/地区（region）等于 空值/)).toBeNull();
});

// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor,within} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App,ConfigProvider} from "antd";
import {GovernanceCapabilities} from "../src/pages/GovernanceCapabilities";
import {GovernanceReleases} from "../src/pages/GovernanceReleases";
import {GovernanceMasterRunView} from "../src/pages/GovernanceCapabilities";
import {useSession} from "../src/stores/session";
import type {GovernanceRecord} from "../src/api/governance";
vi.setConfig({testTimeout:15000});
beforeEach(()=>{history.replaceState(null,"","/platform/governance/metrics?workspace_id=space");useSession.setState({user:{user_id:"owner",username:"治理人员",department_id:"dept",roles:["governor"],platforms:["governance"],csrf_token:"test"},ready:true,epoch:0,identityError:null});Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
function show(child:React.ReactNode){render(<ConfigProvider theme={{token:{motion:false}}}><App>{child}</App></ConfigProvider>)}
it("指标修订提交真实口径及基准版本，保留精确金额定义",async()=>{
 const record:GovernanceRecord={id:"metric",kind:"dataflow",workspace_id:"space",department_id:"dept",version:2,latest_version:2,state:"published",change_note:"核实金额口径",migration_note:"",created_at:"2026-11-09T00:00:00Z",published_at:null,definition:{name:"报名费用",description:"按学校统计金额",owner_id:"owner",steward_id:"owner",metric:{source:{resource:{resource_type:"table_resource",resource_id:"source",contract_version:3},fields:["school","amount"]},dimensions:["school"],filters:[],aggregates:[{source:"amount",operation:"sum",target:"total_amount"}],period:"all_time",time_field:"",from:"",until:""}}};
 const posts:{path:string;body:unknown}[]=[];vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return Response.json(record,{status:201})}if(path.includes("/metadata/source/contract"))return Response.json({object_id:"source",qualified_name:"报名收费",schema_version:3,resource_type:"table_resource",fields:[{name:"school",logical_type:"string"},{name:"amount",logical_type:"decimal",precision:20,scale:6}]});return Response.json({items:[record],total:1,page:1,page_size:15})}));
 show(<GovernanceCapabilities kind="metric"/>);await userEvent.click(await screen.findByRole("button",{name:"修订版本"}));expect(screen.getAllByRole("dialog")).toHaveLength(1);await userEvent.clear(screen.getByLabelText("名称"));await userEvent.type(screen.getByLabelText("名称"),"本年度报名费用");await userEvent.click(screen.getByRole("button",{name:"保存草稿"}));
 await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0]).toMatchObject({path:"/api/v1/governance/metrics/metric/versions?workspace_id=space",body:{base_version:2,definition:{name:"本年度报名费用",metric:record.definition.metric}}});
});
it("从主数据入口跳转的发布表单固定正确候选，不替用户编造迁移安排",async()=>{
 history.replaceState(null,"","/platform/governance/releases?workspace_id=space&kind=master&candidate=master&candidate_version=1");const candidate={id:"master",kind:"master",version:1,latest_version:1,state:"draft",definition:{name:"法人主数据"}};const posts:unknown[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST"){posts.push(JSON.parse(String(init.body)));return Response.json({id:"release",version:1,state:"submitted",body:{}},{status:201})}return Response.json(path.includes("/masters/master/versions/1")?candidate:{items:path.includes("/masters?")?[candidate]:[],total:1,page:1,page_size:15})}));
 show(<GovernanceReleases/>);const dialog=await screen.findByRole("dialog");await userEvent.type(within(dialog).getByLabelText("变更原因"),"核实法人主体匹配键");await userEvent.click(within(dialog).getByRole("button",{name:"提交审核"}));await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0]).toMatchObject({kind:"master",candidate:{id:"master",version:1},base_version:0,reason:"核实法人主体匹配键",migration_note:""});
});
it("主数据运行只展示真实候选，不把成功执行当作实体已确认",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path.includes("/runs/run")?{id:"run",kind:"master",state:"succeeded",processed:100,result:{entities:80,conflicting_entities:5}}:{items:[{id:"candidate",entity_id:"entity1234567890",payload:[{source:"registration",record_id:"1",ordinal:1,values:{name:"甲"}}],conflicts:["name"],confirmed:false}],field_labels:{name:"主体名称"},total:1,page:1,page_size:15})));
 show(<GovernanceMasterRunView id="run"/>);expect(await screen.findByRole("button",{name:"entity123456"})).toBeTruthy();expect(screen.getByText("主体名称（name）")).toBeTruthy();expect(screen.queryByText("已确认")).toBeNull();
});

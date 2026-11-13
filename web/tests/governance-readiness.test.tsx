// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App,ConfigProvider} from "antd";
import {MasterCandidates} from "../src/components/GovernanceMasterCandidates";
import {GovernanceAlerts} from "../src/pages/GovernanceAlerts";
import {useSession} from "../src/stores/session";
beforeEach(()=>{history.replaceState(null,"","/platform/governance/masters?workspace_id=space");useSession.setState({user:{user_id:"owner",username:"治理人员",department_id:"dept",roles:["governor"],platforms:["governance"],csrf_token:"token"},ready:true,epoch:0,identityError:null});Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
function show(child:React.ReactNode){render(<ConfigProvider theme={{token:{motion:false}}}><App>{child}</App></ConfigProvider>)}
it("批量确认先预览中文字段和来源，填写原因后明确确认",async()=>{
 const posts:{path:string;body:Record<string,unknown>}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return Response.json(path.includes("batch-preview")?{preview_token:"fixed",items:[{candidate_id:"candidate",entity_id:"entity",expected_version:0,payload:{name:"甲"},sources:[{source:"registration",record_id:"P1",ordinal:1,values:{name:"甲"}}]}]}:{items:[],total:1})}
  return Response.json({items:[{id:"candidate",entity_id:"entity1234567890",run_id:"run",payload:[{source:"registration",record_id:"P1",ordinal:1,values:{name:"甲"}}],conflicts:[],confirmed:false}],field_labels:{name:"主体名称"},total:1,page:1,page_size:15});
 }));
 show(<MasterCandidates runID="run"/>);await screen.findByRole("button",{name:"entity123456"});await userEvent.click(screen.getAllByRole("checkbox")[1]);await userEvent.click(screen.getByRole("button",{name:"预览批量确认（1）"}));
 expect(await screen.findByText('主体名称（name）："甲"')).toBeTruthy();expect(screen.getByText("registration / P1")).toBeTruthy();expect(posts).toHaveLength(1);expect(screen.getByRole("button",{name:"确认所选实体"})).toHaveProperty("disabled",true);
 await userEvent.type(screen.getByLabelText("批量确认原因"),"核对业务登记来源与实体取值");await userEvent.click(screen.getByRole("button",{name:"确认所选实体"}));
 await waitFor(()=>expect(posts).toHaveLength(2));expect(posts[1].body).toMatchObject({candidate_ids:["candidate"],preview_token:"fixed",reason:"核对业务登记来源与实体取值"});
});
it("整改通知标记已读并随刷新更新，筛选向接口传递阅读状态",async()=>{
 let read=false;let loads=0;const paths:string[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  paths.push(path);if(path.includes("/seen")&&init?.method==="POST"){read=true;return Response.json({id:"notice",seen_at:"2026-11-10T01:00:00Z"})}
  if(path.includes("issue-notices")){loads++;return Response.json({items:[{id:"notice",issue_id:"issue",code:"assigned",created_at:"2026-11-10T01:00:00Z",seen_at:read?"2026-11-10T01:00:00Z":null}],total:1,page:1,page_size:15})}
  return Response.json({items:[],total:0});
 }));
 show(<GovernanceAlerts/>);await userEvent.click(await screen.findByRole("button",{name:"标记已读"}));await screen.findByText("已读");expect(screen.getByRole("button",{name:"标记已读"})).toHaveProperty("disabled",true);const before=loads;await userEvent.click(screen.getByRole("button",{name:/刷\s*新/}));await waitFor(()=>expect(loads).toBeGreaterThan(before));
 await userEvent.click(screen.getByRole("combobox",{name:"通知阅读状态"}));await userEvent.click(await screen.findByText("未读通知",{selector:".ant-select-item-option-content"}));await waitFor(()=>expect(paths.some(path=>path.includes("seen=false"))).toBe(true));
});

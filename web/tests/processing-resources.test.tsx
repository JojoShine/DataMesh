// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App,ConfigProvider} from "antd";
import {ResourcePermissions} from "../src/pages/ResourcePermissions";
import {useSession} from "../src/stores/session";
beforeEach(()=>{useSession.setState({user:{user_id:"approver",username:"审批人",department_id:"provider",roles:["provider"],platforms:["exchange"],csrf_token:"test"},ready:true,epoch:0,identityError:null});Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
it("加工授权可选择已登记表资源，保留资源类型、版本和选定字段且不创建使用申请",async()=>{
 const posts:{path:string;body:Record<string,unknown>}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return Response.json({id:"authorization"})}
  if(path.includes("directory-responsibles"))return Response.json({items:[{user_id:"executor",username:"治理执行人"}]});
  if(path.includes("resource-directories"))return Response.json({items:[{directory_id:"directory",definition:{name:"业务资料目录"}}],total:1});
  if(path.includes("managed-resources"))return Response.json({items:[{resource:{resource_type:"table_resource",resource_id:"table",contract_version:4},name:"业务资料",fields:[{name:"record_id",business_name:"记录编号",logical_type:"string"}]}]});
  return Response.json({items:[],total:0});
 }));
 render(<ConfigProvider theme={{token:{motion:false}}}><App><ResourcePermissions processingOnly/></App></ConfigProvider>);
 await userEvent.click(screen.getByRole("button",{name:"批准加工授权"}));
 await userEvent.click(screen.getByLabelText("来源目录"));await userEvent.click(await screen.findByText("业务资料目录"));
 await userEvent.click(screen.getByLabelText("来源资源"));await userEvent.click(await screen.findByText("业务资料 · v4"));
 await userEvent.click(screen.getByLabelText("允许处理字段"));await userEvent.click(await screen.findByText("记录编号（record_id）"));await userEvent.keyboard("{Escape}");
 await userEvent.click(screen.getByLabelText("加工执行人"));await userEvent.click(await screen.findByText("治理执行人"));
 await userEvent.type(screen.getByLabelText("来源责任部门批准记录"),"APPROVAL-2026-01");
 await userEvent.click(screen.getByRole("checkbox",{name:"允许形成派生成果"}));
 await userEvent.click(screen.getByRole("button",{name:"确认配置"}));
 await waitFor(()=>expect(posts).toHaveLength(1));
 expect(posts[0].path).toBe("/api/v1/processing-authorizations");
 expect(posts[0].body).toMatchObject({resource:{resource_type:"table_resource",resource_id:"table",contract_version:4},user_id:"executor",fields:["record_id"],purpose:"数据治理",allow_derived:true});
});

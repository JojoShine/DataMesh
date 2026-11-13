// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {GovernanceWorkspaces,GovernanceWorkspaceSources} from "../src/pages/GovernanceWorkspaces";
import {GovernanceWorkspaceProvider,GovernanceWorkspaceBar} from "../src/components/GovernanceWorkspaceContext";
import {GovernanceDataflows} from "../src/pages/GovernanceDataflows";
import {navigate} from "../src/app/navigation";
import {useSession} from "../src/stores/session";
vi.setConfig({testTimeout:15000});
const space={id:"customer-space",name:"客户整理",description:"客户分析使用",department_id:"dept",owner_id:"user",state:"active",is_legacy:false,max_concurrent_runs:2,version:1,created_at:"2026-10-23T01:00:00Z",updated_at:"2026-10-23T01:00:00Z"};
const resource={object_id:"source",resource_type:"table_resource",qualified_name:"public.customers",schema_version:1,fields:[{name:"id",logical_type:"integer",primary_key:true,nullable:false,sensitive:false},{name:"name",logical_type:"string",nullable:true,sensitive:false}]};
const record={id:"flow",workspace_id:space.id,kind:"dataflow",department_id:"dept",latest_version:1,version:1,state:"published",definition:{name:"客户处理",owner_id:"user",steward_id:"user",dataflow:{nodes:[{id:"source_1",kind:"source",inputs:[],source:{resource:{resource_type:"table_resource",resource_id:"source",contract_version:1},fields:["id","name"]}},{id:"output_1",kind:"output",inputs:[{node:"source_1"}],model:{id:"model",version:1}}]}}};
beforeEach(()=>{Object.defineProperty(Element.prototype,"scrollIntoView",{configurable:true,value:vi.fn()});history.replaceState(null,"","/platform/governance/workspaces");useSession.setState({user:{user_id:"user",username:"治理人员",department_id:"dept",roles:["governor"],platforms:["governance"],csrf_token:"fixture"},ready:true,epoch:0,identityError:null});window.scrollTo=vi.fn();Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();Reflect.deleteProperty(Element.prototype,"scrollIntoView");vi.unstubAllGlobals()});
function fixture(posts:{path:string;body:unknown}[]){vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
 if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return Response.json(space,{status:201})}
 if(path==="/api/v1/governance/workspaces/customer-space")return Response.json(space);
 if(path.includes("/overview"))return Response.json({workspace:space,source_count:0,model_count:0,dataflow_count:0,workflow_count:0,run_states:[],recent_runs:[],recent_results:[],quality_summary:{checked:0,passed:0,pass_rate:null,scope:"检查行次"}});
 if(path.includes("/dataflows?")&&!path.includes("/runs"))return Response.json({items:[record],total:1,page:1,page_size:20});
 if(path.endsWith("/layout")||path.includes("/layout?"))return Response.json({revision:0,positions:{},viewport:{x:0,y:0,zoom:1}});
 if(path.includes("/observations"))return Response.json({object:resource,items:[]});
 if(path.includes("/metadata?")||path.includes("/source-candidates?"))return Response.json({items:[resource],total:1,page:1,page_size:20});
 if(path.includes("/members"))return Response.json({items:[{user_id:"user",username:"治理人员",department_id:"dept"}]});
 return Response.json({items:[],total:0,page:1,page_size:20});
}));}
function show(node:React.ReactNode){render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><GovernanceWorkspaceProvider>{node}</GovernanceWorkspaceProvider></AntApp></ConfigProvider>)}
it("创建空间只要求名称，帮助不自动弹出，创建后进入新空间",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);show(<GovernanceWorkspaces/>);
 await userEvent.click(screen.getByRole("button",{name:"新建治理空间"}));expect(screen.getAllByRole("dialog")).toHaveLength(1);
 await userEvent.type(screen.getByLabelText("空间名称"),"客户整理");await userEvent.click(screen.getByRole("button",{name:"创建并进入"}));
 await waitFor(()=>expect(location.search).toBe("?workspace_id=customer-space"));expect(posts).toEqual([{path:"/api/v1/governance/workspaces",body:{name:"客户整理",description:"",max_concurrent_runs:2}}]);expect(screen.queryByText(/先认识流程，再开始工作/)).toBeNull();
});
it("来源关联使用明确资源与版本，保持单层 dialog",async()=>{
 history.replaceState(null,"","/platform/governance/space-sources?workspace_id=customer-space");const posts:{path:string;body:unknown}[]=[];fixture(posts);show(<GovernanceWorkspaceSources/>);
 await userEvent.click(await screen.findByRole("button",{name:"选择共享交换资源"}));await screen.findByText("public.customers");
 await userEvent.click(screen.getAllByRole("checkbox")[1]);await userEvent.click(screen.getByRole("button",{name:"关联所选来源"}));
 await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0]).toEqual({path:"/api/v1/governance/workspaces/customer-space/sources",body:{workspace_id:"customer-space",resource:{resource_type:"table_resource",resource_id:"source",contract_version:1}}});
});
it("切换空间前保留未保存的数据流，明确放弃才离开",async()=>{
 history.replaceState(null,"","/platform/governance/dataflows?workspace_id=customer-space");const posts:{path:string;body:unknown}[]=[];fixture(posts);show(<GovernanceDataflows/>);
 await userEvent.click(await screen.findByRole("button",{name:"基于此版本修订"}));await userEvent.click(screen.getByRole("button",{name:"添加整理步骤"}));await userEvent.click(await screen.findByRole("menuitem",{name:"清理文字"}));
 navigate("/platform/governance/workspaces?workspace_id=product-space");expect(location.search).toBe("?workspace_id=customer-space");
 await userEvent.click(await screen.findByRole("button",{name:"继续编辑"}));expect(screen.getByRole("button",{name:/^清理文字/})).toBeTruthy();
 navigate("/platform/governance/workspaces?workspace_id=product-space");await userEvent.click(await screen.findByRole("button",{name:"放弃修改并返回"}));
 await waitFor(()=>expect(location.search).toBe("?workspace_id=product-space"));expect(posts).toHaveLength(0);
});

it("归档空间可以查看数据流和帮助，但不能修改或运行",async()=>{
 history.replaceState(null,"","/platform/governance/dataflows?workspace_id=customer-space");const posts:{path:string;body:unknown}[]=[];fixture(posts);
 const original=globalThis.fetch;vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>path==="/api/v1/governance/workspaces/customer-space"?Promise.resolve(Response.json({...space,state:"archived"})):original(path,init)));
 show(<GovernanceDataflows/>);
 expect((await screen.findByRole("button",{name:"客户处理"})).hasAttribute("disabled")).toBe(false);
 expect(screen.getByRole("button",{name:"基于此版本修订"}).hasAttribute("disabled")).toBe(true);
 expect(screen.getByRole("button",{name:/^运\s*行$/}).hasAttribute("disabled")).toBe(true);
 expect(screen.getByRole("button",{name:"关闭当前数据流"}).hasAttribute("disabled")).toBe(false);
 await userEvent.click(screen.getByRole("button",{name:/更\s*多/}));await userEvent.click(await screen.findByRole("menuitem",{name:"使用帮助"}));
 expect(await screen.findByRole("dialog")).toBeTruthy();
 expect(posts).toHaveLength(0);
});

it("旧空间链接直接进入完整看板",async()=>{
 history.replaceState(null,"","/platform/governance/workspaces?workspace_id=customer-space");fixture([]);show(<div>业务页面</div>);
 await waitFor(()=>expect(location.pathname).toBe("/platform/governance/overview"));expect(location.search).toBe("?workspace_id=customer-space");
});
it("切换空间保持功能页，清除旧空间选中对象",async()=>{
 history.replaceState(null,"","/platform/governance/dataflows?workspace_id=customer-space&definition_id=old-flow&version=2&run_id=old-run");fixture([]);
 const original=globalThis.fetch;vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>path.startsWith("/api/v1/governance/workspaces?")?Promise.resolve(Response.json({items:[space,{...space,id:"product-space",name:"商品整理"}],total:2})):path==="/api/v1/governance/workspaces/product-space"?Promise.resolve(Response.json({...space,id:"product-space",name:"商品整理"})):original(path,init)));
 show(<GovernanceWorkspaceBar/>);await userEvent.click(await screen.findByRole("combobox",{name:"切换治理空间"}));await userEvent.click(await screen.findByText("商品整理",{selector:".ant-select-item-option-content"}));
 await waitFor(()=>expect(location.pathname+location.search).toBe("/platform/governance/dataflows?workspace_id=product-space"));
});
it("没有空间参数时先确定空间再展示业务页面",async()=>{
 history.replaceState(null,"","/platform/governance/overview");fixture([]);
 const original=globalThis.fetch;vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>path.startsWith("/api/v1/governance/workspaces?")?Promise.resolve(Response.json({items:[space],total:1})):original(path,init)));
 show(<div>业务页面</div>);await waitFor(()=>expect(location.search).toBe("?workspace_id=customer-space"));await screen.findByText("业务页面");
});

it("当前空间设置直接使用单层弹窗，保存后保留当前功能及未提交表单",async()=>{
 history.replaceState(null,"","/platform/governance/dataflows?workspace_id=customer-space");const posts:{path:string;body:unknown}[]=[];fixture(posts);
 show(<><GovernanceWorkspaceBar/><input aria-label="工作区未保存内容" defaultValue=""/></>);
 await userEvent.type(await screen.findByLabelText("工作区未保存内容"),"待保存的数据流");
 await userEvent.click(await screen.findByRole("button",{name:"空间管理"}));await userEvent.click(await screen.findByRole("menuitem",{name:"当前空间设置"}));
 expect(screen.getAllByRole("dialog")).toHaveLength(1);await userEvent.clear(screen.getByLabelText("空间名称"));await userEvent.type(screen.getByLabelText("空间名称"),"客户分析");await userEvent.click(screen.getByRole("button",{name:/保\s*存/}));
 await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0].path).toBe("/api/v1/governance/workspaces/customer-space/update");
 await waitFor(()=>expect(screen.queryByRole("dialog")).toBeNull());expect(location.pathname+location.search).toBe("/platform/governance/dataflows?workspace_id=customer-space");expect((screen.getByLabelText("工作区未保存内容") as HTMLInputElement).value).toBe("待保存的数据流");
});

it("来源可全选并一次关联多份，失败项保留供重试",async()=>{
 history.replaceState(null,"","/platform/governance/space-sources?workspace_id=customer-space");const posts:{path:string;body:unknown}[]=[];fixture(posts);
 const original=globalThis.fetch;let failSecond=true;
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(path.includes("/source-candidates?"))return Response.json({items:[resource,{...resource,object_id:"second",qualified_name:"public.products"}],total:2,page:1,page_size:20});
  if(init?.method==="POST"){
   const body=JSON.parse(String(init.body));posts.push({path,body});
   if(body.resource.resource_id==="second"&&failSecond)return Response.json({code:"SOURCE_UNAVAILABLE",message:"来源暂时不可用"},{status:409});
   return Response.json(space);
  }
  return original(path,init);
 }));
 show(<GovernanceWorkspaceSources/>);await userEvent.click(await screen.findByRole("button",{name:"选择共享交换资源"}));await screen.findByText("public.products");
 await userEvent.click(screen.getAllByRole("checkbox")[0]);expect(screen.getByText("已选 2 份")).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"关联所选来源"}));await screen.findByText("部分来源未关联");expect(posts).toHaveLength(2);expect(screen.getByText("已选 1 份")).toBeTruthy();
 failSecond=false;await userEvent.click(screen.getByRole("button",{name:"关联所选来源"}));await waitFor(()=>expect(screen.queryByRole("dialog")).toBeNull());
 expect(posts).toHaveLength(3);expect(posts[2].body).toEqual({workspace_id:"customer-space",resource:{resource_type:"table_resource",resource_id:"second",contract_version:1}});
});

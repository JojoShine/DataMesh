// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,fireEvent,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App} from "../src/app/App";
import {navigate} from "../src/app/navigation";
import {request} from "../src/api/client";
import {useSession} from "../src/stores/session";
const principal={user_id:"experience",username:"experience",department_id:"体验",roles:["consumer"],platforms:["exchange","assets"] as ("exchange"|"assets")[],csrf_token:"fixture",public_test:true};
const asset={dataset_id:"asset",name:"人口基础信息库",description:"已审核的业务说明",provider_department_id:"公安部门",field_count:1,usage_restrictions:"按审批范围使用",resource_type:"governed_dataset",contract_version:1,updated_at:"2026-11-10T00:00:00Z"};
const detail={...asset,details:{resource_code:"POPULATION",provider_name:"公安部门",classification:"base",classification_path:"人口",update_frequency:"每日",format:"database",data_layer:"governed_dataset",items:[{name:"person_id",business_name:"人员编号",logical_type:"string",nullable:false,sensitive:false,precision:null,scale:null}],sharing_type:"conditional",sharing_method:"query",sharing_conditions:"按用途审批",open_conditions:"公开目录",security_classification:"内部",legal_basis:"职责依据"}};
let calls:string[]=[];
beforeEach(()=>{localStorage.clear();history.replaceState(null,"","/");useSession.setState({user:principal,ready:true,epoch:0,identityError:null});Object.defineProperty(window,"matchMedia",{writable:true,value:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}})});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});calls=[];vi.stubGlobal("scrollTo",vi.fn());
 vi.stubGlobal("fetch",vi.fn(async(input:string)=>{const path=String(input);calls.push(path);if(path==="/api/v1/me")return Response.json(principal);if(path==="/api/public/assets/asset")return Response.json(detail);if(path.startsWith("/api/public/assets"))return Response.json({items:[asset],departments:["公安部门"],total:1,page:1,page_size:100});throw new Error("体验页面不应请求内部接口："+path)}));
});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
it.each([{name:/共享交换.*连接原始数据/,path:"exchange",title:"资源目录",menus:["数据源管理","CDC 接入任务","ETL 数据交换","目录编码配置","数据服务","目录与挂载审核"]},{name:/数据治理.*统一标准/,path:"governance",title:"治理看板",menus:[]},{name:/数据资产.*发现并申请/,path:"assets",title:"资产发现",menus:["数据产品","契约核验","认证与生命周期","我的使用统计","我的提醒"]},{name:/数据安全.*保障数据使用/,path:"security",title:"安全总览",menus:["人员与账号","组织与部门","策略预检","水印管理","审计记录","资源权限"]}])("$path 入口保留全部原业务菜单和页面，公开目录可读，身份权限没有提升",async(entry)=>{
 render(<App/>);

  fireEvent.click(await screen.findByRole("button",{name:entry.name}));await screen.findByRole("heading",{name:entry.title});
  for(const name of entry.menus)expect(screen.getByRole("menuitem",{name:new RegExp(name)})).toBeTruthy();
  if(entry.path==="exchange"||entry.path==="assets")expect(await screen.findByRole("button",{name:asset.name})).toBeTruthy();
  if(entry.path==="governance"){
   fireEvent.click(screen.getByRole("button",{name:/准备数据/}));expect(await screen.findByRole("menuitem",{name:/数据建模/})).toBeTruthy();
   fireEvent.click(screen.getByRole("button",{name:/其他功能/}));expect(await screen.findByRole("menuitem",{name:/空间管理/})).toBeTruthy();
  }
  expect(screen.queryByText("当前账号没有此功能权限")).toBeNull();
  fireEvent.click(screen.getAllByRole("button",{name:"返回资产首页"})[0]);await waitFor(()=>expect(location.pathname).toBe("/"));
 expect(useSession.getState().user?.roles).toEqual(["consumer"]);
 expect(calls.every(path=>path==="/api/v1/me"||path.startsWith("/api/public/assets"))).toBe(true);
},20000);
it("原业务页面可打开表单，内部记录明确说明，所有提交都在请求前被阻止",async()=>{
 history.replaceState(null,"","/platform/exchange/sources");render(<App/>);
 await screen.findByRole("heading",{name:"数据源管理"});await screen.findByText("此区域的内部记录未公开");
 await userEvent.click(screen.getByRole("button",{name:/登记数据源|新建数据源|新增数据源/}));
 expect(await screen.findByLabelText("数据源名称")).toBeTruthy();
 for(const path of ["/api/v1/sources","/api/v1/accounts"]){
  await expect(request(path,{method:"POST",body:"{}"})).rejects.toMatchObject({code:"EXPERIENCE_READ_ONLY"});
 }
 expect(calls.some(path=>path.includes("/sources")||path.includes("/accounts"))).toBe(false);
 navigate("/platform/security/accounts");await screen.findByRole("heading",{name:"人员与账号"});
 expect(screen.queryByText("当前账号没有此功能权限")).toBeNull();
},20000);
it("普通账号继续使用原接口和角色过滤",async()=>{
 useSession.setState({user:{...principal,public_test:false}});
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path==="/api/v1/me"?{...principal,public_test:false}:{items:[],total:0})));
 await request("/api/v1/assets/directories");expect(fetch).toHaveBeenCalledWith("/api/v1/assets/directories",expect.anything());
 history.replaceState(null,"","/platform/security/accounts");render(<App/>);await screen.findByText("当前账号没有此功能权限");
});
it("真实业务体验显示真实操作说明，读取内部资产并保留角色限制",async()=>{
 const full={...principal,public_test:false,full_experience:true};useSession.setState({user:full});
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>{calls.push(path);return Response.json(path==="/api/v1/me"?full:{items:[],total:0,page:1,page_size:15})}));
 history.replaceState(null,"","/platform/assets/catalog");render(<App/>);
 expect(await screen.findByText("真实业务体验")).toBeTruthy();
 await userEvent.hover(screen.getByText("真实业务体验"));
 expect(await screen.findByRole("tooltip")).toHaveProperty("textContent","使用现有业务数据，操作会真实保存和执行；遵循正常角色权限、独立审核与审计追踪。");
 await waitFor(()=>expect(calls.some(path=>path.startsWith("/api/v1/assets/"))).toBe(true));
 expect(calls.some(path=>path.startsWith("/api/public/"))).toBe(false);
 expect(screen.queryByText("只读体验")).toBeNull();
 expect(screen.queryByRole("button",{name:"重置体验空间"})).toBeNull();
 navigate("/platform/exchange/directory");await screen.findByRole("heading",{name:"资源目录"});
 expect(screen.queryByRole("menuitem",{name:/数据源管理/})).toBeNull();
 navigate("/platform/security/accounts");expect(await screen.findByText("当前账号没有此功能权限")).toBeTruthy();
});
it("真实治理体验使用现有空间，没有浏览器重置与示例说明",async()=>{
 const full={...principal,public_test:false,full_experience:true,roles:["governor"],platforms:["governance" as const]};useSession.setState({user:full});
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>{calls.push(path);return Response.json(path==="/api/v1/me"?full:{items:[],total:0,page:1,page_size:15})}));
 history.replaceState(null,"","/platform/governance/workspaces");render(<App/>);
 expect(await screen.findByText("真实业务体验")).toBeTruthy();
 await waitFor(()=>expect(calls.some(path=>path.startsWith("/api/v1/governance/workspaces"))).toBe(true));
 expect(screen.queryByRole("button",{name:"重置体验空间"})).toBeNull();
 expect(screen.queryByText("治理体验 · 当前浏览器生效")).toBeNull();
 expect(screen.queryByRole("button",{name:/其他功能/})).toBeNull();
 expect(screen.queryByRole("button",{name:"准备数据"})).toBeTruthy();
});

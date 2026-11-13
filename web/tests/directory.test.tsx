// @vitest-environment jsdom
import {ResourceMountEditor} from "../src/pages/ResourceMountEditor";
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor,within} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider,Modal} from "antd";
import zhCN from "antd/locale/zh_CN";
import {ResourceDirectory} from "../src/pages/ResourceDirectory";
import {DirectoryCenter} from "../src/pages/DirectoryCenter";
import {DirectoryWorkspace} from "../src/pages/DirectoryWorkspace";
import {ApplyModal} from "../src/pages/Catalog";
import type {Dataset,ResourceDirectory as Directory,ResourceMount} from "../src/api/types";
import {useSession} from "../src/stores/session";
const row:Directory={directory_id:"directory-test",version:1,state:"published",department_id:"测试部门",revision_id:"r1",revision_state:"approved",published_revision_id:"r1",reason:"",available:false,sharing_snapshot:null,resource:null,fields:[],permissions:{manage_resource:false,operate_resource:false},definition:{name:"测试资源",description:"已编目，尚未挂载",classification:"department",classification_path:"test",resource_code:"TEST-1",dictionary_id:"dict-test",provider_name:"测试部门",provider_code:"TEST",responsible_user_id:"p",update_frequency:"每日",format:"database",data_layer:"governed_dataset",items:[],sharing_type:"conditional",sharing_conditions:"测试用途",non_sharing_reason:"",legal_basis:"",sharing_method:"query",usage_restrictions:"测试",security_classification:"一般数据",open_to_public:false,open_conditions:"",department_approval_ref:"test",assessment_ref:"test",related_resource_codes:[]}};
beforeEach(()=>{
 ConfigProvider.config({theme:{token:{motion:false}}});
 history.replaceState(null,"","/platform/exchange/directory");
 useSession.setState({user:{user_id:"p",username:"tester",department_id:"测试部门",roles:["consumer"],platforms:["exchange","assets"],csrf_token:"test-only"},ready:true,epoch:0,identityError:null});
 Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});
 vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});window.scrollTo=vi.fn();
});
afterEach(()=>{Modal.destroyAll();cleanup();vi.unstubAllGlobals()});
function show(element:React.ReactNode){return render(<ConfigProvider locale={zhCN} theme={{token:{motion:false}}}><AntApp>{element}</AntApp></ConfigProvider>)}
it("目录名称前显示去重图标，共享方式列直接展示全部支持方式",async()=>{
 const resources:NonNullable<Directory["resources"]>=(["governed_dataset","table_resource","file_resource","api_resource"] as const).map((type,index)=>({resource:{resource_type:type,resource_id:`resource-${index}`,contract_version:1},name:`资源说明${index}`,fields:[],available:true,sharing_snapshot:null}));
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path.includes("resource-access")?{}:{items:[{...row,resources}],total:1})));
 show(<ResourceDirectory/>);
 const name=await screen.findByRole("button",{name:row.definition.name});const tr=name.closest("tr");if(!tr)throw new Error("缺少目录行");
 for(const label of ["数据库","文件","接口"])expect(within(tr).getAllByRole("img",{name:label})).toHaveLength(1);
 expect(within(tr).getByRole("group",{name:"资源形式"}).compareDocumentPosition(name)&Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
 expect(screen.getByRole("columnheader",{name:"共享方式"})).toBeTruthy();
 expect(within(tr).getByRole("cell",{name:"数据库查询、文件下载、接口调用"})).toBeTruthy();
 expect(within(tr).queryByText("资源说明0")).toBeNull();
 await userEvent.click(name);expect(within(await screen.findByRole("dialog")).getByText("资源说明0")).toBeTruthy();
});
it("申请治理成果不会误用同编号的原始资源挂载",async()=>{
 const raw={...row,available:true,resource:{resource_type:"raw_object",resource_id:"same-id",contract_version:1},fields:[]};
 vi.stubGlobal("fetch",vi.fn(async()=>new Response(JSON.stringify({items:[raw],total:1,page:1,page_size:20}),{status:200})));
 show(<ApplyModal resource={{resource_type:"governed_dataset",resource_id:"same-id",contract_version:1}} fields={[]} onClose={()=>{}}/>);
 await screen.findByText("资源尚未完成有效目录挂载，当前不能申请");
 expect(screen.getByRole("button",{name:"提交申请"}).hasAttribute("disabled")).toBe(true);
});
it("原目录同时展示原始与治理数据，申请仅使用所选成果的共享字段",async()=>{
 const original={resource_type:"raw_object" as const,resource_id:"original",contract_version:1};
 const governed={resource_type:"governed_dataset" as const,resource_id:"cleaned",contract_version:2};
 const variants:Directory={...row,available:true,resource:original,definition:{...row.definition,data_layer:"raw_object"},resources:[
  {name:"原始客户",resource:original,fields:[{name:"original_only",logical_type:"string",nullable:true,primary_key:false,sensitive:false,precision:null,scale:null}],available:true,sharing_snapshot:null},
  {name:"治理后客户",resource:governed,fields:[{name:"clean_name",logical_type:"string",nullable:true,primary_key:false,sensitive:false,precision:null,scale:null}],available:true,sharing_snapshot:null,governance:{single_source:true,source:original,source_name:"原始客户",provider_department_id:"测试部门",original_directory_id:row.directory_id,original_directory_name:row.definition.name,steps:[{kind:"transform",name:"清理姓名",description:"去除姓名首尾空格"}]}},
 ]};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify(path.includes("resource-access")?{}:{items:[variants],total:1,page:1,page_size:20}),{status:200})));
 show(<ResourceDirectory/>);
 await userEvent.click(await screen.findByRole("button",{name:"测试资源"}));
 const detail=screen.getByRole("dialog");
 expect(within(detail).getByRole("radio",{name:"原始客户"})).toBeTruthy();
 expect(within(detail).getByText("治理后客户")).toBeTruthy();
 expect(within(detail).getByText(/去除姓名首尾空格/)).toBeTruthy();
 await userEvent.click(within(detail).getByRole("radio",{name:"治理后客户"}));
 await userEvent.click(within(detail).getByRole("button",{name:"申请使用"}));
 expect(await screen.findByRole("checkbox",{name:"clean_name"})).toBeTruthy();
 expect(screen.queryByRole("checkbox",{name:"original_only"})).toBeNull();
 expect(screen.getAllByRole("dialog")).toHaveLength(1);
});
it("治理成果关联原目录时直接预填成果和映射，不替换原始挂载",async()=>{
 history.replaceState(null,"","/platform/exchange/directory?view=manage&action=mount&dataset_id=cleaned&directory_id=directory-test");
 useSession.setState(s=>({user:s.user?{...s.user,roles:["operator"]}:null}));
 const originalDirectory={...row,permissions:{manage_resource:false,operate_resource:true},definition:{...row.definition,data_layer:"raw_object",items:[{name:"id",business_name:"客户编号",logical_type:"integer",nullable:false,sensitive:false,length:null,precision:null,scale:null,description:""}]}};
 const seed={dataset_id:"cleaned",contract_version:1,provider_department_id:"测试部门",publication_status:"published",definition:{name:"治理后客户",description:"客户治理成果",fields:[{name:"id",logical_type:"integer",nullable:false,sensitive:false,primary_key:true,precision:null,scale:null}],usage_restrictions:"限批准用途"},governance:{single_source:true,original_directory_id:row.directory_id,provider_department_id:"测试部门",steps:[]}};
 const writes:{path:string;body:unknown}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,options?:RequestInit)=>{
  if(options?.method==="POST"){writes.push({path,body:JSON.parse(String(options.body))});return new Response(JSON.stringify({mount_id:"governed-mount"}),{status:201})}
  return new Response(JSON.stringify(path.includes("resource-access")?{operate_resource:true}:path==="/api/v1/datasets/cleaned"?seed:path.startsWith("/api/v1/resource-directories/directory-test")?originalDirectory:{items:[],total:0}),{status:200});
 }));
 show(<DirectoryWorkspace/>);
 await screen.findByText("已选择：治理后客户");
 await userEvent.click(screen.getByRole("button",{name:"保存挂载草稿"}));
 await waitFor(()=>expect(writes).toHaveLength(1));
 expect(writes[0]).toEqual({path:"/api/v1/resource-mounts",body:{directory_id:"directory-test",resource:{resource_type:"governed_dataset",resource_id:"cleaned",contract_version:1},mapping:{id:"id"},expected_version:0}});
});
it("原始目录的治理挂载再次修改时加载当前成果并保留已有映射",async()=>{
 const directory:Directory={...row,definition:{...row.definition,data_layer:"raw_object",items:[{name:"id",business_name:"客户编号",logical_type:"integer",nullable:false,sensitive:false,length:null,precision:null,scale:null,description:""}]}};
 const dataset:Dataset={dataset_id:"cleaned",contract_version:2,provider_department_id:"测试部门",publication_status:"published",quality_status:"passed",completeness_status:"complete",latest_watermark:null,definition:{name:"治理后客户",description:"客户治理成果",source_object_id:"",owner_id:"p",fields:[{name:"customer_id",logical_type:"integer",nullable:false,sensitive:false,primary_key:true,precision:null,scale:null}],usage_restrictions:"限批准用途"},governance:{single_source:true,source:{resource_type:"raw_object",resource_id:"original",contract_version:1},original_directory_id:row.directory_id,provider_department_id:"测试部门",steps:[]}};
 const mount:ResourceMount={can_operate:true,mount_id:"variant-mount",directory_id:row.directory_id,state:"active",version:7,revision_id:"variant-r1",revision_state:"approved",active_revision_id:"variant-r1",definition:{directory_revision_id:row.revision_id,resource:{resource_type:"governed_dataset",resource_id:"cleaned",contract_version:2},mapping:{id:"customer_id"},validation:null,submitted_by:"p",approved_by:"reviewer",reason:""}};
 const writes:{path:string;body:unknown}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,options?:RequestInit)=>{
  if(options?.method==="POST"){writes.push({path,body:JSON.parse(String(options.body))});return new Response(JSON.stringify({mount_id:mount.mount_id}),{status:201})}
  return new Response(JSON.stringify(path==="/api/v1/datasets/cleaned"?dataset:{items:[],total:0}),{status:200});
 }));
 const {ResourceMountEditor}=await import("../src/pages/ResourceMountEditor");
 show(<ResourceMountEditor directory={directory} seed={null} row={mount} onClose={()=>{}} onDone={()=>{}}/>);
 await screen.findByText("已选择：治理后客户");
 expect(screen.getByRole("button",{name:"保存挂载草稿"}).hasAttribute("disabled")).toBe(false);
 await userEvent.click(screen.getByRole("button",{name:"保存挂载草稿"}));
 await waitFor(()=>expect(writes).toHaveLength(1));
 expect(writes[0]).toEqual({path:"/api/v1/resource-mounts/variant-mount/revisions",body:{directory_id:row.directory_id,resource:{resource_type:"governed_dataset",resource_id:"cleaned",contract_version:2},mapping:{id:"customer_id"},expected_version:7}});
});
it("治理成果选择新目录时等待成果加载后填入目录草稿",async()=>{
 history.replaceState(null,"","/platform/exchange/directory?view=manage&action=create&dataset_id=cleaned");
 useSession.setState(s=>({user:s.user?{...s.user,roles:["provider"]}:null}));
 const seed={dataset_id:"cleaned",provider_department_id:"测试部门",definition:{name:"治理后客户",description:"清理后的客户信息",fields:[],usage_restrictions:"限批准用途"}};
 let resolveSeed:(value:Response)=>void=()=>{};
 const pendingSeed=new Promise<Response>(resolve=>{resolveSeed=resolve});
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>path==="/api/v1/datasets/cleaned"?pendingSeed:new Response(JSON.stringify(path.includes("resource-access")?{manage_resource:true}:{items:[],total:0}),{status:200})));
 show(<DirectoryWorkspace/>);
 await screen.findByRole("heading",{name:"资源目录"});
 expect(screen.queryByRole("dialog")).toBeNull();
 resolveSeed(new Response(JSON.stringify(seed),{status:200}));
 expect(await screen.findByDisplayValue("治理后客户")).toBeTruthy();
 expect(screen.getByDisplayValue("清理后的客户信息")).toBeTruthy();
 expect(screen.getAllByRole("dialog")).toHaveLength(1);
});
it("已发布未挂载的目录只能查看信息，不能申请数据",async()=>{
 vi.stubGlobal("fetch",vi.fn(async()=>new Response(JSON.stringify({items:[row],total:1,page:1,page_size:20}),{status:200,headers:{"Content-Type":"application/json"}})));
 show(<ResourceDirectory/>);await screen.findByRole("button",{name:"测试资源"});expect(screen.getByRole("button",{name:"申请使用"}).hasAttribute("disabled")).toBe(true);
 expect(screen.queryByRole("button",{name:/更多/})).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:"测试资源"}));await screen.findByText("资源信息");expect(screen.queryByRole("button",{name:"编制目录"})).toBeNull();
});
it("未获派管理岗位时新增入口禁用并说明原因",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify(path.includes("resource-access")?{manage_resource:false,operate_resource:false}:{items:[],total:0,page:1,page_size:20}),{status:200})));
 const view=show(<DirectoryWorkspace/>);await waitFor(()=>expect(view.container.querySelector(".ant-spin-spinning")).toBeNull());expect(within(view.container).getByRole("button",{name:/新增目录/}).hasAttribute("disabled")).toBe(true);expect(within(view.container).getByText("当前账号未获本部门资源管理职责")).toBeTruthy();
});
it("提交发生版本冲突时展示后台错误，不提示成功",async()=>{
 useSession.setState(s=>({user:s.user?{...s.user,roles:["provider"]}:null}));
 const calls:string[]=[];const draft={...row,state:"draft",revision_state:"draft",permissions:{manage_resource:true,operate_resource:false}};
 vi.stubGlobal("fetch",vi.fn(async(path:string,options?:RequestInit)=>{
  calls.push(path);
  if(options?.method==="POST")return new Response(JSON.stringify({code:"STATE_CONFLICT",message:"资源版本已变化",trace_id:"test"}),{status:409});
  return new Response(JSON.stringify(path.includes("resource-access")?{manage_resource:true,operate_resource:false}:{items:[draft],total:1,page:1,page_size:20}),{status:200});
 }));
 show(<DirectoryWorkspace/>);await userEvent.click(await screen.findByRole("button",{name:"提交审核"}));await screen.findByText(/资源版本已变化/);expect(screen.queryByText("操作已完成，请核对最新状态")).toBeNull();expect(calls.filter(p=>p.endsWith("/submit"))).toHaveLength(1);
});

it("目录行直接选择挂载方式并保留当前目录",async()=>{
 useSession.setState(s=>({user:s.user?{...s.user,roles:["operator"]}:null}));
 const available={...row,permissions:{manage_resource:false,operate_resource:true}};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify(path.includes("resource-access")?{manage_resource:false,operate_resource:true}:path.includes("resource-directories")?{items:[available],total:1,page:1,page_size:20}:{items:[],total:0,page:1,page_size:20}),{status:200})));
 show(<DirectoryWorkspace/>);
 await userEvent.click(await screen.findByRole("button",{name:"挂载资源"}));
 await screen.findByRole("dialog",{name:/测试资源 · 挂载资源/});
 expect(screen.queryByRole("button",{name:"新建治理成果"})).toBeNull();
 await userEvent.click(await screen.findByRole("button",{name:"数据库"}));
 const editor=(await screen.findByRole("button",{name:"保存挂载草稿"})).closest('[role="dialog"]');
 expect(editor?.textContent).toContain("测试资源 · 挂载资源");expect(editor?.textContent).toContain("数据接入方式");
 expect(editor?.textContent).toContain("请选择已登记的数据源");
});
it("目录页新增入口打开目录注册表单",async()=>{
 useSession.setState(s=>({user:s.user?{...s.user,roles:["provider"]}:null}));
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify(path.includes("resource-access")?{manage_resource:true,operate_resource:false}:{items:[],total:0,page:1,page_size:20}),{status:200})));
 show(<ResourceDirectory/>);
 const create=screen.getByRole("button",{name:/新增目录/});await waitFor(()=>expect(create.hasAttribute("disabled")).toBe(false));
 await userEvent.click(create);expect(window.location.pathname+window.location.search).toBe("/platform/exchange/directory?view=manage&action=create");
 cleanup();show(<DirectoryWorkspace/>);expect(await screen.findByText("编制资源目录")).toBeTruthy();
});
it("仅 admin 角色也显示目录新增、挂载及申请操作",async()=>{
 useSession.setState(s=>({user:s.user?{...s.user,roles:["admin"],platforms:[]}:null}));
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify(path.includes("resource-access")?{manage_resource:true,operate_resource:true}:{items:[row],total:1,page:1,page_size:20}),{status:200})));
 show(<ResourceDirectory/>);
 await screen.findByRole("button",{name:"测试资源"});
 expect(screen.getByRole("button",{name:/新增目录/}).hasAttribute("disabled")).toBe(false);
 expect(screen.queryByRole("button",{name:"资源挂载"})).toBeNull();
 expect(screen.getByRole("button",{name:"申请使用"})).toBeTruthy();
});

it("常用操作直接展示完整文案，保留权限及挂载目录定位",async()=>{
 useSession.setState(s=>({user:s.user?{...s.user,roles:["admin"]}:null}));
 const managed={...row,permissions:{manage_resource:true,operate_resource:true}};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path.includes("resource-access")?{manage_resource:true,operate_resource:true}:{items:[managed],total:1,page:1,page_size:20})));
 show(<ResourceDirectory/>);
 await screen.findByRole("button",{name:"测试资源"});
 expect(screen.getByRole("button",{name:"申请使用"}).hasAttribute("disabled")).toBe(true);
 expect(screen.getByRole("button",{name:"维护目录"})).toBeTruthy();
 expect(screen.queryByRole("button",{name:/更多/})).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:"挂载资源"}));
 expect(location.pathname+location.search).toBe("/platform/exchange/directory?view=manage&action=mount&directory_id=directory-test");
});

it("目录维护直接展示修改草稿和挂载操作，下架收进更多且仍须填写原因",async()=>{
 useSession.setState(s=>({user:s.user?{...s.user,roles:["admin"]}:null}));
 const managed={...row,permissions:{manage_resource:true,operate_resource:true}};
 const fetcher=vi.fn(async(path:string,options?:RequestInit)=>Response.json(path.includes("resource-access")?{manage_resource:true,operate_resource:true}:{items:[managed],total:1,page:1,page_size:20}));
 vi.stubGlobal("fetch",fetcher);show(<DirectoryWorkspace/>);
 await screen.findByRole("button",{name:"修改草稿"});
 expect(screen.getByRole("button",{name:"挂载资源"})).toBeTruthy();
 expect(screen.getByRole("button",{name:"挂载记录"})).toBeTruthy();
 expect(screen.queryByRole("button",{name:/下\s*架/})).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:/更多/}));
 await userEvent.click(await screen.findByRole("menuitem",{name:"下架"}));
 expect(await screen.findByRole("dialog",{name:"下架目录"})).toBeTruthy();
 expect(screen.getByPlaceholderText("请填写原因")).toBeTruthy();
 expect(fetcher.mock.calls.every(([,options])=>!options?.method||options.method==="GET")).toBe(true);
 await userEvent.click(screen.getByRole("button",{name:/取\s*消/}));
 await waitFor(()=>expect(screen.queryByRole("dialog",{name:"下架目录"})).toBeNull());
});

it("切换步骤先校验当前必填项，数据项逐行编辑",async()=>{
 history.replaceState(null,"","/platform/exchange/workspace?action=create");
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify(path.includes("resource-access")?{manage_resource:true,operate_resource:true}:{items:[],total:0}),{status:200})));
 show(<DirectoryWorkspace/>);
 await userEvent.click(await screen.findByRole("button",{name:"下一步"}));
 expect(await screen.findByText("请填写资源名称")).toBeTruthy();
 expect(screen.queryByRole("button",{name:"添加数据项"})).toBeNull();
 await userEvent.type(screen.getByLabelText("资源名称",{exact:true}),"测试目录");
 await userEvent.click(screen.getByRole("button",{name:"下一步"}));
 await userEvent.click(await screen.findByRole("button",{name:"添加数据项"}));
 await userEvent.click(screen.getByRole("button",{name:"上一步"}));
 expect(await screen.findByText("请输入技术字段")).toBeTruthy();
 expect(screen.getByRole("button",{name:"添加数据项"})).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:/3 共享规则/}));
 expect(screen.getByRole("button",{name:"添加数据项"})).toBeTruthy();
 await userEvent.type(screen.getByLabelText("数据项名称",{exact:true}),"姓名");
 await userEvent.type(screen.getByLabelText("技术字段",{exact:true}),"name");
 expect(document.querySelectorAll(".directory-item-row")).toHaveLength(1);
 await userEvent.click(screen.getByRole("button",{name:"下一步"}));
 await waitFor(()=>expect(screen.queryByRole("button",{name:"添加数据项"})).toBeNull());
 expect(screen.getByLabelText("共享属性",{exact:true})).toBeTruthy();
},15000);

it("原始库表目录提供数据库和 Excel 入口",async()=>{
 const rawDirectory={...row,definition:{...row.definition,data_layer:"raw_object" as const},permissions:{manage_resource:true,operate_resource:true}};
 vi.stubGlobal("fetch",vi.fn(async()=>new Response(JSON.stringify({items:[],total:0}),{status:200})));
 const {ResourceMountEditor}=await import("../src/pages/ResourceMountEditor");
 show(<ResourceMountEditor directory={rawDirectory} seed={null} row={null} onClose={()=>{}} onDone={()=>{}}/>);
 await userEvent.click(await screen.findByRole("button",{name:"数据库"}));
 await userEvent.click(screen.getByRole("combobox",{name:"数据接入方式"}));
 await userEvent.click(await screen.findByText("Excel 上传导入数据库"));
 expect(await screen.findByText(/导入实际数据，目录字段定义保持不变/)).toBeTruthy();
 expect(screen.getByRole("button",{name:"选择并上传文件"})).toBeTruthy();
});

it("文件目录通过真实上传接口确认资源，保存时不要求字段映射",async()=>{
 const directory={...row,definition:{...row.definition,format:"file" as const,data_layer:"raw_object" as const,sharing_method:"download" as const}};
 const calls:{path:string;body:unknown}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,options?:RequestInit)=>{
  if(options?.method==="POST"){
   calls.push({path,body:options.body instanceof FormData?options.body:JSON.parse(String(options.body))});
   if(path.endsWith("/uploads"))return new Response(JSON.stringify({upload_id:"up1",batch_id:"up1",sheets:[],name:"test.txt",size:4,directory_version:1}),{status:201});
   if(path.endsWith("/file-resources"))return new Response(JSON.stringify({resource:{resource_type:"file_resource",resource_id:"file1",contract_version:1}}),{status:201});
   return new Response(JSON.stringify({mount_id:"mount1"}),{status:201});
  }
  return new Response(JSON.stringify({items:[]}),{status:200});
 }));
 const {ResourceMountEditor}=await import("../src/pages/ResourceMountEditor");const done=vi.fn();
 const view=show(<ResourceMountEditor directory={directory} seed={null} row={null} onClose={()=>{}} onDone={done}/>);
 await userEvent.click(screen.getByRole("button",{name:/文\s*件/}));
 const input=view.container.querySelector('input[type="file"]')??document.querySelector('input[type="file"]');if(!(input instanceof HTMLInputElement))throw new Error("upload input missing");
 await userEvent.upload(input,new File(["data"],"test.txt",{type:"text/plain"}));
 await userEvent.click(await screen.findByRole("button",{name:"确认文件资源"}));
 await screen.findByText("已选择：test.txt");
 const saveButton=await screen.findByRole("button",{name:/保存挂载草稿/});await waitFor(()=>expect(saveButton.classList.contains("ant-btn-loading")).toBe(false));await userEvent.click(saveButton);await waitFor(()=>expect(done).toHaveBeenCalledOnce());
 expect(calls[0].body).toBeInstanceOf(FormData);
 expect(calls.find(c=>c.path==="/api/v1/resource-mounts")?.body).toMatchObject({directory_id:row.directory_id,resource:{resource_type:"file_resource",resource_id:"file1"},mapping:{}});
});

it("新增挂载先选择方式，未接入资源时不能保存",async()=>{
 vi.stubGlobal("fetch",vi.fn(async()=>new Response(JSON.stringify({items:[],total:0}),{status:200})));
 const {ResourceMountEditor}=await import("../src/pages/ResourceMountEditor");
 show(<ResourceMountEditor directory={{...row,definition:{...row.definition,data_layer:"raw_object"}}} seed={null} row={null} onClose={()=>{}} onDone={()=>{}}/>);
 expect(screen.queryByRole("combobox")).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:"数据库"}));
 expect(screen.getByRole("button",{name:"保存挂载草稿"}).hasAttribute("disabled")).toBe(true);
 expect(screen.queryByRole("link",{name:"登记数据源"})).toBeNull();
 expect(screen.getByRole("button",{name:"刷新库表"})).toBeTruthy();
});

it("治理目录也按数据库、文件、API 选择，库表来源在第二步选择",async()=>{
 vi.stubGlobal("fetch",vi.fn(async()=>new Response(JSON.stringify({items:[],total:0}),{status:200})));
 const {ResourceMountEditor}=await import("../src/pages/ResourceMountEditor");
 show(<ResourceMountEditor directory={row} seed={null} row={null} onClose={()=>{}} onDone={()=>{}}/>);
 expect(screen.queryByRole("button",{name:"已有资源"})).toBeNull();
 expect(screen.queryByRole("button",{name:"新建治理成果"})).toBeNull();
 expect(screen.getByRole("button",{name:/文\s*件/}).hasAttribute("disabled")).toBe(true);
 expect(screen.getByRole("button",{name:"API"})).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"数据库"}));
 expect(screen.getByText("数据接入方式")).toBeTruthy();
 expect(screen.queryByRole("link",{name:"登记数据源"})).toBeNull();
 expect(screen.getByRole("button",{name:"刷新库表"})).toBeTruthy();
 await userEvent.click(screen.getByRole("combobox",{name:"数据接入方式"}));
 await userEvent.click(await screen.findByText("Excel 上传导入数据库"));
 expect(screen.getByRole("button",{name:"选择并上传文件"})).toBeTruthy();
});

it("治理成果创建链接直接打开登记表单",async()=>{
 history.replaceState(null,"","/platform/assets/catalog?action=create");
 useSession.setState(s=>({user:s.user?{...s.user,roles:["provider"]}:null}));
 vi.stubGlobal("fetch",vi.fn(async()=>new Response(JSON.stringify({items:[],total:0}),{status:200})));
 const {Catalog}=await import("../src/pages/Catalog");show(<Catalog/>);
 expect(await screen.findByRole("dialog")).toBeTruthy();
 expect(screen.getByLabelText("资产名称")).toBeTruthy();
});

it("刷新库表重新读取当前数据库的新表",async()=>{
 let tableReads=0;
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>{
  const data=path.includes("/sources/source-one/tables")?{items:++tableReads>1?[{schema:"public",name:"new_table",kind:"table"}]:[]}:path.includes("/sources?")?{items:[{source_id:"source-one",name:"测试数据库",kind:"postgres"}]}:{items:[]};
  return new Response(JSON.stringify(data),{status:200});
 }));
 const {ResourceMountEditor}=await import("../src/pages/ResourceMountEditor");
 show(<ResourceMountEditor directory={row} seed={null} row={null} onClose={()=>{}} onDone={()=>{}}/>);
 await userEvent.click(screen.getByRole("button",{name:"数据库"}));
 await userEvent.click(screen.getAllByRole("combobox")[1]);
 await userEvent.click(await screen.findByText("测试数据库"));
 await waitFor(()=>expect(tableReads).toBe(1));
 const refresh=screen.getByRole("button",{name:"刷新库表"});
 await waitFor(()=>expect(refresh.classList.contains("ant-btn-loading")).toBe(false));
 await userEvent.click(refresh);
 await waitFor(()=>expect(tableReads).toBe(2));
 await userEvent.click(screen.getAllByRole("combobox")[2]);
 expect(await screen.findByText("public.new_table")).toBeTruthy();
});

it("新增数据源可在保存前测试，编辑连接信息后清除测试结果",async()=>{
 useSession.setState(s=>({user:s.user?{...s.user,roles:["admin"]}:null}));
 const writes:string[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,options?:RequestInit)=>{
  if(options?.method==="POST") { writes.push(path);return new Response(JSON.stringify({source_id:"",kind:"connection",passed:true,checks:[],checked_at:""}),{status:200}); }
  return new Response(JSON.stringify({items:[],total:0}),{status:200});
 }));
 const {Sources}=await import("../src/pages/Sources");show(<Sources/>);
 await userEvent.click(screen.getByRole("button",{name:/登记数据源/}));
 expect((screen.getByLabelText("数据库地址") as HTMLInputElement).value).toBe("");
 await userEvent.type(screen.getByLabelText("数据库地址"),"db.internal");
 await userEvent.type(screen.getByLabelText("数据库名称"),"business");
 await userEvent.type(screen.getByLabelText("数据库账号"),"reader");
 await userEvent.type(screen.getByLabelText("数据库密码"),"test-only-password");
 await userEvent.click(screen.getByRole("button",{name:"测试连接"}));
 expect(await screen.findByText("连接成功（尚未保存）")).toBeTruthy();
 expect(writes).toEqual(["/api/v1/sources/test-connection"]);
 await userEvent.type(screen.getByLabelText("数据库地址"),"2");
 expect(screen.queryByText("连接成功（尚未保存）")).toBeNull();
},15000);

it("目录发现与维护共用入口，切换到维护仍查询有权限的目录范围",async()=>{
 useSession.setState(s=>({user:s.user?{...s.user,roles:["provider"]}:null}));
 const calls:string[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>{calls.push(path);return new Response(JSON.stringify(path.includes("resource-access")?{manage_resource:true}:{items:[],total:0}),{status:200})}));
 show(<DirectoryCenter/>);
 await screen.findByRole("tab",{name:"已发布目录"});
 await userEvent.click(screen.getByRole("tab",{name:"目录维护"}));
 await waitFor(()=>expect(calls.some(path=>path.includes("scope=mine"))).toBe(true));
 expect(location.pathname+location.search).toBe("/platform/exchange/directory?view=manage");
 expect(screen.queryByRole("heading",{name:"目录注册"})).toBeNull();
 await userEvent.click(screen.getByRole("tab",{name:"已发布目录"}));
 expect(location.pathname+location.search).toBe("/platform/exchange/directory");
});
it("普通使用账号保留发现目录且不加载维护范围",async()=>{
 history.replaceState(null,"","/platform/exchange/directory?view=manage");
 const calls:string[]=[];vi.stubGlobal("fetch",vi.fn(async(path:string)=>{calls.push(path);return new Response(JSON.stringify({items:[],total:0}),{status:200})}));
 show(<DirectoryCenter/>);await screen.findByRole("heading",{name:"资源目录"});
 expect(screen.queryByRole("tab",{name:"目录维护"})).toBeNull();
 expect(calls.some(path=>path.includes("scope=mine"))).toBe(false);
});
it("旧目录注册链接继续打开统一入口中的维护视图",async()=>{
 history.replaceState(null,"","/platform/exchange/workspace");
 useSession.setState(s=>({user:s.user?{...s.user,roles:["operator"]}:null}));
 vi.stubGlobal("fetch",vi.fn(async()=>new Response(JSON.stringify({items:[],total:0}),{status:200})));
 show(<DirectoryCenter/>);
 expect((await screen.findByRole("tab",{name:"目录维护"})).getAttribute("aria-selected")).toBe("true");
});

it("挂载记录进入新增挂载时仅显示一个弹窗，返回后保留记录列表",async()=>{
 useSession.setState(s=>({user:s.user?{...s.user,roles:["operator"]}:null}));
 const available={...row,permissions:{manage_resource:false,operate_resource:true}};
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify(path.includes("resource-access")?{manage_resource:false,operate_resource:true}:{items:path.includes("resource-directories")?[available]:[],total:1}))));
 show(<DirectoryWorkspace/>);
 await userEvent.click(await screen.findByRole("button",{name:"挂载记录"}));
 await userEvent.click(await screen.findByRole("button",{name:/新增挂载/}));
 await waitFor(()=>expect(screen.getAllByRole("dialog")).toHaveLength(1));
 expect(screen.getByRole("dialog").textContent).toContain("选择挂载方式");
 await userEvent.click(screen.getByRole("button",{name:"返回挂载记录"}));
 await waitFor(()=>expect(screen.getAllByRole("dialog")).toHaveLength(1));
 expect(screen.getByRole("button",{name:/新增挂载/})).toBeTruthy();
});
it("同目录 API 仅映射实际返回字段，不要求映射原库表的全部字段",async()=>{
 const sourceFields=[{name:"id",logical_type:"integer",nullable:false,primary_key:true,sensitive:false,precision:null,scale:null},{name:"name",logical_type:"string",nullable:true,primary_key:false,sensitive:false,precision:null,scale:null}];
 const directory={...row,definition:{...row.definition,format:"database" as const,items:[{...sourceFields[0],business_name:"编号",length:null,description:""},{...sourceFields[1],business_name:"姓名",length:null,description:""},{...sourceFields[1],name:"amount",business_name:"金额",length:null,description:""}]}};
 const target={resource:{resource_type:"api_resource",resource_id:"query-api",contract_version:1},name:"客户查询接口",fields:sourceFields,service_representation:true,mapping:{id:"id",name:"name"}};
 const writes:{path:string;body:unknown}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,options?:RequestInit)=>{
  if(options?.method==="POST"){writes.push({path,body:JSON.parse(String(options.body))});return Response.json({mount_id:"service-mount"})}
  return Response.json(path.includes("managed-resources")?{items:[target]}:{items:[],total:0});
 }));
 show(<ResourceMountEditor directory={directory} seed={null} row={null} onClose={()=>{}} onDone={()=>{}}/>);
 await userEvent.click(await screen.findByRole("button",{name:"选择已登记资源（含数据服务）"}));
 await userEvent.click(screen.getByRole("combobox",{name:"已登记资源"}));
 await userEvent.click(await screen.findByText("客户查询接口",{selector:".ant-select-item-option-content"}));
 expect(screen.queryByText("金额（amount）")).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:"保存挂载草稿"}));
 await waitFor(()=>expect(writes).toHaveLength(1));
 expect(writes[0]).toEqual({path:"/api/v1/resource-mounts",body:{directory_id:row.directory_id,resource:target.resource,mapping:{id:"id",name:"name"},expected_version:0}});
});

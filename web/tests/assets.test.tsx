// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,within} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {Assets} from "../src/pages/Assets";
import {AssetOverview} from "../src/pages/AssetWorkspace";
import {Applications,Grants} from "../src/pages/Exchange";
import type {ResourceDirectory as Directory} from "../src/api/types";
import {useSession} from "../src/stores/session";
const evidence={name:"测试资源",fields:[],parameters:[],delivery_mode:"snapshot",quality_checks:0,output_validated:false,output_rows:null,rejected_rows:null};
const row:Directory={directory_id:"directory-test",version:1,state:"published",department_id:"测试部门",revision_id:"r1",revision_state:"approved",published_revision_id:"r1",reason:"",available:false,sharing_snapshot:null,resource:null,fields:[],permissions:{manage_resource:false,operate_resource:false},definition:{name:"测试资源",description:"已编目，尚未挂载",classification:"department",classification_path:"test",resource_code:"TEST-1",dictionary_id:"dict-test",provider_name:"测试部门",provider_code:"TEST",responsible_user_id:"p",update_frequency:"每日",format:"database",data_layer:"governed_dataset",items:[],sharing_type:"conditional",sharing_conditions:"测试用途",non_sharing_reason:"",legal_basis:"",sharing_method:"query",usage_restrictions:"测试",security_classification:"一般数据",open_to_public:false,open_conditions:"",department_approval_ref:"test",assessment_ref:"test",related_resource_codes:[]}};
beforeEach(()=>{
 history.replaceState(null,"","/platform/assets/catalog");window.scrollTo=vi.fn();
 useSession.setState({user:{user_id:"p",username:"tester",department_id:"测试部门",roles:["provider","consumer"],platforms:["assets"],csrf_token:"test"},ready:true});
 Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});
 vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});
});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
function show(element:React.ReactNode){return render(<ConfigProvider theme={{token:{motion:false}}}><AntApp>{element}</AntApp></ConfigProvider>)}
it("资产用户在同一详情选择资源后申请，不打开共享交换管理入口",async()=>{
 const ref={resource_type:"governed_dataset" as const,resource_id:"result",contract_version:2};
 const data:Directory={...row,available:true,resources:[{resource:ref,available:true,sharing_snapshot:null,name:"治理后客户",quality_status:"passed",updated_at:"2026-10-30T00:00:00Z",fields:[{name:"name",logical_type:"string",sensitive:true,nullable:false,primary_key:false,precision:null,scale:null}],governance:{single_source:true,source_name:"客户来源",steps:[{kind:"transform",name:"清理姓名",description:"去除首尾空格"}]}}]};
 const fetcher=vi.fn(async(path:RequestInfo|URL)=>Response.json(String(path).includes("/resource-detail?")?evidence:{items:[data],total:1}));vi.stubGlobal("fetch",fetcher);
 show(<Assets/>);const user=userEvent.setup();await user.click(await screen.findByRole("button",{name:"查看与申请"}));
 expect(screen.getByText("去除首尾空格",{exact:false})).toBeTruthy();
 await user.click(screen.getByRole("button",{name:"申请使用所选数据"}));
 expect(await screen.findByRole("checkbox",{name:"name（脱敏）"})).toBeTruthy();
 expect(screen.getAllByRole("dialog")).toHaveLength(1);
 expect(fetcher.mock.calls.every(call=>String(call[0]).startsWith("/api/v1/assets/"))).toBe(true);
 await user.click(screen.getByRole("button",{name:"返回资产详情"}));
 expect(await screen.findByRole("radio",{name:"治理后客户"})).toHaveProperty("checked",true);
});
it("我的申请只显示本人流程，不出现审批操作",async()=>{
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({items:[{application_id:"apply",applicant_id:"p",state:"submitted",request:{resource:{resource_type:"raw_object",resource_id:"raw",contract_version:1},department_id:"测试部门",purpose:"查询",fields:["id"],expires_at:"2099-01-01"},resource_name:"已审核客户目录"}],total:1})));
 show(<Applications assets/>);await screen.findByText("已审核客户目录");
 expect(screen.queryByText("待我审批")).toBeNull();expect(screen.queryByRole("button",{name:"批准"})).toBeNull();
 expect(screen.getByRole("button",{name:/撤\s*回/})).toBeTruthy();
});
it("共享暂停的授权禁止使用；提供者在我的资产也没有撤权入口",async()=>{
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({items:[{grant_id:"grant",application_id:"apply",grantee_user_id:"p",state:"active",available:false,availability_reason:"共享已暂停",resource_name:"客户文件",scope:{resource:{resource_type:"file_resource",resource_id:"file",contract_version:1},department_id:"测试部门",fields:[],expires_at:"2099-01-01"}}],total:1})));
 show(<Grants assets/>);await screen.findByText("客户文件");
 expect(screen.getByRole("button",{name:"下载文件"}).hasAttribute("disabled")).toBe(true);
 expect(screen.queryByRole("button",{name:"撤权"})).toBeNull();
 expect(within(screen.getByRole("table")).getByText("共享已暂停")).toBeTruthy();
});

it("门户的成果链接仍自动打开对应资产，不展示未共享的内部成果",async()=>{
 history.replaceState(null,"","/platform/assets/catalog?dataset_id=result");
 const ref={resource_type:"governed_dataset" as const,resource_id:"result",contract_version:1};
 vi.stubGlobal("fetch",vi.fn(async(path:RequestInfo|URL)=>Response.json(String(path).includes("/resource-detail?")?evidence:{items:[{...row,available:true,resources:[{resource:ref,name:"指定成果",fields:[],available:true,sharing_snapshot:null}]}],total:1})));
 show(<Assets/>);
 const dialog=await screen.findByRole("dialog");
 expect(within(dialog).getByRole("radio",{name:"指定成果"})).toHaveProperty("checked",true);
});

it("资产证据明确未知截止时间，不将成功生成视为全量质量通过",async()=>{
 const ref={resource_type:"governed_dataset" as const,resource_id:"result",contract_version:2};
 vi.stubGlobal("fetch",vi.fn(async(path:RequestInfo|URL)=>Response.json(String(path).includes("/resource-detail?")?{...evidence,output_rows:12800,quality_checks:0,generated_at:"2026-10-30T00:00:00Z"}:{items:[{...row,resources:[{resource:ref,available:true,fields:[],name:"共享结果",sharing_snapshot:null}]}],total:1})));
 show(<Assets/>);const user=userEvent.setup();await user.click(await screen.findByRole("button",{name:"查看与申请"}));await user.click(screen.getByRole("tab",{name:"字段、质量与更新"}));
 expect(await screen.findByText("未知；生成时间不能代替源数据截止时间")).toBeTruthy();
 expect(screen.getByText("未提供业务质量检查证据")).toBeTruthy();
 await user.click(screen.getByRole("tab",{name:"反馈使用问题"}));
 expect(screen.getByRole("button",{name:"提交反馈"})).toBeTruthy();
 expect(screen.queryByRole("button",{name:"申请使用所选数据"})).toBeNull();
 expect(screen.getAllByRole("dialog")).toHaveLength(1);
});

it("我的反馈展示公开处理历史，使用者不能修改回复或打开内部整改单",async()=>{
 history.replaceState(null,"","/platform/assets/grants?tab=feedback");
 const feedback={id:"feedback",resource_name:"共享结果",contract_version:2,title:"核对金额",category:"quality",description:"请核对汇总口径",fields:["amount"],state:"in_progress",response:"已受理",issue_id:"issue",issue_state:"retesting",created_at:"2026-10-30",events:[{id:1,action:"submitted",response:"",created_at:"2026-10-30"}]};
 vi.stubGlobal("fetch",vi.fn(async(path:RequestInfo|URL)=>Response.json(String(path).endsWith("/feedback/feedback")?feedback:{items:String(path).includes("/feedback?")?[feedback]:[],total:1})));
 show(<Grants assets/>);await userEvent.click(await screen.findByRole("button",{name:"核对金额"}));
 expect(await screen.findByText("已受理")).toBeTruthy();expect(screen.getByText("复测中")).toBeTruthy();
 expect(screen.queryByRole("button",{name:"保存回复"})).toBeNull();expect(screen.queryByRole("button",{name:"查看整改单"})).toBeNull();
});

it("分类深链接请求完整分类结果，而非筛选当前页",async()=>{
 history.replaceState(null,"","/platform/assets/catalog?classification=theme");
 vi.stubGlobal("fetch",vi.fn(async(path:RequestInfo|URL)=>{const url=new URL(String(path),location.origin);return Response.json({items:url.searchParams.get("classification")==="theme"?[{...row,definition:{...row.definition,name:"主题分类结果"}}]:[],total:1})}));
 show(<Assets/>);expect(await screen.findByRole("button",{name:"主题分类结果"})).toBeTruthy();
});

it("可信资产入口直接展示所选版本证据，保留单层详情",async()=>{
 history.replaceState(null,"","/platform/assets/trust");
 const ref={resource_type:"governed_dataset" as const,resource_id:"trusted",contract_version:1};
 vi.stubGlobal("fetch",vi.fn(async(path:RequestInfo|URL)=>Response.json(String(path).includes("/resource-detail?")?evidence:{items:[{...row,resources:[{resource:ref,available:true,fields:[],sharing_snapshot:null}]}],total:1})));
 show(<Assets trust/>);await userEvent.click(await screen.findByRole("button",{name:"查看可信证据"}));
 expect(await screen.findByText("未提供业务质量检查证据")).toBeTruthy();expect(screen.getByRole("tab",{name:"字段、质量与更新"}).getAttribute("aria-selected")).toBe("true");
 expect(screen.getAllByRole("dialog")).toHaveLength(1);
});


it("资产总览使用完整总数，接口失败不能显示为零",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:RequestInfo|URL)=>String(path).includes("/grants?")?Response.json({message:"授权记录暂不可用"},{status:503}):Response.json({items:[],total:1280,page:1,page_size:1})));
 show(<AssetOverview/>);
 expect(await screen.findAllByText("1280 项")).toHaveLength(3);
 expect(await screen.findByRole("button",{name:/重\s*试/})).toBeTruthy();
 expect(screen.queryByText("0 项")).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:"核验可信证据"}));expect(location.pathname).toBe("/platform/assets/trust");
});

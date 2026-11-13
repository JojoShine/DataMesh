// @vitest-environment jsdom
import {beforeEach,afterEach,it,expect,vi} from "vitest";
import {render,screen,within,cleanup,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider,Modal} from "antd";
import {DirectoryWorkspace} from "../src/pages/DirectoryWorkspace";
import {Accounts} from "../src/pages/Security";
import {OrganizationManagement} from "../src/pages/OrganizationManagement";
import {useSession} from "../src/stores/session";
const units=[{id:"org",parent_id:null,kind:"organization",name:"提供组织",code:"ORG001",active:true,version:1},{id:"dept",parent_id:"org",kind:"department",name:"业务部门",code:"D001",active:true,version:1}];
beforeEach(()=>{
 ConfigProvider.config({theme:{token:{motion:false}}});
 useSession.setState({user:{user_id:"admin",username:"administrator",department_id:"dept",roles:["admin"],platforms:["security","exchange"],csrf_token:"test"},ready:true});
 Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});
 vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});window.scrollTo=vi.fn();
 history.replaceState(null,"","/platform/exchange/directory");
});
afterEach(()=>{Modal.destroyAll();cleanup();vi.unstubAllGlobals()});
function show(element:React.ReactNode){return render(<ConfigProvider theme={{token:{motion:false}}}><AntApp>{element}</AntApp></ConfigProvider>)}
it("目录周期与路径使用选项，单位自动带入且编码只读",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path.includes("resource-access")?{manage_resource:true}:path.includes("directory-provider")?{department_id:"dept",department_name:"业务部门",organization_id:"org",provider_name:"提供组织",provider_code:"ORG001",active:true}:path.includes("directory-dictionaries")?{items:[{id:"dict",definition:{name:"内部字典",version:"1",classifications:["业务/客户"],code_template:"{provider_code}-{sequence}"}}]}:{items:[],total:0})));
 show(<DirectoryWorkspace/>);await userEvent.click(await screen.findByRole("button",{name:/新增目录/}));
 const dialog=screen.getByRole("dialog");
 await waitFor(()=>expect(within(dialog).getByLabelText("提供单位代码").getAttribute("value")).toBe("ORG001"));
 expect(within(dialog).getByLabelText("提供单位名称").getAttribute("readonly")).not.toBeNull();
 expect(within(dialog).getByLabelText("资源编码").getAttribute("readonly")).not.toBeNull();
 expect(within(dialog).getByLabelText("更新周期").getAttribute("role")).toBe("combobox");
 expect(within(dialog).getByLabelText("分类路径").getAttribute("role")).toBe("combobox");
 expect(within(dialog).getByLabelText("分类路径").hasAttribute("disabled")).toBe(true);
 expect(within(dialog).getByText(/不代表系统已配置自动采集/)).toBeTruthy();
 await userEvent.click(within(dialog).getByLabelText("编码字典"));
 await userEvent.click(await screen.findByText("内部字典 · 1"));
 expect(within(dialog).getByLabelText("分类路径").hasAttribute("disabled")).toBe(false);
});
it("人员编辑使用组织选择并提交版本和变更原因",async()=>{
 const writes:{path:string;body:unknown}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,options?:RequestInit)=>{
  if(options?.method==="POST"){writes.push({path,body:JSON.parse(String(options.body))});return Response.json({user_id:"person"})}
  return Response.json(path.includes("organization-units")?{items:units}:{items:[{user_id:"person",username:"tester",display_name:"原姓名",department_id:"dept",active:true,roles:["consumer"],platforms:["assets"],version:2}],total:1});
 }));
 show(<Accounts/>);await userEvent.click(await screen.findByRole("button",{name:"维护人员"}));
 const dialog=screen.getByRole("dialog");
 expect(within(dialog).getByLabelText("所属组织或部门").getAttribute("role")).toBe("combobox");
 await userEvent.clear(within(dialog).getByLabelText("人员姓名"));await userEvent.type(within(dialog).getByLabelText("人员姓名"),"新姓名");
 await userEvent.type(within(dialog).getByLabelText("变更原因"),"更新人员资料");
 await userEvent.click(within(dialog).getByRole("button",{name:"保存信息"}));
 await waitFor(()=>expect(writes).toHaveLength(1));
 expect(writes[0]).toEqual({path:"/api/v1/accounts/person/revisions",body:{display_name:"新姓名",department_id:"dept",roles:["consumer"],platforms:["assets"],expected_version:2,reason:"更新人员资料"}});
});
it("组织页面呈现层级并提供正式代码维护",async()=>{
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({items:units})));
 show(<OrganizationManagement/>);await screen.findByText("提供组织");
 await userEvent.click(screen.getByLabelText("Expand row"));
 expect(await screen.findByText("业务部门")).toBeTruthy();
 await userEvent.click(screen.getAllByRole("button",{name:"维护信息"})[0]);
 const dialog=screen.getByRole("dialog");expect(within(dialog).getByLabelText("提供单位代码").getAttribute("value")).toBe("ORG001");
 expect(within(dialog).getByText(/未核实时可留空/)).toBeTruthy();
});

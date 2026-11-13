// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App,ConfigProvider} from "antd";
import {Login} from "../src/pages/Login";
beforeEach(()=>{Object.defineProperty(window,"matchMedia",{writable:true,value:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}})});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
it("公开体验信息仅来自配置，填入后仍需用户提交正常登录",async()=>{
 const posts:string[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST")posts.push(path);return Response.json({items:[{username:"experience",password:"fixture-only-password",label:"资产与目录体验",description:"浏览与申请体验"}]})}));
 render(<ConfigProvider><App><Login/></App></ConfigProvider>);await userEvent.click(await screen.findByRole("button",{name:"填入账号"}));expect(screen.getByLabelText("账号")).toHaveProperty("value","experience");expect(screen.getByLabelText("密码")).toHaveProperty("value","fixture-only-password");expect(posts).toEqual([]);
});
it("没有启用体验账号时保持正常登录表单",async()=>{
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({items:[]})));render(<ConfigProvider><App><Login/></App></ConfigProvider>);await waitFor(()=>expect(fetch).toHaveBeenCalled());expect(screen.queryByRole("region",{name:"测试体验账号"})).toBeNull();expect(screen.getByLabelText("账号")).toBeTruthy();
});

it("多个岗位只显示一个选择栏，切换后填入对应账号且不自动登录",async()=>{
 const posts:string[]=[];
 const items=["操作岗位","独立复核","历史来源操作","历史来源复核"].map((label,index)=>({username:`experience_${index}`,password:`fixture-only-password-${index}`,label,description:`${label}说明`}));
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{if(init?.method==="POST")posts.push(path);return Response.json({items})}));
 render(<ConfigProvider><App><Login/></App></ConfigProvider>);
 const picker=await screen.findByRole("combobox",{name:"选择体验账号"});
 expect(screen.getAllByRole("button",{name:"填入账号"})).toHaveLength(1);
 await userEvent.click(picker);await userEvent.click(await screen.findByText("历史来源复核"));
 await userEvent.click(screen.getByRole("button",{name:"填入账号"}));
 expect(screen.getByLabelText("账号")).toHaveProperty("value","experience_3");
 expect(screen.getByLabelText("密码")).toHaveProperty("value","fixture-only-password-3");
 expect(screen.getByText("历史来源复核说明")).toBeTruthy();
 expect(screen.queryByText("操作岗位说明")).toBeNull();
 expect(posts).toEqual([]);
});

it("持久只读身份可进入四个平台查看，公开资产仍可发现",async()=>{
 const {Home}=await import("../src/pages/Home");const {useSession}=await import("../src/stores/session");
 useSession.setState({user:{user_id:"experience",username:"experience",department_id:"体验",roles:["consumer"],platforms:["exchange","assets"],csrf_token:"test",public_test:true},ready:true,epoch:0});
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({items:[],departments:[],total:0,page:1,page_size:9})));
 render(<ConfigProvider><App><Home/></App></ConfigProvider>);expect(screen.getByText("安全体验模式")).toBeTruthy();
 for(const name of [/共享交换/,/数据治理/,/数据资产.*发现并申请/,/数据安全/])expect(screen.getByRole("button",{name})).toHaveProperty("disabled",false);
 await userEvent.click(screen.getByRole("button",{name:"安全体验模式"}));expect((await screen.findByRole("tooltip")).textContent).toContain("治理可在独立体验空间");
 useSession.setState({user:null});
});

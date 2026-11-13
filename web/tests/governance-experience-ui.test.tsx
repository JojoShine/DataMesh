// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,fireEvent,render,screen,waitFor} from "@testing-library/react";
import {App,ConfigProvider} from "antd";
import {GovernanceWorkspaceEditor} from "../src/components/GovernanceWorkspaceEditor";
import {GovernanceDataflows} from "../src/pages/GovernanceDataflows";
import {GovernanceLightweight} from "../src/pages/GovernanceLightweight";
import {GovernanceCatalog} from "../src/pages/GovernanceCatalog";
import {useSession} from "../src/stores/session";
import {request} from "../src/api/client";
import type {GovernanceRecord} from "../src/api/governance";
import type {Page} from "../src/api/types";
const user={user_id:"visitor",username:"experience",department_id:"体验部门",roles:["consumer"],platforms:["exchange","assets"] as ("exchange"|"assets")[],public_test:true,csrf_token:"fixture"};
beforeEach(()=>{localStorage.clear();history.replaceState(null,"","/platform/governance/workspaces");useSession.setState({user,ready:true,epoch:0});Object.defineProperty(window,"matchMedia",{writable:true,value:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}})});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});vi.stubGlobal("fetch",vi.fn(()=>{throw new Error("不应发送正式写入")}));vi.stubGlobal("scrollTo",vi.fn())});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
it("原治理空间表单可填写并创建体验空间，保存后可重新读取",async()=>{
 const saved=vi.fn();render(<ConfigProvider><App><GovernanceWorkspaceEditor editing="new" onSaved={saved} onClose={()=>undefined}/></App></ConfigProvider>);
 const name=screen.getByRole("textbox",{name:"空间名称"});expect(name).toHaveProperty("disabled",false);fireEvent.change(name,{target:{value:"招生信息整理"}});fireEvent.change(screen.getByRole("textbox",{name:"业务用途"}),{target:{value:"关联与质量检查体验"}});fireEvent.click(screen.getByRole("button",{name:"创建并进入"}));await waitFor(()=>expect(saved).toHaveBeenCalled());
 expect(await request("/api/v1/governance/workspaces")).toMatchObject({total:2});expect(fetch).not.toHaveBeenCalled();
},15000);
it("原业务术语页面可新增并实际保存，不再只有查看提示",async()=>{
 history.replaceState(null,"","/platform/governance/terms");render(<ConfigProvider><App><GovernanceCatalog kind="term"/></App></ConfigProvider>);
 fireEvent.click(await screen.findByRole("button",{name:/新建业务术语/}));
 const input=await screen.findByLabelText(/名称/);expect(input).toHaveProperty("disabled",false);fireEvent.change(input,{target:{value:"户籍地址"}});
 const description=screen.queryByLabelText(/业务说明/);if(description)fireEvent.change(description,{target:{value:"居民户籍登记地址"}});
 fireEvent.click(screen.getByRole("button",{name:/保存.*草稿|保存版本/}));
 await waitFor(async()=>{const terms=await request<Page<GovernanceRecord>>("/api/v1/governance/terms");expect(terms.items.some(term=>term.definition.name==="户籍地址")).toBe(true)});
 expect(fetch).not.toHaveBeenCalled();
},15000);
it("原数据流页面可以运行内置流程，并展示计算结果",async()=>{
 history.replaceState(null,"","/platform/governance/dataflows");render(<ConfigProvider><App><GovernanceDataflows/></App></ConfigProvider>);
 fireEvent.click(await screen.findByRole("button",{name:"居民信息整理与质量检查"}));const button=await screen.findByRole("button",{name:/^运\s*行$/});expect(button).toHaveProperty("disabled",false);fireEvent.click(button);
 await waitFor(async()=>expect(await request("/api/v1/governance/runs")).toMatchObject({total:1}));
 await waitFor(()=>expect(document.body.textContent).toContain("78"));expect(fetch).not.toHaveBeenCalled();
},20000);
it("原轻量治理页面可以保存并运行处理单元",async()=>{
 history.replaceState(null,"","/platform/governance/lightweight");render(<ConfigProvider><App><GovernanceLightweight/></App></ConfigProvider>);
 fireEvent.click(await screen.findByRole("button",{name:"居民信息轻量处理"}));
 const run=await screen.findByRole("button",{name:"保存并运行"});await waitFor(()=>expect(run).toHaveProperty("disabled",false));fireEvent.click(run);
 await waitFor(async()=>expect(await request("/api/v1/governance/runs")).toMatchObject({total:1}));
 expect(await screen.findByRole("button",{name:"下载体验 CSV"})).toBeTruthy();expect(fetch).not.toHaveBeenCalled();
},20000);

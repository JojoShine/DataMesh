// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {GovernanceQuickStart} from "../src/components/GovernanceQuickStart";
import {useSession} from "../src/stores/session";

beforeEach(()=>{
 history.replaceState(null,"","/");
 window.scrollTo=vi.fn();
 useSession.setState({user:{user_id:"governor",username:"治理人员",department_id:"dept",roles:["governor"],platforms:["governance"],csrf_token:"test"},ready:true,epoch:0,identityError:null});
});
afterEach(()=>{cleanup();vi.restoreAllMocks()});
it("工作流入口打开对应真实页面并保留指定治理空间",async()=>{
 render(<GovernanceQuickStart workspaceID="customer space"/>);
 const source=screen.getByRole("link",{name:"查看来源数据"});
 expect(source.getAttribute("href")).toBe("/platform/governance/space-sources?workspace_id=customer+space");
 expect(screen.getByRole("link",{name:"设计多来源关联"}).getAttribute("href")).toBe("/platform/governance/dataflows?workspace_id=customer+space");
 expect(screen.getByRole("link",{name:"设置调度计划"}).getAttribute("href")).toBe("/platform/governance/schedules?workspace_id=customer+space");
 await userEvent.click(screen.getByRole("link",{name:"编排定时工作流"}));
 expect(location.pathname+location.search).toBe("/platform/governance/workflows?workspace_id=customer+space");
});
it("没有治理职责的账号可阅读流程但不获得治理操作入口",()=>{
 useSession.setState({user:{user_id:"reader",username:"使用方",department_id:"dept",roles:["consumer"],platforms:["assets","governance"],csrf_token:"test"}});
 render(<GovernanceQuickStart/>);
 expect(screen.queryByRole("link",{name:"设计多来源关联"})).toBeNull();
 expect(screen.queryByRole("link",{name:"设置调度计划"})).toBeNull();
 expect(screen.getByText(/需开通数据治理平台及治理职责/)).toBeTruthy();
});
it("未登录用户的引导入口先进入登录页",async()=>{
 useSession.setState({user:null});
 render(<GovernanceQuickStart/>);
 expect(screen.queryByRole("link",{name:"核验并发布成果"})).toBeNull();
 await userEvent.click(screen.getByRole("link",{name:"登录后开始治理"}));
 expect(location.pathname).toBe("/login");
});

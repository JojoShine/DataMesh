// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "../src/app/App";
import { useSession } from "../src/stores/session";
const principal = {
  user_id: "consumer-fixture",
  username: "reader",
  department_id: "使用部门",
  roles: ["consumer"],
  platforms: ["exchange", "assets"],
  csrf_token: "in-memory-only",
};
beforeEach(() => {
  useSession.setState({
    user: null,
    ready: false,
    epoch: 0,
    identityError: null,
  });
  Object.defineProperty(window, "matchMedia", {
    writable: true,
    value: vi
      .fn()
      .mockImplementation(() => ({
        matches: false,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
  });
  window.scrollTo = vi.fn();
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("登录成功回首页，然后由用户选择模块；不直接进入后台", async () => {
  history.replaceState(null, "", "/login");
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      calls.push(path);
      if (path === "/api/v1/me")
        return new Response(JSON.stringify({ code: "UNAUTHENTICATED" }), {
          status: 401,
        });
      if (path === "/auth/login")
        return new Response(JSON.stringify(principal));
      if (path.startsWith("/api/public/assets"))
        return new Response(
          JSON.stringify({
            items: [],
            total: 0,
            page: 1,
            page_size: 12,
            departments: [],
          }),
        );
      if (path.startsWith("/api/v1/resources/raw") || path.startsWith("/api/v1/resource-directories") || path.startsWith("/api/v1/directory-dictionaries"))
        return new Response(
          JSON.stringify({ items: [], total: 0, page: 1, page_size: 20 }),
        );
      throw new Error("Unexpected fixture endpoint");
    }),
  );
  render(<App />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText("账号"), "reader");
  await user.type(
    screen.getByLabelText("密码", { selector: "input" }),
    "fixture-password-123",
  );
  const submit = document.querySelector<HTMLButtonElement>(
    '.login-panel button[type="submit"]',
  );
  if (!submit) throw new Error("登录提交按钮缺失");
  await user.click(submit);
  await screen.findByRole("heading", { name: "汇聚数据之力连接无限可能" });
  expect(location.pathname).toBe("/");
  expect(useSession.getState().user?.username).toBe("reader");
  expect(screen.queryByText("接入任务")).toBeNull();
  await user.click(
    screen.getByRole("button", { name: /共享交换.*连接原始数据/ }),
  );
  await screen.findByRole("heading", { name: "资源目录" });
  expect(location.pathname).toBe("/platform/exchange/");
  expect(screen.queryByText("数据源管理")).toBeNull();
  expect(calls.some((p) => p.startsWith("/api/v1/resource-directories"))).toBe(true);
  expect(localStorage.length).toBe(0);
  expect(sessionStorage.length).toBe(0);
});
it("未登录默认看到四个入口与资产摘要，进入模块前转到独立登录页", async () => {
  history.replaceState(null, "", "/");
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) =>
      path === "/api/v1/me"
        ? new Response(JSON.stringify({ code: "UNAUTHENTICATED" }), {
            status: 401,
          })
        : new Response(
            JSON.stringify({
              items: [],
              total: 0,
              page: 1,
              page_size: 12,
              departments: [],
            }),
          ),
    ),
  );
  render(<App />);
  await screen.findByRole("heading", { name: "汇聚数据之力连接无限可能" });
  expect(
    screen.getByRole("button", { name: /数据安全 保障数据使用/ }),
  ).toBeDefined();
  await userEvent.click(screen.getByRole("button", { name: /数据资产 发现/ }));
  await screen.findByRole("heading", { name: "登录工作台" });
  await waitFor(() => expect(location.pathname).toBe("/login"));
});
it("治理菜单按业务流程分为六类，保留功能、空间上下文与当前分类", async () => {
 history.replaceState(null,"","/platform/governance/workspaces");
 const space={id:"space",name:"客户资料整理",department_id:"使用部门",state:"active",is_legacy:false,owner_id:"consumer-fixture",max_concurrent_runs:2,version:1};
 const calls:string[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>{calls.push(path);return Response.json(path==="/api/v1/me"?{...principal,roles:["governor"],platforms:["governance"]}:path==="/api/v1/governance/workspaces/space"?space:path.includes("/overview")?{workspace:space,run_states:[],recent_runs:[],recent_results:[],quality_summary:{checked:0,passed:0,pass_rate:null,scope:"检查行次"}}:{items:[],total:0,page:1,page_size:20})}));
 render(<App/>);const user=userEvent.setup();await screen.findByRole("button",{name:"新建治理空间"});
 expect(document.querySelector(".ant-layout-sider")).toBeNull();
 const navigation=screen.getByRole("navigation",{name:"数据治理功能分类"});expect(navigation.textContent).toBe("工作台准备数据处理数据检查与交付标准与规范监控与追溯");
 await user.click(screen.getByRole("button",{name:"标准与规范"}));await user.click(await screen.findByRole("menuitem",{name:"数据标准"}));await screen.findByRole("heading",{name:"数据标准"});expect(location.pathname).toBe("/platform/governance/standards");
 expect(screen.queryByText("这项业务解决什么问题：")).toBeNull();expect(screen.queryByRole("dialog")).toBeNull();
 history.pushState(null,"","/platform/governance/dataflows?workspace_id=space");window.dispatchEvent(new PopStateEvent("popstate"));await screen.findByRole("region",{name:"数据流编排工作区"});
 expect(document.querySelector(".breadcrumb")?.textContent).toContain("数据治理/处理数据/数据流设计");
 await user.click(screen.getByRole("button",{name:"处理数据"}));await user.click(await screen.findByRole("menuitem",{name:"数据流设计"}));
 await screen.findByRole("region",{name:"数据流编排工作区"});expect(location.search).toContain("workspace_id=space");
 await waitFor(()=>expect(calls.some(path=>path.startsWith("/api/v1/governance/dataflows?")&&path.includes("workspace_id=space"))).toBe(true));
 await user.click(screen.getByRole("button",{name:"检查与交付"}));await user.click(await screen.findByRole("menuitem",{name:"质量管理"}));await screen.findByRole("tab",{name:"检查记录"});expect(location.pathname).toBe("/platform/governance/quality");expect(location.search).toContain("workspace_id=space");expect(screen.getByRole("button",{name:"检查与交付"}).getAttribute("aria-current")).toBe("true");
 const expectedGroups={"准备数据":["来源数据","元数据管理","数据建模","主数据管理"],"处理数据":["轻量治理","数据流设计","工作流编排","调度计划","场景方案库"],"检查与交付":["质量管理","成果核验","成果生命周期","变更发布"],"标准与规范":["主题域与责任","业务术语","数据标准","标准落标","指标口径"],"监控与追溯":["持续数据监测","运行运维","治理告警","血缘与影响"]};
 for(const [category,names] of Object.entries(expectedGroups)){
  await user.click(screen.getByRole("button",{name:category}));
  const entries=await waitFor(()=>{
   const trigger=screen.getByRole("button",{name:category});
   expect(trigger.getAttribute("aria-expanded")).toBe("true");
   const menu=document.getElementById(trigger.getAttribute("aria-controls")??"");
   if(!menu)throw new Error("当前分类菜单未打开");
   const items=within(menu).getAllByRole("menuitem");
   expect(items.map(entry=>entry.textContent)).toEqual(names);return items;
  });
  for(const entry of entries)expect(entry.querySelector(".anticon")).toBeTruthy();
  await user.keyboard("{Escape}");
  await waitFor(()=>expect(screen.getByRole("button",{name:category}).getAttribute("aria-expanded")).toBe("false"));
 }
 expect(within(navigation).getAllByRole("button")).toHaveLength(6);
 for(const page of ["quality-templates","issues","quality-trends","rules"]){
  history.pushState(null,"",`/platform/governance/${page}?workspace_id=space`);window.dispatchEvent(new PopStateEvent("popstate"));
  await waitFor(()=>expect(screen.getByRole("button",{name:"检查与交付"}).getAttribute("aria-current")).toBe("true"));
  expect(document.querySelector(".breadcrumb")?.textContent).toContain("数据治理/检查与交付/");
 }


},15000);
it("拥有平台入口但缺少治理角色时不展示治理分类", async () => {
  history.replaceState(null, "", "/platform/governance/domains");
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({...principal, platforms: ["governance"]})));
  render(<App />);
  await screen.findByText("当前账号没有此功能权限");
  expect(screen.queryByRole("navigation", {name: "数据治理功能分类"})).toBeNull();
});

it("平台切换为紧凑按钮，显示当前平台、用途与权限范围并可跳转",async()=>{
 history.replaceState(null,"","/platform/exchange/");
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path==="/api/v1/me"?principal:{items:[],total:0,page:1,page_size:20,departments:[]})));
 render(<App/>);const user=userEvent.setup();
 const trigger=await screen.findByRole("button",{name:"切换平台：共享交换"});
 expect(trigger.textContent).toBe("共享交换");expect(trigger.querySelector(".anticon")).toBeTruthy();
 expect(screen.queryByText("切换平台",{exact:true})).toBeNull();expect(screen.queryByRole("combobox",{name:"切换平台"})).toBeNull();
 await user.click(trigger);
 const current=await screen.findByRole("menuitem",{name:/共享交换.*连接原始数据/});
 expect(current.classList.contains("ant-dropdown-menu-item-selected")).toBe(true);
 expect(screen.getByRole("menuitem",{name:/数据资产.*发现数据资产/})).toBeTruthy();
 expect(screen.queryByRole("menuitem",{name:/数据治理/})).toBeNull();expect(screen.queryByRole("menuitem",{name:/数据安全/})).toBeNull();
 await user.click(current);expect(location.pathname).toBe("/platform/exchange/");
 await user.click(screen.getByRole("button",{name:"切换平台：共享交换"}));
 await user.click(await screen.findByRole("menuitem",{name:/数据资产.*发现数据资产/}));
 await screen.findByRole("button",{name:"切换平台：数据资产"});expect(location.pathname).toBe("/platform/assets/");
});

it("轻量治理直接进入时忽略空间参数，不调用空间或目录接口",async()=>{
 history.replaceState(null,"","/platform/governance/lightweight?workspace_id=space");const calls:string[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>{calls.push(path);return Response.json(path==="/api/v1/me"?{...principal,roles:["governor"],platforms:["governance"]}:{items:[],total:0,page:1,page_size:20})}));
 render(<App/>);await screen.findByRole("region",{name:"轻量治理工作区"});
 await waitFor(()=>expect(location.search).not.toContain("workspace_id"));
 expect(screen.queryByLabelText("当前治理空间")).toBeNull();expect(calls.some(path=>path.includes("/governance/workspaces")||path.includes("/metadata"))).toBe(false);
});
it("从空间菜单进入轻量治理时移除空间参数",async()=>{
 history.replaceState(null,"","/platform/governance/standards?workspace_id=space");
 const space={id:"space",name:"资料整理",department_id:"使用部门",state:"active",is_legacy:false,owner_id:principal.user_id,max_concurrent_runs:2,version:1};const calls:string[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>{calls.push(path);return Response.json(path==="/api/v1/me"?{...principal,roles:["governor"],platforms:["governance"]}:path==="/api/v1/governance/workspaces/space"?space:path.includes("/overview")?{workspace:space,run_states:[],recent_runs:[],recent_results:[],quality_summary:{checked:0,passed:0,pass_rate:null,scope:"检查行次"}}:{items:[],total:0,page:1,page_size:20})}));
 render(<App/>);await screen.findByRole("button",{name:"处理数据"});await userEvent.click(screen.getByRole("button",{name:"处理数据"}));await userEvent.click(await screen.findByRole("menuitem",{name:"轻量治理"}));
 await screen.findByRole("region",{name:"轻量治理工作区"});expect(location.search).not.toContain("workspace_id");expect(screen.queryByLabelText("当前治理空间")).toBeNull();
 const count=calls.filter(path=>path.includes("/governance/workspaces")).length;await new Promise(resolve=>setTimeout(resolve,30));expect(calls.filter(path=>path.includes("/governance/workspaces"))).toHaveLength(count);
});

it("资产分类菜单能进入总览、分类浏览和独立反馈，保留消费权限",async()=>{
 history.replaceState(null,"","/platform/assets/catalog");
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path==="/api/v1/me"?principal:{items:[],total:0,page:1,page_size:20,unread:0})));
 render(<App/>);const user=userEvent.setup();await screen.findByRole("heading",{name:"资产发现"});
 for(const name of ["资产工作台","发现与理解","申请与使用","关注与反馈"])expect(screen.getByRole("menuitem",{name:new RegExp(name+"$")})).toBeTruthy();
 await user.click(screen.getByRole("menuitem",{name:/资产总览$/}));await screen.findByRole("heading",{name:"资产总览"});
 await user.click(screen.getByRole("menuitem",{name:/分类浏览$/}));await screen.findByRole("heading",{name:"分类浏览"});
 await user.click(screen.getByRole("button",{name:"浏览主题信息"}));await screen.findByRole("heading",{name:"资产发现"});expect(location.search).toBe("?classification=theme");
 await user.click(screen.getByRole("menuitem",{name:/我的反馈$/}));await screen.findByRole("heading",{name:"我的反馈"});
 expect(screen.queryByRole("button",{name:"保存回复"})).toBeNull();
});

it("轻量治理合并图形处理与SQL沙箱，旧深链保持权限和文件参数", async () => {
 history.replaceState(null,"","/platform/governance/sandbox?file_id=private-file");
 const calls:string[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>{calls.push(path);return Response.json(path==="/api/v1/me"?{...principal,roles:["governor"],platforms:["governance"]}:{items:[],total:0,page:1,page_size:15})}));
 render(<App/>);const user=userEvent.setup();
 await screen.findByRole("heading",{name:"轻量治理",level:1});
 expect(screen.getByRole("tab",{name:"SQL 数据沙箱"}).getAttribute("aria-selected")).toBe("true");
 expect(location.search).toBe("?file_id=private-file");
 expect(document.querySelector(".breadcrumb")?.textContent).toContain("处理数据/轻量治理");
 await user.click(screen.getByRole("button",{name:"处理数据"}));
 expect(await screen.findByRole("menuitem",{name:"轻量治理"})).toBeTruthy();
 expect(screen.queryByRole("menuitem",{name:"数据沙箱"})).toBeNull();
 await user.keyboard("{Escape}");
 await user.click(screen.getByRole("tab",{name:"可视化处理"}));
 await screen.findByLabelText("处理单元名称");
 expect(location.pathname).toBe("/platform/governance/lightweight");
 expect(calls.some(path=>path.includes("workspaces"))).toBe(false);
 await user.type(screen.getByLabelText("处理单元名称"),"尚未保存的处理");
 await user.click(screen.getByRole("tab",{name:"SQL 数据沙箱"}));
 await screen.findByRole("dialog");
 expect(location.pathname).toBe("/platform/governance/lightweight");
 await user.click(screen.getByRole("button",{name:"继续编辑"}));
 expect((screen.getByLabelText("处理单元名称") as HTMLInputElement).value).toBe("尚未保存的处理");
 await user.click(screen.getByRole("tab",{name:"SQL 数据沙箱"}));
 await user.click(await screen.findByRole("button",{name:"放弃修改并离开"}));
 await screen.findByLabelText("沙箱名称");
 expect(location.pathname).toBe("/platform/governance/sandbox");
 expect(screen.getByRole("tab",{name:"SQL 数据沙箱"}).getAttribute("aria-selected")).toBe("true");
});

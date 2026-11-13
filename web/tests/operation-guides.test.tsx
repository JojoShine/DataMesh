// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "../src/app/App";
import { useSession } from "../src/stores/session";

beforeEach(() => {
  history.replaceState(null, "", "/");
  useSession.setState({ user: null, ready: false, epoch: 0, identityError: null });
  window.scrollTo = vi.fn();
  Object.defineProperty(window, "matchMedia", { writable: true, value: vi.fn(() => ({
    matches: false, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {}, dispatchEvent() {},
  })) });
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.stubGlobal("fetch", vi.fn(async (path: string) => path === "/api/v1/me"
    ? Response.json({ code: "UNAUTHENTICATED" }, { status: 401 })
    : Response.json({ items: [], total: 0, page: 1, page_size: 12, departments: [] })));
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("首页只保留指南入口，Markdown在独立页阅读、渲染表格并可下载", async () => {
  render(<App />);
  await screen.findByRole("heading", { name: "汇聚数据之力连接无限可能" });
  expect(screen.queryByRole("region", { name: "平台使用流程" })).toBeNull();
  expect(screen.queryByRole("region", { name: "常用治理工作流" })).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: "操作指南" }));
  await screen.findByRole("heading", { name: "共享交换操作指南" }, { timeout: 5000 });
  expect(location.pathname).toBe("/guides");
  expect(screen.getByRole("link", { name: "下载 Markdown" }).getAttribute("download")).toBe("共享交换操作指南.md");
  await userEvent.click(screen.getByRole("link", { name: "数据治理" }));
  await screen.findByRole("heading", { name: "数据治理操作指南" });
  expect(location.pathname).toBe("/guides/governance");
  expect(screen.getByRole("heading", { name: "单表清洗与质量检查" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "多来源关联与汇总" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "定时执行与异常处理" })).toBeTruthy();
  const table = screen.getByRole("table");
  expect(within(table).getAllByRole("row")).toHaveLength(7);
  expect(within(table).getAllByRole("columnheader").map(cell => cell.textContent)).toEqual(["顶部菜单", "办理内容"]);
  expect(within(table).getByRole("cell", { name: "准备数据" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "下载 Markdown" }).getAttribute("download")).toBe("数据治理操作指南.md");
  expect(screen.queryByText(/^## /)).toBeNull();
  const requests = vi.mocked(fetch).mock.calls;
  expect(requests.every(([, options]) => !options?.method || options.method === "GET")).toBe(true);
});
it.each([
  ["assets", "数据资产", "按分类找到功能入口", "从来源到首页展示", "用演示资产走一遍", "数据产品"],
  ["security", "数据安全", "按分类找到功能入口", "水印配置与交付核对", "风险事件处置步骤", "防护策略与预检"],
])("%s指南可切换、定位章节和下载完整操作说明", async (key, name, ...sections) => {
  history.replaceState(null, "", "/guides/governance");
  render(<App />);
  await screen.findByRole("heading", { name: "数据治理操作指南" });
  await userEvent.click(within(screen.getByRole("navigation", { name: "选择系统指南" })).getByRole("link", { name }));
  const article = await screen.findByRole("article", { name: `${name}操作指南` });
  expect(location.pathname).toBe(`/guides/${key}`);
  expect(within(article).getAllByRole("table").length).toBeGreaterThan(0);
  for (const section of sections) {
    const heading = within(article).getByRole("heading", { name: section });
    expect(within(screen.getByRole("navigation", { name: "指南章节" })).getByRole("link", { name: section }).getAttribute("href")).toBe(`#${heading.id}`);
  }
  expect(screen.getByRole("link", { name: "下载 Markdown" }).getAttribute("download")).toBe(`${name}操作指南.md`);
});
it("指南地址可直接打开，浏览器返回会恢复对应文档", async () => {
  history.replaceState(null, "", "/guides/governance");
  render(<App />);
  await screen.findByRole("heading", { name: "数据治理操作指南" });
  await userEvent.click(screen.getByRole("link", { name: "共享交换" }));
  await screen.findByRole("heading", { name: "共享交换操作指南" });
  history.back();
  await waitFor(() => expect(location.pathname).toBe("/guides/governance"));
  await screen.findByRole("heading", { name: "数据治理操作指南" });
});

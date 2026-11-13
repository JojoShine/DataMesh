// @vitest-environment jsdom
import {afterEach, beforeEach, expect, it, vi} from "vitest";
import {act, cleanup, render, renderHook, screen, waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp} from "antd";
import {usePagination, pageSizeOptions} from "../src/hooks/usePagination";
import {Home} from "../src/pages/Home";
import {DataServices} from "../src/pages/DataServices";
import {useSession} from "../src/stores/session";

beforeEach(() => {
  history.replaceState(null, "", "/platform/exchange/data-services");
  useSession.setState({user:{user_id:"provider", username:"provider", department_id:"d", roles:["admin"], platforms:["exchange"], csrf_token:"test"}, ready:true});
  Object.defineProperty(window, "matchMedia", {writable:true, value:vi.fn(() => ({matches:false, addListener:vi.fn(), removeListener:vi.fn(), addEventListener:vi.fn(), removeEventListener:vi.fn()}))});
  vi.stubGlobal("ResizeObserver", class {observe() {} unobserve() {} disconnect() {}});
});
afterEach(() => {cleanup(); vi.unstubAllGlobals();});

it("每个列表默认15条，切换条数回到第一页，独立列表互不影响", () => {
  const {result} = renderHook(() => [usePagination(), usePagination()] as const);
  expect(pageSizeOptions).toEqual([15, 20, 25, 50, 100]);
  expect(result.current[0][2].pageSize).toBe(15);
  for (const size of pageSizeOptions.slice(1)) {
    act(() => result.current[0][1](3));
    act(() => result.current[0][2].onChange(3, size));
    expect(result.current[0][0]).toBe(1);
    expect(result.current[0][2].pageSize).toBe(size);
  }
  act(() => result.current[0][2].onChange(2, 100));
  expect(result.current[0][0]).toBe(2);
  act(() => result.current[0][1](1));
  expect(result.current[0][2].pageSize).toBe(100);
  expect(result.current[1][2].pageSize).toBe(15);
});

it("数据服务分页按选定条数请求后台，并从后续页重置到第一页", async () => {
  const reads: URL[] = [];
  vi.stubGlobal("fetch", vi.fn(async (path:string) => {
    const url = new URL(path, location.origin); reads.push(url);
    const size = Number(url.searchParams.get("page_size"));
    const page = Number(url.searchParams.get("page"));
    return Response.json({total:125, items:Array.from({length:size}, (_, i) => ({id:`s${(page-1)*size+i}`, name:`服务${(page-1)*size+i}`, directory_id:"d", query:{mode:"snapshot", max_page_size:100}, fields:[]}))});
  }));
  render(<AntApp><DataServices/></AntApp>);
  const user = userEvent.setup();
  await screen.findByRole("button", {name:"服务14"});
  expect(screen.queryByRole("button", {name:"服务15"})).toBeNull();
  await user.click(screen.getByTitle("2"));
  await screen.findByRole("button", {name:"服务15"});
  await user.click(screen.getByRole("combobox"));
  await user.click(await screen.findByText("50 / page"));
  await screen.findByRole("button", {name:"服务49"});
  expect(screen.getByRole("button", {name:"服务0"})).toBeTruthy();
  await waitFor(() => expect(reads.at(-1)?.searchParams.get("page_size")).toBe("50"));
  expect(reads.at(-1)?.searchParams.get("page")).toBe("1");
}, 15000);

it("首页固定每页9项，翻页和筛选始终请求9项，不显示条数切换", async () => {
  const reads:URL[]=[];
  vi.stubGlobal("fetch",vi.fn(async(path:string)=>{
    const url=new URL(path,location.origin);reads.push(url);
    const size=Number(url.searchParams.get("page_size")),page=Number(url.searchParams.get("page"));
    const items=Array.from({length:27},(_,i)=>({dataset_id:`asset-${i}`,name:`资产${i}`,description:"已发布业务资产",provider_department_id:"提供部门",field_count:3,updated_at:"2026-11-05T00:00:00Z"})).filter(a=>a.name.includes(url.searchParams.get("search")??""));
    return Response.json({items:items.slice((page-1)*size,page*size),total:items.length,departments:["提供部门"]});
  }));
  render(<AntApp><Home/></AntApp>);
  const user=userEvent.setup();
  await screen.findByText("资产8");
  expect(screen.queryByText("资产9")).toBeNull();
  expect(document.querySelector('.ant-pagination-options')).toBeNull();
  await user.click(screen.getByTitle("2"));
  await screen.findByText("资产17");
  expect(reads.at(-1)?.searchParams.get("page")).toBe("2");
  await user.type(screen.getByRole("searchbox",{name:"搜索数据资产"}),"资产1{Enter}");
  await screen.findByText("资产1");
  await waitFor(()=>expect(reads.at(-1)?.searchParams.get("page")).toBe("1"));
  expect(reads.every(url=>url.searchParams.get("page_size")==="9")).toBe(true);
  expect(document.querySelector('.ant-pagination-options')).toBeNull();
});

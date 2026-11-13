// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {AssetSaveControls,AssetSaved,AssetNotices} from "../src/pages/AssetFollowing";
import {PlatformDialog} from "../src/components/PlatformDialog";
import {useSession} from "../src/stores/session";
const ref={resource_type:"raw_object" as const,resource_id:"source",contract_version:1};
beforeEach(()=>{
 history.replaceState(null,"","/platform/assets/saved");window.scrollTo=vi.fn();
 useSession.setState({user:{user_id:"user",username:"使用者",department_id:"dept",roles:["consumer"],platforms:["assets"],csrf_token:"test"},ready:true});
 Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});
 vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});
});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
function show(child:React.ReactNode){render(<ConfigProvider theme={{token:{motion:false}}}><AntApp>{child}</AntApp></ConfigProvider>)}
it("收藏和关注分开保存，不创建使用授权",async()=>{
 let row={version:1,bookmarked:true,following:false};const writes:unknown[]=[];
 const fetcher=vi.fn(async(path:RequestInfo|URL,options?:RequestInit)=>{
  if(options?.method==="POST"){writes.push(JSON.parse(String(options.body)));row={...row,version:2,following:true}}
  return Response.json(row);
 });vi.stubGlobal("fetch",fetcher);
 show(<PlatformDialog open title="共享资产" onCancel={()=>{}}><AssetSaveControls resource={ref}/></PlatformDialog>);
 const follow=await screen.findByRole("button",{name:"关注变化"});await waitFor(()=>expect(follow.hasAttribute("disabled")).toBe(false));await userEvent.click(follow);
 await screen.findByRole("button",{name:"取消关注"});
 expect(writes).toEqual([{resource:ref,base_version:1,bookmarked:true,following:true}]);
 expect(fetcher.mock.calls.every(([path])=>String(path).startsWith("/api/v1/assets/saved"))).toBe(true);
});
it("共享暂停后仍可取消收藏，保留独立关注偏好",async()=>{
 const writes:unknown[]=[];vi.stubGlobal("fetch",vi.fn(async(_path:RequestInfo|URL,options?:RequestInit)=>{
  if(options?.method==="POST"){writes.push(JSON.parse(String(options.body)));return Response.json({})}
  return Response.json({items:[{...ref,id:"saved",resource_name:"客户文件",bookmarked:true,following:true,available:false,version:4,checked_at:"2026-10-30"}],total:1});
 }));show(<AssetSaved/>);
 expect(await screen.findByText("暂不可用或版本变化")).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"取消收藏"}));
 await waitFor(()=>expect(writes).toEqual([{resource:ref,base_version:4,bookmarked:false,following:true}]));
 expect(screen.getByRole("button",{name:"重新发现"})).toBeTruthy();
});
it("提醒页进入时核对一次，已读后不重复触发核对或授权",async()=>{
 let read=false;const paths:string[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:RequestInfo|URL,options?:RequestInit)=>{
  const url=String(path);if(options?.method==="POST")paths.push(url);
  if(url.endsWith("/refresh"))return Response.json({checked_at:"2026-10-30T01:00:00Z"});
  if(url.endsWith("/read")){read=true;return Response.json({})}
  return Response.json({items:[{id:"notice",category:"grant_expiring",resource_name:"客户文件",resource_id:"source",contract_version:1,message:"授权即将到期，请按原流程重新申请",read_at:read?"2026-10-30":null,created_at:"2026-10-30"}],total:1,unread:read?0:1});
 }));show(<AssetNotices/>);
 expect(await screen.findByText("授权即将到期，请按原流程重新申请")).toBeTruthy();
 const button=await screen.findByRole("button",{name:"标记已读"});await waitFor(()=>expect(button.hasAttribute("disabled")).toBe(false));await userEvent.click(button);
 await screen.findByText("已读");expect(paths).toEqual(["/api/v1/assets/notices/refresh","/api/v1/assets/notices/notice/read"]);
 await userEvent.click(screen.getByRole("button",{name:"核对并申请"}));expect(location.pathname+location.search).toBe("/platform/assets/catalog?resource_id=source");
});

it("产品契约提醒跳转对应产品核验，不误入资源申请",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:RequestInfo|URL)=>Response.json(String(path).endsWith("/refresh")?{checked_at:"2026-11-02T00:00:00Z"}:{items:[{id:"contract-notice",product_id:"product-2",category:"product_contract_violated",resource_name:"客户产品",resource_id:"",contract_version:0,message:"主动核验发现契约违约",read_at:null,created_at:"2026-11-02"}],total:1,unread:1})));
 show(<AssetNotices/>);await userEvent.click(await screen.findByRole("button",{name:"查看契约核验"}));
 expect(location.pathname+location.search).toBe("/platform/assets/contracts?product_id=product-2");
 expect(screen.queryByRole("button",{name:"核对并申请"})).toBeNull();
});

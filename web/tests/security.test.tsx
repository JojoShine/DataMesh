// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,fireEvent,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {App} from "../src/app/App";
import {SecurityControls} from "../src/pages/SecurityControls";
import {SecurityProofs} from "../src/pages/SecurityOperations";
import {ResourceSecurityContext} from "../src/components/ResourceSecurityContext";
import {emptySecurityDefinition} from "../src/api/security";
import {request} from "../src/api/client";
import {useSession} from "../src/stores/session";
const principal={user_id:"author",username:"作者",department_id:"dept",roles:["admin"],platforms:["security","assets","exchange"] as ("security"|"assets"|"exchange")[],csrf_token:"test"};
beforeEach(()=>{history.replaceState(null,"","/platform/security/overview");window.scrollTo=vi.fn();useSession.setState({user:principal,ready:true,identityError:null});Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
function show(child:React.ReactNode){render(<ConfigProvider theme={{token:{motion:false}}}><AntApp>{child}</AntApp></ConfigProvider>)}
it("审计员看到六类安全模块和旧审计入口，不能进入管理写入入口",async()=>{
 const auditor={...principal,roles:["auditor"]};useSession.setState({user:auditor});vi.stubGlobal("fetch",vi.fn(async(path:RequestInfo|URL)=>Response.json(String(path)==="/api/v1/me"?auditor:{counts:{pending:3},checked_at:"2026-11-02T00:00:00Z",scope:"真实审计范围",signing_available:false,capabilities:{}})));
 show(<App/>);await screen.findByRole("heading",{name:"安全总览"});for(const group of ["安全工作台","分类分级","身份与权限","策略与防护","风险监测与处置","审计与核查"]){expect(screen.getByText(group)).toBeTruthy()}
 expect(screen.getByRole("menuitem",{name:/审计记录/})).toBeTruthy();expect(screen.getByRole("menuitem",{name:/水印交付与验证/})).toBeTruthy();expect(screen.queryByRole("menuitem",{name:/账号管理/})).toBeNull();expect(screen.queryByRole("menuitem",{name:/资源权限/})).toBeNull();expect(screen.queryByRole("menuitem",{name:/策略预检/})).toBeNull();
});
it("普通使用者直访安全台不能请求安全管理数据",async()=>{
 const consumer={...principal,roles:["consumer"],platforms:["assets"] as ("assets")[]};useSession.setState({user:consumer});const paths:string[]=[];vi.stubGlobal("fetch",vi.fn(async(path:RequestInfo|URL)=>{paths.push(String(path));return Response.json(consumer)}));show(<App/>);await screen.findByText("当前账号没有此功能权限");expect(paths.some(p=>p.startsWith("/api/v1/security/"))).toBe(false)
});
it("水印作者不能自批，独立复核仍提交原因和当前并发版本",async()=>{
 let row={id:"mark",kind:"watermark",state:"submitted",created_by:"author",version:1,generation:2,published_version:0,definition:{...emptySecurityDefinition,name:"接收方水印"},updated_at:"2026-11-02T00:00:00Z"};const writes:{path:string;body:unknown}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:RequestInfo|URL,options?:RequestInit)=>{if(options?.method==="POST"){writes.push({path:String(path),body:JSON.parse(String(options.body))});return Response.json({...row,state:"published",generation:3,published_version:1})}return Response.json({items:[row],total:1})}));show(<SecurityControls kind="watermark"/>);await userEvent.click(await screen.findByRole("button",{name:"接收方水印"}));expect(screen.queryByRole("button",{name:"批准发布"})).toBeNull();cleanup();row={...row,created_by:"another-admin"};show(<SecurityControls kind="watermark"/>);await userEvent.click(await screen.findByRole("button",{name:"接收方水印"}));await userEvent.click(screen.getByRole("button",{name:"批准发布"}));expect(screen.getAllByRole("dialog")).toHaveLength(1);fireEvent.change(screen.getByPlaceholderText("请说明批准、退回或停用的依据"),{target:{value:"独立核对模板范围"}});await userEvent.click(screen.getByRole("button",{name:"确认批准发布"}));await waitFor(()=>expect(writes).toEqual([{path:"/api/v1/security/controls/mark/approve",body:{expected_version:2,reason:"独立核对模板范围"}}]));
});
it("未知密钥显示无法验证，不将其解释为溯源成功",async()=>{
 const proof={id:"proof-id",resource:{resource_type:"api_resource",resource_id:"r",contract_version:2},action:"invoke",state:"unknown",actor_id:"recipient",department_id:"dept",text:"接收方标记",sha256:"a".repeat(64),key_id:"old-key",size_bytes:10,created_at:"2026-11-02T00:00:00Z"};vi.stubGlobal("fetch",vi.fn(async(_path:RequestInfo|URL,opts?:RequestInit)=>Response.json(opts?.method==="POST"?{result:"unverifiable",reason:"签名密钥版本不可用"}:{items:[proof],total:1})));
 show(<SecurityProofs/>);await userEvent.click(await screen.findByRole("button",{name:"proof-id"}));fireEvent.change(screen.getByRole("textbox",{name:"待核对摘要"}),{target:{value:"a".repeat(64)}});await userEvent.click(screen.getByRole("button",{name:"验证摘要与签名"}));expect(await screen.findByText("无法验证")).toBeTruthy();expect(screen.getByText("签名密钥版本不可用")).toBeTruthy();expect(screen.queryByText("摘要与签名记录匹配")).toBeNull();
});
it("安全跳转只携带资源与版本，目标上下文随版本重新加载",async()=>{
 const paths:string[]=[];vi.stubGlobal("fetch",vi.fn(async(path:RequestInfo|URL)=>{paths.push(String(path));return Response.json({resource:{resource_type:"raw_object",resource_id:"r",contract_version:1},level:"sensitive",mask_fields:["phone"],blocked:false,visible:true})}));const {rerender}=render(<AntApp><ResourceSecurityContext resource={{resource_type:"raw_object",resource_id:"r",contract_version:1}}/></AntApp>);await userEvent.click(await screen.findByRole("button",{name:"查看安全工作台"}));expect(location.search).toContain("contract_version=1");expect(location.search).not.toContain("csrf");rerender(<AntApp><ResourceSecurityContext resource={{resource_type:"raw_object",resource_id:"r",contract_version:2}}/></AntApp>);await waitFor(()=>expect(paths.some(p=>p.includes("contract_version=2"))).toBe(true));
});
it("新增水印响应头不改业务JSON或原有cutoff",async()=>{
 const body={records:[{id:"9007199254740993"}],cutoff:{position:42}};vi.stubGlobal("fetch",vi.fn(async()=>Response.json(body,{headers:{"X-DataMesh-Delivery-ID":"proof-id","X-DataMesh-Watermark":"recipient+mark"}})));let id="";const value=await request("/delivery/v1/tables/r/records",{},15000,response=>{id=response.headers.get("X-DataMesh-Delivery-ID")??""});expect(value).toEqual(body);expect(id).toBe("proof-id");
});

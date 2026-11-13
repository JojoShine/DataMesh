// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp} from "antd";
import {DataServices,ServiceAccess} from "../src/pages/DataServices";
import {PlatformDialog} from "../src/components/PlatformDialog";
import {useSession} from "../src/stores/session";
const fields=[{name:"code",logical_type:"string",nullable:false,primary_key:false,sensitive:false,precision:null,scale:null}];
const service={id:"service-one",name:"客户查询",directory_id:"dir",query:{dataset_id:"source",mode:"snapshot",sql:"SELECT code FROM result",max_page_size:100,source_fields:fields},fields,parameters:[],created_at:"2026-10-30"};
beforeEach(()=>{history.replaceState(null,"","/platform/exchange/data-services");window.scrollTo=vi.fn();useSession.setState({user:{user_id:"provider",username:"provider",department_id:"d",roles:["admin"],platforms:["exchange"],csrf_token:"test"},ready:true});Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
it("复制接口需重新校验与选择目录；预览后修改 SQL 会取消登记资格",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>{if(path.includes("/data-services/validate"))return Response.json({records:[{code:"001"}],fields,parameters:[],dataset_id:"source"});if(path.includes("/sources?"))return Response.json({items:[],has_more:false});if(path.includes("resource-directories"))return Response.json({items:[{directory_id:"dir",version:1,state:"published",definition:{name:"客户信息目录",format:"api",data_layer:"governed_dataset",sharing_method:"invoke"}}],total:1});return Response.json({items:[service],total:1})}));
 render(<AntApp><DataServices/></AntApp>);const user=userEvent.setup();await user.click(await screen.findByRole("button",{name:"复制为新接口"}));expect(screen.getByRole("button",{name:"登记接口"}).hasAttribute("disabled")).toBe(true);await user.click(screen.getByRole("button",{name:"校验并预览"}));await screen.findByText("001");await waitFor(()=>expect(screen.getByRole("button",{name:"登记接口"}).hasAttribute("disabled")).toBe(false));await user.type(screen.getByLabelText("查询 SQL")," ORDER BY code");expect(screen.getByRole("button",{name:"登记接口"}).hasAttribute("disabled")).toBe(true);
},15000);
it("凭据仅本次显示，撤销清空密钥，关闭重开不恢复密钥",async()=>{
 let revoked=false;vi.stubGlobal("fetch",vi.fn(async(path:string,options?:RequestInit)=>{if(path.endsWith("/revoke")){revoked=true;return Response.json({revoked:true})}if(options?.method==="POST")return Response.json({token:"key.secret-once"});return Response.json({items:[{id:"key",expires_at:"2099-01-01",revoked}]})}));
 const view=render(<AntApp><PlatformDialog open title="调用 API"><ServiceAccess serviceID="service-one" parameters={[]}/></PlatformDialog></AntApp>);const user=userEvent.setup();await user.click(await screen.findByRole("button",{name:/创建调用凭据/}));await screen.findByText("key.secret-once");await user.click(screen.getByRole("button",{name:/撤\s*销/}));await waitFor(()=>expect(screen.queryByText("key.secret-once")).toBeNull());view.unmount();render(<AntApp><ServiceAccess serviceID="service-one" parameters={[]}/></AntApp>);expect(screen.queryByText("key.secret-once")).toBeNull();expect(localStorage.getItem("service-token")).toBeNull();
});
it("数据服务复用来源业务目录，不再要求新建 API 目录",async()=>{
 const writes:{path:string;body:Record<string,unknown>}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(path:string,options?:RequestInit)=>{
  if(path.includes('/data-services/validate'))return Response.json({records:[{code:'001'}],fields,parameters:[],dataset_id:'source'});
  if(path==='/api/v1/data-services'&&options?.method==='POST'){writes.push({path,body:JSON.parse(String(options.body))});return Response.json({resource:{resource_type:'api_resource',resource_id:'new-service',contract_version:1}})}
  if(path.includes('/sources?'))return Response.json({items:[],has_more:false});
  if(path.includes('/resource-directories'))return Response.json({items:[{directory_id:'business-dir',version:3,state:'published',definition:{name:'客户业务目录',format:'database',data_layer:'governed_dataset',sharing_method:'query'}}],total:1});
  return Response.json({items:[service],total:1});
 }));
 render(<AntApp><DataServices/></AntApp>);const user=userEvent.setup();
 await user.click(await screen.findByRole('button',{name:'复制为新接口'}));
 await user.click(screen.getByRole('button',{name:'校验并预览'}));await screen.findByText('001');
 expect(screen.queryByRole('button',{name:'新建匹配目录'})).toBeNull();
 await waitFor(()=>expect(screen.getByRole('button',{name:'登记接口'}).hasAttribute('disabled')).toBe(false));
 await user.click(screen.getByRole('button',{name:'登记接口'}));
 await waitFor(()=>expect(writes).toHaveLength(1));
 expect(writes[0].body.directory_id).toBe('business-dir');expect(writes[0].body.expected_directory_version).toBe(3);
 expect(writes[0].body.dataset_id).toBe('source');
});

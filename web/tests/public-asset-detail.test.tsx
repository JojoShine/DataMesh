// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {PublicAssetDetail} from "../src/components/PublicAssetDetail";
import {useSession} from "../src/stores/session";
import type {PublicAssetDetail as Detail} from "../src/api/types";
const asset:Detail={dataset_id:"public-result",name:"客户档案",description:"已审核清洗和质量说明",provider_department_id:"演示部门",field_count:1,usage_restrictions:"仅用于演示",contract_version:1,updated_at:"2026-11-03T00:00:00Z",resource_type:"governed_dataset",details:{resource_code:"DEMO-1",provider_name:"演示单位",classification:"theme",classification_path:"theme/demo",update_frequency:"每日",format:"database",data_layer:"governed_dataset",items:[{name:"customer_id",business_name:"客户编号",logical_type:"integer",nullable:false,sensitive:false,description:"客户唯一编号",length:null,precision:null,scale:null}],sharing_type:"conditional",sharing_method:"query",sharing_conditions:"演示用途",open_conditions:"允许公开目录信息",security_classification:"虚构演示",legal_basis:"演示依据"}};
beforeEach(()=>{history.replaceState(null,"","/");useSession.setState({user:null,ready:true});Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
it("匿名浏览介绍、字段和共享条件，只在申请时进入登录",async()=>{
 const fetcher=vi.fn(async()=>Response.json(asset));vi.stubGlobal("fetch",fetcher);
 render(<PublicAssetDetail asset={asset} onClose={()=>{}}/>);
 await screen.findByText("演示单位");expect(location.pathname).toBe("/");
 await userEvent.click(screen.getByRole("tab",{name:"字段说明（1）"}));expect(await screen.findByText("客户唯一编号")).toBeTruthy();
 await userEvent.click(screen.getByRole("tab",{name:"共享与使用"}));expect(await screen.findByText("演示用途")).toBeTruthy();
 expect(fetcher.mock.calls).toHaveLength(1);
 await userEvent.click(screen.getByRole("button",{name:"登录后申请使用"}));expect(location.pathname).toBe("/login");
});
it("公开资产撤下后显示错误并禁止继续申请",async()=>{
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({code:"NOT_FOUND",message:"资产不存在或未发布"},{status:404})));
 render(<PublicAssetDetail asset={asset} onClose={()=>{}}/>);
 await screen.findByRole("button",{name:/重\s*试/});expect(screen.getByRole("button",{name:"登录后申请使用"}).hasAttribute("disabled")).toBe(true);expect(screen.queryByText("客户唯一编号")).toBeNull();
});

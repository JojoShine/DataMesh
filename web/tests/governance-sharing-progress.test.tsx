// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen} from "@testing-library/react";
import {GovernanceSharingProgress} from "../src/components/GovernanceSharingProgress";
import {useSession} from "../src/stores/session";
beforeEach(()=>{useSession.setState({ready:true,epoch:0,user:{user_id:"owner",username:"owner",department_id:"dept",roles:["admin"],platforms:["governance","exchange"],csrf_token:"test"},identityError:null});Object.defineProperty(window,"matchMedia",{value:()=>({matches:false,addListener(){},removeListener(){}}),writable:true})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
it("按实际目录和挂载状态展示，不把启用等同于使用授权",async()=>{
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>Response.json(path.includes("resource-mounts")?{mount_id:"mount",directory_id:"directory",state:"active",revision_state:"draft",active_revision_id:"old"}:{directory_id:"directory",state:"published",definition:{name:"业务资料"}})));
 render(<GovernanceSharingProgress published canExchange mountIDs={["mount"]}/>);
 expect(await screen.findByText(/业务资料/)).toBeTruthy();expect(await screen.findByText(/当前另有草稿修订/)).toBeTruthy();expect(screen.getByText(/使用申请与授权需按具体使用方核对/)).toBeTruthy();
});
it("挂载证据读取失败时显示待核对，不当成尚未挂载",async()=>{
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({code:"FORBIDDEN",message:"无权查看"},{status:403})));
 render(<GovernanceSharingProgress published canExchange mountIDs={["mount"]}/>);
 expect(await screen.findByText(/挂载证据暂不可读，需有权人员核对/)).toBeTruthy();expect(screen.queryByText("尚未关联目录" )).toBeNull();
});

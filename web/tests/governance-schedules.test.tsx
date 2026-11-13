// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor,within} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App,ConfigProvider} from "antd";
import {GovernanceSchedules} from "../src/components/GovernanceSchedules";
import {useSession} from "../src/stores/session";
vi.setConfig({testTimeout:20000});
beforeEach(()=>{useSession.setState({user:{user_id:"owner",username:"治理人员",department_id:"dept",roles:["governor"],platforms:["governance"],csrf_token:"test"},ready:true,epoch:0,identityError:null});Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
it("adjusts the existing paused plan with its version, retained parameters and a fresh preview",async()=>{
 const posts:{path:string;body:unknown}[]=[],previews:string[]=[];
 const row={id:"schedule",workflow:{id:"first",version:1},state:"paused",version:3,timezone:"Asia/Shanghai",next_run_at:"2030-01-01T01:00:00Z",interval_seconds:3600,parameters:{day:"retained"},missed_policy:"skip",concurrency_policy:"forbid"};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return Response.json({...row,version:4})}
  if(path.includes("/preview?")){previews.push(path);return Response.json({items:Array.from({length:5},(_,i)=>`2030-01-01T0${i+1}:00:00Z`),timezone:"Asia/Shanghai"})}
  return Response.json(path.endsWith("/parameters")?{defaults:{day:"default"}}:{items:path.startsWith("/api/v1/governance/schedules?")?[row]:[],total:1});
 }));
 render(<ConfigProvider theme={{token:{motion:false}}}><App><GovernanceSchedules/></App></ConfigProvider>);
 await userEvent.click(await screen.findByRole("button",{name:"管理调度"}));await userEvent.click(screen.getByRole("button",{name:"调整计划"}));
 expect(screen.getAllByRole("dialog")).toHaveLength(1);const dialog=screen.getByRole("dialog");
 await waitFor(()=>expect((within(dialog).getByLabelText("运行参数 day") as HTMLInputElement).value).toBe("retained"));
 expect((within(dialog).getByLabelText("首次触发（所选时区）") as HTMLInputElement).value).toBe("2030-01-01 09:00");
 const version=within(dialog).getByRole("spinbutton",{name:"目标已发布版本"});await userEvent.clear(version);await userEvent.type(version,"2");
 const interval=within(dialog).getByRole("spinbutton",{name:"间隔数值"});await userEvent.clear(interval);await userEvent.type(interval,"2");
 await waitFor(()=>expect(previews.some(path=>path.includes("interval_seconds=7200"))).toBe(true));
 await waitFor(()=>expect(within(dialog).getByRole("button",{name:"保存计划调整"}).hasAttribute("disabled")).toBe(false));
 await userEvent.click(within(dialog).getByRole("button",{name:"保存计划调整"}));
 await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0]).toMatchObject({path:"/api/v1/governance/schedules/schedule/update",body:{base_version:3,workflow:{id:"first",version:2},parameters:{day:"retained"},interval_seconds:7200,next_run_at:"2030-01-01T01:00:00.000Z"}});
});
it("uses aligned Ant Design date and time panels instead of native controls",async()=>{
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({items:[],total:0})));
 render(<ConfigProvider><App><GovernanceSchedules/></App></ConfigProvider>);
 await userEvent.click(screen.getByRole("button",{name:"新建调度"}));
 const date=screen.getByLabelText("首次触发（所选时区）");
 expect(date.closest(".ant-picker")).not.toBeNull();expect(date.getAttribute("type")).not.toBe("datetime-local");
 await userEvent.click(screen.getByLabelText("执行周期"));await userEvent.click(await screen.findByText("每天",{selector:".ant-select-item-option-content"}));
 expect(screen.getByLabelText("执行时间").closest(".ant-picker")).not.toBeNull();
});

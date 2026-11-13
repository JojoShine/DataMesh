// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {ConfigProvider} from "antd";
import zhCN from "antd/locale/zh_CN";
import {GovernanceDateTimePicker,GovernanceTimePicker} from "../src/components/GovernanceDateInputs";
beforeEach(()=>{Object.defineProperty(window,"matchMedia",{value:()=>({matches:false,addListener(){},removeListener(){}}),writable:true});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals();vi.unstubAllEnvs()});
it("preserves wall time when browser timezone has a daylight-saving gap",()=>{
 vi.stubEnv("TZ","America/New_York");
 render(<ConfigProvider locale={zhCN}><label htmlFor="wall">首次触发</label><GovernanceDateTimePicker id="wall" value="2026-03-08T02:30"/></ConfigProvider>);
 expect(screen.getByLabelText("首次触发")).toHaveProperty("value","2026-03-08 02:30");
});
it("commits Ant Design date input as existing wall-clock text",async()=>{
 const change=vi.fn();render(<ConfigProvider locale={zhCN}><label htmlFor="wall">首次触发</label><GovernanceDateTimePicker id="wall" onChange={change}/></ConfigProvider>);
 await userEvent.type(screen.getByLabelText("首次触发"),"2030-01-01 09:30{Enter}");
 await waitFor(()=>expect(change).toHaveBeenCalledWith("2030-01-01T09:30"));
});
it("time-only control keeps the existing HH:mm contract",async()=>{
 const change=vi.fn();render(<ConfigProvider locale={zhCN}><label htmlFor="clock">执行时间</label><GovernanceTimePicker id="clock" value="09:30" onChange={change}/></ConfigProvider>);
 const clock=screen.getByLabelText("执行时间");expect(clock).toHaveProperty("value","09:30");
 await userEvent.clear(clock);await userEvent.type(clock,"08:15{Enter}");
 await waitFor(()=>expect(change).toHaveBeenCalledWith("08:15"));
});

// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,fireEvent,render,screen,waitFor} from "@testing-library/react";
import {ConfigProvider} from "antd";
import {FloatingAgent} from "../src/components/FloatingAgent";
import {appTheme} from "../src/app/theme";
beforeEach(()=>{
 Object.defineProperty(window,"matchMedia",{writable:true,value:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}})});
 Object.defineProperty(window,"innerHeight",{configurable:true,value:1080});
 Object.defineProperty(window,"innerWidth",{configurable:true,value:1920});
 vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});
 vi.stubGlobal("fetch",vi.fn(()=>{throw new Error("预留入口不能查询数据")}));
});
afterEach(()=>{cleanup();document.querySelectorAll(".qa-agent-obstacle").forEach(element=>element.remove());vi.unstubAllGlobals()});
it("悬浮图标打开待接入说明，Esc关闭并恢复焦点，不发送请求",async()=>{
 render(<ConfigProvider theme={appTheme}><FloatingAgent/></ConfigProvider>);
 const trigger=screen.getByRole("button",{name:"打开AI智能问数"});expect(trigger.getAttribute("aria-expanded")).toBe("false");fireEvent.click(trigger);
 expect(await screen.findByRole("dialog",{name:"AI智能问数"})).toBeTruthy();expect(screen.getByText("待接入")).toBeTruthy();expect(screen.queryByRole("textbox")).toBeNull();
 const close=screen.getByRole("button",{name:/关\s*闭/});await waitFor(()=>expect(document.activeElement).toBe(close));
 fireEvent.keyDown(close,{key:"Escape"});await waitFor(()=>expect(screen.queryByRole("dialog")).toBeNull());expect(document.activeElement).toBe(trigger);expect(fetch).not.toHaveBeenCalled();
});
it("窄屏上为固定操作栏留出位置，并可从底部按钮关闭说明",async()=>{
 Object.defineProperty(window,"innerHeight",{configurable:true,value:844});Object.defineProperty(window,"innerWidth",{configurable:true,value:390});
 render(<ConfigProvider theme={appTheme}><footer className="governance-flow-page-footer" ref={node=>{if(node)node.getBoundingClientRect=()=>new DOMRect(0,704,390,140)}}/><FloatingAgent workbench/></ConfigProvider>);
 const trigger=screen.getByRole("button",{name:"打开AI智能问数"});await waitFor(()=>expect(trigger.getAttribute("style")).toContain("156px"));fireEvent.click(trigger);
 fireEvent.click(await screen.findByRole("button",{name:/关\s*闭/}));await waitFor(()=>expect(trigger.getAttribute("aria-expanded")).toBe("false"));expect(document.activeElement).toBe(trigger);
});
it("业务弹窗打开时让出悬浮入口，弹窗关闭后恢复",async()=>{
 render(<ConfigProvider theme={appTheme}><FloatingAgent/></ConfigProvider>);fireEvent.click(screen.getByRole("button",{name:"打开AI智能问数"}));await screen.findByRole("dialog",{name:"AI智能问数"});
 const modal=document.createElement("div");modal.className="ant-modal-wrap qa-agent-obstacle";modal.style.display="block";document.body.append(modal);
 await waitFor(()=>expect(screen.queryByRole("button",{name:"打开AI智能问数"})).toBeNull());modal.style.display="none";
 const trigger=await screen.findByRole("button",{name:"打开AI智能问数"});expect(trigger.getAttribute("aria-expanded")).toBe("false");expect(screen.queryByRole("dialog",{name:"AI智能问数"})).toBeNull();
});

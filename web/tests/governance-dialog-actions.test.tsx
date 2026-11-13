// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,within,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {Button,ConfigProvider,Form,Input} from "antd";
import {GovernanceDialog,GovernanceDialogActions} from "../src/components/GovernanceDialog";
beforeEach(()=>{Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
it("标题和正文不再放置弹窗命令，嵌套操作汇入同一个底栏",async()=>{
 const close=vi.fn(),save=vi.fn();const view=render(<ConfigProvider theme={{token:{motion:false}}}><GovernanceDialog header="模型" onClose={close} actions={<Button onClick={save}>保存</Button>}><p>字段内容</p><GovernanceDialogActions><Button>发布</Button></GovernanceDialogActions></GovernanceDialog></ConfigProvider>);
 const dialog=screen.getByRole("dialog");const footer=dialog.querySelector('.ant-modal-footer')!;
 expect(within(footer as HTMLElement).getByRole("button",{name:/^关\s*闭$/})).toBeTruthy();expect(within(footer as HTMLElement).getByRole("button",{name:/^保\s*存$/})).toBeTruthy();expect(within(footer as HTMLElement).getByRole("button",{name:/^发\s*布$/})).toBeTruthy();
 expect(dialog.querySelector('.ant-modal-header button')).toBeNull();expect(dialog.querySelector('.ant-modal-body button')).toBeNull();expect(dialog.querySelector('.ant-modal-close')).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:/^保\s*存$/}));expect(save).toHaveBeenCalledTimes(1);
 view.rerender(<ConfigProvider theme={{token:{motion:false}}}><GovernanceDialog header="模型" onClose={close} busy><p>执行中</p></GovernanceDialog></ConfigProvider>);expect(screen.getByRole("button",{name:/^关\s*闭$/}).hasAttribute('disabled')).toBe(true);expect(screen.queryByRole("button",{name:/^发\s*布$/})).toBeNull();
});
it("移入底栏的表单提交仍校验必填项，并传递正确字段",async()=>{
 const submit=vi.fn();render(<ConfigProvider theme={{token:{motion:false}}}><GovernanceDialog header="登记" onClose={()=>{}}><Form id="footer-form" onFinish={submit}><Form.Item name="name" label="名称" rules={[{required:true,message:"请填写名称"}]}><Input/></Form.Item><GovernanceDialogActions><Button form="footer-form" htmlType="submit">登记问题</Button></GovernanceDialogActions></Form></GovernanceDialog></ConfigProvider>);
 const button=screen.getByRole("button",{name:"登记问题"});expect(button.closest('.ant-modal-footer')).toBeTruthy();await userEvent.click(button);await screen.findByText("请填写名称");expect(submit).not.toHaveBeenCalled();await userEvent.type(screen.getByLabelText("名称"),"质量问题");await userEvent.click(button);await waitFor(()=>expect(submit).toHaveBeenCalledWith({name:"质量问题"}));
});

import { Alert, Button, Form, Input, Select, Tooltip } from "antd";
import { useState } from "react";
import { errorMessage, request } from "../api/client";
import type { Principal } from "../api/types";
import { useSession } from "../stores/session";
import { navigate } from "../app/navigation";
import { Brand } from "../components/Brand";
import { ModuleIcon } from "../components/ModuleIcon";
import { modules } from "./Home";
import { UserOutlined, LockOutlined, ArrowLeftOutlined } from "@ant-design/icons";
import {useQuery} from "../hooks/useQuery";
interface TestAccount {username:string;password:string;label:string;description:string}
export function Login() {
  const accounts=useQuery<{items:TestAccount[]}>("/auth/test-accounts");
  const [selectedAccount,setSelectedAccount]=useState<string>();
  const account=accounts.data?.items.find(item=>item.username===selectedAccount)??accounts.data?.items[0];
  const [form] = Form.useForm<{ username: string; password: string }>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  async function submit(value: { username: string; password: string }) {
    setBusy(true);
    setError(null);
    try {
      const user = await request<Principal>("/auth/login", {
        method: "POST",
        body: JSON.stringify(value),
      });
      useSession.getState().setUser(user);
      navigate("/");
    } catch (err) {
      setError(err);
    } finally {
      form.setFieldValue("password", "");
      setBusy(false);
    }
  }
  return (
    <main className="login-page">
      <section className="login-story">
        <Brand subtitle />
        <div className="login-story-copy"><h1>让数据创造<br />更大的价值</h1><p>安全 · 规范 · 共享 · 共赢</p></div>
        <span className="login-art-caption">TRUSTED DATA<br />INFRASTRUCTURE</span>
      </section>
      <section className="login-panel">
        <span className="login-eyebrow">欢迎使用 DataMesh</span><h2>登录工作台</h2>
        <p>使用统一账号登录，按权限访问四个平台。</p>
        {error ? (
          <Alert type="error" title={errorMessage(error)} showIcon />
        ) : null}
        <Form
         
          form={form}
          layout="vertical"
          onFinish={submit}
          requiredMark={false}
        >
          <Form.Item
            name="username"
            label="账号"
            rules={[{ required: true, message: "请输入账号" }]}
          >
            <Input prefix={<UserOutlined />} placeholder="请输入账号" autoComplete="username" maxLength={64} />
          </Form.Item>
          <Form.Item
            name="password"
            label="密码"
            rules={[{ required: true, message: "请输入密码" }]}
          >
            <Input.Password
              prefix={<LockOutlined />}
              autoComplete="current-password"
              placeholder="请输入密码"
              maxLength={72}
            />
          </Form.Item>
          <Button
            htmlType="submit"
            type="primary"
            block
            loading={busy}
          >
            登录
          </Button>
        </Form>
        {accounts.error?<Alert type="info" title="测试账号暂时无法加载" action={<Button onClick={accounts.reload}>重试</Button>}/>:account&&<section className="login-test-accounts" aria-label="测试体验账号">
          <h3>测试体验账号</h3>
          <div className="login-test-account-picker">
            <Select aria-label="选择体验账号" value={account.username} onChange={setSelectedAccount} disabled={busy} options={accounts.data?.items.map(item=>({value:item.username,label:item.label}))}/>
            <Button disabled={busy} onClick={()=>{form.setFieldsValue({username:account.username,password:account.password});setError(null)}}>填入账号</Button>
          </div>
          <Tooltip title={account.description} trigger={["hover","focus","click"]}>
            <p className="login-test-account-description" tabIndex={0}>{account.description}</p>
          </Tooltip>
        </section>}
        <p className="login-account-note">登录成功后返回首页，按需选择平台。</p>
        <div className="login-capabilities">{modules.map(m => <div key={m.id}><ModuleIcon platform={m.id} /><span>{m.name}</span></div>)}</div>
        <Button type="default" icon={<ArrowLeftOutlined />} onClick={() => navigate("/")}>
          返回资产首页
        </Button>
      </section>
    </main>
  );
}

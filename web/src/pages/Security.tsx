import {usePagination} from "../hooks/usePagination";
import {PlatformDialog} from "../components/PlatformDialog";
import {
  App,
  Alert,
  TreeSelect,
  Button,
  Checkbox,

  Form,
  Input,
  Modal,
  Space,
  Table,
  Tag,
} from "antd";
import {organizationTreeOptions} from "./OrganizationManagement";
import {usePath,navigate} from "../app/navigation";
import { useState } from "react";
import type { Account, Audit, Page, OrganizationUnit } from "../api/types";
import { query, errorMessage } from "../api/client";
import { useQuery } from "../hooks/useQuery";
import { useAction } from "../hooks/useAction";
import { useSession } from "../stores/session";
import { Failure, JsonView, Stamp, Title } from "../components/common";
export function AuditPage() {
  const [page,setPage,pagePagination] = usePagination();
  const [search, setSearch] = useState(new URLSearchParams(usePath().split("?")[1]).get("search")??"");
  const [selected, setSelected] = useState<Audit | null>(null);
  const list = useQuery<Page<Audit>>(
    "/api/v1/audit?" + query({ page, search ,page_size:pagePagination.pageSize}),
  );
  return (
    <>
      <Title
        title="审计记录"
        description="只读查看业务操作与数据访问，按对象、操作或追踪号定位。"
        actions={<Button onClick={list.reload}>刷新</Button>}
      />
      <div className="table-toolbar">
        <Input.Search
          placeholder="搜索对象编号、操作或追踪号"
          allowClear
          onSearch={(v) => {
            setSearch(v);
            setPage(1);
          }}
        />
      </div>
      {list.error ? (
        <Failure error={list.error} />
      ) : (
        <Table<Audit>
          rowKey="audit_id"
          loading={list.loading}
          dataSource={list.data?.items}
          scroll={{ x: true }}
          pagination={{...pagePagination,total: list.data?.total}}
          columns={[
            {
              title: "时间",
              dataIndex: "created_at",
              render: (v: string) => <Stamp value={v} />,
            },
            { title: "操作", dataIndex: "action" },
            { title: "对象", dataIndex: "object_id", ellipsis: true },
            { title: "操作者", dataIndex: "actor_id", ellipsis: true },
            {
              title: "结果",
              dataIndex: "result",
              render: (v: string) => (
                <Tag
                  color={
                    v === "success"
                      ? "success"
                      : v === "denied"
                        ? "warning"
                        : "error"
                  }
                >
                  {v}
                </Tag>
              ),
            },
            {
              title: "详情",
              render: (_, r) => (
                <Button type="default" onClick={() => setSelected(r)}>查看追踪</Button>
              ),
            },
          ]}
        />
      )}
      <PlatformDialog footer={null} centered styles={{body:{maxHeight:"70vh",overflowY:"auto"}}}
        title="审计详情"
        open={Boolean(selected)}
        onCancel={() => setSelected(null)}
        width={880}
      >
        <JsonView value={selected} />
      </PlatformDialog>
    </>
  );
}
const roles = [
  ["operator", "数据接入"],
  ["governor", "数据治理"],
  ["provider", "数据负责人"],
  ["consumer", "数据使用"],
  ["auditor", "安全审计"],
  ["admin", "平台管理"],
].map(([value, label]) => ({ value, label }));
type PersonValues={username:string;password?:string;display_name:string;department_id:string;platforms:string[];roles:string[];reason?:string};
export function Accounts(){
 const {message}=App.useApp();const user=useSession(s=>s.user)!;
 const [page,setPage,pagePagination]=usePagination(),[search,setSearch]=useState("");
 const [editor,setEditor]=useState<{row:Account|null}|null>(null);const [enable,setEnable]=useState<Account|null>(null);
 const [form]=Form.useForm<PersonValues>();const [enableForm]=Form.useForm<{reason:string}>();const action=useAction();
 const list=useQuery<Page<Account>>("/api/v1/accounts?"+query({page,search,page_size:pagePagination.pageSize}));
 const units=useQuery<{items:OrganizationUnit[]}>("/api/v1/organization-units");
 const names=new Map(units.data?.items.map(u=>[u.id,u.name]));
 function edit(row:Account|null){form.resetFields();form.setFieldsValue(row?{...row,display_name:row.display_name??"",platforms:row.platforms??[],reason:""}:{username:"",display_name:"",platforms:["exchange","assets"],roles:["consumer"]});setEditor({row})}
 async function save(values:PersonValues){if(!editor)return;try{
  const body=editor.row?{display_name:values.display_name??"",department_id:values.department_id,platforms:values.platforms,roles:values.roles,expected_version:editor.row.version,reason:values.reason}:{username:values.username,password:values.password,display_name:values.display_name??"",department_id:values.department_id,platforms:values.platforms,roles:values.roles};
  await action.run(editor.row?`/api/v1/accounts/${editor.row.user_id}/revisions`:"/api/v1/accounts",body);
  const self=editor.row?.user_id===user.user_id;message.success(editor.row?"人员信息已更新，相关账号需重新登录":"账号已创建");setEditor(null);form.resetFields();list.reload();
  if(self){useSession.getState().setUser(null);navigate("/login")}
 }catch(e){message.error(errorMessage(e))}finally{form.setFieldValue("password","")}}
 async function disable(row:Account){try{await action.run(`/api/v1/accounts/${row.user_id}/disable`);list.reload();message.success("账号已禁用，全部会话已失效")}catch(e){message.error(errorMessage(e))}}
 async function activate(values:{reason:string}){if(!enable)return;try{await action.run(`/api/v1/accounts/${enable.user_id}/enable`,{...values,expected_version:enable.version});setEnable(null);list.reload();message.success("账号已启用")}catch(e){message.error(errorMessage(e))}}
 return <><Title title="人员与账号" description="按组织和部门维护人员，分别配置平台入口、业务角色和资源范围权限。" actions={<Space size={16} wrap><Button onClick={()=>navigate("/platform/security/organizations")}>组织部门</Button><Button onClick={()=>navigate("/platform/security/resource-access")}>资源权限</Button><Button onClick={list.reload}>刷新</Button><Button type="primary" onClick={()=>edit(null)}>新增人员</Button></Space>}/>
 <div className="table-toolbar"><Input.Search allowClear placeholder="搜索账号或人员姓名" onSearch={value=>{setSearch(value);setPage(1)}}/></div>
 <Alert type="info" showIcon title="调岗与权限变更" description="修改人员信息后需重新登录。调岗会撤销原资源范围及加工授权，原有资源和审批记录仍归原部门；平台角色不会自动授予资源审批或取数权限。" style={{marginBottom:24}}/>
 {list.error?<Failure error={list.error}/>:<Table<Account> rowKey="user_id" loading={list.loading} dataSource={list.data?.items} scroll={{x:1050}} pagination={{...pagePagination,total:list.data?.total}} columns={[
 {title:"账号",dataIndex:"username",width:180},{title:"姓名",dataIndex:"display_name",width:140,render:(v:string)=>v||"—"},
 {title:"所属组织 / 部门",dataIndex:"department_id",width:180,render:(v:string)=>names.get(v)??v},
 {title:"角色",dataIndex:"roles",render:(v:string[])=>v.map(r=>roles.find(x=>x.value===r)?.label??r).join("、")||"未配置"},
 {title:"状态",dataIndex:"active",width:90,render:(v:boolean)=><Tag color={v?"green":"default"}>{v?"正常":"已禁用"}</Tag>},
 {title:"操作",width:240,render:(_,row)=><Space size={16}><Button onClick={()=>edit(row)}>维护人员</Button>{row.active?<Button danger disabled={row.user_id===user.user_id||action.busy} onClick={()=>Modal.confirm({title:"禁用此账号？",content:"该账号在四个平台的全部会话都会失效。",okText:"禁用账号",cancelText:"取消",onOk:()=>disable(row)})}>禁用账号</Button>:<Button onClick={()=>{enableForm.resetFields();setEnable(row)}}>启用账号</Button>}</Space>}
 ]}/>}
 <PlatformDialog title={editor?.row?"维护人员":"新增人员"} open={Boolean(editor)} onCancel={()=>setEditor(null)} busy={action.busy} footer={<Button form="person-edit" type="primary" htmlType="submit" loading={action.busy}>{editor?.row?"保存信息":"创建账号"}</Button>}>
 {Boolean(units.error)&&<Failure error={units.error}/>}<Form id="person-edit" form={form} layout="vertical" onFinish={save}>
 <Form.Item label="账号" name="username" rules={[{required:true},{pattern:/^[a-z][a-z0-9_.-]{2,63}$/,message:"以小写字母开头，3–64 位字母、数字或 _.-"}]}><Input disabled={Boolean(editor?.row)} maxLength={64} placeholder="请输入登录账号" autoComplete="off"/></Form.Item>
 <Form.Item label="人员姓名" name="display_name"><Input maxLength={200} placeholder="请输入人员姓名"/></Form.Item>
 {!editor?.row&&<Form.Item label="初始密码" name="password" rules={[{required:true},{min:12,message:"至少 12 位"}]}><Input.Password maxLength={72} placeholder="请设置至少 12 位密码" autoComplete="new-password"/></Form.Item>}
 <Form.Item label="所属组织或部门" name="department_id" rules={[{required:true,message:"请选择所属组织或部门"}]} extra="调岗后需重新配置相应的资源范围及加工授权。"><TreeSelect treeDataSimpleMode treeData={organizationTreeOptions(units.data?.items??[])} loading={units.loading} placeholder="请选择所属组织或部门" treeDefaultExpandAll showSearch treeNodeFilterProp="title" disabled={editor?.row?.user_id===user.user_id}/></Form.Item>
 <Form.Item label="允许进入的平台" name="platforms"><Checkbox.Group options={[{value:"exchange",label:"共享交换"},{value:"governance",label:"数据治理"},{value:"assets",label:"数据资产"},{value:"security",label:"数据安全"}]}/></Form.Item>
 <Form.Item label="业务角色" name="roles" extra="角色决定可执行的业务操作；具体数据范围在资源权限中另行配置。"><Checkbox.Group options={roles}/></Form.Item>
 {editor?.row&&<Form.Item label="变更原因" name="reason" rules={[{required:true,whitespace:true,message:"请填写变更原因"}]}><Input.TextArea rows={2} maxLength={1000} placeholder="请说明人员调岗或权限调整原因"/></Form.Item>}
 </Form></PlatformDialog>
 <PlatformDialog title="启用账号" open={Boolean(enable)} onCancel={()=>setEnable(null)} busy={action.busy} footer={<Button form="person-enable" htmlType="submit" type="primary" loading={action.busy}>启用账号</Button>}><Form id="person-enable" layout="vertical" form={enableForm} onFinish={activate}><Form.Item label="启用原因" name="reason" rules={[{required:true,whitespace:true,message:"请填写启用原因"}]}><Input.TextArea rows={2} maxLength={1000} placeholder="请说明重新启用账号的原因"/></Form.Item></Form></PlatformDialog>
 </>;
}

import {sourceDefinitions,sourceNames,sourceOptions,sourceDefinition,sourceSupportsCDC,sourceOptionalCredentials,sourceConnectionScheme,sourceStatusNames,type SourceType} from "../api/sourceTypes";
import {localPagination,usePagination} from "../hooks/usePagination";
import {PlatformDialog} from "../components/PlatformDialog";
import {
  App,
  Badge,
  Alert,
  Skeleton,
  Button,
  Descriptions,

  Form,
  Grid,
  Input,
  InputNumber,
  Select,
  Space,
  Table,
  Tag,
} from "antd";
import { useState } from "react";
import type { Page, Preflight, Source, SourceObject } from "../api/types";
import { query, errorMessage } from "../api/client";
import { useQuery } from "../hooks/useQuery";
import { useAction } from "../hooks/useAction";
import { useSession } from "../stores/session";
import { DatabaseOutlined, PlusOutlined, ReloadOutlined, SearchOutlined } from "@ant-design/icons";
import "./Sources.css";
import { SourceCheckReport } from "../components/SourceCheckReport";
import { SourceAllowlist } from "./SourceAllowlist";
import { EmptyData, Failure, Status, Title } from "../components/common";
const databaseNames=sourceNames;
function CheckStatus({ value }: { value: string }) {
  return <Badge status={value === "passed" ? "success" : value === "failed" ? "error" : "default"} text={value === "passed" ? "已通过" : value === "failed" ? "检查失败" : "待检查"} />;
}
const templates:Record<string,{host:string;port:number;database:string}>=Object.fromEntries(sourceDefinitions.map(t=>[t.kind,{host:"",port:t.port,database:""}]));
export function Sources() {
  const screens = Grid.useBreakpoint();
  const user = useSession((s) => s.user)!;
  const { message } = App.useApp();
  const [page,setPage,pagePagination] = usePagination();
  const [search, setSearch] = useState("");
  const [searchInput, setSearchInput] = useState("");
  const [open, setOpen] = useState(false);
  const [allowlistOpen,setAllowlistOpen] = useState(false);
  const [draftReport, setDraftReport] = useState<Preflight | null>(null);
  const [report, setReport] = useState<Preflight | null>(null);
  const [reportSource,setReportSource] = useState<Source|null>(null);
  const [selected, setSelected] = useState<Source | null>(null);
  const [form] = Form.useForm<{
    name: string;
    kind: string;
    username: string;
    password: string;
    host: string; port: number; database: string; scheme?:string; auth_database?:string;
  }>();
  const databaseKind = Form.useWatch("kind", form) ?? "postgres";
  const endpoint = templates[databaseKind];
  const definition=sourceDefinition(databaseKind);
  const types=useQuery<{items:SourceType[]}>("/api/v1/source-types");
  const type=types.data?.items?.find(t=>t.kind===databaseKind);
  const selectedType=types.data?.items?.find(t=>t.kind===selected?.kind);
  function closeForm() { setOpen(false); form.resetFields(); setDraftReport(null); }
  const list = useQuery<Page<Source>>(
    "/api/v1/sources?" + query({ page, search ,page_size:pagePagination.pageSize}),
  );
  const objects = useQuery<Page<SourceObject>>(
    selected&&sourceSupportsCDC(selected.kind) ? "/api/v1/sources/" + selected.source_id + "/objects" : null,
  );
  const [chosenObject,setChosenObject]=useState<{schema:string;name:string}|null>(null);
  const extended=Boolean(selected&&!sourceSupportsCDC(selected.kind));
  const catalog=useQuery<{items:{schema:string;name:string;kind:string}[]}>(selected&&extended?"/api/v1/sources/"+selected.source_id+"/tables":null);
  const structure=useQuery<{fields:import("../api/types").Field[]}>(selected&&extended&&chosenObject?"/api/v1/sources/"+selected.source_id+"/table-schema?"+query({schema:chosenObject.schema,table:chosenObject.name}):null);
  const action = useAction();
  async function check(source: Source, kind: string) {
    setReportSource(source);
    try {
      const v = await action.run<Preflight>(
        `/api/v1/sources/${source.source_id}/${kind}`,
      );
      setReport(v);
      setSelected(current => current?.source_id === source.source_id ? { ...current, [kind === "test-connection" ? "connection_status" : "cdc_status"]: v.passed ? "passed" : "failed" } : current);
      list.reload();
    } catch (e) {
      message.error(errorMessage(e));
    }
  }
  async function testDraft() {
    let value;
    try { value = await form.validateFields(["kind","host","port","database","username","password","scheme","auth_database"]); } catch { return; }
    setDraftReport(null);
    try {
      const result = await action.run<Preflight>("/api/v1/sources/test-connection", {kind:value.kind,host:value.host,port:value.port,database:value.database,...(value.scheme?{scheme:value.scheme}:{}),...(value.auth_database?{auth_database:value.auth_database}:{}),credentials:{username:value.username,password:value.password}});
      setDraftReport(result);
    } catch(e) { message.error(errorMessage(e)); }
  }
  async function create(value: {
    name: string;
    kind: string;
    username: string;
    password: string;
    host: string; port: number; database: string; scheme?:string; auth_database?:string;
  }) {
    try {
      await action.run("/api/v1/sources", {
        name: value.name,
        kind: value.kind,
        host:value.host,port:value.port,database:value.database,...(value.scheme?{scheme:value.scheme}:{}),...(value.auth_database?{auth_database:value.auth_database}:{}),
        owner_id: user.user_id,
        credentials: { username: value.username, password: value.password },
      });
      closeForm();
      list.reload();
      message.success("数据源已登记，可检查连接后发现对象并挂载目录");
    } catch (e) {
      message.error(errorMessage(e));
    } finally {
      form.setFieldValue("password", "");
    }
  }
  return (
    <div className="source-page">
      <Title
        title="数据源管理"
        description="登记来源，核实连接与采集条件。"
        actions={
          <>
            {user.roles.includes("admin")&&<Button onClick={()=>setAllowlistOpen(true)}>可信数据源</Button>}
            <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>
              登记数据源
            </Button>
          </>
        }
      />
      <section className="source-list-panel" aria-label="数据源列表">
        <div className="source-list-heading"><h3>数据源列表</h3><span>{list.data ? `${search ? "匹配" : "共"} ${list.data.total} 个数据源` : "正在获取列表"}</span></div>
      <div className="source-filter-bar">
        <Input id="source-search" aria-label="搜索数据源名称" prefix={<SearchOutlined />} placeholder="搜索数据源名称" value={searchInput} allowClear onChange={event => setSearchInput(event.target.value)} onPressEnter={() => { setSearch(searchInput); setPage(1); }} />
        <Button type="primary" onClick={() => { setSearch(searchInput); setPage(1); }}>查询</Button>
        <Button onClick={() => { setSearchInput(""); setSearch(""); setPage(1); }}>重置</Button>
        <Button className="source-refresh" icon={<ReloadOutlined />} onClick={list.reload}>刷新</Button>
      </div>
      {list.error ? (
        <Failure error={list.error} retry={list.reload} />
      ) : (
        <Table<Source>
          rowKey="source_id"
          scroll={{ x: 1400 }}
          dataSource={list.data?.items}
          loading={list.loading}
          locale={{
            emptyText: (
              <EmptyData description="登记第一个数据源，开始接入原始数据。" />
            ),
          }}
          pagination={{...pagePagination,total: list.data?.total,showTotal: (total) => `共 ${total} 条`}}
          columns={[
            {
              title: "数据源",
              dataIndex: "name",
              width: 380,
              render: (v: string, row) => (
                <div className="source-name-cell">
                  <span className="source-db-icon"><DatabaseOutlined /></span>
                  <div><Button type="link" onClick={() => {setChosenObject(null);setSelected(row)}}>{v}</Button>
                  <span className="source-subline">{databaseNames[row.kind] ?? row.kind} {row.configured_version}</span></div>
                </div>
              ),
            },
            { title: "提供部门", dataIndex: "department_id", width: 220 },
            {
              title: "连接检查",
              dataIndex: "connection_status",
              render: (v: string) => <CheckStatus value={v} />,
            },
            {
              title: "采集条件",
              dataIndex: "cdc_status",
              render: (v: string,row) => sourceSupportsCDC(row.kind)?<CheckStatus value={v}/>:<Tag>只读接入</Tag>,
            },
            { title: "凭据", render: (_, row) => <span className="source-credential">{row.credential_configured ? "已加密配置" : "未配置"}</span> },
            {
              title: "操作",
              fixed: screens.md ? "right" : undefined,
              width: 260,
              render: (_, row) => (
                <Space size={16}>
                  <Button
                    type="default"
                    disabled={action.busy}
                    onClick={() => check(row, "test-connection")}
                  >
                    测试连接
                  </Button>
                  <Button
                    type="default"
                    disabled={action.busy||!sourceSupportsCDC(row.kind)}
                    onClick={() => check(row, "preflight-cdc")}
                  >
                    CDC 预检
                  </Button>
                </Space>
              ),
            },
          ]}
        />
      )}
      </section>
      {user.roles.includes("admin")&&<SourceAllowlist open={allowlistOpen} onClose={()=>setAllowlistOpen(false)}/>}
      <PlatformDialog
        centered
        closeLabel="取消" className="source-modal"
        width="min(736px, calc(100vw - 32px))"
        title="登记数据源"
        open={open}
        onCancel={action.busy?undefined:closeForm}
       
       
        destroyOnHidden
        footer={<Space wrap><Button loading={action.busy} onClick={()=>void testDraft()}>测试连接</Button><Button type="primary" loading={action.busy} onClick={() => form.submit()}>保存数据源</Button></Space>}
      >
        <p className="source-form-intro">填写连接信息后可先测试连接，再保存数据源。</p>
        <Form
          form={form}
          layout="vertical"
          onFinish={create}
          disabled={action.busy}
          onValuesChange={()=>setDraftReport(null)}
          initialValues={{ kind: "postgres", ...templates.postgres }}
        >
          <h3 className="source-form-section">基本信息</h3>
          <Form.Item
            name="name"
            label="数据源名称"
            rules={[{ required: true }]}
          >
            <Input placeholder="例如：业务系统客户库" maxLength={200} />
          </Form.Item>
          <Form.Item name="kind" label="数据源类型">
            <Select placeholder="请选择需要连接的数据库类型" onChange={kind=>{form.setFieldValue("port",templates[kind].port);form.setFieldValue("database",kind==="redis"?"0":kind==="hbase"?"default":"");form.setFieldValue("scheme",sourceConnectionScheme(kind)?"https":undefined);form.setFieldValue("auth_database",undefined);setDraftReport(null)}}
              options={sourceOptions}
            />
          </Form.Item>
          <div className="source-form-grid"><Form.Item name="host" label="数据库地址" rules={[{required:true,message:"请输入已获准的数据库地址"}]}><Input placeholder="请输入数据库 IP 或域名"/></Form.Item><Form.Item name="port" label="端口" rules={[{required:true,message:"请输入端口"}]}><InputNumber min={1} max={65535} placeholder="例如 5432" style={{width:"100%"}}/></Form.Item><Form.Item name="database" label={definition.database_label} rules={[{required:true,message:"请输入数据库名称"}]}><Input placeholder={databaseKind==="oracle"?"例如 ORCLPDB1":"请输入"+definition.database_label}/></Form.Item></div><p className="helper-text">连接地址须在管理员「可信数据源」白名单内。库表挂载使用只读查询，无需配置 CDC。</p>
          {databaseKind==="mongodb"&&<Form.Item name="auth_database" label="认证库（选填）" extra="默认使用所填数据库；账号在 admin 等其他库创建时填写其认证库。"><Input maxLength={128} placeholder="例如 admin"/></Form.Item>}
          {sourceConnectionScheme(databaseKind)&&<Form.Item name="scheme" label={type?.http?"连接协议":"传输加密"}><Select options={type?.http?[{value:"https",label:"HTTPS"},{value:"http",label:"HTTP"}]:[{value:"https",label:"TLS 加密"},{value:"http",label:"不使用 TLS"}]}/></Form.Item>}
          {type?.reader&&<Alert className="detail-section" showIcon type={type.status==="ready"?"info":"warning"} title={sourceStatusNames[type.status]} description={"支持只读对象发现、结构、预览及全量抽取；每份来源最多10000条/20MiB。暂不作为写入目标，不提供CDC。"+(type.category==="国产数据库"?"需对应版本厂商驱动；实际版本兼容性待实例联调。":"")+ (["mongodb","elasticsearch","redis","hbase"].includes(databaseKind)?"使用稳定记录键及完整JSON文档结构接入，保留嵌套和二进制内容。":"")}/>}
          {Boolean(types.error)&&<Failure error={types.error} retry={types.reload}/>}
          <h3 className="source-form-section">访问凭据</h3>
          <div className="source-form-grid">
          <Form.Item
            name="username"
            label="数据库账号"
            rules={[{ required: !sourceOptionalCredentials(databaseKind) }]}
          >
            <Input placeholder="请输入数据库账号" autoComplete="off" maxLength={128} />
          </Form.Item>
          <Form.Item
            name="password"
            label="数据库密码"
            rules={[{ required: !sourceOptionalCredentials(databaseKind) }]}
          >
            <Input.Password placeholder="请输入数据库密码" autoComplete="new-password" maxLength={4096} />
          </Form.Item>
          </div>
          {draftReport&&<Alert type={draftReport.passed?"success":"error"} showIcon title={draftReport.passed?"连接成功（尚未保存）":"连接失败"} description={draftReport.checks.map(check=>check.reason).filter(Boolean).join("；")}/> }
          <p className="source-security-note">凭据加密保存，保存后不展示密码。</p>
        </Form>
      </PlatformDialog>
      <SourceCheckReport closeLabel={selected?"返回数据源详情":"关闭"} report={report} sourceName={reportSource?.name} busy={action.busy} onClose={()=>setReport(null)} onRetry={()=>{if(reportSource&&report)void check(reportSource,report.kind==="cdc"?"preflight-cdc":"test-connection")}}/>
      <PlatformDialog centered styles={{body:{overflowY:"auto"}}}
        rootClassName="source-drawer"
        title="数据源详情"
        open={Boolean(selected)&&!report}
        onCancel={() => setSelected(null)}
        width={720}
        footer={null}
      >
        {selected && <section className="source-detail-overview">
          <div className="source-detail-name"><span className="source-db-icon"><DatabaseOutlined /></span><div><h2>{selected.name}</h2><span>{databaseNames[selected.kind] ?? selected.kind} {selected.configured_version}</span></div></div>
          <h3 className="source-form-section">基本信息</h3>
          <Descriptions column={1} colon={false} items={[
            { key: "department", label: "提供部门", children: selected.department_id },
            { key: "kind", label: "数据源类型", children: databaseNames[selected.kind] ?? selected.kind },
            { key: "version", label: "配置版本", children: selected.configured_version },
            { key: "credential", label: "凭据状态", children: selected.credential_configured ? "已加密配置" : "未配置" },
          ]} />
          <div className="source-detail-checks"><h3 className="source-form-section">连接与采集</h3>
            <div className="source-check-statuses"><span>连接检查 <CheckStatus value={selected.connection_status} /></span><span>CDC 条件 {sourceSupportsCDC(selected.kind)?<CheckStatus value={selected.cdc_status}/>:<Tag>暂不支持</Tag>}</span></div>
            <Space wrap><Button icon={<ReloadOutlined />} disabled={action.busy} onClick={() => check(selected, "test-connection")}>检查连接</Button><Button disabled={action.busy||!sourceSupportsCDC(selected.kind)} onClick={() => check(selected, "preflight-cdc")}>CDC 预检</Button></Space>
          </div>
        </section>}
        {selected&&extended&&<section className="detail-section">
          <h3>{sourceDefinition(selected.kind).objects_label}</h3>
          <p className="helper-text">只读接入；完整读取与样例预览在目录挂载的资源中办理。每份来源最多10000条/20MiB，不提供CDC和目标写入。</p>
          {selectedType&&<Tag>{sourceStatusNames[selectedType.status]}</Tag>}
          {catalog.error?<Failure error={catalog.error} retry={catalog.reload}/>:<Table rowKey={r=>r.schema+"."+r.name} loading={catalog.loading} dataSource={catalog.data?.items} pagination={localPagination} scroll={{x:480}} columns={[{title:"命名空间",dataIndex:"schema"},{title:"对象名称",render:(_,r)=><Button type="link" onClick={()=>setChosenObject(r)}>{r.name}</Button>},{title:"对象类型",dataIndex:"kind"}]}/>}
          {chosenObject&&<section className="detail-section"><h3>{chosenObject.name} · 结构</h3>{structure.error?<Failure error={structure.error} retry={structure.reload}/>:<Table rowKey="name" loading={structure.loading} dataSource={structure.data?.fields} pagination={false} scroll={{x:480}} columns={[{title:"字段",dataIndex:"name"},{title:"类型",dataIndex:"logical_type"},{title:"主键",render:(_,f)=>f.primary_key?"是":"否"},{title:"允许空值",render:(_,f)=>f.nullable?"是":"否"}]}/>}</section>}
        </section>}
        {!extended&&<>
        <h3 className="source-form-section">数据对象</h3>
        {objects.loading ? <Skeleton active /> : objects.error ? (
          <Failure error={objects.error} />
        ) : (
          !objects.data?.items.length ? <EmptyData description="暂无数据对象，完成 CDC 预检后可查看字段信息。" /> : objects.data.items.map((object) => (
            <section className="source-object-section" key={object.object_id}>
              <Descriptions column={1} colon={false}
                items={[
                  {
                    key: "name",
                    label: "来源表",
                    children: object.qualified_name,
                  },
                  {
                    key: "version",
                    label: "结构版本",
                    children: object.schema_version,
                  },
                ]}
              />
              <Table
                rowKey="name"
                dataSource={object.fields}
                pagination={false}
                columns={[
                  { title: "字段", dataIndex: "name" },
                  { title: "类型", dataIndex: "logical_type" },
                  {
                    title: "主键",
                    dataIndex: "primary_key",
                    render: (v: boolean) => (v ? "是" : "否"),
                  },
                  {
                    title: "敏感标记",
                    dataIndex: "sensitive",
                    render: (v: boolean) => (v ? <Tag color="orange">需脱敏</Tag> : "普通"),
                  },
                ]}
              />
            </section>
          ))
        )}
        </>}
        <p className="helper-text">敏感字段在授权交付时按策略脱敏。</p>
      </PlatformDialog>
    </div>
  );
}

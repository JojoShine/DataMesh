import {applicationURL} from "../app/basePath";
import {pageSizeOptions,usePagination} from "../hooks/usePagination";
import {DeliveryWatermark} from "../components/ResourceSecurityContext";
import {AssetFeedbackForm,AssetFeedbackList} from "../components/AssetFeedback";
import {navigate,usePath} from "../app/navigation";
import {ServiceAccess} from "./DataServices";
import {PlatformDialog,DialogActions} from "../components/PlatformDialog";
import { deliveryMethod, resourceLabel } from "../api/types";
import {Modal,
  App,
  Alert,
  Button,
  Descriptions,

  Form,
  Input,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
} from "antd";
import { useEffect, useRef, useState } from "react";
import type { Application, ApplicationMaterials, Delivery, Grant, Page } from "../api/types";
import { errorMessage, query, request } from "../api/client";
import { useQuery } from "../hooks/useQuery";
import { useAction } from "../hooks/useAction";
import { useSession } from "../stores/session";
import { Failure, JsonView, Stamp, Status, Title } from "../components/common";
export function Applications({assets=false}:{assets?:boolean}={}) {
  const user = useSession((s) => s.user)!;
  const { message } = App.useApp();
  const [scope, setScope] = useState("mine");
  const [page,setPage,pagePagination] = usePagination();
  const [selected, setSelected] = useState<Application | null>(null);
  const [decision, setDecision] = useState<{
    row: Application;
    action: string;
  } | null>(null);
  const [form] = Form.useForm<{ reason: string }>();
 const [supplement,setSupplement]=useState<Application|null>(null);
 const [supplementForm]=Form.useForm<ApplicationMaterials>();
  const action = useAction();
  async function resubmit(v:ApplicationMaterials){if(!supplement)return;try{await action.run(`/api/v1/applications/${supplement.application_id}/resubmit`,{...v,method:deliveryMethod(supplement.request.resource)});setSupplement(null);supplementForm.resetFields();list.reload();message.success("补正材料已重新提交")}catch(e){message.error(errorMessage(e))}}
 const canApprove =
    !assets && (user.roles.includes("admin") || user.roles.includes("provider"));
  const list = useQuery<Page<Application>>(
    (assets?"/api/v1/assets/applications?":"/api/v1/applications?") + query({ page, scope ,page_size:pagePagination.pageSize}),
  );
  async function run(row: Application, kind: string, reason?: string) {
    try {
      await action.run(
        `/api/v1/applications/${row.application_id}/${kind}`,
        reason ? { reason } : {},
      );
      setDecision(null);
      form.resetFields();
      list.reload();
      setSelected(null);
      message.success(
        kind === "approve" ? "审批通过，授权已在同一事务中生成" : "处理完成",
      );
    } catch (e) {
      message.error(errorMessage(e));
    }
  }
  return (
    <>
      <Title
        title={assets?"我的申请":"数据使用申请审批"}
        description="审批谁可以使用哪项资源、哪些字段及有效期限。通过后自动生成数据使用授权。"
        actions={<Space size={16}>{assets&&<Button onClick={()=>navigate("/platform/assets/grants")}>查看已授权资产</Button>}<Button onClick={list.reload}>刷新</Button></Space>}
      />
      {!assets&&<Alert className="detail-section" style={{marginBottom:24}} type="info" showIcon title="这里查看数据使用申请" description="治理加工另由来源责任部门在“数据加工授权”批准，记录不会出现在本申请列表。管理员直接治理也不会自动生成申请；普通治理人员仍需取得对应版本、字段、用途和期限的加工授权。"/>}
      <Tabs
        activeKey={scope}
        onChange={(v) => {
          setScope(v);
          setPage(1);
        }}
        items={[
          { key: "mine", label: "我的申请" },
          ...(canApprove
            ? [
                { key: "pending", label: "待我审批" },
                { key: "processed", label: "已处理" },
              ]
            : []),
        ]}
      />
      {list.error ? (
        <Failure error={list.error} />
      ) : (
        <Table<Application>
          scroll={{x:1300}}
          rowKey="application_id"
          dataSource={list.data?.items}
          loading={list.loading}
          pagination={{...pagePagination,total: list.data?.total}}
          columns={[
            {
              title: "申请编号",
              render: (_, r) => (
                <Button type="link" onClick={() => setSelected(r)}>
                  {r.application_id.slice(0, 12)}
                </Button>
              ),
            },
            {
              title: "数据层",
              render: (_, r) => (
                <Tag
                  color={
                    r.request.resource.resource_type === "raw_object"
                      ? "gold"
                      : "blue"
                  }
                >
                  {resourceLabel(r.request.resource)}
                </Tag>
              ),
            },
            { title: "申请资源", render: (_, r) => r.resource_name || r.request.resource.resource_id },
            { title: "申请部门", render: (_, r) => r.request.department_id },
            { title: "使用用途", render: (_, r) => r.request.purpose },
            {
              title: "状态",
              dataIndex: "state",
              render: (v: string) => <Status value={v} />,
            },
            {
              title: "到期时间",
              render: (_, r) => <Stamp value={r.request.expires_at} />,
            },
            {
              title: "操作",
              render: (_, r) => (
                <Space size={16}>
                  {r.state==="needs_supplement"&&(user.roles.includes("admin")||r.applicant_id===user.user_id)&&<Button type="primary" onClick={()=>{setSupplement(r);supplementForm.setFieldsValue(r.request.materials??{})}}>补正材料</Button>}
                  {r.state === "submitted" &&
                    (user.roles.includes("admin") || r.applicant_id === user.user_id) && (
                      <Button type="default"
                        onClick={() =>
                          setDecision({ row: r, action: "withdraw" })
                        }
                      >
                        撤回
                      </Button>
                    )}
                  {r.state === "submitted" &&
                    (user.roles.includes("admin") || r.applicant_id !== user.user_id) &&
                    canApprove && (
                      <>
                        <Button
                          type="default"
                          disabled={action.busy}
                          onClick={() =>
                            Modal.confirm({
                              title: "批准此申请？",
                              content: "批准后将按申请字段和期限生成授权。",
                              okText: "确认批准",
                              cancelText: "取消",
                              onOk: () => run(r, "approve"),
                            })
                          }
                        >
                          批准
                        </Button>
                        <Button onClick={()=>setDecision({row:r,action:"request-supplement"})}>要求补正</Button>
                        <Button type="default"
                          danger
                          onClick={() =>
                            setDecision({ row: r, action: "reject" })
                          }
                        >
                          驳回
                        </Button>
                      </>
                    )}
                </Space>
              ),
            },
          ]}
        />
      )}
      <PlatformDialog centered open={Boolean(supplement)} title="补正申请材料" footer={null} destroyOnHidden onCancel={()=>{setSupplement(null);supplementForm.resetFields()}}><Form id="dialog-exchange-0" form={supplementForm} layout="vertical" onFinish={resubmit}>{[{name:"legal_basis",placeholder:"请填写数据使用的法律、政策或业务依据",label:"使用依据"},{name:"scenario",placeholder:"请说明使用场景及需要解决的业务问题",label:"使用场景"},{name:"scope",placeholder:"请说明使用对象、字段范围及业务范围",label:"使用范围"},{name:"department_approval_ref",placeholder:"请输入需求部门负责人同意记录的编号或引用",label:"需求部门负责人同意记录"}].map(f=><Form.Item key={f.name} name={f.name} label={f.label} rules={[{required:true}]}><Input.TextArea placeholder={f.placeholder} rows={2} maxLength={f.name==="department_approval_ref"?1000:2000}/></Form.Item>)}<DialogActions><Button form="dialog-exchange-0" type="primary" htmlType="submit" loading={action.busy}>重新提交</Button></DialogActions></Form></PlatformDialog>
      <PlatformDialog centered
        title={decision?.action === "request-supplement"?"一次性告知补正要求":decision?.action === "reject" ? "驳回申请" : "撤回申请"}
        open={Boolean(decision)}
        onCancel={() => {
          setDecision(null);
          form.resetFields();
        }}
        footer={null}
        destroyOnHidden
      >
        <Form id="dialog-exchange-1"
          form={form}
          layout="vertical"
          onFinish={(v) =>
            decision && run(decision.row, decision.action, v.reason)
          }
        >
          <Form.Item label="原因" name="reason" rules={[{ required: true }]}>
            <Input.TextArea placeholder="请说明处理原因及具体要求" rows={3} maxLength={1000} />
          </Form.Item>
          <DialogActions><Button form="dialog-exchange-1" htmlType="submit" type="primary" loading={action.busy}>
            提交处理
          </Button></DialogActions>
        </Form>
      </PlatformDialog>
      <PlatformDialog footer={null} centered styles={{body:{overflowY:"auto"}}}
        title="申请详情"
        open={Boolean(selected)}
        onCancel={() => setSelected(null)}
        width={880}
      >
        {selected && (
          <>
            <Descriptions
              column={1}
              items={[
                {
                  key: "id",
                  label: "申请编号",
                  children: selected.application_id,
                },
                {
                  key: "purpose",
                  label: "用途",
                  children: selected.request.purpose,
                },
                {
                  key: "department",
                  label: "申请部门",
                  children: selected.request.department_id,
                },
                {
                  key: "fields",
                  label: "字段范围",
                  children: selected.request.fields.join("、"),
                },
                {
                  key: "expire",
                  label: "有效期至",
                  children: <Stamp value={selected.request.expires_at} />,
                },
                {
                  key: "status",
                  label: "状态",
                  children: <Status value={selected.state} />,
                },
                {
                  key: "reason",
                  label: "处理原因",
                  children: selected.decision_reason ?? "无",
                },
              ]}
            />
            <h3>资源契约</h3>
            <JsonView value={selected.request.resource} />
          </>
        )}
      </PlatformDialog>
    </>
  );
}
export function Grants({ security = false, assets = false }: { security?: boolean; assets?: boolean }) {
  const route=usePath();const feedbackTab=assets&&new URLSearchParams(route.split("?")[1]).get("tab")==="feedback";
  const [feedbackGrant,setFeedbackGrant]=useState<Grant|null>(null);
  const user = useSession((s) => s.user)!;
  const { message } = App.useApp();
  const [page,setPage,pagePagination] = usePagination();
  const list = useQuery<Page<Grant>>(
    (security ? "/api/v1/security/grants" : assets?"/api/v1/assets/grants":"/api/v1/grants") +
      "?" +
      query({ page ,page_size:pagePagination.pageSize}),
  );
  const [isDataService,setIsDataService]=useState(false);const [servicePageSize,setServicePageSize]=useState(50);
 const [selected, setSelected] = useState<Grant | null>(null);
  const [data, setData] = useState<Delivery | null>(null);
  const [securityMark,setSecurityMark]=useState({text:"",deliveryID:""});
  function readSecurityMark(response:Response){const raw=response.headers.get("X-DataMesh-Watermark")??"";let text=raw;try{text=decodeURIComponent(raw.replace(/\+/g," "))}catch{/* Display an opaque mark if an upstream response uses invalid escaping. */}setSecurityMark({text,deliveryID:response.headers.get("X-DataMesh-Delivery-ID")??""})}
  const [apiGrant,setAPIGrant] = useState<Grant|null>(null);
  const [apiParameters,setAPIParameters] = useState<{name:string;type:string;required:boolean}[]>([]);
  const [apiForm] = Form.useForm<Record<string,string>>();
  const [recordPageSize,setRecordPageSize]=useState(15);
  const [history, setHistory] = useState<(string | undefined)[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const inFlight = useRef<AbortController | null>(null);
  useEffect(() => () => inFlight.current?.abort(), []);
  const action = useAction();
  const canRevoke =
    !assets && (user.roles.includes("admin") || user.roles.includes("provider"));
  async function load(grant: Grant, cursor?: string, next = false, size = recordPageSize) {
    inFlight.current?.abort();
    const controller = new AbortController();
    inFlight.current = controller;
    setError(null);
    setLoading(true);
    const ref = grant.scope.resource;
    try {
      const response = await request<Delivery>(
        `/delivery/v1/${ref.resource_type === "raw_object" ? "raw" : ref.resource_type === "table_resource" ? "tables" : "datasets"}/${ref.resource_id}/records?` +
          query({
            contract_version: ref.contract_version,
            limit: size,
            fields: grant.scope.fields.join(","),
            cursor,
          }),
        { signal: controller.signal },
        15000, readSecurityMark,
      );
      if (controller.signal.aborted) return;
      setSelected(grant);
      setData(response);
      if (next) setHistory((h) => [...h, cursor]);
      else setHistory([cursor]);
    } catch (e) {
      if (!controller.signal.aborted) {
        setError(e);
        setData(null);
        setSelected(grant);
      }
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  }
  async function useGrant(grant: Grant) {
    const ref=grant.scope.resource;
    if(deliveryMethod(ref)==="query") return load(grant);
    setLoading(true);
    try {
      if(deliveryMethod(ref)==="download") {
        const response=await fetch(applicationURL(`/delivery/v1/files/${ref.resource_id}/content?`+query({contract_version:ref.contract_version})),{credentials:"same-origin",signal:AbortSignal.timeout(60000)});
        if(!response.ok) { const body:unknown=await response.json(); throw new Error(typeof body==="object"&&body!==null&&"message" in body?String(body.message):"下载失败"); }
        const blob=await response.blob(); const url=URL.createObjectURL(blob); const link=document.createElement("a");
        const encoded=response.headers.get("Content-Disposition")?.match(/filename\*=UTF-8''([^;]+)/i)?.[1];
        const plain=response.headers.get("Content-Disposition")?.match(/filename="?([^";]+)/i)?.[1];
        link.href=url; link.download=encoded?decodeURIComponent(encoded):plain??"资源文件"; link.click(); setTimeout(()=>URL.revokeObjectURL(url),60000);
      } else {
        const result=await request<{parameters: typeof apiParameters;data_service?:boolean;max_page_size?:number}>(`/delivery/v1/apis/${ref.resource_id}/schema?`+query({contract_version:ref.contract_version}));
        setIsDataService(Boolean(result.data_service));setServicePageSize(result.max_page_size??50);setAPIParameters(result.parameters??[]); apiForm.resetFields(); setAPIGrant(grant);
      }
    } catch(e) { message.error(e instanceof Error?e.message:errorMessage(e)); } finally { setLoading(false); }
  }
  async function invoke(values:Record<string,string>) {
    if(!apiGrant)return; setLoading(true);
    try { const result=await request<Delivery>(`/delivery/v1/apis/${apiGrant.scope.resource.resource_id}/invoke`,{method:"POST",body:JSON.stringify({contract_version:apiGrant.scope.resource.contract_version,fields:apiGrant.scope.fields,parameters:values})},15000,readSecurityMark); setData(result);setSelected(apiGrant);setError(null);setHistory([]);setAPIGrant(null); }
    catch(e){message.error(errorMessage(e))}finally{setLoading(false)}
  }
  async function revoke(row: Grant) {
    try {
      await action.run(`/api/v1/grants/${row.grant_id}/revoke`);
      list.reload();
      setSelected(null);
      setData(null);
      message.success("授权已撤销，后续请求将被拒绝");
    } catch (e) {
      message.error(errorMessage(e));
    }
  }
  return (
    <>
      <Title
        title={security ? "授权管理" : assets?"我的资产":"数据使用授权"}
        description="授权来自已批准的数据使用申请，可按字段与期限查询、下载或调用资源；加工授权另行管理。"
        actions={<Button onClick={list.reload}>刷新</Button>}
      />
      {assets&&<Tabs activeKey={feedbackTab?"feedback":"grants"} onChange={key=>navigate(`/platform/assets/grants${key==="feedback"?"?tab=feedback":""}`)} items={[{key:"grants",label:"已授权资产"},{key:"feedback",label:"我的反馈"}]}/>}
      {feedbackTab?<AssetFeedbackList/>:list.error ? (
        <Failure error={list.error} />
      ) : (
        <Table<Grant>
          scroll={{x:1500}}
          rowKey="grant_id"
          loading={list.loading}
          dataSource={list.data?.items}
          pagination={{...pagePagination,total: list.data?.total}}
          columns={[
            { title: "授权编号", render: (_, r) => r.grant_id.slice(0, 12) },
            {
              title: "数据层",
              render: (_, r) => (
                <Tag>
                  {resourceLabel(r.scope.resource)}
                </Tag>
              ),
            },
            { title: "授权资源", render: (_, r) => <div>{r.resource_name || r.scope.resource.resource_id}{r.availability_reason&&<p className="helper-text">{r.availability_reason}</p>}</div> },
            { title: "来源申请", render: (_, r) => r.application_id },
            { title: "使用部门", render: (_, r) => r.scope.department_id },
            { title: "字段范围", render: (_, r) => r.scope.fields.join("、") },
            {
              title: "到期时间",
              render: (_, r) => <Stamp value={r.scope.expires_at} />,
            },
            {
              title: "状态",
              dataIndex: "state",
              render: (v: string) => <Status value={v} />,
            },
            {
              title: "操作",
              render: (_, r) => (
                <Space size={16}>
                  {(user.roles.includes("admin") || r.grantee_user_id === user.user_id) && (
                    <Button type="default"
                      loading={loading}
                      disabled={r.state !== "active" || r.available === false}
                      onClick={() => useGrant(r)}
                    >
                      {deliveryMethod(r.scope.resource)==="download"?"下载文件":deliveryMethod(r.scope.resource)==="invoke"?"调用 API":"查询数据"}
                    </Button>
                  )}
                  {assets&&<Button onClick={()=>setFeedbackGrant(r)}>反馈问题</Button>}
                  {canRevoke && r.state === "active" && (
                    <Button type="default"
                      danger
                      disabled={action.busy}
                      onClick={() =>
                        Modal.confirm({
                          title: "撤销此授权？",
                          content: "撤销后新的数据查询将立即被拒绝。",
                          okText: "撤销授权",
                          cancelText: "取消",
                          onOk: () => revoke(r),
                        })
                      }
                    >
                      撤权
                    </Button>
                  )}
                </Space>
              ),
            },
          ]}
        />
      )}
      {feedbackGrant&&<PlatformDialog open title={`反馈问题 · ${feedbackGrant.resource_name||feedbackGrant.scope.resource.resource_id}`} onCancel={()=>setFeedbackGrant(null)} width={760} destroyOnHidden><AssetFeedbackForm resource={feedbackGrant.scope.resource} grantID={feedbackGrant.grant_id} fields={feedbackGrant.scope.fields} onDone={()=>{setFeedbackGrant(null);navigate("/platform/assets/grants?tab=feedback")}}/></PlatformDialog>}
      <PlatformDialog centered open={Boolean(apiGrant)} title="调用授权 API" onCancel={()=>setAPIGrant(null)} footer={null} destroyOnHidden>
        <Form id="dialog-exchange-2" form={apiForm} layout="vertical" onFinish={invoke}>
          {apiParameters.length===0&&<p>该接口无需填写参数。</p>}
          {apiParameters.map(p=><Form.Item key={p.name} name={p.name} label={`${p.name}（${p.type}）`} rules={[{required:p.required,message:"请填写"+p.name}]}><Input placeholder={`请输入 ${p.name}`}/></Form.Item>)}
          <DialogActions><Button form="dialog-exchange-2" type="primary" htmlType="submit" loading={loading}>调用</Button></DialogActions>
        </Form>
        {isDataService&&apiGrant&&<ServiceAccess key={apiGrant.scope.resource.resource_id} serviceID={apiGrant.scope.resource.resource_id} parameters={apiParameters} maxPageSize={servicePageSize}/>}
      </PlatformDialog>
      <PlatformDialog footer={null} centered styles={{body:{overflowY:"auto"}}}
        title="受控数据查询"
        open={Boolean(selected)}
        onCancel={() => {
          inFlight.current?.abort();
          setLoading(false);
          setSelected(null);
          setData(null);
          setError(null);
        }}
        width={880}
      >
        <Descriptions
          column={1}
          items={[
            {
              key: "purpose",
              label: "用途",
              children: selected?.scope.purpose,
            },
            {
              key: "fields",
              label: "允许字段",
              children: selected?.scope.fields.join("、"),
            },
          ]}
        />
        {error ? (
          <Failure error={error} retry={() => selected && load(selected)} />
        ) : (
          data && (
            <>
              {(securityMark.text||securityMark.deliveryID)&&<DeliveryWatermark {...securityMark}/>}
              <p className="helper-text">
                敏感字段按策略脱敏。这里显示的是当前授权允许的数据。
              </p>
              {data.records ? (
                <Table<Record<string, unknown>>
                  rowKey={(_, index) => String(index)}
                  scroll={{ x: true }}
                  dataSource={data.records}
                  pagination={false}
                  columns={selected?.scope.fields.map((field) => ({
                    title: field,
                    dataIndex: field,
                    render: (v: unknown) => (v === null ? "空值" : String(v)),
                  }))}
                />
              ) : (
                <JsonView value={data.events} />
              )}
              {selected?.scope.resource.resource_type!=="api_resource" && <div className="section-gap">
                <Space>
                  <Button
                    disabled={history.length < 2 || loading}
                    onClick={() => {
                      if (!selected) return;
                      const previous = history[history.length - 2];
                      const remaining = history.slice(0, -1);
                      void load(selected, previous).then(() =>
                        setHistory(remaining),
                      );
                    }}
                  >
                    上一页
                  </Button>
                  <Select aria-label="数据每页记录数" value={recordPageSize} disabled={loading} options={pageSizeOptions.map(value=>({value,label:`${value} 条/页`}))} onChange={size=>{setRecordPageSize(size);setHistory([]);if(selected)void load(selected,undefined,false,size)}}/>
                  <Button
                    disabled={!data.next_cursor || loading}
                    onClick={() =>
                      selected &&
                      data.next_cursor &&
                      load(selected, data.next_cursor, true)
                    }
                  >
                    下一页
                  </Button>
                </Space>
              </div>}
              {(data.watermark||data.cutoff)&&<><h3>交付范围</h3><JsonView value={data.watermark ?? data.cutoff} /></>}
            </>
          )
        )}
      </PlatformDialog>
    </>
  );
}

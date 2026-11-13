import {usePagination} from "../hooks/usePagination";
import {governanceFieldTooltip} from "../components/GovernanceFieldHelp";
import {GovernanceGuide} from "../components/GovernanceGuide";
import {GovernanceDialog,GovernanceDialogActions} from "../components/GovernanceDialog";
import {GovernancePublicationApprovals} from "../components/GovernancePublicationApprovals";
import {GovernanceProvenanceDetails} from "../components/GovernanceProvenanceDetails";
import {
  App,
  Alert,
  Button,
  Checkbox,
  Col,
  Descriptions,

  Form,
  Input,
  Row,
  Select,
  Space,
  Table,
  Tag,
} from "antd";
import { useEffect, useState } from "react";
import type {
  Dataset,
  ApplicationMaterials,
  ResourceDirectory,
  Field,
  GrantRequest,
  Page,
  SourceObject,
} from "../api/types";
import { deliveryMethod, directoryResources, resourceLabel } from "../api/types";
import { query, errorMessage } from "../api/client";
import { useQuery } from "../hooks/useQuery";
import { useAction } from "../hooks/useAction";
import { useSession } from "../stores/session";
import { Failure, JsonView, Status, Title } from "../components/common";
import { navigate } from "../app/navigation";
export function ApplyModal({
  resource,
  onClose,
  onDone,
  returnLabel,
  assets = false,
}: {
  resource: GrantRequest["resource"] | null;
  fields: Field[];
  onClose: () => void;
  onDone?: () => void;
  returnLabel?: string;
  assets?: boolean;
}) {
  const user = useSession((s) => s.user)!;
  const { message } = App.useApp();
  const [form] = Form.useForm<ApplicationMaterials & {
    purpose: string;
    fields: string[];
    days: number;
  }>();
  const directory=useQuery<Page<ResourceDirectory>>(resource?(assets?"/api/v1/assets/directories?":"/api/v1/resource-directories?")+query({resource_id:resource.resource_id}):null);
 const mounted=directory.data?.items.flatMap(directoryResources).find(d=>d.available&&d.resource.resource_type===resource?.resource_type&&d.resource.resource_id===resource?.resource_id&&d.resource.contract_version===resource?.contract_version);
 const sharedFields=mounted?.fields??[];
 const action = useAction();
  async function submit(v: ApplicationMaterials & {
    purpose: string;
    fields: string[];
    days: number;
  }) {
    if (!resource || !mounted) return;
    try {
      await action.run("/api/v1/applications", {
        resource,
        department_id: user.department_id,
        purpose: v.purpose,
        materials:{legal_basis:v.legal_basis,scenario:v.scenario,scope:v.scope,method:deliveryMethod(resource),department_approval_ref:v.department_approval_ref},
        fields: deliveryMethod(resource)==="download" ? [] : v.fields,
        expires_at: new Date(Date.now() + v.days * 86400000).toISOString(),
      });
      onClose();
      form.resetFields();
      onDone?.();
      message.success("申请已提交，等待资源负责人审批");
    } catch (e) {
      message.error(errorMessage(e));
    }
  }
  return (
    resource&&<GovernanceDialog
      header="申请使用数据"
      busy={action.busy}
      onClose={() => {
        onClose();
        form.resetFields();
      }}
      closeLabel={returnLabel??"取消"}
      width={1100}
    >
      {directory.error?<Failure error={directory.error}/>:!directory.loading&&!mounted?<Alert type="info" showIcon title="资源尚未完成有效目录挂载，当前不能申请"/>:null}
      <Tag>
        {resource ? resourceLabel(resource) : "资源"} · V
        {resource?.contract_version}
      </Tag>
      {mounted?.governance&&<GovernanceProvenanceDetails governance={mounted.governance}/>}
      <Form id="dialog-catalog-0"
        form={form}
        layout="vertical"
        initialValues={{ days: 7, fields: [],method:"query" }}
        onFinish={submit}
        clearOnDestroy
      >
        <Form.Item
          name="purpose"
          label="使用用途"
          rules={[{ required: true, message: "请说明数据用途" }]}
        >
          <Input.TextArea placeholder="请说明数据用途及预期业务目标" maxLength={1000} rows={3} />
        </Form.Item>
        {resource?.resource_type !== "file_resource" && <Form.Item
          name="fields"
          label="申请字段"
          rules={[{ required: true, message: "至少选择一个字段" }]}
        >
          <Checkbox.Group
            className="data-application-fields"
            options={sharedFields.map((f) => ({
              value: f.name,
              label: f.name + (f.sensitive ? "（脱敏）" : ""),
            }))}
          />
        </Form.Item>}
        <Row gutter={24}>
        {[{name:"legal_basis",placeholder:"请填写数据使用的法律、政策或业务依据",label:"使用依据"},{name:"scenario",placeholder:"请说明使用场景及需要解决的业务问题",label:"使用场景"},{name:"scope",placeholder:"请说明使用对象、字段范围及业务范围",label:"使用范围"},{name:"department_approval_ref",placeholder:"请输入需求部门负责人同意记录的编号或引用",label:"需求部门负责人同意记录"}].map(f=><Col key={f.name} xs={24} md={12}><Form.Item name={f.name} label={f.label} rules={[{required:true,message:"请补充"+f.label}]}><Input.TextArea placeholder={f.placeholder} rows={2} maxLength={f.name==="department_approval_ref"?1000:2000}/></Form.Item></Col>)}
        <Col xs={24} md={12}>
        <Form.Item name="days" label="使用期限">
          <Select placeholder="请选择使用期限"
            options={[1, 7, 30, 90].map((d) => ({
              value: d,
              label: d + " 天",
            }))}
          />
        </Form.Item>
        </Col>
        </Row>
        <p className="helper-text">
          授权仅适用于当前账号与部门。原始数据和治理成果分别审批，敏感字段按策略脱敏。本期按资源和字段授权；需要按地区等限定行范围时，请申请对应的独立数据集。
        </p>
        <GovernanceDialogActions><Button form="dialog-catalog-0" type="primary" htmlType="submit" loading={action.busy} disabled={!mounted||directory.loading}>
          提交申请
        </Button></GovernanceDialogActions>
      </Form>
    </GovernanceDialog>
  );
}
export function Catalog({ publications = false }: { publications?: boolean }) {
  const [governanceApprovals,setGovernanceApprovals]=useState(false);
  const user = useSession((s) => s.user)!;
  const canProvide =
    (user.roles.includes("admin") || user.roles.includes("provider"));
  const { message } = App.useApp();
  const [page,setPage,pagePagination] = usePagination();
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<Dataset | null>(null);
  const [apply, setApply] = useState<Dataset | null>(null);
  const [open, setOpen] = useState(() => canProvide && new URLSearchParams(window.location.search).get("action") === "create");
  const [form] = Form.useForm<{
    name: string;
    description: string;
    source_object_id: string;
    fields: string[];
    usage_restrictions: string;
  }>();
  const objectID = Form.useWatch("source_object_id", form);
  const objects = useQuery<Page<SourceObject>>(
    canProvide ? "/api/v1/catalog/objects?page_size=100" : null,
  );
  const list = useQuery<Page<Dataset>>(
    "/api/v1/datasets?" + query({ page, search ,page_size:pagePagination.pageSize}),
  );
  const targetID = new URLSearchParams(window.location.search).get(
    "dataset_id",
  );
  const target = useQuery<Dataset>(
    targetID ? "/api/v1/datasets/" + encodeURIComponent(targetID) : null,
  );
  useEffect(() => {
    if (target.data) setSelected(target.data);
  }, [target.data]);
  const action = useAction();
  const currentObject = objects.data?.items.find(
    (o) => o.object_id === objectID,
  );
  async function create(v: {
    name: string;
    description: string;
    source_object_id: string;
    fields: string[];
    usage_restrictions: string;
  }) {
    const fields =
      currentObject?.fields.filter((f) => v.fields.includes(f.name)) ?? [];
    try {
      await action.run("/api/v1/datasets", {
        ...v,
        fields,
        owner_id: user.user_id,
      });
      setOpen(false);
      form.resetFields();
      list.reload();
      message.success("数据集已登记，请先运行质量与完整性检查");
    } catch (e) {
      message.error(errorMessage(e));
    }
  }
  async function run(r: Dataset, kind: string) {
    try {
      await action.run(`/api/v1/datasets/${r.dataset_id}/${kind}`,r.workspace_id?{workspace_id:r.workspace_id}:{});
      list.reload();
      setSelected(null);
      message.success(
        kind === "publish"
          ? "资产已发布，可发起共享编目；门户摘要需另经公开审核"
          : "资产已下架，既有授权的新查询也将被阻断",
      );
    } catch (e) {
      message.error(errorMessage(e));
    }
  }
  return (
    <>
      <Title
        title={publications ? "发布管理" : "数据目录"}
        description={
          publications
            ? "治理数据流自动登记成果；核验质量与完整性后发布，统一管理对外可用状态。"
            : "发现治理成果，查看契约并申请字段和期限范围。"
        }
        actions={
          <>
            <Button onClick={list.reload}>刷新</Button>
            {publications&&canProvide&&<><Button onClick={()=>{setSelected(null);form.resetFields();setOpen(true)}}>登记历史治理数据集</Button><Button onClick={()=>{setSelected(null);setOpen(false);setGovernanceApprovals(true)}}>治理成果发布审批</Button></>}
            {canProvide && (user.roles.includes("admin") || (user.roles.includes("governor") && user.platforms.includes("governance"))) && (
              <Button type="primary" onClick={() => navigate("/platform/governance/dataflows")}>
                治理开发
              </Button>
            )}
          </>
        }
      />
      {publications&&<GovernanceGuide topic="publication"/>}
      {governanceApprovals&&<GovernanceDialog header="治理成果发布审批" onClose={()=>{setGovernanceApprovals(false);list.reload()}}><GovernancePublicationApprovals/></GovernanceDialog>}
      <section className="business-panel" aria-label="数据集列表">
      <div className="table-toolbar">
        <Input.Search
          placeholder="搜索数据集名称"
          allowClear
          onSearch={(v) => {
            setSearch(v);
            setPage(1);
          }}
        />
      </div>
      {list.error ? (
        <Failure error={list.error} retry={list.reload} />
      ) : (
        <Table<Dataset>
          rowKey="dataset_id"
          scroll={{ x: 1000 }}
          dataSource={list.data?.items}
          loading={list.loading}
          pagination={{...pagePagination,total: list.data?.total}}
          columns={[
            {
              title: "数据集",
              render: (_, r) => (
                <><Button type="link" onClick={() => setSelected(r)}>
                  {r.definition.name}
                </Button><div className="helper-text">成果编号：{r.dataset_id}</div>{r.governance?.source_name&&<div className="helper-text">来源：{r.governance.source_name}</div>}</>
              ),
            },
            { title: "提供部门", dataIndex: "provider_department_id" },
            { title: "版本", dataIndex: "contract_version" },
            {
              title: "发布",
              dataIndex: "publication_status",
              render: (v: string) => <Status value={v} />,
            },
            {
              title: "质量",
              dataIndex: "quality_status",
              render: (v: string) => <Status value={v} />,
            },
            {
              title: "完整性",
              dataIndex: "completeness_status",
              render: (v: string) => <Status value={v} />,
            },
            {
              title: "操作",
              render: (_, r) => (
                <Space size={16} wrap>
                  {canProvide && (user.roles.includes("admin") || r.provider_department_id===user.department_id) && <Button onClick={()=>navigate("/platform/exchange/directory?view=manage&dataset_id="+encodeURIComponent(r.dataset_id))}>发起共享</Button>}
                  {r.publication_status === "published" && (
                    <Button type="default" onClick={() => setApply(r)}>申请使用</Button>
                  )}
                  {canProvide &&
                    (r.definition.owner_id === user.user_id ||
                      user.roles.includes("admin")) && (
                      <>
                        {r.publication_status !== "published" ? (
                          <Button type="default"
                            disabled={action.busy}
                            onClick={() => setSelected(r)}
                          >
                            发布
                          </Button>
                        ) : (
                          <Button type="default"
                            danger
                            disabled={action.busy}
                            onClick={() => setSelected(r)}
                          >
                            下架
                          </Button>
                        )}
                        <Button type="default"
                          onClick={() =>
                            navigate(`/platform/governance/results?dataset_id=${encodeURIComponent(r.dataset_id)}`)
                          }
                        >
                          质量核对
                        </Button>
                      </>
                    )}
                </Space>
              ),
            },
          ]}
        />
      )}
      </section>
      {open&&<GovernanceDialog
        header="登记治理数据集"
        busy={action.busy}
        onClose={() => {
          setOpen(false);
          form.resetFields();
        }}
        width={520}
      >
        <GovernanceGuide topic="legacy"/>
        <Form id="dialog-catalog-1" form={form} layout="vertical" onFinish={create}>
          <Form.Item name="name" label="资产名称" rules={[{ required: true }]}>
            <Input placeholder="请输入资产名称，例如：客户基础信息" maxLength={200} />
          </Form.Item>
          <Form.Item
            name="description"
            label="资产说明"
            rules={[{ required: true }]}
          >
            <Input.TextArea placeholder="请说明资产内容、来源及业务价值" maxLength={2000} />
          </Form.Item>
          <Form.Item
            name="source_object_id"
            label="来源对象" tooltip={governanceFieldTooltip("来源对象")}
            rules={[{ required: true }]}
          >
            <Select placeholder="请选择已接入的数据对象"
              options={objects.data?.items.map((o) => ({
                value: o.object_id,
                label: o.qualified_name,
              }))}
              onChange={(id) =>
                form.setFieldValue(
                  "fields",
                  objects.data?.items
                    .find((o) => o.object_id === id)
                    ?.fields.map((f) => f.name),
                )
              }
            />
          </Form.Item>
          <Form.Item
            name="fields"
            label="发布字段" tooltip={governanceFieldTooltip("发布字段")}
            rules={[{ required: true }]}
          >
            <Checkbox.Group
              options={currentObject?.fields.map((f) => ({
                value: f.name,
                label: f.name + (f.sensitive ? "（敏感）" : ""),
              }))}
            />
          </Form.Item>
          <Form.Item
            name="usage_restrictions"
            label="使用限制" tooltip={governanceFieldTooltip("使用限制")}
            rules={[{ required: true }]}
          >
            <Input.TextArea placeholder="请说明用途、转供及保存期限等限制" maxLength={2000} />
          </Form.Item>
          <GovernanceDialogActions><Button form="dialog-catalog-1" htmlType="submit" type="primary" loading={action.busy}>
            登记数据集
          </Button></GovernanceDialogActions>
        </Form>
      </GovernanceDialog>}
      {selected&&!apply&&<GovernanceDialog
        header={selected.definition.name}
        busy={action.busy}
        closeLabel="返回并刷新列表"
        onClose={() => {setSelected(null);list.reload()}}
        width={880}
      >
        {selected && (
          <>
            {canProvide&&(selected.definition.owner_id===user.user_id||user.roles.includes("admin"))&&<><GovernanceGuide topic="publication"/><Space size={16} wrap className="section-gap"><GovernanceDialogActions><Button type={selected.publication_status==="published"?"default":"primary"} danger={selected.publication_status==="published"} loading={action.busy} onClick={()=>run(selected,selected.publication_status==="published"?"withdraw":"publish")}>{selected.publication_status==="published"?"确认下架成果":"核验并发布"}</Button></GovernanceDialogActions></Space></>}
            <Descriptions
              column={1}
              items={[
                {key:"id",label:"成果编号",children:selected.dataset_id},
                {key:"publication",label:"发布状态",children:<Status value={selected.publication_status}/>},
                {key:"quality",label:"质量状态",children:<Status value={selected.quality_status}/>},
                {key:"completeness",label:"完整性状态",children:<Status value={selected.completeness_status}/>},
                {
                  key: "desc",
                  label: "说明",
                  children: selected.definition.description,
                },
                {
                  key: "dept",
                  label: "提供部门",
                  children: selected.provider_department_id,
                },
                {
                  key: "version",
                  label: "契约版本",
                  children: selected.contract_version,
                },
                {
                  key: "limit",
                  label: "使用限制",
                  children: selected.definition.usage_restrictions,
                },
              ]}
            />
            {selected.governance&&<div className="section-gap"><GovernanceProvenanceDetails governance={selected.governance}/></div>}
            <Table
              className="catalog-detail-fields"
              rowKey="name"
              dataSource={selected.definition.fields}
              pagination={false}
              columns={[
                { title: "字段", dataIndex: "name" },
                { title: "类型", dataIndex: "logical_type" },
                {
                  title: "敏感标记",
                  dataIndex: "sensitive",
                  render: (v: boolean) => (v ? "交付时脱敏" : "普通"),
                },
              ]}
            />
            <h3 className="catalog-detail-section-title">最近核对范围</h3>
            <JsonView value={selected.latest_watermark} />
            {selected.publication_status === "published" && (
              <GovernanceDialogActions><Button type="primary" onClick={() => setApply(selected)}>
                申请使用
              </Button></GovernanceDialogActions>
            )}
          </>
        )}
      </GovernanceDialog>}
      <ApplyModal
        returnLabel={selected?"返回资源详情":undefined}
        key={apply?.dataset_id ?? "none"}
        resource={
          apply
            ? {
                resource_type: "governed_dataset",
                resource_id: apply.dataset_id,
                contract_version: apply.contract_version,
              }
            : null
        }
        fields={apply?.definition.fields ?? []}
        onClose={() => setApply(null)}
      />
    </>
  );
}
export function RawResources() {
  const [page,setPage,pagePagination] = usePagination();
  const [apply, setApply] = useState<SourceObject | null>(null);
  const list = useQuery<Page<SourceObject>>(
    "/api/v1/resources/raw?" + query({ page ,page_size:pagePagination.pageSize}),
  );
  return (
    <>
      <Title
        title="原始数据申请"
        description="申请使用已采集、尚未经治理的原始数据；获批后通过数据使用授权访问。数据接入与同步请使用数据源管理、CDC 接入任务或 ETL 数据交换。"
        actions={<Button onClick={list.reload}>刷新</Button>}
      />
      {list.error ? (
        <Failure error={list.error} />
      ) : (
        <Table<SourceObject>
          rowKey="object_id"
          dataSource={list.data?.items}
          loading={list.loading}
          pagination={{...pagePagination,total: list.data?.total}}
          columns={[
            { title: "原始对象", dataIndex: "qualified_name" },
            { title: "结构版本", dataIndex: "schema_version" },
            { title: "数据层", render: () => <Tag color="gold">原始数据</Tag> },
            { title: "字段数", render: (_, r) => r.fields.length },
            {
              title: "操作",
              render: (_, r) => (
                <Button type="default" onClick={() => setApply(r)}>申请使用</Button>
              ),
            },
          ]}
        />
      )}
      <ApplyModal
        key={apply?.object_id ?? "none"}
        resource={
          apply
            ? {
                resource_type: "raw_object",
                resource_id: apply.object_id,
                contract_version: apply.schema_version,
              }
            : null
        }
        fields={apply?.fields ?? []}
        onClose={() => setApply(null)}
      />
    </>
  );
}

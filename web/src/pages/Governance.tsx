import {usePagination} from "../hooks/usePagination";
import {navigate} from "../app/navigation";
import {governanceFieldLabel} from "../api/governanceFieldLabels";
import {governanceFieldTooltip,GovernanceFieldHelp} from "../components/GovernanceFieldHelp";
import {GovernanceGuide} from "../components/GovernanceGuide";
import {GovernanceDialog,GovernanceDialogActions} from "../components/GovernanceDialog";
import {
  App,
  Alert,
  Button,
  Checkbox,
  Descriptions,

  Form,
  Input,
  InputNumber,
  Select,
  Space,
  Table,
  Tag,
} from "antd";
import { useState } from "react";
import {GovernedResultBindingEditor} from "../components/GovernedResultBindingEditor";
import type {
  Check,
  Dataset,
  Page,
  Quality,
  Rule,
  SourceObject,
} from "../api/types";
import { errorMessage, query } from "../api/client";
import { useQuery } from "../hooks/useQuery";
import { useAction } from "../hooks/useAction";
import { Failure, JsonView, Status, Title } from "../components/common";
interface RuleForm {
  name: string;
  object_id: string;
  checks: {
    field: string;
    kind: string;
    blocking: boolean;
    min?: number;
    max?: number;
    values?: string;
  }[];
}
const kinds = [
  ["required", "必填"],
  ["type", "类型"],
  ["length", "长度"],
  ["range", "范围"],
  ["enum", "枚举"],
  ["business_key", "业务主键"],
].map(([value, label]) => ({ value, label }));
export function Rules() {
  const { message } = App.useApp();
  const [page,setPage,pagePagination] = usePagination();
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<Rule | null>(null);
  const [publishing, setPublishing] = useState<Rule | null>(null);
  const [form] = Form.useForm<RuleForm>();
  const objectID = Form.useWatch("object_id", form);
  const objects = useQuery<Page<SourceObject>>(
    "/api/v1/metadata?page_size=100",
  );
  const list = useQuery<Page<Rule>>("/api/v1/rules?" + query({ page ,page_size:pagePagination.pageSize}));
  const action = useAction();
  const fields =
    objects.data?.items.find((o) => o.object_id === objectID)?.fields ?? [];
  function start(row?: Rule) {
    setEditing(row ?? null);
    form.setFieldsValue(
      row
        ? {
            name: row.definition.name,
            object_id: row.definition.object_id,
            checks: row.definition.checks.map((c) => ({
              field: c.field,
              kind: c.kind,
              blocking: c.blocking,
              min:
                typeof c.parameters.min === "number"
                  ? c.parameters.min
                  : undefined,
              max:
                typeof c.parameters.max === "number"
                  ? c.parameters.max
                  : undefined,
              values: Array.isArray(c.parameters.values)
                ? c.parameters.values.join(",")
                : undefined,
            })),
          }
        : {
            name: "",
            object_id: undefined,
            checks: [
              { field: "id", kind: "required", blocking: true },
              { field: "id", kind: "business_key", blocking: true },
            ],
          },
    );
    setOpen(true);
  }
  async function submit(v: RuleForm) {
    const checks: Check[] = v.checks.map((c) => {
      let parameters: Record<string, unknown> = {};
      if (c.kind === "range" || c.kind === "length") {
        if (c.min !== undefined) parameters.min = c.min;
        if (c.max !== undefined) parameters.max = c.max;
      } else if (c.kind === "enum")
        parameters = {
          values: (c.values ?? "")
            .split(",")
            .map((x) => x.trim())
            .filter(Boolean),
        };
      else if (c.kind === "type")
        parameters = {
          type: fields.find((f) => f.name === c.field)?.logical_type,
        };
      return { field: c.field, kind: c.kind, blocking: c.blocking, parameters };
    });
    try {
      await action.run(
        editing ? `/api/v1/rules/${editing.rule_id}/versions` : "/api/v1/rules",
        { name: v.name, object_id: v.object_id, schema_version: objects.data?.items.find(o=>o.object_id===v.object_id)?.schema_version, checks },
      );
      setOpen(false);
      form.resetFields();
      list.reload();
      message.success("规则版本已保存，发布后可用于新数据集");
    } catch (e) {
      message.error(errorMessage(e));
    }
  }
  async function publish(id: string) {
    try {
      await action.run(`/api/v1/rules/${id}/publish`);
      setPublishing(null);
      list.reload();
      message.success("规则已发布");
    } catch (e) {
      message.error(errorMessage(e));
    }
  }
  return (
    <>
      <Title
        title="历史来源质量规则"
        description="供历史成果登记引用。新数据治理请在数据流中配置检查；这里发布规则不会自动检查数据。"
        actions={
          <>
            <Button onClick={()=>navigate("/platform/governance/quality")}>返回质量管理</Button>
            <Button onClick={list.reload}>刷新</Button>
            <Button type="primary" onClick={() => start()}>
              新增规则
            </Button>
          </>
        }
      /><GovernanceGuide topic="rules"/>
      {list.error ? (
        <Failure error={list.error} />
      ) : (
        <Table<Rule>
          rowKey="rule_id"
          dataSource={list.data?.items}
          loading={list.loading}
          pagination={{...pagePagination,total: list.data?.total}}
          columns={[
            { title: "规则名称", render: (_, r) => r.definition.name },
            { title: "版本", dataIndex: "version" },
            { title: "检查项", render: (_, r) => r.definition.checks.length },
            {
              title: "状态",
              dataIndex: "state",
              render: (v: string) => <Status value={v} />,
            },
            {
              title: "操作",
              render: (_, r) => (
                <Space size={16}>
                  <Button type="default"
                    disabled={action.busy || r.state !== "draft"}
                    onClick={() => setPublishing(r)}
                  >
                    发布
                  </Button>
                  <Button type="default"
                    disabled={r.state !== "published"}
                    onClick={() => start(r)}
                  >
                    新增版本
                  </Button>
                </Space>
              ),
            },
          ]}
        />
      )}
      {publishing&&<GovernanceDialog guide="rules" busy={action.busy} onClose={()=>setPublishing(null)} header={<Title title="核对并发布质量规则" description="核对来源、字段及检查条件；发布后供新登记成果固定引用。" />} actions={<Button type="primary" loading={action.busy} onClick={()=>publish(publishing.rule_id)}>确认发布规则</Button>} width={900}>
        <Descriptions items={[{key:"name",label:"规则",children:publishing.definition.name},{key:"version",label:"规则版本",children:publishing.version},{key:"source",label:"来源对象",children:publishing.definition.object_id}]}/>
        <Table rowKey={(_,index)=>String(index)} dataSource={publishing.definition.checks} pagination={false} scroll={{x:650}} columns={[{title:"字段（中文 / 英文）",render:(_,f)=>governanceFieldLabel({name:f.field})},{title:"检查",render:(_,check)=>kinds.find(kind=>kind.value===check.kind)?.label??check.kind},{title:"阻断",render:(_,check)=>check.blocking?"是":"否"},{title:"条件",render:(_,check)=>JSON.stringify(check.parameters)}]}/>
      </GovernanceDialog>}
      {open&&<GovernanceDialog
        header={editing ? "创建规则新版本" : "新增质量规则"}
        busy={action.busy}
        onClose={() => {
          setOpen(false);
          form.resetFields();
        }}
        width={820}
      >
        <GovernanceGuide topic="rules"/>
        <Form id="dialog-governance-0" form={form} layout="vertical" onFinish={submit}>
          <Form.Item label="规则名称" name="name" rules={[{ required: true }]}>
            <Input placeholder="请输入规则名称，例如：客户信息完整性检查" maxLength={200} />
          </Form.Item>
          <Form.Item
            label="数据对象" tooltip={governanceFieldTooltip("数据对象")}
            name="object_id"
            rules={[{ required: true }]}
          >
            <Select placeholder="请选择需要检查的数据对象"
              disabled={Boolean(editing)}
              options={objects.data?.items.map((o) => ({
                value: o.object_id,
                label: o.qualified_name,
              }))}
            />
          </Form.Item>
          <Form.List name="checks">
            {(items, { add, remove }) => (
              <>
                {items.map((item) => (
                  <div className="rule-row" key={item.key}>
                    <Form.Item
                      name={[item.name, "field"]}
                      label="字段" tooltip={governanceFieldTooltip("字段")}
                      rules={[{ required: true }]}
                    >
                      <Select placeholder="请选择需要检查的字段"
                        options={fields.map((f) => ({
                          value: f.name,
                          label: governanceFieldLabel(f),
                        }))}
                      />
                    </Form.Item>
                    <Form.Item
                      name={[item.name, "kind"]}
                      label="检查类型" tooltip={governanceFieldTooltip("检查类型")}
                      rules={[{ required: true }]}
                    >
                      <Select placeholder="请选择字段检查类型" options={kinds} />
                    </Form.Item>
                    <Form.Item shouldUpdate noStyle>
                      {() => {
                        const kind = form.getFieldValue([
                          "checks",
                          item.name,
                          "kind",
                        ]);
                        return kind === "range" || kind === "length" ? (
                          <Space>
                            <Form.Item name={[item.name, "min"]} label="最小值" tooltip={governanceFieldTooltip("最小值")}>
                              <InputNumber placeholder="请输入允许的最小值" />
                            </Form.Item>
                            <Form.Item name={[item.name, "max"]} label="最大值" tooltip={governanceFieldTooltip("最大值")}>
                              <InputNumber placeholder="请输入允许的最大值" />
                            </Form.Item>
                          </Space>
                        ) : kind === "enum" ? (
                          <Form.Item
                            name={[item.name, "values"]}
                            label="允许值，用逗号分隔" tooltip={governanceFieldTooltip("允许值，用逗号分隔")}
                            rules={[{ required: true }]}
                          >
                            <Input placeholder="请输入允许值，以英文逗号分隔，例如：正常,停用" />
                          </Form.Item>
                        ) : (
                          <span className="helper-text">按字段契约检查</span>
                        );
                      }}
                    </Form.Item>
                    <Form.Item
                      name={[item.name, "blocking"]}
                      valuePropName="checked"
                    >
                      <Checkbox>阻断<GovernanceFieldHelp label="阻断"/></Checkbox>
                    </Form.Item>
                    <Button danger onClick={() => remove(item.name)}>
                      移除
                    </Button>
                  </div>
                ))}
                <Button
                  onClick={() =>
                    add({ field: "id", kind: "required", blocking: true })
                  }
                >
                  增加检查项
                </Button>
              </>
            )}
          </Form.List>
          <div className="section-gap">
            <GovernanceDialogActions><Button form="dialog-governance-0" htmlType="submit" type="primary" loading={action.busy}>
              保存版本
            </Button></GovernanceDialogActions>
          </div>
        </Form>
      </GovernanceDialog>}
    </>
  );
}
export function QualityPage() {
 const [bindingDataset,setBindingDataset]=useState<Dataset|null>(null);
  const { message } = App.useApp();
  const [page,setPage,pagePagination] = usePagination();
  const [selected, setSelected] = useState<string | null>(null);
  const [lineage, setLineage] = useState(false);
  const list = useQuery<Page<Dataset>>(
    "/api/v1/governance/datasets?" + query({ page ,page_size:pagePagination.pageSize}),
  );
  const detail = useQuery<Quality>(
    selected ? `/api/v1/datasets/${selected}/quality` : null,
  );
  const trace = useQuery<Record<string, unknown>>(
    selected && lineage ? `/api/v1/datasets/${selected}/lineage` : null,
  );
  const action = useAction();
  async function check(id: string) {
    try {
      await action.run(`/api/v1/datasets/${id}/check`);
      setSelected(id);
      setLineage(false);
      list.reload();
      detail.reload();
      message.success("质量与完整性检查已完成，请查看门禁结果");
    } catch (e) {
      message.error(errorMessage(e));
    }
  }
  return (
    <>
      <Title
        title="质量检查"
        description="在真实结果上评估规则，核对来源、处理结果和原始归档范围。"
        actions={<Button onClick={list.reload}>刷新</Button>}
      /><GovernanceGuide topic="quality"/>
      {list.error ? (
        <Failure error={list.error} />
      ) : (
        <Table<Dataset>
          rowKey="dataset_id"
          dataSource={list.data?.items}
          loading={list.loading}
          pagination={{...pagePagination,total: list.data?.total}}
          columns={[
            { title: "数据集", render: (_, r) => <><span>{r.definition.name}</span><div className="helper-text">成果编号：{r.dataset_id}</div></> },
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
                  {!r.governance_run_id&&<Button disabled={r.publication_status==="published"} onClick={()=>{setSelected(null);setBindingDataset(r)}}>绑定历史结果库表</Button>}
                  <Button type="default"
                    loading={action.busy}
                    onClick={() => {setSelected(r.dataset_id);setLineage(false)}}
                  >
                    运行检查
                  </Button>
                  <Button type="default"
                    onClick={() => {
                      setSelected(r.dataset_id);
                      setLineage(false);
                    }}
                  >
                    检查结果
                  </Button>
                  <Button type="default"
                    onClick={() => {
                      setSelected(r.dataset_id);
                      setLineage(true);
                    }}
                  >
                    血缘追溯
                  </Button>
                </Space>
              ),
            },
          ]}
        />
      )}
      {bindingDataset&&<GovernedResultBindingEditor key={bindingDataset.dataset_id} dataset={bindingDataset} onClose={()=>setBindingDataset(null)} onQueued={list.reload}/>}
 {selected&&<GovernanceDialog
        header={lineage ? "表级血缘" : "质量检查结果"}
        busy={action.busy}
        onClose={() => setSelected(null)}
        width={880}
      >
        <GovernanceGuide topic={lineage?"lineage":"quality"}/>
        {!lineage&&<Space size={16} className="section-gap"><GovernanceDialogActions><Button type="primary" loading={action.busy} onClick={()=>selected&&check(selected)}>运行检查</Button></GovernanceDialogActions><GovernanceDialogActions><Button disabled={action.busy||action.readOnly} onClick={detail.reload}>刷新检查结果</Button></GovernanceDialogActions></Space>}
        {lineage ? (
          trace.error ? (
            <Failure error={trace.error} />
          ) : (
            <JsonView value={trace.data} />
          )
        ) : detail.error ? (
          <Failure error={detail.error} />
        ) : (
          detail.data && (
            <>
              <Descriptions
                items={[
                  {
                    key: "status",
                    label: "门禁",
                    children: <Status value={detail.data.status} />,
                  },
                  {
                    key: "rule",
                    label: list.data?.items.find(row=>row.dataset_id===selected)?.governance_run_id ? "输出模型版本" : "质量规则版本",
                    children: detail.data.rule_version,
                  },
                  {
                    key: "pass",
                    label: "通过行数",
                    children: detail.data.passed_count,
                  },
                  {
                    key: "fail",
                    label: "阻断行数",
                    children: detail.data.failed_count,
                  },
                ]}
              />
              {detail.data.status === "blocked" && (
                <Alert
                  type="warning"
                  title="存在阻断项，数据集不能发布或交付"
                />
              )}
              <Table
                rowKey="code"
                dataSource={detail.data.failure_reasons}
                pagination={false}
                columns={[
                  { title: "原因", dataIndex: "code" },
                  { title: "数量", dataIndex: "count" },
                ]}
              />
              <h3>已核对范围</h3>
              <JsonView value={detail.data.checked_range} />
            </>
          )
        )}
      </GovernanceDialog>}
    </>
  );
}

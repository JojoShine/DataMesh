import {sourceSupportsCDC} from "../api/sourceTypes";
import {usePagination} from "../hooks/usePagination";
import {PlatformDialog,DialogActions} from "../components/PlatformDialog";
import {
  App,
  Alert,
  Button,
  Descriptions,

  Form,
  Input,
  Select,
  Space,
  Table,
  Typography,
} from "antd";
import { useState } from "react";
import { SourceCheckReport } from "../components/SourceCheckReport";
import { navigate } from "../app/navigation";
import type { Page, Preflight, Source, SourceObject, Task } from "../api/types";
import { errorMessage, query } from "../api/client";
import { useQuery } from "../hooks/useQuery";
import { useAction } from "../hooks/useAction";
import { Failure, JsonView, Status, Title } from "../components/common";
type CDCFormValues={source_id:string;table_key:string;publication?:string;pdb?:string};
type SourceTable = {schema:string;name:string;kind:string};
export function Tasks() {
  const { message } = App.useApp();
  const [page,setPage,pagePagination] = usePagination();
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [source, setSource] = useState<string>();
 const [tablePDB,setTablePDB]=useState("");
  const [cdcReport,setCdcReport] = useState<Preflight|null>(null);
  const [form] = Form.useForm<CDCFormValues>();
  const list = useQuery<Page<Task>>("/api/v1/tasks?" + query({ page ,page_size:pagePagination.pageSize}));
  const sources = useQuery<Page<Source>>("/api/v1/sources?page_size=100");
  const objects = useQuery<Page<SourceObject>>(
    source ? `/api/v1/sources/${source}/objects?page_size=100` : null,
  );
  const sourceRecord = sources.data?.items.find(s=>s.source_id===source);
  const pdb=Form.useWatch("pdb",form);
  const tablePath=source&&(sourceRecord?.kind!=="oracle"||tablePDB)?`/api/v1/sources/${source}/tables`+(sourceRecord?.kind==="oracle"?"?"+query({pdb:tablePDB}):""):null;
  const tables = useQuery<{items:SourceTable[]}>(tablePath);
  const tableKey = Form.useWatch("table_key",form);
  const chosenTable = tables.data?.items.find(t=>JSON.stringify([t.schema,t.name])===tableKey);
  const chosenObject = chosenTable && objects.data?.items.find(o=>o.source_id===source&&o.qualified_name===`${chosenTable.schema}.${chosenTable.name}`);
  const legacy = Boolean(sourceRecord?.capture_template&&chosenObject);
 const publication=Form.useWatch("publication",form);
 const draftReady=Boolean(chosenTable&&chosenTable.kind!=="VIEW"&&sourceRecord?.source_version&&!tables.loading&&!tables.error&&(sourceRecord.kind!=="postgres"||publication?.trim())&&(sourceRecord.kind!=="oracle"||pdb?.trim()));
 const cdcReady = Boolean(chosenObject&&chosenTable?.kind!=="VIEW"&&sourceRecord?.cdc_status==="passed"&&!sources.loading&&!sources.error&&!tables.loading&&!tables.error&&!objects.loading&&!objects.error);
  function closeForm(){setOpen(false);form.resetFields();setSource(undefined);setTablePDB("")}
  function refreshTables(){if(sourceRecord?.kind==="oracle"){if(!pdb?.trim()){message.warning("请先填写目标 PDB");return}setTablePDB(pdb.trim())}form.setFieldValue("table_key",undefined);tables.reload();objects.reload();sources.reload()}
  const detail = useQuery<Task>(selected ? "/api/v1/tasks/" + selected : null);
  const action = useAction();
  async function inspectCDC(){
    if(!source)return;
    try {
     if(!legacy&&!draftReady){await form.validateFields();return}
     const path=legacy?`/api/v1/sources/${source}/preflight-cdc`:"/api/v1/cdc-tasks/validate";
     const payload=legacy?(chosenTable?{schema:chosenTable.schema,table:chosenTable.name}:{}):draftInput();
     const report=await action.run<Preflight>(path,payload);setCdcReport(report);sources.reload();objects.reload()}catch(error){message.error(errorMessage(error))}
  }
  async function run(id: string, kind: string) {
    try {
      await action.run(`/api/v1/tasks/${id}/${kind}`);
      list.reload();
      detail.reload();
      message.success(
        kind === "archive" ? "原始范围与归档清单已核实" : "操作已核实",
      );
    } catch (e) {
      message.error(errorMessage(e));
      list.reload();
    }
  }
  function draftInput(){return {source_id:source,source_version:sourceRecord?.source_version,schema:chosenTable?.schema,table:chosenTable?.name,...(sourceRecord?.kind==="postgres"?{publication:publication?.trim()}:{}),...(sourceRecord?.kind==="oracle"?{pdb:pdb?.trim()}:{})}}
  async function create(value: { source_id: string; table_key: string }) {
    if(legacy?!cdcReady:!draftReady)return;
    try {
      if(!legacy){await action.run("/api/v1/cdc-tasks",draftInput());message.success("采集草稿已保存，可在任务列表检查条件并启动")}
      else if(chosenObject)await action.run("/api/v1/tasks", {
        source_id:value.source_id,
        object_id:chosenObject.object_id,
        mode: "cdc",
        dataset_id: null,
        schema_version: chosenObject.schema_version,
        rule_version: null,
      });
      closeForm();
      list.reload();
    } catch (e) {
      message.error(errorMessage(e));
    }
  }
  return (
    <>
      <Title
        title="CDC 接入任务"
        description="持续增量采集与状态核对。需要选择目标表、转换规则并手动执行时，请进入「ETL 数据交换」。"
        actions={
          <>
            <Button
              onClick={() => {
                list.reload();
                detail.reload();
              }}
            >
              刷新状态
            </Button>
            <Button type="primary" onClick={() => setOpen(true)}>
              建立任务
            </Button>
          </>
        }
      />
      {action.operation && (
        <Alert
          type="info"
          title={
            <Space>
              操作编号 {action.operation.operation_id}
              <Status value={action.operation.status} />
            </Space>
          }
        />
      )}
      <div className="section-gap">
        {list.error ? (
          <Failure error={list.error} retry={list.reload} />
        ) : (
          <Table<Task>
            rowKey="task_id"
            loading={list.loading}
            dataSource={list.data?.items}
            pagination={{...pagePagination,total: list.data?.total}}
            columns={[
              {
                title: "任务",
                render: (_, r) => (
                  <Button type="link" onClick={() => setSelected(r.task_id)}>
                    {sources.data?.items.find(
                      (s) => s.source_id === r.source_id,
                    )?.name ?? r.task_id.slice(0, 8)}
                  </Button>
                ),
              },
              { title: "配置版本", dataIndex: "config_version" },
              {
                title: "采集状态",
                dataIndex: "state",
                render: (v: string) => <Status value={v} />,
              },
              {
                title: "处理",
                render: (_, r) => <Status value={r.processing.status} />,
              },
              {
                title: "结果写入",
                render: (_, r) => <Status value={r.result.status} />,
              },
              {
                title: "原始归档",
                render: (_, r) => <Status value={r.archive.status} />,
              },
              {
                title: "操作",
                render: (_, r) => (
                  <Space size={16} wrap>
                    <Button type="default"
                      disabled={action.busy}
                      onClick={() =>
                        run(
                          r.task_id,
                          r.state === "paused" ? "resume" : "start",
                        )
                      }
                    >
                      {r.state === "paused" ? "恢复" : "启动/核对"}
                    </Button>
                    <Button type="default"
                      disabled={action.busy || r.state === "paused"}
                      onClick={() => run(r.task_id, "pause")}
                    >
                      暂停采集
                    </Button>
                    <Button type="default"
                      disabled={action.busy}
                      onClick={() => run(r.task_id, "reconcile")}
                    >
                      核对状态
                    </Button>
                    <Button type="default"
                      disabled={action.busy}
                      onClick={() => run(r.task_id, "archive")}
                    >
                      归档当前范围
                    </Button>
                  </Space>
                ),
              },
            ]}
          />
        )}
      </div>
      <PlatformDialog centered
        title="建立接入任务"
        width="min(720px, calc(100vw - 32px))"
        open={open&&!cdcReport}
        onCancel={action.busy?undefined:closeForm}
       
       
        styles={{body:{overflowY:"auto"}}}
        footer={null}
      >
        <Form<CDCFormValues> id="dialog-tasks-0" form={form} layout="vertical" onFinish={create} disabled={action.busy} onValuesChange={(changed:Partial<CDCFormValues>)=>{if("pdb" in changed){setTablePDB("");form.setFieldValue("table_key",undefined)}}}>
          {Boolean(sources.error)&&<Failure error={sources.error} retry={sources.reload}/>}
          <Form.Item
            name="source_id"
            label="数据源"
            rules={[{ required: true }]}
          >
            <Select showSearch optionFilterProp="label" loading={sources.loading} placeholder="请选择已登记的数据源"
              options={sources.data?.items.filter(s=>sourceSupportsCDC(s.kind)).map((s) => ({
                value: s.source_id,
                label: s.name,
              }))}
              onChange={(v) => {
                setSource(v);setTablePDB("");
                form.setFieldValue("table_key", undefined);
                form.setFieldValue("publication", undefined);form.setFieldValue("pdb",undefined);
              }}
            />
          </Form.Item>
          {sourceRecord?.kind==="oracle"&&<Form.Item name="pdb" label="目标 PDB" rules={[{required:true,whitespace:true,message:"请填写采集表所在 PDB"}]} extra="数据源需登记 CDB 服务，可信范围同时允许 CDB 与 PDB。填写目标 PDB 后点击刷新库表。"><Input placeholder="请输入目标 PDB 名称" maxLength={128}/></Form.Item>}
          <Form.Item
            name="table_key"
            label="采集对象"
            rules={[{ required: true }]}
          >
            <Select showSearch optionFilterProp="label" placeholder={source?"请选择数据库中的库表":"请先选择数据源，再选择采集对象"}
              disabled={!source||tables.loading||Boolean(tables.error)||(sourceRecord?.kind==="oracle"&&!tablePDB)} loading={tables.loading}
              notFoundContent={source?"没有可见库表，请刷新或检查账号权限":"请先选择数据源"}
              options={tables.data?.items.map(t=>({value:JSON.stringify([t.schema,t.name]),label:`${t.schema}.${t.name}${t.kind==="VIEW"?"（视图）":""}`}))}
            />
          </Form.Item>
          {chosenTable&&!legacy&&sourceRecord?.kind==="postgres"&&<Form.Item name="publication" label="发布名称" rules={[{required:true,whitespace:true,message:"请填写 DBA 为所选表配置的发布名称"}]} extra="由 DBA 配置包含所选表的发布；平台不会自动扩大发布范围。"><Input placeholder="请输入 PostgreSQL publication 名称" maxLength={63}/></Form.Item>}
          <Space orientation="vertical" size={16} style={{width:"100%"}}>
            <Space wrap size={16}><Button disabled={!source||action.busy} loading={tables.loading} onClick={refreshTables}>刷新库表</Button>{source&&tables.data&&<span className="helper-text">读取到 {tables.data.items.length} 个库表（最多显示 1000 个）</span>}</Space>
            {Boolean(tables.error)&&<Failure error={tables.error} retry={refreshTables}/>}
            {Boolean(objects.error)&&<Failure error={objects.error} retry={objects.reload}/>}
            {source&&tables.data?.items.length===0&&!tables.loading&&!tables.error&&<Alert type="info" showIcon title="未发现可见库表" description="请核对数据库名称及当前账号的库表访问权限，建表后可点击刷新库表。"/>}
            {chosenTable&&!legacy&&<Alert type="info" showIcon title={<Typography.Text strong>新建独立采集作业</Typography.Text>} description={<Space orientation="vertical" size={16}><Typography.Text>保存草稿后，在任务列表启动采集。启动前会检查日志、账号权限和库表配置；首次快照完成后持续采集增量。</Typography.Text>{sourceRecord?.kind==="oracle"&&<p className="helper-text">启动会在采集账号默认表空间创建本任务独立的 DMF_ 前缀 LogMiner 工作表；请 DBA 配置 CREATE TABLE 与适当的表空间额度。保存草稿不会修改数据库。</p>}{sourceRecord?.kind==="postgres"&&<p className="helper-text">首次启动会创建本任务独立复制槽；暂停保留槽与位点。发布范围须由 DBA 预先配置。</p>}<Button onClick={()=>navigate("/platform/exchange/etl")}>前往 ETL 数据交换</Button></Space>}/>}
            <DialogActions><Button disabled={!source} loading={action.busy} onClick={()=>void inspectCDC()}>检查 CDC 条件</Button></DialogActions>
            <p className="helper-text">{legacy?"绑定已验证的 CDC 作业并保留已有位点。":"每个任务使用独立连接器、主题和位点。保存草稿不会启动采集；暂停和恢复保留已有位点。"}</p>
            <DialogActions><Button form="dialog-tasks-0" htmlType="submit" type="primary" loading={action.busy} disabled={legacy?!cdcReady:!draftReady}>{legacy?"建立任务":"保存采集草稿"}</Button></DialogActions>
          </Space>
        </Form>
      </PlatformDialog>
      <SourceCheckReport closeLabel="返回接入任务" report={cdcReport} sourceName={sourceRecord?.name} busy={action.busy} onClose={()=>setCdcReport(null)} onRetry={()=>void inspectCDC()}/>
      <PlatformDialog footer={null} centered styles={{body:{overflowY:"auto"}}}
        title="任务运行详情"
        open={Boolean(selected)}
        onCancel={() => setSelected(null)}
        width={880}
      >
        {detail.error ? (
          <Failure error={detail.error} retry={detail.reload} />
        ) : (
          detail.data && (
            <>
              <Descriptions
                column={1}
                items={[
                  {
                    key: "task",
                    label: "任务编号",
                    children: detail.data.task_id,
                  },
                  {
                    key: "run",
                    label: "运行编号",
                    children: detail.data.run_id,
                  },
                  {
                    key: "state",
                    label: "采集状态",
                    children: <Status value={detail.data.state} />,
                  },
                ]}
              />
              {detail.data.capture_binding&&<Descriptions column={1} items={[{key:"table",label:"采集表",children:detail.data.capture_binding.schema+"."+detail.data.capture_binding.table},{key:"connector",label:"采集作业",children:detail.data.capture_binding.connector_name},{key:"snapshot",label:"首次快照",children:detail.data.snapshot?.state==="complete"?"已完成核对":detail.data.snapshot?.state==="manual_required"?"需要核查":"等待完整快照"}]}/>}
              {detail.data.capture_binding?.kind==="oracle"&&<p className="helper-text">本任务 Oracle 采集工作表：DMF_{detail.data.task_id.slice(0,24)}（Oracle 常规标识符按大写显示）。用于 LogMiner 刷新，不属于共享业务表。</p>}{detail.data.capture_binding?.slot&&<p className="helper-text">本任务 PostgreSQL 复制槽：{detail.data.capture_binding.slot}。暂停保留位点，请持续关注槽的日志保留量。</p>}
 {detail.data.capture_binding&&<Alert type="info" showIcon title="采集与治理分别核对" description="采集运行不代表已生成治理结果。原始快照、归档与治理结果将分别显示状态。"/>}
              {(["ingestion", "processing", "result", "archive"] as const).map(
                (key) => (
                  <section className="detail-section" key={key}>
                    <h3>
                      {
                        {
                          ingestion: "原始接入",
                          processing: "治理处理",
                          result: "结果写入",
                          archive: "原始归档",
                        }[key]
                      }{" "}
                      <Status value={detail.data![key].status} />
                    </h3>
                    {detail.data![key].error_code && (
                      <Alert
                        type="warning"
                        title={detail.data![key].error_code}
                      />
                    )}
                    <JsonView value={detail.data![key].position} />
                  </section>
                ),
              )}
            </>
          )
        )}
      </PlatformDialog>
    </>
  );
}

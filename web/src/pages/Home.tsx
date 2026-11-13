import {
  Button,
  Card,
  Input,
  Pagination,
  Select,
  Skeleton,
  Tag,
  Tooltip,
} from "antd";
import { useState } from "react";
import { ArrowRightOutlined, RightOutlined, DatabaseFilled, InfoCircleOutlined } from "@ant-design/icons";
import { query } from "../api/client";
import type { Asset, AssetPage, Platform } from "../api/types";
import { useQuery } from "../hooks/useQuery";
import { useSession } from "../stores/session";
import { navigate } from "../app/navigation";
import { EmptyData, Failure } from "../components/common";
import { PublicAssetDetail } from "../components/PublicAssetDetail";
import { ModuleIcon } from "../components/ModuleIcon";
export const modules: {
  id: Platform;
  name: string;
  description: string;
  summary: string;
  glyph: string;
  entry: string;
}[] = [
  {
    id: "exchange",
    name: "共享交换",
    description: "连接原始数据，支持治理成果共享。",
    summary: "连接原始数据",
    glyph: "⇄",
    entry: "sources",
  },
  {
    id: "governance",
    name: "数据治理",
    description: "统一标准与质量，追踪数据来源。",
    summary: "统一标准与质量",
    glyph: "◇",
    entry: "overview",
  },
  {
    id: "assets",
    name: "数据资产",
    description: "发现数据资产，按需申请使用。",
    summary: "发现并申请数据",
    glyph: "▦",
    entry: "catalog",
  },
  {
    id: "security",
    name: "数据安全",
    description: "管理权限与审计，保障数据使用。",
    summary: "保障数据使用",
    glyph: "◈",
    entry: "audit",
  },
];
export function enterModule(id: Platform, entry?: string) {
  const user = useSession.getState().user;
  if (!user) {
    navigate("/login");
    return;
  }
  navigate(`/platform/${id}/${entry ?? ""}`);
}
const assetPageSize = 9;
export function Home() {
  const publicTest=useSession(state=>Boolean(state.user?.public_test));
  const [search, setSearch] = useState("");
  const [department, setDepartment] = useState("");
  const [page,setPage] = useState(1);
  const [selected, setSelected] = useState<Asset | null>(null);
  const result = useQuery<AssetPage>(
    "/api/public/assets?" +
      query({ search, department_id: department, page, page_size:assetPageSize }),
  );
  return (
    <main className="portal">
      <section className="portal-intro">
        {publicTest&&<Tooltip trigger={["hover","focus","click"]} title="可进入四个平台查看完整菜单和业务页面。治理可在独立体验空间编辑、保存并运行示例数据；其他平台只读，不修改正式业务或连接真实数据源。"><Button className="portal-experience-notice" icon={<InfoCircleOutlined aria-hidden="true"/>}>安全体验模式</Button></Tooltip>}
        <div className="hero-copy"><h1>汇聚数据之力<br />连接无限可能</h1><p>从连接到治理，从资产到共享，<br />让每一份数据成为业务向前的力量。</p></div>
        <span className="hero-caption">DATA<br />FOR A MORE<br />CONNECTED WORLD</span>
      </section>
      <section aria-label="四个平台入口" className="module-grid">
        {modules.map(m => <button className={`module-card ${m.id}`} key={m.id} onClick={() => enterModule(m.id)}>
          <ModuleIcon platform={m.id} illustrated />
          <div className="module-copy"><h2>{m.name}</h2><p>{m.summary}</p></div><RightOutlined className="module-arrow" />
        </button>)}
      </section>
      <section className="asset-section">
        <div className="asset-section-top">
        <div className="asset-heading">
          <div>
            <h2>数据资产</h2>
            <p>发现已发布的数据，按需申请使用。</p>
          </div>
          {result.data && search && (
            <span className="asset-total">
              共 {result.data.total} 项已发布资产
            </span>
          )}
        </div>
        <div className="asset-filters">
          <Input.Search
           
            aria-label="搜索数据资产"
            placeholder="搜索资产名称或说明"
            allowClear
            onSearch={(v) => {
              setSearch(v);
              setPage(1);
            }}
            enterButton="搜索"
          />
          <Select
           
            aria-label="按提供部门筛选"
            value={department || undefined}
            placeholder="全部提供部门"
            allowClear
            options={result.data?.departments.map((d) => ({
              value: d,
              label: d,
            }))}
            onChange={(v: string | undefined) => {
              setDepartment(v ?? "");
              setPage(1);
            }}
          />
        </div>
        </div>
        {result.error ? (
          <Failure error={result.error} retry={result.reload} />
        ) : result.loading ? (
          <div className="asset-grid">
            {[1, 2, 3].map((key) => (
              <Card key={key}>
                <Skeleton active />
              </Card>
            ))}
          </div>
        ) : result.data?.items.length ? (
          <>
            <div className="asset-grid">
              {result.data.items.map((asset) => (
                <Card
                  className="asset-card"
                  key={asset.dataset_id}
                  hoverable
                  onClick={() => setSelected(asset)}
                >
                  <div className="asset-card-top">
                    <span className="asset-symbol"><DatabaseFilled /></span>
                    <div><div className="asset-title-line"><h3>{asset.name}</h3><Tag className="asset-type">治理成果</Tag></div><p className="asset-description">{asset.description}</p></div>
                  </div>
                  <div className="asset-meta">
                    <div><span>提供部门</span><strong>{asset.provider_department_id}</strong></div>
                    <div><span>字段数量</span><strong>{asset.field_count} 个字段</strong></div>
                  </div>
                  <div className="asset-card-footer"><span className="asset-updated">更新于 {new Date(asset.updated_at).toLocaleDateString("zh-CN").replaceAll("/", "-")}</span><Button type="default" onClick={event => { event.stopPropagation(); setSelected(asset); }}>查看详情 <ArrowRightOutlined /></Button></div>
                </Card>
              ))}
            </div>
            <Pagination current={page} pageSize={assetPageSize} showSizeChanger={false} onChange={setPage} total={result.data.total}/>
          </>
        ) : (
          <EmptyData description="暂无已发布资产。提供方完成治理核对并发布后，资产将在这里展示。" />
        )}
      </section>
      {selected && <PublicAssetDetail key={selected.dataset_id} asset={selected} onClose={() => setSelected(null)} />}
    </main>
  );
}

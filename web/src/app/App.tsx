import {FloatingAgent} from "../components/FloatingAgent";
import {SAMPLE_SPACE} from "../api/experience/seed";
import {GovernanceCapabilities} from "../pages/GovernanceCapabilities";
import {GovernanceReleases} from "../pages/GovernanceReleases";
import {GovernanceStandardCoverage} from "../pages/GovernanceStandardCoverage";
import {GovernanceMonitoring} from "../pages/GovernanceMonitoring";
import {SecurityResourceLabels} from "../pages/SecurityResourceLabels";
import {SecurityPreflight} from "../pages/SecurityPreflight";
import {SecurityOverview} from "../pages/SecurityOverview";
import {SecurityControls} from "../pages/SecurityControls";
import {SecurityScans,SecurityIncidents,SecurityProofs} from "../pages/SecurityOperations";
import {AssetProducts} from "../pages/AssetProducts";
import {AssetUsage} from "../pages/AssetUsage";
import {AssetSaved,AssetNotices} from "../pages/AssetFollowing";
import {DataServices} from "../pages/DataServices";
import {GovernanceLightweight} from "../pages/GovernanceLightweight";
import {GovernanceWorkspaces,GovernanceWorkspaceSources} from "../pages/GovernanceWorkspaces";
import {GovernanceWorkspaceProvider,GovernanceWorkspaceBar,useGovernanceWorkspace} from "../components/GovernanceWorkspaceContext";
import {GovernanceQualityWorkspace} from "../pages/GovernanceQualityWorkspace";
import {
  App as AntApp,
  Alert,
  Button,
  ConfigProvider,
  Popconfirm,
  Dropdown,
  Layout,
  Menu,
  Result,
  Space,
  Spin,
  Tag,
  Tooltip,
} from "antd";
import zhCN from "antd/locale/zh_CN";
import { lazy, Suspense, useEffect, useState, type ReactNode } from "react";
import { StarOutlined, ApiOutlined, FileDoneOutlined, SolutionOutlined, ToolOutlined, UnlockOutlined, ArrowLeftOutlined, MenuFoldOutlined, MenuUnfoldOutlined, DatabaseOutlined, FileTextOutlined, SwapOutlined, KeyOutlined, AuditOutlined, TeamOutlined, SafetyCertificateOutlined, ApartmentOutlined, CheckCircleOutlined, BookOutlined, DownOutlined, UserOutlined, AppstoreOutlined, BellOutlined, HistoryOutlined, ProfileOutlined, LineChartOutlined, HomeOutlined, LogoutOutlined } from "@ant-design/icons";
import { Brand } from "../components/Brand";
import { appTheme } from "./theme";
import { request, errorMessage } from "../api/client";
import type { Platform, Principal } from "../api/types";
import { useSession } from "../stores/session";
import { usePath, navigate } from "./navigation";
import { Home, modules } from "../pages/Home";
import { Login } from "../pages/Login";
import { Sources } from "../pages/Sources";
import { ETL } from "../pages/ETL";
import { Tasks } from "../pages/Tasks";
import {GovernanceCatalog} from "../pages/GovernanceCatalog";
import {MetadataWorkspace} from "../pages/GovernanceMetadata";
import {GovernanceWorkflows,GovernanceSchedules} from "../pages/GovernanceWorkflows";
import {GovernanceOverview,GovernanceOperations} from "../pages/GovernanceOperations";
import {GovernanceAlerts} from "../pages/GovernanceAlerts";
import {GovernanceLifecycle} from "../pages/GovernanceLifecycle";
import {GovernanceLineage} from "../pages/GovernanceLineage";
import {GovernanceDataflows} from "../pages/GovernanceDataflows";
import {GovernanceResults} from "../pages/GovernanceResults";
import { Rules } from "../pages/Governance";
import {Assets} from "../pages/Assets";
import {AssetOverview,AssetCategories,AssetFeedbackWorkspace} from "../pages/AssetWorkspace";
import { Catalog } from "../pages/Catalog";
import { Applications, Grants } from "../pages/Exchange";
import {OrganizationManagement} from "../pages/OrganizationManagement";
import {DirectoryCoding} from "../pages/DirectoryCoding";
import { Accounts, AuditPage } from "../pages/Security";
import { Failure } from "../components/common";
import { ModuleIcon } from "../components/ModuleIcon";
import { ResourcePermissions } from "../pages/ResourcePermissions";
import { DirectoryCenter } from "../pages/DirectoryCenter";
import { DirectoryReviews } from "../pages/DirectoryReviews";
const OperationGuides = lazy(() => import("../pages/OperationGuides"));
const menuIcons: Record<string, ReactNode> = {
 "data-services":<ApiOutlined/>,
  masters:<TeamOutlined/>,metrics:<LineChartOutlined/>,scenarios:<ApartmentOutlined/>,releases:<FileDoneOutlined/>,monitoring:<BellOutlined/>,"standard-coverage":<CheckCircleOutlined/>,
  workspaces:<AppstoreOutlined/>,"space-sources":<DatabaseOutlined/>,alerts:<BellOutlined/>,lifecycle:<HistoryOutlined/>,"quality-templates":<ProfileOutlined/>,"quality-trends":<LineChartOutlined/>,
  lightweight:<ToolOutlined/>,overview:<DatabaseOutlined/>,workflows:<ApartmentOutlined/>,issues:<AuditOutlined/>,lineage:<ApartmentOutlined/>,operations:<ToolOutlined/>,
  terms:<BookOutlined/>,schedules:<FileTextOutlined/>,results:<CheckCircleOutlined/>,dataflows:<ApartmentOutlined/>,domains:<TeamOutlined/>,standards:<BookOutlined/>,models:<DatabaseOutlined/>,
  mounts:<SwapOutlined/>,processing:<ToolOutlined/>,directory:<BookOutlined/>,workspace:<ApartmentOutlined/>,reviews:<FileDoneOutlined/>,"resource-access":<SafetyCertificateOutlined/>,
  etl: <SwapOutlined />, sources: <DatabaseOutlined />, tasks: <FileTextOutlined />, raw: <SwapOutlined />,
  saved: <StarOutlined />, notices: <BellOutlined />, categories:<AppstoreOutlined/>,trust:<SafetyCertificateOutlined/>,feedback:<SolutionOutlined/>,
  applications: <SolutionOutlined />, grants: <UnlockOutlined />, metadata: <ApartmentOutlined />,
  rules: <BookOutlined />, quality: <CheckCircleOutlined />, catalog: <DatabaseOutlined />,
  publications: <FileTextOutlined />, audit: <SafetyCertificateOutlined />, organizations:<ApartmentOutlined/>,"directory-coding":<BookOutlined/>, accounts: <TeamOutlined />,
};
const menus: Record<
  Platform,
  { key: string; label: string; roles?: string[] }[]
> = {
  exchange: [
    {key:"data-services",label:"数据服务",roles:["provider","operator"]},
    { key: "sources", label: "数据源管理", roles: ["operator"] },
    { key: "tasks", label: "CDC 接入任务", roles: ["operator"] },
    { key: "etl", label: "ETL 数据交换", roles: ["operator"] },
    { key:"directory",label:"资源目录" },
    {key:"directory-coding",label:"目录编码配置",roles:["provider"]},
    { key:"processing",label:"数据加工授权",roles:["provider","operator","governor"] },
    { key:"reviews",label:"目录与挂载审核",roles:["provider","admin"] },
    { key: "applications", label: "数据使用申请审批" },
    { key: "grants", label: "数据使用授权" },
  ],
  governance: [
    {key:"workspaces",label:"空间管理",roles:["governor"]},
    {key:"space-sources",label:"来源数据",roles:["governor"]},
    { key: "overview", label: "治理看板", roles: ["governor"] },
    { key: "domains", label: "主题域与责任", roles: ["governor"] },
    { key: "standards", label: "数据标准", roles: ["governor"] },
 {key:"standard-coverage",label:"标准落标",roles:["governor"]},
 {key:"masters",label:"主数据管理",roles:["governor"]},
 {key:"metrics",label:"指标口径",roles:["governor"]},
 {key:"scenarios",label:"场景方案库",roles:["governor"]},
 {key:"releases",label:"变更发布",roles:["governor"]},
    { key:"terms",label:"业务术语",roles:["governor"] },
    { key: "models", label: "数据建模", roles: ["governor"] },
    { key: "metadata", label: "元数据管理", roles: ["governor"] },
    {key:"lightweight",label:"轻量治理",roles:["governor"]},
    { key: "dataflows", label: "数据流设计", roles: ["governor"] },
    { key: "workflows", label: "工作流编排", roles: ["governor"] },
    { key:"schedules",label:"调度计划",roles:["governor"] },
    { key:"results",label:"成果核验",roles:["governor"] },
    { key: "issues", label: "问题整改", roles: ["governor"] },
    { key: "lineage", label: "血缘与影响", roles: ["governor"] },
    { key: "operations", label: "运行运维", roles: ["governor"] },
 { key: "alerts", label: "治理告警", roles: ["governor"] },
 {key:"monitoring",label:"持续数据监测",roles:["governor"]},
 { key: "lifecycle", label: "成果生命周期", roles: ["governor"] },
    { key: "rules", label: "历史来源质量规则", roles: ["governor"] },
    { key: "quality-templates", label: "检查方案", roles: ["governor"] },
    { key: "quality-trends", label: "质量趋势", roles: ["governor"] },
    { key: "quality", label: "质量管理", roles: ["governor"] },
  ],
  assets: [
    { key: "catalog", label: "资产发现" },
    { key: "overview", label: "资产总览" },
    { key: "categories", label: "分类浏览" },
    { key: "products", label: "数据产品" },
    { key: "contracts", label: "契约核验" },
    { key: "lifecycle", label: "认证与生命周期" },
    { key: "usage", label: "我的使用统计" },
    { key: "trust", label: "可信资产核验" },
    { key: "applications", label: "我的申请" },
    { key: "grants", label: "我的资产" },
    { key: "saved", label: "收藏与关注" },
    { key: "notices", label: "我的提醒" },
    { key: "feedback", label: "我的反馈" },
  ],
  security: [
    {key:"overview",label:"安全总览",roles:["admin","auditor"]},
    {key:"classification",label:"安全标签",roles:["admin","auditor"]},
    {key:"recognition",label:"识别规则",roles:["admin","auditor"]},
    {key:"scans",label:"扫描与结果复核",roles:["admin","auditor"]},
    {key:"policies",label:"防护策略",roles:["admin","auditor"]},
    {key:"preflight",label:"策略预检",roles:["admin"]},
    {key:"watermarks",label:"水印管理",roles:["admin","auditor"]},
    {key:"risk-rules",label:"风险规则",roles:["admin","auditor"]},
    {key:"incidents",label:"安全事件",roles:["admin","auditor"]},
    {key:"proofs",label:"水印交付与验证",roles:["admin","auditor"]},
    {key:"resource-access",label:"资源权限",roles:["admin"]},
    { key: "grants", label: "授权管理", roles: ["auditor"] },
    { key: "audit", label: "审计记录", roles: ["auditor"] },
    { key:"organizations",label:"组织与部门",roles:["admin"] },
    { key: "accounts", label: "人员与账号", roles: ["admin"] },
  ],
};
const menuGroups: Partial<Record<Platform, { key: string; label: string; icon: ReactNode; keys: string[] }[]>> = {
  security: [
    {key:"security-workbench",label:"安全工作台",icon:<HomeOutlined/>,keys:["overview"]},
    {key:"security-classification",label:"分类分级",icon:<BookOutlined/>,keys:["classification","recognition","scans"]},
    {key:"security-identity",label:"身份与权限",icon:<KeyOutlined/>,keys:["organizations","accounts","resource-access","grants"]},
    {key:"security-protection",label:"策略与防护",icon:<SafetyCertificateOutlined/>,keys:["policies","watermarks","preflight"]},
    {key:"security-risk",label:"风险监测与处置",icon:<BellOutlined/>,keys:["risk-rules","incidents"]},
    {key:"security-audit",label:"审计与核查",icon:<AuditOutlined/>,keys:["audit","proofs"]},
  ],
  assets: [
    {key:"asset-workbench",label:"资产工作台",icon:<HomeOutlined/>,keys:["overview","lifecycle"]},
    {key:"asset-discovery",label:"发现与理解",icon:<BookOutlined/>,keys:["catalog","categories","products","trust","contracts"]},
    {key:"asset-consumption",label:"申请与使用",icon:<UnlockOutlined/>,keys:["applications","grants","usage"]},
    {key:"asset-feedback",label:"关注与反馈",icon:<BellOutlined/>,keys:["saved","notices","feedback"]},
  ],
  exchange: [
    { key: "resources-group", label: "目录与资源", icon: <BookOutlined />, keys: ["directory","directory-coding"] },
    { key: "sync-group", label: "数据接入与同步", icon: <SwapOutlined />, keys: ["sources", "tasks", "etl"] },
    { key: "sharing-group", label: "数据共享与服务", icon: <ApiOutlined />, keys: ["data-services", "applications", "grants"] },
    { key: "review-group", label: "审核与加工授权", icon: <AuditOutlined />, keys: ["reviews", "processing"] },
  ],
  governance: [
    { key: "overview", label: "工作台", icon: <HomeOutlined />, keys: ["overview"] },
    { key: "preparation", label: "准备数据", icon: <DatabaseOutlined />, keys: ["space-sources", "metadata", "models","masters"] },
    { key: "processing", label: "处理数据", icon: <ApartmentOutlined />, keys: ["lightweight", "dataflows", "workflows", "schedules","scenarios"] },
    { key: "delivery", label: "检查与交付", icon: <CheckCircleOutlined />, keys: ["quality", "results", "lifecycle","releases"] },
    { key: "standards", label: "标准与规范", icon: <BookOutlined />, keys: ["domains", "terms", "standards", "standard-coverage","metrics"] },
    { key: "monitoring", label: "监控与追溯", icon: <ToolOutlined />, keys: ["monitoring", "operations", "alerts", "lineage"] },
  ],
};
function hasRole(user: Principal, roles: string[] | undefined) {
  return (
    !roles ||
    user.roles.includes("admin") ||
    roles.some((r) => user.roles.includes(r))
  );
}
function PlatformPage({
  platform,
  path,
  header,
}: {
  platform: Platform;
  path: string;
  header: (navigation?: ReactNode) => ReactNode;
}) {
  const user = useSession((s) => s.user)!;
 const {workspaceID,navigation:spaceNavigate}=useGovernanceWorkspace();
 const {message:experienceMessage}=AntApp.useApp();
 async function resetExperience(){try{await request("/api/v1/governance/experience-reset",{method:"POST",body:"{}"});useSession.getState().setUser(user);setTimeout(()=>navigate(`/platform/governance/overview?workspace_id=${SAMPLE_SPACE}`,true),0);experienceMessage.success("已重置本浏览器的治理体验空间")}catch(error){experienceMessage.error(errorMessage(error))}}
  const epoch = useSession((s) => s.epoch);
  const [collapsed, setCollapsed] = useState(() => window.matchMedia("(max-width: 991.98px)").matches);
  const [openGovernanceMenu,setOpenGovernanceMenu]=useState<string|null>(null);
  useEffect(()=>setOpenGovernanceMenu(null),[platform,path,workspaceID]);
  useEffect(()=>{
    if(!openGovernanceMenu)return;
    const close=(event:KeyboardEvent)=>{if(event.key==="Escape")setOpenGovernanceMenu(null)};
    window.addEventListener("keydown",close);return()=>window.removeEventListener("keydown",close);
  },[openGovernanceMenu]);
  const module = modules.find((m) => m.id === platform)!;
  const allowed = menus[platform].filter((m) => user.public_test || hasRole(user, m.roles));
  const groups = menuGroups[platform];
  const menuItem = (m: {key: string; label: string}) => ({key: m.key, label: m.label, icon: menuIcons[m.key] ?? <FileTextOutlined />});
  const navigationItems = groups ? [
    ...allowed.filter(m => !groups.some(group => group.keys.includes(m.key))).map(menuItem),
    ...groups.filter(group => group.keys.some(key => allowed.some(m => m.key === key))).map(group => ({
      key: group.key, label: group.label, icon: group.icon,
      children: group.keys.flatMap(key => allowed.filter(m => m.key === key).map(menuItem)),
    })),
  ] : allowed.map(menuItem);
  const page = platform === "exchange" && ["mounts", "workspace"].includes(path) ? "directory" : path === "raw" ? (allowed.some(m=>m.key==="etl")?"etl":"directory") : path || (platform==="governance"?"overview":user.public_test&&platform==="exchange"?"directory":allowed[0]?.key);
  const navigationPage = platform==="governance"&&page==="sandbox"?"lightweight":page;
  const item = platform==="exchange"&&page==="security-labels"&&hasRole(user,["provider"])?{key:"security-labels",label:"资源安全标签"}:allowed.find((m) => m.key === navigationPage) ?? (platform === "assets" && page === "publications" && hasRole(user,["provider"]) ? {key:"publications",label:"历史发布管理"} : undefined);
  if (!(user.public_test || user.roles.includes("admin") || user.platforms.includes(platform)) || !item)
    return (
      <Result
        status="403"
        title="当前账号没有此功能权限"
        subTitle="平台入口与业务角色分别控制，请联系管理员核对。"
        extra={<Button onClick={() => navigate("/")}>返回首页</Button>}
      />
    );
  let content;
  switch (platform + "/" + page) {
    case "exchange/data-services":content=<DataServices/>;break;
    case "exchange/directory":content=<DirectoryCenter/>;break;
    case "exchange/processing":content=<ResourcePermissions processingOnly/>;break;
    case "exchange/reviews":content=<DirectoryReviews/>;break;
    case "exchange/sources":
      content = <Sources />;
      break;
    case "exchange/etl":content=<ETL/>;break;
    case "exchange/tasks":
      content = <Tasks />;
      break;
    case "exchange/applications":
      content = <Applications />;
      break;
    case "exchange/grants":
      content = <Grants />;
      break;
    case "governance/workspaces": content=<GovernanceWorkspaces/>;break;
    case "governance/space-sources":content=<GovernanceWorkspaceSources/>;break;
    case "governance/domains": content=<GovernanceCatalog kind="domain"/>;break;
    case "governance/standards": content=<GovernanceCatalog kind="standard"/>;break;
    case "governance/terms": content=<GovernanceCatalog kind="term"/>;break;
    case "governance/models": content=<GovernanceCatalog kind="model"/>;break;
    case "governance/metadata":
      content = <MetadataWorkspace />;
      break;
    case "governance/overview":content=<GovernanceOverview/>;break;
    case "governance/workflows":content=<GovernanceWorkflows definitionsOnly/>;break;
    case "governance/schedules":content=<GovernanceSchedules/>;break;
    case "governance/results":content=<GovernanceResults/>;break;
    case "governance/issues":content=<GovernanceQualityWorkspace/>;break;
    case "governance/lineage":content=<GovernanceLineage/>;break;
    case "governance/masters":content=<GovernanceCapabilities key="masters" kind="master"/>;break;
 case "governance/metrics":content=<GovernanceCapabilities key="metrics" kind="metric"/>;break;
 case "governance/scenarios":content=<GovernanceCapabilities key="scenarios" kind="scenario"/>;break;
 case "governance/releases":content=<GovernanceReleases/>;break;
 case "governance/standard-coverage":content=<GovernanceStandardCoverage/>;break;
    case "governance/monitoring":content=<GovernanceMonitoring/>;break;
    case "governance/alerts":content=<GovernanceAlerts/>;break;
 case "governance/lifecycle":content=<GovernanceLifecycle/>;break;
 case "governance/operations":content=<GovernanceOperations/>;break;
    case "governance/sandbox": content=<GovernanceLightweight mode="sql"/>;break;
    case "governance/lightweight": content=<GovernanceLightweight/>;break;
    case "governance/dataflows": content=<GovernanceDataflows/>;break;
    case "governance/quality-templates":content=<GovernanceQualityWorkspace/>;break;
    case "governance/quality-trends":content=<GovernanceQualityWorkspace/>;break;
    case "governance/rules":
      content = <Rules />;
      break;
    case "governance/quality":
      content = <GovernanceQualityWorkspace />;
      break;
    case "assets/catalog":
      content = <Assets />;
      break;
    case "assets/products": content=<AssetProducts/>;break;
    case "assets/contracts": content=<AssetProducts mode="contracts"/>;break;
    case "assets/lifecycle": content=<AssetProducts mode="lifecycle"/>;break;
    case "assets/usage": content=<AssetUsage/>;break;
    case "assets/overview": content=<AssetOverview/>;break;
    case "assets/categories": content=<AssetCategories/>;break;
    case "assets/trust": content=<Assets trust/>;break;
    case "assets/feedback": content=<AssetFeedbackWorkspace/>;break;
    case "assets/applications": content = <Applications assets/>; break;
    case "assets/saved": content = <AssetSaved/>; break;
    case "assets/notices": content = <AssetNotices/>; break;
    case "assets/grants": content = <Grants assets/>; break;
    case "assets/publications":
      content = <Catalog publications />;
      break;
    case "exchange/security-labels":content=<SecurityResourceLabels/>;break;
    case "security/preflight":content=<SecurityPreflight/>;break;
    case "security/overview": content=<SecurityOverview/>;break;
    case "security/classification":content=<SecurityControls kind="classification"/>;break;
    case "security/recognition":content=<SecurityControls kind="recognition"/>;break;
    case "security/policies":content=<SecurityControls kind="policy"/>;break;
    case "security/watermarks":content=<SecurityControls kind="watermark"/>;break;
    case "security/risk-rules":content=<SecurityControls kind="risk_rule"/>;break;
    case "security/scans":content=<SecurityScans/>;break;
    case "security/incidents":content=<SecurityIncidents/>;break;
    case "security/proofs":content=<SecurityProofs/>;break;
    case "security/grants":
      content = <Grants security />;
      break;
    case "security/audit":
      content = <AuditPage />;
      break;
    case "security/resource-access":content=<ResourcePermissions/>;break;
    case "exchange/directory-coding":
      content=<DirectoryCoding/>;break;
    case "security/organizations":
      content=<OrganizationManagement/>;break;
    case "security/accounts":
      content = <Accounts />;
      break;
    default:
      content = <Result status="404" title="页面不存在" />;
  }
  const visibleGroups = (groups ?? []).filter(group => group.keys.some(key => allowed.some(m => m.key === key)));
  const governanceItems=[...visibleGroups,...(user.public_test?[{key:"experience-additional",label:"其他功能",icon:<AppstoreOutlined/>,keys:allowed.filter(item=>!visibleGroups.some(group=>group.keys.includes(item.key))).map(item=>item.key)}].filter(group=>group.keys.length):[])];
  const qualityPage=["rules","quality-templates","quality-trends","issues"].includes(page)?"quality":navigationPage;
  const currentGovernanceGroup=platform==="governance"?governanceItems.find(group=>group.keys.includes(qualityPage)):undefined;
  const openGovernance=(key:string)=>{setOpenGovernanceMenu(null);(["lightweight","sandbox"].includes(key)?navigate:spaceNavigate)(`/platform/governance/${key}`)};
  const governanceNavigation=<nav className="governance-top-navigation" aria-label="数据治理功能分类">{governanceItems.map(group=>group.keys.length===1?<Button type="text" key={group.key} aria-current={currentGovernanceGroup?.key===group.key?"page":undefined} onClick={()=>openGovernance(group.keys[0])}>{group.label}</Button>:<Dropdown key={group.key} open={openGovernanceMenu===group.key} onOpenChange={open=>setOpenGovernanceMenu(current=>open?group.key:current===group.key?null:current)} trigger={["click"]} placement="bottomLeft" rootClassName="governance-navigation-dropdown" menu={{id:`governance-${group.key}-menu`,selectedKeys:[qualityPage],items:group.keys.flatMap(key=>allowed.filter(m=>m.key===key)).map(entry=>({key:entry.key,label:entry.label,icon:<span aria-hidden="true">{menuIcons[entry.key]}</span>})),onClick:({key})=>openGovernance(key)}}><Button type="text" aria-haspopup="menu" aria-controls={openGovernanceMenu===group.key?`governance-${group.key}-menu`:undefined} aria-expanded={openGovernanceMenu===group.key} aria-current={currentGovernanceGroup?.key===group.key?"true":undefined}>{group.label}<DownOutlined aria-hidden="true"/></Button></Dropdown>)}</nav>;
  return (
    <Layout className={platform==="governance"&&page==="dataflows"?"workbench governance-dataflow-workbench":platform==="assets"?"workbench assets-workbench":platform==="security"?"workbench security-workbench":"workbench"}>
      {platform !== "governance" && <Layout.Sider
        width="clamp(220px, 14.2vw, 272px)"
        breakpoint="lg"
        collapsedWidth={64}
        collapsible
        trigger={null}
        collapsed={collapsed}
        onCollapse={setCollapsed}
        theme="dark"
      >
        <Brand compact={collapsed} />
        <Menu
          className={platform==="exchange"?"exchange-sidebar-menu":undefined}
          theme="dark"
          mode="inline"
          selectedKeys={[page]}
          items={navigationItems}
          defaultOpenKeys={collapsed ? [] : Object.values(menuGroups).flatMap(platformGroups => platformGroups.map(group => group.key))}
          onClick={({ key }) => navigate(`/platform/${platform}/${key}`)}
        />
        <div className="sidebar-tools">
          <Button className="sidebar-return" type="text" icon={<ArrowLeftOutlined />} aria-label="返回资产首页" title="返回门户" onClick={() => navigate("/")}>
            {!collapsed && "返回门户"}
          </Button>
          <Button type="text" icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />} aria-label={collapsed ? "展开侧栏" : "收起侧栏"} title={collapsed ? "展开侧栏" : "收起侧栏"} onClick={() => setCollapsed(value => !value)} />
        </div>
      </Layout.Sider>}
      <Layout className="workbench-main">
      {header(platform === "governance" ? governanceNavigation : undefined)}
      <Layout.Content
        className={platform === "governance" ? "workspace governance-workspace" : "workspace"}
        key={platform + "/" + page + "/" + user.user_id + "/" + epoch + "/" + (workspaceID??"")}
      >
        <div className={platform==="governance"?"governance-page-context":undefined}><div className="breadcrumb">
          {module.name}
          <span>/</span>
          {currentGovernanceGroup&&currentGovernanceGroup.keys.length>1&&<><span>{currentGovernanceGroup.label}</span><span>/</span></>}
          {item.label}
          {user.full_experience&&!user.public_test&&<Tooltip title="使用现有业务数据，操作会真实保存和执行；遵循正常角色权限、独立审核与审计追踪。"><Tag color="blue" tabIndex={0}>真实业务体验</Tag></Tooltip>}
          {user.public_test&&<Tooltip title={platform==="governance"?"此治理空间只在当前浏览器保存和处理示例数据。可编辑、保存、运行内置步骤；不连接正式数据库，不执行任意代码，不产生正式发布或审核证据。CSV每份最多1 MiB、1000行。":"完整菜单与业务页面可浏览；仅查看已公开的目录和字段，不能修改正式业务数据。"}><Tag color="blue" tabIndex={0}>{platform==="governance"?"治理体验 · 当前浏览器生效":"只读体验"}</Tag></Tooltip>}
          {user.public_test&&platform==="governance"&&<Popconfirm title="重置当前浏览器的治理体验？" description="将删除本浏览器已保存的体验配置、导入文件和运行结果，并恢复内置样本。" onConfirm={resetExperience} okText="重置体验" cancelText="保留数据"><Button danger>重置体验空间</Button></Popconfirm>}
        </div>
        {platform==="governance"&&!["lightweight","sandbox"].includes(page)&&<GovernanceWorkspaceBar/>}</div>{content}
      </Layout.Content>
      {!(platform==="governance"&&page==="dataflows")&&<footer className="workspace-footer">数据有来源 · 使用有授权 · 操作可追溯</footer>}
      </Layout>
    </Layout>
  );
}
function Root() {
  const path = usePath().split("?")[0];
  const user = useSession((s) => s.user);
  const ready = useSession((s) => s.ready);
  const identityError = useSession((s) => s.identityError);
  const [startupError, setStartupError] = useState<unknown>(null);
  const { message } = AntApp.useApp();
  async function hydrate() {
    setStartupError(null);
    try {
      const p = await request<Principal>("/api/v1/me");
      useSession.getState().setUser(p);
    } catch (e) {
      if (!useSession.getState().ready) {
        setStartupError(e);
        useSession.getState().setUser(null);
      }
    }
  }
  useEffect(() => {
    void hydrate();
  }, []);
  useEffect(() => {
    if (ready && !user && path.startsWith("/platform/")) navigate("/login");
  }, [ready, user, path]);
  useEffect(() => {
    function recheck() {
      if (
        document.visibilityState === "visible" &&
        useSession.getState().user
      ) {
        void request<Principal>("/api/v1/me")
          .then((next) => {
            const old = useSession.getState().user;
            if (
              old &&
              JSON.stringify([old.platforms, old.roles, old.department_id, old.public_test, old.full_experience]) !==
                JSON.stringify([next.platforms, next.roles, next.department_id, next.public_test, next.full_experience])
            )
              useSession.getState().setUser(next);
          })
          .catch((error) => {
            if (useSession.getState().user)
              message.warning(errorMessage(error));
          });
      }
    }
    document.addEventListener("visibilitychange", recheck);
    return () => document.removeEventListener("visibilitychange", recheck);
  }, [message]);
  async function logout() {
    try {
      await request<void>("/auth/logout", { method: "POST" });
      useSession.getState().reset();
      navigate("/");
    } catch (e) {
      message.error(errorMessage(e));
    }
  }
  const parts = path.split("/");
  const platform = parts[2] as Platform;
  const isPlatform =
    path.startsWith("/platform/") && modules.some((m) => m.id === platform);
  const governanceHeader = isPlatform && platform === "governance";
  const currentPlatform=modules.find(module=>module.id===platform);
  const platformSelector = isPlatform && user && currentPlatform ? <div className="header-platform"><Dropdown trigger={["click"]} placement="bottomLeft" rootClassName="header-platform-menu" menu={{
    selectedKeys:[platform],
    items:modules.filter(module=>user.public_test||user.roles.includes("admin")||user.platforms.includes(module.id)).map(module=>({key:module.id,label:<div className="header-platform-option"><span className="header-platform-option-icon" aria-hidden="true"><ModuleIcon platform={module.id}/></span><span className="header-platform-option-content"><span className="header-platform-option-name">{module.name}</span><span className="header-platform-option-description">{module.description}</span></span><span className="header-platform-option-status">{module.id===platform&&<CheckCircleOutlined aria-label="当前平台"/>}</span></div>})),
    onClick:({key})=>{if(key!==platform)navigate(`/platform/${key}/`)}
  }}><Button className="header-platform-trigger" aria-label={`切换平台：${currentPlatform.name}`} aria-haspopup="menu" icon={<ModuleIcon platform={platform}/>}>{currentPlatform.name}<DownOutlined aria-hidden="true"/></Button></Dropdown></div> : null;
  const header = (navigation?: ReactNode) => (
      <header className={governanceHeader ? "global-header governance-header" : "global-header"}>
        {governanceHeader ? <Brand /> : platformSelector ?? <Brand subtitle />}
        {navigation}
        <Space className={governanceHeader ? "governance-header-account" : undefined}>
          {governanceHeader && platformSelector}
          <Button className="header-guide-button" aria-label="操作指南" icon={<BookOutlined aria-hidden="true" />} onClick={() => navigate(governanceHeader ? "/guides/governance" : isPlatform && platform === "assets" ? "/guides/assets" : isPlatform && platform === "security" ? "/guides/security" : isPlatform && platform === "exchange" ? "/guides/exchange" : "/guides")}>
            操作指南
          </Button>
          <div className="header-account">
            {user ? (
              <>
                <Dropdown
                  menu={{
                    items: [
                      { key: "home", label: "返回首页", icon: <HomeOutlined aria-hidden="true" /> },
                      ...(user.roles.includes("admin")?[{key:"accounts",label:"组织与人员管理",icon:<TeamOutlined aria-hidden="true"/>}]:[]),
                      { key: "logout", label: "退出登录", icon: <LogoutOutlined aria-hidden="true" /> },
                    ],
                    onClick: ({ key }) =>
                      key === "logout" ? void logout() : key === "accounts" ? navigate("/platform/security/accounts") : navigate("/"),
                  }}
                >
                  <Button type="text" className="account-button"><span className="account-avatar"><UserOutlined /></span><span className="account-copy"><strong>{user.username}</strong><small>{user.department_id}</small></span><DownOutlined /></Button>
                </Dropdown>
              </>
            ) : (
              <Button type="primary" onClick={() => navigate("/login")}>
                登录
              </Button>
            )}
          </div>
        </Space>
      </header>
  );
  return (
    <div className={`app-shell ${isPlatform ? "workbench-shell" : path === "/login" ? "login-shell" : "public-shell"}`}>
      {!isPlatform && path !== "/login" && header()}
      {identityError && (
        <div className="startup-warning">
          <Alert
            type="warning"
            title={identityError}
            action={
              <Button onClick={() => void hydrate()}>重新核对身份</Button>
            }
          />
        </div>
      )}
      {Boolean(startupError) && (
        <div className="startup-warning">
          <Failure error={startupError} retry={() => void hydrate()} />
        </div>
      )}
      {!ready ? (
        <div className="full-loading">
          <Spin description="正在核对登录状态" />
        </div>
      ) : path === "/login" ? (
        <Login />
      ) : isPlatform && user ? (
        platform==="governance"&&!["lightweight","sandbox"].includes(parts[3])?<GovernanceWorkspaceProvider><PlatformPage platform={platform} path={parts[3] ?? ""} header={header}/></GovernanceWorkspaceProvider>:<PlatformPage platform={platform} path={parts[3] ?? ""} header={header}/>
      ) : ["/guides", "/guides/exchange", "/guides/governance", "/guides/assets", "/guides/security"].includes(path) ? (
        <Suspense fallback={<div className="full-loading"><Spin description="正在打开操作指南" /></div>}><OperationGuides /></Suspense>
      ) : path === "/" ? (
        <Home />
      ) : (
        <Result
          status="404"
          title="页面不存在"
          extra={<Button onClick={() => navigate("/")}>返回首页</Button>}
        />
      )}
      {ready&&path!=="/login"&&<FloatingAgent key={`${user?.user_id??"visitor"}:${path}`} workbench={isPlatform}/>}
    </div>
  );
}
export function App() {
  return <ConfigProvider locale={zhCN} theme={appTheme}><AntApp><Root /></AntApp></ConfigProvider>;
}

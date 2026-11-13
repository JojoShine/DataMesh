import { Alert, Button, Empty, Space, Tag, Typography } from "antd";
import type { ReactNode } from "react";
import { ApiError, errorMessage } from "../api/client";
export const statusNames: Record<string, string> = {
  untested: "待检查",
  cancelled: "已取消",
  skipped: "已跳过",
  open: "待处理",
  waiting_approval: "待发布审批",
  in_progress: "处理中",
  retesting: "复测中",
  closed: "已关闭",
  superseded: "已替代",
  interrupted: "已中断",
 checking:"核验中",queued:"排队中",
  review_pending:"待审核",
 department_pending:"待部门审核",authority_pending:"待主管审核",validated:"校验通过",needs_supplement:"待补正",unmounted:"已卸载",
  passed: "已通过",
  failed: "失败",
  not_applicable: "不适用",
  draft: "草稿",
  retired: "已停用",
  published: "已发布",
  withdrawn: "已下架",
  not_checked: "待检查",
  blocked: "已阻断",
  pending: "待完成",
  complete: "完整",
  archive_gap: "归档待补偿",
  result_gap: "结果待补偿",
  both_gap: "两路待补偿",
  streaming: "增量运行中",
  preflight_passed: "预检通过",
  initializing: "初始化中",
  pausing: "暂停中",
  paused: "已暂停",
  recovering: "恢复中",
  error: "异常",
  manual_required: "需人工处理",
  finished: "已结束",
  running: "运行中",
  unknown: "结果未知",
  submitted: "待审批",
  approved: "已批准",
  rejected: "已驳回",
  active: "有效",
  revoked: "已撤销",
  expired: "已到期",
  suspended: "已暂停",
  accepted: "已受理",
  succeeded: "已完成",
};
export function Status({ value }: { value: string }) {
  const green = [
    "passed",
    "published",
    "complete",
    "active",
    "succeeded",
    "approved",
    "streaming",
  ];
  const red = [
    "failed",
    "blocked",
    "error",
    "revoked",
    "archive_gap",
    "result_gap",
    "both_gap",
  ];
  return (
    <Tag
      color={
        green.includes(value)
          ? "success"
          : red.includes(value)
            ? "error"
            : ["pending", "unknown", "submitted", "not_checked"].includes(value)
              ? "warning"
              : "default"
      }
    >
      {statusNames[value] ?? value}
    </Tag>
  );
}
export function Failure({
  error,
  retry,
}: {
  error: unknown;
  retry?: () => void;
}) {
  const restricted=error instanceof ApiError && error.code==="EXPERIENCE_DATA_PRIVATE";
  return (
    <Alert
      title={restricted?"此区域的内部记录未公开":errorMessage(error)}
      description={restricted?"菜单和业务页面保持完整展示。内部记录、连接凭据、源数据及私人信息不向公开测试账号提供；未公开不代表记录为零。可继续查看操作指南及其他功能页面。":undefined}
      type={restricted?"info":"error"}
      showIcon
      action={!restricted&&retry ? <Button onClick={retry}>重试</Button> : undefined}
    />
  );
}
export function Title({
  title,
  description,
  actions,
}: {
  title: string;
  description: string;
  actions?: ReactNode;
}) {
  return (
    <div className="page-heading">
      <div>
        <Typography.Title level={2}>{title}</Typography.Title>
        <p>{description}</p>
      </div>
      <Space wrap>{actions}</Space>
    </div>
  );
}
export function EmptyData({
  description = "暂无记录",
}: {
  description?: string;
}) {
  return (
    <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={description} />
  );
}
export function JsonView({ value }: { value: unknown }) {
  return <pre className="json-view">{JSON.stringify(value, null, 2)}</pre>;
}
export function Stamp({ value }: { value: string | undefined | null }) {
  return (
    <>
      {value
        ? new Date(value).toLocaleString("zh-CN", { hour12: false })
        : "尚无记录"}
    </>
  );
}

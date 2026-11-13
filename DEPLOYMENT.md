# DataMesh 生产部署

依据 [deploy-standards](https://github.com/JojoShine/deploy-standards/blob/main/DEPLOYMENT.md)，2026-11-13 用户确认：PostgreSQL 随项目独立部署，React SPA 与 Go 后端合并为一个业务镜像。仅维护此组合。

正式入口 `https://tbtparent.me/datamesh`，`BASE_PATH=/datamesh`，`APP_ORIGIN=https://tbtparent.me`。Compose 项目固定 `datamesh-production`，目录 `/opt/datamesh`，业务端口为本机 `127.0.0.1:31010`，容器使用 TLS 8080。前端在 CI 用 `BASE_PATH=/datamesh` 构建，后端、API、静态资源、健康和会话 Cookie 均保留此前缀。运行目录和端口首次使用前必须检查冲突。

## 产物与发布

- PR 仅检查、构建与验证；main 验证后发布 `sha-<完整提交>` 候选；正式 `vX.Y.Z` 只提升已通过 main CI 的产物，不重建。
- 服务器只下载版本部署包、已编译插件和镜像，不上传 Go、React 或 Java 源码，不安装业务依赖或构建业务镜像。
- 发布清单记录提交、架构、模式、全部镜像摘要与文件 SHA-256。应用、迁移、首次管理员初始化共用 `APP_IMAGE` 的同一摘要；代码执行器也复用此镜像。
- Flink、Kafka/Connect、Doris、RustFS 和来源 PostgreSQL 独立运行；Java 来源读取服务和 Python/JavaScript 隔离执行环境有各自的受控产物。镜像架构为原生 `linux/amd64`，不得直接发布本机 ARM64 镜像或把 ARM64 锁文件的本地 image ID 当作仓库摘要。
- `CODE_PYTHON_REF`、`CODE_JAVASCRIPT_REF` 是先拉取的仓库摘要；执行器的 `CODE_*_IMAGE` 是这些镜像对应的配置 ID。保留现有执行器只接受不可变 ID 的边界。
- 项目尚未声明开源许可证，OCI 标记 `NOASSERTION`；不要将仓库可见性等同于授予开源许可。

## 私密配置与角色

环境文件 `.env.prod`、可选 `.env.bootstrap` 权限600，不执行 `source` 或 `eval`。秘密留在 `secrets/`，不进入 Git、GitHub artifact 或镜像：

```text
/opt/datamesh/
  compose.prod.yaml
  .env.prod
  release-manifest.json
  config/{postgres,source-postgres,doris,flink}/
  config/public-test-accounts/
  config/runtime/
  plugins/{flink,jdbc}/
  secrets/{postgres-admin-password,migration-db-password,app-db-password}
  secrets/{bootstrap-admin.json,code-runner-token,stream-script-token}
  secrets/tls/{server.crt,server.key}
  backups/
```

PostgreSQL17.11 数据位于项目专用 `platform_pg` 卷，实际卷名前缀来自固定 Compose 项目名；Docker 数据根在 `/data/docker`。不发布5432。`datamesh_bootstrap` 仅数据库初始化与维护使用；`datamesh_owner` 是非超级、不可建库/建角色的迁移身份；应用仅接收 `datamesh_app` 连接。创建角色的配置只在首次空数据目录执行，升级不重复初始化角色或删除数据卷。

秘密文件按实际镜像 Unix 用户分配读取权限：PostgreSQL Alpine 的 postgres 用户并非 Debian 的999，须从精确镜像读取UID/GID；应用为10001:10001。公开内部证书可644，私钥与 bootstrap JSON 仅对应任务读取；Flink 和应用共享的令牌为受限组只读。初始化脚本应保持755，归档传输后检查权限。

`CREDENTIAL_KEY`、水印历史密钥、来源凭据及已迁移对象的关联 ID 必须保留，否则旧加密内容与签名无法读取。现有环境升级不重新生成它们。应用长期运行不接收数据库管理、迁移或 bootstrap 管理员秘密。

应用仅信任 `TRUSTED_PROXY_IPS` 列出的精确 IP；部署者在项目网络创建后填入宿主机 Nginx 连接所用的实际 Docker 网关 IP。缺省不信任转发头，拒绝全网 CIDR 或通配符，不能用任意客户端自报地址作为限流身份。

## 首次部署与升级顺序

1. 核对发布清单、文件摘要、原生架构、资源与端口；保留当前和上一版本部署文件。确认服务器上其他项目的容器和 Nginx 状态。
2. 备份平台 PostgreSQL、来源 PostgreSQL、Doris、RustFS 和私密配置；若有持续任务，另外保存位点和恢复点。验证隔离还原，不能只确认备份文件存在。
3. 从同一版本包获取完整 Compose 和配置，准备私密环境文件、令牌、内部 TLS 证书及插件。只使用清单内的准确镜像摘要，先拉取独立代码运行时，核对其配置 ID。
4. 启动并等待 PostgreSQL 就绪，显式执行迁移；迁移失败不启动新应用。启动计算与存储组件，核对 Doris FE/BE Alive、Flink 实际 slots、Gateway、Connect 插件及 S3 读写。
5. 全新环境显式运行 bootstrap；迁移已有平台时按恢复计划还原一致快照，保留已有身份和初始化账本，不重复播种或覆盖账号。旧登录会话、私人 CSV 沙箱及其私人对象不默认迁移。复制的业务与历史记录须按约定清单核对。
6. 启动应用，先从本机 TLS8080 验证健康、静态资源、登录和真实业务，再安装唯一 Nginx 业务片段。确认没有 location 冲突；`nginx -t` 通过后平滑加载。
7. 检查实际 HTTPS `/datamesh/healthz`、页面、API、CSV/SQL/轻量治理和启用的连续链路，同时复核原有项目。失败按下节回退，不将部分验证写成全部通过。

以下命令均在 `/opt/datamesh`，真实连接及密码只来自权限600的环境文件，不放命令行：

```sh
docker compose --env-file .env.prod -f compose.prod.yaml --profile ops pull
docker compose --env-file .env.prod -f compose.prod.yaml up -d --no-build --wait postgres
docker compose --env-file .env.prod -f compose.prod.yaml run --rm --no-deps migrate
# 仅首次空平台创建管理员，bootstrap JSON 仅挂载到该一次性任务。
docker compose --env-file .env.prod -f compose.prod.yaml run --rm --no-deps bootstrap
docker compose --env-file .env.prod -f compose.prod.yaml up -d --no-build --wait rustfs doris-fe doris-be kafka connect jobmanager taskmanager sql-gateway postgres-source source-reader code-runner
docker compose --env-file .env.prod -f compose.prod.yaml up -d --no-build --wait app
docker compose --env-file .env.prod -f compose.prod.yaml ps
```

`/app/bin/migrate` 使用镜像内迁移，重复运行安全，并授予应用只读结构版本的权限。`/app/bin/bootstrap` 保留已有管理员密码；遇到停用、公开体验或非管理员的同名身份时拒绝，而非提权或复活。`/app/bin/healthcheck` 验证受信任 TLS、正确前缀、真实数据库和干净同版本迁移；存活接口是 `/datamesh/health/live`。

独立角色准备、元数据库迁移、首次管理员初始化和后续业务数据导入是不同操作。已有平台恢复前先核对来源地址、加密密钥和组件版本；PostgreSQL 使用逻辑备份，Doris 使用支持的备份/恢复机制，不以跨架构复制数据目录替代验证。

## Nginx 与入口

`nginx/business.conf.example` 仅定义业务 location，安装为 `/etc/nginx/business/tbtparent.me/datamesh.conf`，由既有 HTTPS server 的业务 include 加载，不新增冲突的 server/listen/证书配置。代理保留 `/datamesh` 前缀，以内部受信证书校验上游 TLS；`/datamesh/internal/` 不向公网开放。SPA 回退只发生在真实页面路径，API 和健康错误不得返回 index.html。静态资源沿用应用的保守缓存策略。

## 回滚与验收边界

保留当前及上一发布清单、环境、Nginx 片段和经过核验的数据库备份。升级先停止派发、确认无活跃任务，再执行向后兼容迁移。应用回退使用上一兼容 `APP_IMAGE` 和匹配配置，不更换数据库主版本、不删除卷、不自动回滚数据库。只有一致恢复计划明确允许时才恢复数据库和关联存储；迁移48含数据时会拒绝直接降级。

CI 真实验证受限数据库角色、重复迁移、重复初始化、持久卷重建、TLS/前缀、静态与API链路、会话以及原生脚本精度/空值协议。持续 CDC、Flink、Doris、S3 和迁入业务数据还须在目标环境做实际验收；未提供的第三方数据库实例不记作已通过。当前部署是单实例方案，允许短暂维护窗口，不声明高可用或并发性能保证。

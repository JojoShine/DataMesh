# 厂商驱动

将与目标版本匹配、允许部署的官方 JDBC 驱动放在本机 `.local/source-reader-drivers`，由只读卷挂载到 `/opt/drivers`。

- 达梦：`dm.jdbc.driver.DmDriver`
- 金仓：`com.kingbase8.Driver`
- 瀚高：`com.highgo.jdbc.Driver`
- 海量 Vastbase：`cn.com.vastbase.Driver`（v版，jdbc:vastbase）或 `org.postgresql.Driver`（p版，jdbc:postgresql；文件名需包含vastbase以区分普通PG驱动），按驱动实际提供的类自动识别。

安装后重建 source-reader 容器；平台显示实际驱动是否可加载。驱动缺失时禁止连接操作，不使用其他数据库的驱动冒充国产原生连接。不要提交商业驱动、账号、口令或连接文件。

本机目前安装以下 Maven Central 官方发布包；它们各自的 POM 声明 Apache License 2.0。迁移时保留此目录挂载，并按目标实例版本重新核对，不将驱动加载检查视为数据库联调。

| 厂商 | Maven 坐标 | 本机安装版本 |
| --- | --- | --- |
| 达梦 | `com.dameng:DmJdbcDriver18` | `8.1.3.140` |
| 瀚高 | `com.highgo:HgdbJdbc` | `6.2.5` |
| 金仓 | `cn.com.kingbase:kingbase8` | `9.0.1` |
| 海量 | `cn.com.vastdata:vastbase-jdbc` | `2.16.1v` |

本机四种国产驱动均已加载，版本兼容性仍待实例验证。以上四个包可按坐标从 `https://repo.maven.apache.org/maven2/` 下载到 `.local/source-reader-drivers`；保持文件对容器运行用户可读，挂载仍为只读。不要关闭 HTTPS 证书校验来下载驱动。

新增环境先执行 `scripts/dev_env.py init` 创建内部服务令牌，再构建、启动 source-reader 和 server。已有环境仅补充缺失的 `SOURCE_READER_TOKEN`，保留其余连接与密钥。该令牌至少32字符；两个服务必须使用同一值。source-reader 不发布主机端口，服务健康检查也需要令牌。复制部署配置时同时迁移安全保存的环境配置及驱动目录，按42版迁移扩展白名单类型。

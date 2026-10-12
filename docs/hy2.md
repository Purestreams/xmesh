# HY2 接入

新建 Gateway 和完整线路向导默认同时开启 VMess/WS 与 HY2。在 Gateway 列表或编辑表单中，可分别设置“启用 VMess/WS”和“启用 HY2”，保存后由 Gateway 应用。一个 Gateway 的全部用户和线路共用一个 HY2 入站，每个 User × Node 授权（Grant）使用独立随机凭据。默认 TCP VMess/WS 和 UDP HY2 都使用 8080，也可分别指定端口。

关闭协议会移除对应入站及订阅项，不会删除授权、HY2 证书、pin 或已保存端口；重新开启时继续使用原凭据和证书。两种协议都关闭时，Gateway–Agent 隧道仍保留。已有 Gateway 保持原接入设置；旧配置中的 HY2 端口 0 表示尚未开启，在面板勾选 HY2 后自动使用 VMess 端口。应先升级 Gateway 才能应用全部开关；Controller 会对旧 Gateway 隐藏已关闭协议的凭据。

## 证书验证

Controller 为 Gateway 自动生成 ECDSA P-256 自签证书，并在状态文件中持久保存证书和私钥。证书 SAN 对应 Gateway 的公网地址，支持域名、IPv4 和 IPv6。pinSHA256 是证书 DER 的 SHA-256 十六进制摘要，不是公钥摘要。证书有效期为十年；重启、重装和端口变更不会改变 pin，修改公网地址会重新生成证书和 pin，重新应用后更新订阅。

HY2 分享链接携带 sni 和 pinSHA256，不输出 insecure。官方 Hysteria 客户端仍需要信任自签证书才能完成正常的证书链、主机名及有效期验证。因此仅有分享链接并不保证任意客户端可以直接使用；客户端必须支持该 pin，并正确配置公开证书。不要通过关闭证书验证解决导入问题。

在面板的授权管理中，已发布的授权提供“下载 HY2 严格验证客户端包”。该 ZIP 包包含 client.json、gateway.pem 和使用说明，仅包含公开证书，不含私钥。解压后在该目录运行：

```sh
hysteria client -c client.json
```

官方客户端会同时检查正常 TLS 验证和 SHA-256 pin。客户端包也可通过原订阅 URL 添加 `?format=hysteria2&grant=<授权ID>` 下载，仍按订阅令牌、用户启用状态及线路授权校验访问。重置订阅会同时轮换 HY2、VMess 和 SOCKS 凭据。

## 入口与出口

HY2 未认证的 HTTP/3 请求固定返回 `404 Not Found` 和静态正文，没有文件、proxy URL 或原站转发。请求 Host 写成 api.bilibili.com 也不会代取该站内容。Gateway–Agent 的 REALITY target 与 HY2 分别配置，HY2 不使用 REALITY target 提供网站内容。

认证后按 Grant 身份选择现有 Agent 隧道或外部出口。Agent 流量继续经 REALITY/smux 转发，客户端接入 HY2 不会改变 Agent 的公网要求。端口、凭据或授权变化按现有配置应用机制重启 Gateway Xray；现有会话会断开，客户端需要重新连接。

## 部署

放行所选 UDP 端口及 UDP 端口映射；仅开放同号 TCP 端口无法接入 HY2。普通 HTTP/HTTPS 反向代理和 WS CDN 不承载 HY2。安装命令会携带 `--hy2-port` 并检查新安装的 UDP 端口冲突，已有安装升级不会把自身监听误判为冲突。

先升级 Controller，再升级 Gateway 和 Agent，使外部与内嵌 Xray 版本匹配。HY2 分享链接和客户端包只在 Gateway 报告对应配置已应用且 HY2 就绪后发布。旧 Gateway 不报告 HY2Ready，不能发布 HY2 接入。

## Docker 验收

`tests/reality/Dockerfile` 包含固定版本的 Xray 和经过 SHA-256 校验的官方 Hysteria 客户端。Gateway 集成测试覆盖多个 Grant 共用 TCP/UDP 端口、不同 Agent 路由、TCP/UDP 回显、外部出口与用量、撤销已有及新连接、错误 pin、未信任证书、错误 SNI、错误凭据和未认证 HTTP/3 固定 404。Web 开关测试覆盖两种协议同时开启、仅 HY2、仅 VMess/WS、全部关闭和重新开启，检查真实监听、订阅、剩余协议传输、证书稳定及 Agent 隧道。全部新 HY2 客户端配置都禁止 insecure。

`tests/reality/multicontainer.ps1` 启动独立 Controller、两个 Gateway、Agent、VMess 客户端和官方 HY2 客户端，实际从 Controller 下载已发布的客户端包，再验证 VMess/HY2 → REALITY → Agent 的 TCP/UDP 及用量。

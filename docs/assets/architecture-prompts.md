# Architecture image generation

Generated on 2026-10-13 with the built-in `image_gen` tool. The English image localizes the newly generated Chinese diagram while preserving its routes and layout.

Outputs: `xmesh-architecture-zh.png` and `xmesh-architecture.png`. The live monitor screenshot is captured separately from the public page; it is not generated.

## Chinese generation prompt

```text
Use case: infographic-diagram.
Asset type: Simplified Chinese architecture image for the XMesh GitHub README.
Primary request: Regenerate the project's architecture diagram to accurately show its current implementation. Landscape 16:9 white background, crisp professional flat technical illustration, large readable Simplified Chinese text with exact English protocol names, generous spacing. Consistent blue control plane, teal Agent traffic, amber external-exit route; dark navy typography. No decorative background, no watermark.
Title verbatim: "XMesh 架构".
Composition: top band labelled "管理面" with three boxes: left "公开监控" with subtext "/monitor · 只读状态与 RTT"; center "Controller 控制器" with subtext "管理面板 · API · 订阅 · JSON 状态" and "不转发用户流量"; right "xmesh-updater" with subtext "节点宿主机升级助手". Blue dashed double-arrow between monitor and Controller labelled "公开状态"; blue dashed double-arrow between updater and Controller labelled "任务与结果".
Middle band labelled "数据面", left to right four distinct boxes: "客户端", "Gateway 网关" with subtext "公网入口 · Xray + 调度器", "Agent 出口节点" with subtext "无需公网 IP · 内嵌 Xray-core", "目标网络".
Solid arrow client to Gateway labelled on two lines "VMess / WebSocket" and "HY2 / QUIC + TLS". Solid double-arrow Gateway to Agent labelled "REALITY + smux v2" and "TCP / UDP". Solid arrow Agent to target labelled "目标 DNS · TCP / UDP".
Blue dashed management double-arrows from Controller down to Gateway and Agent labelled "HTTPS 配置与状态"; these must not connect to client or target. Under the Gateway–Agent route put a teal dashed arrow pointing FROM Agent TO Gateway labelled "Agent 主动建连"; do not reverse this arrow.
Bottom alternate route: a separate amber box "外部出口" with subtext "无需 Agent / Link". Amber solid arrow FROM Gateway TO external exit labelled "VMess / VLESS", then amber solid arrow FROM external exit TO target. External exit is parallel to Agent, never routed through Agent, and no direct Gateway-to-target bypass exists.
Small footer verbatim: "新建 Gateway 默认开启 VMess/WS 与 HY2，可独立开关".
Ensure every required box and edge is present, arrowheads are unambiguous, no crossed labels, no added protocols, IP addresses, credentials, or performance claims. Keep all required text spelled verbatim.
```
## English localization prompt

```text
Use case: text-localization.
Asset type: English XMesh architecture image for README.en.md.
Input image 1 is the edit target: the freshly generated Simplified Chinese XMesh architecture diagram.
Replace only the in-image language with accurate English labels. Preserve every icon, box, color, position, size, arrowhead, line, and route exactly. Keep the landscape white-background layout. Ensure readable typography and no clipped text.
Text replacements verbatim:
"XMesh 架构" -> "XMesh Architecture"
"管理面" -> "Control plane"
"公开监控" -> "Public monitor"
"/monitor · 只读状态与 RTT" -> "/monitor · Read-only status & RTT"
"公开状态" -> "Public status"
"Controller 控制器" -> "Controller"
"管理面板 · API · 订阅 · JSON 状态" -> "Web panel · API · Subscriptions · JSON state"
"不转发用户流量" -> "No user traffic"
"xmesh-updater" stays unchanged
"节点宿主机升级助手" -> "Node host upgrade helper"
"任务与结果" -> "Tasks & results"
"HTTPS 配置与状态" -> "HTTPS config & status"
"数据面" -> "Data plane"
"客户端" -> "Client"
"VMess / WebSocket" and "HY2 / QUIC + TLS" stay unchanged
"Gateway 网关" -> "Gateway"
"公网入口 · Xray + 调度器" -> "Public entry · Xray + scheduler"
"REALITY + smux v2" and "TCP / UDP" stay unchanged
"Agent 主动建连" -> "Agent initiates tunnel"
"Agent 出口节点" -> "Agent"
"无需公网 IP · 内嵌 Xray-core" -> "No public IP · Embedded Xray-core"
"目标 DNS · TCP / UDP" -> "Target DNS · TCP / UDP"
"目标网络" -> "Destination"
"VMess / VLESS" stays unchanged
"外部出口" -> "External exit"
"无需 Agent / Link" -> "No Agent / Link"
"新建 Gateway 默认开启 VMess/WS 与 HY2，可独立开关" -> "New Gateways enable VMess/WS and HY2 by default; independent switches"
Constraints: retain teal Agent-to-Gateway initiation arrow pointing left, solid bidirectional Gateway–Agent traffic arrow, amber Gateway-to-external-exit-to-destination route, and dashed blue Controller management connections. Do not introduce new connections or protocols. Every original object must remain.
```

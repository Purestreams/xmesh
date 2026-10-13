"use strict";

const i18n = typeof window !== "undefined" ? window.XMeshI18n : require("./i18n.js");
const t = i18n.t;

function defaultRealityTarget(region) {
  if (region === "cn") return "api.bilibili.com:443";
  if (region === "overseas") return "www.swift.com:443";
  return "";
}

function sampleRates(samples) {
  return samples.map((sample, index) => {
    const previous = samples[index - 1];
    const seconds = previous
      ? (Date.parse(sample.at) - Date.parse(previous.at)) / 1000
      : 0;
    const valid =
      previous &&
      seconds > 0 &&
      seconds <= 600 &&
      (sample.instance_id || previous.instance_id
        ? sample.instance_id && sample.instance_id === previous.instance_id
        : sample.generation === previous.generation) &&
      sample.upload_bytes >= previous.upload_bytes &&
      sample.download_bytes >= previous.download_bytes &&
      sample.ready &&
      previous.ready;
    return {
      ...sample,
      uploadRate: valid
        ? (sample.upload_bytes - previous.upload_bytes) / seconds
        : null,
      downloadRate: valid
        ? (sample.download_bytes - previous.download_bytes) / seconds
        : null,
    };
  });
}
function selectionPlan(kind, owner, ids, data) {
  const result = { created: 0, reenabled: 0, unchanged: 0 };
  for (const id of new Set(ids)) {
    const existing =
      kind === "assign"
        ? data.routes.find((r) => r.agent_id === owner && r.gateway_id === id)
        : data.grants.find((g) => g.user_id === owner && g.route_id === id);
    const active =
      kind === "assign"
        ? existing?.enabled &&
          data.links.some((l) => l.route_id === existing.id && l.enabled)
        : existing?.enabled;
    if (!existing) result.created++;
    else if (!active) result.reenabled++;
    else result.unchanged++;
  }
  return result;
}
function pendingConnectionCandidates(data) {
  const routes = new Map(
    data.routes.filter((r) => r.agent_id && !r.upstream_id)
      .map((r) => [JSON.stringify([r.agent_id, r.gateway_id]), r]),
  );
  const linked = new Set(data.links.map((l) => l.route_id));
  const activeLinks = new Set(data.links.filter((l) => l.enabled).map((l) => l.route_id));
  const candidates = [];
  for (const agent of data.nodes.filter((n) => n.role === "Agent")) {
    for (const gateway of data.nodes.filter((n) => n.role === "Gateway")) {
      const value = JSON.stringify([agent.id, gateway.id]);
      const route = routes.get(value);
      if ((route?.assignment_enabled ?? route?.enabled) && activeLinks.has(route.id)) continue;
      const target = gateway.target || defaultRealityTarget(gateway.region);
      candidates.push({
        value, agent, gateway, target,
        available: !!(agent.enabled && gateway.enabled && gateway.host),
        needsTarget: !linked.has(route?.id) && !target,
        action: !linked.has(route?.id) ? "created" : "reenabled",
        state: !route ? t("未建立") : !linked.has(route.id) ? t("待补建 Link") : t("已停用"),
      });
    }
  }
  return candidates;
}
function formatBytes(value) {
  if (value == null || !Number.isFinite(value)) return "—";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024;
    i++;
  }
  return `${value.toFixed(i ? 1 : 0)} ${units[i]}`;
}
function formatRate(value) {
  if (value > 0 && value < 1) return `${value.toFixed(2)} B/s`;
  return `${formatBytes(value)}/s`;
}
if (typeof module !== "undefined")
  module.exports = {
    sampleRates,
    selectionPlan,
    formatBytes,
    formatRate,
    defaultRealityTarget,
    pendingConnectionCandidates,
  };

if (typeof document !== "undefined")
  (() => {
    const $ = (selector, root = document) => root.querySelector(selector);
    const $$ = (selector, root = document) => [
      ...root.querySelectorAll(selector),
    ];
    const el = (tag, text, className) => {
      const node = document.createElement(tag);
      if (text != null) node.textContent = text;
      if (className) node.className = className;
      return node;
    };
    const svgEl = (tag, attrs, text) => {
      const node = document.createElementNS("http://www.w3.org/2000/svg", tag);
      Object.entries(attrs || {}).forEach(([key, value]) =>
        node.setAttribute(key, value),
      );
      if (text != null) node.textContent = text;
      return node;
    };
    const names = {
      overview: [t("总览"), t("查看网络健康状况与需要处理的事项。")],
      network: [t("节点与线路"), t("管理入口、出口和连接策略。")],
      users: [t("用户与订阅"), t("选择授权线路，跟踪配置发布并复制订阅。")],
      deploy: [t("部署向导"), t("新建或复用节点，跟踪从安装到可用的每一步。")],
      maintenance: [t("系统维护"), t("版本升级、发布源、安装令牌与操作记录。")],
    };
    const stages = {
      ready: t("可用"),
      pending: t("待就绪"),
      offline: t("离线"),
      disabled: t("停用"),
      configured: t("已配置，待自动测试"),
      testing: t("测试中"),
      probe_failed: t("测试失败"),
    };
    let data = null,
      refreshBusy = false,
      submitting = false,
      mapView = "topology",
      lastSuccessfulRefresh = null;
    const nodeName = (id) => data?.nodes.find((n) => n.id === id)?.name || id;
    const exitID = (route) => route.upstream_id || route.agent_id;
    const routeName = (route) =>
      `${nodeName(route.gateway_id)} / ${nodeName(exitID(route))}`;
    function notice(text, error = false) {
      $("#notice").textContent = t(text);
      $("#notice").classList.toggle("error", error);
    }
    function clearFormError(form) {
      $(".form-error", form)?.remove();
    }
    function showFormError(form, message) {
      clearFormError(form);
      const error = el("div", message, "form-error");
      error.setAttribute("role", "alert");
      error.tabIndex = -1;
      form.prepend(error);
      error.focus();
      error.scrollIntoView({ block: "center", behavior: "smooth" });
    }
    function navigate() {
      const target = location.hash.slice(1) || "overview";
      const section = document.getElementById(target);
      const page = names[target] ? target : section?.dataset.page || "overview";
      $$("section[data-page]").forEach((s) => {
        s.hidden = s.dataset.page !== page;
      });
      $$(".sidebar a").forEach((a) => {
        a.classList.toggle("active", a.hash === `#${page}`);
        if (a.hash === `#${page}`) a.setAttribute("aria-current", "page");
        else a.removeAttribute("aria-current");
      });
      $("#page-title").textContent = names[page][0];
      $("#page-description").textContent = names[page][1];
      if (section && target !== page) {
        const details = $("details.create-details", section);
        if (details) details.open = true;
        section.scrollIntoView({ block: "start" });
      } else {
        window.scrollTo(0, 0);
        requestAnimationFrame(() => window.scrollTo(0, 0));
      }
    }
    function badge(stage, label) {
      return el("span", label || stages[stage] || stage, `badge ${stage}`);
    }
    function showDetail(title, content) {
      $("#detail-title").textContent = title;
      $("#detail-body").replaceChildren(content);
      if (!$("#detail-dialog").open) $("#detail-dialog").showModal();
    }
    function showNode(id) {
      const node = data.nodes.find((n) => n.id === id);
      if (!node) return;
      const body = el("div"),
        dl = el("dl", null, "detail-grid");
      const values = node.role === "External exit" ? {
        [t("角色")]: t(node.role),
        [t("地址")]: node.host || t("尚未选择节点"),
        [t("状态")]: !node.enabled ? t("停用") : node.status.online ? t("已选择（未探测）") : t("待选择"),
        [t("最近错误")]: t(node.next) || t("无"),
      } : {
        [t("角色")]: t(node.role),
        [t("地址")]: node.host || t("主动连接 Gateway"),
        [t("状态")]: !node.enabled ? t("停用") : node.status.online ? t("在线") : t("离线"),
        [t("配置版本")]: `${node.status.applied_version} / ${node.desired}`,
        [t("软件版本")]: node.status.binary_version || t("尚未上报"),
        [t("最后上报")]:
          node.status.last_seen && !node.status.last_seen.startsWith("0001")
            ? new Date(node.status.last_seen).toLocaleString(i18n.locale())
            : t("尚未上报"),
        [t("下一步")]: t(node.next),
        [t("最近错误")]:
          node.status.apply_error ||
          node.status.last_error ||
          node.status.xray_error ||
          t("无"),
      };
      Object.entries(values).forEach(([k, v]) =>
        dl.append(el("dt", k), el("dd", v)),
      );
      body.append(dl);
      const link = el("a", t("打开节点管理"));
      link.href = node.role === "Gateway" ? "#gateways" : node.role === "Agent" ? "#agents" : "#upstreams";
      link.addEventListener("click", () => $("#detail-dialog").close());
      body.append(link);
      showDetail(node.name, body);
    }
    function showRoute(id) {
      const route = data.routes.find((r) => r.id === id);
      if (!route) return;
      const body = el("div");
      body.append(badge(route.state));
      if (route.upstream_id) {
        body.append(upstreamProbeSummary(route), upstreamProbeForm(route));
      }
      data.links
        .filter((l) => l.route_id === id)
        .forEach((link) => {
          const p = el("p");
          p.append(
            el("strong", `Link：${link.name}`),
            el(
              "small",
              t("RTT {0} ms · {1} streams · 累计 ↑ {2} ↓ {3}", link.status.rtt_millis ? link.status.rtt_millis.toFixed(1) : "—", link.status.active_streams || 0, formatBytes(link.status.upload_bytes), formatBytes(link.status.download_bytes)),
            ),
          );
          if (link.status.last_error)
            p.append(el("small", link.status.last_error, "error"));
          body.append(p);
        });
      const a = el("a", t("查看链路配置"));
      a.href = route.upstream_id ? "#upstreams" : "#links";
      a.textContent = route.upstream_id ? t("查看外部出口") : t("查看链路配置");
      a.addEventListener("click", () => $("#detail-dialog").close());
      body.append(a);
      showDetail(t("节点组合：{0}", routeName(route)), body);
    }
    function renderMetrics() {
      const runtimeNodes = data.nodes.filter((n) => n.role !== "External exit");
      const ready = data.routes.filter((r) => r.state === "ready").length,
        configured = data.routes.filter((r) => r.state === "configured").length,
        online = runtimeNodes.filter((n) => n.enabled && n.status.online).length;
      const pending = runtimeNodes.filter(
        (n) =>
          n.enabled &&
          (!n.enrolled ||
            n.status.applied_version < n.desired ||
            n.status.apply_error),
      ).length;
      const items = [
        [
          t("已确认可用线路"),
          `${ready} / ${data.routes.length}`,
          configured ? t("另有 {0} 条外部线路已配置，待自动测试", configured) : t("两端配置与链路均已就绪"),
          "good",
        ],
        [
          t("在线节点"),
          `${online} / ${runtimeNodes.length}`,
          t("Gateway 与 Agent 实时心跳"),
          "",
        ],
        [
          t("待完成配置"),
          pending,
          t("未安装、待应用或应用失败"),
          pending ? "warn" : "good",
        ],
        [
          t("已发布授权"),
          data.grants.filter(
            (g) =>
              g.enabled &&
              g.published &&
              data.users.some((u) => u.id === g.user_id && u.enabled) &&
              data.routes.some((r) => r.id === g.route_id && r.selectable),
          ).length,
          t("线路可用状态请查看拓扑"),
          "",
        ],
      ];
      $("#metrics").replaceChildren(
        ...items.map(([label, value, hint, color]) => {
          const box = el("div", null, "metric");
          box.append(
            el("span", label),
            el("strong", value, color),
            el("small", hint),
          );
          return box;
        }),
      );
    }
    function upstreamProbeForm(route) {
      const form = el("form", null, "inline");
      form.method = "post";
      form.action = "/admin/attachments/" + encodeURIComponent(route.id) + "/probe";
      const csrf = el("input");
      csrf.type = "hidden";
      csrf.name = "csrf";
      csrf.value = $('input[name="csrf"]')?.value || "";
      const button = el("button", t("测试连接"));
      button.disabled = !route.can_probe || route.state === "testing";
      form.append(csrf, button);
      return form;
    }
    function upstreamProbeSummary(route) {
      const body = el("div", null, "upstream-probe");
      const probe = route.probe;
      if (route.probe_supported) body.append(el("p", t("定时测试间隔：{0} 秒", route.probe_interval_seconds || 300), "muted"));
      if (!route.probe_supported) {
        body.append(el("p", t("升级 Gateway 后可使用出口连通性测试。"), "muted"));
      } else if (!probe) {
        body.append(el("p", t("配置应用后将自动测试 ip.sb 和 microsoft.com；也可手动测试。"), "muted"));
      } else if (!probe.completed_at || probe.completed_at.startsWith("0001")) {
        body.append(el("p", t("Gateway 正在经此上游测试 ip.sb 和 microsoft.com…")));
      } else {
        body.append(el("p", t("最近测试：{0}", new Date(probe.completed_at).toLocaleString(i18n.locale())), "muted"));
        for (const target of probe.targets || []) {
          const name = target.url === "https://api.ip.sb/ip" ? "ip.sb" : "microsoft.com";
          const row = el("p", t("{0}：{1} · HTTP {2} · {3} ms", name, target.success ? t("成功") : t("失败"), target.http_status || "—", target.duration_millis), target.success ? "good" : "error");
          body.append(row);
          if (target.exit_ip) body.append(el("p", t("出口 IP：{0}", target.exit_ip)));
          if (target.error) body.append(el("p", target.error, "error"));
        }
      }
      return body;
    }
    function renderUpstreamProbes() {
      for (const root of $$("[data-upstream-probe]")) {
        const route = data.routes.find((r) => r.id === root.dataset.upstreamProbe);
        if (route) root.replaceChildren(upstreamProbeSummary(route));
      }
      for (const button of $$("[data-upstream-probe-button]")) {
        const route = data.routes.find((r) => r.id === button.dataset.upstreamProbeButton);
        button.disabled = !route?.can_probe || route.state === "testing";
      }
    }
    function renderRouteOverview() {
      const root = $("#route-overview");
      if (!root || !data) return;
      if (!data.routes.length) {
        root.replaceChildren(el("p", t("尚无线路。先从部署向导创建或绑定节点。"), "empty"));
        return;
      }
      const query = $("#route-search").value.trim().toLowerCase(),
        state = $("#route-state").value,
        visible = data.routes.filter((route) =>
          (!query || routeName(route).toLowerCase().includes(query)) &&
          (state === "all" || route.state === state));
      if (!visible.length) {
        root.replaceChildren(el("p", t("没有匹配的线路。"), "empty"));
        return;
      }
      const list = el("div", null, "route-cards");
      visible.forEach((route) => {
        const card = el("article", null, "route-card"),
          heading = el("div", null, "route-card-heading"),
          title = el("strong", routeName(route)),
          kind = route.upstream_id ? t("外部出口") : t("Agent 出口"),
          grantCount = data.grants.filter((grant) => grant.route_id === route.id && grant.enabled && grant.published).length,
          actions = el("div", null, "route-card-actions"),
          detail = el("button", t("查看状态"));
        detail.type = "button";
        detail.className = "secondary";
        detail.addEventListener("click", () => showRoute(route.id));
        heading.append(title, badge(route.state));
        card.append(heading, el("small", t("{0} · {1} 个已发布授权", kind, grantCount)));
        if (route.upstream_id) {
          card.append(upstreamProbeSummary(route));
          actions.append(upstreamProbeForm(route));
        }
        actions.append(detail);
        const config = el("a", t("管理配置"));
        config.href = route.upstream_id ? "#upstreams" : "#links";
        const grant = el("a", t("授权用户"));
        grant.href = "#subscribe";
        actions.append(config, grant);
        card.append(actions);
        list.append(card);
      });
      root.replaceChildren(list);
    }
    function filteredMap() {
      const query = $("#map-search").value.toLowerCase(),
        state = $("#map-state").value;
      const matches = (n) =>
        `${n.name} ${n.host}`.toLowerCase().includes(query);
      const enabledNodes = data.nodes.filter((n) => n.enabled),
        enabledIDs = new Set(enabledNodes.map((n) => n.id)),
        ids = new Set(enabledNodes.filter(matches).map((n) => n.id));
      const routes = data.routes.filter(
        (r) =>
          enabledIDs.has(r.gateway_id) && enabledIDs.has(exitID(r)) &&
          (!query || ids.has(r.gateway_id) || ids.has(exitID(r))) &&
          (state === "all" || r.state === state),
      );
      const connected = new Set(
        routes.flatMap((r) => [r.gateway_id, exitID(r)]),
      );
      return {
        routes,
        nodes: enabledNodes.filter(
          (n) => connected.has(n.id) || (state === "all" && matches(n)),
        ),
      };
    }
    function renderMap() {
      if (!data) return;
      const { nodes, routes } = filteredMap(),
        gateways = nodes.filter((n) => n.role === "Gateway"),
        agents = nodes.filter((n) => n.role === "Agent"),
        exits = nodes.filter((n) => n.role !== "Gateway");
      $("#topology").hidden = mapView !== "topology";
      $("#matrix").hidden = mapView !== "matrix";
      if (!nodes.length) {
        $("#topology").replaceChildren(
          el("p", t("没有匹配的节点。前往部署向导创建第一条线路。"), "empty"),
        );
        $("#matrix").replaceChildren(el("p", t("没有匹配的节点。"), "empty"));
        return;
      }
      const height = Math.max(
          210,
          Math.max(gateways.length, exits.length) * 100 + 70,
        ),
        svg = svgEl("svg", {
          viewBox: `0 0 900 ${height}`,
          class: "network",
          role: "img",
          "aria-label": t("Gateway 与出口网络拓扑"),
        });
      svg.append(
        svgEl(
          "text",
          { x: 80, y: 30, class: "subtext" },
          t("GATEWAYS / 公网入口"),
        ),
        svgEl("text", { x: 620, y: 30, class: "subtext" }, t("AGENT / 外部出口")),
      );
      const positions = new Map();
      gateways.forEach((n, i) =>
        positions.set(n.id, { x: 65, y: 55 + i * 100 }),
      );
      exits.forEach((n, i) =>
        positions.set(n.id, { x: 610, y: 55 + i * 100 }),
      );
      routes.forEach((route) => {
        const a = positions.get(route.gateway_id),
          b = positions.get(exitID(route));
        if (!a || !b) return;
        const rtts = data.links
          .filter((link) => link.route_id === route.id && link.enabled && link.status.online && link.status.ready && link.status.rtt_millis > 0)
          .map((link) => `${link.name}: ${link.status.rtt_millis.toFixed(1)} ms`);
        const path = svgEl("path", {
          d: `M ${a.x + 225} ${a.y + 31} C 450 ${a.y + 31}, 450 ${b.y + 31}, ${b.x} ${b.y + 31}`,
          class: `edge ${route.state}`,
        });
        path.append(svgEl("title", {}, `${routeName(route)} · ${stages[route.state]}${rtts.length ? " · " + rtts.join(" / ") : ""}`));
        svg.append(path);
      });
      nodes.forEach((n) => {
        const p = positions.get(n.id),
          g = svgEl("g", {
            class: "node",
            tabindex: 0,
            role: "button",
            "aria-label": `${n.role} ${n.name}`,
          });
        g.append(
          svgEl("rect", { x: p.x, y: p.y, width: 225, height: 64, rx: 10 }),
          svgEl("circle", {
            cx: p.x + 17,
            cy: p.y + 24,
            r: 4,
            fill: n.role === "External exit" ? "#d4a242" : !n.enabled
              ? "#b6c1cd"
              : n.status.online
                ? "#27a68c"
                : "#d99393",
          }),
          svgEl(
            "text",
            { x: p.x + 30, y: p.y + 28 },
            n.name.length > 22 ? n.name.slice(0, 21) + "…" : n.name,
          ),
          svgEl(
            "text",
            { x: p.x + 17, y: p.y + 48, class: "subtext" },
            n.host ||
              `${n.status.tunnel_connections || 0} tunnels · ${n.status.tcp_connections || 0} TCP`,
          ),
        );
        g.addEventListener("click", () => showNode(n.id));
        g.addEventListener("keydown", (e) => {
          if (e.key === "Enter") showNode(n.id);
        });
        svg.append(g);
      });
      const routeList = el("div", null, "topology-routes"),
        routeTable = el("table"),
        routeHead = el("tr"),
        routeBody = el("tbody");
      routeList.append(el("h3", t("线路延迟 · Gateway 上报")));
      [t("节点组合"), "Link", t("状态"), "RTT", t("详情")].forEach((name) =>
        routeHead.append(el("th", name)),
      );
      const routeThead = el("thead");
      routeThead.append(routeHead);
      routeTable.append(routeThead);
      routes.forEach((route) => {
        const links = data.links.filter((link) => link.route_id === route.id && link.enabled);
        (links.length ? links : [null]).forEach((link) => {
          const row = el("tr"),
            detail = el("button", t("查看"));
          detail.type = "button";
          detail.addEventListener("click", () => showRoute(route.id));
          [
            routeName(route),
            link?.name || (route.upstream_id ? nodeName(route.upstream_id) : t("无启用 Link")),
            stages[route.state],
            link?.status.online && link.status.ready && link.status.rtt_millis > 0
              ? `${link.status.rtt_millis.toFixed(1)} ms`
              : "—",
          ].forEach((value) => row.append(el("td", value)));
          const action = el("td");
          action.append(detail);
          row.append(action);
          routeBody.append(row);
        });
      });
      routeTable.append(routeBody);
      routeList.append(routeTable);
      $("#topology").replaceChildren(svg, routeList);
      const table = el("table"),
        thead = el("thead"),
        tr = el("tr");
      tr.append(el("th", "Gateway / Agent"));
      agents.forEach((a) => tr.append(el("th", a.name)));
      thead.append(tr);
      table.append(thead);
      const tbody = el("tbody");
      gateways.forEach((g) => {
        const row = el("tr");
        row.append(el("th", g.name));
        agents.forEach((a) => {
          const td = el("td"),
            route = data.routes.find(
              (r) => r.gateway_id === g.id && r.agent_id === a.id,
            ),
            visible = route && routes.some((r) => r.id === route.id),
            button = el(
              "button",
              route ? (visible ? `${stages[route.state]} · ${data.links.filter((l) => l.route_id === route.id).length} Link` : t("已筛除")) : t("+ 绑定"),
              `matrix-cell ${route?.state || "unassigned"}`,
            );
          button.type = "button";
          button.disabled = route ? !visible : !g.enabled || !a.enabled;
          button.addEventListener("click", () => {
            if (route) showRoute(route.id);
            else {
              location.hash = "assign";
              setAssignmentTab("agent");
              const f = $("#assign form");
              f.elements.agent_id.value = a.id;
              $$("[name=gateway_id]", f).forEach(
                (c) => (c.checked = c.value === g.id),
              );
              f.dataset.dirty = "true";
              updateChooser(f);
            }
          });
          td.append(button);
          row.append(td);
        });
        tbody.append(row);
      });
      table.append(tbody);
      $("#matrix").replaceChildren(table);
    }
    function renderProgress() {
      const cards = data.nodes.filter((n) => n.role !== "External exit").map((n) => {
        const c = el("div", null, "progress-card"),
          steps = el("ol", null, "steps");
        c.append(el("strong", `${n.role} · ${n.name}`));
        const online = n.enabled && n.status.online,
          applied =
            online &&
            n.status.applied_version >= n.desired &&
            !n.status.apply_error,
          ready =
            applied &&
            n.status.ready &&
            (n.role !== "Gateway" || n.status.xray_ready),
          hasRoute = data.routes.some((r) => r.gateway_id === n.id || r.agent_id === n.id),
          linked = data.routes.some(
            (r) =>
              (r.gateway_id === n.id || exitID(r) === n.id) &&
              r.state === "ready",
          );
        [
          [t("已创建"), true],
          [t("已安装"), n.enrolled],
          [t("在线"), online],
          [t("配置应用"), applied],
          [t("运行就绪"), ready],
          [t("线路可用"), linked],
        ].forEach(([label, done]) =>
          steps.append(
            el("li", `${done ? "✓ " : ""}${label}`, done ? "done" : ""),
          ),
        );
        c.append(steps, el("p", !n.enabled
          ? t("节点已停用。启用后继续部署。")
          : !hasRoute && !n.enrolled
            ? t("节点已创建，尚无可用线路。先安装节点，再连接已有节点。")
            : t(n.next)));
        if (n.enabled && !hasRoute) {
          if (!n.enrolled) {
            const install = el("a", t("生成安装命令 →"));
            install.href = "#quick-install";
            c.append(install);
          }
          const next = el("a", t("连接已有节点 →"));
          next.href = "#assign";
          c.append(next);
        }
        return c;
      });
      $("#deployment-progress").replaceChildren(
        ...(cards.length
          ? cards
          : [el("p", t("先创建线路或节点，再按照下方步骤完成安装。"), "empty")]),
      );
      const todos = data.nodes.filter((n) => n.role !== "External exit")
        .filter(
          (n) =>
            n.enabled &&
            (!n.enrolled ||
              !n.status.online ||
              !n.status.ready ||
              n.status.apply_error ||
              n.status.applied_version < n.desired ||
              (n.role === "Gateway" && !n.status.xray_ready)),
        )
        .map((n) => ({
          title: `${n.role} · ${n.name}`,
          text: t(n.next),
          target: !n.enrolled ? "quick-install" : "readiness",
        }));
      data.routes
        .filter((r) => !["ready", "configured", "disabled"].includes(r.state))
        .forEach((r) =>
          todos.push({
            title: routeName(r),
            text: r.upstream_id ? t("线路{0}：检查 Gateway 配置与上游节点。", stages[r.state]) : t("线路{0}：检查两端配置、Link 与 REALITY 连接。", stages[r.state]),
            target: r.upstream_id ? "upstreams" : "links",
          }),
        );
      data.users
        .filter(
          (u) =>
            u.enabled &&
            data.grants.some(
              (g) => g.user_id === u.id && g.enabled && !g.published,
            ),
        )
        .forEach((u) =>
          todos.push({
            title: u.name,
            text: t("{0} · 等待 Gateway 应用配置后发布。", t(u.publication)),
            target: "subscribe",
          }),
        );
      $("#todo-list").replaceChildren(
        ...(todos.length
          ? todos.map((todo) => {
              const row = el("div", null, "todo"),
                text = el("div"),
                a = el("a", t("查看 →"));
              a.href = `#${todo.target}`;
              text.append(el("strong", todo.title), el("p", todo.text));
              row.append(text, a);
              return row;
            })
          : [
              el(
                "p",
                data.nodes.length
                  ? t("当前没有待处理异常。")
                  : t("创建第一条线路，开始连接你的网络。"),
                "empty",
              ),
            ]),
      );
    }
    function chart(title, samples, series, format) {
      const box = el("div", null, "chart");
      box.append(el("h3", title));
      const valid = samples
        .flatMap((s) => series.map((k) => s[k]))
        .filter((v) => v != null && Number.isFinite(v));
      if (!valid.length) {
        box.append(
          el("p", t("尚无足够样本；不会填补缺失或重启期间的数据。"), "empty"),
        );
        return box;
      }
      const actualMax = Math.max(...valid, 0),
        max = Math.max(actualMax, 0.01),
        svg = svgEl("svg", {
          viewBox: "0 0 500 160",
          role: "img",
          "aria-label": t("{0}，最高 {1}", title, format(actualMax)),
        }),
        from = Date.parse(samples[0].at),
        to = Date.parse(samples.at(-1).at),
        span = Math.max(to - from, 1);
      [20, 80, 140].forEach((y) =>
        svg.append(svgEl("line", { x1: 5, x2: 495, y1: y, y2: y })),
      );
      series.forEach((key, index) => {
        let points = [];
        const flush = () => {
          if (points.length === 1) {
            const [cx, cy] = points[0].split(",");
            svg.append(
              svgEl("circle", {
                cx,
                cy,
                r: 3,
                fill: index ? "#22a78f" : "#4679ed",
              }),
            );
          } else if (points.length > 1)
            svg.append(
              svgEl("polyline", {
                points: points.join(" "),
                class: index ? "download" : "",
              }),
            );
          points = [];
        };
        samples.forEach((sample, i) => {
          if (sample[key] == null || !Number.isFinite(sample[key])) {
            flush();
            return;
          }
          if (
            i &&
            Date.parse(sample.at) - Date.parse(samples[i - 1].at) > 600000
          )
            flush();
          points.push(
            `${5 + (490 * (Date.parse(sample.at) - from)) / span},${140 - (120 * sample[key]) / max}`,
          );
        });
        flush();
      });
      box.append(svg);
      const caption = el("p");
      caption.append(
        el("span", new Date(from).toLocaleTimeString(i18n.locale())),
        el("span", t("最高 {0}", format(actualMax))),
        el("span", new Date(to).toLocaleTimeString(i18n.locale())),
      );
      box.append(caption);
      return box;
    }
    function renderHistory() {
      const select = $("#history-link"),
        old = select.value;
      if (
        [...select.options]
          .map((o) => o.value + "|" + o.textContent)
          .join("\n") !== data.links.map((l) => l.id + "|" + l.name).join("\n")
      ) {
        select.replaceChildren(
          ...data.links.map((l) => {
            const o = el("option", l.name);
            o.value = l.id;
            return o;
          }),
        );
        if (data.links.some((l) => l.id === old)) select.value = old;
      }
      const samples = sampleRates(data.history?.[select.value] || []).map(
        (s) => ({
          ...s,
          rtt: s.ready && s.rtt_millis > 0 ? s.rtt_millis : null,
        }),
      );
      const grid = el("div", null, "chart-grid");
      grid.append(
        chart(
          t("吞吐速率 · 蓝色上传 / 绿色下载"),
          samples,
          ["uploadRate", "downloadRate"],
          formatRate,
        ),
        chart(t("往返延迟 RTT"), samples, ["rtt"], (v) => v.toFixed(1) + " ms"),
      );
      $("#history-chart").replaceChildren(grid);
      const ranking = el("div", null, "ranking"),
        links = data.links
          .filter(
            (l) =>
              l.enabled &&
              l.status.online &&
              l.status.ready &&
              l.status.rtt_millis > 0,
          )
          .sort((a, b) => b.status.rtt_millis - a.status.rtt_millis)
          .slice(0, 5),
        max = Math.max(...links.map((l) => l.status.rtt_millis), 1);
      ranking.append(el("h3", t("实时 RTT · 最高 5 条链路")));
      links.forEach((l) => {
        const row = el("div", null, "rank-row"),
          track = el("div", null, "rank-track"),
          bar = el("div", null, "rank-bar");
        bar.style.width = `${(100 * l.status.rtt_millis) / max}%`;
        track.append(bar);
        row.append(
          el("span", l.name),
          track,
          el("span", `${l.status.rtt_millis.toFixed(1)} ms`),
        );
        ranking.append(row);
      });
      $("#rankings").replaceChildren(ranking);
    }
    function renderUsage() {
      const root = $("#usage-table");
      if (!root) return;
      const expanded = new Set(
        [...root.querySelectorAll("details[open]")].map((d) => d.dataset.userId),
      );
      const users = data.usage || [];
      if (!users.length) {
        root.replaceChildren(el("p", t("暂无用户。"), "empty"));
        return;
      }
      const periods = [["5h", t("近 5 小时")], ["1d", t("近 1 天")], ["7d", t("近 7 天")], ["30d", t("近 30 天")]];
      const traffic = (bytes) =>
        `↑ ${formatBytes(bytes?.upload || 0)} · ↓ ${formatBytes(bytes?.download || 0)}`;
      const table = el("table");
      const head = el("tr");
      [t("用户"), ...periods.map(([, label]) => label), t("链路明细")].forEach((label) =>
        head.append(el("th", label)),
      );
      const thead = el("thead");
      thead.append(head);
      table.append(thead);
      const body = el("tbody");
      users.forEach((usage) => {
        const user = data.users.find((item) => item.id === usage.user_id);
        const row = el("tr");
        row.append(el("th", user?.name || usage.user_id));
        periods.forEach(([key]) => row.append(el("td", traffic(usage.windows?.[key]))));
        const cell = el("td");
        const details = el("details");
        details.dataset.userId = usage.user_id;
        details.open = expanded.has(usage.user_id);
        details.append(el("summary", t("{0} 条 Link", usage.links.length)));
        if (usage.links.length) {
          const links = el("table");
          const linkHead = el("tr");
          ["Link", ...periods.map(([, label]) => label)].forEach((label) =>
            linkHead.append(el("th", label)),
          );
          const linkThead = el("thead");
          linkThead.append(linkHead);
          links.append(linkThead);
          const linkBody = el("tbody");
          usage.links.forEach((link) => {
            const linkRow = el("tr");
            linkRow.append(el("th", link.name));
            periods.forEach(([key]) =>
              linkRow.append(el("td", traffic(link.windows?.[key]))),
            );
            linkBody.append(linkRow);
          });
          links.append(linkBody);
          details.append(links);
        } else {
          details.append(el("p", t("尚未分配 Link。"), "empty"));
        }
        cell.append(details);
        row.append(cell);
        body.append(row);
      });
      table.append(body);
      root.replaceChildren(table);
    }
    function renderOperations() {
      const upgrade = data.upgrade || {};
      $("#upgrade-progress").textContent =
        `Controller ${upgrade.Version || "—"} · ${t(upgrade.State) || t("暂无升级任务")}${upgrade.At ? " · " + upgrade.At : ""}`;
      const upgradeButton = $("#upgrade form button");
      if (upgradeButton)
        upgradeButton.disabled = ["queued", "running"].includes(upgrade.State);
      const table = el("table"),
        head = el("tr");
      [t("时间"), t("操作人"), t("操作"), t("请求结果")].forEach((v) =>
        head.append(el("th", v)),
      );
      const thead = el("thead");
      thead.append(head);
      table.append(thead);
      const body = el("tbody");
      [...(data.operations || [])].reverse().forEach((o) => {
        const tr = el("tr");
        [
          new Date(o.at).toLocaleString(i18n.locale()),
          o.actor,
          o.action,
          o.status < 400 ? t("成功 / 已接受") : t("失败 ({0})", o.status),
        ].forEach((v) => tr.append(el("td", v)));
        body.append(tr);
      });
      table.append(body);
      $("#operation-list").replaceChildren(
        data.operations?.length ? table : el("p", t("暂无操作记录。"), "empty"),
      );
    }
    function updateLiveTables() {
      for (const [id, role] of [
        ["gateways", "Gateway"],
        ["agents", "Agent"],
      ]) {
        $$(`#${id} tbody tr`).forEach((row) => {
          const action = $('form[action$="/toggle"]', row)?.getAttribute(
              "action",
            ),
            node = data.nodes.find(
              (n) =>
                action ===
                `/admin/${role === "Gateway" ? "gateways" : "agents"}/${n.id}/toggle`,
            );
          if (!node) return;
          const cells = row.cells,
            config = cells[role === "Gateway" ? 2 : 1],
            runtime = cells[role === "Gateway" ? 3 : 2];
          config.replaceChildren(
            badge(
              !node.enabled
                ? "disabled"
                : node.status.apply_error
                  ? "offline"
                  : node.status.applied_version < node.desired
                    ? "pending"
                    : "ready",
              !node.enabled
                ? t("停用")
                : node.status.apply_error
                  ? t("应用失败")
                  : node.status.applied_version < node.desired
                    ? t("待应用")
                    : t("已应用"),
            ),
            el(
              "small",
              t("配置 {0} / {1}", node.status.applied_version, node.desired),
            ),
          );
          runtime.replaceChildren(
            badge(
              node.status.online ? "ready" : "offline",
              node.status.online ? t("在线") : t("离线"),
            ),
            el(
              "small",
              t("{0}：隧道 {1} · TCP {2} · UDP {3}", node.status.online ? t("当前") : t("上次上报"), node.status.tunnel_connections, node.status.tcp_connections, node.status.udp_associations),
            ),
          );
          if (role === "Gateway") {
            runtime.append(el("small", `VMess/WS ${node.vmess_disabled ? t("已关闭") : t("已开启")}`));
            runtime.append(el("small", `HY2 ${node.hy2_disabled || !node.hy2_port ? t("已关闭") : `UDP ${node.hy2_port} · ${node.status.hy2_ready ? t("就绪") : t("待应用")}`}`));
          }
          if (!node.status.online && node.status.last_seen && !node.status.last_seen.startsWith("0001"))
            runtime.append(el("small", t("最后上报 {0}", new Date(node.status.last_seen).toLocaleString(i18n.locale()))));
          if (node.status.last_error)
            runtime.append(el("small", node.status.last_error, "error"));
        });
      }
      $$("#links>table:first-of-type tbody tr").forEach((row) => {
        const action = $('form[action$="/toggle"]', row)?.getAttribute(
            "action",
          ),
          link = data.links.find(
            (l) => action === `/admin/links/${l.id}/toggle`,
          );
        if (!link) return;
        const st = link.status;
        row.cells[3].replaceChildren(
          badge(
            !link.enabled
              ? "disabled"
              : st.online
                ? st.ready
                  ? "ready"
                  : "pending"
                : "offline",
            !link.enabled
              ? t("停用")
              : st.online
                ? st.ready
                  ? t("上报就绪")
                  : t("待就绪")
                : t("离线"),
          ),
          el(
            "small",
            t("{0}：{1} 条连接 · {2} 个流 · RTT {3} ms", st.online ? t("当前") : t("上次上报"), st.connections || 0, st.active_streams || 0, st.online ? st.rtt_millis || "—" : "—"),
          ),
          el(
            "small",
            `↑ ${formatBytes(st.upload_bytes)} ↓ ${formatBytes(st.download_bytes)}`,
          ),
          el(
            "small",
            t("写阻塞 {0} 次 / {1} ms · 丢弃 {2}", st.write_stalls || 0, st.write_blocked_millis || 0, st.queue_drops || 0),
          ),
          el("small", st.last_error || "", "error"),
        );
        if (!st.online && st.last_seen && !st.last_seen.startsWith("0001"))
          row.cells[3].append(el("small", t("最后上报 {0}", new Date(st.last_seen).toLocaleString(i18n.locale()))));
      });
      $$("#grant-table tbody tr").forEach((row) => {
        const action = $('form[action$="/toggle"]', row)?.getAttribute(
            "action",
          ),
          grant = data.grants.find(
            (g) => action === `/admin/grants/${g.id}/toggle`,
          );
        if (!grant) return;
        const state = grant.enabled
          ? grant.published
            ? "ready"
            : "pending"
          : "disabled";
        row.cells[3].replaceChildren(
          badge(state),
          el("small", grant.published ? t("已发布") : t("尚未发布")),
        );
        row.cells[4].replaceChildren(
          el(
            "small",
            `↑ ${formatBytes(grant.status.upload_bytes)} ↓ ${formatBytes(grant.status.download_bytes)}`,
          ),
          el(
            "small",
            `TCP ${grant.status.tcp_connections || 0} · UDP ${grant.status.udp_associations || 0}`,
          ),
        );
      });
      const readiness = $("#readiness tbody");
      if (readiness)
        readiness.replaceChildren(
          ...data.nodes.filter((n) => n.role !== "External exit").map((n) => {
            const row = el("tr");
            [
              n.role + " " + n.name,
              n.status.binary_version || "—",
              n.enrolled ? t("已安装") : t("待安装"),
              n.enabled && n.status.online ? t("在线") : t("离线"),
              n.status.apply_error ||
                (n.status.applied_version >= n.desired ? t("已应用") : t("待应用")),
              n.status.ready && (n.role !== "Gateway" || n.status.xray_ready)
                ? t("就绪")
                : t("待就绪"),
              t(n.next),
            ].forEach((v) => row.append(el("td", v)));
            return row;
          }),
        );
      $$("#subscribe .subscription-row").forEach((row) => {
        const user = data.users.find((u) => u.id === row.dataset.userId);
        if (user) $(".publication", row).textContent = `${user.name}（${t(user.publication)}）`;
      });
    }
    function renderNodeUpgrades() {
      const section = $("#node-upgrades");
      if (!section || !data) return;
      section.querySelectorAll('form[action="/admin/upgrades"] fieldset label').forEach((label) => {
        const input = label.querySelector('input[name="node_id"]');
        const node = data.nodes.find((item) => item.id === input?.value);
        if (!node) return;
        const up = data.updaters?.[node.id];
        const text = `${node.role} ${node.name} · ${t(up?.State) || t("未配对")} · ${node.status.binary_version || "—"} · ${label.dataset.links} Links / ${label.dataset.users} users`;
        label.replaceChildren(input, document.createTextNode(text));
      });
      const csrf = section.querySelector('form[action="/admin/upgrades"] input[name="csrf"]')?.value;
      const tables = section.querySelectorAll("table");
      const nodesBody = tables[0]?.querySelector("tbody");
      const tasksBody = tables[1]?.querySelector("tbody");
      if (nodesBody) {
        for (const row of nodesBody.rows) {
          const action = row.querySelector("form")?.getAttribute("action") || "";
          const match = action.match(/\/admin\/updaters\/(?:gateway|agent)\/([^/]+)\/pair$/);
          if (!match) continue;
          const up = data.updaters?.[match[1]];
          const node = data.nodes.find((n) => n.id === match[1]);
          row.cells[1].textContent = t("{0} · {1}/{2} · 节点 {3} · 升级助手 {4}", t(up?.State) || t("未配对"), up?.Mode || "—", up?.Arch || "—", node?.status.binary_version || "—", up?.Version || "—");
        }
      }
      if (!tasksBody) return;
      tasksBody.replaceChildren();
      const tasks = new Map((data.upgrade_tasks || []).map((task) => [task.id, task]));
      const resumeTasks = new Set();
      for (const batch of Object.values(data.upgrade_batches || {})) {
        if (batch.stage !== "paused" || !batch.error?.startsWith("previously healthy Link ")) continue;
        let lastCompleted = "", pending = false, valid = true;
        for (const id of batch.task_ids || []) {
          const task = tasks.get(id);
          if (!task || task.batch_id !== batch.id) { valid = false; break; }
          if (task.stage === "succeeded" && !pending) lastCompleted = id;
          else if (task.stage === "pending") pending = true;
          else { valid = false; break; }
        }
        if (valid && lastCompleted) resumeTasks.add(lastCompleted);
      }
      for (const task of data.upgrade_tasks || []) {
        const row = document.createElement("tr");
        for (const value of [
          `${task.role} ${task.node_id}`,
          `${task.from_version || "—"} → ${task.target_version}`,
          `${t(task.stage)} · ${t("批次")} ${t(data.upgrade_batches?.[task.batch_id]?.stage) || "—"}${data.upgrade_batches?.[task.batch_id]?.error ? ": " + data.upgrade_batches[task.batch_id].error : ""} · ${task.updated_at}`,
          task.error || "",
        ]) row.append(el("td", value));
        const action = el("td");
        if (resumeTasks.has(task.id)) {
          const form = document.createElement("form");
          form.method = "post";
          form.action = `/admin/upgrades/${encodeURIComponent(task.id)}/resume`;
          const hidden = document.createElement("input");
          hidden.type = "hidden";
          hidden.name = "csrf";
          hidden.value = csrf || "";
          form.append(hidden, el("button", t("重新检查并继续")));
          action.append(form);
        }
        if (["pending", "preparing"].includes(task.stage)) {
          const form = document.createElement("form");
          form.method = "post";
          form.action = `/admin/upgrades/${encodeURIComponent(task.id)}/cancel`;
          const hidden = document.createElement("input");
          hidden.type = "hidden";
          hidden.name = "csrf";
          hidden.value = csrf || "";
          const button = el("button", t("取消"));
          form.append(hidden, button);
          action.append(form);
        }
        row.append(action);
        tasksBody.append(row);
      }
      if (!tasksBody.children.length) {
        const row = document.createElement("tr");
        const cell = el("td", t("暂无升级任务"));
        cell.colSpan = 5;
        row.append(cell);
        tasksBody.append(row);
      }
    }
    function labelResponsiveTables(root = document) {
      $$("table", root).forEach((table) => {
        const labels = [...(table.tHead?.rows[0]?.cells || [])].map((cell) => t(cell.textContent.trim()));
        for (const body of table.tBodies)
          for (const row of body.rows)
            [...row.cells].forEach((cell, index) => {
              if (cell.colSpan === 1) cell.dataset.label = labels[index] || "";
            });
      });
    }
    function setAssignmentTab(tab) {
      const section = $("#assign");
      section.dataset.assignmentTab = tab;
      $$("[data-assignment-tab]", section).forEach((button) => {
        const selected = button.dataset.assignmentTab === tab;
        button.setAttribute("aria-selected", String(selected));
        button.tabIndex = selected ? 0 : -1;
        document.getElementById(button.getAttribute("aria-controls")).hidden = !selected;
      });
    }
    function enhanceAssignmentTabs(section) {
      const tabs = $(".assignment-tabs", section);
      if (!tabs || tabs.dataset.enhanced) return;
      tabs.dataset.enhanced = "true";
      tabs.hidden = false;
      const buttons = $$("[data-assignment-tab]", tabs);
      buttons.forEach((button, index) => {
        button.addEventListener("click", () => setAssignmentTab(button.dataset.assignmentTab));
        button.addEventListener("keydown", (event) => {
          let next;
          if (event.key === "ArrowRight") next = buttons[(index + 1) % buttons.length];
          else if (event.key === "ArrowLeft") next = buttons[(index + buttons.length - 1) % buttons.length];
          else if (event.key === "Home") next = buttons[0];
          else if (event.key === "End") next = buttons[buttons.length - 1];
          if (next) {
            event.preventDefault();
            setAssignmentTab(next.dataset.assignmentTab);
            next.focus();
          }
        });
      });
      setAssignmentTab(section.dataset.assignmentTab || "agent");
    }
    function updatePendingChooser(form) {
      if (!data) return;
      const candidates = pendingConnectionCandidates(data);
      const field = $("fieldset", form);
      const rows = new Map($$(".pending-choice", form).map((row) => [$("input", row).value, row]));
      const query = $(".pending-search", form).value.trim().toLowerCase();
      const agentFilter = $(".pending-agent", form).value;
      const gatewayFilter = $(".pending-gateway", form).value;
      const manualTarget = form.elements.reality_target.value.trim();
      const plan = { created: 0, reenabled: 0 };
      let selected = 0, visible = 0, available = 0, needsTarget = false;
      for (const candidate of candidates) {
        let row = rows.get(candidate.value);
        if (!row) {
          row = el("label", null, "pending-choice");
          const checkbox = el("input");
          checkbox.type = "checkbox";
          checkbox.name = "connection";
          checkbox.value = candidate.value;
          row.append(checkbox, el("span"), el("small", "", "choice-state"));
        }
        rows.delete(candidate.value);
        row.dataset.agentId = candidate.agent.id;
        row.dataset.gatewayId = candidate.gateway.id;
        const title = `${candidate.gateway.name} → ${candidate.agent.name} (${candidate.gateway.host || t("无公网地址")})`;
        $("span", row).textContent = title;
        const target = candidate.gateway.target || manualTarget || defaultRealityTarget(candidate.gateway.region);
        $(".choice-state", row).textContent = `${t(candidate.state)} · ${target || t("需设置所在地或填写 target")}${candidate.available ? "" : t(" · 节点停用或缺少公网地址")}`;
        const checkbox = $("input", row);
        checkbox.disabled = !candidate.available;
        if (checkbox.disabled) checkbox.checked = false;
        row.hidden = !title.toLowerCase().includes(query) ||
          (agentFilter && candidate.agent.id !== agentFilter) ||
          (gatewayFilter && candidate.gateway.id !== gatewayFilter);
        if (!row.hidden) visible++;
        if (!row.hidden && candidate.available) available++;
        if (checkbox.checked) {
          selected++;
          plan[candidate.action]++;
          needsTarget ||= candidate.needsTarget;
        }
        if (!row.isConnected) field.append(row);
      }
      rows.forEach((row) => row.remove());
      const empty = $(".pending-empty", form);
      empty.hidden = visible > 0;
      empty.textContent = candidates.length ? t("当前筛选下没有未开启连接。") : t("所有连接均已开启，或尚未添加 Gateway / Agent。");
      $(".selection-summary", form).textContent = t("未开启 {0} · 筛选结果 {1} / 可选 {2} · 已选 {3} · 新增 {4} · 重新启用 {5}。清除选择仅影响本次提交。", candidates.length, visible, available, selected, plan.created, plan.reenabled);
      form.elements.reality_target.required = needsTarget;
      $("button[type=submit]", form).disabled = !selected || submitting;
      $("[data-pending-select]", form).disabled = !available || submitting;
      $("[data-pending-clear]", form).disabled = !selected || submitting;
    }
    function enhancePendingChooser(form) {
      $(".pending-tools", form).hidden = false;
      $("button[type=submit]", form).disabled = true;
      $("[data-pending-select]", form).addEventListener("click", () => {
        $$(".pending-choice:not([hidden]) input:not(:disabled)", form).forEach((c) => (c.checked = true));
        form.dataset.dirty = "true";
        updatePendingChooser(form);
      });
      $("[data-pending-clear]", form).addEventListener("click", () => {
        $$(".pending-choice input", form).forEach((c) => (c.checked = false));
        form.dataset.dirty = "true";
        updatePendingChooser(form);
      });
      form.addEventListener("input", () => updatePendingChooser(form));
      form.addEventListener("change", () => updatePendingChooser(form));
    }
    function updateChooser(form) {
      if (!data) return;
      if (form.classList.contains("pending-chooser")) {
        updatePendingChooser(form);
        return;
      }
      const kind = form.closest("section").id,
        owner = form.elements[kind === "assign" ? "agent_id" : "user_id"].value,
        query = $(".selection-search", form).value.toLowerCase(),
        filter = $(".selection-filter", form).value,
        group = $(".selection-group", form).value;
      const checkboxes = $$("fieldset input[type=checkbox]", form),
        ownerActive =
          kind === "assign"
            ? data.nodes.some((n) => n.id === owner && n.enabled)
            : data.users.some((u) => u.id === owner && u.enabled);
      checkboxes.forEach((c) => {
        const row = c.closest("label"),
          node =
            kind === "assign" ? data.nodes.find((n) => n.id === c.value) : null,
          route =
            kind === "subscribe"
              ? data.routes.find((r) => r.id === c.value)
              : null;
        const selectable =
          ownerActive &&
          (kind === "assign"
            ? node?.enabled && !!node.host
            : route?.selectable);
        c.disabled = !selectable;
        if (!selectable) c.checked = false;
        const existing =
          kind === "assign"
            ? data.routes.find(
                (r) => r.agent_id === owner && r.gateway_id === c.value,
              )
            : data.grants.find(
                (g) => g.user_id === owner && g.route_id === c.value,
              );
        const active =
          kind === "assign"
            ? existing?.enabled &&
              data.links.some((l) => l.route_id === existing.id && l.enabled)
            : existing?.enabled;
        const label = !selectable
          ? t("不可用")
          : existing
            ? active
              ? t("已存在")
              : t("将重新启用")
            : t("可新增");
        const target =
          kind === "assign"
            ? node?.target ||
              form.elements.reality_target.value.trim() ||
              defaultRealityTarget(node?.region)
            : "";
        $(".choice-state", row).textContent =
          kind === "assign"
            ? `${label} · ${target || t("需设置所在地或填写 target")}`
            : label;
        row.hidden =
          !row.dataset.search.includes(query) ||
          (filter === "new" && !!existing) ||
          (filter === "active" && !active) ||
          (filter === "available" && !selectable) ||
          (group && kind === "subscribe" && route?.gateway_id !== group);
      });
      const selected = checkboxes
          .filter((c) => c.checked && !c.disabled)
          .map((c) => c.value),
        plan = selectionPlan(kind, owner, selected, data);
      $(".selection-summary", form).textContent =
        t("已选 {0} / 可选 {1} · 新增 {2} · 重新启用 {3} · 已存在 {4}。清除选择仅影响本次提交。", selected.length, checkboxes.filter((c) => !c.disabled).length, plan.created, plan.reenabled, plan.unchanged);
      $("button[type=submit]", form).disabled = !selected.length || submitting;
      if (kind === "assign") {
        const targets = new Set(
          selected.map((id) => {
            const node = data.nodes.find((n) => n.id === id);
            return node?.target || defaultRealityTarget(node?.region);
          }),
        );
        form.elements.reality_target.placeholder =
          targets.size === 1 && !targets.has("")
            ? t("默认：{0}", [...targets][0])
            : t("留空：按各 Gateway 所在地使用默认值");
      }
    }
    function enhance(root = document) {
      $$("time[datetime]", root).forEach((time) => {
        const value = new Date(time.dateTime);
        if (!Number.isNaN(value.getTime())) {
          time.textContent = value.toLocaleString(i18n.locale());
          time.title = t("浏览器本地时间");
        }
      });
      const assign = root.id === "assign" ? root : $("#assign", root);
      if (assign) enhanceAssignmentTabs(assign);
      $$("section[data-page] > form.stack, section[data-page] > .assignment-panel > form.stack", root).forEach((form) => {
        const section = form.closest("section");
        if (form.dataset.enhanced) return;
        form.dataset.enhanced = "true";
        if (form.classList.contains("pending-chooser")) {
          enhancePendingChooser(form);
        } else if (["assign", "subscribe"].includes(section.id)) {
          form.classList.add("chooser");
          const field = $("fieldset", form),
            tools = el("div", null, "selection-tools"),
            search = el("input", null, "selection-search");
          search.type = "search";
          search.placeholder = t("搜索名称或地址");
          search.setAttribute("aria-label", t("搜索可选项"));
          const filter = el("select", null, "selection-filter");
          filter.setAttribute("aria-label", t("选择状态筛选"));
          [
            ["all", t("全部")],
            ["available", t("可选")],
            ["new", t("未配置")],
            ["active", t("已存在")],
          ].forEach(([v, t]) => {
            const o = el("option", t);
            o.value = v;
            filter.append(o);
          });
          const group = el("select", null, "selection-group");
          group.setAttribute("aria-label", t("按 Gateway 分组"));
          const all = el("option", t("所有 Gateway"));
          all.value = "";
          group.append(all);
          if (section.id === "subscribe") {
            data?.nodes
              .filter((n) => n.role === "Gateway")
              .forEach((n) => {
                const o = el("option", n.name);
                o.value = n.id;
                group.append(o);
              });
          } else group.hidden = true;
          const select = el("button", t("全选筛选结果")),
            clear = el("button", t("清除选择"));
          select.type = clear.type = "button";
          tools.append(search, filter, group, select, clear);
          field.prepend(tools);
          $$("fieldset>label", form).forEach((label) => {
            label.dataset.search = label.textContent.toLowerCase();
            label.append(el("small", "", "choice-state"));
          });
          const summary = el("div", null, "selection-summary");
          summary.setAttribute("aria-live", "polite");
          field.after(summary);
          const submit = $("button:not([type])", form);
          if (submit) submit.type = "submit";
          select.addEventListener("click", () => {
            $$(
              "fieldset>label:not([hidden]) input:not(:disabled)",
              form,
            ).forEach((c) => (c.checked = true));
            form.dataset.dirty = "true";
            updateChooser(form);
          });
          clear.addEventListener("click", () => {
            $$("fieldset input[type=checkbox]", form).forEach(
              (c) => (c.checked = false),
            );
            form.dataset.dirty = "true";
            updateChooser(form);
          });
          form.addEventListener("input", () => updateChooser(form));
          form.addEventListener("change", () => updateChooser(form));
        } else if (!["install"].includes(section.id)) {
          const details = el("details", null, "create-details"),
            summary = el(
              "summary",
              `＋ ${$("button", form)?.textContent || t("创建")}`,
            );
          form.before(details);
          details.append(summary, form);
        }
        const advancedNames = [
          "vmess_port",
          "vmess_path",
          "vmess_host",
          "allowed_cidrs",
          "denied_cidrs",
          "link_http_host",
          "tls_server_name",
          "http_host",
          "tls_verify",
          "priority",
          "weight",
          "connections",
          "max_streams",
        ];
        if (
          ["create-route", "wizard-gateway", "gateways", "agents", "links"].includes(section.id)
        ) {
          const advanced = el("details", null, "advanced"),
            inner = el("div");
          advanced.append(el("summary", t("高级设置")), inner);
          $$(":scope > label", form)
            .filter((l) => advancedNames.includes($("input", l)?.name))
            .forEach((l) => inner.append(l));
          if (inner.childElementCount)
            $(":scope > button", form).before(advanced);
        }
      });
      $$("section[data-page]>table", root).forEach((table) => {
        if (table.dataset.enhanced) return;
        table.dataset.enhanced = "true";
        if (table.closest("section").id === "readiness") return;
        const search = el("input", null, "table-search");
        search.type = "search";
        search.placeholder = t("搜索此列表");
        search.setAttribute(
          "aria-label",
          t("{0} 搜索", $("h2", table.closest("section")).textContent),
        );
        table.before(search);
        search.addEventListener("input", () =>
          $$("tbody tr", table).forEach(
            (row) =>
              (row.hidden = !row.textContent
                .toLowerCase()
                .includes(search.value.toLowerCase())),
          ),
        );
      });
      $$("details form button", root).forEach((b) => {
        if (/Delete|删除/.test(b.textContent)) b.classList.add("danger");
      });
      const quick = $("#create-route form", root);
      if (quick) {
        const region = quick.elements.region,
          target = quick.elements.reality_target;
        let previousDefault = defaultRealityTarget(region.value);
        region.addEventListener("change", () => {
          const nextDefault = defaultRealityTarget(region.value);
          if (!target.value.trim() || target.value === previousDefault)
            target.value = nextDefault;
          previousDefault = nextDefault;
          target.placeholder = nextDefault || t("手动填写 REALITY target");
        });
        const host = quick.elements.public_host,
          url = quick.elements.link_url;
        host.addEventListener("input", () => {
          if (!url.dataset.manual) {
            const h = host.value.trim();
            url.value = h
              ? `reality://${h.includes(":") && !h.startsWith("[") ? "[" + h + "]" : h}:8443/tunnel`
              : "";
          }
        });
        url.addEventListener("input", () => (url.dataset.manual = "true"));
      }
      const install = $("#install form", root);
      if (install) {
        const role = install.elements.role,
          node = install.elements.node_id;
        const sync = () => {
          [...node.options].forEach((o) => {
            const match =
              o.parentElement.label?.toLowerCase() ===
              (role.value === "gateway" ? "gateways" : "agents");
            o.hidden = !match;
            o.disabled = !match;
          });
          if (node.selectedOptions[0]?.disabled)
            node.value =
              [...node.options].find((o) => !o.disabled)?.value || "";
        };
        role.addEventListener("change", sync);
        sync();
      }
      $$("form", root).forEach((form) => {
        form.addEventListener(
          "invalid",
          (e) => {
            for (
              let d = e.target.closest("details");
              d;
              d = d.parentElement.closest("details")
            )
              d.open = true;
          },
          true,
        );
      });
      if (data) {
        $$(".chooser", root).forEach(updateChooser);
      }
    }
    async function refresh() {
      if (refreshBusy || submitting) return;
      refreshBusy = true;
      try {
        const response = await fetch("/admin/dashboard", {
          cache: "no-store",
          signal: AbortSignal.timeout(12000),
          headers: { Accept: "application/json" },
        });
        if (
          response.redirected ||
          !response.ok ||
          !response.headers.get("content-type")?.includes("application/json")
        )
          throw new Error(t("会话已过期或服务不可用，请重新登录后刷新。"));
        data = await response.json();
        renderMetrics();
        renderMap();
        renderRouteOverview();
        renderUpstreamProbes();
        renderProgress();
        renderHistory();
        renderUsage();
        renderOperations();
        updateLiveTables();
        renderNodeUpgrades();
        labelResponsiveTables();
        $$(".chooser").forEach((f) => {
          if (f.classList.contains("pending-chooser")) {
            updatePendingChooser(f);
            return;
          }
          const group = $(".selection-group", f),
            old = group.value;
          if (f.closest("section").id === "subscribe") {
            const desired = data.nodes.filter((n) => n.role === "Gateway");
            if (group.options.length !== desired.length + 1) {
              group.replaceChildren(
                new Option(t("所有 Gateway"), ""),
                ...desired.map((n) => new Option(n.name, n.id)),
              );
              group.value = old;
            }
          }
          updateChooser(f);
        });
        lastSuccessfulRefresh = new Date(data.at);
        $("#refresh-note").textContent =
          t("已更新 {0} · 每 15 秒自动刷新", lastSuccessfulRefresh.toLocaleTimeString(i18n.locale()));
        $("#refresh-note").classList.remove("error");
        $("#stale-banner").hidden = true;
      } catch (error) {
        $("#refresh-note").textContent =
          t("刷新失败 · 上次成功 {0}", lastSuccessfulRefresh ? lastSuccessfulRefresh.toLocaleTimeString(i18n.locale()) : t("无"));
        $("#refresh-note").classList.add("error");
        $("#stale-banner").textContent = t("状态数据可能已过期：{0}。上次成功刷新：{1}。", error.message, lastSuccessfulRefresh ? lastSuccessfulRefresh.toLocaleString(i18n.locale()) : t("无"));
        $("#stale-banner").hidden = false;
      } finally {
        refreshBusy = false;
      }
    }
    async function replaceSections(html, submittedForm) {
      const doc = new DOMParser().parseFromString(html, "text/html");
      if (!$("#page-title", doc)) throw new Error(t("会话已过期，请重新登录。"));
      const originID = submittedForm.closest("section")?.id;
      delete submittedForm.dataset.dirty;
      clearFormError(submittedForm);
      $$("section[data-page]")
        .filter(
          (s) =>
            s.id !== "overview" &&
            [
              "release",
              "create-route",
              "readiness",
              "users",
              "gateways",
              "agents",
              "wizard-gateway",
              "wizard-agent",
              "routes",
              "upstreams",
              "assignments",
              "links",
              "grants",
              "install",
              "quick-install",
              "assign",
              "subscribe",
              "upgrade",
            ].includes(s.id),
        )
        .forEach((section) => {
          if (
            $("form[data-dirty]", section) ||
            (!section.contains(submittedForm) && $("details[open]", section))
          )
            return;
          const incoming = doc.getElementById(section.id);
          if (!incoming) return;
          const searches = $$(".table-search", section).map((s) => s.value);
          section.replaceChildren(
            ...[...incoming.childNodes].map((n) =>
              document.importNode(n, true),
            ),
          );
          i18n.apply(section);
          enhance(section);
          labelResponsiveTables(section);
          $$(".table-search", section).forEach((s, i) => {
            s.value = searches[i] || "";
            s.dispatchEvent(new Event("input"));
          });
        });
      const message = submittedForm.action.endsWith("/probe")
        ? t("测试请求已提交；请等待 Gateway 回传结果。")
        : t($("#notice", doc)?.textContent.trim()) || t("操作成功。正在跟踪配置与运行状态。");
      notice(message);
      const section = originID && document.getElementById(originID);
      if (section) {
        $(".form-success", section)?.remove();
        const feedback = el("p", message, "form-success");
        feedback.setAttribute("role", "status");
        $("h2", section)?.after(feedback);
      }
      navigate();
    }
    document.addEventListener("input", (event) => {
      if (
        event.target.matches(
          "input[type=search],.selection-filter,.selection-group,.pending-agent,.pending-gateway",
        )
      )
        return;
      event.target.closest("form")?.setAttribute("data-dirty", "true");
    });
    document.addEventListener("change", (event) => {
      if (
        event.target.closest("form") &&
        !event.target.matches(".selection-filter,.selection-group,.pending-agent,.pending-gateway")
      )
        event.target.closest("form").dataset.dirty = "true";
    });
    document.addEventListener("submit", async (event) => {
      const form = event.target;
      if (!form.action.includes("/admin/")) return;
      event.preventDefault();
      if (submitting) return;
      submitting = true;
      clearFormError(form);
      $(".form-success", form.closest("section"))?.remove();
      const buttons = $$("button", form);
      buttons.forEach((b) => (b.disabled = true));
      try {
        const response = await fetch(form.action, {
            method: "POST",
            body: new URLSearchParams(new FormData(form)),
            headers: { Accept: "text/html, text/plain" },
          }),
          text = await response.text();
        if (!response.ok) throw new Error(t(text.slice(0, 700).trim()));
        if (response.headers.get("content-type")?.includes("text/plain")) {
          const content = el("div"),
            pre = el("pre", text),
            copy = el("button", t("复制安装说明"));
          copy.type = "button";
          copy.dataset.copy = text;
          content.append(
            el("p", t("在对应主机执行安装命令；完成后面板自动跟踪上线状态。")),
            copy,
            pre,
          );
          showDetail(t("安装选项 · 一次性令牌"), content);
          delete form.dataset.dirty;
          notice(t("已生成安装选项。关闭窗口前保存所需令牌。"));
        } else await replaceSections(text, form);
      } catch (error) {
        const message = t("操作未完成：{0}。输入已保留；网络中断时请先检查操作记录，避免重复提交。", error.message);
        notice(message, true);
        showFormError(form, message);
      } finally {
        submitting = false;
        buttons.forEach((b) => (b.disabled = false));
        await refresh();
      }
    });
    document.addEventListener("click", async (event) => {
      const button = event.target.closest("button[data-copy]");
      if (!button) return;
      try {
        await navigator.clipboard.writeText(button.dataset.copy);
        const before = button.textContent;
        button.textContent = t("已复制");
        setTimeout(() => (button.textContent = before), 2000);
      } catch {
        button.previousElementSibling?.select?.();
        notice(t("无法访问剪贴板，请手动选择并复制。"), true);
      }
    });
    $("#close-detail").addEventListener("click", () => {
      $("#detail-dialog").close();
      $("#detail-body").replaceChildren();
    });
    $("#detail-dialog").addEventListener("close", () =>
      $("#detail-body").replaceChildren(),
    );
    $("#refresh-status").addEventListener("click", refresh);
    $("#history-link").addEventListener(
      "change",
      () => data && renderHistory(),
    );
    $("#map-search").addEventListener("input", renderMap);
    $("#map-state").addEventListener("change", renderMap);
    $("#route-search").addEventListener("input", renderRouteOverview);
    $("#route-state").addEventListener("change", renderRouteOverview);
    $$("[data-view]").forEach((b) =>
      b.addEventListener("click", () => {
        mapView = b.dataset.view;
        $$("[data-view]").forEach((other) =>
          other.setAttribute("aria-pressed", String(other === b)),
        );
        renderMap();
      }),
    );
    window.addEventListener("hashchange", navigate);
    i18n.apply();
    enhance();
    navigate();
    refresh();
    setInterval(() => {
      if (document.visibilityState === "visible") refresh();
    }, 15000);
  })();

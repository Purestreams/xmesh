"use strict";

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
      sample.generation === previous.generation &&
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
if (typeof module !== "undefined")
  module.exports = {
    sampleRates,
    selectionPlan,
    formatBytes,
    defaultRealityTarget,
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
      overview: ["总览", "查看网络健康状况与需要处理的事项。"],
      network: ["节点与线路", "管理入口、出口和连接策略。"],
      users: ["用户与订阅", "选择授权线路，跟踪配置发布并复制订阅。"],
      deploy: ["部署向导", "新建或复用节点，跟踪从安装到可用的每一步。"],
      maintenance: ["系统维护", "版本升级、发布源、安装令牌与操作记录。"],
    };
    const stages = {
      ready: "可用",
      pending: "待就绪",
      offline: "离线",
      disabled: "停用",
    };
    let data = null,
      refreshBusy = false,
      submitting = false,
      mapView = "topology";
    const nodeName = (id) => data?.nodes.find((n) => n.id === id)?.name || id;
    const routeName = (route) =>
      `${nodeName(route.gateway_id)} / ${nodeName(route.agent_id)}`;
    function notice(text, error = false) {
      $("#notice").textContent = text;
      $("#notice").classList.toggle("error", error);
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
      const values = {
        角色: node.role,
        地址: node.host || "主动连接 Gateway",
        状态: !node.enabled ? "停用" : node.status.online ? "在线" : "离线",
        配置版本: `${node.status.applied_version} / ${node.desired}`,
        软件版本: node.status.binary_version || "尚未上报",
        最后上报:
          node.status.last_seen && !node.status.last_seen.startsWith("0001")
            ? new Date(node.status.last_seen).toLocaleString()
            : "尚未上报",
        下一步: node.next,
        最近错误:
          node.status.apply_error ||
          node.status.last_error ||
          node.status.xray_error ||
          "无",
      };
      Object.entries(values).forEach(([k, v]) =>
        dl.append(el("dt", k), el("dd", v)),
      );
      body.append(dl);
      const link = el("a", "打开节点管理");
      link.href = node.role === "Gateway" ? "#gateways" : "#agents";
      link.addEventListener("click", () => $("#detail-dialog").close());
      body.append(link);
      showDetail(node.name, body);
    }
    function showRoute(id) {
      const route = data.routes.find((r) => r.id === id);
      if (!route) return;
      const body = el("div");
      body.append(badge(route.state));
      data.links
        .filter((l) => l.route_id === id)
        .forEach((link) => {
          const p = el("p");
          p.append(
            el("strong", link.name),
            el(
              "small",
              `RTT ${link.status.rtt_millis || "—"} ms · ${link.status.active_streams || 0} streams · ↑ ${formatBytes(link.status.upload_bytes)} ↓ ${formatBytes(link.status.download_bytes)}`,
            ),
          );
          if (link.status.last_error)
            p.append(el("small", link.status.last_error, "error"));
          body.append(p);
        });
      const a = el("a", "查看链路配置");
      a.href = "#links";
      a.addEventListener("click", () => $("#detail-dialog").close());
      body.append(a);
      showDetail(routeName(route), body);
    }
    function renderMetrics() {
      const ready = data.routes.filter((r) => r.state === "ready").length,
        online = data.nodes.filter((n) => n.enabled && n.status.online).length;
      const pending = data.nodes.filter(
        (n) =>
          n.enabled &&
          (!n.enrolled ||
            n.status.applied_version < n.desired ||
            n.status.apply_error),
      ).length;
      const items = [
        [
          "可用线路",
          `${ready} / ${data.routes.length}`,
          "两端配置与链路均已就绪",
          "good",
        ],
        [
          "在线节点",
          `${online} / ${data.nodes.length}`,
          "Gateway 与 Agent 实时心跳",
          "",
        ],
        [
          "待完成配置",
          pending,
          "未安装、待应用或应用失败",
          pending ? "warn" : "good",
        ],
        [
          "已发布授权",
          data.grants.filter(
            (g) =>
              g.enabled &&
              g.published &&
              data.users.some((u) => u.id === g.user_id && u.enabled) &&
              data.routes.some((r) => r.id === g.route_id && r.selectable),
          ).length,
          "线路可用状态请查看拓扑",
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
    function filteredMap() {
      const query = $("#map-search").value.toLowerCase(),
        state = $("#map-state").value;
      const matches = (n) =>
        `${n.name} ${n.host}`.toLowerCase().includes(query);
      const ids = new Set(data.nodes.filter(matches).map((n) => n.id));
      const routes = data.routes.filter(
        (r) =>
          (!query || ids.has(r.gateway_id) || ids.has(r.agent_id)) &&
          (state === "all" || r.state === state),
      );
      const connected = new Set(
        routes.flatMap((r) => [r.gateway_id, r.agent_id]),
      );
      return {
        routes,
        nodes: data.nodes.filter(
          (n) => connected.has(n.id) || (state === "all" && matches(n)),
        ),
      };
    }
    function renderMap() {
      if (!data) return;
      const { nodes, routes } = filteredMap(),
        gateways = nodes.filter((n) => n.role === "Gateway"),
        agents = nodes.filter((n) => n.role === "Agent");
      $("#topology").hidden = mapView !== "topology";
      $("#matrix").hidden = mapView !== "matrix";
      if (!nodes.length) {
        $("#topology").replaceChildren(
          el("p", "没有匹配的节点。前往部署向导创建第一条线路。", "empty"),
        );
        $("#matrix").replaceChildren(el("p", "没有匹配的节点。", "empty"));
        return;
      }
      const height = Math.max(
          210,
          Math.max(gateways.length, agents.length) * 100 + 70,
        ),
        svg = svgEl("svg", {
          viewBox: `0 0 900 ${height}`,
          class: "network",
          role: "img",
          "aria-label": "Gateway 与 Agent 网络拓扑",
        });
      svg.append(
        svgEl(
          "text",
          { x: 80, y: 30, class: "subtext" },
          "GATEWAYS / 公网入口",
        ),
        svgEl("text", { x: 620, y: 30, class: "subtext" }, "AGENTS / 网络出口"),
      );
      const positions = new Map();
      gateways.forEach((n, i) =>
        positions.set(n.id, { x: 65, y: 55 + i * 100 }),
      );
      agents.forEach((n, i) =>
        positions.set(n.id, { x: 610, y: 55 + i * 100 }),
      );
      routes.forEach((route, index) => {
        const a = positions.get(route.gateway_id),
          b = positions.get(route.agent_id);
        if (!a || !b) return;
        const link = data.links.find(
            (l) => l.route_id === route.id && l.enabled,
          ),
          label = `${stages[route.state]}${link?.status.rtt_millis ? " · " + link.status.rtt_millis.toFixed(1) + " ms" : ""}`;
        const path = svgEl("path", {
          d: `M ${a.x + 225} ${a.y + 31} C 450 ${a.y + 31}, 450 ${b.y + 31}, ${b.x} ${b.y + 31}`,
          class: `edge ${route.state}`,
        });
        path.append(svgEl("title", {}, `${routeName(route)} · ${label}`));
        svg.append(path);
        const t = svgEl(
          "text",
          {
            x: 360 + (index % 3) * 42,
            y: (a.y + b.y) / 2 + 24,
            class: "route-label",
            tabindex: 0,
            role: "button",
          },
          label,
        );
        t.addEventListener("click", () => showRoute(route.id));
        t.addEventListener("keydown", (e) => {
          if (e.key === "Enter") showRoute(route.id);
        });
        svg.append(t);
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
            fill: !n.enabled
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
      $("#topology").replaceChildren(svg);
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
              route ? (visible ? stages[route.state] : "已筛除") : "+ 绑定",
              `matrix-cell ${route?.state || "unassigned"}`,
            );
          button.type = "button";
          button.disabled = route ? !visible : !g.enabled || !a.enabled;
          button.addEventListener("click", () => {
            if (route) showRoute(route.id);
            else {
              location.hash = "assign";
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
      const cards = data.nodes.map((n) => {
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
          linked = data.routes.some(
            (r) =>
              (r.gateway_id === n.id || r.agent_id === n.id) &&
              r.state === "ready",
          );
        [
          ["已创建", true],
          ["已安装", n.enrolled],
          ["在线", online],
          ["配置应用", applied],
          ["运行就绪", ready],
          ["线路可用", linked],
        ].forEach(([label, done]) =>
          steps.append(
            el("li", `${done ? "✓ " : ""}${label}`, done ? "done" : ""),
          ),
        );
        c.append(
          steps,
          el("p", n.enabled ? n.next : "节点已停用。启用后继续部署。"),
        );
        return c;
      });
      $("#deployment-progress").replaceChildren(
        ...(cards.length
          ? cards
          : [el("p", "先创建线路或节点，再按照下方步骤完成安装。", "empty")]),
      );
      const todos = data.nodes
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
          text: n.next,
          target: !n.enrolled ? "quick-install" : "readiness",
        }));
      data.routes
        .filter((r) => r.state !== "ready" && r.state !== "disabled")
        .forEach((r) =>
          todos.push({
            title: routeName(r),
            text: `线路${stages[r.state]}：检查两端配置、Link 与 REALITY 连接。`,
            target: "links",
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
            text: `${u.publication} · 等待 Gateway 应用配置后发布。`,
            target: "subscribe",
          }),
        );
      $("#todo-list").replaceChildren(
        ...(todos.length
          ? todos.map((t) => {
              const row = el("div", null, "todo"),
                text = el("div"),
                a = el("a", "查看 →");
              a.href = `#${t.target}`;
              text.append(el("strong", t.title), el("p", t.text));
              row.append(text, a);
              return row;
            })
          : [
              el(
                "p",
                data.nodes.length
                  ? "当前没有待处理异常。"
                  : "创建第一条线路，开始连接你的网络。",
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
          el("p", "尚无足够样本；不会填补缺失或重启期间的数据。", "empty"),
        );
        return box;
      }
      const max = Math.max(...valid, 1),
        svg = svgEl("svg", {
          viewBox: "0 0 500 160",
          role: "img",
          "aria-label": `${title}，最高 ${format(max)}`,
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
        el("span", new Date(from).toLocaleTimeString()),
        el("span", `最高 ${format(max)}`),
        el("span", new Date(to).toLocaleTimeString()),
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
          "吞吐速率 · 蓝色上传 / 绿色下载",
          samples,
          ["uploadRate", "downloadRate"],
          (v) => formatBytes(v) + "/s",
        ),
        chart("往返延迟 RTT", samples, ["rtt"], (v) => v.toFixed(1) + " ms"),
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
      ranking.append(el("h3", "实时 RTT · 最高 5 条链路"));
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
    function renderOperations() {
      const upgrade = data.upgrade || {};
      $("#upgrade-progress").textContent =
        `Controller ${upgrade.Version || "—"} · ${upgrade.State || "暂无升级任务"}${upgrade.At ? " · " + upgrade.At : ""}`;
      const upgradeButton = $("#upgrade form button");
      if (upgradeButton)
        upgradeButton.disabled = ["queued", "running"].includes(upgrade.State);
      const table = el("table"),
        head = el("tr");
      ["时间", "操作人", "操作", "请求结果"].forEach((v) =>
        head.append(el("th", v)),
      );
      const thead = el("thead");
      thead.append(head);
      table.append(thead);
      const body = el("tbody");
      [...(data.operations || [])].reverse().forEach((o) => {
        const tr = el("tr");
        [
          new Date(o.at).toLocaleString(),
          o.actor,
          o.action,
          o.status < 400 ? "成功 / 已接受" : `失败 (${o.status})`,
        ].forEach((v) => tr.append(el("td", v)));
        body.append(tr);
      });
      table.append(body);
      $("#operation-list").replaceChildren(
        data.operations?.length ? table : el("p", "暂无操作记录。", "empty"),
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
                ? "停用"
                : node.status.apply_error
                  ? "应用失败"
                  : node.status.applied_version < node.desired
                    ? "待应用"
                    : "已应用",
            ),
            el(
              "small",
              `配置 ${node.status.applied_version} / ${node.desired}`,
            ),
          );
          runtime.replaceChildren(
            badge(
              node.status.online ? "ready" : "offline",
              node.status.online ? "在线" : "离线",
            ),
            el(
              "small",
              `隧道 ${node.status.tunnel_connections} · TCP ${node.status.tcp_connections} · UDP ${node.status.udp_associations}`,
            ),
          );
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
              ? "停用"
              : st.online
                ? st.ready
                  ? "上报就绪"
                  : "待就绪"
                : "离线",
          ),
          el(
            "small",
            `${st.connections || 0} connections · ${st.active_streams || 0} streams · RTT ${st.rtt_millis || "—"} ms`,
          ),
          el(
            "small",
            `↑ ${formatBytes(st.upload_bytes)} ↓ ${formatBytes(st.download_bytes)}`,
          ),
          el(
            "small",
            `写阻塞 ${st.write_stalls || 0} 次 / ${st.write_blocked_millis || 0} ms · 丢弃 ${st.queue_drops || 0}`,
          ),
          el("small", st.last_error || "", "error"),
        );
      });
      $$("#grants>table tbody tr").forEach((row) => {
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
          el("small", grant.published ? "已发布" : "尚未发布"),
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
          ...data.nodes.map((n) => {
            const row = el("tr");
            [
              n.role + " " + n.name,
              n.status.binary_version || "—",
              n.enrolled ? "已安装" : "待安装",
              n.enabled && n.status.online ? "在线" : "离线",
              n.status.apply_error ||
                (n.status.applied_version >= n.desired ? "已应用" : "待应用"),
              n.status.ready && (n.role !== "Gateway" || n.status.xray_ready)
                ? "就绪"
                : "待就绪",
              n.next,
            ].forEach((v) => row.append(el("td", v)));
            return row;
          }),
        );
      $$("#subscribe p").forEach((p) => {
        const input = $("input[readonly]", p);
        if (!input) return;
        const user = data.users.find(
          (u) =>
            input.getAttribute("aria-label") ===
            `Subscription URL for ${u.name}`,
        );
        if (user && p.firstChild?.nodeType === Node.TEXT_NODE)
          p.firstChild.textContent = `${user.name} (${user.publication}): `;
      });
    }
    function renderNodeUpgrades() {
      const section = $("#node-upgrades");
      if (!section || !data) return;
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
          row.cells[1].textContent = `${up?.State || "未配对"} · ${up?.Mode || "—"}/${up?.Arch || "—"} · ${up?.Version || "—"}`;
        }
      }
      if (!tasksBody) return;
      tasksBody.replaceChildren();
      for (const task of data.upgrade_tasks || []) {
        const row = document.createElement("tr");
        for (const value of [
          `${task.role} ${task.node_id}`,
          `${task.from_version || "—"} → ${task.target_version}`,
          `${task.stage} · batch ${data.upgrade_batches?.[task.batch_id]?.stage || "—"}${data.upgrade_batches?.[task.batch_id]?.error ? ": " + data.upgrade_batches[task.batch_id].error : ""} · ${task.updated_at}`,
          task.error || "",
        ]) row.append(el("td", value));
        const action = el("td");
        if (["pending", "preparing"].includes(task.stage)) {
          const form = document.createElement("form");
          form.method = "post";
          form.action = `/admin/upgrades/${encodeURIComponent(task.id)}/cancel`;
          const hidden = document.createElement("input");
          hidden.type = "hidden";
          hidden.name = "csrf";
          hidden.value = csrf || "";
          const button = el("button", "取消");
          form.append(hidden, button);
          action.append(form);
        }
        row.append(action);
        tasksBody.append(row);
      }
      if (!tasksBody.children.length) {
        const row = document.createElement("tr");
        const cell = el("td", "暂无升级任务");
        cell.colSpan = 5;
        row.append(cell);
        tasksBody.append(row);
      }
    }
    function updateChooser(form) {
      if (!data) return;
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
          ? "不可用"
          : existing
            ? active
              ? "已存在"
              : "将重新启用"
            : "可新增";
        const target =
          kind === "assign"
            ? node?.target ||
              form.elements.reality_target.value.trim() ||
              defaultRealityTarget(node?.region)
            : "";
        $(".choice-state", row).textContent =
          kind === "assign"
            ? `${label} · ${target || "需设置所在地或填写 target"}`
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
        `已选 ${selected.length} / 可选 ${checkboxes.filter((c) => !c.disabled).length} · 新增 ${plan.created} · 重新启用 ${plan.reenabled} · 已存在 ${plan.unchanged}。清除选择仅影响本次提交。`;
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
            ? `默认：${[...targets][0]}`
            : "留空：按各 Gateway 所在地使用默认值";
      }
    }
    function enhance(root = document) {
      $$("section[data-page] > form.stack", root).forEach((form) => {
        const section = form.closest("section");
        if (form.dataset.enhanced) return;
        form.dataset.enhanced = "true";
        if (["assign", "subscribe"].includes(section.id)) {
          form.classList.add("chooser");
          const field = $("fieldset", form),
            tools = el("div", null, "selection-tools"),
            search = el("input", null, "selection-search");
          search.type = "search";
          search.placeholder = "搜索名称或地址";
          search.setAttribute("aria-label", "搜索可选项");
          const filter = el("select", null, "selection-filter");
          filter.setAttribute("aria-label", "选择状态筛选");
          [
            ["all", "全部"],
            ["available", "可选"],
            ["new", "未配置"],
            ["active", "已存在"],
          ].forEach(([v, t]) => {
            const o = el("option", t);
            o.value = v;
            filter.append(o);
          });
          const group = el("select", null, "selection-group");
          group.setAttribute("aria-label", "按 Gateway 分组");
          const all = el("option", "所有 Gateway");
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
          const select = el("button", "全选筛选结果"),
            clear = el("button", "清除选择");
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
              `＋ ${$("button", form)?.textContent || "创建"}`,
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
          ["create-route", "gateways", "agents", "links"].includes(section.id)
        ) {
          const advanced = el("details", null, "advanced"),
            inner = el("div");
          advanced.append(el("summary", "高级设置"), inner);
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
        search.placeholder = "搜索此列表";
        search.setAttribute(
          "aria-label",
          `${$("h2", table.closest("section")).textContent} 搜索`,
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
          target.placeholder = nextDefault || "手动填写 REALITY target";
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
          throw new Error("会话已过期或服务不可用，请重新登录后刷新。");
        data = await response.json();
        renderMetrics();
        renderMap();
        renderProgress();
        renderHistory();
        renderOperations();
        updateLiveTables();
        renderNodeUpgrades();
        $$(".chooser").forEach((f) => {
          const group = $(".selection-group", f),
            old = group.value;
          if (f.closest("section").id === "subscribe") {
            const desired = data.nodes.filter((n) => n.role === "Gateway");
            if (group.options.length !== desired.length + 1) {
              group.replaceChildren(
                new Option("所有 Gateway", ""),
                ...desired.map((n) => new Option(n.name, n.id)),
              );
              group.value = old;
            }
          }
          updateChooser(f);
        });
        $("#refresh-note").textContent =
          `已更新 ${new Date(data.at).toLocaleTimeString()} · 每 15 秒自动刷新`;
      } catch (error) {
        $("#refresh-note").textContent =
          `刷新失败：${error.message} · 保留最后结果`;
      } finally {
        refreshBusy = false;
      }
    }
    async function replaceSections(html, submittedForm) {
      const doc = new DOMParser().parseFromString(html, "text/html");
      if (!$("#page-title", doc)) throw new Error("会话已过期，请重新登录。");
      delete submittedForm.dataset.dirty;
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
          enhance(section);
          $$(".table-search", section).forEach((s, i) => {
            s.value = searches[i] || "";
            s.dispatchEvent(new Event("input"));
          });
        });
      notice(
        $("#notice", doc)?.textContent.trim() ||
          "操作成功。正在跟踪配置与运行状态。",
      );
      navigate();
    }
    document.addEventListener("input", (event) => {
      if (
        event.target.matches(
          "input[type=search],.selection-filter,.selection-group",
        )
      )
        return;
      event.target.closest("form")?.setAttribute("data-dirty", "true");
    });
    document.addEventListener("change", (event) => {
      if (
        event.target.closest("form") &&
        !event.target.matches(".selection-filter,.selection-group")
      )
        event.target.closest("form").dataset.dirty = "true";
    });
    document.addEventListener("submit", async (event) => {
      const form = event.target;
      if (!form.action.includes("/admin/")) return;
      event.preventDefault();
      if (submitting) return;
      submitting = true;
      const buttons = $$("button", form);
      buttons.forEach((b) => (b.disabled = true));
      try {
        const response = await fetch(form.action, {
            method: "POST",
            body: new URLSearchParams(new FormData(form)),
            headers: { Accept: "text/html, text/plain" },
          }),
          text = await response.text();
        if (!response.ok) throw new Error(text.slice(0, 700));
        if (response.headers.get("content-type")?.includes("text/plain")) {
          const content = el("div"),
            pre = el("pre", text),
            copy = el("button", "复制安装说明");
          copy.type = "button";
          copy.dataset.copy = text;
          content.append(
            el("p", "在对应主机执行安装命令；完成后面板自动跟踪上线状态。"),
            copy,
            pre,
          );
          showDetail("安装选项 · 一次性令牌", content);
          delete form.dataset.dirty;
          notice("已生成安装选项。关闭窗口前保存所需令牌。");
        } else await replaceSections(text, form);
      } catch (error) {
        notice(
          `操作未完成：${error.message}。输入已保留；网络中断时请先检查操作记录，避免重复提交。`,
          true,
        );
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
        button.textContent = "已复制";
        setTimeout(() => (button.textContent = before), 2000);
      } catch {
        button.previousElementSibling?.select?.();
        notice("无法访问剪贴板，请手动选择并复制。", true);
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
    enhance();
    navigate();
    refresh();
    setInterval(() => {
      if (document.visibilityState === "visible") refresh();
    }, 15000);
  })();

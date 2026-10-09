(() => {
  "use strict";
  const $ = (id) => document.getElementById(id);
  const labels = {ready: "就绪", pending: "待就绪", offline: "离线", stale: "数据过期", unknown: "等待上报", disabled: "停用", unprobed: "未探测"};
  const roles = {gateway: "Gateway · 入口", agent: "Agent · 出口", external: "外部出口"};
  let data = null, selection = null, loading = false;
  const el = (tag, text, cls) => {
    const node = document.createElement(tag);
    if (text !== undefined) node.textContent = text;
    if (cls) node.className = cls;
    return node;
  };
  const stateLabel = (stage) => el("span", `${stage === "ready" ? "●" : "○"} ${labels[stage] || "等待上报"}`, `state ${stage}`);
  const rtt = (link) => link.rtt_ms === null || link.rtt_ms === undefined ? "—" : `${link.rtt_ms.toFixed(1)} ms`;
  const time = (value) => new Date(value).toLocaleTimeString("zh-CN", {hour12: false});

  function render() {
    if (!data) return;
    const measuredNodes = data.nodes.filter((n) => n.role !== "external");
    const online = measuredNodes.filter((n) => ["ready", "pending"].includes(n.state)).length;
    const healthy = data.links.filter((l) => l.state === "ready").length;
    $("metrics").replaceChildren(...[
      ["在线节点", online, measuredNodes.length],
      ["就绪链路", healthy, data.links.length],
      ["已发布节点", data.nodes.length, null],
    ].map(([label, count, total]) => {
      const card = el("div", undefined, "metric");
      const value = el("strong", String(count));
      if (total !== null) value.append(el("small", ` / ${total}`));
      card.append(el("span", label), value);
      return card;
    }));
    $("nodes").replaceChildren(...data.nodes.map((node) => {
      const card = el("div", undefined, "node"), name = el("div", node.name, "node-name");
      name.append(el("small", roles[node.role], "node-role"));
      card.append(name, stateLabel(node.state));
      return card;
    }));
    if (!data.nodes.length) $("nodes").append(el("p", "暂未发布节点。", "empty"));
    renderMatrix();
    renderDetail();
  }

  function renderMatrix() {
    const query = $("search").value.trim().toLocaleLowerCase();
    const names = new Map(data.nodes.map((n) => [n.key, n.name]));
    const links = data.links.filter((l) => !query || [l.name, names.get(l.gateway), names.get(l.exit)].some((v) => v?.toLocaleLowerCase().includes(query)));
    const visible = (n) => !query || n.name.toLocaleLowerCase().includes(query) || links.some((l) => l.gateway === n.key || l.exit === n.key);
    const gateways = data.nodes.filter((n) => n.role === "gateway" && visible(n));
    const exits = data.nodes.filter((n) => n.role !== "gateway" && visible(n));
    if (!gateways.length || !exits.length) {
      $("matrix").replaceChildren(el("p", query ? "没有匹配的公开名称。" : "暂未发布入口与出口组合。", "empty"));
      return;
    }
    const table = el("table"), thead = el("thead"), header = el("tr");
    header.append(el("th", "入口 ↓ / 出口 →"));
    exits.forEach((node) => header.append(el("th", node.name)));
    thead.append(header);
    const body = el("tbody");
    gateways.forEach((gateway) => {
      const row = el("tr"), title = el("th", gateway.name);
      title.scope = "row";
      row.append(title);
      exits.forEach((exit) => {
        const cell = el("td"), pair = links.filter((l) => l.gateway === gateway.key && l.exit === exit.key);
        if (!pair.length) cell.append(el("span", "—", "muted"));
        else {
          const healthy = pair.filter((l) => l.state === "ready");
          const representative = healthy.find((l) => l.rtt_ms !== null) || healthy[0] || pair[0];
          const button = el("button", undefined, representative.state);
          button.type = "button";
          button.setAttribute("aria-label", `${gateway.name} → ${exit.name}，${pair.length} 条链路，查看详情`);
          button.append(stateLabel(representative.state), el("span", rtt(representative), "cell-value"));
          button.append(el("span", pair.length > 1 ? `${healthy.length}/${pair.length} 就绪 · ${pair.length} 条 Link` : representative.name, "cell-extra"));
          button.addEventListener("click", () => {
            selection = {gateway: gateway.name, exit: exit.name};
            renderDetail();
            $("detail").scrollIntoView({behavior: "smooth", block: "start"});
            $("detail-title").focus({preventScroll: true});
          });
          cell.append(button);
        }
        row.append(cell);
      });
      body.append(row);
    });
    table.append(thead, body);
    $("matrix").replaceChildren(table);
  }

  function renderDetail() {
    if (!selection || !data) { $("detail").hidden = true; return; }
    const gateway = data.nodes.find((n) => n.role === "gateway" && n.name === selection.gateway);
    const exit = data.nodes.find((n) => n.role !== "gateway" && n.name === selection.exit);
    const links = gateway && exit ? data.links.filter((l) => l.gateway === gateway.key && l.exit === exit.key) : [];
    if (!links.length) { selection = null; $("detail").hidden = true; return; }
    $("detail").hidden = false;
    $("detail-title").textContent = `${gateway.name} → ${exit.name}`;
    $("detail-body").replaceChildren(...links.map((link) => {
      const card = el("article", undefined, "link-detail"), heading = el("div", undefined, "link-heading");
      heading.append(el("h3", link.name), stateLabel(link.state));
      card.append(heading, el("p", rtt(link), "link-value"));
      card.append(el("p", link.measured_at ? `最近成功探测 ${time(link.measured_at)}` : "暂无当前有效延迟测量", "muted"));
      if (link.history.length) {
        const canvas = el("canvas", undefined, "history");
        canvas.setAttribute("role", "img");
        canvas.setAttribute("aria-label", `${link.name} 近 24 小时隧道 RTT 趋势，单位毫秒`);
        card.append(canvas);
        requestAnimationFrame(() => { if (canvas.isConnected) drawHistory(canvas, link.history); });
        const details = el("details", undefined, "history-data"), summary = el("summary", `采样数据（${link.history.length} 点，约 5 分钟间隔）`);
        const table = el("table"), head = el("tr");
        head.append(el("th", "时间"), el("th", "RTT"));
        const thead = el("thead"); thead.append(head); table.append(thead);
        const tbody = el("tbody");
        [...link.history].reverse().forEach((sample) => {
          const row = el("tr");
          row.append(el("td", new Date(sample.at).toLocaleString("zh-CN", {hour12: false})), el("td", rtt(sample)));
          tbody.append(row);
        });
        table.append(tbody); details.append(summary, table); card.append(details);
      } else card.append(el("p", "暂无延迟历史。", "muted"));
      return card;
    }));
  }

  function drawHistory(canvas, samples) {
    const width = Math.max(220, canvas.clientWidth), height = 180, ratio = window.devicePixelRatio || 1;
    canvas.width = width * ratio; canvas.height = height * ratio;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    ctx.scale(ratio, ratio);
    const values = samples.filter((s) => s.rtt_ms !== null);
    const max = Math.max(10, ...values.map((s) => s.rtt_ms)) * 1.1;
    const start = new Date(data.at).getTime() - 24 * 3600 * 1000, end = new Date(data.at).getTime();
    const left = 48, right = width - 12, top = 16, bottom = height - 30;
    ctx.font = "11px system-ui"; ctx.fillStyle = "#71868e";
    for (let i = 0; i <= 2; i++) {
      const y = top + (bottom - top) * i / 2;
      ctx.strokeStyle = "#e6eeee"; ctx.beginPath(); ctx.moveTo(left, y); ctx.lineTo(right, y); ctx.stroke();
      ctx.fillText(`${(max * (1 - i / 2)).toFixed(0)}`, 5, y + 4);
    }
    ctx.fillText("ms", 5, 10);
    ctx.fillText("24 小时前", left, height - 6);
    ctx.fillText("现在", right - 26, height - 6);
    ctx.strokeStyle = "#117c79"; ctx.fillStyle = "#117c79"; ctx.lineWidth = 2;
    let previous = null;
    samples.forEach((sample) => {
      const at = new Date(sample.at).getTime();
      if (sample.rtt_ms === null) { previous = null; return; }
      const x = left + (right - left) * (at - start) / (end - start), y = bottom - (bottom - top) * sample.rtt_ms / max;
      if (previous && at - previous.at <= 10 * 60 * 1000) {
        ctx.beginPath(); ctx.moveTo(previous.x, previous.y); ctx.lineTo(x, y); ctx.stroke();
      }
      ctx.beginPath(); ctx.arc(x, y, 2, 0, Math.PI * 2); ctx.fill();
      previous = {x, y, at};
    });
  }

  async function refresh() {
    if (loading) return;
    loading = true; $("refresh").disabled = true;
    const controller = new AbortController(), timeout = setTimeout(() => controller.abort(), 10000);
    try {
      const response = await fetch("/api/public/monitor", {credentials: "omit", cache: "no-store", signal: controller.signal});
      if (!response.ok) {
        if (response.status === 404) {
          data = null; selection = null;
          ["metrics", "nodes", "matrix", "detail-body"].forEach((id) => $(id).replaceChildren());
          $("detail").hidden = true;
          throw new Error("公开监控已关闭。");
        }
        throw new Error("状态更新失败，当前数据已标记为过期。");
      }
      data = await response.json();
      $("notice").hidden = true;
      $("updated").textContent = `已更新 ${time(data.at)} · 每 15 秒刷新`;
      render();
    } catch (error) {
      $("notice").hidden = false;
      $("notice").textContent = error.name === "AbortError" ? "状态请求超时，当前数据已标记为过期。" : error.message;
      $("updated").textContent = "等待恢复更新";
      if (data) {
        data.nodes.forEach((n) => { n.state = "stale"; });
        data.links.forEach((l) => { l.state = "stale"; l.rtt_ms = null; l.measured_at = null; });
        render();
      }
    } finally {
      clearTimeout(timeout); loading = false; $("refresh").disabled = false;
    }
  }
  $("refresh").addEventListener("click", refresh);
  $("search").addEventListener("input", () => { if (data) renderMatrix(); });
  $("close-detail").addEventListener("click", () => { selection = null; renderDetail(); });
  window.addEventListener("resize", () => { if (selection) renderDetail(); });
  refresh();
  setInterval(() => { if (!document.hidden) refresh(); }, 15000);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) refresh(); });
})();

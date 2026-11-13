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
      refreshRequested = false,
      mapView = "topology",
      lastSuccessfulRefresh = null;
    const submittingForms = new Set();
    const deferredFormUpdates = new Map();
    const sectionRevisions = new Map();
    const copyFeedback = new WeakMap();
    const tableSearches = new WeakMap();
    let grantStructure = null;
    let managementStructure = null;
    const managementSections = ["gateways", "agents", "assignments", "links", "upstreams"];
    let currentCSRF = $('input[name="csrf"]')?.value || "";
    const yieldToBrowser = () => globalThis.scheduler?.yield
      ? globalThis.scheduler.yield()
      : new Promise((resolve) => setTimeout(resolve, 0));
    const nodeName = (id) => data?.nodes.find((n) => n.id === id)?.name || id;
    const exitID = (route) => route.upstream_id || route.agent_id;
    const routeName = (route) =>
      route.display_name || route.name || `${nodeName(route.gateway_id)} / ${nodeName(exitID(route))}`;
    function notice(text, error = false) {
      $("#notice").textContent = t(text);
      $("#notice").classList.toggle("error", error);
    }
    function clearFormError(form) {
      $(".form-error", form)?.remove();
    }
    function applyCurrentCSRF(root = document) {
      if (currentCSRF) $$('input[name="csrf"]', root).forEach((input) => input.value = currentCSRF);
    }
    function synchronizeCSRF(response, requestCSRF) {
      const token = response.headers.get("X-XMesh-CSRF-Token");
      // A delayed response from an older login must not undo a newer session token.
      if (!token || (currentCSRF !== requestCSRF && currentCSRF !== token)) return;
      currentCSRF = token;
      applyCurrentCSRF();
    }
    function sessionExpiredError() {
      const error = new Error(t("会话已过期，请在新窗口重新登录。当前输入已保留。"));
      error.sessionExpired = true;
      return error;
    }
    function checkSessionResponse(response) {
      if (response.status === 401 || (response.redirected && new URL(response.url).pathname === "/login"))
        throw sessionExpiredError();
    }
    function appendLoginRecovery(container) {
      const link = el("a", t("在新窗口重新登录"));
      link.href = "/login"; link.target = "_blank"; link.rel = "noopener";
      container.append(el("p", t("重新登录后返回此页，点击“刷新状态”，即可继续编辑和提交。")), link);
    }
    function showFormError(form, message, recoverLogin = false) {
      clearFormError(form);
      const error = el("div", message, "form-error");
      if (recoverLogin) appendLoginRecovery(error);
      error.setAttribute("role", "alert");
      error.tabIndex = -1;
      form.prepend(error);
      error.focus();
      error.scrollIntoView({ block: "center", behavior: "smooth" });
    }
    const isAccessMutation = (form) => {
      const path = new URL(form.action).pathname;
      return path === "/admin/subscribe" || /^\/admin\/grants(?:\/|$)/.test(path) ||
        /^\/admin\/users\/[^/]+\/(?:state|toggle|subscribe|reset-subscription|archive)$/.test(path);
    };
    const accessMutationBusy = () => [...submittingForms].some(isAccessMutation);
    function updateAccessControls() {
      const section = $("#grants");
      if (!section) return;
      const user = $("#grant-user").value,
        query = $("#grant-search").value.trim().toLowerCase(),
        state = $("#grant-state").value,
        rows = $$("tr[data-grant-id]", section),
        busy = accessMutationBusy();
      rows.forEach((row) => {
        const names = `${$(".grant-user-name", row).textContent} ${$(".grant-route-name", row).textContent}`;
        row.hidden = !!((user && row.dataset.userId !== user) ||
          (state && row.dataset.enabled !== state) || !names.toLowerCase().includes(query));
        const choice = $('input[name="grant_id"]', row);
        choice.disabled = busy;
      });
      const visible = rows.filter((row) => !row.hidden),
        selected = rows.filter((row) => $('input[name="grant_id"]', row).checked),
        users = new Map(selected.map((row) => [row.dataset.userId, $(".grant-user-name", row).textContent]));
      $("#grant-selection-summary").textContent = selected.length
        ? t("已选 {0} 项授权，涉及 {1} 位用户：{2}", selected.length, users.size, [...users.values()].join("、"))
        : t("显示 {0} / {1} 项授权；请选择要管理的线路。", visible.length, rows.length);
      if (selected.some((row) => row.hidden)) $("#grant-selection-summary").textContent += " " + t("其中 {0} 项被筛选隐藏，提交仍包含这些选择。", selected.filter((row) => row.hidden).length);
      $('#grant-bulk button[type="submit"]').disabled = !selected.length || busy;
      $("#grant-bulk select").disabled = busy;
      $$("#grant-table form button").forEach((button) => button.disabled = busy);
      $("[data-grant-select]", section).disabled = !visible.length || busy;
      $("[data-grant-clear]", section).disabled = !selected.length || busy;
      $$(".grant-filters input, .grant-filters select", section).forEach((control) => control.disabled = busy);
      $("#grant-empty").hidden = !rows.length || !!visible.length;
      $$("form").filter((form) => isAccessMutation(form) && !section.contains(form)).forEach((form) => {
        if (form.classList.contains("chooser")) {
          updateChooser(form);
          if (busy) $("button[type=submit]", form).disabled = true;
        } else $$("button", form).forEach((button) => button.disabled = busy ||
          (form.classList.contains("user-state-form") && form.closest("tr").dataset.archived === "true"));
      });
    }
    function enhanceGrantManager(root) {
      const section = root.id === "grants" ? root : $("#grants", root);
      if (!section) return;
      const filters = $(".grant-filters", section);
      if (filters.dataset.enhanced) return;
      filters.dataset.enhanced = "true";
      filters.addEventListener("input", updateAccessControls);
      filters.addEventListener("change", updateAccessControls);
      const selectRows = (selected) => {
        $$("tr[data-grant-id]", section).forEach((row) => {
          if (!selected || !row.hidden) $('input[name="grant_id"]', row).checked = selected;
        });
        $("#grant-bulk").dataset.dirty = "true";
        updateAccessControls();
      };
      $("[data-grant-select]", section).addEventListener("click", () => selectRows(true));
      $("[data-grant-clear]", section).addEventListener("click", () => selectRows(false));
      $("#grant-table", section).addEventListener("change", (event) => {
        if (event.target.name !== "grant_id") return;
        $("#grant-bulk").dataset.dirty = "true";
        updateAccessControls();
      });
      updateAccessControls();
    }
    function reconcileGrants(incoming, revision) {
      const section = $("#grants");
      if (!incoming || revision < (sectionRevisions.get("grants") || 0n)) return false;
      const body = $("#grant-table tbody"),
        existing = new Map($$("tr[data-grant-id]", body).map((row) => [row.dataset.grantId, row])),
        desired = $$("tr[data-grant-id]", incoming),
        retained = new Set(desired.map((row) => row.dataset.grantId));
      $$("tr", body).filter((row) => !retained.has(row.dataset.grantId)).forEach((row) => row.remove());
      desired.forEach((source, index) => {
        let row = existing.get(source.dataset.grantId);
        if (!row) {
          row = document.importNode(source, true);
          enhance(row);
        } else {
          // Update display cells without replacing focused inputs, selections,
          // open details or a pending/unfinished deletion form.
          for (const cell of [1, 2, 3, 4]) row.cells[cell].replaceChildren(...[...source.cells[cell].childNodes].map((n) => document.importNode(n, true)));
          row.dataset.enabled = source.dataset.enabled;
          row.dataset.userId = source.dataset.userId;
          row.dataset.confirmation = source.dataset.confirmation;
          $('input[name="confirm_name"]', row).placeholder = $('input[name="confirm_name"]', source).placeholder;
          $(".sr-only", row).textContent = $(".sr-only", source).textContent;
          const technical = $("td:first-child details small", row);
          technical.replaceChildren(...[...$("td:first-child details small", source).childNodes].map((n) => document.importNode(n, true)));
          const stateForm = $(".grant-state-form", row);
          if (!submittingForms.has(stateForm)) {
            stateForm.elements.enabled.value = $(".grant-state-form", source).elements.enabled.value;
            $("button", stateForm).textContent = t($(".grant-state-form button", source).textContent.trim());
          }
        }
        if (body.children[index] !== row) body.insertBefore(row, body.children[index] || null);
      });
      if (!desired.length) body.append(...[...$("#grant-table tbody", incoming).children].map((row) => document.importNode(row, true)));
      const userFilter = $("#grant-user"), oldUser = userFilter.value;
      userFilter.replaceChildren(...[...$("#grant-user", incoming).children].map((o) => document.importNode(o, true)));
      userFilter.value = [...userFilter.options].some((o) => o.value === oldUser) ? oldUser : "";
      sectionRevisions.set("grants", revision);
      i18n.apply(section);
      applyCurrentCSRF(section);
      labelResponsiveTables(section);
      updateAccessControls();
      return true;
    }
    function reconcileUsers(doc, revision) {
      const incoming = $("#user-table", doc), body = $("#user-table tbody");
      if (!incoming || !body || revision < (sectionRevisions.get("users") || 0n)) return;
      const existing = new Map($$("tr[data-user-id]", body).map((row) => [row.dataset.userId, row])),
        desired = $$("tr[data-user-id]", incoming),
        retained = new Set(desired.map((row) => row.dataset.userId));
      $$("tr", body).filter((row) => !retained.has(row.dataset.userId)).forEach((row) => row.remove());
      desired.forEach((source, index) => {
        let row = existing.get(source.dataset.userId);
        if (!row) {
          row = document.importNode(source, true);
          enhance(row);
        } else {
          updateIdentityCell(row.cells[0], $("strong", source.cells[0]).textContent, source.dataset.userId);
          const freshNotes = $(":scope > small", source.cells[0]);
          let notes = $(":scope > small", row.cells[0]);
          if (freshNotes) {
            if (!notes) { notes = el("small"); $("details", row.cells[0]).before(notes); }
            notes.textContent = freshNotes.textContent;
          } else notes?.remove();
          row.cells[1].replaceChildren(...[...source.cells[1].childNodes].map((n) => document.importNode(n, true)));
          row.dataset.enabled = source.dataset.enabled;
          row.dataset.archived = source.dataset.archived;
          const form = $(".user-state-form", row);
          syncLiveForm(form, $(".user-state-form", source));
          for (const suffix of ["edit", "archive"]) {
            const target = $(`form[action$="/${suffix}"]`, row), fresh = $(`form[action$="/${suffix}"]`, source);
            if (target && fresh) syncLiveForm(target, fresh);
          }
        }
        if (body.children[index] !== row) body.insertBefore(row, body.children[index] || null);
      });
      if (!desired.length) body.append(...[...$("tbody", incoming).children].map((row) => document.importNode(row, true)));
      if (data && revision >= BigInt(data.revision)) {
        desired.forEach((source) => {
          const user = data.users.find((u) => u.id === source.dataset.userId);
          if (user) Object.assign(user, {
            name: $("td:first-child strong", source).textContent,
            notes: $('form[action$="/edit"] [name="notes"]', source).value,
            enabled: source.dataset.enabled === "true", archived: source.dataset.archived === "true",
          });
        });
      }
      sectionRevisions.set("users", revision);
      i18n.apply($("#users"));
      applyCurrentCSRF($("#users"));
      labelResponsiveTables($("#users"));
      $("#users .table-search")?.dispatchEvent(new Event("input"));
      updateAccessControls();
      renderUserContext();
    }
    function reconcileSubscriptionLinks(doc, revision) {
      const incoming = $("#subscription-links", doc), target = $("#subscription-links");
      if (!incoming || !target || revision < (sectionRevisions.get("subscribe") || 0n)) return;
      const existing = new Map($$(".subscription-row", target).map((row) => [row.dataset.userId, row])),
        desired = $$(".subscription-row", incoming),
        retained = new Set(desired.map((row) => row.dataset.userId));
      $$(".subscription-row", target).filter((row) => !retained.has(row.dataset.userId)).forEach((row) => row.remove());
      desired.forEach((source, index) => {
        let row = existing.get(source.dataset.userId);
        if (!row) row = document.importNode(source, true);
        else {
          row.dataset.userName = source.dataset.userName;
          row.dataset.archived = source.dataset.archived;
          $(".publication", row).replaceChildren(...[...$(".publication", source).childNodes].map((n) => document.importNode(n, true)));
          const copy = $("button[data-copy]", row), url = $("button[data-copy]", source).dataset.copy;
          if (copy.dataset.copy !== url) copy.textContent = t("复制订阅链接");
          copy.dataset.copy = url;
          $("input[readonly]", row).value = url;
        }
        if (target.children[index] !== row) target.insertBefore(row, target.children[index] || null);
        row.hidden = row.dataset.userId !== selectedUser;
      });
      const select = $('#subscribe select[name="user_id"]');
      if (syncNamedOptions(select, desired.map((row) => ({ id: row.dataset.userId, name: row.dataset.userName, disabled: row.dataset.archived === "true" })))) {
        updateChooser($("#subscribe form.chooser"));
      }
      // The same watermark protects both these links and their parent section.
      sectionRevisions.set("subscribe", revision);
      i18n.apply(target);
    }
    async function syncGrantStructure() {
      const structure = JSON.stringify([
        data.users.map((u) => [u.id, u.name, u.notes, u.archived, u.enabled, u.subscription_version]),
        data.routes.map((r) => [r.id, r.name, r.display_name, r.gateway_id, r.agent_id, r.upstream_id, r.enabled]),
        data.links.map((l) => [l.id, l.name, l.route_id]),
        data.grants.map((g) => [g.id, g.user_id, g.route_id, g.enabled, g.published, g.importable, g.hy2_available, g.access]),
        data.nodes.map((n) => [n.id, n.name, n.enabled, n.hy2_disabled, n.hy2_port,
          n.status.hy2_ready, n.status.ipv6_ingress_ready, n.status.binary_version]),
      ]);
      if (structure === grantStructure) return;
      const requestCSRF = currentCSRF;
      const response = await fetch("/admin/grants", { cache: "no-store", signal: AbortSignal.timeout(12000) });
      synchronizeCSRF(response, requestCSRF);
      checkSessionResponse(response);
      if (!response.ok || response.redirected) throw new Error(t("服务请求失败（HTTP {0}），请稍后重试。", response.status));
      const doc = new DOMParser().parseFromString(await response.text(), "text/html"),
        incoming = $("#grants", doc);
      if (!incoming) throw new Error(t("服务返回了无法识别的页面，请稍后重试。"));
      const revision = BigInt(incoming.dataset.revision || "0");
      if (reconcileGrants(incoming, revision)) grantStructure = structure;
      reconcileSubscriptionLinks(doc, revision);
      reconcileUsers(doc, revision);
      if (revision > BigInt(data.revision)) refreshRequested = true;
    }
    function syncLiveForm(target, source) {
      const dirty = formHasUnsavedChanges(target);
      if (submittingForms.has(target) || dirty) {
        // Keep only the latest remote snapshot while the local form is protected.
        // The configuration fingerprint can stay unchanged after the draft is
        // reverted, so a later refresh must be able to apply this without refetching.
        deferredFormUpdates.set(target, source.cloneNode(true));
        if (submittingForms.has(target)) return;
      } else deferredFormUpdates.delete(target);
      for (const fresh of source.elements) {
        if (!fresh.name) continue;
        const input = [...target.elements].find((item) => item.name === fresh.name && item.type === fresh.type);
        if (!input) continue;
        if (fresh.tagName === "SELECT") {
          const previous = input.value, entries = [...fresh.options];
          if (input.options.length !== entries.length || entries.some((option, i) =>
            input.options[i].value !== option.value || input.options[i].text !== option.text || input.options[i].disabled !== option.disabled)) {
            input.replaceChildren(...entries.map((option) => option.cloneNode(true)));
          }
          const value = dirty || ["gateway_id", "agent_id", "upstream_id"].includes(input.name) ? previous : fresh.value;
          input.value = value;
          if (!dirty) [...input.options].forEach((option) => option.defaultSelected = option.selected);
        } else if (!dirty) {
          if (["checkbox", "radio"].includes(input.type)) input.checked = input.defaultChecked = fresh.checked;
          else {
            if (input.value !== fresh.value) input.value = fresh.value;
            if (input.defaultValue !== fresh.value) input.defaultValue = fresh.value;
          }
          input.required = fresh.required;
          input.disabled = fresh.disabled;
        }
      }
      if (!dirty) $$("button", target).forEach((button, i) => {
        const fresh = $$("button", source)[i];
        if (fresh) { button.disabled = fresh.disabled; if (button.textContent !== fresh.textContent) button.replaceChildren(...[...fresh.childNodes].map((node) => node.cloneNode(true))); }
      });
      if (!dirty) delete target.dataset.dirty;
    }
    function applyDeferredFormUpdates() {
      for (const [form, source] of deferredFormUpdates) {
        if (!form.isConnected || form.closest('[data-management-deleted="true"]')) {
          deferredFormUpdates.delete(form);
          continue;
        }
        if (submittingForms.has(form) || formHasUnsavedChanges(form)) continue;
        syncLiveForm(form, source);
        i18n.apply(form); applyCurrentCSRF(form);
      }
    }
    const managementDraft = (root) => [...submittingForms].some((form) => root.contains(form)) ||
      (root.matches("form") && formHasUnsavedChanges(root)) || $$("form", root).some(formHasUnsavedChanges);
    function mergeManagement(target, source) {
      if (target.nodeType !== Node.ELEMENT_NODE) { if (target.textContent !== source.textContent) target.textContent = source.textContent; return; }
      if (target.matches('table[data-live-reports]')) return;
      if (target.tagName === "FORM") { syncLiveForm(target, source); return; }
      // Preserve local expansion and enhanced widgets, while synchronizing server metadata.
      for (const attribute of [...target.attributes])
        if (!source.hasAttribute(attribute.name) && attribute.name !== "open" &&
            !(attribute.name === "hidden" && target.matches("section[data-page]"))) target.removeAttribute(attribute.name);
      for (const attribute of source.attributes)
        if (attribute.name !== "open" && target.getAttribute(attribute.name) !== attribute.value) target.setAttribute(attribute.name, attribute.value);
      if (target.matches("input.table-search")) return;
      // Records follow their action URL; layout containers follow their tag
      // order, so adding a success message cannot replace a table or its drafts.
      const localFeedback = (node) => node.nodeType === Node.ELEMENT_NODE && node.matches(".form-success");
      const keyed = (nodes) => {
        const counts = new Map();
        return nodes.filter((node) => !localFeedback(node)).map((node) => {
          const action = node.nodeType === Node.ELEMENT_NODE &&
            (node.tagName === "FORM" ? node.getAttribute("action") : /^(TR|DETAILS|ARTICLE|DIV)$/.test(node.tagName) ? $("form[action]", node)?.getAttribute("action") : null);
          const index = counts.get(node.nodeName) || 0;
          counts.set(node.nodeName, index + 1);
          return [action ? `${node.nodeName}:${action}` : `${node.nodeName}:${index}`, node];
        });
      };
      const old = new Map(keyed([...target.childNodes]));
      const desired = keyed([...source.childNodes]), keys = new Set(desired.map(([id]) => id));
      old.forEach((node, id) => {
        if (keys.has(id)) return;
        if (node.nodeType === Node.ELEMENT_NODE && managementDraft(node)) {
          node.dataset.managementDeleted = "true";
          if (!$(".management-deleted-note", node)) {
            const note = el("p", t("此项目已删除或不再可用，未提交内容已保留。"), "management-deleted-note error");
            (node.tagName === "TR" ? node.cells[0] : node).append(note);
          }
        } else node.remove();
      });
      let cursor = target.firstChild;
      desired.forEach(([id, fresh]) => {
        const existing = old.get(id);
        const node = existing?.nodeType === fresh.nodeType && existing?.nodeName === fresh.nodeName ? existing : fresh;
        if (node === existing) mergeManagement(node, fresh);
        else existing?.remove();
        while (cursor && localFeedback(cursor)) cursor = cursor.nextSibling;
        if (cursor !== node) target.insertBefore(node, cursor);
        else cursor = cursor.nextSibling;
      });
    }
    function updateDeletedManagement() {
      $$('[data-management-deleted="true"]').forEach((node) => {
        if (!managementDraft(node)) { node.remove(); return; }
        $$("button", node).forEach((button) => button.disabled = true);
      });
    }
    function reconcileManagement(doc, revision) {
      const active = document.activeElement, value = active?.value,
        selection = typeof active?.selectionStart === "number" ? [active.selectionStart, active.selectionEnd, active.selectionDirection] : null;
      for (const id of managementSections) {
        const section = document.getElementById(id), source = doc.getElementById(id);
        if (!section || !source || revision < (sectionRevisions.get(id) || 0n)) continue;
        const fresh = document.importNode(source, true);
        enhance(fresh);
        mergeManagement(section, fresh);
        sectionRevisions.set(id, revision);
        i18n.apply(section); applyCurrentCSRF(section); labelResponsiveTables(section);
        $$(':scope > table', section).forEach(filterTable);
      }
      updateDeletedManagement();
      if (active?.isConnected && document.activeElement !== active) active.focus({preventScroll:true});
      if (selection && active?.isConnected && active.value === value) active.setSelectionRange(...selection);
    }
    async function syncManagementStructure() {
      if (!data.management_revision || data.management_revision === managementStructure) return;
      const requestCSRF = currentCSRF, response = await fetch("/", {cache:"no-store",signal:AbortSignal.timeout(12000)});
      synchronizeCSRF(response, requestCSRF); checkSessionResponse(response);
      if (!response.ok || response.redirected) throw new Error(t("服务请求失败（HTTP {0}），请稍后重试。", response.status));
      const doc = new DOMParser().parseFromString(await response.text(), "text/html");
      if (!$("#page-title", doc)) throw new Error(t("服务返回了无法识别的页面，请稍后重试。"));
      const revision = BigInt($("main", doc)?.dataset.revision || "0");
      reconcileManagement(doc, revision);
      managementStructure = data.management_revision;
      if (revision > BigInt(data.revision)) refreshRequested = true;
    }
    let expandedWorkspace = false, selectedUser = null;
    try { expandedWorkspace = sessionStorage.getItem("xmesh_expand_sections") === "true"; } catch {}
    try { selectedUser = sessionStorage.getItem("xmesh_selected_user"); } catch {}
    function userContext(id) {
      if (selectedUser !== id) {
        $$('#grant-table input[name="grant_id"]').forEach((c) => c.checked = false);
      }
      selectedUser = id;
      try { sessionStorage.setItem("xmesh_selected_user", id); } catch {}
      for (const selector of ["#workspace-user", "#grant-user", '#subscribe select[name="user_id"]']) {
        const select = $(selector);
        if (select) select.value = id;
      }
      const chooser = $("#subscribe form.chooser");
      if (chooser) updateChooser(chooser);
      $$(".subscription-row").forEach((row) => row.hidden = row.dataset.userId !== id);
      if (!data) {
        $("#user-context-summary").textContent = $("#workspace-user option:checked")?.textContent || t("先创建或选择用户，再管理线路与订阅。");
        $("#subscription-guide").textContent = t("正在读取当前用户的线路状态…");
      }
      updateAccessControls();
      renderUserContext();
    }
    function filterUserRows() {
      const mode = $("#user-archive-filter")?.value || "active",
        query = $("#users .table-search")?.value.trim().toLowerCase() || "";
      $$("#user-table tr[data-user-id]").forEach((row) => {
        row.hidden = !row.textContent.toLowerCase().includes(query) ||
          (mode !== "all" && (row.dataset.archived === "true") !== (mode === "archived"));
      });
    }
    function filterContextRows(section) {
      const query = $(".table-search", section)?.value.trim().toLowerCase() || "";
      $$("tr[data-user-id]", section).forEach((row) => {
        row.hidden = (!!selectedUser && row.dataset.userId !== selectedUser) || !row.textContent.toLowerCase().includes(query);
      });
    }
    function filterTable(table) {
      const search = tableSearches.get(table);
      if (!search) return;
      const section = table.closest("section");
      if (section.id === "users") { filterUserRows(); return; }
      if (section.id === "user-audit") { filterContextRows(section); return; }
      const query = search.value.trim().toLowerCase();
      $$("tbody tr", table).forEach((row) => row.hidden = !row.textContent.toLowerCase().includes(query));
    }
    function updateIdentityCell(cell, name, id) {
      let title=$(':scope > strong',cell), details=$(':scope > details',cell);
      if (!title || !details) {
        title ||= el("strong");
        if (!details) {details=el("details");details.append(el("summary",t("技术详情")),el("code"));}
        cell.replaceChildren(title,details);
      }
      if (title.textContent!==name) title.textContent=name;
      const code=$("code",details);if (code.textContent!==id) code.textContent=id;
    }
    function renderUserAudit() {
      const section = $("#user-audit"), body = $("tbody", section);
      if (!body || BigInt(data.revision) < (sectionRevisions.get("users") || 0n)) return;
      const rows = new Map($$("tr[data-user-id]", body).map((row) => [row.dataset.userId, row]));
      $$("tr:not([data-user-id])", body).forEach((row) => row.remove());
      data.users.forEach((user, index) => {
        let row = rows.get(user.id); rows.delete(user.id);
        if (!row) { row = el("tr"); row.dataset.userId = user.id; for (let i=0;i<4;i++) row.append(el("td")); }
        updateIdentityCell(row.cells[0],user.name,user.id);
        row.cells[1].textContent = t(user.audit_enabled ? "已开启" : "已关闭");
        row.cells[2].textContent = `${user.audit_limit_mb || 20} MB`;
        let link = $("a", row.cells[3]);
        if (!link) { link = el("a", t("查看与设置审计")); row.cells[3].append(link); }
        link.href = `/admin/users/${encodeURIComponent(user.id)}/audit`;
        if (body.children[index] !== row) body.insertBefore(row, body.children[index] || null);
      });
      rows.forEach((row) => row.remove());
      if (!data.users.length) { const row=el("tr"), cell=el("td", t("暂无用户")); cell.colSpan=4; row.append(cell); body.append(row); }
      filterContextRows(section);
    }
    function renderEnrollments() {
      const section = $("#install"), body = $("tbody", section);
      if (!body || !data.enrollments || BigInt(data.revision) < (sectionRevisions.get("install") || 0n) ||
        [...submittingForms].some((form) => section.contains(form))) return;
      const rows = new Map($$("tr[data-enrollment-id]", body).map((row) => [row.dataset.enrollmentId, row]));
      $$("tr:not([data-enrollment-id])", body).forEach((row) => row.remove());
      data.enrollments.forEach((item, index) => {
        let row=rows.get(item.id); rows.delete(item.id);
        if (!row) { row=el("tr"); row.dataset.enrollmentId=item.id; for(let i=0;i<4;i++) row.append(el("td")); }
        updateIdentityCell(row.cells[0],`${item.role} · ${item.node_name}`,item.node_id);
        row.cells[1].textContent=t(item.state);
        row.cells[2].textContent=new Date(item.expires_at).toLocaleString(i18n.locale());
        if (item.state !== "active") row.cells[3].replaceChildren();
        else if (!$("form",row.cells[3])) {
          const form=el("form"), csrf=el("input"); form.method="post"; form.action=`/admin/enrollments/${encodeURIComponent(item.id)}/revoke`;
          csrf.type="hidden";csrf.name="csrf";csrf.value=$('input[name="csrf"]').value;
          form.append(csrf,el("button",t("撤销")));row.cells[3].append(form);
        }
        if (body.children[index]!==row) body.insertBefore(row,body.children[index]||null);
      });
      rows.forEach((row)=>row.remove());
      if (!data.enrollments.length) {const row=el("tr"),cell=el("td",t("暂无安装令牌。"));cell.colSpan=4;row.append(cell);body.append(row);}
      const query=$(".table-search",section)?.value.toLowerCase()||"";
      $$("tr",body).forEach((row)=>row.hidden=!row.textContent.toLowerCase().includes(query));
      sectionRevisions.set("install",BigInt(data.revision));
    }
    function standaloneNodes() {
      return data.nodes.filter((node) => node.role === "Gateway" || (node.role === "Agent" && !node.gateway_id));
    }
    function renderQuickInstall() {
      const section = $("#quick-install");
      if (!section || !data || BigInt(data.revision) < (sectionRevisions.get(section.id) || 0n) ||
        [...submittingForms].some((form) => section.contains(form))) return;
      const forms = new Map($$('form[action="/admin/enrollments"]', section).map((form) => [form.elements.node_id.value, form]));
      const nodes = standaloneNodes();
      let previous = $(":scope > p", section) || $("h2", section);
      for (const node of nodes) {
        let form = forms.get(node.id); forms.delete(node.id);
        if (!form) {
          form = el("form", null, "inline"); form.method = "post"; form.action = "/admin/enrollments";
          for (const [name, value] of [["csrf", currentCSRF], ["role", node.role.toLowerCase()], ["node_id", node.id]]) {
            const input = el("input"); input.type = "hidden"; input.name = name; input.value = value; form.append(input);
          }
          form.append(el("button"));
        }
        const label = (node.role === "Gateway" ? t("安装 Gateway：") : t("安装 Agent：")) + node.name;
        if ($("button", form).textContent !== label) $("button", form).textContent = label;
        if (previous.nextElementSibling !== form) previous.after(form);
        previous = form;
      }
      forms.forEach((form) => form.remove());
      let empty = $(".empty", section);
      if (!nodes.length && !empty) { empty = el("p", t("暂无可安装节点。先创建 Gateway 或独立 Agent。"), "empty"); section.append(empty); }
      if (nodes.length) empty?.remove();
      sectionRevisions.set(section.id, BigInt(data.revision));
    }
    function renderUserContext() {
      if (!data || !$("#workspace-user")) return;
      ensureWorkspacePanels();
      const select = $("#workspace-user"), wasEmpty = ![...select.options].some((o) => o.value);
      syncNamedOptions(select, data.users.map((u) => ({...u, name: u.name + (u.archived ? " · " + t("已归档") : "")})), t("所有用户"));
      if (selectedUser === null || (!selectedUser && wasEmpty)) {
        selectedUser = data.users.find((u) => !u.archived)?.id || "";
        try { sessionStorage.setItem("xmesh_selected_user", selectedUser); } catch {}
      }
      // A deleted owner leaves an explicit empty context instead of switching to another user.
      if (!data.users.some((u) => u.id === selectedUser)) selectedUser = "";
      select.value = selectedUser;
      $("#grant-user").value = selectedUser;
      const chooser = $("#subscribe form.chooser");
      chooser.elements.user_id.value = selectedUser; updateChooser(chooser);
      const user = data.users.find((u) => u.id === selectedUser),
        grants = data.grants.filter((g) => g.user_id === selectedUser),
        published = grants.filter((g) => g.importable && user?.enabled && !user.archived);
      $("#user-context-summary").textContent = user
        ? t("{0} · {1} · 已发布 {2} / 已授权 {3}", user.name, t(user.archived ? "已归档" : user.enabled ? "启用" : "停用"), published.length, grants.filter((g) => g.enabled).length)
        : t("先创建或选择用户，再管理线路与订阅。");
      $("#user-context-notes").textContent = user?.notes || "";
      let audit = $("#user-context-audit");
      if (!audit) { audit = el("a", t("当前用户的审计记录")); audit.id = "user-context-audit"; $("#user-workbench").append(audit); }
      audit.hidden = !user; if (user) audit.href = `/admin/users/${encodeURIComponent(user.id)}/audit`;
      $$(".subscription-row").forEach((row) => row.hidden = row.dataset.userId !== selectedUser);
      for (const section of ["#usage", "#user-audit"]) {
        filterContextRows($(section));
      }
      const guide = $("#subscription-guide");
      if (guide) {
        guide.replaceChildren(el("h3", t("交付当前用户的订阅")), el("p", user
          ? t("当前用户：{0}。可导入 {1} 条已发布线路。", user.name, published.length)
          : t("先创建或选择用户，再管理线路与订阅。")));
        if (user) {
          guide.append(el("p", published.length
            ? t("1. 复制下方订阅链接。2. 在支持 VMess/HY2 的客户端添加订阅并更新。3. 选择线路连接。")
            : t("订阅暂无可导入线路。先开通线路，再等待入口确认配置；停用或归档的用户需先恢复启用。")));
          const reasons = new Set();
          for (const grant of grants) {
            if (!grant.access || grant.access.state === "ready" || reasons.has(grant.access.reason)) continue;
            reasons.add(grant.access.reason);
            const p = el("p", t(grant.access.reason) + " · "), a = el("a", t("查看下一步"));
            a.href = "#" + grant.access.target; p.append(a); guide.append(p);
          }
          const strict = el("a", t("HY2 严格校验：到该用户线路下载配置与证书")); strict.href = "#grants"; guide.append(strict);
        }
      }
      filterUserRows();
    }
    function ensureWorkspacePanels() {
      if (!$("#user-archive-filter")) {
        const label = el("label", t("用户范围")), select = el("select"); select.id = "user-archive-filter";
        select.append(new Option(t("正常用户"), "active"), new Option(t("已归档"), "archived"), new Option(t("全部"), "all"));
        label.append(select); $("#user-table").before(label); select.addEventListener("change", filterUserRows);
      }
      if (!$("#subscription-guide")) {
        const guide = el("div", null, "subscription-guide"); guide.id = "subscription-guide"; $("#subscription-links").before(guide);
      }
    }
    function enhanceWorkspace() {
      const nav = el("nav", null, "section-nav"); nav.id = "section-nav"; nav.setAttribute("aria-label", t("页面分区"));
      $(".page-heading").after(nav);
      const workbench = el("section", null, "user-workbench"); workbench.id = "user-workbench"; workbench.dataset.page = "users";
      const label = el("label", t("当前用户")), select = el("select"); select.id = "workspace-user"; label.append(select);
      workbench.append(label, el("p", "", "context-summary"), el("p", "", "muted"));
      $(".context-summary", workbench).id = "user-context-summary"; $(".muted", workbench).id = "user-context-notes";
      $("#users").before(workbench);
      select.addEventListener("change", () => userContext(select.value));
      ensureWorkspacePanels();
      $("#users").addEventListener("input", filterUserRows);
      document.addEventListener("change", (event) => {
        if (event.target.matches('#grant-user, #subscribe select[name="user_id"]')) userContext(event.target.value);
      });
      const entries = $$(".subscription-row").map((row) => ({id:row.dataset.userId, name:row.dataset.userName}));
      syncNamedOptions(select, entries, t("所有用户"));
      if (selectedUser === null) selectedUser = $('#subscribe select[name="user_id"]').value;
      if (!entries.some((entry) => entry.id === selectedUser)) selectedUser = "";
      userContext(selectedUser);
    }
    function renderSectionNav(page, active) {
      const nav = $("#section-nav");
      if (!nav) return;
      nav.replaceChildren(); nav.hidden = page === "overview";
      if (nav.hidden) return;
      $$(`section[data-page="${page}"]`).filter((s) => s.id !== "user-workbench").forEach((section) => {
        const a = el("a", $("h2", section)?.textContent || section.id); a.href = "#" + section.id;
        if (section.id === active) a.setAttribute("aria-current", "page"); nav.append(a);
      });
      const toggle = el("button", t(expandedWorkspace ? "收起为分区浏览" : "展开所有区域")); toggle.type = "button";
      toggle.addEventListener("click", () => {
        expandedWorkspace = !expandedWorkspace;
        try { sessionStorage.setItem("xmesh_expand_sections", String(expandedWorkspace)); } catch {}
        navigate({preserveScroll: true});
      }); nav.append(toggle);
    }
    function formHasUnsavedChanges(form) {
      return [...form.elements].some((input) => {
        if (!input.name || input.type === "hidden" || input.type === "search" || input.readOnly || input.disabled) return false;
        if (["checkbox", "radio"].includes(input.type)) return input.checked !== input.defaultChecked;
        if (input.tagName === "SELECT") {
          if (["user_id","agent_id"].includes(input.name)) return false;
          const options=[...input.options];
          if (input.multiple) return options.some((o)=>o.selected!==o.defaultSelected);
          const defaults=options.filter((o)=>o.defaultSelected), fallback=options.find((o)=>!o.disabled && !o.closest("optgroup:disabled"));
          return input.value !== ((defaults.at(-1)||fallback)?.value || "");
        }
        return ["INPUT","TEXTAREA"].includes(input.tagName) && input.value !== input.defaultValue;
      });
    }
    function syncFormDirtyState(form) {
      if (!form) return;
      if (submittingForms.has(form) || formHasUnsavedChanges(form)) form.dataset.dirty = "true";
      else delete form.dataset.dirty;
    }
    function acceptSubmittedValues(form, values, resetSource = null) {
      const submitted = new URLSearchParams(values);
      const ownerChanged = ["user_id", "agent_id"].some((name) => submitted.has(name) &&
        form.elements.namedItem(name)?.value !== submitted.get(name));
      const fieldKey = (input) => JSON.stringify([input.name, input.type,
        ["checkbox", "radio"].includes(input.type) ? input.value : ""]);
      const resetInputs = new Map(resetSource ? [...resetSource.elements].map((input) => [fieldKey(input), input]) : []);
      const checks = new Map([...form.elements].filter((input) => ["checkbox", "radio"].includes(input.type))
        .map((input) => [input, input.checked]));
      // Move the saved baseline forward while retaining edits made during the request.
      // Creation and batch selection use fresh defaults for the next draft.
      // A new owner makes even identical choices a separate, unsaved draft.
      for (const input of form.elements) {
        if (!input.name || input.disabled || input.readOnly || ["hidden", "search"].includes(input.type)) continue;
        const fresh = resetInputs.get(fieldKey(input));
        if (["checkbox", "radio"].includes(input.type)) {
          const saved = submitted.getAll(input.name).includes(input.value);
          // Successfully opened pairs may already be absent from the returned form.
          const resetChoice = fresh || (resetSource && form.classList.contains("pending-chooser") && input.name === "connection");
          const baseline = resetChoice ? (fresh?.checked ?? false) : saved;
          if (resetChoice && !ownerChanged && checks.get(input) === saved) checks.set(input, baseline);
          input.defaultChecked = baseline;
        } else if (input.tagName === "SELECT") {
          const options = [...input.options], selected = options.map((option) => option.selected);
          const saved = submitted.getAll(input.name), defaults = fresh
            ? [...fresh.selectedOptions].map((option) => option.value) : saved;
          const unchanged = !ownerChanged && !["user_id", "agent_id"].includes(input.name) &&
            options.every((option, index) => selected[index] === saved.includes(option.value));
          options.forEach((option) => option.defaultSelected = defaults.includes(option.value));
          options.forEach((option, index) => option.selected = fresh && unchanged ? defaults.includes(option.value) : selected[index]);
        } else if (["INPUT", "TEXTAREA"].includes(input.tagName) && submitted.has(input.name)) {
          const value = input.value;
          input.defaultValue = fresh ? fresh.value : submitted.get(input.name);
          input.value = fresh && !ownerChanged && value === submitted.get(input.name) ? fresh.value : value;
        }
      }
      checks.forEach((checked, input) => input.checked = checked);
      if (formHasUnsavedChanges(form)) form.dataset.dirty = "true";
      else delete form.dataset.dirty;
    }
    const detailQueue = [];
    let detailClosing = false;
    function hasUnsavedChanges() {
      return submittingForms.size > 0 || detailQueue.length > 0 || $("#detail-dialog[open] .install-token") ||
        $$("form[data-dirty]").some(formHasUnsavedChanges) || $$('#grant-table input[name="grant_id"]:checked').length > 0;
    }
    function detailFeedback(message) {
      let feedback = $("#detail-feedback");
      if (!feedback) {
        feedback = el("p", null, "form-success"); feedback.id = "detail-feedback";
        feedback.setAttribute("role", "status"); $("#detail-body").before(feedback);
      }
      feedback.textContent = message;
    }
    function closeDetail() {
      detailClosing = true;
      $("#detail-dialog").close();
    }
    function requestDetailClose() {
      const dialog=$("#detail-dialog");
      if ([...submittingForms].some((form)=>dialog.contains(form))) { detailFeedback(t("正在保存，请等待操作完成。")); return false; }
      if ($$("form[data-dirty]",dialog).some(formHasUnsavedChanges) &&
        !window.confirm(t("窗口中有未保存的修改，关闭后将丢失。仍要关闭吗？"))) return false;
      closeDetail(); return true;
    }
    function followDetailLink(event) {
      if (!requestDetailClose()) event.preventDefault();
    }
    let skipDraftUnload = false;
    window.addEventListener("beforeunload", (event) => {
      if (!skipDraftUnload && hasUnsavedChanges()) { event.preventDefault(); event.returnValue = ""; }
    });
    window.addEventListener("xmesh:before-language-change", (event) => {
      if (submittingForms.size) {
        event.preventDefault();
        const message = t("操作正在进行，请等待结果后再切换语言。当前请求不会重新提交。");
        notice(message);
        if ($("#detail-dialog").open) detailFeedback(message);
        return;
      }
      if (!hasUnsavedChanges()) return;
      const message = detailQueue.length
        ? t("还有尚未查看的安装选项，切换语言会丢失一次性令牌。仍要继续吗？")
        : $("#detail-dialog[open] .install-token")
          ? t("当前窗口的令牌只显示一次，切换语言后无法再次查看。请先保存令牌和命令。仍要继续吗？")
          : t("切换语言会重新加载页面，未提交的修改会丢失。仍要继续吗？");
      if (!window.confirm(message)) event.preventDefault();
      else skipDraftUnload = true;
    });
    function navigate({ preserveScroll = false } = {}) {
      const target = location.hash.slice(1) || "overview";
      const section = document.getElementById(target);
      const page = names[target] ? target : section?.dataset.page || "overview";
      const active = section?.dataset.page === page && target !== page ? target : ({users:"users", network:"routes", deploy:"wizard", maintenance:"node-upgrades"}[page] || target);
      $$("section[data-page]").forEach((s) => {
        s.hidden = s.dataset.page !== page || (!expandedWorkspace && page !== "overview" && s.id !== active && s.id !== "user-workbench");
      });
      renderSectionNav(page, active);
      $$(".sidebar a").forEach((a) => {
        a.classList.toggle("active", a.hash === `#${page}`);
        if (a.hash === `#${page}`) a.setAttribute("aria-current", "page");
        else a.removeAttribute("aria-current");
      });
      $("#page-title").textContent = names[page][0];
      $("#page-description").textContent = names[page][1];
      if (preserveScroll) return;
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
      if ($("#detail-dialog").open || detailClosing) {
        detailQueue.push({title, content});
        return false;
      }
      $("#detail-title").textContent = title;
      $("#detail-body").replaceChildren(content);
      $("#detail-dialog").showModal();
      return true;
    }
    function nodeRuntime(node) {
      const dl = el("dl", null, "detail-grid");
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
      if (node.role === "Gateway") {
        values[t("地址模式")] = node.public_host_auto ? t("自动地址") : t("手动覆盖");
        values[t("已确认公网 IP")] = node.detected_public_ip || t("等待公网 IP 确认");
        values[t("确认来源")] = node.public_ip_sources || "—";
        values[t("Controller 观察 IP")] = node.observed_public_ip || "—";
      }
      Object.entries(values).forEach(([k, v]) =>
        dl.append(el("dt", k), el("dd", v)),
      );
      return dl;
    }
    function showNode(id) {
      const node = data.nodes.find((n) => n.id === id);
      if (!node) return;
      const body = el("div"), runtime = el("div", null, "node-runtime");
      body.dataset.nodeDetail = id;
      runtime.append(nodeRuntime(node)); body.append(runtime);
      const link = el("a", t("打开节点管理"));
      link.href = node.role === "Gateway" ? "#gateways" : node.role === "Agent" ? "#agents" : "#upstreams";
      link.addEventListener("click", followDetailLink);
      body.append(link);
      showDetail(node.name, body);
    }
    function showRoute(id) {
      const route = data.routes.find((r) => r.id === id);
      if (!route) return;
      const body = el("div"), runtime = el("div", null, "route-runtime"), links = el("div", null, "route-link-status");
      body.dataset.routeDetail = id;
      body.append(runtime);
      const rename = el("form"); rename.method = "post"; rename.action = `/admin/attachments/${encodeURIComponent(route.id)}/edit`;
      const csrf = el("input"); csrf.type = "hidden"; csrf.name = "csrf"; csrf.value = $('input[name="csrf"]').value;
      const label = el("label", t("线路名称")), input = el("input"); input.name = "name"; input.value = input.defaultValue = route.name || ""; input.maxLength = 120;
      input.placeholder = t("留空使用 Link 或节点名称"); label.append(input);
      rename.append(csrf, label, el("button", t("保存线路名称")));
      body.append(rename);
      if (route.upstream_id) {
        const probe = el("div"); probe.dataset.upstreamProbe = id;
        probe.append(upstreamProbeSummary(route)); body.append(probe, upstreamProbeForm(route));
      }
      body.append(links);
      renderRouteRuntime(body, route);
      const a = el("a", t("查看链路配置"));
      a.href = route.upstream_id ? "#upstreams" : "#links";
      a.textContent = route.upstream_id ? t("查看外部出口") : t("查看链路配置");
      a.addEventListener("click", followDetailLink);
      body.append(a);
      showDetail(t("节点组合：{0}", routeName(route)), body);
    }
    function renderRouteRuntime(body, route) {
      const runtime = $(".route-runtime", body), links = $(".route-link-status", body);
      let state = $(".badge", runtime), reason = $("p", runtime), next = $("a", runtime);
      if (!state || !next) {
        state = badge(route.state); reason = el("p"); next = el("a", t("查看下一步"));
        next.addEventListener("click", followDetailLink); runtime.replaceChildren(state, reason, next);
      }
      state.className = `badge ${route.state}`; state.textContent = stages[route.state] || route.state;
      reason.textContent = t(route.issue?.reason || "待就绪");
      next.href = "#" + (route.issue?.target || "readiness");
      links.replaceChildren();
      data.links
        .filter((l) => l.route_id === route.id)
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
          links.append(p);
        });
    }
    function renderRuntimeDetails() {
      for (const body of $$("#detail-body [data-node-detail]")) {
        const node = data.nodes.find((item) => item.id === body.dataset.nodeDetail);
        $(".node-runtime", body).replaceChildren(node ? nodeRuntime(node) : el("p", t("节点已删除，请关闭窗口后选择其他节点。"), "error"));
        if (node) $("#detail-title").textContent = node.name;
      }
      for (const body of $$("#detail-body [data-route-detail]")) {
        const route = data.routes.find((item) => item.id === body.dataset.routeDetail);
        if (!route) {
          $(".route-runtime", body).replaceChildren(el("p", t("线路已删除，未提交的名称已保留。请关闭窗口后选择其他线路。"), "error"));
          $$("form", body).filter((form) => !submittingForms.has(form)).forEach((form) => $$("button", form).forEach((button) => button.disabled = true));
          continue;
        }
        renderRouteRuntime(body, route);
        $("#detail-title").textContent = t("节点组合：{0}", routeName(route));
        const form = $('form[action$="/edit"]', body);
        if (form && !submittingForms.has(form)) {
          $("button", form).disabled = false;
          if (!formHasUnsavedChanges(form)) {
            form.elements.name.value = form.elements.name.defaultValue = route.name || "";
            syncFormDirtyState(form);
          }
        }
      }
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
          data.grants.filter((g) => g.importable).length,
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
      button.dataset.upstreamProbeButton = route.id;
      button.disabled = !route.can_probe || route.state === "testing";
      form.append(csrf, button);
      return form;
    }
    function upstreamProbeSummary(route) {
      const body = el("div", null, "upstream-probe");
      const probe = route.probe;
      if (route.probe_supported) body.append(el("p", t("定时测试间隔：{0} 秒", route.probe_interval_seconds || 60), "muted"));
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
        if (submittingForms.has(button.closest("form"))) continue;
        const route = data.routes.find((r) => r.id === button.dataset.upstreamProbeButton);
        button.disabled = !route?.can_probe || route.state === "testing";
      }
    }
    function renderRouteOverview() {
      const root = $("#route-overview");
      if (!root || !data) return;
      if ([...submittingForms].some((form) => root.contains(form))) return;
      if (!data.routes.length) {
        root.replaceChildren(el("p", t("尚无线路。先从部署向导创建或绑定节点。"), "empty"));
        return;
      }
      const query = $("#route-search").value.trim().toLowerCase(),
        state = $("#route-state").value,
        visible = data.routes.filter((route) =>
          (!query || `${routeName(route)} ${nodeName(route.gateway_id)} ${nodeName(exitID(route))}`.toLowerCase().includes(query)) &&
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
          grantCount = data.grants.filter((grant) => grant.route_id === route.id && grant.importable).length,
          actions = el("div", null, "route-card-actions"),
          detail = el("button", t("查看状态"));
        detail.type = "button";
        detail.className = "secondary";
        detail.addEventListener("click", () => showRoute(route.id));
        heading.append(title, badge(route.state));
        card.append(heading, el("small", t("{0} · {1} 个已发布授权", kind, grantCount)));
        card.append(el("small", `${nodeName(route.gateway_id)} / ${nodeName(exitID(route))}`));
        if (route.issue && route.state !== "ready") card.append(el("p", t(route.issue.reason), "route-reason"));
        if (route.upstream_id) {
          card.append(upstreamProbeSummary(route));
          actions.append(upstreamProbeForm(route));
        }
        actions.append(detail);
        const config = el("a", t("管理配置"));
        config.href = route.upstream_id ? "#upstreams" : "#links";
        const grant = el("a", t("授权用户"));
        grant.href = "#subscribe";
        grant.dataset.grantRoute = route.id;
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
      const enabledNodes = data.nodes,
        ids = new Set(enabledNodes.filter(matches).map((n) => n.id));
      const routes = data.routes.filter(
        (r) =>
          (!query || ids.has(r.gateway_id) || ids.has(exitID(r)) || routeName(r).toLowerCase().includes(query)) &&
          (state === "all" || r.state === state),
      );
      const connected = new Set(
        routes.flatMap((r) => [r.gateway_id, exitID(r)]),
      );
      return {
        routes,
        nodes: enabledNodes.filter(
          (n) => connected.has(n.id) || ((state === "all" || (state === "disabled" && !n.enabled)) && matches(n)),
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
              f.dataset.owner = a.id;
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
      const progress = $("#deployment-progress");
      let picker = $(":scope > select", progress), content = $(".progress-cards", progress);
      if (!content) { content = el("div", null, "progress-cards"); progress.append(content); }
      if (!picker) {
        picker = el("select"); picker.setAttribute("aria-label", t("跟踪部署节点"));
        picker.addEventListener("change", () => {
          $$(".progress-card", progress).forEach((card) => card.hidden = card.dataset.nodeId !== picker.value);
        });
        progress.prepend(picker);
      }
      syncNamedOptions(picker, data.nodes.filter((node) => node.role !== "External exit")
        .map((node) => ({id: node.id, name: `${node.role} · ${node.name}`})));
      if (picker.options.length && picker.selectedIndex < 0) picker.selectedIndex = 0;
      const cards = data.nodes.filter((n) => n.role !== "External exit").map((n) => {
        const c = el("div", null, "progress-card"),
          steps = el("ol", null, "steps");
        c.dataset.nodeId = n.id;
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
      content.replaceChildren(
        ...(cards.length
          ? cards
          : [el("p", t("先创建线路或节点，再按照下方步骤完成安装。"), "empty")]),
      );
      picker.hidden = !cards.length;
      cards.forEach((card) => card.hidden = card.dataset.nodeId !== picker.value);
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
      const users = data.usage || [];
      if (!users.length) { root.replaceChildren(el("p", t("暂无用户。"), "empty")); return; }
      const periods = [["5h", t("近 5 小时")], ["1d", t("近 1 天")], ["7d", t("近 7 天")], ["30d", t("近 30 天")]];
      const traffic = (bytes) => `↑ ${formatBytes(bytes?.upload || 0)} · ↓ ${formatBytes(bytes?.download || 0)}`;
      const makeTable = (labels) => {
        const table = el("table"), head = el("tr"), thead = el("thead");
        labels.forEach((label) => head.append(el("th", label)));
        thead.append(head); table.append(thead, el("tbody")); return table;
      };
      let table = $(":scope > table", root);
      if (!table) { table = makeTable([t("用户"), ...periods.map(([, label]) => label), t("链路明细")]); root.replaceChildren(table); }
      const body = table.tBodies[0], rows = new Map([...body.rows].map((row) => [row.dataset.userId, row]));
      users.forEach((usage, index) => {
        let row = rows.get(usage.user_id); rows.delete(usage.user_id);
        if (!row) {
          row = el("tr"); row.dataset.userId = usage.user_id; row.append(el("th"));
          for (let i = 0; i < 5; i++) row.append(el("td"));
          const details = el("details"); details.dataset.userId = usage.user_id;
          details.append(el("summary")); row.cells[5].append(details);
        }
        const user = data.users.find((item) => item.id === usage.user_id);
        row.cells[0].textContent = user?.name || usage.user_id;
        periods.forEach(([key], i) => row.cells[i + 1].textContent = traffic(usage.windows?.[key]));
        const details = $("details", row.cells[5]), summary = $(":scope > summary", details);
        summary.textContent = t("{0} 条 Link", usage.links.length);
        let links = $(":scope > table", details), empty = $(":scope > .empty", details);
        if (usage.links.length) {
          empty?.remove();
          if (!links) { links = makeTable(["Link", ...periods.map(([, label]) => label)]); details.append(links); }
          const linkBody = links.tBodies[0], linkRows = new Map([...linkBody.rows].map((row) => [row.dataset.usageLinkId, row]));
          usage.links.forEach((link, i) => {
            let linkRow = linkRows.get(link.id); linkRows.delete(link.id);
            if (!linkRow) {
              linkRow = el("tr"); linkRow.dataset.usageLinkId = link.id; linkRow.append(el("th"));
              for (let j = 0; j < periods.length; j++) linkRow.append(el("td"));
            }
            linkRow.cells[0].textContent = link.name;
            periods.forEach(([key], j) => linkRow.cells[j + 1].textContent = traffic(link.windows?.[key]));
            if (linkBody.children[i] !== linkRow) linkBody.insertBefore(linkRow, linkBody.children[i] || null);
          });
          linkRows.forEach((row) => row.remove());
        } else {
          links?.remove();
          if (!empty) details.append(el("p", t("尚未分配 Link。"), "empty"));
        }
        if (body.children[index] !== row) body.insertBefore(row, body.children[index] || null);
      });
      rows.forEach((row) => row.remove());
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
    function updateNetworkActions() {
      const states = new Map();
      data.nodes.forEach((node) => {
        const kind = {Gateway:"gateways",Agent:"agents","External exit":"upstreams"}[node.role];
        states.set(`/admin/${kind}/${node.id}/toggle`, node.configured_enabled ?? node.enabled);
        if (node.role === "Gateway") states.set(`/admin/gateways/${node.id}/agent`, node.agent_enabled);
      });
      data.routes.forEach((route) => states.set(`/admin/attachments/${route.id}/toggle`, route.assignment_enabled));
      data.links.forEach((link) => states.set(`/admin/links/${link.id}/toggle`, link.enabled));
      $$('form[action]').forEach((form) => {
        const path = new URL(form.action).pathname;
        if (!/^\/admin\/(?:gateways|agents|attachments|links|upstreams)\/[^/]+\/(?:toggle|agent)$/.test(path) ||
            submittingForms.has(form) || BigInt(data.revision) < (sectionRevisions.get(form.closest("section")?.id) || 0n)) return;
        const button = $("button", form);
        if (!states.has(path)) { button.disabled = true; button.textContent = t("不可用"); return; }
        const enabled = states.get(path), embedded = path.endsWith("/agent");
        form.elements.enabled.value = String(!enabled);
        button.textContent = embedded ? t(enabled ? "关闭内置 Agent" : "启用内置 Agent") : t(enabled ? "action.disable" : "action.enable");
        if (!embedded && !/^\/admin\/(?:gateways|agents)\//.test(path)) {
          const row = form.closest("tr");
          const label = path.startsWith("/admin/attachments/") && row ? row.cells[3] :
            path.startsWith("/admin/upstreams/") ? $(":scope > summary > span[data-i18n]", form.parentElement) :
            $(":scope > span[data-i18n]", form.parentElement);
          if (label) { label.textContent = t(enabled ? "启用" : "停用"); if (label.hasAttribute("data-i18n")) label.dataset.i18n = enabled ? "启用" : "停用"; }
        }
      });
    }
    function updateLiveTables() {
      updateNetworkActions();
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
            if (BigInt(data.revision) >= (sectionRevisions.get("gateways") || 0n)) {
              $$('form[action$="/ingress"], form[action$="/edit"]', row).forEach((form) => {
                if (submittingForms.has(form) || formHasUnsavedChanges(form)) return;
                for (const [name, checked] of [["vmess_enabled", !node.vmess_disabled], ["hy2_enabled", !node.hy2_disabled && !!node.hy2_port]]) {
                  const input = form.elements[name];
                  if (input) input.checked = input.defaultChecked = checked;
                }
                const port = form.elements.hy2_port;
                if (port) port.value = port.defaultValue = String(node.hy2_port || 0);
                const realityPort = form.elements.reality_port;
                if (realityPort) realityPort.value = realityPort.defaultValue = String(node.reality_port || "");
              });
            }
            const autoControl = $('[name="public_host_auto"]', row);
            const ipv6Control = $('[name="ipv6_ingress_enabled"]', row);
            const realityControl = $('[name="reality_port"]', row);
            if (autoControl) autoControl.disabled = !node.public_ip_supported && !autoControl.checked;
            if (ipv6Control) ipv6Control.disabled = !node.ipv6_ingress_supported && !ipv6Control.checked;
            const canChangeRealityPort = !node.enrolled || node.status.reality_port_control;
            if (realityControl) realityControl.readOnly = !canChangeRealityPort;
            const realityUpgrade = $("[data-reality-port-upgrade]", row);
            if (realityUpgrade) realityUpgrade.hidden = canChangeRealityPort;
            config.append(el("small", node.reality_port ? `REALITY TCP ${node.reality_port}` : t("REALITY：使用节点设置（默认 TCP 8443）")));
            if (node.previous_reality_link_port) {
              config.append(el("small", t("REALITY 等待 Gateway 应用；自动 Link 暂用 TCP {0}", node.previous_reality_link_port)));
            }
            const endpoint = node.host?.includes(":") ? `[${node.host}]` : node.host || t("等待公网 IP 确认");
            cells[1].replaceChildren(
              el("div", node.vmess_disabled ? t("VMess/WS 已关闭") : `VMess/WS ${endpoint}:${node.vmess_port}${node.vmess_path}`),
              el("div", node.hy2_disabled || !node.hy2_port ? t("HY2 已关闭") : `HY2 UDP ${endpoint}:${node.hy2_port}`),
            );
            if (!node.vmess_disabled) cells[1].append(el("small", t("WS TLS 未启用")));
            if (!node.hy2_disabled && node.hy2_port) cells[1].append(el("small", t("自签证书 + SHA-256 pin")), el("small", node.status.hy2_ready ? t("HY2 就绪") : t("HY2 待应用")));
            const ipInfo = $("[data-public-ip]", row);
            if (ipInfo) ipInfo.textContent = `${node.public_host_auto ? t("自动地址") : t("手动覆盖")} · ${node.host || t("等待公网 IP 确认")} · ${t("已确认公网 IP")}: ${node.detected_public_ip || "—"} · ${node.public_ip_sources || "—"} · IPv6: ${node.detected_public_ipv6 || "—"} · ${node.public_ipv6_sources || "—"}`;
            if (node.ipv6_ingress_enabled && node.detected_public_ipv6 && node.detected_public_ipv6 !== node.host) {
              const v6 = `[${node.detected_public_ipv6}]`;
              if (!node.vmess_disabled) cells[1].append(el("div", `VMess/WS IPv6 ${v6}:${node.vmess_port}${node.vmess_path}`));
              if (!node.hy2_disabled && node.hy2_port) cells[1].append(el("div", `HY2 IPv6 UDP ${v6}:${node.hy2_port}`));
            }
            runtime.append(el("small", `VMess/WS ${node.vmess_disabled ? t("已关闭") : t("已开启")}`));
            runtime.append(el("small", `HY2 ${node.hy2_disabled || !node.hy2_port ? t("已关闭") : `UDP ${node.hy2_port} · ${node.status.hy2_ready ? t("就绪") : t("待应用")}`}`));
          }
          if (!node.status.online && node.status.last_seen && !node.status.last_seen.startsWith("0001"))
            runtime.append(el("small", t("最后上报 {0}", new Date(node.status.last_seen).toLocaleString(i18n.locale()))));
          if (node.status.last_error)
            runtime.append(el("small", node.status.last_error, "error"));
          if (node.status.apply_error)
            config.append(el("small", node.status.apply_error, "error"));
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
        const linkAddress = $("td:nth-child(2) code", row);
        if (linkAddress) linkAddress.textContent = link.url;
        const urlInput = $('form[action$="/edit"] [name="url"]', row);
        if (urlInput && urlInput.value === urlInput.defaultValue &&
            document.activeElement !== urlInput && !urlInput.form.hasAttribute("aria-busy")) {
          urlInput.value = urlInput.defaultValue = link.url;
          urlInput.required = !!link.url;
        }
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
      $$("#grant-table tr[data-grant-id]").forEach((row) => {
        if (BigInt(data.revision) < (sectionRevisions.get("grants") || 0n)) return;
        const grant = data.grants.find((g) => g.id === row.dataset.grantId);
        if (!grant) return;
        const userEnabled = data.users.some((u) => u.id === grant.user_id && u.enabled);
        const state = grant.access?.state || (grant.enabled && userEnabled
          ? grant.published
            ? "ready"
            : "pending"
          : "disabled");
        row.cells[3].replaceChildren(
          badge(state, grant.enabled ? t("启用") : t("停用")),
          el("small", t(grant.access?.reason || (!grant.enabled ? "已从订阅撤下" : !userEnabled ? "用户已停用" : grant.published ? "已发布" : "尚未发布"))),
        );
        if (grant.access) { const next = el("a", t("查看下一步")); next.href = "#" + grant.access.target; row.cells[3].append(next); }
        if (!userEnabled && grant.enabled) row.cells[3].append(el("small", t("用户已停用")));
        if (!grant.enabled) row.cells[3].append(el("small", t("已从订阅撤下")));
        row.dataset.enabled = String(grant.enabled);
        const form = $(".grant-state-form", row);
        form.elements.enabled.value = String(!grant.enabled);
        $("button", form).textContent = grant.enabled ? t("停用此授权") : t("启用此授权");
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
      updateAccessControls();
      $$("#users tr[data-user-id]").forEach((row) => {
        if (BigInt(data.revision) < (sectionRevisions.get("users") || 0n)) return;
        const user = data.users.find((u) => u.id === row.dataset.userId);
        if (!user) return;
        row.dataset.enabled = String(user.enabled);
        row.dataset.archived = String(!!user.archived);
        row.cells[1].textContent = t(user.archived ? "已归档" : user.enabled ? "启用" : "停用");
        const form = $(".user-state-form", row);
        if (!submittingForms.has(form)) {
          form.elements.enabled.value = String(!user.enabled);
          $("button", form).textContent = t(user.enabled ? "action.disable" : "action.enable");
        }
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
        if (BigInt(data.revision) < (sectionRevisions.get("subscribe") || 0n)) return;
        const user = data.users.find((u) => u.id === row.dataset.userId);
        if (user) $(".publication", row).textContent = `${user.name}（${t(user.publication)}）`;
      });
    }
    function renderLinkReports() {
      const table = $$('#links > table')[1];
      if (!table) return;
      table.dataset.liveReports = "true";
      const body = table.tBodies[0], reports = data.link_reports || [];
      const rows = new Map([...body.rows].filter(row=>row.dataset.reportKey).map(row=>[row.dataset.reportKey,row]));
      [...body.rows].filter(row=>!row.dataset.reportKey).forEach(row=>row.remove());
      reports.forEach((report,i)=>{
        const key = JSON.stringify([report.link_id, report.reporter_node_id]);
        let row = rows.get(key); rows.delete(key);
        if (!row) { row = el("tr"); row.dataset.reportKey = key; for (let j=0;j<4;j++) row.append(el("td")); }
        updateIdentityCell(row.cells[0], data.links.find(link=>link.id===report.link_id)?.name || report.link_id, report.link_id);
        updateIdentityCell(row.cells[1], nodeName(report.reporter_node_id), report.reporter_node_id);
        row.cells[2].textContent = t("{0} 条连接 · {1} 个流", report.connections, report.active_streams);
        let time = $("time",row.cells[3]), error = $(".error",row.cells[3]);
        if (!time) { time = el("time"); error = el("small","","error"); row.cells[3].append(time,error); }
        time.dateTime = report.last_seen;
        time.textContent = report.last_seen && !report.last_seen.startsWith("0001") ? new Date(report.last_seen).toLocaleString(i18n.locale()) : t("尚未上报");
        error.textContent = report.last_error || "";
        if (body.children[i] !== row) body.insertBefore(row,body.children[i]||null);
      });
      rows.forEach(row=>row.remove());
      if (!reports.length) { const row=el("tr"),cell=el("td",t("暂无运行上报"));cell.colSpan=4;row.append(cell);body.append(row); }
      labelResponsiveTables($("#links"));
      filterTable(table);
    }
    function renderNodeUpgrades() {
      const section = $("#node-upgrades");
      if (!section || !data) return;
      if ([...submittingForms].some((form) => section.contains(form)) ||
        BigInt(data.revision) < (sectionRevisions.get(section.id) || 0n)) return;
      const nodes = standaloneNodes();
      const batchForm = $('form[action="/admin/upgrades"]', section);
      if (batchForm) {
        syncChoiceRows(batchForm, "node_id", nodes.map((node) => ({id: node.id,
          name: `${node.role} ${node.name} · ${t(data.updaters?.[node.id]?.State) || t("未配对")} · ${node.status.binary_version || "—"} · ${t("{0} 条链路 / {1} 位用户", node.impact.links, node.impact.users)}`,
        })));
        syncFormDirtyState(batchForm);
      }
      const actionForm = (path, text) => {
        const form = el("form"), hidden = el("input");
        form.method = "post"; form.action = path;
        hidden.type = "hidden"; hidden.name = "csrf"; hidden.value = currentCSRF;
        form.append(hidden, el("button", text));
        return form;
      };
      const nodesBody = $("#updater-table tbody", section);
      const tasksBody = $("#upgrade-task-table tbody", section);
      if (nodesBody) {
        const rows = new Map($$("tr[data-node-id]", nodesBody).map((row) => [row.dataset.nodeId, row]));
        $$("tr:not([data-node-id])", nodesBody).forEach((row) => row.remove());
        nodes.forEach((node, index) => {
          let row = rows.get(node.id); rows.delete(node.id);
          if (!row) {
            row = el("tr"); row.dataset.nodeId = node.id;
            for (let i = 0; i < 3; i++) row.append(el("td"));
            row.cells[2].append(actionForm(`/admin/updaters/${node.role.toLowerCase()}/${encodeURIComponent(node.id)}/pair`, t("生成配对令牌")));
          }
          updateIdentityCell(row.cells[0], `${node.role} ${node.name}`, node.id);
          const up = data.updaters?.[node.id];
          row.cells[1].textContent = t("{0} · {1}/{2} · 节点 {3} · 升级助手 {4}", t(up?.State) || t("未配对"), up?.Mode || "—", up?.Arch || "—", node.status.binary_version || "—", up?.Version || "—");
          if (nodesBody.children[index] !== row) nodesBody.insertBefore(row, nodesBody.children[index] || null);
        });
        rows.forEach((row) => row.remove());
      }
      if (!tasksBody) return;
      const taskRows = new Map($$("tr[data-upgrade-task-id]", tasksBody).map((row) => [row.dataset.upgradeTaskId, row]));
      $$("tr:not([data-upgrade-task-id])", tasksBody).forEach((row) => row.remove());
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
      (data.upgrade_tasks || []).forEach((task, index) => {
        let row = taskRows.get(task.id); taskRows.delete(task.id);
        if (!row) {
          row = el("tr"); row.dataset.upgradeTaskId = task.id;
          for (let i = 0; i < 5; i++) row.append(el("td"));
        }
        updateIdentityCell(row.cells[0], `${task.role === "gateway" ? "Gateway" : "Agent"} ${nodeName(task.node_id)}`, task.node_id);
        [
          `${task.from_version || "—"} → ${task.target_version}`,
          `${t(task.stage)} · ${t("批次")} ${t(data.upgrade_batches?.[task.batch_id]?.stage) || "—"}${data.upgrade_batches?.[task.batch_id]?.error ? ": " + data.upgrade_batches[task.batch_id].error : ""} · ${task.updated_at}`,
          task.error || "",
        ].forEach((value, i) => row.cells[i + 1].textContent = value);
        const action = row.cells[4];
        action.replaceChildren();
        if (resumeTasks.has(task.id)) {
          action.append(actionForm(`/admin/upgrades/${encodeURIComponent(task.id)}/resume`, t("重新检查并继续")));
        }
        if (["pending", "preparing"].includes(task.stage)) {
          action.append(actionForm(`/admin/upgrades/${encodeURIComponent(task.id)}/cancel`, t("取消")));
        }
        if (tasksBody.children[index] !== row) tasksBody.insertBefore(row, tasksBody.children[index] || null);
      });
      taskRows.forEach((row) => row.remove());
      if (!tasksBody.children.length) {
        const row = document.createElement("tr");
        const cell = el("td", t("暂无升级任务"));
        cell.colSpan = 5;
        row.append(cell);
        tasksBody.append(row);
      }
      sectionRevisions.set(section.id, BigInt(data.revision));
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
    function updateConnectionTarget(form, missingGateways, optionalPlaceholder = t("留空：按各 Gateway 所在地使用默认值")) {
      const input = form.elements.reality_target, required = missingGateways.size > 0;
      let hint = $(".connection-target-hint, .pending-target-hint", form);
      if (!hint) {
        hint = el("p", "", "connection-target-hint muted");
        input.closest("label").after(hint);
      }
      hint.id ||= `${form.closest(".assignment-panel").id}-target-hint`;
      input.setAttribute("aria-describedby", hint.id);
      const message = required
        ? t("以下 Gateway 未设置所在地且没有已有 target：{0}。请填写 REALITY target，或先设置这些 Gateway 的所在地。", [...missingGateways.values()].join(", "))
        : "";
      input.required = required;
      input.placeholder = required ? t("必填：example.com:443（所选 Gateway 缺少默认值）") : optionalPlaceholder;
      input.setCustomValidity(required && !input.value.trim() ? message : "");
      hint.textContent = message;
      hint.hidden = !required;
    }
    function updatePendingChooser(form) {
      if (!data || BigInt(data.revision) < (sectionRevisions.get("assign") || 0n)) return;
      syncNamedOptions($(".pending-agent", form), data.nodes.filter((n) => n.role === "Agent"), t("所有 Agent"));
      syncNamedOptions($(".pending-gateway", form), data.nodes.filter((n) => n.role === "Gateway"), t("所有 Gateway"));
      const candidates = pendingConnectionCandidates(data);
      const field = $("fieldset", form);
      const rows = new Map($$(".pending-choice", form).map((row) => [$("input", row).value, row]));
      const query = $(".pending-search", form).value.trim().toLowerCase();
      const agentFilter = $(".pending-agent", form).value;
      const gatewayFilter = $(".pending-gateway", form).value;
      const manualTarget = form.elements.reality_target.value.trim();
      const plan = { created: 0, reenabled: 0 };
      let selected = 0, visible = 0, available = 0;
      const missingTargetGateways = new Map();
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
        $(".choice-state", row).textContent = `${t(candidate.state)} · ${target || t(candidate.needsTarget ? "需设置所在地或填写 target" : "保留已有 Link 配置")}${candidate.available ? "" : t(" · 节点停用或缺少公网地址")}`;
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
          if (candidate.needsTarget) missingTargetGateways.set(candidate.gateway.id, candidate.gateway.name);
        }
        if (!row.isConnected) field.append(row);
      }
      rows.forEach((row) => row.remove());
      const empty = $(".pending-empty", form);
      empty.hidden = visible > 0;
      empty.textContent = candidates.length ? t("当前筛选下没有未开启连接。") : t("所有连接均已开启，或尚未添加 Gateway / Agent。");
      $(".selection-summary", form).textContent = t("未开启 {0} · 筛选结果 {1} / 可选 {2} · 已选 {3} · 新增 {4} · 重新启用 {5}。清除选择仅影响本次提交。", candidates.length, visible, available, selected, plan.created, plan.reenabled);
      const hiddenSelected = $$(".pending-choice[hidden] input:checked", form).length;
      if (hiddenSelected) $(".selection-summary", form).textContent += " " + t("其中 {0} 项被筛选隐藏，提交仍包含这些选择。", hiddenSelected);
      updateConnectionTarget(form, missingTargetGateways);
      $("button[type=submit]", form).disabled = !selected || submittingForms.has(form);
      $("[data-pending-select]", form).disabled = !available || submittingForms.has(form);
      $("[data-pending-clear]", form).disabled = !selected || submittingForms.has(form);
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
      if (form.classList.contains("pending-chooser")) {
        if (data) updatePendingChooser(form);
        return;
      }
      const kind = form.closest("section").id,
        owner = form.elements[kind === "assign" ? "agent_id" : "user_id"].value;
      if (form.dataset.owner !== undefined && form.dataset.owner !== owner)
        $$("fieldset input[type=checkbox]", form).forEach((c) => c.checked = false);
      form.dataset.owner = owner;
      if (!data) return;
      if (kind === "assign" && BigInt(data.revision) < (sectionRevisions.get("assign") || 0n)) return;
      const query = $(".selection-search", form).value.trim().toLowerCase(),
        filter = $(".selection-filter", form).value,
        group = $(".selection-group", form).value;
      const checkboxes = $$("fieldset input[type=checkbox]", form),
        ownerActive =
          kind === "assign"
            ? data.nodes.some((n) => n.id === owner && n.enabled)
            : data.users.some((u) => u.id === owner && u.enabled && !u.archived);
      const missingTargetGateways = new Map();
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
        const needsNewLink = kind === "assign" && !data.links.some((link) => link.route_id === existing?.id);
        const target =
          kind === "assign"
            ? node?.target ||
              form.elements.reality_target.value.trim() ||
              defaultRealityTarget(node?.region)
            : "";
        $(".choice-state", row).textContent =
          kind === "assign"
            ? `${label} · ${target || t(needsNewLink ? "需设置所在地或填写 target" : "保留已有 Link 配置")}`
            : !ownerActive ? t("用户已停用或归档，请先恢复启用") : route?.issue && route.issue.state !== "ready" ? `${label} · ${t(route.issue.reason)}` : label;
        if (c.checked && !c.disabled && needsNewLink && !node.target && !defaultRealityTarget(node.region))
          missingTargetGateways.set(node.id, node.name);
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
      const hiddenSelected = checkboxes.filter((c) => c.checked && c.closest("label").hidden).length;
      if (hiddenSelected) $(".selection-summary", form).textContent += " " + t("其中 {0} 项被筛选隐藏，提交仍包含这些选择。", hiddenSelected);
      $("button[type=submit]", form).disabled = !selected.length || submittingForms.has(form) ||
        (kind === "subscribe" && accessMutationBusy());
      if (kind === "assign") {
        const targets = new Set(
          selected.map((id) => {
            const node = data.nodes.find((n) => n.id === id);
            return node?.target || defaultRealityTarget(node?.region);
          }),
        );
        updateConnectionTarget(form, missingTargetGateways,
          targets.size === 1 && !targets.has("")
            ? t("默认：{0}", [...targets][0])
            : t("留空：按各 Gateway 所在地使用默认值"));
      }
    }
    function syncNamedOptions(select, entries, allLabel = null) {
      const options = entries.map((entry) => { const option = new Option(entry.name, entry.id); option.disabled = !!entry.disabled; return option; });
      if (allLabel !== null) options.unshift(new Option(allLabel, ""));
      if (select.options.length === options.length && options.every((option, i) =>
        select.options[i].value === option.value && select.options[i].text === option.text && select.options[i].disabled === option.disabled)) return false;
      const previous = select.value, wasEmpty = !select.options.length;
      select.replaceChildren(...options);
      // Pick the first owner only when populating an initially empty list.
      // Removing the selected owner must not silently select someone else.
      if (!wasEmpty || allLabel !== null) select.value = previous;
      return true;
    }
    function syncChoiceRows(form, inputName, choices) {
      const field = $("fieldset", form),
        existing = new Map($$("fieldset > label", form).map((row) => [$("input", row).value, row])),
        retained = new Set(choices.map((choice) => choice.id));
      existing.forEach((row, id) => { if (!retained.has(id)) row.remove(); });
      let previous = $(".selection-tools", field) || $("legend", field);
      choices.forEach((choice) => {
        let row = existing.get(choice.id);
        if (!row) {
          row = el("label");
          const input = el("input");
          input.type = "checkbox";
          input.name = inputName;
          input.value = choice.id;
          row.append(input, el("span", "", "choice-name"), el("small", "", "choice-state"));
        }
        let name = $(".choice-name", row);
        if (!name) {
          // Keep the original input in place so refresh does not steal focus.
          [...row.childNodes].forEach((node) => {
            if (node !== $("input", row) && node !== $(".choice-state", row)) node.remove();
          });
          name = el("span", "", "choice-name");
          row.insertBefore(name, $(".choice-state", row));
        }
        name.textContent = choice.name;
        row.dataset.search = name.textContent.toLowerCase();
        if (previous.nextElementSibling !== row) previous.after(row);
        previous = row;
      });
    }
    function syncSubscriptionChoices() {
      if (BigInt(data.revision) < (sectionRevisions.get("subscribe") || 0n)) return;
      syncChoiceRows($("#subscribe form.chooser"), "attachment_id", data.routes.map((route) => ({
        id: route.id,
        name: routeName(route) + " · " + nodeName(route.gateway_id) + " / " + nodeName(exitID(route)) + (route.upstream_id ? " " + t("· 外部出口") : " · Agent"),
      })));
    }
    function syncAssignmentChoices() {
      if (BigInt(data.revision) < (sectionRevisions.get("assign") || 0n)) return;
      const form = $("#assign-agent-panel form.chooser");
      syncNamedOptions(form.elements.agent_id, data.nodes.filter((n) => n.role === "Agent"));
      syncChoiceRows(form, "gateway_id", data.nodes.filter((n) => n.role === "Gateway").map((node) => ({
        id: node.id, name: `${node.name} (${node.host})`,
      })));
      sectionRevisions.set("assign", BigInt(data.revision));
    }
    function enhance(root = document) {
      applyCurrentCSRF(root);
      enhanceGrantManager(root);
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
          form.dataset.owner = form.elements[section.id === "assign" ? "agent_id" : "user_id"].value;
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
        if (table.closest("section").id === "readiness" || table.id === "grant-table") return;
        const search = el("input", null, "table-search");
        search.type = "search";
        search.placeholder = t("搜索此列表");
        search.setAttribute(
          "aria-label",
          t("{0} 搜索", table.dataset.searchTitle ? t(table.dataset.searchTitle) : $("h2", table.closest("section")).textContent),
        );
        tableSearches.set(table, search);
        table.before(search);
        search.addEventListener("input", () => filterTable(table));
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
          port = quick.elements.reality_port,
          url = quick.elements.link_url,
          preview = $("#quick-reality-url", quick);
        const syncLinkAddress = () => {
          const h = quick.elements.public_host_auto?.checked ? "" : host.value.trim();
          preview.textContent = !url.value.trim() && h
            ? `reality://${h.includes(":") && !h.startsWith("[") ? "[" + h + "]" : h}:${port.value || port.placeholder}/tunnel`
            : "";
        };
        host.addEventListener("input", syncLinkAddress);
        port.addEventListener("input", syncLinkAddress);
        quick.elements.public_host_auto?.addEventListener("change", syncLinkAddress);
        url.addEventListener("input", syncLinkAddress);
        syncLinkAddress();
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
      if (refreshBusy) {
        refreshRequested = true;
        return;
      }
      refreshBusy = true;
      const refreshButton = $("#refresh-status");
      refreshButton.disabled = true;
      refreshButton.setAttribute("aria-busy", "true");
      try {
        const requestCSRF = currentCSRF;
        const response = await fetch("/admin/dashboard", {
          cache: "no-store",
          signal: AbortSignal.timeout(12000),
          headers: { Accept: "application/json" },
        });
        synchronizeCSRF(response, requestCSRF);
        checkSessionResponse(response);
        if (
          response.redirected ||
          !response.ok ||
          !response.headers.get("content-type")?.includes("application/json")
        )
          throw new Error(t("服务请求失败（HTTP {0}），请稍后重试。", response.status));
        const next = await response.json();
        if (BigInt(next.revision) < (sectionRevisions.get("grants") || 0n) ||
          BigInt(next.revision) < (sectionRevisions.get("subscribe") || 0n) ||
          BigInt(next.revision) < (sectionRevisions.get("assign") || 0n) ||
          managementSections.some((id) => BigInt(next.revision) < (sectionRevisions.get(id) || 0n))) {
          refreshRequested = true;
          return;
        }
        data = next;
        await syncGrantStructure();
        await syncManagementStructure();
        applyDeferredFormUpdates();
        syncSubscriptionChoices();
        syncAssignmentChoices();
        for (const render of [renderMetrics, renderMap, renderRouteOverview,
          renderUpstreamProbes, renderProgress, renderHistory, renderUsage, renderUserAudit, renderQuickInstall, renderEnrollments,
          renderOperations, updateLiveTables, renderLinkReports, renderNodeUpgrades, renderRuntimeDetails, updateDeletedManagement]) {
          render();
          await yieldToBrowser();
        }
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
            if (group.options.length !== desired.length + 1 ||
              desired.some((n, i) => group.options[i + 1]?.value !== n.id || group.options[i + 1]?.text !== n.name)) {
              group.replaceChildren(
                new Option(t("所有 Gateway"), ""),
                ...desired.map((n) => new Option(n.name, n.id)),
              );
              group.value = desired.some((n) => n.id === old) ? old : "";
            }
          }
          updateChooser(f);
        });
        renderUserContext();
        $$("section[data-page]>table").forEach(filterTable);
        lastSuccessfulRefresh = new Date(data.at);
        $("#refresh-note").textContent =
          t("已更新 {0} · 每 15 秒自动刷新", lastSuccessfulRefresh.toLocaleTimeString(i18n.locale()));
        $("#refresh-note").classList.remove("error");
        const expiredErrors = $$('.form-error a[href="/login"]');
        if (expiredErrors.length || $('#stale-banner a[href="/login"]')) {
          expiredErrors.forEach((link) => link.closest(".form-error").remove());
          notice(t("登录已恢复，可以继续操作。未提交的输入已保留。"));
        }
        $("#stale-banner").replaceChildren();
        $("#stale-banner").hidden = true;
      } catch (error) {
        $("#refresh-note").textContent =
          t("刷新失败 · 上次成功 {0}", lastSuccessfulRefresh ? lastSuccessfulRefresh.toLocaleTimeString(i18n.locale()) : t("无"));
        $("#refresh-note").classList.add("error");
        $("#stale-banner").textContent = t("状态数据可能已过期：{0}。上次成功刷新：{1}。", error.message, lastSuccessfulRefresh ? lastSuccessfulRefresh.toLocaleString(i18n.locale()) : t("无"));
        if (error.sessionExpired) appendLoginRecovery($("#stale-banner"));
        $("#stale-banner").hidden = false;
      } finally {
        refreshBusy = false;
        refreshButton.disabled = false;
        refreshButton.removeAttribute("aria-busy");
        if (refreshRequested) {
          refreshRequested = false;
          void refresh();
        }
      }
    }
    async function replaceSections(html, submittedForm, submittedValues) {
      const doc = new DOMParser().parseFromString(html, "text/html");
      if (!$("#page-title", doc)) {
        if ($('form[action="/login"]', doc)) throw sessionExpiredError();
        throw new Error(t("服务返回了无法识别的页面，请稍后重试。"));
      }
      const revision = BigInt($("main", doc)?.dataset.revision || "0");
      const originID = submittedForm.closest("section")?.id;
      reconcileSubscriptionLinks(doc, revision);
      reconcileUsers(doc, revision);
      // Frozen external checkboxes are omitted from FormData. A successful bulk
      // request clears its selection while preserving other forms in the table.
      if (submittedForm.id === "grant-bulk") delete submittedForm.dataset.dirty;
      else {
        const path = new URL(submittedForm.action).pathname;
        const resetSource = ["/admin/users", "/admin/gateways", "/admin/agents", "/admin/upstreams",
          "/admin/subscribe", "/admin/assign-gateways", "/admin/assign-connections"].includes(path)
          ? $(`form[action="${path}"]`, doc.getElementById(originID)) : null;
        acceptSubmittedValues(submittedForm, submittedValues, resetSource);
      }
      reconcileManagement(doc, revision);
      if (submittedForm.id === "grant-bulk")
        $$('#grant-table input[name="grant_id"]').forEach((c) => c.checked = false);
      clearFormError(submittedForm);
      const sections = $$("section[data-page]")
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
        );
      for (const section of sections) {
        // Rows already reconciled above; keep search, archive scope and open profiles.
        if (section.id === "users") continue;
        if (managementSections.includes(section.id)) continue;
        if (section.id === "grants") {
          reconcileGrants($("#grants", doc), revision);
          continue;
        }
        if (
          $("form[data-dirty]", section) ||
          (!section.contains(submittedForm) && $("details[open]", section)) ||
          [...submittingForms].some((form) => form !== submittedForm && section.contains(form)) ||
          revision < (sectionRevisions.get(section.id) || 0n)
        )
          continue;
        const incoming = doc.getElementById(section.id);
        if (!incoming) continue;
        const searches = $$(".table-search", section).map((s) => s.value);
        const chooserFilters = [".selection-search", ".selection-filter", ".selection-group",
          ".pending-search", ".pending-agent", ".pending-gateway"].flatMap((selector) => {
          const control = $(selector, section);
          return control ? [[selector, control.value]] : [];
        });
        const ownerSelector = 'form.chooser select[name="user_id"], form.chooser select[name="agent_id"]',
          chooserOwner = $(ownerSelector, section)?.value;
        section.replaceChildren(
          ...[...incoming.childNodes].map((n) =>
            document.importNode(n, true),
          ),
        );
        i18n.apply(section);
        enhance(section);
        updateAccessControls();
        if (chooserOwner !== undefined) $(ownerSelector, section).value = chooserOwner;
        chooserFilters.forEach(([selector, value]) => {
          const control = $(selector, section);
          if (!control) return;
          control.value = value;
          // A filter referring to a deleted node falls back to all nodes.
          if (control.tagName === "SELECT" && control.selectedIndex < 0) control.selectedIndex = 0;
        });
        if (data) $$("form.chooser", section).forEach(updateChooser);
        labelResponsiveTables(section);
        $$(".table-search", section).forEach((s, i) => {
          s.value = searches[i] || "";
          s.dispatchEvent(new Event("input"));
        });
        sectionRevisions.set(section.id, revision);
        if (section.id === "subscribe") renderUserContext();
        await yieldToBrowser();
      }
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
      navigate({ preserveScroll: true });
    }
    document.addEventListener("input", (event) => {
      if (
        event.target.matches(
          "input[type=search],.selection-filter,.selection-group,.pending-agent,.pending-gateway",
        )
      )
        return;
      syncFormDirtyState(event.target.closest("form"));
    });
    document.addEventListener("change", (event) => {
      if (
        event.target.closest("form") &&
        !event.target.matches(".selection-filter,.selection-group,.pending-agent,.pending-gateway")
      )
        syncFormDirtyState(event.target.closest("form"));
    });
    document.addEventListener("keydown", (event) => {
      if (event.key === "Enter" && !event.isComposing && event.target.matches('input[type="search"]'))
        event.preventDefault();
    });
    document.addEventListener("submit", async (event) => {
      const form = event.target;
      if (!form.action.includes("/admin/")) return;
      event.preventDefault();
      if (form.closest('[data-management-deleted="true"]')) return;
      if (submittingForms.has(form)) return;
      if (isAccessMutation(form) && accessMutationBusy()) return;
      submittingForms.add(form);
      form.setAttribute("aria-busy", "true");
      clearFormError(form);
      $(".form-success", form.closest("section") || document)?.remove();
      const buttons = $$("button", form);
      const submittedValues = new URLSearchParams(new FormData(form)).toString();
      const pairing = new URL(form.action).pathname.match(/^\/admin\/updaters\/(gateway|agent)\/([^/]+)\/pair$/);
      const installNodeID = pairing ? decodeURIComponent(pairing[2]) : form.elements.node_id?.value;
      const installRole = (pairing?.[1] || form.elements.role?.value) === "gateway" ? "Gateway" : "Agent";
      const installNodeName = installNodeID ? nodeName(installNodeID) : "";
      buttons.forEach((b) => (b.disabled = true));
      updateAccessControls();
      if (isAccessMutation(form)) notice(t("正在处理授权变更，请稍候。"));
      try {
        const response = await fetch(form.action, {
            method: "POST",
            body: new URLSearchParams(submittedValues),
            headers: { Accept: "text/html, text/plain" },
            signal: AbortSignal.timeout(30000),
          }),
          text = await response.text();
        synchronizeCSRF(response, new URLSearchParams(submittedValues).get("csrf"));
        checkSessionResponse(response);
        if (response.status === 403 && text.trim() === "invalid CSRF token" &&
            currentCSRF !== new URLSearchParams(submittedValues).get("csrf")) {
          const error = new Error(t("登录状态已更新，请再次提交。输入已保留。"));
          error.sessionChanged = true;
          throw error;
        }
        if (!response.ok) throw new Error(t(text.slice(0, 700).trim()));
        if (response.headers.get("content-type")?.includes("text/plain")) {
          const content = el("div"),
            pre = el("pre", text),
            copy = el("button", t("复制所选安装命令"));
          copy.type = "button";
          copy.className = "copy-install-command";
          copy.dataset.copy = text;
          content.append(
            el("p", t("目标节点：{0} {1}", installRole, installNodeName), "install-target"),
            el("p", pairing
              ? t("在目标节点宿主机执行所选命令。配对令牌已包含在命令中，无需再手动输入。")
              : t("在对应主机执行安装命令；完成后面板自动跟踪上线状态。")),
            copy,
            pre,
          );
          const blocks = [...text.matchAll(/^(Controller on-demand cache \/ systemd|Controller on-demand cache \/ Docker Compose|GitHub release \/ systemd|GitHub release \/ Docker Compose|Controller cache \/ (?:systemd )?rotate|GitHub \/ (?:systemd )?rotate|systemd|Docker Compose[^\n]*):\r?\n([^\n]+)/gm)];
          if (blocks.length) {
            const label = el("label", t("选择安装方式与下载源")), select = el("select"); select.id = "install-command-choice";
            blocks.forEach((block, index) => select.append(new Option(t(block[1]), String(index)))); label.append(select); content.prepend(label);
            const rotation = el("p", t("此选项用于更换同一节点的凭据。旧凭据有 15 分钟宽限期，新节点上报后旧凭据撤销。")); rotation.id = "install-rotation-hint"; label.after(rotation);
            const choose = () => { const block = blocks[Number(select.value)]; pre.textContent = block[2]; copy.dataset.copy = pre.textContent; rotation.hidden = !block[1].includes("rotate"); };
            select.addEventListener("change", choose); choose();
            content.prepend(el("p", pairing
              ? t("配对令牌 30 分钟内有效。重新生成配对令牌会使之前尚未使用的令牌失效。")
              : t("仅在目标节点运行所选命令。一次性令牌在本窗口显示，安装时按提示输入；未使用令牌可在安装令牌区撤销。")));
            const tokenValue = pairing ? text.match(/^Pair token[^\n]*: (\S+)/m)?.[1]
              : text.match(/^One-time enrollment token[^\n]*:\r?\n(\S+)/m)?.[1];
            if (tokenValue) {
              const token = el("pre", tokenValue, "install-token"), copyToken = el("button", pairing ? t("复制配对令牌") : t("复制一次性令牌"));
              copyToken.type = "button"; copyToken.dataset.copy = tokenValue;
              content.prepend(el("p", t("令牌仅在本窗口显示一次，30 分钟内有效。")), token, copyToken);
            }
          }
          const shown = showDetail(pairing ? t("配对升级助手：{0}", installNodeName) : t("安装节点：{0}", installNodeName), content);
          acceptSubmittedValues(form, submittedValues);
          const message = shown ? t("已生成安装选项。关闭窗口前保存所需令牌。")
            : t("安装选项已生成，关闭当前窗口后显示。当前输入已保留。");
          notice(message);
          if (!shown) detailFeedback(message);
        } else {
          await replaceSections(text, form, submittedValues);
          const dialog = form.closest("#detail-dialog");
          if (dialog) {
            const dirty = $$("form[data-dirty]", dialog).some(formHasUnsavedChanges),
              pending = [...submittingForms].some((other) => other !== form && dialog.contains(other));
            if (!dirty && !pending) closeDetail();
            else if (dirty) detailFeedback(t("提交的内容已保存；窗口内的新修改已保留，请继续保存。"));
          }
        }
      } catch (error) {
        const message = error.sessionExpired || error.sessionChanged ? error.message
          : t("操作未完成：{0}。输入已保留；网络中断时请先检查操作记录，避免重复提交。", error.message);
        notice(message, true);
        showFormError(form, message, error.sessionExpired);
      } finally {
        submittingForms.delete(form);
        form.removeAttribute("aria-busy");
        buttons.forEach((b) => (b.disabled = false));
        updateAccessControls();
        updateDeletedManagement();
        await refresh();
      }
    });
    document.addEventListener("click", async (event) => {
      const manage = event.target.closest("a[data-manage-user], a[data-manage-selected-user]");
      if (manage) {
        const id = manage.hasAttribute("data-manage-user")
          ? manage.dataset.manageUser : $('#subscribe select[name="user_id"]').value;
        userContext(id);
        $("#grant-search").value = $("#grant-state").value = "";
        $$('#grant-table input[name="grant_id"]').forEach((c) => c.checked = false);
        updateAccessControls();
        document.getElementById("grants").scrollIntoView({ block: "start" });
      }
      const grantRoute = event.target.closest("a[data-grant-route]");
      if (grantRoute) {
        const form = $("#subscribe form.chooser");
        if (selectedUser) form.elements.user_id.value = selectedUser;
        $(".selection-search", form).value = $(".selection-group", form).value = "";
        $(".selection-filter", form).value = "all";
        $$('input[name="attachment_id"]', form).forEach((c) => c.checked = c.value === grantRoute.dataset.grantRoute);
        form.dataset.dirty = "true"; updateChooser(form);
      }
      const subscribe = event.target.closest("a[data-subscribe-selected-user]");
      if (subscribe && $("#grant-user").value) {
        const form = $("#subscribe form.chooser");
        form.elements.user_id.value = $("#grant-user").value;
        updateChooser(form);
      }
      const button = event.target.closest("button[data-copy]");
      if (!button) return;
      try {
        await navigator.clipboard.writeText(button.dataset.copy);
        const previous = copyFeedback.get(button), before = previous?.label || button.textContent;
        if (previous) clearTimeout(previous.timer);
        button.textContent = t("已复制");
        copyFeedback.set(button, { label: before, timer: setTimeout(() => {
          button.textContent = before;
          copyFeedback.delete(button);
        }, 2000) });
      } catch {
        const subscription = button.closest(".subscription-row"),
          input = subscription ? $("input[readonly]", subscription) : button.previousElementSibling;
        if (typeof input?.select === "function") {
          const details = input.closest("details");
          if (details) details.open = true;
          input.focus();
          input.select();
        } else {
          const source = $$("pre,code", button.closest("#detail-body") || button.parentElement)
            .find((element) => element.textContent === button.dataset.copy);
          const selection = document.getSelection();
          if (source && selection) {
            const range = document.createRange();
            range.selectNodeContents(source);
            selection.removeAllRanges(); selection.addRange(range);
            source.scrollIntoView({block:"nearest"});
          }
        }
        notice(t("无法访问剪贴板，请手动选择并复制。"), true);
      }
    });
    $("#close-detail").addEventListener("click", requestDetailClose);
    $("#detail-dialog").addEventListener("cancel", (event) => {event.preventDefault();requestDetailClose();});
    $("#detail-dialog").addEventListener("close", () => {
      $("#detail-body").replaceChildren();
      $("#detail-feedback")?.remove();
      detailClosing = false;
      const next = detailQueue.shift();
      if (next) {
        showDetail(next.title, next.content);
        notice(t("已生成安装选项。关闭窗口前保存所需令牌。"));
      }
    });
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
    enhanceWorkspace();
    navigate();
    refresh();
    setInterval(() => {
      if (document.visibilityState === "visible") refresh();
    }, 15000);
  })();

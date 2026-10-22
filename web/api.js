/* Mihomo Studio API integration. Persisted desired state is not live until a candidate is published. */
(() => {
  'use strict';
  const API = '/api/v1';
  const state = { online: false, subscriptions: [], terminals: [], groups: [], rules: [], nodes: [], candidate: null, deployment: null, editingGroupId: null, editingTerminalId: null, editingRuleId: null, editingSubscriptionId: null };
  const $ = (id) => document.getElementById(id);
  const esc = (v) => String(v ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const labelType = (v) => ({'select':'手动选择 select','url-test':'自动测速 url-test','fallback':'故障转移 fallback','load-balance':'负载均衡 load-balance'}[v] || v);
  const identityLabel = (v) => ({source_ip:'源 IP',cidr:'CIDR 网段',mac:'MAC 地址',manual:'手动标识'}[v] || v);
  const ruleLabel = (v) => ({'DOMAIN-SUFFIX':'域名后缀','DOMAIN':'完整域名','DOMAIN-KEYWORD':'域名关键词','IP-CIDR':'IPv4 网段','IP-CIDR6':'IPv6 网段','GEOSITE':'GeoSite 规则集','GEOIP':'GeoIP 规则'}[v] || v);
  // The panel credential is the same MIHOMO_SECRET that protects the controller, matching
  // the single-secret model documented in docs/security.md. It is sent as a custom header,
  // never in the URL, so it does not appear in browser history or access logs. It is kept
  // in sessionStorage so closing the tab forgets it rather than leaving it on disk.
  const TOKEN_KEY = 'mihomoStudioAdminToken';
  const auth = { required: false, header: 'X-Admin-Token', token: '' };
  try { auth.token = sessionStorage.getItem(TOKEN_KEY) || ''; } catch (_) { auth.token = ''; }
  function setToken(value) {
    auth.token = value || '';
    try { auth.token ? sessionStorage.setItem(TOKEN_KEY, auth.token) : sessionStorage.removeItem(TOKEN_KEY); } catch (_) {}
  }
  async function request(path, options={}) {
    const headers = new Headers(options.headers || {});
    if (options.body !== undefined) headers.set('Content-Type','application/json');
    if (auth.token) headers.set(auth.header, auth.token);
    let response;
    try { response = await fetch(path, {...options, headers, credentials:'same-origin'}); }
    catch (e) {
      // A failed fetch never reaches the server, so reporting "backend not connected" without
      // the reason hides the real cause: an extension blocking private-IP requests, a dropped
      // network, or a page served from a different origin. Carry the message through.
      state.online = false;
      setConnection(false, describeFetchFailure(e, path));
      throw new Error('无法连接控制平面：' + describeFetchFailure(e, path));
    }
    if (response.status === 401) {
      state.online = false;
      // Surface the server's own explanation. It reports whether the header was missing and
      // how many characters arrived, which makes a truncated paste or a stray quote obvious
      // instead of looking like an unexplained rejection.
      let detail = '';
      try { const body = await response.json(); detail = body.error || ''; } catch (_) {}
      setToken('');
      showLoginGate(detail || '访问被拒绝，请重新输入密码。');
      throw new Error(detail || '需要管理密码才能读取或修改配置。');
    }
    if (!response.ok) {
      let detail = `HTTP ${response.status}`;
      try { const body = await response.json(); detail = body.error || detail; } catch (_) {}
      throw new Error(detail);
    }
    if (response.status === 204) return null;
    return response.json();
  }
  // describeFetchFailure turns an opaque fetch rejection into something actionable. The browser
  // deliberately hides most detail from script, but the type and message still separate a
  // blocked request from a server that answered.
  function describeFetchFailure(error, path) {
    const name = error && error.name ? error.name : 'Error';
    const message = error && error.message ? error.message : String(error);
    const hint = '请求 ' + path + ' 未能送达服务端。可能是浏览器扩展拦截了该地址、网络中断，或页面与接口不同源。';
    return hint + '（' + name + ': ' + message + '）';
  }
  // Login gate. There is no user or role model by design: one password, the same
  // MIHOMO_SECRET that protects the controller. The gate is a convenience; the server
  // rejects every unauthenticated API call regardless of what the page shows.
  function showLoginGate(message) {
    const gate = $('loginGate');
    const app = $('appRoot');
    if (gate) gate.style.display = 'flex';
    if (app) app.style.display = 'none';
    const error = $('loginError');
    if (error) {
      if (message) { error.textContent = message; error.style.display = ''; }
      else { error.style.display = 'none'; }
    }
    const intro = $('loginIntro');
    if (intro) {
      intro.textContent = auth.required
        ? '请输入管理密码。面板与 Mihomo 控制器使用同一个 secret。'
        : '本实例未启用密码校验，可直接进入。';
    }
    const field = $('loginPassword');
    if (field) { field.value = ''; setTimeout(() => field.focus(), 30); }
  }
  function hideLoginGate() {
    const gate = $('loginGate');
    const app = $('appRoot');
    if (gate) gate.style.display = 'none';
    if (app) app.style.display = '';
  }
  // Submitting the gate verifies the password against the protected verify endpoint, so a
  // wrong password is reported before the console tries to load anything.
  async function submitLogin(event) {
    if (event) event.preventDefault();
    const field = $('loginPassword');
    const button = $('loginSubmit');
    const candidate = field ? field.value.trim() : '';
    if (!candidate) { showLoginGate('请输入管理密码。'); return; }
    setToken(candidate);
    if (button) { button.disabled = true; button.textContent = '正在验证…'; }
    try {
      await request('/api/v1/auth/verify');
      const error = $('loginError');
      if (error) error.style.display = 'none';
      hideLoginGate();
      setLogoutVisible(true);
      startConsole();
    } catch (e) {
      // request() already cleared the token and reopened the gate on 401. For any other
      // failure the gate must show why, otherwise a blocked request looks like a bad password.
      showLoginGate(e.message);
    } finally {
      if (button) { button.disabled = false; button.textContent = '登录'; }
    }
  }
  function logout() {
    setToken('');
    state.online = false;
    showLoginGate('已退出登录。');
  }
  // The logout control only makes sense when a credential is actually in play.
  function setLogoutVisible(visible) {
    const button = $('logoutButton');
    if (button) button.style.display = visible ? '' : 'none';
  }
  async function loadAuthStatus() {
    try {
      const status = await request('/api/v1/auth/status');
      auth.required = !!status.required;
      if (status.header) auth.header = status.header;
      const badge = $('authNotice');
      if (badge) {
        badge.style.display = auth.required ? '' : 'none';
        badge.innerHTML = auth.required
          ? `<strong>已用管理密码登录。</strong>面板与 Mihomo 控制器共用 MIHOMO_SECRET；请求通过 <code>${esc(auth.header)}</code> 头携带，不会出现在地址栏。`
          : '';
      }
      return auth.required;
    } catch (e) {
      // The status endpoint is public, so failing to read it means the panel cannot reach the
      // server at all. Returning "no credential needed" here would send the console into a
      // load that fails for an unexplained reason; report the connection problem instead.
      auth.statusError = e.message;
      return false;
    }
  }
  function setConnection(online, reason) {
    state.online = online;
    const pill = document.querySelector('.runtime-pill');
    if (pill) { pill.style.background = online ? '#e9f8f1' : '#fff3e8'; pill.style.color = online ? '#157d5c' : '#9a5b16'; pill.querySelector('span').textContent = online ? '控制平面已连接' : '控制平面未连接'; }
    let badge = $('apiConnectionNotice');
    if (!badge) {
      badge = document.createElement('div'); badge.id='apiConnectionNotice'; badge.className='notice';
      const notice = $('workspaceNotice'); if (notice) notice.insertAdjacentElement('afterend', badge);
    }
    // When the cause is known, show it. A bare "not connected" is a guess that hides network,
    // extension, and same-origin problems behind a message that looks like the server is down.
    badge.innerHTML = online
      ? '<strong>已连接真实后端。</strong>订阅、终端、代理组、规则、节点清单、发布历史与总览统计均来自持久化数据；运行状态来自 Mihomo 控制器实时上报，连不上时会明确显示为未知而不是正常。保存配置不会立即改变运行实例，必须生成候选并完成发布。'
      : '<strong>无法连接控制平面' + (reason ? '' : '。') + '</strong>'
        + (reason ? '<br>' + esc(reason) : '当前无法读取或保存配置。为避免误导，写入操作会提示失败，不会将模拟数据报告为已保存。')
        + '<br><span class="muted tiny">排查方向：服务是否在本机可访问、浏览器扩展是否拦截了该地址、页面与接口是否同源。</span>';
    badge.style.borderColor = online ? '#bdebd7' : '#f1d5a8';
    badge.style.background = online ? '#f0fbf6' : '#fff8ec';
  }
  function groupByID(id) { return state.groups.find(x => x.id === id); }
  function groupByName(name) { return state.groups.find(x => x.name === name); }
  function groupOptions(selected='') {
    const select = $('terminalGroup');
    if (!select) return;
    select.innerHTML = state.groups.map(g => `<option value="${esc(g.id)}" ${g.id===selected?'selected':''}>${esc(g.name)}</option>`).join('');
    if (!state.groups.length) select.innerHTML = '<option value="">请先创建代理组</option>';
  }
  function ruleTargetOptions(selected='') {
    const select = $('newRuleTarget'); if (!select) return;
    select.innerHTML = '<option value="DIRECT">DIRECT · 直连</option><option value="REJECT">REJECT · 拦截</option>' + state.groups.map(g=>`<option value="${esc(g.id)}" ${g.id===selected?'selected':''}>${esc(g.name)}（代理组）</option>`).join('');
    if (selected && !['DIRECT','REJECT'].includes(selected) && !groupByID(selected)) select.value='DIRECT';
  }
  function renderGroupNodeChoices(selectedIDs=[]) {
    const tbody=document.querySelector('#groupNodeTable tbody'); if(!tbody) return;
    const subs=[...new Set(state.nodes.map(n=>n.subscription))].sort();
    const regions=[...new Set(state.nodes.map(n=>n.region).filter(Boolean))].sort();
    const subSel=$('groupSubFilter'), regionSel=$('groupRegionFilter'); const hasLatency=state.nodes.some(n=>Number.isFinite(n.latencyMs)&&!n.probeStale); $('groupLatencyFilter').disabled=!hasLatency; $('groupLatencyFilter').title=hasLatency?'按最近一次真实探测延迟筛选':'尚无真实延迟样本；请先探测已加载到运行配置的节点';
    const oldSub=subSel.value, oldRegion=regionSel.value;
    subSel.innerHTML='<option value="all">全部订阅源</option>'+subs.map(x=>`<option value="${esc(x)}">${esc(x)}</option>`).join('');
    regionSel.innerHTML='<option value="all">全部地区</option>'+regions.map(x=>`<option value="${esc(x)}">${esc(x)}</option>`).join('');
    subSel.value=subs.includes(oldSub)?oldSub:'all'; regionSel.value=regions.includes(oldRegion)?oldRegion:'all';
    const selected=new Set(selectedIDs);
    tbody.innerHTML=state.nodes.map(n=>{const st=n.probeStale?['结果过期','amber']:({available:['可用','green'],unavailable:['不可用','red'],not_loaded:['未加载','amber'],error:['探测异常','amber'],unmeasured:['未探测','amber']}[n.status]||['未探测','amber']);const latency=n.latencyMs!=null?`${n.latencyMs} ms`:'—';return `<tr data-node-id="${esc(n.id)}" data-name="${esc(n.name)}" data-sub="${esc(n.subscription)}" data-region="${esc(n.region||'')}" data-latency="${n.probeStale?'':(n.latencyMs??'')}"><td><input type="checkbox" class="group-node-check" ${selected.has(n.id)?'checked':''} onchange="updateGroupNodeCount()"></td><td>${esc(n.name)} <span class="muted tiny">${esc(n.id)}</span></td><td>${esc(n.subscription)}</td><td>${n.region?esc(n.region)+(n.regionInferred?'（推断）':''):'未知'}</td><td>${latency}</td><td><span class="tag ${st[1]}" title="${esc(n.probeMessage||n.lastCheckedAt||'')}">${st[0]}</span></td></tr>`}).join('') || '<tr><td colspan="6" class="muted" style="padding:22px;text-align:center">暂无真实节点。先添加订阅并生成候选配置；节点行不会使用原型示例。</td></tr>';
    const notice=document.querySelector('#groupModal .modal-body .notice'); if(notice) notice.innerHTML='<strong>真实节点清单：</strong>节点来自已保存的订阅快照。地区仅根据节点名称推断；延迟与状态只使用 Mihomo 控制器的真实探测结果，未加载到运行配置的节点不会被判定为网络故障。节点 ID 用于稳定引用。';
    window.filterGroupNodes();
  }
  window.filterGroupNodes = function() {
    const sub=$('groupSubFilter')?.value||'all', region=$('groupRegionFilter')?.value||'all', latency=$('groupLatencyFilter')?.value||'any', q=$('groupNameFilter')?.value.trim()||'';
    let re=null, invalid=false; if(q){try{re=new RegExp(q,'i')}catch(_){invalid=true}}
    document.querySelectorAll('#groupNodeTable tbody tr[data-node-id]').forEach(r=>{let ok=(sub==='all'||r.dataset.sub===sub)&&(region==='all'||r.dataset.region===region); if(latency!=='any') ok=ok&&r.dataset.latency!==''&&Number(r.dataset.latency)<=Number(latency); if(q){if(invalid) ok=ok&&r.dataset.name.toLowerCase().includes(q.toLowerCase()); else ok=ok&&re.test(r.dataset.name)} r.style.display=ok?'':'none';});
    window.updateGroupNodeCount();
  };
  window.updateGroupNodeCount = function() { const rows=[...document.querySelectorAll('#groupNodeTable tbody tr[data-node-id]')]; const selected=rows.filter(r=>r.querySelector('input')?.checked).length; const visible=rows.filter(r=>r.style.display!=='none').length; const el=$('groupNodeCount'); if(el) el.textContent=`已选 ${selected} 个 · 当前筛选 ${visible} 个`; };
  function renderNodes() {
    const tbody=document.querySelector('#nodeTable tbody'); if(!tbody) return;
    const groupRefs=new Map();
    state.groups.forEach(g=>{(g.memberNodeIds||[]).forEach(id=>{if(!groupRefs.has('id:'+id))groupRefs.set('id:'+id,[]);groupRefs.get('id:'+id).push(g.name)});(g.memberNodeNames||[]).forEach(n=>{if(!groupRefs.has(n))groupRefs.set(n,[]);groupRefs.get(n).push(g.name)});});
    tbody.innerHTML=state.nodes.map(n=>{
      const refs=groupRefs.get('id:'+n.id)||groupRefs.get(n.name)||groupRefs.get(n.sourceName)||[];
      const region=n.region ? `${esc(n.region)}${n.regionInferred?' · 推断':''}` : '未知';
      const latency=n.latencyMs!=null?`${n.latencyMs} ms`:'—';
      const statusMap={available:['可用','green'],unavailable:['不可用','red'],not_loaded:['未加载','amber'],error:['探测异常','amber'],unmeasured:['未探测','amber']}; const status=n.probeStale?['结果过期','amber']:(statusMap[n.status]||statusMap.unmeasured);
      return `<tr data-node-id="${esc(n.id)}"><td><div class="node"><span class="node-flag">⌘</span><div><div class="name-cell">${esc(n.name)}</div><div class="sub-cell">节点 ID：${esc(n.id)} · ${esc(n.type||'类型未知')}</div></div></div></td><td>${esc(n.subscription)}</td><td>${region}</td><td>${latency}</td><td>${refs.length?esc([...new Set(refs)].join('、')):'未引用'}</td><td><span class="tag ${status[1]}" title="${esc(n.probeMessage||n.lastCheckedAt||'')}">${status[0]}</span></td></tr>`;
    }).join('') || '<tr><td colspan="6" class="muted" style="padding:22px;text-align:center">暂无真实节点快照。先添加订阅并生成一次配置候选，系统才有可展示的节点清单。</td></tr>';
    const title=document.querySelector('#page-nodes .card-title'); if(title) title.innerHTML=`节点列表 <span class="muted" style="font-size:11px;font-weight:500">共 ${state.nodes.length} 条</span>`;
    const stats=document.querySelectorAll('#page-nodes .stats .stat-value');
    const available=state.nodes.filter(n=>n.status==='available'&&!n.probeStale).length;
    const measured=state.nodes.filter(n=>['available','unavailable'].includes(n.status)&&!n.probeStale).length;
    const currentIDs=new Set(state.nodes.map(n=>n.id)); const referencedIDs=new Set(state.groups.flatMap(g=>g.memberNodeIds||[]));
    const invalid=[...referencedIDs].filter(id=>!currentIDs.has(id)).length;
    const unmeasured=state.nodes.filter(n=>n.probeStale||n.status==='unmeasured'||n.status==='not_loaded').length;
    if(stats.length>0) stats[0].textContent=state.nodes.length;
    if(stats.length>1) stats[1].textContent=available;
    if(stats.length>2) stats[2].textContent=invalid;
    if(stats.length>3) stats[3].textContent=unmeasured;
    const feet=document.querySelectorAll('#page-nodes .stats .stat-foot');
    if(feet.length>0) feet[0].textContent=`来自 ${new Set(state.nodes.map(n=>n.subscriptionId)).size} 个已生成快照的订阅源`;
    if(feet.length>1) feet[1].textContent=measured?`已测 ${measured} 个；可用率 ${Math.round(available/measured*100)}%`:'尚无真实探测结果';
    if(feet.length>2) feet[2].textContent='当前快照中无法解析的成员 ID';
    if(feet.length>3) feet[3].textContent='未测或尚未加载到运行配置';
  }
  function renderTerminals() {
    const tbody = document.querySelector('#terminalTable tbody'); if (!tbody) return;
    tbody.innerHTML = state.terminals.map(t => {
      const g = groupByID(t.proxyGroupId);
      const verified = t.identityType === 'source_ip' || t.identityType === 'cidr' ? '待验证' : '仅台账';
      return `<tr data-id="${esc(t.id)}"><td><div class="node"><span class="node-flag">▣</span><div><div class="name-cell">${esc(t.name)}</div><div class="sub-cell">${esc(t.notes || '已保存到后端')}</div></div></div></td><td>${esc(identityLabel(t.identityType))}</td><td>${esc(t.identityValue)}</td><td><span class="tag blue">${esc(g?.name || '引用缺失')}</span></td><td><span class="tag amber">${verified}</span></td><td><button class="btn btn-quiet" data-edit-terminal="${esc(t.id)}">编辑</button> <button class="btn btn-quiet" data-delete-terminal="${esc(t.id)}">删除</button></td></tr>`;
    }).join('') || '<tr><td colspan="6" class="muted" style="padding:22px;text-align:center">后端暂无终端。点击“添加终端”创建第一条记录。</td></tr>';
    tbody.querySelectorAll('[data-edit-terminal]').forEach(b=>b.addEventListener('click',()=>openTerminalByID(b.dataset.editTerminal)));
    tbody.querySelectorAll('[data-delete-terminal]').forEach(b=>b.addEventListener('click',()=>deleteTerminal(b.dataset.deleteTerminal)));
    // Terminal statistics. "Routable" counts only identities Mihomo can enforce as rules;
    // MAC and manual identities are inventory-only, so counting them as routable would
    // overstate what the configuration can actually do.
    const boundGroups = new Set(state.terminals.map(t => t.proxyGroupId).filter(Boolean));
    const routable = state.terminals.filter(t => t.identityType === 'source_ip' || t.identityType === 'cidr');
    const count = $('terminalCount');
    if (count) count.textContent = String(state.terminals.length);
    const countFoot = $('terminalCountFoot');
    if (countFoot) countFoot.textContent = state.terminals.length ? '已保存到后端' : '还没有登记终端';
    const bound = $('terminalBoundGroups');
    if (bound) bound.textContent = String(boundGroups.size);
    const boundFoot = $('terminalBoundGroupsFoot');
    if (boundFoot) boundFoot.textContent = boundGroups.size ? '不同代理组数量' : '尚未绑定代理组';
    const routableEl = $('terminalRoutable');
    if (routableEl) routableEl.textContent = String(routable.length);
    const routableFoot = $('terminalRoutableFoot');
    if (routableFoot) {
      const ledgerOnly = state.terminals.length - routable.length;
      routableFoot.textContent = ledgerOnly > 0
        ? '另 ' + ledgerOnly + ' 个为 MAC/手动标识，仅登记不可路由'
        : '源 IP / CIDR 会生成路由规则';
    }
  }
  function renderGroups() {
    const container = document.querySelector('#page-groups .card'); if (!container) return;
    const oldRows = [...container.querySelectorAll('.group-row:not(.header)')]; oldRows.forEach(r=>r.remove());
    // The initial placeholder is not a group row, so remove it explicitly once real data is in.
    const placeholder = $('groupListPlaceholder'); if (placeholder) placeholder.remove();
    const header = container.querySelector('.group-row.header');
    const rows = state.groups.map(g => {
      const memberCount = Math.max((g.memberNodeNames || []).length,(g.memberNodeIds || []).length);
      const filter = g.memberFilter || {};
      const mode = g.selectionMode === 'manual' ? '手动指定节点' : `动态筛选${Object.keys(filter).length ? ' · '+Object.keys(filter).join(' / ') : ''}`;
      const row = document.createElement('div'); row.className='group-row'; row.dataset.id=g.id;
      row.innerHTML = `<div><div class="node"><span class="node-flag">◉</span><div><div class="node-name">${esc(g.name)}</div><div class="node-meta">${esc(g.id)}</div></div></div></div><div>${esc(labelType(g.mihomoType))}</div><div>${memberCount} 个优先/手动节点</div><div>${esc(mode)}</div><div><span class="tag ${memberCount?'green':'amber'}">${memberCount?'已配置':'待核对成员'}</span> <button class="btn btn-quiet" data-edit-group="${esc(g.id)}">编辑</button> <button class="btn btn-quiet" data-delete-group="${esc(g.id)}">删除</button></div>`;
      return row;
    });
    rows.forEach(r=>header ? header.insertAdjacentElement('afterend',r) : container.appendChild(r));
    if (!state.groups.length) {
      const empty=document.createElement('div'); empty.className='notice'; empty.textContent='后端暂无代理组。原型中的示例组未自动写入数据库，请创建实际代理组后再绑定终端或规则。';
      container.appendChild(empty);
    }
    container.querySelectorAll('[data-edit-group]').forEach(b=>b.addEventListener('click',()=>openGroupByID(b.dataset.editGroup)));
    container.querySelectorAll('[data-delete-group]').forEach(b=>b.addEventListener('click',()=>deleteGroup(b.dataset.deleteGroup)));
    groupOptions($('terminalGroup')?.value || ''); ruleTargetOptions($('newRuleTarget')?.value || '');
    const total = $('groupTotal');
    if (total) total.textContent = String(state.groups.length);
  }
  function renderRules() {
    const list=$('ruleList'); if (!list) return;
    list.innerHTML = state.rules.slice().sort((a,b)=>a.priority-b.priority).map((r,i)=>{
      const target = ['DIRECT','REJECT'].includes(r.target) ? r.target : (groupByID(r.target)?.name || '目标组不存在');
      return `<div class="rule-row" data-id="${esc(r.id)}"><span class="rule-kind">${esc(ruleLabel(r.ruleType))}</span><div class="rule-main"><strong>${esc(r.ruleValue)}</strong><span>匹配：${esc(ruleLabel(r.ruleType))} · ${esc(r.source)} · 第 ${String(i+1).padStart(2,'0')} 条${r.enabled?'':' · 已停用'}</span></div><span class="rule-target">→ ${esc(target)}</span><button class="btn btn-quiet" data-edit-rule="${esc(r.id)}">编辑</button> <button class="btn btn-quiet" data-delete-rule="${esc(r.id)}">删除</button></div>`;
    }).join('') || '<div class="notice">后端暂无规则。原型中的示例规则不会自动进入正式配置，请通过“新建规则”添加。</div>';
    list.querySelectorAll('[data-edit-rule]').forEach(b=>b.addEventListener('click',()=>openRuleByID(b.dataset.editRule)));
    list.querySelectorAll('[data-delete-rule]').forEach(b=>b.addEventListener('click',()=>deleteRule(b.dataset.deleteRule)));
    const countLabel = $('ruleCount');
    if (countLabel) {
      const enabled = state.rules.filter(r => r.enabled).length;
      countLabel.textContent = state.rules.length ? `共 ${state.rules.length} 条 · 启用 ${enabled} 条` : '尚未添加规则';
    }
    // Rules carry a source field; counting by it is honest because it is persisted data.
    const bySource = (source) => state.rules.filter(r => r.source === source).length;
    const custom = $('ruleCountCustom'); if (custom) custom.textContent = bySource('custom') + ' 条';
    const external = $('ruleCountExternal'); if (external) external.textContent = bySource('external') + ' 条';
    const builtin = $('ruleCountBuiltin'); if (builtin) builtin.textContent = bySource('built_in') + ' 条';
    loadRuleConflicts();
  }
  // Rule order analysis is read-only: it reports what cannot match and leaves the fix to the
  // operator, because reordering rules automatically could change routing intent.
  async function loadRuleConflicts() {
    const container = $('ruleConflicts');
    const tag = $('ruleConflictTag');
    if (!container) return;
    try {
      const analysis = await request(`${API}/rules/conflicts`);
      const conflicts = analysis.conflicts || [];
      if (tag) {
        tag.textContent = conflicts.length ? `发现 ${conflicts.length} 处` : `已检查 ${analysis.checked} 条`;
        tag.className = 'tag ' + (conflicts.length ? 'amber' : 'green');
        tag.title = analysis.limitations || '';
      }
      container.innerHTML = conflicts.length ? conflicts.map(c => {
        const kindLabel = c.kind === 'duplicate' ? '重复' : c.kind === 'shadowed' ? '被遮蔽' : '源网段重叠';
        return `<div class="notice" style="margin:0 0 8px;background:#fff8e8;border-color:#f2dfb7;color:#805b18">`
          + `<strong>[${kindLabel}] ${esc(c.name)}</strong><br>${esc(c.detail)}</div>`;
      }).join('') + `<div class="muted tiny" style="margin-top:6px">${esc(analysis.limitations || '')}</div>`
        : `<div class="notice" style="margin:0">未发现被前序规则完全覆盖的规则。<br><span class="muted tiny">${esc(analysis.limitations || '')}</span></div>`;
    } catch (e) {
      if (tag) { tag.textContent = '读取失败'; tag.className = 'tag amber'; }
      container.innerHTML = `<div class="notice">无法读取规则顺序检查结果：${esc(e.message)}</div>`;
    }
  }
  function renderSubscriptions() {
    const cards = document.querySelector('#page-subscriptions .card'); if (!cards) return;
    const providerCards = [...cards.querySelectorAll('.provider-card')]; providerCards.forEach(c=>c.remove());
    const anchor = cards.querySelector('.card-head');
    const list = document.createElement('div'); list.id='subscriptionList';
    list.innerHTML = state.subscriptions.map(s=>`<div class="provider-card" data-id="${esc(s.id)}"><div class="provider-logo">⇄</div><div class="provider-info"><strong>${esc(s.name)} <span class="tag ${s.enabled?'green':'amber'}" style="margin-left:6px">${s.enabled?'已启用':'已停用'}</span></strong><span>${esc(maskURL(s.url))}</span><span>最近状态：${esc(s.lastStatus || '尚未拉取')}</span></div><button class="btn btn-quiet" data-refresh-sub="${esc(s.id)}">立即刷新</button> <button class="btn btn-quiet" data-history-sub="${esc(s.id)}">快照历史</button> <button class="btn btn-quiet" data-edit-sub="${esc(s.id)}">编辑</button> <button class="btn btn-quiet" data-delete-sub="${esc(s.id)}">删除</button></div>`).join('') || '<div class="notice">后端暂无订阅源。添加后候选构建会拉取其 Mihomo YAML 输入。</div>';
    if (anchor) anchor.insertAdjacentElement('afterend',list); else cards.appendChild(list);
    list.querySelectorAll('[data-refresh-sub]').forEach(b=>b.addEventListener('click',()=>refreshSubscription(b.dataset.refreshSub)));
    list.querySelectorAll('[data-history-sub]').forEach(b=>b.addEventListener('click',()=>showSnapshotHistory(b.dataset.historySub)));
    list.querySelectorAll('[data-edit-sub]').forEach(b=>b.addEventListener('click',()=>openSubscriptionByID(b.dataset.editSub)));
    list.querySelectorAll('[data-delete-sub]').forEach(b=>b.addEventListener('click',()=>deleteSubscription(b.dataset.deleteSub)));
    renderSubscriptionSummary();
  }
  // Summarises the most recent fetch attempt. The URL is deliberately not shown in full;
  // maskURL keeps the host visible so an operator can tell sources apart without exposing
  // the token path.
  function renderSubscriptionSummary() {
    const enabled = state.subscriptions.filter(s => s.enabled).length;
    const tag = $('enabledSubscriptionsTag');
    if (tag) {
      tag.textContent = state.subscriptions.length ? `${enabled} / ${state.subscriptions.length} 个已启用` : '还没有订阅源';
      tag.className = 'tag ' + (state.subscriptions.length ? 'green' : 'blue');
    }
    const body = $('lastFetchBody');
    const statusTag = $('lastFetchTag');
    if (!body) return;
    const fetched = state.subscriptions
      .filter(s => s.lastFetchAt)
      .sort((a, b) => new Date(b.lastFetchAt) - new Date(a.lastFetchAt));
    const latest = fetched[0];
    if (!latest) {
      if (statusTag) { statusTag.textContent = '尚无记录'; statusTag.className = 'tag blue'; }
      body.innerHTML = '<div class="notice">还没有完成过订阅获取。添加订阅后点击「立即刷新」，这里会显示实际结果。</div>';
      return;
    }
    const nodesForSource = state.nodes.filter(n => n.subscriptionId === latest.id).length;
    const ok = latest.lastStatus === 'fetched';
    if (statusTag) {
      statusTag.textContent = ok ? '成功' : (latest.lastStatus || '未知');
      statusTag.className = 'tag ' + (ok ? 'green' : 'amber');
    }
    body.innerHTML =
      `<div class="summary-line"><span>订阅</span><strong>${esc(latest.name)}</strong></div>`
      + `<div class="summary-line"><span>最近获取</span><strong>${esc(formatOverviewTime(latest.lastFetchAt))}</strong></div>`
      + `<div class="summary-line"><span>解析结果</span><strong>${ok ? nodesForSource + ' 个节点' : esc(latest.lastStatus || '未知')}</strong></div>`
      + `<div class="summary-line"><span>快照节点</span><strong>${nodesForSource} 个</strong></div>`
      + (ok ? '' : '<div class="notice">本次获取未成功。系统保留了上一次成功快照，但候选构建仍要求所有启用订阅本次获取成功。</div>');
  }
  function maskURL(raw) { try { const u=new URL(raw); return u.origin + u.pathname.replace(/[^/]+$/, '••••••••'); } catch (_) { return '已保存的订阅地址'; } }
  async function refreshAll() {
    try {
      await request('/healthz'); setConnection(true);
      const [subscriptions, terminals, groups, rules, nodes] = await Promise.all([
        request(`${API}/subscriptions`), request(`${API}/terminals`), request(`${API}/proxy-groups`), request(`${API}/rules`), request(`${API}/nodes`)
      ]);
      state.subscriptions=subscriptions; state.terminals=terminals; state.groups=groups; state.rules=rules; state.nodes=nodes;
      renderGroups(); renderTerminals(); renderRules(); renderSubscriptions(); renderNodes(); renderGroupNodeChoices(state.editingGroupId ? (groupByID(state.editingGroupId)?.memberNodeIds||[]) : []);
      return true;
    } catch (e) { setConnection(false, e.message); toast(e.message); return false; }
  }
  async function runAction(action, successMessage) {
    try { await action(); await refreshAll(); toast(successMessage); return true; }
    catch(e) { toast('未保存：'+e.message); return false; }
  }
  function openGroupByID(id) {
    const g=state.groups.find(x=>x.id===id); if(!g) return toast('未找到后端代理组');
    state.editingGroupId=id;
    $('groupModalTitle').textContent='编辑代理组：'+g.name; $('groupName').value=g.name; $('groupType').value=g.mihomoType;
    const f=g.memberFilter||{}; $('groupSubFilter').value=f.subscription||'all'; $('groupRegionFilter').value=f.region||'all'; $('groupLatencyFilter').value='any'; $('groupNameFilter').value=f.regex||f.keyword||'';
    renderGroupNodeChoices(g.memberNodeIds||[]);
    document.querySelectorAll('#groupNodeTable tbody tr[data-node-id]').forEach(row=>{if(!(g.memberNodeIds||[]).length){const n=row.dataset.name; row.querySelector('input').checked=(g.memberNodeNames||[]).some(x=>x===n||x===row.dataset.sourceName);}});
    filterGroupNodes(); updateGroupTypeHelp(); openModal('groupModal');
  }
  async function deleteGroup(id) { const g=groupByID(id); if(!g||!confirm(`删除代理组「${g.name}」？如果终端或规则仍引用它，后端会拒绝删除。`)) return; await runAction(()=>request(`${API}/proxy-groups/${id}`,{method:'DELETE'}),'代理组已删除'); }
  async function deleteTerminal(id) { const t=state.terminals.find(x=>x.id===id); if(!t||!confirm(`删除终端「${t.name}」？`)) return; await runAction(()=>request(`${API}/terminals/${id}`,{method:'DELETE'}),'终端已删除'); }
  async function deleteRule(id) { const r=state.rules.find(x=>x.id===id); if(!r||!confirm(`删除规则「${r.name}」？`)) return; await runAction(()=>request(`${API}/rules/${id}`,{method:'DELETE'}),'规则已删除'); }
  async function deleteSubscription(id) { const s=state.subscriptions.find(x=>x.id===id); if(!s||!confirm(`删除订阅「${s.name}」？`)) return; await runAction(()=>request(`${API}/subscriptions/${id}`,{method:'DELETE'}),'订阅已删除'); }
  async function showSnapshotHistory(id) {
    const sub=state.subscriptions.find(x=>x.id===id); if(!sub) return;
    try {
      const history=await request(`${API}/subscriptions/${id}/snapshots`);
      if(!history.length) return toast('暂无成功快照历史；先刷新订阅。');
      const lines=history.slice(0,8).map((x,i)=>{
        const d=x.diff||{}; const rename=(d.possibleRenames||[]).map(r=>`疑似改名 ${r.from} → ${r.to}`).join('；');
        return [`${i+1}. ${x.current?'[当前] ':''}${x.fetchedAt} · ${x.proxyCount} 个节点 · ${x.sha256.slice(0,12)}`,
          `   新增 ${d.added?.length||0}，删除 ${d.removed?.length||0}，未变 ${d.unchanged||0}`,
          rename?`   ${rename}`:'',d.added?.length?`   新增：${d.added.slice(0,5).join('、')}`:'',d.removed?.length?`   删除：${d.removed.slice(0,5).join('、')}`:''].filter(Boolean).join('\n');
      });
      alert(`订阅「${sub.name}」快照历史（最多显示 8 条）\n\n${lines.join('\n\n')}\n\n疑似改名仅按代理类型做低置信度提示，不会自动修改代理组引用。`);
    } catch(e) { toast('读取快照历史失败：'+e.message); }
  }
  window.verifyRuntimeGroups = async function() {
    try {
      const result=await request(`${API}/proxy-groups/runtime-check`);
      const lines=(result.groups||[]).map(g=>`${g.name}: ${g.status} · 类型 ${g.runtimeType||'未加载'} · 当前 ${g.current||'—'} · 成员 ${(g.members||[]).length}${g.semantics?'\n  '+g.semantics:''}`);
      alert(`运行时策略组核验
时间：${result.checkedAt}
配置组：${result.configuredGroups} · 运行时组：${result.runtimeGroups} · 类型匹配：${result.typeMatched}

${lines.join('\n\n')}

${result.limitations}`);
    } catch(e) { toast('运行时策略组核验失败：'+e.message); }
  };
  window.probeNodes = async function() {
    const button=document.querySelector('#page-nodes .page-head .actions button'); if(button){button.disabled=true;button.textContent='探测中…';}
    try { const result=await request(`${API}/nodes/probe`,{method:'POST',body:JSON.stringify({})}); await refreshAll(); toast(`探测完成：可用 ${result.available}，不可用 ${result.unavailable}，未加载 ${result.notLoaded}，异常 ${result.errors}。`); }
    catch(e) { toast('节点探测未完成：'+e.message); }
    finally { if(button){button.disabled=false;button.textContent='↻ 探测节点';} }
  };
  async function refreshSubscription(id) {
    const s=state.subscriptions.find(x=>x.id===id); if(!s) return;
    if(!confirm(`立即拉取订阅「${s.name}」并更新当前节点快照？此操作不会发布运行配置。`)) return;
    try { const result=await request(`${API}/subscriptions/${id}/refresh`,{method:'POST',body:JSON.stringify({})}); await refreshAll(); toast(`订阅刷新成功：${result.proxyCount} 个节点。快照已更新，运行配置未发布。`); }
    catch(e) { await refreshAll(); toast(`订阅刷新失败，旧的成功快照会保留：${e.message}`); }
  }
  // Refreshing each enabled source in turn rather than in parallel: the sources share one
  // SQLite connection, and a failed source must not be hidden behind a concurrent success.
  window.refreshAllSubscriptions = async function refreshAllSubscriptions() {
    const targets = state.subscriptions.filter(s => s.enabled);
    if (!targets.length) return toast('没有已启用的订阅可刷新。');
    if (!confirm(`依次刷新 ${targets.length} 个已启用订阅？这只会更新输入快照，不会发布运行配置。`)) return;
    const failures = [];
    let okCount = 0;
    for (const s of targets) {
      try {
        await request(`${API}/subscriptions/${s.id}/refresh`, { method:'POST', body: JSON.stringify({}) });
        okCount += 1;
      } catch (e) {
        failures.push(s.name + '：' + e.message);
      }
    }
    await refreshAll();
    if (failures.length) {
      toast(`刷新完成：${okCount} 个成功，${failures.length} 个失败。失败的订阅会保留上一次成功快照。`);
    } else {
      toast(`全部 ${okCount} 个订阅刷新成功。快照已更新，运行配置未发布。`);
    }
  };
  function openTerminalByID(id) {
    const t=state.terminals.find(x=>x.id===id); if(!t) return;
    state.editingTerminalId=id; $('terminalModalTitle').textContent='编辑终端：'+t.name; $('terminalName').value=t.name;
    $('terminalMethod').value=({source_ip:'源 IP',cidr:'CIDR 网段',mac:'MAC 地址',manual:'其他标识'}[t.identityType]||'源 IP');
    $('terminalIdentifier').value=t.identityValue; groupOptions(t.proxyGroupId); updateTerminalMethodHelp(); openModal('terminalModal');
  }
  function openRuleByID(id) {
    const r=state.rules.find(x=>x.id===id); if(!r) return;
    state.editingRuleId=id; $('newRuleType').value=r.ruleType; $('newRuleValue').value=r.ruleValue; $('newRulePriority').value=r.priority; ruleTargetOptions(r.target);
    const title=$('ruleModal').querySelector('h2'); if(title) title.textContent='编辑流量规则'; updateRuleHelp(); updateRulePreview(); openModal('ruleModal');
  }
  function openSubscriptionByID(id) {
    const s=state.subscriptions.find(x=>x.id===id); if(!s) return;
    state.editingSubscriptionId=id; $('subName').value=s.name; $('subUrl').value=s.url; openModal('subModal');
  }
  // Replace demo-only write handlers with real API-backed persistence.
  window.openGroupEditor = function(name) {
    if(name==='new') { state.editingGroupId=null; $('groupModalTitle').textContent='新建代理组'; $('groupName').value=''; $('groupType').value='select'; $('groupSubFilter').value='all'; $('groupRegionFilter').value='all'; $('groupLatencyFilter').value='any'; $('groupNameFilter').value=''; renderGroupNodeChoices([]); updateGroupTypeHelp(); return openModal('groupModal'); }
    const g=groupByName(name); if(g) return openGroupByID(g.id);
    toast('这是原型示例组，尚未保存到后端。请先新建实际代理组。');
  };
  window.saveGroupDraft = async function() {
    const name=$('groupName').value.trim(); if(!name) return toast('请填写代理组名称');
    const selectedRows=[...document.querySelectorAll('#groupNodeTable tbody tr[data-node-id]')].filter(r=>r.querySelector('input')?.checked); const selected=selectedRows.map(r=>r.dataset.name); const selectedIDs=selectedRows.map(r=>r.dataset.nodeId);
    const sub=$('groupSubFilter').value, region=$('groupRegionFilter').value, q=$('groupNameFilter').value.trim();
    const memberFilter={}; if(sub!=='all') memberFilter.subscription=sub; if(region!=='all') memberFilter.region=region;
    if(q) { try { new RegExp(q); memberFilter.regex=q; } catch (_) { memberFilter.keyword=q; } }
    const dynamic=Object.keys(memberFilter).length>0;
    if(!selected.length && !dynamic) return toast('请至少勾选一个节点，或设置动态筛选条件');
    const body={name,mihomoType:$('groupType').value,selectionMode:dynamic?'dynamic':'manual',memberNodeIds:selectedIDs,memberNodeNames:selected,memberFilter,healthUrl:'https://www.gstatic.com/generate_204',intervalSeconds:300,toleranceMs:50};
    const url=state.editingGroupId?`${API}/proxy-groups/${state.editingGroupId}`:`${API}/proxy-groups`;
    const ok=await runAction(()=>request(url,{method:state.editingGroupId?'PUT':'POST',body:JSON.stringify(body)}),'代理组已保存到后端；运行配置尚未改变');
    if(ok) { closeModal('groupModal'); state.editingGroupId=null; }
  };
  window.openTerminalEditor = function(name) {
    if(name!=='new') { const t=state.terminals.find(x=>x.name===name); if(t) return openTerminalByID(t.id); toast('该终端是原型示例，尚未保存到后端。'); return; }
    state.editingTerminalId=null; $('terminalModalTitle').textContent='添加终端'; $('terminalName').value=''; $('terminalIdentifier').value=''; groupOptions(); $('terminalMethod').value='源 IP'; updateTerminalMethodHelp(); openModal('terminalModal');
  };
  window.saveTerminalDraft = async function() {
    const name=$('terminalName').value.trim(), value=$('terminalIdentifier').value.trim(), groupId=$('terminalGroup').value;
    if(!name||!value) return toast('请填写终端名称与识别值'); if(!groupId) return toast('请先创建代理组，再绑定终端');
    const methodMap={'源 IP':'source_ip','CIDR 网段':'cidr','MAC 地址':'mac','独立入口':'manual','其他标识':'manual'};
    const body={name,identityType:methodMap[$('terminalMethod').value]||'source_ip',identityValue:value,proxyGroupId:groupId,enabled:true,notes:''};
    const url=state.editingTerminalId?`${API}/terminals/${state.editingTerminalId}`:`${API}/terminals`;
    const ok=await runAction(()=>request(url,{method:state.editingTerminalId?'PUT':'POST',body:JSON.stringify(body)}),'终端已保存；真实流量路由仍需运行验证');
    if(ok) { closeModal('terminalModal'); state.editingTerminalId=null; }
  };
  window.saveNewRule = async function() {
    const value=$('newRuleValue').value.trim(), target=$('newRuleTarget').value, type=$('newRuleType').value;
    if(!value) return toast('请先填写匹配内容');
    if(!target) return toast('请先创建目标代理组，或选择直连/拦截');
    const priority=Number($('newRulePriority').value); if(!Number.isInteger(priority)||priority<0||priority>100000) return toast('优先级必须是 0 到 100000 的整数');
    const body={name:`${type} ${value}`,ruleType:type,ruleValue:value,target,priority,source:'custom',enabled:true};
    const url=state.editingRuleId?`${API}/rules/${state.editingRuleId}`:`${API}/rules`;
    const ok=await runAction(()=>request(url,{method:state.editingRuleId?'PUT':'POST',body:JSON.stringify(body)}),'规则已保存；需要生成候选并发布后才会生效');
    if(ok) { closeModal('ruleModal'); state.editingRuleId=null; $('newRulePriority').value='100'; const title=$('ruleModal').querySelector('h2'); if(title) title.textContent='新建流量规则'; }
  };
  window.addSubscription = async function() {
    const name=$('subName').value.trim(), urlValue=$('subUrl').value.trim(); if(!name||!urlValue) return toast('请填写订阅名称和 URL');
    const body={name,url:urlValue,enabled:true}; const url=state.editingSubscriptionId?`${API}/subscriptions/${state.editingSubscriptionId}`:`${API}/subscriptions`;
    const ok=await runAction(()=>request(url,{method:state.editingSubscriptionId?'PUT':'POST',body:JSON.stringify(body)}),'订阅已保存；本操作尚未执行拉取');
    if(ok) { closeModal('subModal'); $('subName').value=''; $('subUrl').value=''; state.editingSubscriptionId=null; }
  };
  window.createPreview = async function() {
    const button=document.querySelectorAll('#page-releases button[onclick="createPreview()"]'); button.forEach(b=>b.disabled=true);
    try {
      const candidate=await request(`${API}/config-candidates`,{method:'POST',body:JSON.stringify({})}); state.candidate=candidate;
      $('releaseEmpty').style.display='none'; $('releasePreview').style.display='block';
      $('releaseStateTag').textContent=candidate.status==='validated'?'候选已校验':'校验未通过';
      $('releaseStateTag').className='tag '+(candidate.status==='validated'?'green':'red');
      // Pull the same health checks the overview uses so the preview and the dashboard cannot
      // disagree about whether this configuration is publishable.
      let health = null;
      try { const overview = await request(`${API}/overview`); health = overview.health; } catch (_) { health = null; }
      renderReleasePreview(candidate, health);
      const confirmRelease = $('confirmRelease'); if (confirmRelease) confirmRelease.textContent = candidate.id;
      toast(candidate.status==='validated'?'候选配置已生成并通过内核校验':'候选已生成，但校验未通过：'+(candidate.validationMessage||''));
      $('step1').className='step done'; $('step1').querySelector('.step-num').textContent='✓';
      $('step2').className='step '+(candidate.status==='validated'?'done':'current'); $('step2').querySelector('.step-num').textContent=candidate.status==='validated'?'✓':'!';
      { const confirmButton=$('releasePreview').querySelector('button[onclick="confirmPublish()"]'); if(confirmButton) confirmButton.disabled=candidate.status!=='validated'; }
    } catch(e) { toast('候选生成失败：'+e.message); }
    finally { button.forEach(b=>b.disabled=false); }
  };
  window.confirmPublish = function() {
    if(!state.candidate || state.candidate.status!=='validated') return toast('必须先生成并通过校验的候选配置');
    $('confirmVariant').textContent='当前 Mihomo 主实例'; $('confirmDesc').textContent='将发布候选 '+state.candidate.id+'。只有后端确认运行状态后才会标记为已验证。'; $('confirmRelease').textContent=state.candidate.id;
    $('confirmPhrase').value=''; $('doPublish').disabled=true; openModal('confirmModal');
  };
  window.doPublish = async function() {
    if(!state.candidate || $('confirmPhrase').value!=='PUBLISH') return toast('请完成发布确认');
    try {
      const deployment=await request(`${API}/config-candidates/${state.candidate.id}/publish`,{method:'POST',body:JSON.stringify({confirm:true})}); state.deployment=deployment;
      closeModal('confirmModal'); $('releaseStateTag').textContent=deployment.status; $('releaseStateTag').className='tag '+(deployment.status==='verified'?'green':'amber');
      $('step3').className='step done'; $('step3').querySelector('.step-num').textContent='✓'; $('step4').className='step '+(deployment.status==='verified'?'done':'current'); $('step4').querySelector('.step-num').textContent=deployment.status==='verified'?'✓':'!';
      if(['unknown','published_unverified'].includes(deployment.status)) { let verify=$('verifyDeploymentButton'); if(!verify) { verify=document.createElement('button'); verify.id='verifyDeploymentButton'; verify.className='btn btn-secondary'; verify.textContent='核验运行状态'; $('releasePreview').querySelector('div[style*="justify-content:flex-end"]')?.prepend(verify); } verify.onclick=()=>verifyDeployment(deployment.id); verify.style.display='inline-block'; }
      toast(`发布状态：${deployment.status}。${deployment.runtimeMessage||''}`);
      // The recorded history is the source of truth, so re-read it rather than assuming.
      await loadDeployments();
      await loadOverview();
    } catch(e) { toast('发布未确认成功：'+e.message+'。请检查发布状态，不要盲目重复操作。'); await loadDeployments(); await loadOverview(); }
  };
  async function verifyDeployment(id) { try { const d=await request(`${API}/deployments/${id}/verify`,{method:'POST',body:JSON.stringify({})}); state.deployment=d; $('releaseStateTag').textContent=d.status; $('releaseStateTag').className='tag '+(d.status==='verified'?'green':'amber'); $('verifyDeploymentButton').style.display=d.status==='verified'?'none':'inline-block'; toast(`核验结果：${d.status}。${d.runtimeMessage||''}`); await loadDeployments(); await loadOverview(); } catch(e) { toast('运行核验未完成：'+e.message+'。状态仍按未知处理。'); } }
  // The overview is assembled server-side so the dashboard reflects one consistent read
  // instead of six requests that can disagree with each other.
  const healthGradeMeta = {
    healthy:   { label:'健康', tag:'green' },
    attention: { label:'需要关注', tag:'amber' },
    degraded:  { label:'需处理', tag:'amber' },
    blocked:   { label:'存在阻断项', tag:'red' }
  };
  function formatOverviewTime(value) {
    if (!value) return '—';
    const d = new Date(value);
    return Number.isNaN(d.getTime()) ? esc(value) : d.toLocaleString('zh-CN', { month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit' });
  }
  function renderOverview(data) {
    const stats = document.querySelectorAll('#overviewStats .stat-card');
    if (stats.length >= 4) {
      // Runtime status distinguishes "not configured" from "unreachable". Neither is
      // rendered as healthy, because neither establishes that traffic is flowing.
      const rt = data.runtime || {};
      let runtimeValue = '未配置', runtimeFoot = '未设置控制器地址，运行状态未知';
      if (rt.configured && rt.reachable) { runtimeValue = '已连接'; runtimeFoot = 'Mihomo ' + (rt.version || '未知版本'); }
      else if (rt.configured) { runtimeValue = '无法连接'; runtimeFoot = '控制器未响应，不代表已停止'; }
      stats[0].querySelector('.stat-value').textContent = runtimeValue;
      stats[0].querySelector('.stat-foot').textContent = runtimeFoot;

      const active = data.activeDeployment;
      stats[1].querySelector('.stat-value').textContent = active ? active.candidateId : '—';
      stats[1].querySelector('.stat-foot').innerHTML = active
        ? 'SHA-256 <span class="muted">' + esc((active.candidateSha256 || '').slice(0, 12)) + '…</span>'
        : '尚无已核验的发布';

      const nodes = data.nodes || {};
      stats[2].querySelector('.stat-value').textContent = String(nodes.total || 0);
      stats[2].querySelector('.stat-foot').textContent = nodes.total
        ? '已测可用 ' + (nodes.available || 0) + ' · 未测 ' + ((nodes.unmeasured || 0) + (nodes.notLoaded || 0))
        : '尚未生成节点快照';

      const health0 = data.health || {};
      const grade0 = healthGradeMeta[health0.grade] || { label:'未知' };
      stats[3].querySelector('.stat-value').textContent = health0.score != null ? String(health0.score) : '—';
      stats[3].querySelector('.stat-foot').textContent = grade0.label + ' · 阻断 ' + (health0.blockers || 0) + ' · 警告 ' + (health0.warnings || 0);
    }

    const tag = $('overviewRuntimeTag');
    if (tag) {
      const rt = data.runtime || {};
      const online = rt.configured && rt.reachable;
      tag.textContent = !rt.configured ? '未配置控制器' : (online ? '运行时已连接' : '运行时未知');
      tag.className = 'tag ' + (online ? 'green' : 'amber');
      tag.title = rt.error || '';
    }
    // The sidebar previously claimed "Mihomo online · v1.19.12" unconditionally. It now
    // reflects the same runtime fact as the dashboard, including the unknown case.
    const sideText = $('sideRuntime');
    const sideDetail = $('sideRuntimeDetail');
    const sideDot = $('sideDot');
    if (sideText && sideDetail) {
      const rt = data.runtime || {};
      const online = rt.configured && rt.reachable;
      sideText.textContent = !rt.configured ? '未配置' : (online ? '已连接' : '无法连接');
      sideDetail.textContent = !rt.configured
        ? '未设置控制器地址'
        : online ? (rt.version || '版本未知') : '控制器未响应，状态未知';
      if (sideDot) sideDot.style.background = online ? '#23c58e' : '#e5ad45';
    }

    const releaseBox = $('overviewRelease');
    if (releaseBox) {
      const active = data.activeDeployment;
      releaseBox.innerHTML = active
        ? '<div class="release-hero"><div class="release-top"><strong>' + esc(active.candidateId) + '</strong><span class="tag green">已核验</span></div>'
          + '<p>' + esc(active.runtimeMessage || '') + '</p>'
          + '<div class="release-meta"><span>发布时间 <b>' + formatOverviewTime(active.startedAt) + '</b></span>'
          + '<span>操作 <b>' + esc(active.operation) + '</b></span>'
          + '<span>SHA <b>' + esc((active.candidateSha256 || '').slice(0, 12)) + '</b></span></div></div>'
        : '<div class="notice">还没有已核验的发布。生成候选并确认发布后，这里会显示实际生效的版本。</div>';
    }

    // Health renders the score plus every contributing check with its own deduction, so the
    // number always carries its evidence.
    const health = data.health || {};
    const grade = healthGradeMeta[health.grade] || { label:'未知' };
    const summary = $('overviewHealthSummary');
    if (summary) {
      const pct = Math.max(0, Math.min(100, health.score || 0));
      const stroke = health.grade === 'blocked' ? '#d14343' : (health.grade === 'healthy' ? '#28b58b' : '#e5ad45');
      summary.innerHTML = '<div style="display:flex;align-items:center;gap:20px;margin:8px 0 18px">'
        + '<svg width="100" height="100" viewBox="0 0 100 100"><circle cx="50" cy="50" r="40" fill="none" stroke="#edf0f6" stroke-width="9"/>'
        + '<circle cx="50" cy="50" r="40" fill="none" stroke="' + stroke + '" stroke-width="9" stroke-linecap="round" stroke-dasharray="'
        + Math.round(251 * pct / 100) + ' 251" transform="rotate(-90 50 50)"/>'
        + '<text x="50" y="48" text-anchor="middle" font-size="22" font-weight="750" fill="#1b2940">' + (health.score != null ? health.score : '—') + '</text>'
        + '<text x="50" y="64" text-anchor="middle" font-size="9" fill="#8b95a8">/ 100</text></svg>'
        + '<div><div style="font-size:15px;font-weight:750">' + esc(grade.label) + '</div>'
        + '<div class="muted" style="font-size:11px;margin-top:4px">每项扣分都对应下方一条可核对的事实</div>'
        + '<div style="margin-top:9px"><span class="tag ' + (health.blockers ? 'red' : 'green') + '">BLOCK ' + (health.blockers || 0) + '</span> '
        + '<span class="tag amber">WARN ' + (health.warnings || 0) + '</span></div></div></div>';
    }
    const checkList = $('overviewHealthChecks');
    if (checkList) {
      const checks = health.checks || [];
      checkList.innerHTML = checks.length ? checks.map(c => {
        const badge = c.severity === 'block' ? '<span class="tag red">阻断</span>'
          : c.severity === 'warn' ? '<span class="tag amber">警告</span>' : '<span class="tag green">通过</span>';
        const icon = c.severity === 'ok' ? '✓' : (c.severity === 'block' ? '⚑' : '!');
        const cost = c.deduction ? '−' + c.deduction : '—';
        return '<div class="health-row"><div class="health-icon">' + icon + '</div>'
          + '<div class="health-copy"><strong>' + esc(c.title) + '</strong><span>' + esc(c.detail) + '</span></div>'
          + '<div class="health-value" style="text-align:right">' + badge
          + '<div class="muted" style="font-size:10px;margin-top:3px">' + cost + '</div></div></div>';
      }).join('') : '<div class="notice">暂无检查项。</div>';
    }

    const deployBody = $('overviewDeployments');
    if (deployBody) {
      const items = data.deployments || [];
      deployBody.innerHTML = items.length ? items.map(d => {
        const meta = deploymentStatusMeta[d.status] || { label: d.status, tag:'amber' };
        return '<tr><td><b>' + esc(d.candidateId) + '</b><div class="sub-cell">' + esc((d.candidateSha256 || '').slice(0, 12)) + '…</div></td>'
          + '<td>' + esc(d.operation) + '</td><td><span class="tag ' + meta.tag + '">' + esc(meta.label) + '</span></td>'
          + '<td>' + formatOverviewTime(d.startedAt) + '</td></tr>';
      }).join('') : '<tr><td colspan="4" class="muted" style="padding:18px;text-align:center">还没有发布记录</td></tr>';
    }

    const activity = $('overviewActivity');
    if (activity) loadOverviewActivity(activity);
    renderOverviewSettings(data);
  }
  // Recent activity comes from the audit trail, which is the only persisted lifecycle record.
  async function loadOverviewActivity(container) {
    try {
      const payload = await request(API + '/audit-events?limit=8');
      const items = payload.items || [];
      container.innerHTML = items.length ? items.map(e => {
        const tail = (e.action || '').split('.').pop();
        const meta = deploymentStatusMeta[tail] || { icon:'·' };
        return '<div class="event"><div class="event-icon">' + (meta.icon || '·') + '</div><div class="event-body">'
          + '<div class="event-title">' + esc(e.action) + '</div>'
          + '<div class="event-sub">' + esc(e.resourceType) + ' · ' + esc(e.resourceId) + '</div></div>'
          + '<div class="event-time">' + formatOverviewTime(e.createdAt) + '</div></div>';
      }).join('') : '<div class="event"><div class="event-body"><div class="event-title">还没有活动记录</div>'
        + '<div class="event-sub">发布、核验与回滚会记录在这里。</div></div></div>';
    } catch (e) {
      container.innerHTML = '<div class="event"><div class="event-body"><div class="event-title">无法读取活动记录</div>'
        + '<div class="event-sub">' + esc(e.message) + '</div></div></div>';
    }
  }
  // The settings page shows the values the running process actually uses, replacing the
  // sample paths it displayed before.
  function renderOverviewSettings(data) {
    const cfg = data.config || {};
    const rt = data.runtime || {};
    const values = {
      'Mihomo 版本': !rt.configured ? '未配置控制器' : (rt.reachable ? (rt.version || '未知') : '控制器未响应'),
      '控制器地址': cfg.controllerUrl || '未配置',
      '平台管理访问': (data.auth && data.auth.required) ? '需要访问令牌（与内核 secret 相同）' : '未启用认证（仅限本机监听）',
      '运行配置路径': cfg.activeConfigPath || '未配置',
      '快照目录': cfg.snapshotDir || '未配置',
      'Mihomo 二进制': cfg.mihomoBinary || '未配置'
    };
    document.querySelectorAll('#page-settings .setting-row').forEach(row => {
      const strong = row.querySelector('strong');
      const target = row.querySelector('[data-setting-value]');
      if (strong && target && values[strong.textContent.trim()] !== undefined) {
        target.textContent = values[strong.textContent.trim()];
      }
    });
  }
  async function loadOverview() {
    try {
      const data = await request(API + '/overview');
      renderOverview(data);
      return true;
    } catch (e) {
      const box = $('overviewRelease');
      if (box) box.innerHTML = '<div class="notice">无法读取总览数据：' + esc(e.message) + '</div>';
      return false;
    }
  }
  // The YAML viewer shows exactly what a candidate would write to the runtime. The server
  // verifies the snapshot hash before returning it, so a tampered file is reported instead
  // of being displayed as authoritative.
  window.viewCandidateYaml = async function viewCandidateYaml(candidateId) {
    if (!candidateId) return toast('还没有生成候选配置。');
    try {
      const payload = await request(`${API}/config-candidates/${candidateId}/content`);
      const title = $('yamlModalTitle');
      const desc = $('yamlModalDesc');
      const meta = $('yamlModalMeta');
      const body = $('yamlModalBody');
      if (title) title.textContent = '候选配置 YAML · ' + payload.id;
      if (desc) desc.textContent = '这是该候选在发布时会写入运行实例的精确内容。';
      if (meta) meta.innerHTML = `SHA-256 <b>${esc(payload.sha256)}</b> · 状态 <b>${esc(payload.status)}</b><br>${esc(payload.validationMessage || '')}`;
      if (body) body.textContent = payload.content;
      openModal('yamlModal');
    } catch (e) {
      toast('无法读取候选配置：' + e.message);
    }
  };
  // Opens the YAML of whichever candidate is currently verified as in effect.
  window.viewActiveYaml = async function viewActiveYaml() {
    try {
      const payload = await request(`${API}/deployments?limit=20&status=verified`);
      const latest = (payload.items || [])[0];
      if (!latest) return toast('还没有已核验的发布，无法查看生效配置。');
      await viewCandidateYaml(latest.candidateId);
    } catch (e) {
      toast('无法读取生效配置：' + e.message);
    }
  };
  // The release preview renders the candidate the server actually produced. It previously
  // showed invented check rows and a fabricated diff, which misrepresented what would ship.
  function renderReleasePreview(candidate, health) {
    const setText = (id, value) => { const el = $(id); if (el) el.textContent = value; };
    setText('previewCandidateId', candidate.id);
    setText('previewCandidateSha', (candidate.sha256 || '').slice(0, 12) + '…');
    setText('previewCandidateStatus', candidate.status === 'validated' ? '已校验' : candidate.status);

    const summary = $('previewCheckSummary');
    if (summary) {
      const blockers = health ? (health.blockers || 0) : 0;
      const warnings = health ? (health.warnings || 0) : 0;
      summary.innerHTML = `<span class="tag ${blockers ? 'red' : 'green'}">BLOCK ${blockers}</span> <span class="tag amber">WARN ${warnings}</span>`;
    }

    const checks = $('previewChecks');
    if (checks) {
      const items = (health && health.checks) || [];
      const kernelOk = candidate.status === 'validated';
      let rows = `<div class="check-item"><div class="check-mark ${kernelOk ? '' : 'warn'}">${kernelOk ? '✓' : '!'}</div>`
        + `<div class="check-copy"><strong>${kernelOk ? 'Mihomo 内核校验通过' : '内核校验未通过'}</strong>`
        + `<p>${esc(candidate.validationMessage || '')}</p></div>`
        + `<span class="tag ${kernelOk ? 'green' : 'red'}">${kernelOk ? 'PASS' : 'FAIL'}</span></div>`;
      rows += items.map(c => {
        const ok = c.severity === 'ok';
        const warn = c.severity === 'warn';
        return `<div class="check-item"><div class="check-mark ${ok ? '' : 'warn'}">${ok ? '✓' : '!'}</div>`
          + `<div class="check-copy"><strong>${esc(c.title)}</strong><p>${esc(c.detail)}</p></div>`
          + `<span class="tag ${ok ? 'green' : warn ? 'amber' : 'red'}">${ok ? 'PASS' : warn ? 'WARN' : 'BLOCK'}</span></div>`;
      }).join('');
      checks.innerHTML = rows;
    }

    const meta = $('previewMeta');
    if (meta) {
      meta.innerHTML = `候选 SHA-256：<b>${esc(candidate.sha256 || '')}</b><br>`
        + `输入指纹：<b>${esc(candidate.sourceFingerprint || '')}</b><br>`
        + `校验状态：<b>${esc(candidate.status)}</b> · 预览后输入发生变化时必须重新生成候选。`;
    }
  }
  // Deployment history is read from the backend so the release center reflects what was
  // actually recorded, not a sample timeline. Only `verified` deployments may be rolled
  // back, and that gate is enforced by the server as well.
  const deploymentStatusMeta = {
    verified:            { label:'已核验', tag:'green', icon:'✓' },
    published_unverified:{ label:'已发布·未核验', tag:'amber', icon:'!' },
    unknown:             { label:'结果未知', tag:'amber', icon:'?' },
    failed:              { label:'失败', tag:'red', icon:'×' },
    rolled_back:         { label:'已回滚', tag:'blue', icon:'↶' },
    publishing:          { label:'发布中', tag:'blue', icon:'↥' },
    prepared:            { label:'已准备', tag:'blue', icon:'·' }
  };
  function formatDeploymentTime(value) {
    if (!value) return '—';
    const d = new Date(value);
    if (Number.isNaN(d.getTime())) return esc(value);
    return d.toLocaleString('zh-CN', { month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit' });
  }
  function renderDeployments(payload) {
    const container = $('deploymentHistory'); if (!container) return;
    const items = (payload && payload.items) || [];
    if (!items.length) {
      container.innerHTML = '<div class="event"><div class="event-body"><div class="event-title">还没有发布记录</div><div class="event-sub">生成候选并确认发布后，这里会显示每一次发布与回滚的真实结果。</div></div></div>';
      return;
    }
    container.innerHTML = items.map((d, index) => {
      const meta = deploymentStatusMeta[d.status] || { label: d.status, tag:'amber', icon:'·' };
      const isCurrent = index === 0 && d.status === 'verified';
      const sha = (d.candidateSha256 || '').slice(0, 12);
      const opLabel = d.operation === 'rollback' ? '回滚发布' : '发布';
      // Rollback needs two recorded facts: this deployment was verified, and a previously
      // verified candidate was active when it ran. Explain which one is missing instead of
      // silently hiding the control, so the operator is not left guessing.
      const canRollback = d.status === 'verified' && d.previousCandidateId;
      let action = '';
      if (d.status === 'unknown' || d.status === 'published_unverified') {
        action = '<div class="event-sub">结果未经确认，不能自动回滚。请先核验运行状态；系统不会在不确定的状态下改写运行配置。</div>';
      } else if (d.status === 'verified' && !d.previousCandidateId) {
        action = '<div class="event-sub">这是最初的已核验发布，没有更早的已核验版本可以回退。</div>';
      } else if (canRollback) {
        action = `<button class="btn btn-secondary" style="margin-top:7px;padding:5px 8px" data-rollback="${esc(d.id)}">回滚到此版本</button>`;
      }
      return `<div class="event"><div class="event-icon" style="background:${meta.tag==='green'?'#e5f8ef':meta.tag==='red'?'#fff0f0':'#eef2ff'};color:${meta.tag==='green'?'#159a72':meta.tag==='red'?'#d14343':'#4169e1'}">${meta.icon}</div>`
        + `<div class="event-body"><div class="event-title">${esc(d.candidateId)} <span class="tag ${meta.tag}">${esc(meta.label)}</span>${isCurrent?' <span class="tag green">当前生效</span>':''}${d.operation==='rollback'?' <span class="tag blue">rollback</span>':''}</div>`
        + `<div class="event-sub">${esc(opLabel)} · SHA ${esc(sha)}${sha?'…':''}</div>`
        + `<div class="event-sub">${esc(d.runtimeMessage || '')}</div>${action}`
        + `<button class="btn btn-secondary" style="margin-top:7px;padding:5px 8px" data-deployment-events="${esc(d.id)}">查看操作事件</button>`
        + `<div class="event-sub" data-deployment-events-container hidden></div></div>`
        + `<div class="event-time">${formatDeploymentTime(d.startedAt)}</div></div>`;
    }).join('');
    container.querySelectorAll('[data-rollback]').forEach(button => {
      button.addEventListener('click', () => rollbackDeployment(button.dataset.rollback));
    });
    container.querySelectorAll('[data-deployment-events]').forEach(button => {
      button.addEventListener('click', () => toggleDeploymentEvents(button));
    });
  }
  async function toggleDeploymentEvents(button) {
    const row = button.closest('.event');
    const panel = row && row.querySelector('[data-deployment-events-container]');
    if (!panel) return;
    if (!panel.hidden) {
      panel.hidden = true;
      button.textContent = '查看操作事件';
      return;
    }
    panel.hidden = false;
    button.textContent = '收起操作事件';
    if (panel.dataset.loaded === 'true') return;
    panel.textContent = '正在读取事件…';
    try {
      const payload = await request(`${API}/deployments/${encodeURIComponent(button.dataset.deploymentEvents)}/events`);
      const events = payload.items || [];
      panel.innerHTML = events.length ? events.map(event =>
        `<div style="padding:7px 0;border-top:1px solid var(--border)">`
        + `<strong>${esc(event.action || '未知操作')}</strong> · ${esc(event.outcome || '状态未知')}`
        + `<div>${esc(formatDeploymentTime(event.createdAt))} · ${esc(event.resourceType || '')} / ${esc(event.resourceId || '')}</div>`
        + (event.detailJson ? `<pre style="white-space:pre-wrap;overflow-wrap:anywhere;margin:5px 0">${esc(event.detailJson)}</pre>` : '')
        + `</div>`
      ).join('') : '该发布记录没有关联的操作事件。';
      panel.dataset.loaded = 'true';
    } catch (error) {
      panel.textContent = '读取操作事件失败：' + error.message;
    }
  }
  // Keep pagination state explicit so large histories remain bounded and navigable.
  const deploymentPageSize = 20;
  let deploymentOffset = 0;
  function renderDeploymentPagination(payload) {
    const total = Number(payload.total) || 0;
    const limit = Number(payload.limit) || deploymentPageSize;
    const offset = Number(payload.offset) || 0;
    const pageInfo = $('deploymentPageInfo');
    const previous = $('deploymentPrevPage');
    const next = $('deploymentNextPage');
    if (pageInfo) pageInfo.textContent = total === 0 ? '共 0 条发布记录' : `显示第 ${offset + 1}–${Math.min(offset + (payload.items || []).length, total)} 条，共 ${total} 条`;
    if (previous) previous.disabled = offset <= 0;
    if (next) next.disabled = offset + limit >= total;
    const notice = $('deploymentHistoryNotice');
    if (notice) notice.textContent = '回滚会创建新的发布记录，不会删除历史。未知或未经核验的结果不会提供自动回滚。';
  }
  window.loadDeployments = async function loadDeployments(options = {}) {
    if (options.reset) deploymentOffset = 0;
    try {
      let payload = await request(`${API}/deployments?limit=${deploymentPageSize}&offset=${deploymentOffset}`);
      // If history shrank while this page was open, move to the last valid page instead
      // of leaving the operator with an empty page and a misleading count.
      if (deploymentOffset > 0 && payload.total <= deploymentOffset) {
        deploymentOffset = Math.max(0, Math.floor(Math.max(0, payload.total - 1) / deploymentPageSize) * deploymentPageSize);
        payload = await request(`${API}/deployments?limit=${deploymentPageSize}&offset=${deploymentOffset}`);
      }
      renderDeployments(payload);
      renderDeploymentPagination(payload);
    } catch (e) {
      const container = $('deploymentHistory');
      if (container) container.innerHTML = `<div class="event"><div class="event-body"><div class="event-title">无法读取发布记录</div><div class="event-sub">${esc(e.message)}</div></div></div>`;
      const pageInfo = $('deploymentPageInfo');
      if (pageInfo) pageInfo.textContent = '分页状态不可用，请刷新重试。';
    }
  };
  document.addEventListener('DOMContentLoaded', () => {
    const previous = $('deploymentPrevPage');
    const next = $('deploymentNextPage');
    if (previous) previous.addEventListener('click', () => {
      deploymentOffset = Math.max(0, deploymentOffset - deploymentPageSize);
      loadDeployments();
    });
    if (next) next.addEventListener('click', () => {
      deploymentOffset += deploymentPageSize;
      loadDeployments();
    });
  });
  async function rollbackDeployment(deploymentId) {
    if (!confirm('确认回滚到这个发布？系统会用它记录的上一版已核验候选重新发布，并新增一条发布记录；历史不会被删除。')) return;
    try {
      const result = await request(`${API}/deployments/${deploymentId}/rollback`, { method:'POST', body: JSON.stringify({ confirm:true }) });
      toast(`回滚结果：${result.status}。${result.runtimeMessage || ''}`);
      await loadDeployments();
    } catch (e) {
      toast('回滚未完成：' + e.message + '。请检查发布记录，不要盲目重复操作。');
      await loadDeployments();
    }
  }
  window.rollback = function(version) { toast('请从下方「版本历史」中选择真实发布记录进行回滚；演示标签不能用于回滚。'); };
  // Loads everything the console shows. Called once after the gate is satisfied, and again
  // after login, so the panel never renders before the credential is known to be valid.
  function startConsole() {
    hideLoginGate();
    refreshAll();
    // History and overview load independently of the CRUD refresh so a failing list or an
    // unreachable controller cannot blank the whole console.
    loadDeployments();
    loadOverview();
  }
  window.logout = logout;
  // Subscription refresh is an explicit operation; it updates the current input snapshot but never publishes a candidate.
  document.addEventListener('DOMContentLoaded', () => {
    const addSubButton=document.querySelector('#page-subscriptions .page-head .btn-primary');
    if(addSubButton) addSubButton.addEventListener('click',()=>{state.editingSubscriptionId=null;$('subName').value='';$('subUrl').value='';},true);
    const newRuleButton=document.querySelector('#page-rules .page-head .btn-primary');
    if(newRuleButton) newRuleButton.addEventListener('click',()=>{state.editingRuleId=null;$('newRulePriority').value='100';const title=$('ruleModal').querySelector('h2');if(title)title.textContent='新建流量规则';},true);
    const loginForm = $('loginForm');
    if (loginForm) loginForm.addEventListener('submit', submitLogin);

    // Decide whether to show the gate before any protected request runs. An unauthenticated
    // panel must never flash stale or empty console chrome first.
    loadAuthStatus().then(async required => {
      // A failed status read means the panel never reached the server. Show that instead of
      // loading the console, which would only produce a second, vaguer failure.
      if (auth.statusError) {
        setConnection(false, auth.statusError);
        showLoginGate(auth.statusError);
        return;
      }
      if (!required) { startConsole(); return; }
      setLogoutVisible(true);
      if (!auth.token) { showLoginGate(); return; }
      // A stored token may be stale (for example after the secret was rotated). Verify it
      // before trusting it, so the operator gets a clear re-prompt instead of a 401 storm.
      try {
        await request('/api/v1/auth/verify');
        startConsole();
      } catch (_) {
        showLoginGate('保存的密码已失效，请重新输入。');
      }
    });
  });
})();

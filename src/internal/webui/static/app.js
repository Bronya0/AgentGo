// === AgentGo Web UI — JavaScript ===

if (typeof marked !== 'undefined') {
  marked.setOptions({ breaks: true, gfm: true });
}

// ===== State =====
let ws = null;
let sessionId = 'default';
let currentWorkspacePath = '';
let isGenerating = false;
let currentMsgContainer = null;
let currentTextEl = null;
let currentTextBuf = '';
let currentThinkEl = null;
let currentThinkBuf = '';
let toolSeq = 0;
let pendingImages = [];
let userScrolledUp = false;
let renderPending = false;
let contextMaxTokens = 100000;
let currentModel = '';
let currentMode = 'chat';
let currentPerm = 'manual';
let renameOrig = '';

// ===== RAF Render =====
function scheduleRender() {
  if (renderPending) return;
  renderPending = true;
  requestAnimationFrame(() => {
    renderPending = false;
    if (currentTextEl && currentTextBuf !== undefined) {
      const md = (typeof marked !== 'undefined')
        ? marked.parse(currentTextBuf)
        : escapeHtml(currentTextBuf).replace(/\n/g, '<br>');
      const safe = (typeof DOMPurify !== 'undefined') ? DOMPurify.sanitize(md) : md;
      currentTextEl.innerHTML = safe;
      if (typeof hljs !== 'undefined') {
        currentTextEl.querySelectorAll('pre code').forEach(block => {
          hljs.highlightElement(block);
        });
      }
      addCopyButtons(currentTextEl);
    }
    if (currentThinkEl && currentThinkBuf !== undefined) {
      currentThinkEl.textContent = currentThinkBuf;
      currentThinkEl.scrollTop = currentThinkEl.scrollHeight;
    }
    smartScroll();
  });
}

function smartScroll() {
  if (userScrolledUp) return;
  const el = document.getElementById('chatMessages');
  el.scrollTop = el.scrollHeight;
}

document.addEventListener('DOMContentLoaded', () => {
  const el = document.getElementById('chatMessages');
  el.addEventListener('scroll', () => {
    const threshold = 100;
    userScrolledUp = (el.scrollHeight - el.scrollTop - el.clientHeight) > threshold;
    const pill = document.getElementById('newMsgPill');
    if (userScrolledUp) {
      pill.style.display = 'none';
    }
  });
});

function scrollToBottom() {
  const el = document.getElementById('chatMessages');
  el.scrollTop = el.scrollHeight;
  document.getElementById('newMsgPill').style.display = 'none';
  userScrolledUp = false;
}

// ===== Timestamp =====
function timeStr() {
  const d = new Date();
  return d.getHours().toString().padStart(2,'0') + ':' + d.getMinutes().toString().padStart(2,'0');
}

function formatTime(ts) {
  const d = new Date(ts);
  return d.getHours().toString().padStart(2,'0') + ':' + d.getMinutes().toString().padStart(2,'0');
}

// ===== WebSocket =====
function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const url = proto + '//' + location.host + '/v1/ws?session_id=' + encodeURIComponent(sessionId);
  ws = new WebSocket(url);
  ws.onopen = () => { setStatus(true); };
  ws.onclose = () => {
    setStatus(false);
    isGenerating = false;
    updateInputArea();
    setTimeout(connectWS, 3000);
  };
  ws.onerror = () => {};
  ws.onmessage = (e) => {
    try { handleEvent(JSON.parse(e.data)); } catch (_) {}
  };
}

function setStatus(connected) {
  const el = document.getElementById('wsStatus');
  const txt = document.getElementById('wsStatusText');
  if (connected) {
    el.className = 'ch-status connected';
    txt.textContent = sessionId;
  } else {
    el.className = 'ch-status disconnected';
    txt.textContent = '未连接';
  }
}

// ===== Handle Events =====
function handleEvent(data) {
  switch (data.type) {
    case 'reasoning':
      removeThinkingIndicator();
      appendAssistantReasoning(data.text);
      break;
    case 'text':
      removeThinkingIndicator();
      appendAssistantText(data.text);
      break;
    case 'tool_start':
      removeThinkingIndicator();
      appendToolCard(data.call_id, data.tool, data.args);
      break;
    case 'tool_end':
      updateToolCard(data.call_id, data.tool, data.output);
      break;
    case 'done':
    case 'aborted':
      removeThinkingIndicator();
      if (currentThinkEl) {
        currentThinkEl.closest('.think-block').classList.remove('open');
      }
      if (currentMsgContainer) {
        currentMsgContainer.querySelectorAll('.tool-group.open').forEach(g => g.classList.remove('open'));
      }
      isGenerating = false;
      currentMsgContainer = null;
      currentTextEl = null;
      currentTextBuf = '';
      currentThinkEl = null;
      currentThinkBuf = '';
      updateInputArea();
      loadWorkspaces(); // refresh sidebar
      if (data.type === 'aborted') appendSystemNote('已中止');
      break;
    case 'error':
      removeThinkingIndicator();
      appendError(data.error);
      isGenerating = false;
      currentMsgContainer = null;
      currentTextEl = null;
      currentTextBuf = '';
      currentThinkEl = null;
      currentThinkBuf = '';
      updateInputArea();
      break;
  }
}

// ===== Send / Abort =====
function sendMessage() {
  const input = document.getElementById('chatInput');
  const msg = input.value.trim();
  if ((!msg && pendingImages.length === 0) || isGenerating || !ws || ws.readyState !== WebSocket.OPEN) return;

  const empty = document.getElementById('emptyState');
  if (empty) empty.remove();
  document.getElementById('newMsgPill').style.display = 'none';

  appendUserMessage(msg, pendingImages);

  const payload = { type: 'message', message: msg || '请分析这些图片' };
  if (pendingImages.length > 0) {
    payload.images = pendingImages;
  }
  ws.send(JSON.stringify(payload));

  input.value = '';
  input.style.height = '44px';
  pendingImages = [];
  document.getElementById('inputAttachments').innerHTML = '';
  isGenerating = true;
  currentMsgContainer = null;
  currentTextEl = null;
  currentTextBuf = '';
  currentThinkEl = null;
  currentThinkBuf = '';
  userScrolledUp = false;
  updateInputArea();
  appendThinkingIndicator();
}

function abortMessage() {
  if (!ws || ws.readyState !== WebSocket.OPEN) return;
  ws.send(JSON.stringify({ type: 'abort' }));
}

function updateInputArea() {
  document.getElementById('sendBtn').style.display = isGenerating ? 'none' : '';
  document.getElementById('abortBtn').style.display = isGenerating ? '' : 'none';
}

function useSuggestion(text) {
  document.getElementById('chatInput').value = text;
  document.getElementById('chatInput').focus();
}

// ===== Workspace / Session Management =====
async function loadWorkspaces() {
  try {
    const resp = await fetch('/ui/api/workspaces');
    if (!resp.ok) return;
    const workspaces = await resp.json();
    renderSidebar(workspaces);
  } catch (e) {
    // ignore
  }
}

function renderSidebar(workspaces) {
  const container = document.getElementById('workspaceList');
  if (!workspaces || workspaces.length === 0) {
    container.innerHTML = '<div style="padding:12px;font-size:13px;color:var(--text3);text-align:center;">暂无工作区</div>';
    return;
  }

  let html = '';
  for (const ws of workspaces) {
    const isActive = ws.path === currentWorkspacePath;
    const collapsed = localStorage.getItem('ws_collapsed_' + ws.path) === 'true';

    html += '<div class="ws-group' + (collapsed ? ' collapsed' : '') + '" data-ws="' + escapeAttr(ws.path) + '">';
    html += '<div class="ws-header" onclick="toggleWorkspace(\'' + escapeAttr(ws.path) + '\')">';
    html += '<span class="ws-chevron">▼</span>';
    html += '<span class="ws-icon">📁</span>';
    html += '<span class="ws-name">' + escapeHtml(ws.name) + '</span>';
    html += '</div>';
    html += '<div class="ws-path">' + escapeHtml(ws.path) + '</div>';
    html += '<div class="ws-sessions">';

    const sessions = ws.sessions || [];
    if (sessions.length === 0) {
      html += '<div style="padding:4px 12px 4px 38px;font-size:12px;color:var(--text3);">暂无会话</div>';
    } else {
      for (const sess of sessions) {
        const isSessActive = isActive && sess.id === sessionId;
        const ts = sess.updated_at ? formatTime(sess.updated_at) : '';
        html += '<div class="sess-row' + (isSessActive ? ' active' : '') + '"';
        html += ' data-ws="' + escapeAttr(ws.path) + '" data-sess="' + escapeAttr(sess.id) + '"';
        html += ' onclick="switchSession(\'' + escapeAttr(ws.path) + '\',\'' + escapeAttr(sess.id) + '\')"';
        html += ' ondblclick="event.stopPropagation();startRenameSessRow(this,\'' + escapeAttr(sess.id) + '\')">';
        html += '<div class="sess-lead"></div>';
        html += '<span class="sess-title">' + escapeHtml(sess.id) + '</span>';
        html += '<span class="sess-ts">' + ts + '</span>';
        html += '</div>';
      }
    }

    html += '</div></div>';
  }

  container.innerHTML = html;
}

function toggleWorkspace(path) {
  const group = document.querySelector('.ws-group[data-ws="' + escapeAttr(path) + '"]');
  if (!group) return;
  const collapsed = group.classList.toggle('collapsed');
  localStorage.setItem('ws_collapsed_' + path, collapsed);
}

function switchSession(wsPath, sessId) {
  if (wsPath === currentWorkspacePath && sessId === sessionId) return;

  // Clear current messages and state
  document.getElementById('chatMessages').innerHTML = '';
  // Re-add empty state
  const empty = document.createElement('div');
  empty.className = 'empty-state';
  empty.id = 'emptyState';
  empty.innerHTML =
    '<div class="empty-icon">🤖</div>' +
    '<h3>AgentGo</h3>' +
    '<p>AI 助手已就绪。输入消息开始对话。</p>';
  document.getElementById('chatMessages').appendChild(empty);

  currentMsgContainer = null;
  currentTextEl = null;
  currentTextBuf = '';
  currentThinkEl = null;
  currentThinkBuf = '';
  isGenerating = false;
  updateInputArea();

  currentWorkspacePath = wsPath;
  sessionId = sessId;

  // Update breadcrumb
  const wsName = wsPath.split('/').pop() || wsPath;
  document.getElementById('chWorkspace').textContent = wsName;
  document.getElementById('chSession').textContent = sessId;
  document.getElementById('chSession').style.display = '';
  document.getElementById('chRenameInput').style.display = 'none';

  // Reconnect WebSocket with new session
  if (ws) ws.close();
  connectWS();

  // Update sidebar highlight
  document.querySelectorAll('.sess-row.active').forEach(r => r.classList.remove('active'));
  const row = document.querySelector('.sess-row[data-ws="' + escapeAttr(wsPath) + '"][data-sess="' + escapeAttr(sessId) + '"]');
  if (row) row.classList.add('active');
}

async function newSession() {
  try {
    const resp = await fetch('/ui/api/sessions', { method: 'PUT' });
    if (!resp.ok) return;
    const data = await resp.json();
    // Use current workspace path or fall back to the first workspace
    const wsPath = currentWorkspacePath || (await getFirstWorkspacePath());
    switchSession(wsPath, data.id);
    loadWorkspaces();
  } catch (e) {}
}

async function getFirstWorkspacePath() {
  try {
    const resp = await fetch('/ui/api/config');
    const cfg = await resp.json();
    return cfg.workspace || '';
  } catch (e) { return ''; }
}

// ===== Session Rename =====
function startRenameSession() {
  const el = document.getElementById('chSession');
  const input = document.getElementById('chRenameInput');
  renameOrig = el.textContent;
  input.value = renameOrig;
  el.style.display = 'none';
  input.style.display = '';
  input.focus();
  input.select();
}

function commitRenameSession() {
  const input = document.getElementById('chRenameInput');
  const el = document.getElementById('chSession');
  const newName = input.value.trim();
  if (newName && newName !== renameOrig) {
    el.textContent = newName;
    sessionId = newName;
    if (ws) ws.close();
    connectWS();
    loadWorkspaces();
  }
  el.style.display = '';
  input.style.display = 'none';
}

function cancelRenameSession() {
  document.getElementById('chSession').style.display = '';
  document.getElementById('chRenameInput').style.display = 'none';
}

function startRenameSessRow(row, oldId) {
  const title = row.querySelector('.sess-title');
  const input = document.createElement('input');
  input.className = 'sess-title-input';
  input.value = oldId;
  title.style.display = 'none';
  title.parentNode.insertBefore(input, title.nextSibling);
  input.focus();
  input.select();

  input.onblur = () => {
    const newId = input.value.trim();
    input.remove();
    title.style.display = '';
    if (newId && newId !== oldId) {
      // Just update display - session rename via API not implemented yet
      title.textContent = newId;
      if (oldId === sessionId) {
        document.getElementById('chSession').textContent = newId;
        sessionId = newId;
      }
    }
  };
  input.onkeydown = (e) => {
    if (e.key === 'Enter') input.blur();
    if (e.key === 'Escape') { input.value = oldId; input.blur(); }
  };
}

// ===== Chat Rendering =====
function appendUserMessage(text, images) {
  const container = document.getElementById('chatMessages');
  const div = document.createElement('div');
  div.className = 'msg msg-user';
  let imagesHtml = '';
  if (images && images.length > 0) {
    imagesHtml = '<div class="msg-images">' +
      images.map(uri => '<img src="' + uri + '" onclick="openLightbox(this.src)">').join('') +
      '</div>';
  }
  div.innerHTML =
    '<div class="msg-avatar">👤</div>' +
    '<div class="msg-body">' +
      '<div class="msg-header"><span class="msg-name">你</span><span class="msg-time">' + timeStr() + '</span></div>' +
      '<div class="msg-content">' + escapeHtml(text) + '</div>' +
      imagesHtml +
    '</div>';
  container.appendChild(div);
  smartScroll();
}

function appendThinkingIndicator() {
  removeThinkingIndicator();
  const container = document.getElementById('chatMessages');
  const el = document.createElement('div');
  el.id = 'thinkingIndicator';
  el.className = 'msg msg-assistant';
  el.innerHTML =
    '<div class="msg-avatar">🤖</div>' +
    '<div class="msg-body">' +
      '<div class="msg-header"><span class="msg-name">Agent</span><span class="msg-time">' + timeStr() + '</span></div>' +
      '<div class="thinking-indicator">' +
        '<span>处理中</span>' +
        '<div class="thinking-dot"></div>' +
        '<div class="thinking-dot"></div>' +
        '<div class="thinking-dot"></div>' +
      '</div>' +
    '</div>';
  container.appendChild(el);
  smartScroll();
}
function removeThinkingIndicator() {
  const el = document.getElementById('thinkingIndicator');
  if (el) el.remove();
}

function ensureContainer() {
  if (!currentMsgContainer) {
    const container = document.getElementById('chatMessages');
    const div = document.createElement('div');
    div.className = 'msg msg-assistant';
    div.innerHTML =
      '<div class="msg-avatar">🤖</div>' +
      '<div class="msg-body">' +
        '<div class="msg-header"><span class="msg-name">Agent</span><span class="msg-time">' + timeStr() + '</span></div>' +
      '</div>';
    container.appendChild(div);
    currentMsgContainer = div.querySelector('.msg-body');
  }
}
function ensureTextSegment() {
  ensureContainer();
  if (!currentTextEl) {
    const bubble = document.createElement('div');
    bubble.className = 'msg-bubble';
    currentMsgContainer.appendChild(bubble);
    currentTextEl = bubble;
    currentTextBuf = '';
  }
}

function appendAssistantReasoning(text) {
  ensureContainer();
  if (!currentThinkEl) {
    const block = document.createElement('div');
    block.className = 'think-block open';
    block.innerHTML =
      '<div class="think-block-header" onclick="this.parentElement.classList.toggle(\'open\')">' +
      '<em class="think-chevron">▶</em> <span>思考过程</span>' +
      '</div>' +
      '<div class="think-block-body"></div>';
    const firstBubble = currentMsgContainer.querySelector('.msg-bubble');
    if (firstBubble) currentMsgContainer.insertBefore(block, firstBubble);
    else currentMsgContainer.appendChild(block);
    currentThinkEl = block.querySelector('.think-block-body');
    currentThinkBuf = '';
  }
  currentThinkBuf += text;
  scheduleRender();
}

function appendAssistantText(text) {
  ensureTextSegment();
  currentTextBuf += text;
  scheduleRender();
}

// ===== Tool Cards =====
function ensureToolGroup() {
  ensureContainer();
  const last = currentMsgContainer.lastElementChild;
  if (last && last.classList.contains('tool-group')) return last;
  const group = document.createElement('div');
  group.className = 'tool-group open';
  group.innerHTML =
    '<div class="tool-group-header" onclick="this.parentElement.classList.toggle(\'open\')">' +
      '<em class="tool-group-chevron">▶</em> <span>🔧 工具调用</span>' +
      '<span class="tool-group-counter">0</span>' +
    '</div>' +
    '<div class="tool-group-body"></div>';
  currentMsgContainer.appendChild(group);
  return group;
}
function updateToolGroupCounter(group) {
  const counter = group.querySelector('.tool-group-counter');
  const count = group.querySelectorAll('.tool-card').length;
  const running = group.querySelectorAll('.tool-status.running').length;
  counter.textContent = count + ' 个调用' + (running > 0 ? ' · ' + running + ' 运行中' : ' · 全部完成');
}
function appendToolCard(callId, name, args) {
  ensureContainer();
  currentTextEl = null;
  currentTextBuf = '';
  const group = ensureToolGroup();
  const body = group.querySelector('.tool-group-body');
  const card = document.createElement('div');
  card.className = 'tool-card';
  card.setAttribute('data-call-id', callId || ('tool-' + (++toolSeq)));
  card.innerHTML =
    '<div class="tool-card-header" onclick="this.parentElement.classList.toggle(\'expanded\')">' +
      '<span class="tool-icon">🔧</span>' +
      '<span class="tool-name">' + escapeHtml(name) + '</span>' +
      '<span class="tool-summary">调用工具</span>' +
      '<span class="tool-status running">运行中...</span>' +
    '</div>' +
    '<div class="tool-card-body">' +
      '<div class="tool-section"><div class="tool-section-label">参数</div><div class="tool-args">' + escapeHtml(args || '{}') + '</div></div>' +
      '<div class="tool-section"><div class="tool-section-label">输出</div><div class="tool-output">等待结果...</div></div>' +
    '</div>';
  body.appendChild(card);
  updateToolGroupCounter(group);
  body.scrollTop = body.scrollHeight;
  smartScroll();
}
function updateToolCard(callId, name, output) {
  let card = null;
  if (callId) card = document.querySelector('.tool-card[data-call-id="' + callId + '"]');
  if (!card) {
    const cards = document.querySelectorAll('.tool-card');
    card = cards[cards.length - 1];
  }
  if (!card) return;
  const status = card.querySelector('.tool-status');
  status.className = 'tool-status done';
  status.textContent = '完成 ✓';
  const summary = card.querySelector('.tool-summary');
  if (summary) {
    const out = (output || '(无输出)').replace(/\s+/g, ' ').trim();
    summary.textContent = out ? out.slice(0, 48) : '无输出';
  }
  const outputEl = card.querySelector('.tool-output');
  if (outputEl) outputEl.textContent = output || '(无输出)';
  const group = card.closest('.tool-group');
  if (group) updateToolGroupCounter(group);
}

// ===== Code Copy =====
function addCopyButtons(container) {
  container.querySelectorAll('pre').forEach(pre => {
    if (pre.querySelector('.code-copy-btn')) return;
    const btn = document.createElement('button');
    btn.className = 'code-copy-btn';
    btn.textContent = '复制';
    btn.onclick = (e) => {
      e.stopPropagation();
      const code = pre.querySelector('code');
      const text = code ? code.textContent : pre.textContent;
      navigator.clipboard.writeText(text).then(() => {
        btn.textContent = '已复制';
        btn.classList.add('copied');
        setTimeout(() => { btn.textContent = '复制'; btn.classList.remove('copied'); }, 2000);
      });
    };
    pre.style.position = 'relative';
    pre.appendChild(btn);
  });
}

// ===== Error / System Notes =====
function appendError(err) {
  const container = document.getElementById('chatMessages');
  const div = document.createElement('div');
  div.className = 'msg msg-assistant';
  div.innerHTML =
    '<div class="msg-avatar" style="background:rgba(248,113,113,0.2)">⚠️</div>' +
    '<div class="msg-body">' +
      '<div class="msg-header"><span class="msg-name" style="color:var(--red)">错误</span></div>' +
      '<div class="msg-bubble" style="border:1px solid var(--red);color:var(--red)">' + escapeHtml(err) + '</div>' +
    '</div>';
  container.appendChild(div);
  smartScroll();
}
function appendSystemNote(text) {
  const container = document.getElementById('chatMessages');
  const div = document.createElement('div');
  div.style.cssText = 'text-align:center;font-size:12px;color:var(--text2);margin:6px 0;';
  div.textContent = text;
  container.appendChild(div);
  smartScroll();
}

// ===== Context Ring =====
function updateContextRing(usedTokens) {
  const max = contextMaxTokens || 100000;
  const pct = Math.min(100, Math.round((usedTokens / max) * 100));
  const arc = document.getElementById('ctxRingArc');
  const label = document.getElementById('ctxLabel');
  const circ = 2 * Math.PI * 8; // r=8
  const offset = circ - (pct / 100) * circ;
  arc.setAttribute('stroke-dasharray', circ);
  arc.setAttribute('stroke-dashoffset', Math.max(0, offset));
  arc.classList.toggle('warning', pct >= 70 && pct < 90);
  arc.classList.toggle('danger', pct >= 90);
  label.textContent = pct + '%';
}

// ===== Config Loading =====
async function loadConfig() {
  try {
    const resp = await fetch('/ui/api/config');
    const cfg = await resp.json();
    contextMaxTokens = cfg.max_context_tokens || 100000;
    currentModel = cfg.model || '';
    if (currentModel) {
      document.getElementById('chModel').textContent = '🤖 ' + currentModel;
      document.getElementById('modelPill').textContent = currentModel;
    }
    if (cfg.workspace && !currentWorkspacePath) {
      currentWorkspacePath = cfg.workspace;
      document.getElementById('chWorkspace').textContent = cfg.workspace_name || cfg.workspace.split('/').pop();
      document.getElementById('chSession').textContent = sessionId;
    }
  } catch (e) {}
}

// ===== Sidebar Toggle =====
function toggleSidebar() {
  const app = document.getElementById('app');
  const collapsed = app.classList.toggle('sidebar-collapsed');
  localStorage.setItem('sidebar_collapsed', collapsed);
}

// ===== Resize Handle =====
function initResizeHandle() {
  const handle = document.getElementById('resizeHandle');
  let startX, startW;
  handle.addEventListener('mousedown', (e) => {
    startX = e.clientX;
    startW = parseInt(getComputedStyle(document.documentElement).getPropertyValue('--side-w'));
    handle.classList.add('resizing');
    document.addEventListener('mousemove', onMove);
    document.addEventListener('mouseup', onUp);
  });
  function onMove(e) {
    const w = Math.max(180, Math.min(400, startW + (e.clientX - startX)));
    document.documentElement.style.setProperty('--side-w', w + 'px');
  }
  function onUp() {
    handle.classList.remove('resizing');
    document.removeEventListener('mousemove', onMove);
    document.removeEventListener('mouseup', onUp);
  }
}

// ===== Dropdowns =====
function closeAllDropdowns() {
  document.getElementById('modelDropdown').style.display = 'none';
  document.getElementById('modeDropdown').style.display = 'none';
  document.getElementById('permDropdown').style.display = 'none';
  document.getElementById('dropdownOverlay').classList.remove('open');
}

function toggleModelDropdown() {
  closeAllDropdowns();
  const dd = document.getElementById('modelDropdown');
  if (dd.style.display === 'block') return;
  dd.style.display = 'block';
  dd.innerHTML = '<div class="dropdown-item" onclick="selectModel(\'' + escapeAttr(currentModel) + '\')" style="color:var(--text);cursor:default;">' + escapeHtml(currentModel || '(未配置)') + '</div>';
  dd.innerHTML += '<div class="dropdown-sep"></div>';
  dd.innerHTML += '<div class="dropdown-item" style="color:var(--text3);cursor:default;">模型由服务端配置</div>';
  // Position dropdown
  const pill = event && event.currentTarget || document.getElementById('modelPill');
  const rect = pill.getBoundingClientRect();
  dd.style.top = (rect.top - dd.offsetHeight - 4) + 'px';
  dd.style.right = (window.innerWidth - rect.right) + 'px';
  document.getElementById('dropdownOverlay').classList.add('open');
}

function toggleModeDropdown() {
  closeAllDropdowns();
  const dd = document.getElementById('modeDropdown');
  if (dd.style.display === 'block') return;
  const modes = ['chat', 'plan', 'goal'];
  dd.innerHTML = modes.map(m =>
    '<div class="dropdown-item' + (m === currentMode ? ' active' : '') + '" onclick="selectMode(\'' + m + '\')">' +
      '<span class="di-check">' + (m === currentMode ? '✓' : '') + '</span>' + m +
    '</div>'
  ).join('');
  dd.style.display = 'block';
  const pill = document.getElementById('modePill');
  const rect = pill.getBoundingClientRect();
  dd.style.bottom = (window.innerHeight - rect.top + 4) + 'px';
  dd.style.left = rect.left + 'px';
  document.getElementById('dropdownOverlay').classList.add('open');
}

function togglePermDropdown() {
  closeAllDropdowns();
  const dd = document.getElementById('permDropdown');
  if (dd.style.display === 'block') return;
  const perms = ['manual', 'auto', 'yolo'];
  dd.innerHTML = perms.map(p =>
    '<div class="dropdown-item' + (p === currentPerm ? ' active' : '') + '" onclick="selectPerm(\'' + p + '\')">' +
      '<span class="di-check">' + (p === currentPerm ? '✓' : '') + '</span>' + p +
    '</div>'
  ).join('');
  dd.style.display = 'block';
  const pill = document.getElementById('permPill');
  const rect = pill.getBoundingClientRect();
  dd.style.bottom = (window.innerHeight - rect.top + 4) + 'px';
  dd.style.left = rect.left + 'px';
  document.getElementById('dropdownOverlay').classList.add('open');
}

function selectModel(m) {
  closeAllDropdowns();
}

function selectMode(m) {
  currentMode = m;
  document.getElementById('modePill').textContent = m;
  closeAllDropdowns();
}

function selectPerm(p) {
  currentPerm = p;
  document.getElementById('permPill').textContent = p;
  closeAllDropdowns();
}

// ===== Preview Panel =====
function openPreview(title, content) {
  document.getElementById('app').classList.add('preview-open');
  document.getElementById('pvTitle').textContent = title;
  document.getElementById('previewBody').innerHTML = content;
}
function closePreview() {
  document.getElementById('app').classList.remove('preview-open');
}

// ===== Image Handling =====
document.addEventListener('paste', (e) => {
  const items = e.clipboardData && e.clipboardData.items;
  if (!items) return;
  for (const item of items) {
    if (item.type.startsWith('image/')) {
      e.preventDefault();
      const file = item.getAsFile();
      if (file) addImageFile(file);
    }
  }
});

const chatPanel = document.getElementById('conversation');
const dragOverlay = document.getElementById('dragOverlay');
let dragCounter = 0;
chatPanel.addEventListener('dragenter', (e) => {
  e.preventDefault(); dragCounter++;
  if (e.dataTransfer.types.includes('Files')) dragOverlay.classList.add('visible');
});
chatPanel.addEventListener('dragleave', (e) => {
  e.preventDefault(); dragCounter--;
  if (dragCounter <= 0) { dragCounter = 0; dragOverlay.classList.remove('visible'); }
});
chatPanel.addEventListener('dragover', (e) => e.preventDefault());
chatPanel.addEventListener('drop', (e) => {
  e.preventDefault(); dragCounter = 0; dragOverlay.classList.remove('visible');
  const files = e.dataTransfer.files;
  for (const file of files) { if (file.type.startsWith('image/')) addImageFile(file); }
});

function triggerImageUpload() { document.getElementById('imageFileInput').click(); }
function handleFileSelect(e) {
  for (const file of e.target.files) { if (file.type.startsWith('image/')) addImageFile(file); }
  e.target.value = '';
}
function addImageFile(file) {
  if (file.size > 20 * 1024 * 1024) { alert('图片大小不能超过 20MB'); return; }
  if (pendingImages.length >= 5) { alert('最多同时附加 5 张图片'); return; }
  const reader = new FileReader();
  reader.onload = () => { pendingImages.push(reader.result); renderAttachmentPreviews(); };
  reader.readAsDataURL(file);
}
function renderAttachmentPreviews() {
  document.getElementById('inputAttachments').innerHTML = pendingImages.map((uri, idx) =>
    '<div class="attach-preview"><img src="' + uri + '"><button class="attach-remove" onclick="removeAttachment(' + idx + ')">✕</button></div>'
  ).join('');
}
function removeAttachment(idx) { pendingImages.splice(idx, 1); renderAttachmentPreviews(); }

// ===== Lightbox =====
function openLightbox(src) {
  document.getElementById('lightboxImg').src = src;
  document.getElementById('lightbox').classList.add('open');
}
function closeLightbox() { document.getElementById('lightbox').classList.remove('open'); }
document.addEventListener('keydown', (e) => { if (e.key === 'Escape') closeLightbox(); });

// ===== Textarea =====
const chatInput = document.getElementById('chatInput');
chatInput.addEventListener('input', function () {
  this.style.height = '44px';
  this.style.height = Math.min(this.scrollHeight, 120) + 'px';
});
chatInput.addEventListener('keydown', function (e) {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendMessage(); }
});

// ===== Helpers =====
function escapeHtml(s) {
  if (!s) return '';
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}
function escapeAttr(s) {
  if (!s) return '';
  return s.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

// ===== Auto-resize from collapsed state =====
(function() {
  if (localStorage.getItem('sidebar_collapsed') === 'true') {
    document.getElementById('app').classList.add('sidebar-collapsed');
  }
})();

// ===== Init =====
async function initApp() {
  updateInputArea();
  initResizeHandle();
  await loadConfig(); // 先获取工作区路径等配置
  if (currentWorkspacePath) {
    await newSession(); // 新建空会话作为当前会话，旧会话出现在侧边栏但不自动加载
  } else {
    connectWS(); // 兜底：用 default 连接
  }
  loadWorkspaces(); // 加载工作区和历史会话到侧边栏
}
initApp();

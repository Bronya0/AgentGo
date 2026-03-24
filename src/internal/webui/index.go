package webui

// indexHTML 是内嵌的单页应用 HTML。
const indexHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>AgentGo Dashboard</title>
<script src="https://cdn.jsdelivr.net/npm/marked@13/marked.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/dompurify@3/dist/purify.min.js"></script>
<style>
* { margin:0; padding:0; box-sizing:border-box; }
:root {
  --bg: #0f0f11; --bg2: #1a1a1f; --bg3: #24242b;
  --border: #2e2e38; --text: #e4e4e7; --text2: #9898a4;
  --accent: #6366f1; --accent2: #818cf8; --green: #34d399;
  --orange: #fb923c; --red: #f87171; --blue: #60a5fa;
  --radius: 10px; --mono: 'SF Mono', 'Cascadia Code', 'Fira Code', monospace;
}
body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  background: var(--bg); color: var(--text); height: 100vh;
  display: flex; overflow: hidden;
}

/* --- sidebar --- */
.sidebar {
  width: 260px; min-width: 260px; background: var(--bg2);
  border-right: 1px solid var(--border); display: flex; flex-direction: column;
}
.sidebar-header {
  padding: 20px 16px 12px; border-bottom: 1px solid var(--border);
}
.sidebar-header h1 {
  font-size: 18px; font-weight: 700; display: flex; align-items: center; gap: 8px;
}
.sidebar-header h1 .dot { width:8px; height:8px; border-radius:50%; background:var(--green); }
.sidebar-header .subtitle { font-size: 12px; color: var(--text2); margin-top: 4px; }
.sidebar-nav { padding: 12px 8px; flex: 1; overflow-y: auto; }
.nav-item {
  display: flex; align-items: center; gap: 10px; padding: 10px 12px;
  border-radius: var(--radius); cursor: pointer; transition: all 0.15s;
  font-size: 14px; color: var(--text2); margin-bottom: 2px;
}
.nav-item:hover { background: var(--bg3); color: var(--text); }
.nav-item.active { background: var(--accent); color: #fff; }
.nav-item .icon { font-size: 16px; width: 20px; text-align: center; }
.sidebar-sessions {
  padding: 0 8px 8px; border-top: 1px solid var(--border);
  max-height: 200px; overflow-y: auto;
}
.sidebar-sessions .label {
  font-size: 11px; text-transform: uppercase; color: var(--text2);
  padding: 12px 12px 6px; letter-spacing: 0.5px;
}
.sess-item {
  display: flex; align-items: center; justify-content: space-between;
  padding: 7px 12px; border-radius: 6px; cursor: pointer;
  font-size: 13px; color: var(--text2); transition: all 0.15s;
}
.sess-item:hover { background: var(--bg3); color: var(--text); }
.sess-item.active { color: var(--accent2); }
.sess-item .count { font-size: 11px; background: var(--bg3); padding: 1px 6px; border-radius: 8px; }

/* --- main content --- */
.main { flex:1; display: flex; flex-direction: column; overflow: hidden; }

/* --- top bar --- */
.topbar {
  height: 52px; padding: 0 20px; display: flex; align-items: center;
  justify-content: space-between; border-bottom: 1px solid var(--border);
  background: var(--bg2); flex-shrink: 0;
}
.topbar .title { font-size: 15px; font-weight: 600; }
.topbar .status {
  font-size: 12px; padding: 4px 10px; border-radius: 20px;
  display: flex; align-items: center; gap: 6px;
}
.status-dot { width:6px; height:6px; border-radius:50%; }
.status-connected .status-dot { background: var(--green); }
.status-connected { color: var(--green); background: rgba(52,211,153,0.1); }
.status-disconnected .status-dot { background: var(--red); }
.status-disconnected { color: var(--red); background: rgba(248,113,113,0.1); }

/* --- chat panel --- */
.panel { flex:1; display:none; flex-direction:column; overflow:hidden; }
.panel.active { display: flex; }
.chat-messages {
  flex: 1; overflow-y: auto; padding: 20px; scroll-behavior: smooth;
}
.msg {
  max-width: 85%; margin-bottom: 16px; animation: fadeIn 0.2s;
}
@keyframes fadeIn { from { opacity:0; transform:translateY(6px); } to { opacity:1; transform:none; } }
.msg-user { margin-left: auto; }
.msg-user .msg-bubble {
  background: var(--accent); color: #fff; border-radius: 16px 16px 4px 16px;
  padding: 10px 16px; font-size: 14px; line-height: 1.6;
}
.msg-assistant .msg-bubble {
  background: var(--bg3); border-radius: 16px 16px 16px 4px;
  padding: 12px 16px; font-size: 14px; line-height: 1.6;
}
.msg-assistant .msg-bubble pre {
  background: var(--bg); border-radius: 6px; padding: 10px 12px;
  margin: 8px 0; overflow-x: auto; font-family: var(--mono); font-size: 13px;
}
.msg-assistant .msg-bubble code {
  font-family: var(--mono); font-size: 13px; background: var(--bg);
  padding: 1px 5px; border-radius: 3px;
}
.msg-assistant .msg-bubble pre code { background: none; padding: 0; }
.msg-label { font-size: 11px; color: var(--text2); margin-bottom: 4px; padding: 0 4px; }
.msg-user .msg-label { text-align: right; }

/* tool call cards */
.tool-card {
  background: rgba(255,255,255,0.02); border: 1px solid rgba(255,255,255,0.06); border-radius: 8px;
  margin: 8px 0; overflow: hidden; font-size: 12px;
}
.tool-card-header {
  display: flex; align-items: center; gap: 8px; padding: 7px 10px;
  background: rgba(255,255,255,0.02); cursor: pointer;
}
.tool-card-header .tool-icon { font-size: 12px; opacity: 0.7; }
.tool-card-header .tool-name {
  font-family: var(--mono); font-weight: 600; color: var(--text2);
}
.tool-card-header .tool-summary { color: var(--text2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; flex: 1; }
.tool-card-header .tool-status { font-size: 11px; color: var(--text2); }
.tool-card-header .tool-status.running { color: var(--blue); }
.tool-card-header .tool-status.done { color: var(--green); }
.tool-card-body {
  padding: 8px 10px; font-family: var(--mono); color: var(--text2);
  max-height: 180px; overflow-y: auto; white-space: pre-wrap; word-break: break-word;
  display: none;
}
.tool-card.expanded .tool-card-body { display: block; }
.tool-section-label {
  color: var(--text2); opacity: 0.8; margin-bottom: 4px; text-transform: uppercase; font-size: 10px;
}
.tool-section + .tool-section { margin-top: 10px; }

/* --- input area --- */
.chat-input-area {
  padding: 12px 20px 16px; border-top: 1px solid var(--border);
  background: var(--bg2); flex-shrink: 0;
}
.input-row { display: flex; gap: 8px; align-items: flex-end; }
.input-row textarea {
  flex: 1; background: var(--bg3); border: 1px solid var(--border);
  border-radius: var(--radius); padding: 10px 14px; color: var(--text);
  font-size: 14px; resize: none; height: 44px; max-height: 120px;
  font-family: inherit; outline: none; transition: border 0.15s;
}
.input-row textarea:focus { border-color: var(--accent); }
.input-row textarea::placeholder { color: var(--text2); }
.send-btn {
  width: 44px; height: 44px; border-radius: var(--radius); border: none;
  background: var(--accent); color: #fff; cursor: pointer; font-size: 18px;
  display: flex; align-items: center; justify-content: center;
  transition: background 0.15s; flex-shrink: 0;
}
.send-btn:hover { background: var(--accent2); }
.send-btn:disabled { opacity: 0.5; cursor: not-allowed; }

/* --- overview panel --- */
.overview { padding: 24px; overflow-y: auto; }
.overview h2 { font-size: 20px; margin-bottom: 20px; }
.stat-grid {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 12px; margin-bottom: 24px;
}
.stat-card {
  background: var(--bg2); border: 1px solid var(--border); border-radius: var(--radius);
  padding: 16px;
}
.stat-card .stat-label { font-size: 12px; color: var(--text2); margin-bottom: 4px; }
.stat-card .stat-value { font-size: 28px; font-weight: 700; }
.stat-card .stat-value.green { color: var(--green); }
.stat-card .stat-value.accent { color: var(--accent2); }
.stat-card .stat-value.orange { color: var(--orange); }
.tool-grid {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 8px;
}
.tool-tag {
  background: var(--bg3); border: 1px solid var(--border); border-radius: 6px;
  padding: 8px 12px; font-family: var(--mono); font-size: 13px; color: var(--text2);
}

/* --- sessions panel --- */
.session-list { padding: 24px; overflow-y: auto; }
.session-list h2 { font-size: 20px; margin-bottom: 16px; }
.session-table { width: 100%; border-collapse: collapse; }
.session-table th {
  text-align: left; padding: 10px 14px; font-size: 12px; text-transform: uppercase;
  color: var(--text2); border-bottom: 1px solid var(--border); letter-spacing: 0.5px;
}
.session-table td {
  padding: 10px 14px; font-size: 14px; border-bottom: 1px solid var(--border);
}
.session-table tr:hover td { background: var(--bg2); }
.btn-sm {
  padding: 4px 10px; border-radius: 6px; border: 1px solid var(--border);
  background: var(--bg3); color: var(--text2); cursor: pointer; font-size: 12px;
  transition: all 0.15s;
}
.btn-sm:hover { border-color: var(--red); color: var(--red); }
.btn-open { border-color: var(--accent); color: var(--accent); }
.btn-open:hover { background: var(--accent); color: #fff; }

/* abort button */
.abort-btn {
  width: 44px; height: 44px; border-radius: var(--radius); border: none;
  background: var(--red); color: #fff; cursor: pointer; font-size: 16px;
  display: flex; align-items: center; justify-content: center;
  transition: background 0.15s; flex-shrink: 0;
}
.abort-btn:hover { background: #ef4444; }

/* thinking animation */
.thinking-indicator {
  display: flex; gap: 5px; padding: 12px 16px; align-items: center;
}
.thinking-indicator span { font-size:13px; color:var(--text2); margin-right:6px; }
.thinking-dot {
  width: 7px; height: 7px; border-radius: 50%; background: var(--accent2);
  animation: bounce 1.2s infinite;
}
.thinking-dot:nth-child(2) { animation-delay: 0.2s; }
.thinking-dot:nth-child(3) { animation-delay: 0.4s; }
@keyframes bounce {
  0%,60%,100% { transform: translateY(0); opacity:.6; }
  30% { transform: translateY(-6px); opacity:1; }
}

/* think block (model reasoning) */
.think-block {
  background: var(--bg); border: 1px solid var(--border);
  border-radius: 8px; margin: 8px 0; font-size: 13px; overflow: hidden;
}
.think-block-header {
  display: flex; align-items: center; gap: 6px; padding: 7px 12px;
  cursor: pointer; user-select: none; color: var(--text2);
}
.think-block-header:hover { color: var(--text); }
.think-chevron { transition: transform 0.2s; font-style: normal; }
.think-block.open .think-chevron { transform: rotate(90deg); }
.think-block-body {
  display: none; padding: 8px 12px 10px; color: var(--text2);
  font-style: italic; white-space: pre-wrap; line-height: 1.5;
  border-top: 1px solid var(--border);
  max-height: 300px; overflow-y: auto;
}
.think-block.open .think-block-body { display: block; }

/* markdown output */
.msg-assistant .msg-bubble h1,.msg-assistant .msg-bubble h2,
.msg-assistant .msg-bubble h3 { margin: 8px 0 4px; font-weight:600; }
.msg-assistant .msg-bubble ul,.msg-assistant .msg-bubble ol {
  padding-left: 20px; margin: 4px 0;
}
.msg-assistant .msg-bubble li { margin: 2px 0; }
.msg-assistant .msg-bubble table {
  border-collapse: collapse; margin: 8px 0; width: 100%;
}
.msg-assistant .msg-bubble th,.msg-assistant .msg-bubble td {
  border: 1px solid var(--border); padding: 5px 10px;
}
.msg-assistant .msg-bubble blockquote {
  border-left: 3px solid var(--accent); margin: 8px 0; padding: 4px 12px;
  color: var(--text2);
}

/* scrollbar */
::-webkit-scrollbar { width: 6px; }
::-webkit-scrollbar-track { background: transparent; }
::-webkit-scrollbar-thumb { background: var(--bg3); border-radius: 3px; }
::-webkit-scrollbar-thumb:hover { background: var(--border); }

/* responsive */
@media (max-width: 768px) {
  .sidebar { width: 60px; min-width: 60px; }
  .sidebar-header h1 span, .sidebar-header .subtitle, .nav-item span:not(.icon),
  .sidebar-sessions { display: none; }
  .nav-item { justify-content: center; padding: 12px; }
  .nav-item .icon { margin: 0; }
}
</style>
</head>
<body>

<div class="sidebar">
  <div class="sidebar-header">
    <h1><span class="dot"></span><span>AgentGo</span></h1>
    <div class="subtitle">AI Agent Dashboard</div>
  </div>
  <div class="sidebar-nav">
    <div class="nav-item active" data-panel="chat" onclick="switchPanel('chat')">
      <span class="icon">💬</span><span>对话</span>
    </div>
    <div class="nav-item" data-panel="overview" onclick="switchPanel('overview')">
      <span class="icon">📊</span><span>概览</span>
    </div>
    <div class="nav-item" data-panel="sessions" onclick="switchPanel('sessions')">
      <span class="icon">📋</span><span>会话</span>
    </div>
  </div>
  <div class="sidebar-sessions" id="sidebarSessions">
    <div class="label">会话列表</div>
  </div>
</div>

<div class="main">
  <div class="topbar">
    <div class="title" id="topTitle">对话</div>
    <div class="status status-disconnected" id="wsStatus">
      <span class="status-dot"></span><span id="wsStatusText">未连接</span>
    </div>
  </div>

  <!-- Chat Panel -->
  <div class="panel active" id="panel-chat">
    <div class="chat-messages" id="chatMessages"></div>
    <div class="chat-input-area">
      <div class="input-row">
        <textarea id="chatInput" placeholder="输入消息... (Shift+Enter 换行)" rows="1"></textarea>
        <button class="abort-btn" id="abortBtn" onclick="abortMessage()" style="display:none" title="中止生成">■</button>
        <button class="send-btn" id="sendBtn" onclick="sendMessage()" title="发送">▶</button>
      </div>
    </div>
  </div>

  <!-- Overview Panel -->
  <div class="panel overview" id="panel-overview">
    <h2>系统概览</h2>
    <div class="stat-grid" id="statGrid"></div>
    <h3 style="margin-bottom:12px;font-size:15px;color:var(--text2)">已注册工具</h3>
    <div class="tool-grid" id="toolGrid"></div>
  </div>

  <!-- Sessions Panel -->
  <div class="panel session-list" id="panel-sessions">
    <h2>会话管理</h2>
    <table class="session-table">
      <thead><tr><th>会话 ID</th><th>消息数</th><th>Token 估算</th><th>操作</th></tr></thead>
      <tbody id="sessionTableBody"></tbody>
    </table>
  </div>
</div>

<script>
// --- State ---
let ws = null;
let sessionId = 'default';
let isGenerating = false;
// Per-response state: one container holds all segments in order
let currentMsgContainer = null; // .msg-assistant div for this response
let currentTextEl = null;       // current .msg-bubble for text segment
let currentTextBuf = '';        // text accumulated for current segment
let currentThinkEl = null;      // .think-block-body for reasoning
let currentThinkBuf = '';       // accumulated reasoning text
let toolSeq = 0;

// 配置 marked
if (typeof marked !== 'undefined') {
  marked.setOptions({ breaks: true, gfm: true });
}

// --- Panel switch ---
function switchPanel(name) {
  document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
  document.getElementById('panel-' + name).classList.add('active');
  document.querySelectorAll('.nav-item').forEach(n => n.classList.remove('active'));
  document.querySelector('[data-panel="' + name + '"]').classList.add('active');
  const titles = { chat: '对话', overview: '系统概览', sessions: '会话管理' };
  document.getElementById('topTitle').textContent = titles[name] || '';
  if (name === 'overview') loadOverview();
  if (name === 'sessions') loadSessions();
}

// --- WebSocket ---
function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const url = proto + '//' + location.host + '/v1/ws?session_id=' + encodeURIComponent(sessionId);
  ws = new WebSocket(url);
  ws.onopen = () => {
    setStatus(true);
  };
  ws.onclose = () => {
    setStatus(false);
    isGenerating = false;
    updateInputArea();
    setTimeout(connectWS, 3000);
  };
  ws.onerror = (e) => {
    console.warn('WebSocket error', e);
  };
  ws.onmessage = (e) => {
    try {
      const data = JSON.parse(e.data);
      handleEvent(data);
    } catch(_) {}
  };
}

function setStatus(connected) {
  const el = document.getElementById('wsStatus');
  const txt = document.getElementById('wsStatusText');
  if (connected) {
    el.className = 'status status-connected';
    txt.textContent = '已连接 · ' + sessionId;
  } else {
    el.className = 'status status-disconnected';
    txt.textContent = '未连接';
  }
}

// --- Handle events ---
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
      // Collapse think block on finish
      if (currentThinkEl) {
        currentThinkEl.closest('.think-block').classList.remove('open');
      }
      isGenerating = false;
      currentMsgContainer = null;
      currentTextEl = null;
      currentTextBuf = '';
      currentThinkEl = null;
      currentThinkBuf = '';
      updateInputArea();
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
      currentTextEl = null;
      currentTextBuf = '';
      currentThinkEl = null;
      currentThinkBuf = '';
      updateInputArea();
      break;
  }
}

// --- Send / Abort ---
function sendMessage() {
  const input = document.getElementById('chatInput');
  const msg = input.value.trim();
  if (!msg || isGenerating || !ws || ws.readyState !== WebSocket.OPEN) return;
  appendUserMessage(msg);
  ws.send(JSON.stringify({ type: 'message', message: msg }));
  input.value = '';
  input.style.height = '44px';
  isGenerating = true;
  currentMsgContainer = null;
  currentTextEl = null;
  currentTextBuf = '';
  currentThinkEl = null;
  currentThinkBuf = '';
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

// --- Chat rendering ---
function appendUserMessage(text) {
  const container = document.getElementById('chatMessages');
  const div = document.createElement('div');
  div.className = 'msg msg-user';
  div.innerHTML = '<div class="msg-label">你</div><div class="msg-bubble">' + escapeHtml(text) + '</div>';
  container.appendChild(div);
  scrollToBottom();
}

// --- Thinking indicator ---
function appendThinkingIndicator() {
  removeThinkingIndicator();
  const container = document.getElementById('chatMessages');
  const el = document.createElement('div');
  el.id = 'thinkingIndicator';
  el.className = 'msg msg-assistant';
  el.innerHTML = '<div class="msg-label">Agent</div>' +
    '<div class="thinking-indicator">' +
    '<span>处理中</span>' +
    '<div class="thinking-dot"></div>' +
    '<div class="thinking-dot"></div>' +
    '<div class="thinking-dot"></div>' +
    '</div>';
  container.appendChild(el);
  scrollToBottom();
}
function removeThinkingIndicator() {
  const el = document.getElementById('thinkingIndicator');
  if (el) el.remove();
}

// --- Ensure per-response container (one per full response) ---
function ensureContainer() {
  if (!currentMsgContainer) {
    const container = document.getElementById('chatMessages');
    const div = document.createElement('div');
    div.className = 'msg msg-assistant';
    div.innerHTML = '<div class="msg-label">Agent</div>';
    container.appendChild(div);
    currentMsgContainer = div;
  }
}

// --- Ensure we have a text bubble to write into ---
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

// --- Reasoning: stream into think-block, auto-open while streaming ---
function appendAssistantReasoning(text) {
  removeThinkingIndicator();
  ensureContainer();
  if (!currentThinkEl) {
    const block = document.createElement('div');
    block.className = 'think-block open';
    block.innerHTML =
      '<div class="think-block-header" onclick="this.parentElement.classList.toggle(\'open\')">' +
      '<em class="think-chevron">▶</em> <span>思考过程</span>' +
      '</div>' +
      '<div class="think-block-body"></div>';
    // Think block goes before any text segment
    const firstBubble = currentMsgContainer.querySelector('.msg-bubble');
    if (firstBubble) currentMsgContainer.insertBefore(block, firstBubble);
    else currentMsgContainer.appendChild(block);
    currentThinkEl = block.querySelector('.think-block-body');
    currentThinkBuf = '';
  }
  currentThinkBuf += text;
  currentThinkEl.textContent = currentThinkBuf;
  // Auto-scroll think body
  currentThinkEl.scrollTop = currentThinkEl.scrollHeight;
  scrollToBottom();
}

// --- Text: stream into current text segment only ---
function appendAssistantText(text) {
  removeThinkingIndicator();
  ensureTextSegment();
  currentTextBuf += text;
  const md = (typeof marked !== 'undefined')
    ? marked.parse(currentTextBuf)
    : escapeHtml(currentTextBuf).replace(/\n/g, '<br>');
  currentTextEl.innerHTML = (typeof DOMPurify !== 'undefined') ? DOMPurify.sanitize(md) : md;
  scrollToBottom();
}

// --- Tool cards: seal current text segment, append card to container ---
function appendToolCard(callId, name, args) {
  removeThinkingIndicator();
  ensureContainer();
  // Seal current text segment so next text creates a fresh bubble
  currentTextEl = null;
  currentTextBuf = '';

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
      '<div class="tool-section">' +
        '<div class="tool-section-label">参数</div>' +
        '<div class="tool-args">' + escapeHtml(args || '{}') + '</div>' +
      '</div>' +
      '<div class="tool-section">' +
        '<div class="tool-section-label">输出</div>' +
        '<div class="tool-output">等待结果...</div>' +
      '</div>' +
    '</div>';
  currentMsgContainer.appendChild(card);
  scrollToBottom();
}

function updateToolCard(callId, name, output) {
  let card = null;
  if (callId) {
    card = document.querySelector('.tool-card[data-call-id="' + callId + '"]');
  }
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
  if (outputEl) {
    outputEl.textContent = output || '(无输出)';
  }
}

function appendError(err) {
  const container = document.getElementById('chatMessages');
  const div = document.createElement('div');
  div.className = 'msg msg-assistant';
  div.innerHTML = '<div class="msg-label">错误</div><div class="msg-bubble" style="border:1px solid var(--red);color:var(--red)">' + escapeHtml(err) + '</div>';
  container.appendChild(div);
  scrollToBottom();
}

function appendSystemNote(text) {
  const container = document.getElementById('chatMessages');
  const div = document.createElement('div');
  div.style.cssText = 'text-align:center;font-size:12px;color:var(--text2);margin:8px 0;';
  div.textContent = text;
  container.appendChild(div);
  scrollToBottom();
}

function scrollToBottom() {
  const el = document.getElementById('chatMessages');
  el.scrollTop = el.scrollHeight;
}

function escapeHtml(s) {
  if (!s) return '';
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

// --- Overview ---
async function loadOverview() {
  try {
    const resp = await fetch('/ui/api/info');
    const info = await resp.json();
    const grid = document.getElementById('statGrid');
    grid.innerHTML =
      statCard('版本', info.version || '1.0.0', 'accent') +
      statCard('工具数', info.tool_count, 'green') +
      statCard('活跃会话', (info.sessions || []).length, 'orange');
    const tg = document.getElementById('toolGrid');
    tg.innerHTML = (info.tools || []).map(t => '<div class="tool-tag">' + escapeHtml(t) + '</div>').join('');
  } catch(e) {
    document.getElementById('statGrid').innerHTML = '<p style="color:var(--red)">加载失败</p>';
  }
}
function statCard(label, value, color) {
  return '<div class="stat-card"><div class="stat-label">' + label + '</div><div class="stat-value ' + color + '">' + value + '</div></div>';
}

// --- Sessions ---
async function loadSessions() {
  try {
    const resp = await fetch('/ui/api/sessions');
    const list = await resp.json();
    const tbody = document.getElementById('sessionTableBody');
    if (!list || list.length === 0) {
      tbody.innerHTML = '<tr><td colspan="4" style="color:var(--text2);text-align:center">暂无会话</td></tr>';
      return;
    }
    tbody.innerHTML = list.map(s =>
      '<tr>' +
        '<td style="font-family:var(--mono)">' + escapeHtml(s.id) + '</td>' +
        '<td>' + s.message_count + '</td>' +
        '<td>' + s.token_estimate + '</td>' +
        '<td>' +
          '<button class="btn-sm btn-open" onclick="openSession(\'' + escapeHtml(s.id) + '\')">打开</button> ' +
          '<button class="btn-sm" onclick="resetSession(\'' + escapeHtml(s.id) + '\')">重置</button>' +
        '</td>' +
      '</tr>').join('');
    // sidebar sessions
    const sb = document.getElementById('sidebarSessions');
    sb.innerHTML = '<div class="label">会话列表</div>' +
      list.map(s =>
        '<div class="sess-item' + (s.id === sessionId ? ' active' : '') + '" onclick="openSession(\'' + escapeHtml(s.id) + '\')">' +
          '<span>' + escapeHtml(s.id) + '</span>' +
          '<span class="count">' + s.message_count + '</span>' +
        '</div>').join('');
  } catch(e) {}
}

function openSession(id) {
  sessionId = id;
  document.getElementById('chatMessages').innerHTML = '';
  currentMsgContainer = null;
  currentTextEl = null;
  currentTextBuf = '';
  currentThinkEl = null;
  currentThinkBuf = '';
  if (ws) ws.close();
  connectWS();
  switchPanel('chat');
}

async function resetSession(id) {
  if (!confirm('确定要重置会话 "' + id + '" 吗？')) return;
  await fetch('/ui/api/sessions?id=' + encodeURIComponent(id), { method: 'DELETE' });
  loadSessions();
  if (id === sessionId) {
    document.getElementById('chatMessages').innerHTML = '';
    currentMsgContainer = null;
    currentTextEl = null;
    currentTextBuf = '';
    currentThinkEl = null;
    currentThinkBuf = '';
  }
}

// --- Textarea auto-resize + keyboard ---
const chatInput = document.getElementById('chatInput');
chatInput.addEventListener('input', function() {
  this.style.height = '44px';
  this.style.height = Math.min(this.scrollHeight, 120) + 'px';
});
chatInput.addEventListener('keydown', function(e) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault();
    sendMessage();
  }
});

// --- Init ---
updateInputArea();
connectWS();
loadSessions();
</script>
</body>
</html>` + ""

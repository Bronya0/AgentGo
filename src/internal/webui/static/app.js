// --- 配置 marked + highlight.js ---
if (typeof marked !== 'undefined') {
  marked.setOptions({
    breaks: true,
    gfm: true,
  });
}

// --- State ---
let ws = null;
let sessionId = 'default';
let isGenerating = false;
// Per-response state: one container holds all segments in order
let currentMsgContainer = null; // .msg-assistant div for this response
let currentTextEl = null;       // current .msg-bubble for text segment
let currentTextBuf = '';        // text accumulated for current segment
let currentThinkEl = null;      // .think-block-body for reasoning stream
let currentThinkBuf = '';       // accumulated reasoning text
let toolSeq = 0;

// RAF-based render throttle: batch DOM updates to at most 1 per animation frame
let renderPending = false;
function scheduleRender() {
  if (renderPending) return;
  renderPending = true;
  requestAnimationFrame(() => {
    renderPending = false;
    // Render text segment
    if (currentTextEl && currentTextBuf !== undefined) {
      const md = (typeof marked !== 'undefined')
        ? marked.parse(currentTextBuf)
        : escapeHtml(currentTextBuf).replace(/\n/g, '<br>');
      const safe = (typeof DOMPurify !== 'undefined') ? DOMPurify.sanitize(md) : md;
      currentTextEl.innerHTML = safe;
      // Highlight code blocks in the updated segment
      if (typeof hljs !== 'undefined') {
        currentTextEl.querySelectorAll('pre code').forEach(block => {
          hljs.highlightElement(block);
        });
      }
    }
    // Render reasoning
    if (currentThinkEl && currentThinkBuf !== undefined) {
      currentThinkEl.textContent = currentThinkBuf;
      currentThinkEl.scrollTop = currentThinkEl.scrollHeight;
    }
    scrollToBottom();
  });
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
  ws.onopen = () => { setStatus(true); };
  ws.onclose = () => {
    setStatus(false);
    isGenerating = false;
    updateInputArea();
    setTimeout(connectWS, 3000);
  };
  ws.onerror = (e) => { console.warn('WebSocket error', e); };
  ws.onmessage = (e) => {
    try { handleEvent(JSON.parse(e.data)); } catch (_) {}
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
      // Collapse think block when response ends
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

// --- Ensure per-response container (one .msg-assistant per full response) ---
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

// --- Ensure we have a text bubble segment to write into ---
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

// --- Reasoning: stream into think-block; auto-open while streaming, collapsed on done ---
function appendAssistantReasoning(text) {
  ensureContainer();
  if (!currentThinkEl) {
    const block = document.createElement('div');
    block.className = 'think-block open'; // auto-expand while streaming
    block.innerHTML =
      '<div class="think-block-header" onclick="this.parentElement.classList.toggle(\'open\')">' +
      '<em class="think-chevron">▶</em> <span>思考过程</span>' +
      '</div>' +
      '<div class="think-block-body"></div>';
    // Reasoning always goes before text bubbles
    const firstBubble = currentMsgContainer.querySelector('.msg-bubble');
    if (firstBubble) currentMsgContainer.insertBefore(block, firstBubble);
    else currentMsgContainer.appendChild(block);
    currentThinkEl = block.querySelector('.think-block-body');
    currentThinkBuf = '';
  }
  currentThinkBuf += text;
  scheduleRender(); // batched via RAF
}

// --- Text: stream into current text segment only (resets after each tool) ---
function appendAssistantText(text) {
  ensureTextSegment();
  currentTextBuf += text;
  scheduleRender(); // batched via RAF — at most 1 marked.parse() per frame
}

// --- Tool cards: seal current text segment, append card to same container ---
function appendToolCard(callId, name, args) {
  ensureContainer();
  // Seal current text segment: next text creates a fresh bubble (no repetition)
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
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
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
  } catch (e) {
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
  } catch (e) {}
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
chatInput.addEventListener('input', function () {
  this.style.height = '44px';
  this.style.height = Math.min(this.scrollHeight, 120) + 'px';
});
chatInput.addEventListener('keydown', function (e) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault();
    sendMessage();
  }
});

// --- Init ---
updateInputArea();
connectWS();
loadSessions();

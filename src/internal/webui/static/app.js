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
let currentMsgContainer = null;
let currentTextEl = null;
let currentTextBuf = '';
let currentThinkEl = null;
let currentThinkBuf = '';
let toolSeq = 0;
let pendingImages = []; // base64 data URIs of attached images
let userScrolledUp = false; // smart scroll tracking

// RAF-based render throttle
let renderPending = false;
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
      // Highlight code blocks + add copy buttons
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

// --- Smart scroll: only auto-scroll if user is near bottom ---
function smartScroll() {
  if (userScrolledUp) return;
  const el = document.getElementById('chatMessages');
  el.scrollTop = el.scrollHeight;
}

// Track whether user has scrolled up
document.addEventListener('DOMContentLoaded', () => {
  const el = document.getElementById('chatMessages');
  el.addEventListener('scroll', () => {
    const threshold = 100;
    userScrolledUp = (el.scrollHeight - el.scrollTop - el.clientHeight) > threshold;
  });
});

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

// --- Timestamp helper ---
function timeStr() {
  const d = new Date();
  return d.getHours().toString().padStart(2,'0') + ':' + d.getMinutes().toString().padStart(2,'0');
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
  if ((!msg && pendingImages.length === 0) || isGenerating || !ws || ws.readyState !== WebSocket.OPEN) return;

  // Hide empty state
  const empty = document.getElementById('emptyState');
  if (empty) empty.remove();

  appendUserMessage(msg, pendingImages);

  // Send to backend
  const payload = { type: 'message', message: msg || '请分析这些图片' };
  if (pendingImages.length > 0) {
    payload.images = pendingImages;
  }
  ws.send(JSON.stringify(payload));

  // Reset input
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

// --- Suggestion chips ---
function useSuggestion(text) {
  const input = document.getElementById('chatInput');
  input.value = text;
  input.focus();
}

// --- Chat rendering ---
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

// --- Thinking indicator ---
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

// --- Ensure per-response container ---
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

// --- Reasoning ---
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

// --- Text ---
function appendAssistantText(text) {
  ensureTextSegment();
  currentTextBuf += text;
  scheduleRender();
}

// --- Tool cards ---
function appendToolCard(callId, name, args) {
  ensureContainer();
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
  smartScroll();
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

// --- Code copy buttons ---
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

// --- Error / System notes ---
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
  div.style.cssText = 'text-align:center;font-size:12px;color:var(--text2);margin:8px 0;';
  div.textContent = text;
  container.appendChild(div);
  smartScroll();
}

function escapeHtml(s) {
  if (!s) return '';
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

// --- Image paste/drop support ---

// Clipboard paste
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

// Drag & drop
const chatPanel = document.getElementById('panel-chat');
const dragOverlay = document.getElementById('dragOverlay');
let dragCounter = 0;

chatPanel.addEventListener('dragenter', (e) => {
  e.preventDefault();
  dragCounter++;
  if (e.dataTransfer.types.includes('Files')) {
    dragOverlay.classList.add('visible');
  }
});

chatPanel.addEventListener('dragleave', (e) => {
  e.preventDefault();
  dragCounter--;
  if (dragCounter <= 0) {
    dragCounter = 0;
    dragOverlay.classList.remove('visible');
  }
});

chatPanel.addEventListener('dragover', (e) => {
  e.preventDefault();
});

chatPanel.addEventListener('drop', (e) => {
  e.preventDefault();
  dragCounter = 0;
  dragOverlay.classList.remove('visible');
  const files = e.dataTransfer.files;
  for (const file of files) {
    if (file.type.startsWith('image/')) {
      addImageFile(file);
    }
  }
});

// File input button
function triggerImageUpload() {
  document.getElementById('imageFileInput').click();
}

function handleFileSelect(e) {
  for (const file of e.target.files) {
    if (file.type.startsWith('image/')) {
      addImageFile(file);
    }
  }
  e.target.value = ''; // reset
}

// Process image file → base64 → preview
function addImageFile(file) {
  if (file.size > 20 * 1024 * 1024) {
    alert('图片大小不能超过 20MB');
    return;
  }
  if (pendingImages.length >= 5) {
    alert('最多同时附加 5 张图片');
    return;
  }
  const reader = new FileReader();
  reader.onload = () => {
    const dataURI = reader.result;
    pendingImages.push(dataURI);
    renderAttachmentPreviews();
  };
  reader.readAsDataURL(file);
}

function renderAttachmentPreviews() {
  const container = document.getElementById('inputAttachments');
  container.innerHTML = pendingImages.map((uri, idx) =>
    '<div class="attach-preview">' +
      '<img src="' + uri + '">' +
      '<button class="attach-remove" onclick="removeAttachment(' + idx + ')">✕</button>' +
    '</div>'
  ).join('');
}

function removeAttachment(idx) {
  pendingImages.splice(idx, 1);
  renderAttachmentPreviews();
}

// --- Lightbox ---
function openLightbox(src) {
  const lb = document.getElementById('lightbox');
  document.getElementById('lightboxImg').src = src;
  lb.classList.add('open');
}

function closeLightbox() {
  document.getElementById('lightbox').classList.remove('open');
}

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') closeLightbox();
});

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

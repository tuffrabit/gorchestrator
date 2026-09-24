// Dashboard helpers (drawer, CSRF for HTMX, SSE card refresh).
// HTMX and highlight.js are vendored separately in this directory.

function openDrawer(title) {
  var drawer = document.getElementById('drawer');
  var scrim = document.getElementById('scrim');
  var titleEl = document.getElementById('drawer-title');
  if (titleEl && title) titleEl.textContent = title;
  if (drawer) drawer.classList.add('open');
  if (scrim) scrim.classList.add('open');
  document.body.style.overflow = 'hidden';
}

function closeDrawer() {
  var drawer = document.getElementById('drawer');
  var scrim = document.getElementById('scrim');
  if (drawer) drawer.classList.remove('open');
  if (scrim) scrim.classList.remove('open');
  document.body.style.overflow = '';
}

// openArtifactDrawer loads the artifact drawer for an issue.
// tab: result | output | activity | workspace
// phase: the flow step key to show (optional — defaults to the card's
// current step).
function openArtifactDrawer(issueId, tab, phase) {
  var resolvedTab = tab || 'result';
  var resolvedPhase = phase;
  if (!resolvedPhase && resolvedTab !== 'workspace') {
    var card = document.getElementById('issue-' + issueId);
    if (card && card.dataset.phase) {
      resolvedPhase = card.dataset.phase;
    }
  }
  var title = 'Issue #' + issueId;
  if (resolvedTab === 'workspace') {
    title += ' · workspace';
  } else if (resolvedPhase) {
    title += ' · ' + resolvedPhase;
  }
  openDrawer(title);
  if (window.htmx) {
    var url = '/partials/issues/' + issueId + '/drawer?tab=' + encodeURIComponent(resolvedTab);
    if (resolvedPhase) {
      url += '&phase=' + encodeURIComponent(resolvedPhase);
    }
    htmx.ajax('GET', url, {
      target: '#drawer-body',
      swap: 'innerHTML'
    });
  }
}

// Drawer content bootstrap. HTMX swaps partials (artifact, chat, submit) into
// #drawer-body; inline <script> tags inside swapped partials are unreliable,
// so the drawer's one-time setup lives here instead: build the activity JSON
// tree and syntax-highlight code blocks.
document.addEventListener('htmx:afterSwap', function (e) {
  var body = document.getElementById('drawer-body');
  if (!body) return;
  var target = e.target || (e.detail && e.detail.elt);
  if (!target || (target !== body && !body.contains(target))) return;
  initDrawerContent(body);
});

function initDrawerContent(root) {
  if (!root) return;
  root.querySelectorAll('.events-tree').forEach(function (tree) {
    if (tree.dataset.built) return;
    tree.dataset.built = '1';
    if (typeof renderJsonTree === 'function') renderJsonTree(tree);
  });
  if (window.hljs) {
    root.querySelectorAll('pre code').forEach(function (el) {
      if (el.dataset.hljsDone) return;
      el.dataset.hljsDone = '1';
      hljs.highlightElement(el);
    });
  }
}

// --- Collapsible JSON tree (activity/events drawer tab) ------------------

var JSON_TREE_STRING_CAP = 300;

// renderJsonTree builds a collapsible tree of the phase event log inside
// container. The JSON array payload lives in a hidden sibling
// .js-events-data element (HTML-escaped by the template; read via
// textContent, which decodes it back).
function renderJsonTree(container) {
  var scope = container.closest('.drawer-content-pane') || container.parentElement;
  var dataEl = scope.querySelector('.js-events-data');
  if (!dataEl) return;
  var events;
  try {
    events = JSON.parse(dataEl.textContent);
  } catch (err) {
    container.textContent = 'Could not parse activity events.';
    return;
  }
  container.textContent = '';
  events.forEach(function (ev, i) {
    container.appendChild(jsonTreeEventNode(ev, i));
  });
}

function jsonTreeEventNode(ev, i) {
  var body = document.createElement('div');
  body.appendChild(jsonTreeValue(ev));
  return jsonTreeToggle('jt-event-head', jsonTreeEventSummary(ev, i), body, false);
}

function jsonTreeEventSummary(ev, i) {
  var parts = ['[' + i + ']'];
  if (ev && typeof ev === 'object') {
    parts.push(ev.type || 'event');
    var tc = ev.tool_call;
    var tr = ev.tool_result;
    if (tc && tc.name) parts.push(String(tc.name));
    if (tr && tr.name) parts.push(String(tr.name) + ' result');
    if (ev.tokens) parts.push(ev.tokens + ' tok');
    if (ev.error) parts.push('error');
    if (ev.timestamp) parts.push(String(ev.timestamp));
  }
  return parts.join(' · ');
}

// jsonTreeToggle builds a collapsible node: a .jt-toggle button controlling
// the sibling .jt-body element.
function jsonTreeToggle(headClass, labelText, body, expanded) {
  var node = document.createElement('div');
  node.className = 'jt-node';
  var head = document.createElement('button');
  head.type = 'button';
  head.className = 'jt-toggle ' + headClass;
  head.setAttribute('aria-expanded', expanded ? 'true' : 'false');
  var caret = document.createElement('span');
  caret.className = 'jt-caret';
  caret.textContent = expanded ? '▾' : '▸';
  head.appendChild(caret);
  var label = document.createElement('span');
  label.className = 'jt-label';
  label.textContent = labelText;
  head.appendChild(label);
  body.classList.add('jt-body');
  body.hidden = !expanded;
  head.addEventListener('click', function () {
    jsonTreeSet(head, body, body.hidden);
  });
  node.appendChild(head);
  node.appendChild(body);
  return node;
}

function jsonTreeSet(head, body, open) {
  body.hidden = !open;
  head.setAttribute('aria-expanded', open ? 'true' : 'false');
  var caret = head.querySelector('.jt-caret');
  if (caret) caret.textContent = open ? '▾' : '▸';
}

function jsonTreeLeaf(text, cls) {
  var span = document.createElement('span');
  span.className = cls;
  span.textContent = text;
  return span;
}

function jsonTreeString(s) {
  if (s.length <= JSON_TREE_STRING_CAP) {
    return jsonTreeLeaf(JSON.stringify(s), 'jt-str');
  }
  var wrap = document.createElement('span');
  var text = jsonTreeLeaf(JSON.stringify(s.slice(0, JSON_TREE_STRING_CAP)) + '… ', 'jt-str');
  var more = document.createElement('button');
  more.type = 'button';
  more.className = 'jt-more';
  var expanded = false;
  more.textContent = '(' + (s.length - JSON_TREE_STRING_CAP) + ' more chars)';
  more.addEventListener('click', function () {
    expanded = !expanded;
    text.textContent = expanded ? JSON.stringify(s) : JSON.stringify(s.slice(0, JSON_TREE_STRING_CAP)) + '… ';
    more.textContent = expanded ? '(less)' : '(' + (s.length - JSON_TREE_STRING_CAP) + ' more chars)';
  });
  wrap.appendChild(text);
  wrap.appendChild(more);
  return wrap;
}

function jsonTreeValue(value) {
  if (value === null || value === undefined) {
    return jsonTreeLeaf('null', 'jt-null');
  }
  if (Array.isArray(value)) {
    if (value.length === 0) {
      return jsonTreeLeaf('[]', 'jt-null');
    }
    var arrBody = document.createElement('div');
    value.forEach(function (item, i) {
      arrBody.appendChild(jsonTreeRow('[' + i + ']', item));
    });
    return jsonTreeToggle('jt-coll-head', '[… ' + value.length + (value.length === 1 ? ' item' : ' items') + ']', arrBody, true);
  }
  if (typeof value === 'object') {
    var keys = Object.keys(value);
    if (keys.length === 0) {
      return jsonTreeLeaf('{}', 'jt-null');
    }
    var objBody = document.createElement('div');
    keys.forEach(function (k) {
      objBody.appendChild(jsonTreeRow(k + ':', value[k]));
    });
    return jsonTreeToggle('jt-coll-head', '{… ' + keys.length + (keys.length === 1 ? ' key' : ' keys') + '}', objBody, true);
  }
  if (typeof value === 'string') {
    return jsonTreeString(value);
  }
  if (typeof value === 'number') {
    return jsonTreeLeaf(String(value), 'jt-num');
  }
  return jsonTreeLeaf(String(value), 'jt-bool');
}

function jsonTreeRow(key, value) {
  var row = document.createElement('div');
  row.className = 'jt-row';
  var keyEl = document.createElement('span');
  keyEl.className = 'jt-key';
  keyEl.textContent = key;
  row.appendChild(keyEl);
  row.appendChild(jsonTreeValue(value));
  return row;
}

// jsonTreeAll expands/collapses every node in the tree containing btn.
function jsonTreeAll(btn, open) {
  var scope = btn.closest('.drawer-artifact');
  if (!scope) return;
  scope.querySelectorAll('.json-tree .jt-toggle').forEach(function (head) {
    var body = head.nextElementSibling;
    if (body && body.classList.contains('jt-body')) {
      jsonTreeSet(head, body, open);
    }
  });
}

// jsonTreeToggleRaw switches between the tree and the raw JSONL text.
function jsonTreeToggleRaw(btn) {
  var scope = btn.closest('.drawer-artifact');
  if (!scope) return;
  var tree = scope.querySelector('.json-tree');
  var raw = scope.querySelector('.json-raw');
  if (!tree || !raw) return;
  var showRaw = raw.hidden;
  raw.hidden = !showRaw;
  tree.hidden = showRaw;
  btn.textContent = showRaw ? 'Tree' : 'Raw';
}

function warnEmptyFeedback(form) {
  var ta = form.querySelector('textarea[name="feedback"]');
  if (ta && !ta.value.trim()) {
    return window.confirm('No feedback provided. Submit anyway?');
  }
  return true;
}

function csrfToken() {
  if (document.body && document.body.dataset.csrf) {
    return document.body.dataset.csrf;
  }
  var el = document.querySelector('input[name="csrf_token"]');
  return el ? el.value : '';
}

// --- Toasts (HTMX 4xx/5xx feedback) ---

function ensureToastHost() {
  var host = document.getElementById('toast-host');
  if (host) return host;
  host = document.createElement('div');
  host.id = 'toast-host';
  host.className = 'toast-host';
  host.setAttribute('aria-live', 'assertive');
  host.setAttribute('aria-relevant', 'additions');
  (document.body || document.documentElement).appendChild(host);
  return host;
}

// message: main body text. opts: { title, kind: 'error'|'client', durationMs }
function showToast(message, opts) {
  opts = opts || {};
  var kind = opts.kind || 'error';
  var title = opts.title || (kind === 'client' ? 'Request error' : 'Server error');
  var duration = opts.durationMs != null ? opts.durationMs : 8000;
  var text = String(message == null ? '' : message).trim();
  if (!text) text = 'Something went wrong.';

  var host = ensureToastHost();
  var el = document.createElement('div');
  el.className = 'toast toast-' + kind;
  el.setAttribute('role', 'alert');

  var body = document.createElement('div');
  body.className = 'toast-body';
  var titleEl = document.createElement('p');
  titleEl.className = 'toast-title';
  titleEl.textContent = title;
  var msgEl = document.createElement('p');
  msgEl.className = 'toast-message';
  msgEl.textContent = text;
  body.appendChild(titleEl);
  body.appendChild(msgEl);

  var dismiss = document.createElement('button');
  dismiss.type = 'button';
  dismiss.className = 'toast-dismiss';
  dismiss.setAttribute('aria-label', 'Dismiss');
  dismiss.textContent = '✕';

  function remove() {
    if (el._toastTimer) clearTimeout(el._toastTimer);
    if (!el.parentNode) return;
    el.classList.add('toast-out');
    setTimeout(function () {
      if (el.parentNode) el.parentNode.removeChild(el);
    }, 180);
  }
  dismiss.addEventListener('click', remove);

  el.appendChild(body);
  el.appendChild(dismiss);
  host.appendChild(el);
  if (duration > 0) {
    el._toastTimer = setTimeout(remove, duration);
  }
  return el;
}

// Pull a human-readable message from XHR responseText (plain text or JSON error).
function messageFromXHR(xhr) {
  if (!xhr) return '';
  var raw = (xhr.responseText || '').trim();
  if (!raw) return (xhr.statusText || '').trim();
  if (raw.charAt(0) === '{') {
    try {
      var j = JSON.parse(raw);
      if (j && typeof j.error === 'string' && j.error) return j.error;
      if (j && typeof j.message === 'string' && j.message) return j.message;
    } catch (e) { /* not JSON */ }
  }
  // http.Error is plain text; ignore HTML error pages.
  if (raw.charAt(0) === '<') return (xhr.statusText || '').trim();
  if (raw.length > 400) return raw.slice(0, 400) + '…';
  return raw;
}

function toastForHTTPError(xhr) {
  if (!xhr) return;
  var status = xhr.status | 0;
  if (status < 400) return;
  var isClient = status >= 400 && status < 500;
  var title = isClient
    ? ('Request failed' + (status ? ' (' + status + ')' : ''))
    : ('Server error' + (status ? ' (' + status + ')' : ''));
  var msg = messageFromXHR(xhr);
  if (!msg) {
    msg = isClient
      ? 'The request could not be completed.'
      : 'The server hit an error. Check logs and try again.';
  }
  showToast(msg, { title: title, kind: isClient ? 'client' : 'error' });
}

// Attach CSRF header to every HTMX request (forms also post csrf_token).
document.addEventListener('htmx:configRequest', function (e) {
  var tok = csrfToken();
  if (tok) {
    e.detail.headers['X-CSRF-Token'] = tok;
  }
});

// Surface HTMX 4xx/5xx responses as toasts (e.g. submit validation / server faults).
document.addEventListener('htmx:afterRequest', function (e) {
  if (!e.detail || !e.detail.xhr) return;
  if ((e.detail.xhr.status | 0) < 400) return;
  toastForHTTPError(e.detail.xhr);
});

// Network failure (no HTTP response).
document.addEventListener('htmx:sendError', function (e) {
  showToast('Network error — could not reach the server.', {
    title: 'Connection failed',
    kind: 'error'
  });
});

document.addEventListener('keydown', function (e) {
  if (e.key === 'Escape') closeDrawer();
});

// Debounced per-issue card refresh so rapid phase/status events collapse to one swap.
var cardRefreshTimers = {};
var cardRefreshInflight = {};

function upsertIssueCardHTML(issueId, html) {
  var feed = document.getElementById('issue-feed');
  if (!feed) return;
  var wrap = document.createElement('div');
  wrap.innerHTML = String(html).trim();
  var incoming = wrap.firstElementChild;
  if (!incoming) return;

  var existing = document.getElementById('issue-' + issueId);
  if (existing) {
    existing.replaceWith(incoming);
  } else {
    var empty = feed.querySelector(':scope > .empty');
    if (empty) empty.remove();
    feed.insertAdjacentElement('afterbegin', incoming);
  }
  if (window.htmx && typeof htmx.process === 'function') {
    htmx.process(incoming);
  }
  if (window.hljs && typeof hljs.highlightAll === 'function') {
    // no-op on cards; drawer uses highlightElement after load
  }
}

function refreshIssueCard(issueId) {
  if (!issueId) return;
  var id = String(issueId);
  if (cardRefreshTimers[id]) {
    clearTimeout(cardRefreshTimers[id]);
  }
  cardRefreshTimers[id] = setTimeout(function () {
    delete cardRefreshTimers[id];
    if (cardRefreshInflight[id]) {
      cardRefreshTimers[id] = setTimeout(function () {
        delete cardRefreshTimers[id];
        refreshIssueCard(id);
      }, 80);
      return;
    }
    cardRefreshInflight[id] = true;
    var el = document.getElementById('issue-' + id);
    var expanded = el && el.querySelector('.card-body') ? '1' : '0';
    var headers = { 'HX-Request': 'true' };
    var tok = csrfToken();
    if (tok) headers['X-CSRF-Token'] = tok;

    fetch('/partials/issues/' + id + '?expanded=' + expanded, {
      credentials: 'same-origin',
      headers: headers
    })
      .then(function (r) {
        if (!r.ok) throw new Error('card refresh ' + r.status);
        return r.text();
      })
      .then(function (html) {
        upsertIssueCardHTML(id, html);
      })
      .catch(function () { /* ignore transient errors */ })
      .finally(function () {
        cardRefreshInflight[id] = false;
      });
  }, 50);
}

// --- Chat drawer (SSE refresh, drafts, auto-scroll) ------------------------

var chatRefreshTimer = null;

function chatThreadRoot() {
  return document.getElementById('chat-thread-inner');
}

function chatDrawerOpen() {
  var drawer = document.getElementById('drawer');
  return !!(drawer && drawer.classList.contains('open'));
}

function chatDraftKey(project, agent) {
  return 'chat-draft:' + (project || '') + ':' + (agent || '');
}

// Debounced re-render of the visible chat thread. The thread partial always
// reflects the current (project, agent, flavor) selection carried on the
// thread root's data attributes, so any chat_message event just refreshes
// whatever selection is visible — no per-thread matching needed.
function refreshChatThread() {
  if (!chatDrawerOpen()) return;
  if (chatRefreshTimer) clearTimeout(chatRefreshTimer);
  chatRefreshTimer = setTimeout(function () {
    chatRefreshTimer = null;
    var root = chatThreadRoot();
    if (!root || !chatDrawerOpen() || !window.htmx) return;
    var url = '/partials/chat/thread?project=' + encodeURIComponent(root.dataset.chatProject || '') +
      '&agent=' + encodeURIComponent(root.dataset.chatAgent || '');
    htmx.ajax('GET', url, { target: '#chat-thread', swap: 'innerHTML' });
  }, 150);
}

function chatScrollThread(root) {
  var box = root.querySelector('.chat-messages');
  if (!box) return;
  // Keep the user's place when they scrolled up to read history.
  var nearBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 120;
  if (nearBottom) box.scrollTop = box.scrollHeight;
}

// Called by the inline script in the chat thread partial after every swap
// (and on the initial drawer render): restores a stored draft unless this
// swap is the echo of a just-sent message, then auto-scrolls.
function chatInitThread(root) {
  if (!root) return;
  var ta = root.querySelector('textarea.chat-draft');
  if (ta) {
    var key = chatDraftKey(root.dataset.chatProject, root.dataset.chatAgent);
    if (window._chatLastSentKey === key) {
      ta.value = '';
    } else {
      var val = '';
      try { val = localStorage.getItem(key) || ''; } catch (e) { /* storage unavailable */ }
      ta.value = val;
    }
  }
  chatScrollThread(root);
}

// Persist composer drafts per (project, agent, flavor) as the user types.
document.addEventListener('input', function (e) {
  var ta = e.target;
  if (!ta || !ta.classList || !ta.classList.contains('chat-draft')) return;
  var root = ta.closest('#chat-thread-inner');
  if (!root) return;
  var key = chatDraftKey(root.dataset.chatProject, root.dataset.chatAgent);
  try {
    if (ta.value) localStorage.setItem(key, ta.value);
    else localStorage.removeItem(key);
  } catch (err) { /* storage unavailable */ }
});

// Send lifecycle: remember the draft key so the post-send swap doesn't
// restore the just-sent text into the fresh composer. The stored draft is
// cleared only on success; on failure the textarea keeps its text and the
// draft is re-persisted.
document.addEventListener('htmx:beforeRequest', function (e) {
  var elt = e.detail && e.detail.elt;
  if (!elt || !elt.getAttribute) return;
  var post = elt.getAttribute('hx-post') || elt.getAttribute('data-hx-post') || '';
  if (post !== '/partials/chat/send') return;
  var root = chatThreadRoot();
  if (!root) return;
  window._chatLastSentKey = chatDraftKey(root.dataset.chatProject, root.dataset.chatAgent);
  var form = elt.closest ? elt.closest('form') : null;
  var ta = form ? form.querySelector('textarea.chat-draft') : null;
  window._chatLastSentBackup = ta ? ta.value : '';
});

document.addEventListener('htmx:afterRequest', function (e) {
  if (window._chatLastSentKey == null) return;
  var elt = e.detail && e.detail.elt;
  var post = elt && elt.getAttribute ? (elt.getAttribute('hx-post') || '') : '';
  if (post !== '/partials/chat/send') return;
  var ok = e.detail.successful !== undefined
    ? e.detail.successful
    : (e.detail.xhr && e.detail.xhr.status >= 200 && e.detail.xhr.status < 300);
  var key = window._chatLastSentKey;
  var backup = window._chatLastSentBackup;
  window._chatLastSentKey = null;
  window._chatLastSentBackup = null;
  try {
    if (ok) localStorage.removeItem(key);
    else if (backup) localStorage.setItem(key, backup);
  } catch (err) { /* storage unavailable */ }
});

document.addEventListener('DOMContentLoaded', function () {
  var expandId = document.body && document.body.dataset.expandId;
  var drawer = document.body && document.body.dataset.drawer;
  var drawerPhase = document.body && document.body.dataset.drawerPhase;
  if (expandId && expandId !== '0' && drawer) {
    openArtifactDrawer(expandId, drawer, drawerPhase || undefined);
  }

  if (typeof EventSource === 'undefined') return;
  try {
    var es = new EventSource('/api/events');

    function onIssueEvent(ev) {
      try {
        var data = JSON.parse(ev.data);
        if (!data.issue_id) return;
        refreshIssueCard(data.issue_id);
      } catch (err) { /* ignore malformed */ }
    }

    es.addEventListener('issue_status', onIssueEvent);
    es.addEventListener('phase_started', onIssueEvent);
    es.addEventListener('phase_finished', onIssueEvent);
    es.addEventListener('model_activity', onIssueEvent);
    es.addEventListener('decision_requested', onIssueEvent);
    es.addEventListener('decision_applied', onIssueEvent);
    es.addEventListener('issue_submitted', onIssueEvent);
    es.addEventListener('issue_deleted', function (ev) {
      try {
        var data = JSON.parse(ev.data);
        if (!data.issue_id) return;
        var el = document.getElementById('issue-' + data.issue_id);
        if (el) el.remove();
        // Close drawer if it was showing the deleted issue.
        var title = document.getElementById('drawer-title');
        if (title && title.textContent && title.textContent.indexOf('#' + data.issue_id) !== -1) {
          closeDrawer();
        }
      } catch (err) { /* ignore */ }
    });
    // Chat turns publish on every stage change and on completion; refresh
    // the chat drawer when it is open (no-op otherwise).
    es.addEventListener('chat_message', function (ev) {
      try {
        JSON.parse(ev.data);
        refreshChatThread();
      } catch (err) { /* ignore malformed */ }
    });
  } catch (e) { /* SSE unavailable */ }
});

// True when this HTMX event came from the New-issue submit form (POST only).
function isIssueSubmitRequest(detail) {
  if (!detail) return false;
  var elt = detail.elt;
  if (elt && elt.getAttribute) {
    var post = elt.getAttribute('hx-post') || elt.getAttribute('data-hx-post') || '';
    if (post === '/partials/submit') return true;
  }
  var verb = (detail.requestConfig && detail.requestConfig.verb) || '';
  if (String(verb).toLowerCase() !== 'post') return false;
  var path = (detail.pathInfo && (detail.pathInfo.requestPath || detail.pathInfo.finalRequestPath)) || '';
  // Match POST /partials/submit but not GET /partials/submit or /partials/submit/flow.
  return /\/partials\/submit\/?$/.test(String(path));
}

// After HTMX inserts a card (e.g. submit afterbegin), drop the empty-state
// placeholder and any duplicate ids. afterbegin alone leaves "No issues yet."
// in place when the feed was empty on load. Also close the submit drawer —
// this path is reliable because the feed swap already succeeded.
document.addEventListener('htmx:afterSwap', function (e) {
  var feed = document.getElementById('issue-feed');
  if (!feed || !e.target || (e.target !== feed && !feed.contains(e.target))) return;
  if (feed.querySelector('.issue-card')) {
    var empty = feed.querySelector(':scope > .empty');
    if (empty) empty.remove();
  }
  var seen = {};
  feed.querySelectorAll('.issue-card[id]').forEach(function (card) {
    if (seen[card.id]) {
      card.remove();
    } else {
      seen[card.id] = true;
    }
  });
  // Submit form targets #issue-feed with afterbegin; close once the card is in.
  // (afterSwap sets detail.elt to the target, so detect via requestConfig path.)
  if (isIssueSubmitRequest(e.detail)) {
    closeDrawer();
  }
});

// Close the drawer after a successful issue *submission* (POST), not after the
// GET that loads the submit form into #drawer-body (that would slam it shut).
document.addEventListener('htmx:afterRequest', function (e) {
  if (!e.detail) return;
  var ok = e.detail.successful;
  if (ok === undefined && e.detail.xhr) {
    ok = e.detail.xhr.status >= 200 && e.detail.xhr.status < 300;
  }
  if (!ok) return;
  if (!isIssueSubmitRequest(e.detail)) return;
  closeDrawer();
});

// Server sends HX-Trigger / HX-Trigger-After-Swap: close-drawer on success.
// Listen on document (not body) so the handler is registered even if this
// script ever runs before <body> exists.
document.addEventListener('close-drawer', function () {
  closeDrawer();
});

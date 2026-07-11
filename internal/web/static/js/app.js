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
// tab: result | output | activity (legacy "diff" is remapped server-side to
// implementation workspace output)
// phase: research | plan | implementation (optional — defaults to the card's
// current phase, then research).
function openArtifactDrawer(issueId, tab, phase) {
  var resolvedTab = tab || 'result';
  var resolvedPhase = phase;
  if (resolvedTab === 'diff') {
    resolvedTab = 'output';
    if (!resolvedPhase) resolvedPhase = 'implementation';
  }
  if (!resolvedPhase) {
    var card = document.getElementById('issue-' + issueId);
    if (card && card.dataset.phase) {
      resolvedPhase = card.dataset.phase;
    }
  }
  var title = 'Issue #' + issueId;
  if (resolvedPhase) {
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
  // Match POST /partials/submit but not GET /partials/submit or /partials/submit/flavors.
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

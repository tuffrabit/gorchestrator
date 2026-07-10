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

// Attach CSRF header to every HTMX request (forms also post csrf_token).
document.addEventListener('htmx:configRequest', function (e) {
  var tok = csrfToken();
  if (tok) {
    e.detail.headers['X-CSRF-Token'] = tok;
  }
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

// After HTMX inserts a card (e.g. submit afterbegin), drop any duplicate ids.
document.addEventListener('htmx:afterSwap', function (e) {
  var feed = document.getElementById('issue-feed');
  if (!feed || !e.target || (e.target !== feed && !feed.contains(e.target))) return;
  var seen = {};
  feed.querySelectorAll('.issue-card[id]').forEach(function (card) {
    if (seen[card.id]) {
      card.remove();
    } else {
      seen[card.id] = true;
    }
  });
});

// Close the drawer after a successful issue *submission* (POST), not after the
// GET that loads the submit form into #drawer-body (that would slam it shut).
document.addEventListener('htmx:afterRequest', function (e) {
  if (!e.detail || !e.detail.successful) return;
  var path = (e.detail.pathInfo && e.detail.pathInfo.requestPath) || '';
  if (String(path).indexOf('/partials/submit') === -1) return;
  var verb = (e.detail.requestConfig && e.detail.requestConfig.verb) || '';
  if (String(verb).toLowerCase() !== 'post') return;
  closeDrawer();
});

// Server also sends HX-Trigger: close-drawer on successful POST /partials/submit.
document.body.addEventListener('close-drawer', function () {
  closeDrawer();
});

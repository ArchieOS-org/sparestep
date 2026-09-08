(() => {
  'use strict';

  const POLL_INTERVAL = 10000;
  const CSRF_KEY = 'sparestep.csrf';
  const $ = (id) => document.getElementById(id);

  let report = null;
  let csrf = readSessionValue(CSRF_KEY);
  let reportRequest = null;
  let pollTimer = null;
  let draftState = null;
  let lastDraftTrigger = null;

  class ApiError extends Error {
    constructor(status, message) {
      super(message);
      this.name = 'ApiError';
      this.status = status;
    }
  }

  function readSessionValue(key) {
    try {
      return window.sessionStorage.getItem(key) || '';
    } catch (_) {
      return '';
    }
  }

  function writeSessionValue(key, value) {
    try {
      window.sessionStorage.setItem(key, value);
    } catch (_) {
      // A private browsing policy can make sessionStorage unavailable. The
      // current page can still use the in-memory CSRF value.
    }
  }

  function removeSessionValue(key) {
    try {
      window.sessionStorage.removeItem(key);
    } catch (_) {
      // Nothing else is needed when storage is unavailable.
    }
  }

  function makeElement(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  function clearElement(node) {
    while (node.firstChild) node.removeChild(node.firstChild);
  }

  function setNotice(message, isError = false) {
    const notice = $('notice');
    notice.textContent = message || '';
    notice.classList.toggle('notice-error', Boolean(message && isError));
    notice.classList.toggle('notice-success', Boolean(message && !isError));
  }

  async function api(path, options = {}) {
    const method = (options.method || 'GET').toUpperCase();
    const headers = { ...(options.headers || {}) };
    if (options.body !== undefined && !headers['Content-Type']) {
      headers['Content-Type'] = 'application/json';
    }
    if (method !== 'GET' && !options.skipCsrf) {
      headers['X-CSRF-Token'] = csrf;
    }
    const response = await fetch(path, {
      ...options,
      method,
      headers,
      credentials: 'same-origin'
    });
    if (!response.ok) {
      const detail = (await response.text()).trim();
      throw new ApiError(response.status, detail || `Request failed (${response.status})`);
    }
    if (response.status === 204) return null;
    const text = await response.text();
    if (!text) return null;
    try {
      return JSON.parse(text);
    } catch (_) {
      throw new ApiError(response.status, 'The report returned an invalid response.');
    }
  }

  async function authenticate(token) {
    const response = await fetch('/api/auth', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ token })
    });
    if (!response.ok) {
      const detail = (await response.text()).trim();
      throw new ApiError(response.status, detail || 'The report token was rejected.');
    }
    const data = await response.json();
    if (!data || typeof data.csrf !== 'string' || !data.csrf) {
      throw new ApiError(500, 'The report did not return a CSRF token.');
    }
    csrf = data.csrf;
    writeSessionValue(CSRF_KEY, csrf);
    window.history.replaceState(null, document.title, window.location.pathname + window.location.search);
  }

  function tokenFromFragment() {
    const params = new URLSearchParams(window.location.hash.replace(/^#/, ''));
    const token = params.get('token') || '';
    return /^[0-9a-f]+$/i.test(token) ? token : '';
  }

  function connectionMessage() {
    return 'This report is not connected yet. Open the local report link printed by sparestep serve; it includes a one-time #token=… connection token.';
  }

  async function requestReport() {
    return api('/api/report');
  }

  async function loadReport(initial = false) {
    if (reportRequest) return reportRequest;
    reportRequest = (async () => {
      try {
        let next;
        try {
          next = await requestReport();
        } catch (error) {
          if (!initial || !(error instanceof ApiError) || error.status !== 401) throw error;
          const token = tokenFromFragment();
          if (!token) {
            setNotice(connectionMessage(), true);
            return false;
          }
          try {
            await authenticate(token);
            next = await requestReport();
          } catch (authError) {
            removeSessionValue(CSRF_KEY);
            csrf = '';
            setNotice('The report token could not connect. Open a fresh local report link from sparestep serve.', true);
            return false;
          }
        }
        report = next || {};
        renderReport(report);
        setNotice('');
        return true;
      } catch (error) {
        if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
          setNotice('The report connection has expired. Open a fresh local report link from sparestep serve.', true);
        } else {
          setNotice(`Could not refresh the report: ${error.message || 'unknown error'}`, true);
        }
        return false;
      } finally {
        reportRequest = null;
      }
    })();
    return reportRequest;
  }

  function statusText(value) {
    if (value.paused || value.status === 'paused') return 'Recording is paused.';
    switch (value.status) {
      case 'recording':
      case 'working':
        return 'Codex is recording activity.';
      case 'checks_passed':
        return 'Checks passed after the latest edit.';
      case 'needs_attention':
        return 'Some checks need attention.';
      case 'no_activity':
        return 'Waiting for the first recorded task.';
      default:
        return Number(value.events_count || 0) === 0
          ? 'Waiting for the first recorded task.'
          : 'Activity is recorded. Open the evidence to see what was checked.';
    }
  }

  function number(value) {
    return Number(value || 0).toLocaleString();
  }

  function duration(milliseconds) {
    const value = Number(milliseconds || 0);
    if (!value) return '0 seconds';
    if (value < 1000) return `${value} ms`;
    if (value < 60000) return `${(value / 1000).toFixed(1)} seconds`;
    const minutes = value / 60000;
    return `${minutes < 10 ? minutes.toFixed(1) : Math.round(minutes)} minutes`;
  }

  function eventAnchorId(id) {
    return `event-${String(id || '').replace(/[^a-z0-9_-]/gi, '-')}`;
  }

  function eventSummary(event) {
    return event.summary || event.command || event.tool || event.kind || 'Recorded event';
  }

  function isOpen(finding) {
    return finding.disposition !== 'necessary' && finding.disposition !== 'dismissed';
  }

  function renderReport(value) {
    const events = Array.isArray(value.events) ? value.events : [];
    const findings = Array.isArray(value.findings) ? value.findings : [];
    const isDemo = events.some((event) => event && event.source === 'demo');
    const hostname = isDemo ? 'Example Linux VM' : value.hostname || window.location.hostname || 'This computer';

    $('project').textContent = (value.project || '').split('/').filter(Boolean).pop() || 'Your report';
    $('project-path').textContent = value.project || 'Not provided';
    $('host').textContent = hostname;
    $('task-context').textContent = value.task_id ? ` · Task ${value.task_id}` : '';
    $('status').textContent = statusText(value);
    $('pause').textContent = value.paused ? 'Resume capture' : 'Pause capture';
    $('pause').disabled = false;

    $('demo-banner').hidden = !isDemo;

    const checks = Number(value.checks || 0);
    const unknown = Number(value.unknown_checks || 0);
    const failed = Number(value.failed_checks || 0);
    $('checks').textContent = number(checks);
    if (!checks) {
      $('check-coverage').textContent = 'No check results were recorded.';
    } else if (unknown) {
      const failureText = failed ? ` ${number(failed)} failed.` : '';
      $('check-coverage').textContent = `${number(unknown)} check${unknown === 1 ? '' : 's'} did not report an outcome.${failureText}`;
    } else if (failed) {
      $('check-coverage').textContent = `${number(failed)} check${failed === 1 ? '' : 's'} failed.`;
    } else {
      $('check-coverage').textContent = 'Every recorded check reported an outcome.';
    }

    const elapsed = Number(value.elapsed_ms || 0);
    $('elapsed').textContent = elapsed ? duration(elapsed) : '—';
    if (value.token_usage && typeof value.token_usage.total === 'number') {
      $('usage').textContent = `${number(value.token_usage.total)} tokens`;
      const source = value.token_usage.source ? ` Source: ${value.token_usage.source}.` : '';
      $('usage-note').textContent = (value.usage_note || 'Recorded token total.') + source;
    } else {
      $('usage').textContent = 'Unknown';
      $('usage-note').textContent = value.usage_note || 'Token totals are unavailable for this connection.';
    }

    renderFindings(findings, events);
    renderEvidence(events);
  }

  function renderFindings(findings, events) {
    const open = findings.filter(isOpen);
    const history = findings.filter((finding) => !isOpen(finding));
    $('finding-count').textContent = open.length ? `${number(open.length)} open` : 'None open';
    clearElement($('findings'));
    if (!open.length) {
      $('findings').appendChild(makeElement('p', 'empty', 'No open findings in the activity we recorded.'));
    } else {
      open.slice(0, 3).forEach((finding) => $('findings').appendChild(createFindingCard(finding, events, false)));
      if (open.length > 3) {
        $('findings').appendChild(makeElement('p', 'list-note', `Showing the 3 clearest findings out of ${number(open.length)} open findings.`));
      }
    }

    $('history-section').hidden = history.length === 0;
    clearElement($('history'));
    history.forEach((finding) => $('history').appendChild(createFindingCard(finding, events, true)));
  }

  function createFindingCard(finding, events, historical) {
    const article = makeElement('article', `finding${historical ? ' finding-history' : ''}`);
    article.dataset.findingId = finding.id || '';

    const heading = makeElement('div', 'finding-heading');
    const title = makeElement('h3', '', finding.title || 'Untitled finding');
    heading.appendChild(title);
    if (finding.issue_url) heading.appendChild(makeElement('span', 'linked-badge', 'Linked'));
    if (historical) heading.appendChild(makeElement('span', `disposition disposition-${finding.disposition}`, finding.disposition === 'necessary' ? 'Marked necessary' : 'Dismissed'));
    article.appendChild(heading);

    article.appendChild(makeElement('p', 'finding-explanation', finding.explanation || 'No explanation was recorded.'));
    const meta = makeElement('p', 'meta');
    meta.textContent = `${number(finding.occurrences)} occurrence${Number(finding.occurrences) === 1 ? '' : 's'} · ${finding.confidence || 'Unknown'} confidence`;
    article.appendChild(meta);
    article.appendChild(makeElement('p', 'observed-duration', `Observed duration: ${duration(finding.duration_ms)} (not estimated savings).`));
    if (finding.suggestion) article.appendChild(makeElement('p', 'suggestion', finding.suggestion));

    const evidence = makeElement('div', 'finding-evidence');
    const evidenceHeading = makeElement('span', 'evidence-label', 'Evidence:');
    evidence.appendChild(evidenceHeading);
    const byId = new Map(events.map((event) => [event.id, event]));
    const ids = Array.isArray(finding.evidence_ids) ? finding.evidence_ids : [];
    if (!ids.length) {
      evidence.appendChild(makeElement('span', 'muted', ' No linked events recorded.'));
    } else {
      const list = makeElement('ul');
      ids.forEach((id, index) => {
        const item = makeElement('li');
        const event = byId.get(id);
        if (event) {
          const link = makeElement('a', '', `Event ${index + 1}: ${eventSummary(event)}`);
          link.href = `#${eventAnchorId(event.id)}`;
          item.appendChild(link);
        } else {
          item.appendChild(makeElement('span', 'muted', `Event ${index + 1}: no longer present in this report`));
        }
        list.appendChild(item);
      });
      evidence.appendChild(list);
    }
    article.appendChild(evidence);

    if (finding.issue_url) {
      const link = makeElement('a', 'issue-link', 'Open linked Linear issue');
      link.href = finding.issue_url;
      link.target = '_blank';
      link.rel = 'noopener noreferrer';
      article.appendChild(link);
    }

    const actions = makeElement('div', 'finding-actions');
    if (historical) {
      actions.appendChild(actionButton('Undo', 'open', finding.id));
    } else {
      actions.appendChild(actionButton('This was necessary', 'necessary', finding.id));
      actions.appendChild(actionButton('Dismiss', 'dismissed', finding.id));
    }
    const draft = makeElement('button', '', 'Review issue draft');
    draft.type = 'button';
    draft.dataset.draftId = finding.id || '';
    draft.addEventListener('click', () => openDraft(finding, draft));
    actions.appendChild(draft);
    article.appendChild(actions);
    return article;
  }

  function actionButton(label, value, id) {
    const button = makeElement('button', '', label);
    button.type = 'button';
    button.dataset.dispositionId = id || '';
    button.dataset.dispositionValue = value;
    button.addEventListener('click', () => changeDisposition(id, value, button));
    return button;
  }

  async function changeDisposition(id, value, button) {
    const card = button.closest('.finding');
    const buttons = card ? Array.from(card.querySelectorAll('button')) : [button];
    buttons.forEach((item) => { item.disabled = true; });
    try {
      await api('/api/finding/disposition', {
        method: 'POST',
        body: JSON.stringify({ id, value })
      });
      const refreshed = await loadReport();
      if (!refreshed) buttons.forEach((item) => { item.disabled = false; });
    } catch (error) {
      setNotice(`Could not save that feedback: ${error.message || 'unknown error'}`, true);
      buttons.forEach((item) => { item.disabled = false; });
    }
  }

  function renderEvidence(events) {
    $('evidence-count').textContent = events.length ? `(${number(events.length)})` : '(none)';
    clearElement($('evidence'));
    if (!events.length) {
      $('evidence').appendChild(makeElement('p', 'empty', 'No evidence has been recorded yet.'));
      return;
    }
    events.forEach((event) => {
      const row = makeElement('article', 'event');
      row.id = eventAnchorId(event.id);
      const title = makeElement('h3', '', eventSummary(event));
      if (event.source === 'demo') title.appendChild(makeElement('span', 'badge', 'Demo source'));
      row.appendChild(title);
      const details = makeElement('p', 'event-details');
      const bits = [event.kind || 'Activity'];
      if (event.tool) bits.push(event.tool);
      if (event.duration_ms) bits.push(`Observed duration: ${duration(event.duration_ms)}`);
      details.textContent = bits.join(' · ');
      row.appendChild(details);
      if (event.timestamp) {
        const time = makeElement('time', 'event-time');
        const date = new Date(event.timestamp);
        time.dateTime = date.toISOString();
        time.textContent = Number.isNaN(date.getTime()) ? String(event.timestamp) : date.toLocaleString();
        row.appendChild(time);
      }
      if (event.command) row.appendChild(makeElement('p', 'event-command', event.command));
      if (event.gap) row.appendChild(makeElement('p', 'event-gap', `Recording note: ${event.gap}`));
      $('evidence').appendChild(row);
    });
  }

  const dialog = $('draft-dialog');
  const draftControls = [$('draft-title'), $('draft-body'), $('draft-download'), $('draft-open-linear'), $('issue-url'), $('issue-save')];

  function setDraftBusy(busy) {
    draftControls.forEach((control) => { control.disabled = busy; });
  }

  function setDraftError(message) {
    $('draft-error').textContent = message || '';
  }

  function setIssueStatus(message, isError = false) {
    const status = $('issue-status');
    status.textContent = message || '';
    status.classList.toggle('status-error', Boolean(message && isError));
  }

  async function openDraft(finding, trigger) {
    lastDraftTrigger = trigger;
    draftState = { id: finding.id, finding, data: null };
    $('draft-finding-label').textContent = finding.title || 'Finding';
    $('draft-title').value = '';
    $('draft-body').value = '';
    $('issue-url').value = '';
    setDraftError('Loading the generated draft…');
    setIssueStatus('');
    setDraftBusy(true);
    if (!dialog.open) dialog.showModal();
    try {
      const data = await api('/api/draft', {
        method: 'POST',
        body: JSON.stringify({ id: finding.id })
      });
      if (!draftState || draftState.id !== finding.id) return;
      draftState.data = data || {};
      $('draft-title').value = data.title || '';
      $('draft-body').value = data.body || '';
      $('issue-url').value = data.issue_url || '';
      setDraftError('');
      setIssueStatus(data.issue_url ? 'Linked to Linear.' : 'No Linear issue is linked yet.');
      if (data.issue_url) {
        finding.issue_url = data.issue_url;
        renderFindings(report.findings || [], report.events || []);
      }
    } catch (error) {
      setDraftError(`Could not load the issue draft: ${error.message || 'unknown error'}`);
    } finally {
      setDraftBusy(false);
    }
  }

  function closeDraft() {
    if (dialog.open) dialog.close();
    setDraftError('');
    if (lastDraftTrigger) lastDraftTrigger.focus();
    lastDraftTrigger = null;
    draftState = null;
  }

  function editedDraftValues() {
    const title = $('draft-title').value.trim();
    const body = $('draft-body').value.trim();
    if (!title) {
      setDraftError('Add an issue title before continuing.');
      $('draft-title').focus();
      return null;
    }
    return { title, body };
  }

  function downloadDraft() {
    const values = editedDraftValues();
    if (!values) return;
    const markdown = `# ${values.title}\n\n${values.body}\n`;
    const url = window.URL.createObjectURL(new Blob([markdown], { type: 'text/markdown;charset=utf-8' }));
    const anchor = makeElement('a');
    anchor.href = url;
    anchor.download = 'sparestep-draft.md';
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    window.setTimeout(() => window.URL.revokeObjectURL(url), 0);
  }

  function openLinear() {
    const values = editedDraftValues();
    if (!values) return;
    const url = `https://linear.new?title=${encodeURIComponent(values.title)}&description=${encodeURIComponent(values.body)}`;
    window.open(url, '_blank', 'noopener');
  }

  async function saveIssueLink() {
    if (!draftState || !draftState.id) return;
    const value = $('issue-url').value.trim();
    let parsed;
    try {
      parsed = new URL(value);
    } catch (_) {
      setIssueStatus('Enter a full https://linear.app issue URL.', true);
      return;
    }
    if (parsed.protocol !== 'https:' || parsed.hostname !== 'linear.app' || !parsed.pathname || parsed.username || parsed.password) {
      setIssueStatus('Enter a full https://linear.app issue URL.', true);
      return;
    }
    const button = $('issue-save');
    button.disabled = true;
    setIssueStatus('Saving link…');
    try {
      await api('/api/issue-url', {
        method: 'POST',
        body: JSON.stringify({ id: draftState.id, url: value })
      });
      draftState.data = draftState.data || {};
      draftState.data.issue_url = value;
      draftState.data.state = 'linked';
      const finding = (report.findings || []).find((item) => item.id === draftState.id);
      if (finding) finding.issue_url = value;
      setIssueStatus('Linked to Linear.');
      renderFindings(report.findings || [], report.events || []);
    } catch (error) {
      setIssueStatus(`Could not save the link: ${error.message || 'unknown error'}`, true);
    } finally {
      button.disabled = false;
    }
  }

  function schedulePoll() {
    if (pollTimer) window.clearTimeout(pollTimer);
    pollTimer = null;
    if (document.visibilityState !== 'visible') return;
    pollTimer = window.setTimeout(async () => {
      pollTimer = null;
      if (document.visibilityState === 'visible') await loadReport();
      schedulePoll();
    }, POLL_INTERVAL);
  }

  $('refresh').addEventListener('click', async () => {
    const button = $('refresh');
    button.disabled = true;
    await loadReport();
    button.disabled = false;
    schedulePoll();
  });

  $('pause').addEventListener('click', async () => {
    if (!report) return;
    const button = $('pause');
    button.disabled = true;
    try {
      await api('/api/pause', { method: 'POST', body: JSON.stringify({ paused: !report.paused }) });
      const refreshed = await loadReport();
      if (!refreshed) button.disabled = false;
    } catch (error) {
      setNotice(`Could not change recording: ${error.message || 'unknown error'}`, true);
      button.disabled = false;
    }
  });

  $('draft-close').addEventListener('click', closeDraft);
  $('draft-download').addEventListener('click', downloadDraft);
  $('draft-open-linear').addEventListener('click', openLinear);
  $('issue-save').addEventListener('click', saveIssueLink);
  $('draft-form').addEventListener('submit', (event) => event.preventDefault());
  dialog.addEventListener('cancel', () => closeDraft());
  dialog.addEventListener('click', (event) => {
    if (event.target === dialog) closeDraft();
  });
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') {
      loadReport();
      schedulePoll();
    } else if (pollTimer) {
      window.clearTimeout(pollTimer);
      pollTimer = null;
    }
  });

  loadReport(true).finally(schedulePoll);
})();

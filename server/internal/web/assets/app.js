/* Smart Remote dashboard.
 *
 * Every status is conveyed by text as well as colour, and the layout keeps
 * working if the web fonts fail to load, because Inter and JetBrains Mono are
 * declared with system fallbacks rather than required.
 */
'use strict';

const $ = (id) => document.getElementById(id);

/** Poll interval for the REST status refresh, in ms. */
const POLL_MS = 4000;

let socket = null;
let reconnectDelay = 1000;

/** Fetch JSON, tolerating a non-2xx response without throwing. */
async function getJSON(url) {
  const res = await fetch(url, { cache: 'no-store' });
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
  return res.json();
}

/** Set a status pill; text always accompanies the colour. */
function setPill(id, state, text) {
  const el = $(id);
  el.dataset.state = state;
  el.innerHTML = '<i class="dot"></i>' + text;
}

function renderStatus(s) {
  $('pin').textContent = s.pin || '------';
  $('addr').textContent = `${s.host}:${s.port}`;
  $('hostname').textContent = s.hostname || 'â€”';
  $('version').textContent = s.version || 'â€”';
  $('uptime').textContent = s.uptime || 'â€”';
  $('display').textContent = s.display
    ? `${s.display.width}Ã—${s.display.height} @ ${s.display.refreshHz}Hz`
    : 'â€”';

  setPill('pill-server', 'ok', 'Server');
  setPill('pill-clients', s.clients > 0 ? 'ok' : 'warn', `${s.clients} clients`);

  // appVersion and developer are sent by /api/panel/status. They are optional
  // so an older server still renders a footer rather than printing "undefined".
  const rel = s.appVersion ? ` v${s.appVersion}` : '';
  const by = s.developer ? ` Â· by ${s.developer}` : '';

  $('foot-stats').textContent =
    `${s.commandsRun} commands Â· ${s.commandsFailed} failed Â· ` +
    `${s.macroCount} macros Â· protocol ${s.version}${rel}${by}`;
}

/** Reflect input health, including the UIPI diagnosis when blocked. */
function renderInput(s) {
  const line = $('input-status');
  const diag = $('input-diagnosis');
  if (s.inputBlocked) {
    line.textContent = 'Input is blocked';
    line.className = 'status-line bad';
    diag.hidden = false;
    diag.textContent = s.diagnosis || 'SendInput is failing repeatedly.';
  } else {
    line.textContent = 'Input is working';
    line.className = 'status-line ok';
    diag.hidden = true;
  }
}

async function renderNet() {
  let n;
  try {
    n = await getJSON('/api/net');
  } catch {
    return;
  }
  $('firewall').textContent = n.firewallOk ? 'Allowed' : 'No rule';
  $('profile').textContent = n.networkProfile || 'Unknown';
  $('mdns').textContent = n.mdnsActive ? 'Advertising' : 'Off';

  const list = $('advice');
  list.textContent = '';
  (n.advice || []).forEach((text) => {
    const li = document.createElement('li');
    li.textContent = text;
    list.appendChild(li);
  });
}

async function renderMacros() {
  let macros;
  try {
    macros = await getJSON('/api/macros');
  } catch {
    return;
  }
  const list = $('macros');
  list.textContent = '';
  macros.forEach((m) => {
    const li = document.createElement('li');
    li.className = 'macro-item';

    // The label is the primary signal and the kind is secondary. An icon is
    // deliberately not used as the only identifier.
    const label = document.createElement('span');
    label.className = 'label';
    label.textContent = m.label;

    const kind = document.createElement('span');
    kind.className = 'kind';
    kind.textContent = m.kind;

    li.append(label, kind);
    list.appendChild(li);
  });
}


async function copyPin() {
  const pin = $('pin').textContent.trim();
  const note = $('copy-note');
  try {
    // The clipboard API needs a secure context; fall back to selection so
    // copying still works when the dashboard is opened over plain HTTP.
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(pin);
      note.textContent = 'PIN copied to clipboard.';
      return;
    }
    const range = document.createRange();
    range.selectNodeContents($('pin'));
    const sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
    note.textContent = 'Press Ctrl+C to copy the selected PIN.';
  } catch {
    note.textContent = 'Could not copy â€” select the PIN and copy manually.';
    note.className = 'note danger';
  }
}

async function regeneratePin() {
  const note = $('copy-note');
  note.className = 'note';
  try {
    const res = await fetch('/api/pin', { method: 'POST' });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || res.statusText);
    $('pin').textContent = data.pin;
    note.textContent = 'New PIN generated. Existing clients must pair again.';
  } catch (err) {
    note.textContent = 'Could not regenerate: ' + err.message;
    note.className = 'note danger';
  }
}

/** Open the live event socket, reconnecting with backoff. */
function connect() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  socket = new WebSocket(`${proto}//${location.host}/ws`);

  socket.onopen = () => {
    reconnectDelay = 1000;
    setPill('pill-socket', 'ok', 'Live');
  };

  socket.onclose = () => {
    setPill('pill-socket', 'bad', 'Reconnecting');
    // Exponential backoff, capped, so a stopped server does not spin.
    setTimeout(connect, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * 1.7, 15000);
  };

  socket.onerror = () => socket.close();

  socket.onmessage = (ev) => {
    let msg;
    try {
      msg = JSON.parse(ev.data);
    } catch {
      return;
    }
    // Events prompt an immediate refresh so the dashboard is never stale.
    if (msg.kind === 'pin_changed') refresh();
    if (msg.kind === 'macros_changed' || msg.kind === 'clients_changed') {
      refresh();
      renderMacros();
    }
  };
}

async function refresh() {
  try {
    const status = await getJSON('/api/panel/status');
    renderStatus(status);
    renderInput(status);
  } catch {
    setPill('pill-server', 'bad', 'Server unreachable');
  }
}

function init() {
  $('btn-copy').addEventListener('click', copyPin);
  $('btn-regen').addEventListener('click', regeneratePin);
  $('btn-reload').addEventListener('click', () => {
    renderMacros();
    renderNet();
  });

  refresh();
  renderMacros();
  renderNet();
  connect();
  setInterval(refresh, POLL_MS);
  setInterval(renderNet, POLL_MS * 3);
}

document.addEventListener('DOMContentLoaded', init);

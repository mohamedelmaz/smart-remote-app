/* Smart Remote dashboard.
 *
 * Every status is conveyed by text as well as colour, and the layout keeps
 * working if the web fonts fail to load, because Inter and JetBrains Mono are
 * declared with system fallbacks rather than required.
 */
'use strict';

// Disable browser context menu for professional app feel
document.addEventListener('contextmenu', e => e.preventDefault());

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

  const serving = s.serving !== false;
  setPill('pill-server', serving ? 'ok' : 'warn', serving ? 'Server' : 'Stopped');
  const deviceCount = Array.isArray(s.devices) ? s.devices.length : (s.clients || 0);
  setPill('pill-clients', deviceCount > 0 ? 'ok' : 'warn', `${deviceCount} clients`);
  renderDevices(s.devices || [], serving);
  renderBlocked(s.blocked || [], !!s.blockCorrupt);

  // appVersion and developer are sent by /api/panel/status. They are optional
  // so an older server still renders a footer rather than printing "undefined".
  const rel = s.appVersion ? ` v${s.appVersion}` : '';
  const by = s.developer ? ` Â· by ${s.developer}` : '';

  $('foot-stats').textContent =
    `${s.commandsRun} commands Â· ${s.commandsFailed} failed Â· ` +
    `${s.macroCount} macros Â· protocol ${s.version}${rel}${by}`;
}

/** Render one row per connected device: name + address + time, never a bare count. */
function renderDevices(devices, serving) {
  const list = $('devices');
  const hint = $('devices-hint');
  if (!list || !hint) return;
  list.textContent = '';
  if (!serving) {
    hint.textContent = 'Server stopped from tray - press Activate server to resume.';
  } else if (devices.length === 0) {
    hint.textContent = 'No phones connected yet.';
  } else {
    hint.textContent = `${devices.length} phone${devices.length === 1 ? '' : 's'} connected.`;
  }
  devices.forEach((d) => {
    const li = document.createElement('li');
    li.className = 'device-item' + (d.authenticated ? '' : ' pending');

    // textContent everywhere: a hostile device name can never inject markup.
    const avatar = document.createElement('span');
    avatar.className = 'avatar';
    const label = (d.name || '').trim() || 'Unknown device';
    avatar.textContent = label.slice(0, 1).toUpperCase();

    const meta = document.createElement('div');
    meta.className = 'meta';
    const name = document.createElement('span');
    name.className = 'name';
    name.textContent = label;
    const sub = document.createElement('div');
    sub.className = 'sub mono';
    const when = d.connectedAt ? ` - ${d.connectedAt}` : '';
    sub.textContent = `${d.addr || 'unknown address'}${when}`;

    const badge = document.createElement('span');
    badge.className = 'badge';
    badge.textContent = d.authenticated ? 'Paired' : 'Pairing';

    const block = document.createElement('button');
    block.className = 'btn btn-small btn-danger';
    block.textContent = 'Block';
    block.setAttribute('aria-label', `Block ${label}`);
    block.addEventListener('click', () => setBlocked(d.addr, d.name || label, true));

    meta.append(name, sub);
    li.append(avatar, meta, badge, block);
    list.appendChild(li);
  });
}

/** Render the refused IPs with an Unblock action each. */
function renderBlocked(blocked, corrupt) {
  const list = $('blocked');
  const head = $('blocked-head');
  const hint = $('blocked-hint');
  if (!list || !head || !hint) return;
  list.textContent = '';
  head.hidden = blocked.length === 0 && !corrupt;
  if (corrupt) {
    hint.hidden = false;
    hint.textContent = 'The block file was corrupt and was reset; the list started empty.';
  } else if (blocked.length > 0) {
    hint.hidden = false;
    hint.textContent = 'Blocked phones are refused even with the right PIN.';
  } else {
    hint.hidden = true;
  }
  blocked.forEach((b) => {
    const li = document.createElement('li');
    li.className = 'device-item blocked-row';

    const avatar = document.createElement('span');
    avatar.className = 'avatar muted';
    const label = (b.name || '').trim() || b.ip || 'Unknown device';
    avatar.textContent = label.slice(0, 1).toUpperCase();

    const meta = document.createElement('div');
    meta.className = 'meta';
    const name = document.createElement('span');
    name.className = 'name';
    name.textContent = label;
    const sub = document.createElement('div');
    sub.className = 'sub mono';
    const when = b.blockedAt ? ` - ${b.blockedAt}` : '';
    sub.textContent = `${b.ip || 'unknown address'}${when}`;

    const unblock = document.createElement('button');
    unblock.className = 'btn btn-small';
    unblock.textContent = 'Unblock';
    unblock.setAttribute('aria-label', `Unblock ${label}`);
    unblock.addEventListener('click', () => setBlocked(b.ip, '', false));

    meta.append(name, sub);
    li.append(avatar, meta, unblock);
    list.appendChild(li);
  });
}

/** Block or unblock one IP, then refresh immediately so the UI never lies. */
async function setBlocked(ip, name, block) {
  try {
    const res = await fetch(block ? '/api/devices/block' : '/api/devices/unblock', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(block ? { ip, name } : { ip }),
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      throw new Error(data.error || res.statusText);
    }
  } catch (err) {
    const hint = $('devices-hint');
    if (hint) hint.textContent = 'Could not update block: ' + err.message;
    return;
  }
  refresh();
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
    // (The macro count in the footer refreshes here too; the deck itself
    // lives on the phone, which reads /api/macros directly.)
    if (msg.kind === 'pin_changed' || msg.kind === 'macros_changed' ||
        msg.kind === 'clients_changed' || msg.kind === 'blocked_changed') {
      refresh();
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

  refresh();
  renderNet();
  connect();
  setInterval(refresh, POLL_MS);
  setInterval(renderNet, POLL_MS * 3);
}

document.addEventListener('DOMContentLoaded', init);

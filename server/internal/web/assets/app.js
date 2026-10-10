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

/** Consecutive failed refreshes before the server is declared gone. */
const OFFLINE_AFTER = 3;

/** Health probe run after the event socket drops: two tries, 300ms apart. */
const PROBE_TIMEOUT_MS = 800;
const PROBE_GAP_MS = 300;

let socket = null;
let reconnectDelay = 1000;

/** Failed refreshes in a row, and whether the closed screen is up. */
let failStreak = 0;
let closed = false;

/** True while a health probe is in flight, so overlapping ones cannot race. */
let probing = false;

/** Fetch JSON, tolerating a non-2xx response without throwing. */
async function getJSON(url) {
  const res = await fetch(url, { cache: 'no-store' });
  if (!res.ok) {
    // The status travels with the error: a 401 means "locked", which is a
    // completely different situation from "the server is not answering".
    const err = new Error(`${res.status} ${res.statusText}`);
    err.status = res.status;
    throw err;
  }
  return res.json();
}

/** Set a status pill; text always accompanies the colour. */
function setPill(id, state, text) {
  const el = $(id);
  // The pills no longer exist once the closed screen has replaced the
  // dashboard, but the websocket and the poller keep calling in.
  if (!el) return;
  el.dataset.state = state;
  el.innerHTML = '<i class="dot"></i>' + text;
}

function renderStatus(s) {
  $('pin').textContent = s.pin || '------';
  $('addr').textContent = `${s.host}:${s.port}`;
  $('hostname').textContent = s.hostname || '\u2014';
  $('version').textContent = s.version || '\u2014';
  $('uptime').textContent = s.uptime || '\u2014';
  // The refresh rate is printed only when the driver reported one. A desktop
  // that answers 0 has told us nothing, and "0Hz" reads as a broken reading
  // rather than as an absent one. Escapes, not literal characters: a save in
  // another code page would turn these back into Windows-1252 mojibake.
  const refresh = s.display && s.display.refreshHz > 1
    ? ` @ ${s.display.refreshHz}Hz`
    : '';
  $('display').textContent = s.display
    ? `${s.display.width}\u00d7${s.display.height}${refresh}`
    : '\u2014';

  const serving = s.serving !== false;
  lastServing = serving;
  setPill('pill-server', serving ? 'ok' : 'warn', serving ? 'Server' : 'Stopped');
  const deviceCount = Array.isArray(s.devices) ? s.devices.length : (s.clients || 0);
  setPill('pill-clients', deviceCount > 0 ? 'ok' : 'warn', `${deviceCount} clients`);
  renderDevices(s.devices || [], serving);
  renderBlocked(s.blocked || [], !!s.blockCorrupt);

  // appVersion and developer are sent by /api/panel/status. They are optional
  // so an older server still renders a footer rather than printing "undefined".
  const rel = s.appVersion ? ` v${s.appVersion}` : '';
  const by = s.developer ? ` \u00b7 by ${s.developer}` : '';

  $('foot-stats').textContent =
    `${s.commandsRun} commands \u00b7 ${s.commandsFailed} failed \u00b7 ` +
    `${s.macroCount} macros \u00b7 protocol ${s.version}${rel}${by}`;
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
  // Same reason as setPill: the diagnostics nodes are gone once the server has
  // been declared closed, and this runs on its own slower interval.
  if (!$('firewall')) return;
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
    note.textContent = 'Could not copy \u2014 select the PIN and copy manually.';
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

/**
 * Decide whether the process is really gone after the event socket dropped.
 *
 * A dropped socket is the earliest sign there is - quitting the server stops
 * answering /healthz in the same instant - but it proves nothing on its own:
 * the tray's "Close server" drops every socket while the HTTP listener stays
 * bound and keeps serving. Two quick health checks separate the two cases.
 * If /healthz answers, the normal reconnect path deals with it. If it does
 * not, the process has gone and the closed screen goes up now instead of
 * after three more poll intervals.
 */
async function probeServer() {
  if (closed || probing) return;
  probing = true;
  try {
    for (let attempt = 0; attempt < 2; attempt++) {
      if (attempt > 0) {
        await new Promise((resolve) => setTimeout(resolve, PROBE_GAP_MS));
      }
      // A dropped packet would otherwise hang the probe for the browser's own
      // timeout, which is far longer than this whole decision should take.
      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), PROBE_TIMEOUT_MS);
      let alive = false;
      try {
        const res = await fetch('/healthz', {
          cache: 'no-store',
          signal: controller.signal,
        });
        alive = res.ok;
      } catch {
        alive = false;
      } finally {
        clearTimeout(timer);
      }
      if (alive) return;
    }
    if (!closed) showClosedScreen();
  } finally {
    probing = false;
  }
}

/** True while the security lock is holding the dashboard. */
let lockedNow = false;

/** The action the tray asked for through #do=, executed only after a Confirm. */
let pendingAction = '';

/** Open the live event socket, reconnecting with backoff. */
function connect() {
  // A locked dashboard has no business on this socket: it would be refused,
  // and the retry loop would hammer the server for nothing.
  if (lockedNow) return;
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  socket = new WebSocket(`${proto}//${location.host}/ws`);

  socket.onopen = () => {
    reconnectDelay = 1000;
    setPill('pill-socket', 'ok', 'Live');
  };

  socket.onclose = () => {
    setPill('pill-socket', 'bad', 'Reconnecting');
    // Ask whether the process is gone rather than waiting out three poll
    // intervals: the socket is already dead, so the answer is available now.
    probeServer();
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

/**
 * Destroy the dashboard and show that the server is gone.
 *
 * The nodes are removed rather than hidden: a PIN or a device name left in the
 * document can still be read by anyone opening the developer tools, and this
 * page may sit open on an unattended PC. Removing them in one step is also the
 * only way to be sure nothing sensitive survives anywhere in the markup.
 */
function showClosedScreen() {
  closed = true;
  document.title = 'Smart Remote - closed';

  const shell = document.createElement('div');
  shell.className = 'shell closed-note';
  const card = document.createElement('section');
  card.className = 'card';
  const title = document.createElement('h1');
  title.textContent = 'Server closed';
  const line = document.createElement('p');
  line.className = 'hint';
  line.textContent = 'Smart Remote is not running. You can close this tab.';
  card.append(title, line);
  shell.appendChild(card);
  document.body.replaceChildren(shell);

  // Chrome refuses this for a tab it did not open from script, which is the
  // normal case here. The screen is the real answer; this is a courtesy for a
  // browser that does allow it.
  try {
    window.close();
  } catch {
    /* nothing to do: the screen above already says it */
  }
}

async function refresh() {
  try {
    const status = await getJSON('/api/panel/status');
    if (closed) {
      // The server is back. Reload rather than rebuild: the dashboard this
      // page was showing has been destroyed, and a fresh document is both
      // simpler and impossible to get out of step with the server.
      location.reload();
      return;
    }
    failStreak = 0;
    if (lockedNow) {
      // The lock came back on: this tab no longer has a session.
      showLockScreen();
      return;
    }
    renderStatus(status);
    renderInput(status);
  } catch (err) {
    if (err.status === 401) {
      // A 401 is an answer, not an outage. The server is plainly alive, so
      // this must not touch failStreak and must never reach the closed screen.
      if (!lockedNow) showLockScreen();
      return;
    }
    failStreak++;
    if (closed) return;
    // One or two failures are a blip and stay exactly as they are today; only
    // a run long enough to matter closes the page.
    if (failStreak >= OFFLINE_AFTER) {
      showClosedScreen();
      return;
    }
    setPill('pill-server', 'bad', 'Server unreachable');
  }
}

/** Read the action the tray put in the fragment, if any. */
function requestedAction() {
  const m = /^#do=(newpin|toggle|quit)$/.exec(location.hash);
  return m ? m[1] : '';
}

/**
 * Build a code field.
 *
 * It is deliberately NOT type="password". Chrome gives a password field a key
 * icon, offers to save it, and will then fill it in by itself - which would
 * hand anyone who walks up to the machine the code and quietly defeat the
 * whole lock. A plain text field plus the masking in the stylesheet looks the
 * same and cannot be stored by a password manager.
 *
 * Every password manager's opt-out attribute is set as well, and no id or name
 * here mentions the secret: some managers key off the field name.
 */
function makeCodeInput(id) {
  const input = document.createElement('input');
  input.type = 'text';
  input.className = 'code-input';
  input.id = id;
  input.name = id;
  input.autocomplete = 'off';
  // autocorrect and autocapitalize are not reflected IDL attributes in every
  // browser: assigning to the property can leave a plain expando behind and
  // write no attribute at all. setAttribute always puts it in the markup.
  input.setAttribute('autocapitalize', 'off');
  input.setAttribute('autocorrect', 'off');
  input.spellcheck = false;
  input.setAttribute('data-lpignore', 'true');
  input.setAttribute('data-1p-ignore', 'true');
  input.setAttribute('data-bwignore', 'true');
  return input;
}

/** The dot and wordmark above a lock screen, in the dashboard's header style. */
function lockBrand() {
  const brand = document.createElement('div');
  brand.className = 'lock-brand';
  const dot = document.createElement('span');
  dot.className = 'brand-dot';
  dot.setAttribute('aria-hidden', 'true');
  const name = document.createElement('h1');
  name.textContent = 'Smart Remote';
  brand.append(dot, name);
  return brand;
}

/**
 * Replace the dashboard with the code form.
 *
 * The same wipe the closed screen uses is applied first: a PIN or a device
 * name must not survive in the document just because the lock came back on.
 */
function showLockScreen() {
  lockedNow = true;
  if (socket) {
    try { socket.close(); } catch { /* already gone */ }
    socket = null;
  }
  pendingAction = requestedAction();

  const shell = document.createElement('div');
  shell.className = 'shell lock-note';

  const card = document.createElement('section');
  card.className = 'card lock-card';
  const title = document.createElement('h2');
  title.textContent = 'Security lock';
  card.appendChild(title);

  const line = document.createElement('p');
  line.className = 'hint';
  line.textContent = 'Enter the code to show this dashboard.';
  card.appendChild(line);

  const input = makeCodeInput('lock-entry');
  input.placeholder = 'Code';
  card.appendChild(input);

  const row = document.createElement('div');
  row.className = 'row';
  const go = document.createElement('button');
  go.className = 'btn btn-primary';
  go.id = 'btn-unlock';
  go.textContent = 'Unlock';
  row.appendChild(go);
  card.appendChild(row);

  // The note keeps its height whether or not there is anything to say, so a
  // wrong code does not shove the card around.
  const note = document.createElement('p');
  note.className = 'note lock-note-line';
  note.id = 'lock-note';
  card.appendChild(note);

  shell.append(lockBrand(), card);
  document.body.replaceChildren(shell);
  input.focus();

  go.addEventListener('click', submitUnlock);
  input.addEventListener('keydown', (ev) => {
    if (ev.key === 'Enter') submitUnlock();
  });
}

async function submitUnlock() {
  const note = $('lock-note');
  const go = $('btn-unlock');
  const code = $('lock-entry').value;
  if (!code) return;
  go.disabled = true;
  note.className = 'note';
  note.textContent = '';
  try {
    const res = await fetch('/api/lock/unlock', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code: code }),
    });
    const data = await res.json().catch(() => ({}));
    if (res.status === 429) {
      const secs = data.retryIn || 30;
      note.className = 'note danger';
      note.textContent = `Too many attempts. Try again in ${secs} seconds.`;
      startWaitCountdown(secs, note);
      go.disabled = false;
      return;
    }
    if (!res.ok) {
      note.className = 'note danger';
      note.textContent = 'That code was not accepted.';
      go.disabled = false;
      return;
    }
    // Unlocked: the dashboard is rebuilt by a clean reload. The fragment is
    // deliberately left in place so the action the tray asked for survives the
    // trip through the code form.
    location.reload();
  } catch {
    note.className = 'note danger';
    note.textContent = 'Could not reach the server.';
    go.disabled = false;
  }
}

/** Count the lockout down in place so the wait is visible rather than guessed. */
function startWaitCountdown(seconds, note) {
  let left = seconds;
  const tick = () => {
    if (left <= 0) {
      note.className = 'note';
      note.textContent = '';
      const go = $('btn-unlock');
      if (go) go.disabled = false;
      return;
    }
    note.textContent = `Too many attempts. Try again in ${left} seconds.`;
    left -= 1;
    setTimeout(tick, 1000);
  };
  tick();
}

/**
 * Ask for confirmation before a privileged action runs.
 *
 * The fragment alone never acts: it only chooses this screen, and the action
 * is sent only when the owner presses the button below.
 */
function showConfirmScreen(action) {
  const labels = {
    newpin: 'Generate a new pairing PIN',
    toggle: servingLabelForAction(),
    quit: 'Exit Smart Remote',
  };
  const bodies = {
    newpin: 'Your phone will have to pair again with the new PIN.',
    toggle: servingBodyForAction(),
    quit: 'The server stops. Your phone loses the connection.',
  };

  const shell = document.createElement('div');
  shell.className = 'shell lock-note';
  const card = document.createElement('section');
  card.className = 'card lock-card';

  const title = document.createElement('h2');
  title.textContent = labels[action];
  card.appendChild(title);

  const line = document.createElement('p');
  line.className = 'hint';
  line.textContent = bodies[action];
  card.appendChild(line);

  const row = document.createElement('div');
  row.className = 'row lock-pair';
  const yes = document.createElement('button');
  yes.className = 'btn btn-primary';
  yes.textContent = 'Confirm';
  yes.id = 'btn-confirm';
  const no = document.createElement('button');
  no.className = 'btn';
  no.textContent = 'Cancel';
  no.id = 'btn-cancel';
  row.append(yes, no);
  card.appendChild(row);

  const note = document.createElement('p');
  note.className = 'note lock-note-line';
  note.id = 'lock-note';
  card.appendChild(note);

  shell.append(lockBrand(), card);
  document.body.replaceChildren(shell);

  no.addEventListener('click', () => { location.hash = ''; location.reload(); });
  yes.addEventListener('click', async () => {
    yes.disabled = true;
    try {
      const res = await fetch('/api/lock/action', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ do: action }),
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) {
        note.className = 'note danger';
        note.textContent = data.error || 'Could not do that.';
        yes.disabled = false;
        return;
      }
      if (action === 'quit') {
        note.className = 'note';
        note.textContent = 'Closing. You can close this tab.';
        return;
      }
      location.hash = '';
      location.reload();
    } catch {
      note.className = 'note danger';
      note.textContent = 'Could not reach the server.';
      yes.disabled = false;
    }
  });
}

// The toggle has to be named after what it will actually do, so the
// confirmation reads "Stop server" or "Activate server" correctly.
let lastServing = true;

function servingLabelForAction() {
  return lastServing ? 'Stop server' : 'Activate server';
}

function servingBodyForAction() {
  return lastServing
    ? 'The PC stops accepting phones. It stays installed and can be resumed.'
    : 'The PC starts accepting phones again on the same address.';
}

/**
 * Add the security-lock card to an unlocked dashboard.
 *
 * Everything is built with textContent, never innerHTML, so nothing the
 * server sends can become markup.
 */
function installLockUI(enabled) {
  const grid = document.querySelector('.grid');
  if (!grid) return;

  const card = document.createElement('section');
  card.className = 'card';
  card.id = 'card-lock';

  const title = document.createElement('h2');
  title.textContent = 'Security lock';
  card.appendChild(title);

  const line = document.createElement('p');
  line.className = 'hint';
  line.textContent = enabled
    ? 'On. This browser asks for the code before anything is shown.'
    : 'Off. Set a code to ask for it before this dashboard shows anything.';
  card.appendChild(line);

  const fields = document.createElement('div');
  fields.className = 'kv';

  const addField = (label, id) => {
    const wrap = document.createElement('div');
    const dt = document.createElement('dt');
    dt.textContent = label;
    const dd = document.createElement('dd');
    dd.appendChild(makeCodeInput(id));
    wrap.append(dt, dd);
    fields.appendChild(wrap);
    return dd.querySelector('input');
  };

  const current = addField(enabled ? 'Current code' : 'New code', 'lock-primary');
  const confirmInput = enabled ? null : addField('Confirm code', 'lock-repeat');
  card.appendChild(fields);

  const row = document.createElement('div');
  row.className = 'row';

  const note = document.createElement('p');
  note.className = 'note lock-note-line';
  note.id = 'lock-card-note';

  const report = (text, bad) => {
    note.className = bad ? 'note danger' : 'note';
    note.textContent = text;
  };

  const postJSON = async (path, payload) => {
    const res = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    const data = await res.json().catch(() => ({}));
    return { ok: res.ok, status: res.status, data };
  };

  if (enabled) {
    // addField already placed it in the list, so there is nothing to append.
    const newCode = addField('New code (leave empty to keep)', 'lock-swap');

    const change = document.createElement('button');
    change.className = 'btn';
    change.textContent = 'Change code';
    change.id = 'btn-change-code';
    change.addEventListener('click', async () => {
      const value = newCode.value;
      if (value && (value.length < 6 || value.length > 64)) {
        report('The code must be between 6 and 64 characters.', true);
        return;
      }
      const payload = { code: value, current: current.value };
      const r = await postJSON('/api/lock/set', payload);
      if (!r.ok) {
        report(r.data.error || 'Could not change the code.', true);
        return;
      }
      location.reload();
    });

    const disable = document.createElement('button');
    disable.className = 'btn btn-danger';
    disable.textContent = 'Disable lock';
    disable.id = 'btn-disable-lock';
    disable.addEventListener('click', async () => {
      const r = await postJSON('/api/lock/disable', { code: current.value });
      if (!r.ok) {
        report(r.data.error || 'Could not disable the lock.', true);
        return;
      }
      location.reload();
    });

    row.append(change, disable);
    card.appendChild(row);

    const lockNow = document.createElement('button');
    lockNow.className = 'btn';
    lockNow.textContent = 'Lock now';
    lockNow.id = 'btn-lock-now';
    lockNow.addEventListener('click', async () => {
      await postJSON('/api/lock/logout', {});
      showLockScreen();
    });
    row.appendChild(lockNow);
  } else {
    const enable = document.createElement('button');
    enable.className = 'btn btn-primary';
    enable.textContent = 'Enable lock';
    enable.id = 'btn-enable-lock';
    enable.addEventListener('click', async () => {
      const a = current.value;
      const b = confirmInput ? confirmInput.value : '';
      if (a.length < 6 || a.length > 64) {
        report('The code must be between 6 and 64 characters.', true);
        return;
      }
      if (a !== b) {
        report('The two codes do not match.', true);
        return;
      }
      const r = await postJSON('/api/lock/set', { code: a });
      if (!r.ok) {
        report(r.data.error || 'Could not enable the lock.', true);
        return;
      }
      location.reload();
    });
    row.appendChild(enable);
    card.appendChild(row);
  }

  card.appendChild(note);
  grid.appendChild(card);

  if (location.hash === '#lock') {
    card.scrollIntoView({ block: 'center' });
  }
}

/** Decide, before anything else runs, whether this tab may see the dashboard. */
async function bootstrapLock() {
  let state = null;
  try {
    state = await getJSON('/api/lock/state');
  } catch {
    // The state endpoint is on the always-open list, so a failure here means
    // the server is gone; the normal poller will say so in its own time.
    return;
  }
  const action = requestedAction();

  if (state.enabled && !state.unlocked) {
    showLockScreen();
    return;
  }
  if (action) {
    showConfirmScreen(action);
    return;
  }
  installLockUI(state.enabled);
}

function init() {
  $('btn-copy').addEventListener('click', copyPin);
  $('btn-regen').addEventListener('click', regeneratePin);

  // The lock decides the shape of the page, so it is settled first and
  // nothing else starts until it says which world we are in.
  bootstrapLock().then(() => {
    if (lockedNow) return;
    refresh();
    renderNet();
    connect();
    setInterval(refresh, POLL_MS);
    setInterval(renderNet, POLL_MS * 3);
  });
}

document.addEventListener('DOMContentLoaded', init);

// TalkTalk browser client: one WebRTC call, a transcript, a latency ledger.

const $ = (id) => document.getElementById(id);
const ui = {
  call: $("call"), callLabel: $("call-label"), notice: $("notice"),
  onair: $("onair"), onairLabel: $("onair-label"), timecode: $("timecode"),
  stage: document.querySelector(".stage"), word: $("state-word"), zh: $("state-zh"),
  turns: $("turns"), turnCount: $("turn-count"), ledger: $("ledger"),
  voice: $("voice"), micMeter: $("meter-mic"), voiceMeter: $("meter-voice"),
};

const STATES = {
  standby:   ["standby.", "待命"],
  listening: ["listening.", "在听"],
  thinking:  ["thinking.", "在想"],
  speaking:  ["speaking.", "在说"],
};
const ICE_WAIT_MS = 2500;
const TICKS = 64;

let call = null; // { pc, dc, id, mic, startedAt, timer, meters }

// ---------- state ----------

function setState(name) {
  const [word, zh] = STATES[name] ?? STATES.standby;
  ui.word.textContent = word;
  ui.zh.textContent = zh;
  ui.stage.dataset.state = name;
}

function setLive(live) {
  ui.onair.dataset.live = String(live);
  ui.onairLabel.textContent = live ? "ON AIR" : "OFF AIR";
  ui.call.dataset.live = String(live);
  ui.callLabel.textContent = live ? "End conversation · 挂断" : "Begin conversation · 开始对话";
  if (!live) setState("standby");
}

function notice(text) { ui.notice.textContent = text ?? ""; }

function fmtClock(ms) {
  const s = Math.floor(ms / 1000);
  const p = (n) => String(n).padStart(2, "0");
  return `${p(Math.floor(s / 3600))}:${p(Math.floor(s / 60) % 60)}:${p(s % 60)}`;
}

// ---------- transcript ----------

// The server owns turn boundaries: "turn" opens a block with exactly the
// words being answered, "retracted" withdraws it, and replies, backchannels
// and cuts attach to their turn id. Interim recognition only ever lives in
// the single "hearing" line at the bottom.
const transcript = {
  shown: 0,
  blocks: new Map(), // turn id → { li, reply: <p>|null }
  live: null,

  liveLine() {
    if (!this.live) {
      this.live = document.createElement("li");
      this.live.className = "live";
      this.live.innerHTML = `<div class="no">Hearing</div><p class="said"></p>`;
    }
    ui.turns.append(this.live); // keep it last
    return this.live;
  },

  hearing(text, final) {
    const li = this.liveLine();
    li.querySelector(".said").textContent = final ? "" : text;
    li.hidden = final || !text;
    this.scroll();
  },

  open(turn, userText) {
    $("empty-card")?.remove();
    this.shown += 1;
    const li = document.createElement("li");
    li.className = "turn";
    const tc = call ? fmtClock(performance.now() - call.startedAt) : "00:00:00";
    li.innerHTML = `<div class="no">No. ${String(this.shown).padStart(2, "0")}<span class="tc">${tc}</span></div>
      <div class="line user"><div class="who">You</div><p class="said"></p></div>`;
    li.querySelector(".said").textContent = userText;
    ui.turns.insertBefore(li, this.live);
    this.blocks.set(turn, { li, reply: null, no: this.shown });
    this.count();
    this.scroll();
  },

  retract(turn) {
    const b = this.blocks.get(turn);
    if (!b) return;
    b.li.remove();
    this.blocks.delete(turn);
    this.shown -= 1;
    this.count();
  },

  reply(turn, text) {
    const b = this.blocks.get(turn);
    if (!b) return;
    if (!b.reply) {
      const div = document.createElement("div");
      div.className = "line assistant";
      div.innerHTML = `<div class="who">TalkTalk</div><p class="said"></p>`;
      b.li.append(div);
      b.reply = div.querySelector(".said");
    }
    b.reply.append(document.createTextNode(text));
    this.scroll();
  },

  aside(turn, text) {
    const b = this.blocks.get(turn);
    if (!b) return;
    const p = document.createElement("p");
    p.className = "aside";
    p.textContent = `(${text.replace(/[。.]$/, "")})`;
    b.li.append(p);
    this.scroll();
  },

  cut(turn) {
    const b = this.blocks.get(turn);
    if (!b) return;
    const cut = document.createElement("span");
    cut.className = "cut";
    cut.textContent = "[ CUT ]";
    (b.reply ?? b.li).append(cut);
  },

  // Displayed number of a turn (server ids skip retracted turns).
  number(turn) { return this.blocks.get(turn)?.no ?? turn; },

  count() { ui.turnCount.textContent = `${this.shown} turn${this.shown === 1 ? "" : "s"}`; },
  scroll() { ui.turns.scrollTop = ui.turns.scrollHeight; },
};

// ---------- latency ledger ----------

function addMetrics(m) {
  const ms = (ns) => Math.max(0, Math.round(ns / 1e6));
  const end = ms(m.speech_end_to_final_ns), llm = ms(m.llm_first_token_ns),
        tts = ms(m.tts_first_audio_ns), out = ms(m.first_audio_out_ns),
        total = ms(m.total_ns) || end + out;
  ui.ledger.querySelector(".ledger-empty")?.remove();
  const scale = 3000; // ms spanned by a full-width bar
  const pct = (v) => `${Math.min(100, (v / scale) * 100).toFixed(1)}%`;
  const tr = document.createElement("tr");
  tr.innerHTML = `
    <td>${String(transcript.number(m.turn)).padStart(2, "0")}</td>
    <td>${end}</td><td>${llm}</td><td>${tts}</td><td>${out}</td>
    <td class="total">${total}</td>
    <td class="bar-cell"><div class="bar" title="endpoint ${end} · llm ${llm} · tts ${tts - llm}">
      <span class="s-end" style="width:${pct(end)}"></span>
      <span class="s-llm" style="width:${pct(llm)}"></span>
      <span class="s-tts" style="width:${pct(Math.max(0, out - llm))}"></span>
    </div></td>`;
  ui.ledger.prepend(tr);
}

// ---------- server events ----------

function onEvent(ev) {
  switch (ev.type) {
    case "state": setState(ev.state); break;
    case "user": transcript.hearing(ev.text, ev.final); break;
    case "turn": transcript.open(ev.turn, ev.text); break;
    case "retracted": transcript.retract(ev.turn); break;
    case "assistant": transcript.reply(ev.turn, ev.text); break;
    case "backchannel": transcript.aside(ev.turn, ev.text); break;
    case "interrupted": transcript.cut(ev.turn); break;
    case "metrics": addMetrics(ev.metrics); break;
    case "error": notice(`! ${ev.error}`); break;
  }
}

// ---------- level meters ----------

function buildTicks(el) {
  el.replaceChildren(...Array.from({ length: TICKS }, () => document.createElement("i")));
}

function meter(ctx, stream, el) {
  const src = ctx.createMediaStreamSource(stream);
  const an = ctx.createAnalyser();
  an.fftSize = 512;
  src.connect(an);
  const buf = new Float32Array(an.fftSize);
  const ticks = [...el.children];
  return () => {
    an.getFloatTimeDomainData(buf);
    let sum = 0;
    for (const v of buf) sum += v * v;
    const db = 10 * Math.log10(sum / buf.length + 1e-12);   // dBFS
    const level = Math.max(0, Math.min(1, (db + 60) / 50));  // -60..-10 dBFS
    const lit = Math.round(level * TICKS);
    el.dataset.active = String(lit > 1);
    ticks.forEach((t, i) => {
      const on = i < lit;
      t.style.height = on ? `${30 + 70 * (i / TICKS)}%` : "1px";
      t.className = on ? (i > TICKS * 0.85 ? "on peak" : "on") : "";
    });
  };
}

function startMeters(mic) {
  const ctx = new AudioContext();
  const fns = [meter(ctx, mic, ui.micMeter)];
  let raf = 0;
  const loop = () => { fns.forEach((f) => f()); raf = requestAnimationFrame(loop); };
  loop();
  return {
    addVoice(stream) { fns.push(meter(ctx, stream, ui.voiceMeter)); },
    stop() { cancelAnimationFrame(raf); ctx.close(); [ui.micMeter, ui.voiceMeter].forEach(buildTicks); },
  };
}

// ---------- call lifecycle ----------

function iceComplete(pc) {
  if (pc.iceGatheringState === "complete") return Promise.resolve();
  return new Promise((resolve) => {
    const done = () => { if (pc.iceGatheringState === "complete") resolve(); };
    pc.addEventListener("icegatheringstatechange", done);
    setTimeout(resolve, ICE_WAIT_MS); // host candidates are enough on LAN
  });
}

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  let env = null;
  try { env = await res.json(); } catch { /* non-JSON error page */ }
  if (!res.ok || !env?.success) throw new Error(env?.error ?? `HTTP ${res.status}`);
  return env.data;
}

async function begin() {
  ui.call.disabled = true;
  notice("");
  let mic;
  try {
    mic = await navigator.mediaDevices.getUserMedia({
      audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true, channelCount: 1 },
    });
  } catch (err) {
    notice("! 拿不到麦克风：" + (err.message || err.name));
    ui.call.disabled = false;
    return;
  }

  const pc = new RTCPeerConnection();
  const c = { pc, mic, id: null, startedAt: performance.now(), timer: 0, meters: startMeters(mic) };
  call = c;
  mic.getTracks().forEach((t) => pc.addTrack(t, mic));
  // The server answers on the same transceiver; its audio arrives here.
  pc.ontrack = (e) => {
    const stream = e.streams[0] ?? new MediaStream([e.track]);
    ui.voice.srcObject = stream;
    c.meters.addVoice(stream);
  };
  pc.onconnectionstatechange = () => {
    if (["failed", "closed", "disconnected"].includes(pc.connectionState) && call === c) {
      notice(pc.connectionState === "failed" ? "! 连接失败" : "");
      end();
    }
  };
  const dc = pc.createDataChannel("events");
  dc.onmessage = (m) => { try { onEvent(JSON.parse(m.data)); } catch (e) { console.error(e); } };

  // end() may run while we are still negotiating (hang-up, pagehide);
  // after every await, stop if this call is no longer the current one.
  const abandoned = () => call !== c;
  try {
    await pc.setLocalDescription(await pc.createOffer());
    await iceComplete(pc);
    if (abandoned()) return;
    const ans = await api("POST", "/api/sessions", { type: "offer", sdp: pc.localDescription.sdp });
    if (abandoned()) {
      api("DELETE", `/api/sessions/${ans.id}`).catch(() => {});
      return;
    }
    c.id = ans.id;
    await pc.setRemoteDescription({ type: ans.type, sdp: ans.sdp });
    if (abandoned()) return;
  } catch (err) {
    if (abandoned()) return;
    notice("! " + err.message);
    end();
    ui.call.disabled = false;
    return;
  } finally {
    if (abandoned()) ui.call.disabled = false;
  }

  setLive(true);
  setState("listening");
  c.timer = setInterval(() => { ui.timecode.textContent = fmtClock(performance.now() - c.startedAt); }, 250);
  ui.call.disabled = false;
}

function end() {
  const c = call;
  if (!c) return;
  call = null;
  clearInterval(c.timer);
  c.meters.stop();
  c.mic.getTracks().forEach((t) => t.stop());
  if (c.id) api("DELETE", `/api/sessions/${c.id}`).catch(() => {});
  c.pc.close();
  ui.voice.srcObject = null;
  transcript.hearing("", true);
  setLive(false);
}

ui.call.addEventListener("click", () => (call ? end() : begin()));
document.addEventListener("keydown", (e) => {
  if (e.code === "Space" && !e.repeat && e.target === document.body) {
    e.preventDefault();
    if (!ui.call.disabled) ui.call.click();
  }
});
window.addEventListener("pagehide", end);

[ui.micMeter, ui.voiceMeter].forEach(buildTicks);
setLive(false);

// Exposed for automated end-to-end checks.
window.__talktalk = { state: () => ui.stage.dataset.state, live: () => Boolean(call) };

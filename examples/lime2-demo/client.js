"use strict";
const $ = (id) => document.getElementById(id);
let ws,
  node = "",
  session = "",
  stream = null,
  aliasReady = false;
const active = new Map(),
  complete = new Map(),
  pending = new Map(),
  commands = new Map();
function log(direction, e) {
  const safe = { ...e };
  delete safe.authentication;
  const lines = $("wire").textContent.split("\n").filter(Boolean);
  lines.push(direction + " " + JSON.stringify(safe));
  $("wire").textContent = lines.slice(-100).join("\n");
  $("wire").scrollTop = $("wire").scrollHeight;
}
function status(text) {
  $("state").textContent = text;
}
function send(e) {
  if (ws?.readyState !== WebSocket.OPEN) throw Error("Connect first");
  const wire = { ...e };
  if (wire.to === "") delete wire.to;
  if (wire.thread === "") delete wire.thread;
  ws.send(JSON.stringify(wire));
  log("→", wire);
}
function key(e) {
  return (e.from || "") + "/" + e.id + "/" + (e.rev || 1);
}
function base(id) {
  return {
    id,
    to: stream?.to ?? $("recipient").value,
    thread: stream?.thread ?? $("thread").value,
  };
}
function controls(connected) {
  $("live").disabled = !connected;
  for (const id of [
    "disconnect",
    "peers",
    "send",
    "json",
    "alias",
    "retry",
    "read",
    "text",
  ])
    $(id).disabled = !connected;
  $("connect").disabled = connected;
}
function request(uri, method = "get", resource) {
  const id = crypto.randomUUID();
  commands.set(id, uri);
  const e = { id, method, uri };
  if (resource) {
    e.type = "json";
    e.resource = resource;
  }
  send(e);
}
function patch(target, p) {
  if (p === null || typeof p !== "object" || Array.isArray(p))
    return structuredClone(p);
  const out =
    target !== null && typeof target === "object" && !Array.isArray(target)
      ? structuredClone(target)
      : {};
  for (const [k, v] of Object.entries(p)) {
    if (v === null) delete out[k];
    else
      Object.defineProperty(out, k, {
        value: patch(out[k], v),
        writable: true,
        enumerable: true,
        configurable: true,
      });
  }
  return out;
}
function render(e, value, done) {
  const k = key(e);
  let el = e.id ? complete.get(k)?.element || active.get(k)?.element : null;
  if (!el) {
    el = document.createElement("article");
    $("messages").append(el);
  }
  el.replaceChildren();
  const label = document.createElement("small");
  label.textContent =
    (e.from || node) +
    " · " +
    (e.thread || "no thread") +
    " · " +
    (done ? "complete" : "streaming");
  const content = document.createElement("span");
  content.textContent =
    typeof value === "string" ? value : JSON.stringify(value, null, 2);
  el.append(label, content);
  el.className = done ? "" : "pending";
  return el;
}
function received(e, value, element) {
  const k = key(e);
  complete.set(e.id ? k : "idless/" + crypto.randomUUID(), {
    message: e,
    value,
    element,
  });
  if (complete.size > 256) {
    const first = complete.keys().next().value;
    complete.get(first).element.remove();
    complete.delete(first);
  }
  active.delete(k);
  if (e.id) send({ id: e.id, rev: e.rev, to: e.from, event: "received" });
}
function handle(e) {
  if (e.state) {
    if (e.state === "authenticating") {
      send({
        id: e.id,
        from: node,
        state: "authenticating",
        scheme: "plain",
        authentication: { password: btoa(window.loginToken) },
      });
      window.loginToken = "";
      return;
    }
    if (e.state === "established") {
      session = e.id;
      node = e.to;
      status("Established");
      $("identity").textContent = node + " · session " + session;
      controls(true);
      request("/peers");
      return;
    }
    if (e.state === "failed") status("Failed: " + e.reason?.description);
    if (e.state === "finished") status("Finished");
    ws.close();
    return;
  }
  if (e.method) {
    const uri = commands.get(e.id);
    commands.delete(e.id);
    if (e.status === "failure") {
      status("Command failed: " + e.reason?.description);
      return;
    }
    if (uri === "/peers") {
      const selected = $("recipient").value;
      $("recipient").replaceChildren(new Option("All other clients", ""));
      for (const name of e.resource) $("recipient").add(new Option(name, name));
      if ([...$("recipient").options].some((o) => o.value === selected))
        $("recipient").value = selected;
    }
    if (uri === "/protocol/aliases") {
      aliasReady = true;
      status("Alias note registered");
    }
    return;
  }
  if (e.event) {
    if (e.event === "received") {
      if (e.scope === "session") {
        const list = [...pending.keys()];
        const marker = list.findIndex((k) => k === e.id + "/" + (e.rev || 1));
        if (marker >= 0) {
          const prefix = list.slice(0, marker + 1);
          if (prefix.some((k) => pending.get(k) === null)) {
            status("Rejected receipt watermark with incomplete gap");
            return;
          }
          for (const k of prefix) pending.delete(k);
        }
      } else pending.delete(e.id + "/" + (e.rev || 1));
    }
    if (e.event === "received" && pending.size < 256)
      $("send").disabled = false;
    status(
      e.event + " " + e.id + (e.reason ? " · " + e.reason.description : ""),
    );
    return;
  }
  const k = key(e);
  if (e.stream === "start") {
    const item = {
      message: e,
      value:
        e.type === "text" || e.type === "text/plain" || e.type === "note"
          ? ""
          : {},
      element: render(e, "", false),
    };
    active.set(k, item);
    return;
  }
  if (e.stream === "data") {
    const item = active.get(k);
    if (!item) {
      status("Rejected data without start");
      return;
    }
    item.value =
      item.message.type === "text" ||
      item.message.type === "text/plain" ||
      item.message.type === "note"
        ? item.value + e.content
        : patch(item.value, e.content);
    item.element = render(item.message, item.value, false);
    return;
  }
  if (e.stream === "end") {
    const item = active.get(k);
    if (!item) {
      status("Rejected end without start");
      return;
    }
    const msg = { ...item.message, content: item.value };
    delete msg.stream;
    item.element = render(msg, item.value, true);
    received(msg, item.value, item.element);
    return;
  }
  if (e.id && complete.has(k)) {
    if (e.id) send({ id: e.id, rev: e.rev, to: e.from, event: "received" });
    return;
  }
  received(e, e.content, render(e, e.content, true));
}
$("connect").onclick = async () => {
  try {
    complete.clear();
    active.clear();
    $("messages").replaceChildren();
    const resp = await fetch("/identity", { method: "POST" });
    if (!resp.ok) throw Error(await resp.text());
    const auth = await resp.json();
    node = auth.node;
    window.loginToken = auth.token;
    ws = new WebSocket(
      (location.protocol === "https:" ? "wss://" : "ws://") +
        location.host +
        "/ws",
      "lime",
    );
    status("Connecting");
    $("connect").disabled = true;
    ws.onopen = () => send({ state: "new", version: 2 });
    ws.onmessage = (event) => {
      const e = JSON.parse(event.data);
      log("←", e);
      handle(e);
    };
    ws.onerror = () => status("Connection error");
    ws.onclose = () => {
      for (const item of active.values()) {
        item.element.className = "pending";
        const label = item.element.querySelector("small");
        if (label) label.textContent += " · interrupted";
      }
      active.clear();
      pending.clear();
      commands.clear();
      stream = null;
      aliasReady = false;
      window.loginToken = "";
      controls(false);
      if ($("state").textContent === "Established") status("Disconnected");
    };
  } catch (err) {
    status(err.message);
    controls(false);
  }
};
$("disconnect").onclick = () => {
  if (stream) finish();
  send({ id: session, state: "finishing" });
  controls(false);
};
$("peers").onclick = () => request("/peers");
$("alias").onclick = () =>
  request("/protocol/aliases", "set", { note: "text/plain" });
$("text").addEventListener("input", () => {
  if (!$("live").checked) return;
  const value = $("text").value;
  if (!stream) {
    if (!admit()) return;
    stream = {
      id: crypto.randomUUID(),
      sent: "",
      to: $("recipient").value,
      thread: $("thread").value,
    };
    pending.set(stream.id + "/1", null);
    send({
      ...base(stream.id),
      type: aliasReady ? "note" : "text",
      stream: "start",
    });
    $("live").disabled = true;
  }
  if (!value.startsWith(stream.sent)) {
    status("Finish this stream before editing earlier text");
    $("text").value = stream.sent;
    return;
  }
  const delta = value.slice(stream.sent.length);
  if (delta) {
    send({ id: stream.id, to: stream.to, stream: "data", content: delta });
    stream.sent = value;
  }
});
function finish() {
  if (stream) {
    send({ id: stream.id, to: stream.to, stream: "end" });
    pending.set(stream.id + "/1", {
      ...base(stream.id),
      type: aliasReady ? "note" : "text",
      content: stream.sent,
    });
    stream = null;
  } else {
    if (!admit()) return;
    const id = crypto.randomUUID();
    const e = {
      ...base(id),
      type: aliasReady ? "note" : "text",
      content: $("text").value,
    };
    send(e);
    pending.set(id + "/1", e);
  }
  $("text").value = "";
  $("live").disabled = false;
  if (pending.size >= 256) {
    status("Pending limit: reconnect or wait for receipts");
    $("send").disabled = true;
  }
}
$("send").onclick = finish;
$("json").onclick = () => {
  if (!admit()) return;
  const connection = ws;
  const id = crypto.randomUUID(),
    e = base(id);
  pending.set(id + "/1", null);
  send({ ...e, type: "json", stream: "start" });
  send({
    id,
    to: e.to,
    stream: "data",
    content: {
      text: "Choose a topic",
      options: ["Delivery", "Returns"],
      temporary: true,
    },
  });
  setTimeout(() => {
    if (ws !== connection || ws?.readyState !== WebSocket.OPEN) return;
    send({
      id,
      to: e.to,
      stream: "data",
      content: { options: ["Payments"], temporary: null },
    });
    send({ id, to: e.to, stream: "end" });
    pending.set(id + "/1", {
      ...e,
      type: "json",
      content: { text: "Choose a topic", options: ["Payments"] },
    });
  }, 750);
};
$("read").onclick = () => {
  for (const item of complete.values())
    if (item.message.id)
      send({
        id: item.message.id,
        rev: item.message.rev,
        to: item.message.from,
        thread: item.message.thread,
        event: "consumed",
      });
};
$("retry").onclick = () => {
  for (const e of pending.values()) if (e) send(e);
};

function admit() {
  if (pending.size < 256) return true;
  status("Pending limit: reconnect or wait for receipts");
  return false;
}

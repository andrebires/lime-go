import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import vm from "node:vm";
const patchSource = fs.readFileSync(new URL("./json-patch.js", import.meta.url), "utf8");
const source = fs.readFileSync(new URL("./client.js", import.meta.url), "utf8");
class Element {
  constructor() {
    this.textContent = "";
    this.value = "";
    this.disabled = false;
    this.checked = false;
    this.children = [];
    this.options = [];
    this.listeners = {};
    this.className = "";
  }
  append(...items) {
    for (const item of items) {
      item.parent = this;
      this.children.push(item);
    }
  }
  replaceChildren(...items) {
    this.children = [];
    this.options = [];
    this.append(...items);
    for (const item of items)
      if (item.tag === "option") this.options.push(item);
  }
  add(item) {
    this.options.push(item);
  }
  addEventListener(name, fn) {
    this.listeners[name] = fn;
  }
  querySelector() {
    return this.children[0];
  }
  remove() {
    if (this.parent)
      this.parent.children = this.parent.children.filter((e) => e !== this);
  }
}
function lab() {
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new Element());
    return elements.get(id);
  };
  get("thread").value = "demo";
  get("live").checked = true;
  get("autoReceipt").checked = true;
  get("receiptScope").value = "message";
  get("readScope").value = "message";
  let nextID = 0;
  const sockets = [],
    timers = [];
  class Socket {
    static OPEN = 1;
    constructor(url, protocol) {
      this.url = url;
      this.protocol = protocol;
      this.readyState = 0;
      this.sent = [];
      sockets.push(this);
    }
    open() {
      this.readyState = 1;
      this.onopen();
    }
    deliver(e) {
      this.onmessage({ data: JSON.stringify(e) });
    }
    send(s) {
      const e = JSON.parse(s);
      assert.notEqual(e.to, "");
      assert.notEqual(e.thread, "");
      this.sent.push(e);
    }
    close() {
      this.readyState = 3;
      this.onclose?.();
    }
  }
  const context = vm.createContext({
    document: {
      getElementById: get,
      createElement: (tag) => Object.assign(new Element(), { tag }),
    },
    window: {},
    crypto: { randomUUID: () => `id-${++nextID}` },
    location: { protocol: "http:", host: "localhost:8080" },
    WebSocket: Socket,
    Option: class extends Element {
      constructor(text, value) {
        super();
        this.textContent = text;
        this.value = value;
        this.tag = "option";
      }
    },
    fetch: async () => ({
      ok: true,
      json: async () => ({ node: "alice", token: "secret" }),
    }),
    structuredClone,
    btoa: (s) => Buffer.from(s).toString("base64"),
    setTimeout: (fn) => timers.push(fn),
  });
  vm.runInContext(patchSource, context, {filename: new URL("./json-patch.js", import.meta.url).pathname});
  vm.runInContext(source, context, {
    filename: new URL("./client.js", import.meta.url).pathname,
  });
  return {
    get,
    context,
    sockets,
    timers,
    async connect(fallback = true) {
      await get("connect").onclick();
      const socket = sockets.at(-1);
      socket.open();
      if (fallback)
        socket.deliver({
          id: "s",
          state: "authenticating",
          schemeOptions: ["plain"],
        });
      socket.deliver({ id: "s", state: "established", to: "alice" });
      return socket;
    },
  };
}
test("authentication, peers, aliases and plain text sends", async () => {
  const l = lab(),
    s = await l.connect();
  assert.equal(s.protocol, "lime");
  assert.equal(s.sent[0].version, 2);
  assert.equal(s.sent[1].authentication.password, "c2VjcmV0");
  assert.ok(!l.get("wire").textContent.includes("c2VjcmV0"));
  assert.equal(l.get("send").disabled, false);
  const peerRequest = s.sent.at(-1);
  s.deliver({
    id: peerRequest.id,
    method: "get",
    status: "success",
    resource: ["bob", "carol"],
  });
  assert.equal(l.get("recipient").options.length, 3);
  l.get("recipient").value = "bob";
  l.get("peers").onclick();
  s.deliver({
    id: s.sent.at(-1).id,
    method: "get",
    status: "success",
    resource: ["bob"],
  });
  assert.equal(l.get("recipient").value, "bob");
  l.get("alias").onclick();
  s.deliver({ id: s.sent.at(-1).id, method: "set", status: "success" });
  assert.match(l.get("state").textContent, /registered/);
  l.get("live").checked = false;
  l.get("text").value = "hello";
  l.get("send").onclick();
  assert.equal(s.sent.at(-1).type, "note");
  assert.equal(s.sent.at(-1).content, "hello");
  l.get("text").value = "";
  l.get("send").onclick();
  assert.equal(s.sent.at(-1).content, "");
  l.get("retry").onclick();
  assert.equal(s.sent.at(-1).stream, undefined);
  s.deliver({ id: "unknown", event: "received", scope: "session" });
  s.deliver({ id: s.sent.at(-1).id, event: "received", scope: "session" });
  l.get("alias").onclick();
  s.deliver({
    id: s.sent.at(-1).id,
    method: "set",
    status: "failure",
    reason: { description: "conflict" },
  });
  assert.match(l.get("state").textContent, /conflict/);
  s.deliver({
    id: "bad",
    event: "failed",
    reason: { description: "unsupported" },
  });
  assert.match(l.get("state").textContent, /unsupported/);
  l.get("disconnect").onclick();
  assert.equal(s.sent.at(-1).state, "finishing");
  s.deliver({ id: "s", state: "finished" });
  assert.equal(l.get("connect").disabled, false);
});
test("typing emits ordered start/data/end and freezes routing", async () => {
  const l = lab(),
    s = await l.connect(false);
  l.get("recipient").value = "bob";
  l.get("text").value = "Olá";
  l.get("text").listeners.input();
  assert.equal(s.sent.at(-2).stream, "start");
  assert.equal(s.sent.at(-1).content, "Olá");
  l.get("recipient").value = "carol";
  l.get("thread").value = "new";
  l.get("text").value = "Olá 😀";
  l.get("text").listeners.input();
  assert.equal(s.sent.at(-1).content, " 😀");
  assert.equal(s.sent.at(-1).to, "bob");
  l.get("text").value = "edited";
  l.get("text").listeners.input();
  assert.equal(l.get("text").value, "Olá 😀");
  assert.match(l.get("state").textContent, /editing/);
  const before = s.sent.length;
  l.get("text").listeners.input();
  assert.equal(s.sent.length, before);
  l.get("disconnect").onclick();
  assert.equal(s.sent.at(-2).stream, "end");
  assert.equal(s.sent.at(-2).to, "bob");
  assert.equal(l.get("live").disabled, true);
});
test("receiving streaming, JSON patches, receipts, read and deduplication", async () => {
  const l = lab(),
    s = await l.connect();
  s.deliver({ id: "orphan", from: "bob", stream: "data", content: "x" });
  assert.match(l.get("state").textContent, /without start/);
  s.deliver({ id: "orphan", from: "bob", stream: "end" });
  assert.match(l.get("state").textContent, /without start/);
  s.deliver({
    id: "text",
    from: "bob",
    thread: "t",
    type: "text",
    stream: "start",
  });
  s.deliver({ id: "text", from: "bob", stream: "data", content: "Hello " });
  s.deliver({ id: "text", from: "bob", stream: "data", content: "world" });
  assert.equal(s.sent.at(-1).event, undefined);
  s.deliver({ id: "text", from: "bob", stream: "end" });
  assert.equal(s.sent.at(-1).event, "received");
  assert.equal(
    l.get("messages").children.at(-1).children[1].textContent,
    "Hello world",
  );
  s.deliver({ id: "json", rev: 2, from: "bob", type: "json", stream: "start" });
  s.deliver({
    id: "json",
    rev: 2,
    from: "bob",
    stream: "data",
    content: [{op:"add",path:"/a",value:{b:1,c:2}},{op:"add",path:"/items",value:[1,2]}],
  });
  s.deliver({
    id: "json",
    rev: 2,
    from: "bob",
    stream: "data",
    content: [{op:"remove",path:"/a/b"},{op:"add",path:"/items/-",value:3}],
  });
  s.deliver({ id: "json", rev: 2, from: "bob", stream: "end" });
  const value = JSON.parse(
    l.get("messages").children.at(-1).children[1].textContent,
  );
  assert.deepEqual(value, { a: { c: 2 }, items: [1,2,3] });
  assert.equal(s.sent.at(-1).rev, 2);
  s.deliver({ id: "scalar", from: "bob", type: "json", stream: "start" });
  s.deliver({ id: "scalar", from: "bob", stream: "data", content: [{op:"replace",path:"",value:null}] });
  s.deliver({
    id: "scalar",
    from: "bob",
    stream: "data",
    content: [{op:"replace",path:"",value:{safe:true}}],
  });
  s.deliver({ id: "scalar", from: "bob", stream: "end" });
  s.deliver({ id: "whole", from: "bob", type: "text", content: "atomic" });
  const count = l.get("messages").children.length;
  s.deliver({ id: "whole", from: "bob", type: "text", content: "atomic" });
  assert.equal(l.get("messages").children.length, count);
  assert.equal(s.sent.at(-1).event, "received");
  s.deliver({ type: "text", content: "IDless" });
  l.get("read").onclick();
  assert.equal(s.sent.at(-1).event, "consumed");
  assert.ok(!s.sent.some((e) => e.event === "consumed" && !e.id));
  for (let i = 0; i < 258; i++)
    s.deliver({ id: `w${i}`, from: "bob", type: "text", content: "bounded" });
  assert.equal(l.get("messages").children.length, 256);
  s.deliver({ id: "partial", from: "bob", type: "text", stream: "start" });
  s.deliver({ id: "partial", from: "bob", stream: "data", content: "partial" });
  s.close();
  assert.match(
    l.get("messages").children.at(-1).children[0].textContent,
    /interrupted/,
  );
  assert.equal(l.get("connect").disabled, false);
});
test("JSON sender and reconnection/fetch failures", async () => {
  const l = lab(),
    s = await l.connect();
  l.get("json").onclick();
  assert.equal(s.sent.at(-2).stream, "start");
  assert.equal(s.sent.at(-1).stream, "data");
  const messageID = s.sent.at(-1).id;
  l.timers.shift()();
  assert.equal(s.sent.at(-1).stream, "end");
  l.get("retry").onclick();
  assert.equal(s.sent.at(-1).uri, "/messages/delivery");
  assert.equal(s.sent.at(-1).method, "set");
  assert.deepEqual(s.sent.at(-1).resource, { id: messageID, rev: 1 });
  s.deliver({
    id: s.sent.at(-1).id,
    method: "set",
    status: "success",
    resource: { pending: [] },
  });
  l.get("json").onclick();
  s.close();
  l.timers.shift()();
  assert.equal(l.get("connect").disabled, false);
  l.context.fetch = async () => ({
    ok: false,
    text: async () => "capability limit",
  });
  await l.get("connect").onclick();
  assert.match(l.get("state").textContent, /capability limit/);
  l.context.fetch = async () => ({
    ok: true,
    json: async () => ({ node: "alice", token: "secret" }),
  });
  l.context.location.protocol = "https:";
  const next = await l.connect();
  assert.match(next.url, /^wss:/);
  next.onerror();
  assert.match(l.get("state").textContent, /error/);
  next.deliver({ state: "failed", reason: { description: "denied" } });
  assert.match(l.get("state").textContent, /denied/);
});
test("pending messages remain bounded", async () => {
  const l = lab(),
    s = await l.connect();
  l.get("live").checked = false;
  for (let i = 0; i < 257; i++) {
    l.get("text").value = "x";
    l.get("send").onclick();
  }
  assert.equal(l.get("send").disabled, true);
  assert.match(l.get("state").textContent, /Pending limit/);
  s.close();
  assert.equal(l.get("retry").disabled, true);
});

test("JSON scalar patches, ID-less messages and streamed display bounds", async () => {
  const l = lab(),
    s = await l.connect();
  s.deliver({ id: "replace", from: "bob", type: "json", stream: "start" });
  s.deliver({
    id: "replace",
    from: "bob",
    stream: "data",
    content: [{op:"replace",path:"",value:"initial scalar"}],
  });
  s.deliver({
    id: "replace",
    from: "bob",
    stream: "data",
    content: [{op:"replace",path:"",value:{nested:true}}],
  });
  s.deliver({ id: "replace", from: "bob", stream: "data", content: [{op:"replace",path:"",value:[1,2]}] });
  s.deliver({ id: "replace", from: "bob", stream: "end" });
  assert.deepEqual(
    JSON.parse(l.get("messages").children.at(-1).children[1].textContent),
    [1, 2],
  );
  s.deliver({ type: "text", content: "first IDless" });
  s.deliver({ type: "text", content: "second IDless" });
  assert.equal(l.get("messages").children.length, 3);
  for (let i = 0; i < 257; i++) {
    s.deliver({ id: "stream" + i, from: "bob", type: "text", stream: "start" });
    s.deliver({ id: "stream" + i, from: "bob", stream: "end" });
  }
  assert.equal(l.get("messages").children.length, 256);
  s.close();
  const next = await l.connect();
  assert.equal(l.get("messages").children.length, 0);
  l.get("read").onclick();
  assert.equal(next.sent.at(-1).event, undefined);
});
test("incomplete streams reserve retry slots and never resume on new connections", async () => {
  const l = lab(),
    s = await l.connect();
  l.get("json").onclick();
  const id = s.sent.at(-1).id;
  l.get("retry").onclick();
  assert.equal(s.sent.at(-1).stream, "data");
  s.deliver({ id, event: "received", scope: "session" });
  assert.match(l.get("state").textContent, /incomplete gap/);
  s.close();
  const next = await l.connect();
  const before = next.sent.length;
  l.timers.shift()();
  assert.equal(next.sent.length, before);
  for (let i = 0; i < 256; i++) l.get("json").onclick();
  const count = next.sent.length;
  l.get("json").onclick();
  assert.equal(next.sent.length, count);
  l.get("text").value = "blocked";
  l.get("text").listeners.input();
  assert.equal(next.sent.length, count);
});

test("manual session receipts preserve wire order, revisions and all origins", async () => {
  const l = lab(),
    s = await l.connect();
  l.get("autoReceipt").checked = false;
  l.get("receiptScope").value = "session";
  const before = s.sent.length;
  s.deliver({
    id: "first",
    rev: 2,
    from: "bob",
    thread: "one",
    type: "text",
    content: "first",
  });
  s.deliver({
    id: "gap",
    from: "bob",
    thread: "one",
    type: "text",
    stream: "start",
  });
  s.deliver({
    id: "carol",
    rev: 3,
    from: "carol",
    thread: "two",
    type: "text",
    content: "other sender",
  });
  s.deliver({
    id: "last",
    from: "bob",
    thread: "two",
    type: "text",
    content: "last",
  });
  s.deliver({ from: "bob", type: "text", content: "ID-less" });
  assert.equal(s.sent.length, before);
  l.get("receipt").onclick();
  assert.deepEqual(s.sent.slice(before), [
    { id: "first", rev: 2, to: "bob", event: "received", scope: "session" },
  ]);
  assert.match(l.get("state").textContent, /incomplete/);
  s.deliver({ id: "gap", from: "bob", stream: "end" });
  const start = s.sent.length;
  l.get("receipt").onclick();
  assert.deepEqual(s.sent.slice(start), [
    { id: "carol", rev: 3, to: "carol", event: "received", scope: "session" },
    { id: "last", to: "bob", event: "received", scope: "session" },
  ]);
  l.get("autoReceipt").checked = true;
  s.deliver({
    id: "last",
    from: "bob",
    thread: "two",
    type: "text",
    content: "last",
  });
  assert.equal(s.sent.at(-1).scope, "session");
  s.close();
  const next = await l.connect();
  const count = next.sent.length;
  l.get("receipt").onclick();
  assert.equal(next.sent.length, count);
});

test("automatic session receipts wait behind a streaming gap", async () => {
  const l = lab(),
    s = await l.connect();
  l.get("receiptScope").value = "session";
  const before = s.sent.length;
  s.deliver({ id: "gap", from: "bob", type: "text", stream: "start" });
  s.deliver({ id: "later", from: "bob", type: "text", content: "later" });
  assert.equal(s.sent.length, before);
  s.deliver({ id: "gap", from: "bob", stream: "end" });
  assert.deepEqual(s.sent.at(-1), {
    id: "later",
    to: "bob",
    event: "received",
    scope: "session",
  });
});

test("thread read prefixes isolate threads and stop at delivery gaps", async () => {
  const l = lab(),
    s = await l.connect();
  l.get("readScope").value = "thread";
  l.get("thread").value = "selected";
  s.deliver({
    id: "first",
    rev: 2,
    from: "bob",
    thread: "selected",
    type: "text",
    content: "first",
  });
  s.deliver({
    id: "other-gap",
    from: "bob",
    thread: "other",
    type: "text",
    stream: "start",
  });
  s.deliver({
    id: "gap",
    from: "bob",
    thread: "selected",
    type: "text",
    stream: "start",
  });
  s.deliver({
    id: "carol",
    from: "carol",
    thread: "selected",
    type: "text",
    content: "carol",
  });
  s.deliver({
    id: "last",
    rev: 4,
    from: "bob",
    thread: "selected",
    type: "text",
    content: "last",
  });
  s.deliver({
    id: "no-thread",
    from: "carol",
    type: "text",
    content: "no thread",
  });
  const before = s.sent.length;
  l.get("read").onclick();
  assert.deepEqual(s.sent.slice(before), [
    {
      id: "first",
      rev: 2,
      to: "bob",
      event: "consumed",
      scope: "thread",
      thread: "selected",
    },
  ]);
  assert.match(l.get("state").textContent, /incomplete/);
  s.deliver({ id: "gap", from: "bob", stream: "end" });
  const start = s.sent.length;
  l.get("read").onclick();
  assert.deepEqual(s.sent.slice(start), [
    {
      id: "carol",
      to: "carol",
      event: "consumed",
      scope: "thread",
      thread: "selected",
    },
    {
      id: "last",
      rev: 4,
      to: "bob",
      event: "consumed",
      scope: "thread",
      thread: "selected",
    },
  ]);
  l.get("thread").value = "";
  const count = s.sent.length;
  l.get("read").onclick();
  assert.equal(s.sent.length, count);
  assert.match(l.get("state").textContent, /Choose a Thread/);
  l.get("thread").value = "missing";
  l.get("read").onclick();
  assert.equal(s.sent.length, count);
  l.get("readScope").value = "message";
  l.get("read").onclick();
  assert.ok(
    s.sent.slice(count).every((e) => e.event === "consumed" && !e.scope),
  );
});

test("session receipts clear only the acknowledging recipient's delivery prefix", async () => {
  const l = lab(),
    s = await l.connect();
  l.get("live").checked = false;
  function sendTo(to) {
    l.get("recipient").value = to;
    l.get("text").value = to;
    l.get("send").onclick();
    return s.sent.at(-1).id;
  }
  const first = sendTo("bob"),
    other = sendTo("carol"),
    marker = sendTo("bob");
  s.deliver({ id: marker, from: "carol", event: "received", scope: "session" });
  const beforeWrong = s.sent.length;
  l.get("retry").onclick();
  assert.deepEqual(
    s.sent.slice(beforeWrong).map((e) => e.resource.id),
    [first, other, marker],
  );
  s.deliver({ id: marker, from: "bob", event: "received" });
  s.deliver({ id: marker, from: "bob", event: "received", scope: "session" });
  assert.match(l.get("state").textContent, /received · session/);
  const before = s.sent.length;
  l.get("retry").onclick();
  assert.deepEqual(
    s.sent.slice(before).map((e) => e.resource.id),
    [other],
  );
  // A gap to another recipient cannot block Bob's session prefix.
  l.get("recipient").value = "carol";
  l.get("json").onclick();
  const later = sendTo("bob");
  s.deliver({ id: later, from: "bob", event: "received", scope: "session" });
  assert.ok(!l.get("state").textContent.includes("Rejected"));
  const failed = sendTo("bob"),
    tail = sendTo("bob");
  s.deliver({
    id: failed,
    from: "bob",
    event: "failed",
    reason: { description: "failed delivery" },
  });
  s.deliver({ id: tail, from: "bob", event: "received", scope: "session" });
  assert.match(l.get("state").textContent, /failed delivery/);
  s.deliver({
    id: "unknown",
    event: "failed",
    reason: { description: "unknown" },
  });
  assert.match(l.get("state").textContent, /unknown/);
});

test("manual message receipts and notification histories remain bounded", async () => {
  const l = lab(),
    s = await l.connect();
  l.get("autoReceipt").checked = false;
  s.deliver({ id: "m", from: "bob", type: "text", content: "m" });
  s.deliver({ id: "m", from: "bob", type: "text", content: "m" });
  const before = s.sent.length;
  l.get("receipt").onclick();
  assert.deepEqual(s.sent.slice(before), [
    { id: "m", to: "bob", event: "received" },
  ]);
  l.get("autoReceipt").checked = true;
  l.get("live").checked = false;
  l.get("recipient").value = "bob";
  for (let i = 0; i < 258; i++) {
    l.get("send").onclick();
    s.deliver({ id: s.sent.at(-1).id, event: "received" });
  }
  assert.equal(l.get("send").disabled, false);
  l.get("autoReceipt").checked = false;
  for (let i = 0; i < 258; i++) {
    if (s.readyState !== 1) break;
    s.deliver({
      id: "manual" + i,
      from: "bob",
      type: "text",
      content: "manual",
    });
  }
  assert.equal(s.readyState, 3);
  assert.match(l.get("state").textContent, /Delivery history limit/);
});

test("broadcast payloads remain pending until all original recipients acknowledge", async () => {
  const l = lab(),
    s = await l.connect();
  l.get("live").checked = false;
  l.get("text").value = "broadcast";
  l.get("send").onclick();
  const messageID = s.sent.at(-1).id;
  s.deliver({ id: messageID, from: "bob", event: "received" });
  const query = s.sent.at(-1);
  assert.equal(query.method, "get");
  assert.deepEqual(query.resource, { id: messageID, rev: 1 });
  s.deliver({
    id: query.id,
    method: "get",
    status: "success",
    resource: { pending: ["carol"] },
  });
  assert.match(l.get("state").textContent, /Waiting for 1/);
  // Changes to the selector cannot change the original broadcast's recipients.
  l.get("recipient").value = "dave";
  l.get("retry").onclick();
  const retry = s.sent.at(-1);
  assert.equal(retry.method, "set");
  assert.equal(retry.uri, "/messages/delivery");
  assert.deepEqual(retry.resource, { id: messageID, rev: 1 });
  s.deliver({ id: messageID, from: "carol", event: "received" });
  // A stale status reply cannot resurrect a received payload.
  s.deliver({
    id: retry.id,
    method: "set",
    status: "success",
    resource: { pending: ["carol"] },
  });
  assert.match(l.get("state").textContent, /All original recipients/);
  const before = s.sent.length;
  l.get("retry").onclick();
  assert.equal(s.sent.length, before);
});

test("broadcast cumulative receipts retain other peers and merge receipt/status races", async () => {
  const l = lab(),
    s = await l.connect();
  l.get("live").checked = false;
  l.get("send").onclick();
  const first = s.sent.at(-1).id;
  l.get("recipient").value = "bob";
  l.get("send").onclick();
  const marker = s.sent.at(-1).id;
  s.deliver({ id: marker, from: "bob", event: "received", scope: "session" });
  const query = s.sent.at(-1);
  assert.equal(query.resource.id, first);
  const count = s.sent.length;
  s.deliver({ id: first, from: "carol", event: "received" });
  assert.equal(s.sent.length, count); // One in-flight query per revision.
  s.deliver({
    id: query.id,
    method: "get",
    status: "success",
    resource: { pending: ["carol"] },
  });
  l.get("retry").onclick();
  assert.equal(s.sent.length, count);
  // Failed status queries leave the broadcast retry payload intact.
  l.get("recipient").value = "";
  l.get("send").onclick();
  const next = s.sent.at(-1).id;
  s.deliver({ id: next, from: "bob", event: "received" });
  s.deliver({
    id: s.sent.at(-1).id,
    method: "get",
    status: "failure",
    reason: { description: "unavailable" },
  });
  assert.match(l.get("state").textContent, /unavailable/);
  l.get("retry").onclick();
  assert.equal(s.sent.at(-1).resource.id, next);
});

test("confirmed delivery completion remains terminal across reordered status replies", async () => {
  const l = lab(),
    s = await l.connect();
  l.get("live").checked = false;
  l.get("send").onclick();
  l.get("retry").onclick();
  const older = s.sent.at(-1);
  l.get("retry").onclick();
  const newer = s.sent.at(-1);
  s.deliver({
    id: newer.id,
    method: "set",
    status: "success",
    resource: { pending: [] },
  });
  s.deliver({
    id: older.id,
    method: "set",
    status: "success",
    resource: { pending: ["carol"] },
  });
  assert.match(l.get("state").textContent, /All original recipients/);
  const count = s.sent.length;
  l.get("retry").onclick();
  assert.equal(s.sent.length, count);
});


test("browser patch engine matches shared vectors and rejects incomplete batches", async () => {
  const vectors=JSON.parse(fs.readFileSync(new URL("../../testdata/json-patch-vectors.json",import.meta.url),"utf8"));
  const l=lab();
  assert.throws(()=>vm.runInContext("LimeJsonPatch.copyJsonValue(Array(1),64)",l.context),/JSON value/);
  for(const v of vectors){
    const run=()=>vm.runInContext(`(()=>{
      const d=new LimeJsonPatch.default(LimeJsonPatch.copyJsonValue,64,1048576);
      d.apply([{op:"add",path:"",value:${JSON.stringify(v.target)}}],256,1048576);
      d.apply(${JSON.stringify(v.patch)},256,1048576);
      if(d.value===undefined)throw new Error("Missing root");
      return JSON.stringify(d.value);
    })()`,l.context);
    if(v.error)assert.throws(run,v.name);else assert.deepEqual(JSON.parse(run()),v.expected,v.name);
  }
  const s=await l.connect();
  for(const [id,patch] of [["bad",[{op:"add",path:"/x",value:1},{op:"remove",path:"/absent"}]],["root",[{op:"remove",path:""}]]]){
    s.deliver({id,from:"bob",type:"json",stream:"start"});s.deliver({id,from:"bob",stream:"data",content:patch});s.deliver({id,from:"bob",stream:"end"});
    assert.ok(!s.sent.some(e=>e.event==="received"&&e.id===id));
  }
  s.deliver({id:"large",from:"bob",type:"json",stream:"start"});
  s.deliver({id:"large",from:"bob",stream:"data",content:[{op:"add",path:"/x",value:"x".repeat(1048576)}]});
  assert.match(l.get("state").textContent,/Rejected JSON patch/);
  s.close();
});

test("browser JSON work budgets reset per contribution and bound copy work independently", async () => {
  const l=lab();const s=await l.connect();
  const start=id=>s.deliver({id,from:"bob",type:"json",stream:"start"});
  const data=(id,content)=>s.deliver({id,from:"bob",stream:"data",content});
  const end=id=>s.deliver({id,from:"bob",stream:"end"});
  start("replace");
  data("replace",[{op:"add",path:"/x",value:"x".repeat(600000)}]);
  for(let i=0;i<3;i++)data("replace",[{op:"replace",path:"/x",value:"y".repeat(600000)}]);
  end("replace");
  assert.ok(s.sent.some(e=>e.event==="received"&&e.id==="replace"));
  start("independent");
  data("independent",[{op:"add",path:"/x",value:"x".repeat(300000)},
    ...Array.from({length:3},()=>[{op:"copy",from:"/x",path:"/y"},{op:"remove",path:"/y"}]).flat()]);
  end("independent");
  assert.ok(s.sent.some(e=>e.event==="received"&&e.id==="independent"));
  start("copy-work");data("copy-work",[{op:"add",path:"/x",value:"x".repeat(600000)}]);
  data("copy-work",Array.from({length:2},()=>[{op:"copy",from:"/x",path:"/x"}]).flat());
  assert.match(l.get("state").textContent,/copy work limit/);
  end("copy-work");
  assert.ok(!s.sent.some(e=>e.event==="received"&&e.id==="copy-work"));s.close();
});

// Generated from lime-js JsonPatch.ts; see json-patch-source.md for provenance.
var LimeJsonPatch = (() => {
  var __defProp = Object.defineProperty;
  var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
  var __getOwnPropNames = Object.getOwnPropertyNames;
  var __hasOwnProp = Object.prototype.hasOwnProperty;
  var __export = (target, all) => {
    for (var name in all)
      __defProp(target, name, { get: all[name], enumerable: true });
  };
  var __copyProps = (to, from, except, desc) => {
    if (from && typeof from === "object" || typeof from === "function") {
      for (let key of __getOwnPropNames(from))
        if (!__hasOwnProp.call(to, key) && key !== except)
          __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
    }
    return to;
  };
  var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: true }), mod);

  // src/Lime/Protocol/JsonPatch.ts
  var JsonPatch_exports = {};
  __export(JsonPatch_exports, {
    copyJsonValue: () => copyJsonValue,
    default: () => JsonPatchDocument,
    jsonBytes: () => jsonBytes
  });

  // src/Lime/Protocol/Validation.ts
  var has = (value, key) => Object.prototype.hasOwnProperty.call(value, key);

  // src/Lime/Protocol/JsonPatch.ts
  var object = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
  function set(target, key, value) {
    Object.defineProperty(target, key, { value, writable: true, enumerable: true, configurable: true });
  }
  function copyJsonValue(value, depth) {
    if (depth < 0) throw new Error("JSON nesting limit exceeded");
    if (value === null || typeof value === "string" || typeof value === "boolean") return value;
    if (typeof value === "number" && Number.isFinite(value)) return value;
    if (Array.isArray(value)) return value.map((item) => copyJsonValue(item, depth - 1));
    if (!object(value) || Object.getPrototypeOf(value) !== Object.prototype && Object.getPrototypeOf(value) !== null) throw new Error("Content must be a JSON value");
    const result = {};
    for (const key of Object.keys(value)) set(result, key, copyJsonValue(value[key], depth - 1));
    return result;
  }
  function jsonBytes(value) {
    const encoded = JSON.stringify(value);
    let bytes = encoded.length;
    for (let i = 0; i < encoded.length; i++) {
      const code = encoded.charCodeAt(i);
      if (code >= 128 && code < 2048) bytes++;
      else if (code >= 55296 && code <= 56319 && i + 1 < encoded.length && encoded.charCodeAt(i + 1) >= 56320 && encoded.charCodeAt(i + 1) <= 57343) {
        bytes += 2;
        i++;
      } else if (code >= 2048) bytes += 2;
    }
    return bytes;
  }
  var container = (value) => value !== null && typeof value === "object";
  function pointer(value) {
    if (typeof value !== "string" || value !== "" && !value.startsWith("/") || /~(?![01])/.test(value)) throw new Error("Invalid JSON Pointer");
    return value === "" ? [] : value.slice(1).split("/").map((token) => token.replace(/~1/g, "/").replace(/~0/g, "~"));
  }
  function index(key, length, add) {
    if (add && key === "-") return length;
    if (!/^(0|[1-9][0-9]*)$/.test(key)) throw new Error("Invalid array index");
    const result = Number(key);
    if (!Number.isSafeInteger(result) || result >= length + Number(add)) throw new Error("Array index out of bounds");
    return result;
  }
  function equal(a, b) {
    if (a === b) return true;
    if (!container(a) || !container(b) || Array.isArray(a) !== Array.isArray(b)) return false;
    const keys = Object.keys(a);
    return keys.length === Object.keys(b).length && keys.every((key) => has(b, key) && equal(a[key], b[key]));
  }
  var JsonPatchDocument = class {
    constructor(clone, maxDepth, maxBytes) {
      this.clone = clone;
      this.maxDepth = maxDepth;
      this.maxBytes = maxBytes;
      this.value = {};
      this.sizes = /* @__PURE__ */ new WeakMap();
    }
    size(value) {
      if (value === void 0) return 0;
      if (!container(value)) return jsonBytes(value);
      const cached = this.sizes.get(value);
      if (cached !== void 0) return cached;
      const keys = Object.keys(value);
      let size = 2 + Math.max(0, keys.length - 1);
      for (const key of keys) size += this.size(value[key]) + (Array.isArray(value) ? 0 : jsonBytes(key) + 1);
      this.sizes.set(value, size);
      return size;
    }
    location(path) {
      let parent = this.value;
      const ancestors = [];
      for (let i = 0; i < path.length; i++) {
        if (!container(parent)) throw new Error("JSON Pointer parent does not exist");
        ancestors.push(parent);
        if (i === path.length - 1) return { parent, key: path[i], ancestors };
        const key = Array.isArray(parent) ? index(path[i], parent.length, false) : path[i];
        if (!has(parent, String(key))) throw new Error("JSON Pointer parent does not exist");
        parent = parent[key];
      }
      throw new Error("JSON Pointer has no parent");
    }
    get(path) {
      if (!path.length) {
        if (this.value === void 0) throw new Error("JSON Pointer target does not exist");
        return this.value;
      }
      const { parent, key } = this.location(path);
      const target = Array.isArray(parent) ? index(key, parent.length, false) : key;
      if (!has(parent, String(target))) throw new Error("JSON Pointer target does not exist");
      return parent[target];
    }
    write(path, op, value) {
      if (!path.length) {
        if (op !== "add") this.get(path);
        this.value = op === "remove" ? void 0 : value;
        if (this.size(this.value) > this.maxBytes) throw new Error("Assembled JSON limit exceeded");
        return;
      }
      const { parent, key, ancestors } = this.location(path);
      const array = Array.isArray(parent);
      const target = array ? index(key, parent.length, op === "add") : key;
      const exists = has(parent, String(target));
      if (op !== "add" && !exists) throw new Error("JSON Pointer target does not exist");
      const rootSize = this.size(this.value);
      let delta;
      if (array) {
        if (op === "add") delta = this.size(value) + Number(parent.length > 0);
        else if (op === "remove") delta = -this.size(parent[target]) - Number(parent.length > 1);
        else delta = this.size(value) - this.size(parent[target]);
      } else {
        if (op === "remove") delta = -this.size(parent[target]) - jsonBytes(key) - 1 - Number(Object.keys(parent).length > 1);
        else delta = this.size(value) - (exists ? this.size(parent[target]) : 0) + (exists ? 0 : jsonBytes(key) + 1 + Number(Object.keys(parent).length > 0));
      }
      if (rootSize + delta > this.maxBytes) throw new Error("Assembled JSON limit exceeded");
      if (array) {
        if (op === "add") parent.splice(target, 0, value);
        else if (op === "remove") parent.splice(target, 1);
        else parent[target] = value;
      } else if (op === "remove") delete parent[target];
      else Object.defineProperty(parent, target, { value, writable: true, enumerable: true, configurable: true });
      for (const ancestor of ancestors) this.sizes.set(ancestor, this.sizes.get(ancestor) + delta);
    }
    apply(patch, maxOperations, copyBudget) {
      if (!Array.isArray(patch)) throw new Error("JSON Patch must be an operation array");
      if (patch.length > maxOperations) throw new Error("JSON Patch operation limit exceeded");
      let copied = 0;
      for (const operation of patch) {
        if (!container(operation) || Array.isArray(operation) || !has(operation, "op") || !has(operation, "path")) throw new Error("Invalid JSON Patch operation");
        const { op } = operation;
        const path = pointer(operation.path);
        if (op === "add" || op === "replace" || op === "test") {
          if (!has(operation, "value")) throw new Error("JSON Patch operation requires value");
          if (op === "test") {
            if (!equal(this.get(path), operation.value)) throw new Error("JSON Patch test failed");
          } else this.write(path, op, this.clone(operation.value, this.maxDepth - path.length));
        } else if (op === "remove") this.write(path, op);
        else if (op === "copy" || op === "move") {
          if (!has(operation, "from")) throw new Error("JSON Patch operation requires from");
          const from = pointer(operation.from);
          const source = this.get(from);
          if (op === "move" && from.length < path.length && from.every((token, i) => token === path[i])) throw new Error("Cannot move a value into its descendant");
          copied += this.size(source);
          if (copied > copyBudget) throw new Error("JSON Patch copy work limit exceeded");
          const value = this.clone(source, this.maxDepth - path.length);
          if (op === "move") this.write(from, "remove");
          this.write(path, "add", value);
        } else throw new Error("Unknown JSON Patch operation");
      }
      return copied;
    }
  };
  return __toCommonJS(JsonPatch_exports);
})();

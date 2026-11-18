import reportShape from "./runnerUnitReportShape.json";

// Mirrors runnerunit.validateSourceReport's canonical-writer comparison:
// encoding/json decodes the retained report into its typed DTOs and re-encodes
// it; the result must equal the compacted retained bytes. Field order, key
// sets, omitempty and numeric kinds come from a shape generated from the Go
// types (internal/runnerunit/report_shape_test.go). Types with custom JSON
// codecs are "opaque": their presence makes the browser result unverified.

export type ReportCanonicalCheck = "absent" | "canonical" | "unverified";

type Node =
  | { kind: "object"; members: [string, Node][]; start: number; end: number }
  | { kind: "array"; items: Node[]; start: number; end: number }
  | { kind: "string"; value: string; start: number; end: number }
  | { kind: "number"; token: string; start: number; end: number }
  | { kind: "literal"; token: "true" | "false" | "null"; start: number; end: number };

interface ShapeField { name: string; type: string; omitempty?: boolean }
const structs = (reportShape as { structs: Record<string, { fields: ShapeField[] | null }> }).structs;

class NotCanonical extends Error {}

function parseNodes(text: string): Node {
  let offset = 0;
  const space = () => { while (/[ \t\r\n]/.test(text[offset] ?? "")) offset++; };
  const fail = (): never => { throw new NotCanonical(); };
  const stringNode = (): Extract<Node, { kind: "string" }> => {
    const start = offset++;
    while (offset < text.length && text[offset] !== '"') offset += text[offset] === "\\" ? 2 : 1;
    if (offset >= text.length) fail();
    offset++;
    return { kind: "string", value: JSON.parse(text.slice(start, offset)) as string, start, end: offset };
  };
  const value = (): Node => {
    space();
    const start = offset;
    const character = text[offset];
    if (character === "{") {
      offset++;
      const members: [string, Node][] = [];
      space();
      if (text[offset] === "}") { offset++; return { kind: "object", members, start, end: offset }; }
      for (;;) {
        space();
        if (text[offset] !== '"') fail();
        const key = stringNode().value;
        space();
        if (text[offset++] !== ":") fail();
        members.push([key, value()]);
        space();
        const delimiter = text[offset++];
        if (delimiter === "}") return { kind: "object", members, start, end: offset };
        if (delimiter !== ",") fail();
      }
    }
    if (character === "[") {
      offset++;
      const items: Node[] = [];
      space();
      if (text[offset] === "]") { offset++; return { kind: "array", items, start, end: offset }; }
      for (;;) {
        items.push(value());
        space();
        const delimiter = text[offset++];
        if (delimiter === "]") return { kind: "array", items, start, end: offset };
        if (delimiter !== ",") fail();
      }
    }
    if (character === '"') return stringNode();
    while (offset < text.length && !/[ \t\r\n,\]}]/.test(text[offset]!)) offset++;
    const token = text.slice(start, offset);
    if (token === "true" || token === "false" || token === "null") return { kind: "literal", token, start, end: offset };
    if (!/^-?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?$/.test(token)) fail();
    return { kind: "number", token, start, end: offset };
  };
  return value();
}

const hex = (code: number) => `\\u${code.toString(16).padStart(4, "0")}`;
const shortEscapes: Record<number, string> = {
  0x22: '\\"', 0x5c: "\\\\", 0x08: "\\b", 0x0c: "\\f", 0x0a: "\\n", 0x0d: "\\r", 0x09: "\\t",
};

// encoding/json's string writer with HTML escaping enabled.
export function goJSONString(value: string): string {
  let result = '"';
  for (const character of value) {
    const code = character.codePointAt(0)!;
    if (shortEscapes[code]) result += shortEscapes[code];
    else if (code < 0x20 || code === 0x3c || code === 0x3e || code === 0x26 || code === 0x2028 || code === 0x2029) {
      result += hex(code);
    } else if (code >= 0xd800 && code <= 0xdfff) result += "\ufffd";
    else result += character;
  }
  return `${result}"`;
}

// json.Marshal compacts a json.RawMessage and HTML-escapes its strings
// without otherwise rewriting escapes, so this is the retained wire form.
export function goCompact(raw: string): string {
  let result = "";
  let quoted = false;
  for (let index = 0; index < raw.length; index++) {
    const character = raw[index]!;
    if (!quoted) {
      if (!/[ \t\r\n]/.test(character)) result += character;
      if (character === '"') quoted = true;
      continue;
    }
    if (character === "\\") {
      result += character + raw[++index]!;
    } else if (character === "<" || character === ">" || character === "&" ||
               character === "\u2028" || character === "\u2029") {
      result += hex(character.charCodeAt(0));
    } else {
      result += character;
      if (character === '"') quoted = false;
    }
  }
  return result;
}

const utf8 = new TextEncoder();
function compareGoStrings(left: string, right: string): number {
  const a = utf8.encode(left);
  const b = utf8.encode(right);
  for (let index = 0; index < Math.min(a.length, b.length); index++) {
    if (a[index] !== b[index]) return a[index]! - b[index]!;
  }
  return a.length - b.length;
}

function goFloat(value: number): string {
  if (Object.is(value, -0)) return "-0";
  // Number#toString uses the same shortest digits and exponent thresholds as
  // encoding/json's float64 writer, which also drops exponent zero padding.
  return String(value);
}

interface Encoded { text: string; empty: boolean }

class Writer {
  opaque = false;
  constructor(private readonly raw: string) {}

  private isNull(node: Node | undefined): boolean {
    return !node || (node.kind === "literal" && node.token === "null");
  }

  private any(node: Node): string {
    switch (node.kind) {
      case "object":
        return `{${[...node.members].sort(([a], [b]) => compareGoStrings(a, b))
          .map(([key, member]) => `${goJSONString(key)}:${this.any(member)}`).join(",")}}`;
      case "array":
        return `[${node.items.map((item) => this.any(item)).join(",")}]`;
      case "string":
        return goJSONString(node.value);
      default:
        // Report decoding uses json.Decoder.UseNumber, which keeps dynamic tokens.
        return node.token;
    }
  }

  private integer(node: Node | undefined, type: string): Encoded {
    if (this.isNull(node)) return { text: "0", empty: true };
    if (node!.kind !== "number" || !/^-?\d+$/.test(node!.token)) throw new NotCanonical();
    const bits = BigInt(type.replace(/^u?int/, ""));
    const value = BigInt(node!.token);
    const [minimum, maximum] = type.startsWith("u")
      ? [0n, (1n << bits) - 1n]
      : [-(1n << (bits - 1n)), (1n << (bits - 1n)) - 1n];
    if (value < minimum || value > maximum) throw new NotCanonical();
    return { text: value.toString(), empty: value === 0n };
  }

  encode(node: Node | undefined, type: string): Encoded {
    if (type === "opaque") {
      if (!node) {
        this.opaque = true;
        return { text: "", empty: true };
      }
      this.opaque = true;
      return { text: goCompact(this.raw.slice(node.start, node.end)), empty: false };
    }
    if (type.startsWith("ptr:")) {
      return this.isNull(node) ? { text: "null", empty: true } : { ...this.encode(node, type.slice(4)), empty: false };
    }
    if (type.startsWith("slice:")) {
      if (this.isNull(node)) return { text: "null", empty: true };
      if (node!.kind !== "array") throw new NotCanonical();
      const element = type.slice(6);
      return { text: `[${node!.items.map((item) => this.encode(item, element).text).join(",")}]`, empty: node!.items.length === 0 };
    }
    if (type.startsWith("map:")) {
      if (this.isNull(node)) return { text: "null", empty: true };
      if (node!.kind !== "object") throw new NotCanonical();
      const element = type.slice(4);
      const members = [...node!.members].sort(([a], [b]) => compareGoStrings(a, b));
      return {
        text: `{${members.map(([key, member]) => `${goJSONString(key)}:${this.encode(member, element).text}`).join(",")}}`,
        empty: members.length === 0,
      };
    }
    switch (type) {
      case "any":
        return this.isNull(node) ? { text: "null", empty: true } : { text: this.any(node!), empty: false };
      case "string":
        if (this.isNull(node)) return { text: '""', empty: true };
        if (node!.kind !== "string") throw new NotCanonical();
        return { text: goJSONString(node!.value), empty: node!.value === "" };
      case "bool":
        if (this.isNull(node)) return { text: "false", empty: true };
        if (node!.kind !== "literal") throw new NotCanonical();
        return { text: node!.token, empty: node!.token === "false" };
      case "float64": {
        if (this.isNull(node)) return { text: "0", empty: true };
        if (node!.kind !== "number") throw new NotCanonical();
        const value = Number(node!.token);
        if (!Number.isFinite(value)) throw new NotCanonical();
        return { text: goFloat(value), empty: value === 0 };
      }
    }
    if (/^u?int\d+$/.test(type)) return this.integer(node, type);
    const shape = structs[type];
    if (!shape) throw new Error(`Runner-unit report shape has no type ${type}.`);
    if (!this.isNull(node) && node!.kind !== "object") throw new NotCanonical();
    const fields = shape.fields ?? [];
    const members = new Map(this.isNull(node) ? [] : (node as Extract<Node, { kind: "object" }>).members);
    // encoding/json drops unknown keys and case-folded aliases when it re-encodes.
    for (const key of members.keys()) {
      if (!fields.some((field) => field.name === key)) throw new NotCanonical();
    }
    const parts: string[] = [];
    for (const field of fields) {
      const encoded = this.encode(members.get(field.name), field.type);
      if (field.type === "opaque" && !members.has(field.name)) continue;
      if (field.omitempty && encoded.empty) continue;
      parts.push(`"${field.name}":${encoded.text}`);
    }
    return { text: `{${parts.join(",")}}`, empty: false };
  }
}

function member(node: Node | undefined, key: string): Node | undefined {
  return node?.kind === "object" ? node.members.find(([name]) => name === key)?.[1] : undefined;
}

// checkRetainedReportCanonical expects text already admitted by parseStrictJSON.
export function checkRetainedReportCanonical(text: string): ReportCanonicalCheck {
  let report: Node | undefined;
  try {
    report = member(member(parseNodes(text), "submission"), "report");
  } catch {
    throw new Error("Checkpoint retained submission report is malformed.");
  }
  if (!report) return "absent";
  const writer = new Writer(text);
  let canonical: string;
  try {
    canonical = writer.encode(report, (reportShape as { root: string }).root).text;
  } catch (cause) {
    if (!(cause instanceof NotCanonical)) throw cause;
    canonical = "";
  }
  if (canonical !== goCompact(text.slice(report.start, report.end))) {
    throw new Error("Checkpoint retained submission report contradicts the native canonical writer.");
  }
  return writer.opaque ? "unverified" : "canonical";
}

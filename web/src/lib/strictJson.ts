export function parseStrictJSON(text: string): unknown {
  let offset = 0;
  const invalid = () => { throw new Error("Ambiguous or malformed JSON."); };
  if (text.length > 16 * 1024 * 1024) return invalid();
  const space = () => {
    while (text[offset] !== undefined && /[ \t\r\n]/.test(text[offset]!)) offset++;
  };
  function string(): string {
    const start = offset++;
    while (offset < text.length) {
      const character = text[offset++];
      if (character === "\\") {
        offset++;
      } else if (character === '"') {
        const decoded: unknown = JSON.parse(text.slice(start, offset));
        if (typeof decoded !== "string") return invalid();
        for (let index = 0; index < decoded.length; index++) {
          const code = decoded.charCodeAt(index);
          if (code >= 0xd800 && code <= 0xdbff) {
            const next = decoded.charCodeAt(++index);
            if (!(next >= 0xdc00 && next <= 0xdfff)) return invalid();
          } else if (code >= 0xdc00 && code <= 0xdfff) return invalid();
        }
        return decoded;
      }
    }
    return invalid();
  }
  function read(depth: number) {
    if (depth > 64) return invalid();
    space();
    if (text[offset] === "{") {
      offset++;
      space();
      const keys = new Set<string>();
      if (text[offset] === "}") { offset++; return; }
      while (offset < text.length) {
        if (text[offset] !== '"') return invalid();
        const key = string();
        if (keys.has(key)) return invalid();
        keys.add(key);
        space();
        if (text[offset++] !== ":") return invalid();
        read(depth + 1);
        space();
        const delimiter = text[offset++];
        if (delimiter === "}") return;
        if (delimiter !== ",") return invalid();
        space();
      }
      return invalid();
    }
    if (text[offset] === "[") {
      offset++;
      space();
      if (text[offset] === "]") { offset++; return; }
      while (offset < text.length) {
        read(depth + 1);
        space();
        const delimiter = text[offset++];
        if (delimiter === "]") return;
        if (delimiter !== ",") return invalid();
      }
      return invalid();
    }
    if (text[offset] === '"') { string(); return; }
    const start = offset;
    while (offset < text.length && !/[ \t\r\n,\]}]/.test(text[offset]!)) offset++;
    if (start === offset) return invalid();
  }
  read(0);
  space();
  if (offset !== text.length) return invalid();
  return JSON.parse(text, (_key: string, value: unknown) => {
    if (typeof value === "number" && !Number.isFinite(value)) return invalid();
    return value;
  });
}

export function parseCSV(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let cell = "";
  let quoted = false;
  for (let index = 0; index < text.length; index++) {
    const char = text[index];
    if (char === '"') {
      if (quoted && text[index + 1] === '"') {
        cell += '"';
        index++;
      } else quoted = !quoted;
    } else if (!quoted && (char === "," || char === "\r" || char === "\n")) {
      row.push(cell);
      cell = "";
      if (char !== ",") {
        if (char === "\r" && text[index + 1] === "\n") index++;
        rows.push(row);
        row = [];
      }
    } else cell += char;
  }
  if (quoted) throw new Error("CSV has an unterminated quoted field.");
  row.push(cell);
  rows.push(row);
  return rows;
}

import { test, expect } from "@playwright/test";
import { toCSV } from "../src/lib/export";
import { parseCSV } from "./helpers/csv";

test("shared CSV encoder quotes CR, LF and CRLF without creating formula rows", () => {
  for (const separator of ["\r", "\n", "\r\n"]) {
    const output = `hello${separator}=1+1`;
    const csv = toCSV(["ID", "Output"], [["attempt-1", output], ["attempt-2", "hello"]]);
    expect(csv).toBe(`ID,Output\nattempt-1,"${output}"\nattempt-2,hello`);
    expect(parseCSV(csv)).toEqual([
      ["ID", "Output"], ["attempt-1", output], ["attempt-2", "hello"],
    ]);
    const unquoted = csv.replace(`"${output}"`, output);
    expect(parseCSV(unquoted)).toEqual([
      ["ID", "Output"], ["attempt-1", "hello"], ["=1+1"], ["attempt-2", "hello"],
    ]);
  }
});

test("CSV reader recognizes all record separators but preserves quoted newlines", () => {
  expect(parseCSV('ID,Output\rfirst,"a\rb"\nsecond,"a\nb"\r\nthird,"a\r\nb"')).toEqual([
    ["ID", "Output"], ["first", "a\rb"], ["second", "a\nb"], ["third", "a\r\nb"],
  ]);
  expect(() => parseCSV('ID,Output\nfirst,"hello\r=1+1')).toThrow("CSV has an unterminated quoted field.");
});

test("CSV formula defenses still apply to leading whitespace and all formula prefixes", () => {
  for (const formula of ["=1+1", "+1+1", "-1+1", "@SUM(1)", " \t=1+1", "\r=1+1", "\n=1+1", "\r\n=1+1"]) {
    const csv = toCSV(["Output"], [[formula]]);
    expect(parseCSV(csv)).toEqual([["Output"], [`'${formula}`]]);
    if (/[\r\n]/.test(formula)) expect(csv).toBe(`Output\n"'${formula}"`);
    else expect(csv).toBe(`Output\n'${formula}`);
  }
});

test("CSV legacy exact bytes, quotes, precision and empty cells remain unchanged", () => {
  const csv = toCSV(["ID", "Output", "Usage"], [
    ["plain", "hello", "9007199254740993"],
    ["commas", "hello,world", "0"],
    ["quotes", 'say "hello"', "0.123456789"],
    ["empty", "", ""],
    ["negative", "-0", ""],
  ]);
  expect(csv).toBe(
    'ID,Output,Usage\nplain,hello,9007199254740993\ncommas,"hello,world",0\nquotes,"say ""hello""",0.123456789\nempty,,\nnegative,\'-0,',
  );
  expect(parseCSV(csv)).toEqual([
    ["ID", "Output", "Usage"],
    ["plain", "hello", "9007199254740993"],
    ["commas", "hello,world", "0"],
    ["quotes", 'say "hello"', "0.123456789"],
    ["empty", "", ""],
    ["negative", "'-0", ""],
  ]);
});

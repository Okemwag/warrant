import { describe, expect, it } from "vitest";
import { dollarsInput, money, parseDollars, shortHash } from "./format";

describe("money", () => {
  it("always shows cents and thousands separators", () => {
    expect(money(4800)).toBe("$48.00");
    expect(money(5)).toBe("$0.05");
    expect(money(123456789)).toBe("$1,234,567.89");
    expect(money(-250)).toBe("-$2.50");
  });
});

describe("parseDollars", () => {
  it("accepts plain dollar amounts", () => {
    expect(parseDollars("48")).toBe(4800);
    expect(parseDollars("48.5")).toBe(4850);
    expect(parseDollars("$1,200.00")).toBe(120000);
  });
  it("rejects everything else", () => {
    for (const bad of ["", "abc", "-5", "1.005", "1e3"]) expect(parseDollars(bad)).toBeNull();
  });
  it("round-trips with dollarsInput", () => {
    for (const c of [100, 2500, 4850, 7]) expect(parseDollars(dollarsInput(c))).toBe(c);
  });
});

describe("shortHash", () => {
  it("shows hex of the first bytes", () => {
    expect(shortHash(btoa("\x01\x02\xff"))).toBe("0102ff");
  });
});

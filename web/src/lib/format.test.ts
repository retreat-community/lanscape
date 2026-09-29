import { describe, expect, it } from "vitest";
import { bytes, duration, mask, mbps, ms, rate, worse } from "./format";

describe("format", () => {
  it("formats rates", () => {
    expect(rate(0)).toBe("—");
    expect(rate(95_300_000)).toBe("95.3 Mbit/s");
    expect(rate(940_000_000)).toBe("940 Mbit/s");
    expect(rate(2_350_000_000)).toBe("2.35 Gbit/s");
    expect(mbps(1_234_000)).toBe("1.23");
  });
  it("formats time and sizes", () => {
    expect(ms(1500)).toBe("1.50 ms");
    expect(bytes(1536)).toBe("1.5 KiB");
    expect(duration(3700)).toBe("1h 1m");
  });
  it("orders verdicts", () => {
    expect(worse("green", "red")).toBe("red");
    expect(worse("yellow", "purple")).toBe("yellow");
    expect(worse(undefined, "none")).toBe("none");
  });
  it("masks secrets", () => {
    expect(mask("lsr_abcdefghijklmnop")).toBe("lsr_••••mnop");
  });
});

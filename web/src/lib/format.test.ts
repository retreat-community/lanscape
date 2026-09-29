import { describe, expect, it } from "vitest";
import { bytes, daysLeft, duration, hue, initials, latency, mask, mbps, ms, pct, rate, statusClass, worse } from "./format";

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

describe("service formatting", () => {
  it("maps statuses and numbers", () => {
    expect(statusClass("up")).toBe("v-green");
    expect(statusClass("down")).toBe("v-red");
    expect(statusClass("")).toBe("v-none");
    expect(pct(null)).toBe("—");
    expect(pct(100)).toBe("100%");
    expect(pct(99.5)).toBe("99.50%");
    expect(pct(50)).toBe("50.0%");
    expect(latency(3.21)).toBe("3.2 ms");
    expect(latency(120.4)).toBe("120 ms");
    expect(daysLeft(86400000 * 3 + 5, 0)).toBe(3);
  });
  it("builds fallback icons", () => {
    expect(initials("Home Assistant")).toBe("HA");
    expect(initials("gitea")).toBe("GI");
    expect(initials("media/jellyfin")).toBe("MJ");
    expect(hue("a")).toBe(hue("a"));
  });
});

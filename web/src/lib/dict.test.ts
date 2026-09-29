import { describe, expect, it } from "vitest";
import { en, ru, translate } from "./dict";

describe("dictionaries", () => {
  it("have the same keys and no empty strings", () => {
    expect(Object.keys(ru).sort()).toEqual(Object.keys(en).sort());
    for (const [k, v] of Object.entries(ru)) expect(v, k).not.toBe("");
  });
  it("interpolates parameters", () => {
    expect(translate("en", "net.running", { done: 1, total: 5, eta: "3s" })).toBe("Running: 1 of 5, ~3s left");
    expect(translate("ru", "net.check_all")).toBe("Проверить всё");
    expect(translate("ru", "missing.key")).toBe("missing.key");
  });
});

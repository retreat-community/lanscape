import { describe, expect, it } from "vitest";
import { wallets } from "./donate";

// a typo in an address would send donations nowhere: check the format of every network
const formats: Record<string, RegExp> = {
  Bitcoin: /^bc1[02-9ac-hj-np-z]{38,58}$/,
  Ethereum: /^0x[0-9a-fA-F]{40}$/,
  TRON: /^T[1-9A-HJ-NP-Za-km-z]{33}$/,
  TON: /^[UE]Q[A-Za-z0-9_-]{46}$/,
};

describe("donation wallets", () => {
  it("have valid addresses for their networks", () => {
    expect(wallets.map((w) => w.network)).toEqual(["Bitcoin", "Ethereum", "TRON", "TON"]);
    for (const w of wallets) expect(w.address).toMatch(formats[w.network]);
  });
});

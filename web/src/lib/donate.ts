// Crypto wallets for donations (the same as the donut-team projects use).
export interface Wallet {
  network: string;
  assets: string[];
  address: string;
}

export const wallets: Wallet[] = [
  { network: "Bitcoin", assets: ["BTC"], address: "bc1qzyc34w6jk9lhnagync80724wxhuklwfspk95ez" },
  { network: "Ethereum", assets: ["ETH", "USDT"], address: "0x57D67fE406994fC7e5095a0edA72B2EB3A2AffA2" },
  { network: "TRON", assets: ["TRX", "USDT"], address: "TRgcXpqvPrcntuq5ouHuJ7ofTyWqzQVBzu" },
  { network: "TON", assets: ["TON", "USDT"], address: "UQBNRESRFYTtMeRQ5t-x1u-zEg14zOeTcUzBoBg6td25Eqcl" },
];

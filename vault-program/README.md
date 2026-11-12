# Deposit vault program (Anchor)

On-chain guarantee for the gateway's deposit-address mode: each vault is a
PDA of this program that records the merchant's **cold wallet at creation**
and can move funds **only to that wallet**. Customers pay the vault with a
plain SOL transfer from any wallet; the gateway's hot wallet can trigger
sweeps but cannot redirect or steal funds.

Built with [Anchor](https://github.com/otter-sec/anchor) (otter-sec fork):
PDA seeds, rent funding and account constraints are declarative — the
destination invariant is a `constraint` checked by the runtime *before* the
handler runs, so `sweep` to any other wallet fails with a named
`WrongColdWallet` error.

## Instructions

Anchor discriminants: first 8 bytes of `sha256("global:<name>")`.

| Method | Accounts | Effect |
|---|---|---|
| `create_vault(cold_wallet: Pubkey)` | `[vault (init, seeds=["vault", seed], payer), seed key, payer (signer), system]` | Creates the PDA, stores `cold_wallet ‖ seed_key` (8B discriminator + 64B) |
| `sweep` | `[vault (seeds), cold_wallet (mut, == vault.cold_wallet)]` | Moves all lamports above the rent-exempt minimum to the stored cold wallet via direct lamport adjustment |

## Threat model

- **Gateway server fully compromised:** attacker can create vaults, pay
  rent, and call `sweep` — funds still land in the cold wallet. They can
  withhold sweeps (DoS) and burn hot-wallet SOL on fees. They cannot
  redirect payments or drain vaults.
- **Program upgrade authority compromised:** attacker can upgrade the
  program and drain all vaults. **Mitigate by revoking or multi-sig-ing the
  upgrade authority after deployment** (`solana program set-upgrade-authority
  --final` makes the program immutable).
- No instruction closes vaults or reclaims rent (prototype); the rent-exempt
  minimum (~0.0001 SOL for 72B data) is locked while the vault lives.

## Build & deploy

```sh
export PATH="$HOME/.local/share/solana/install/active_release/bin:$PATH"
cd vault-program && cargo-build-sbf
solana-keygen new -o program-keypair.json --no-bip39-passphrase   # once
# put the pubkey in src/lib.rs declare_id!(), rebuild, then:
solana program deploy target/deploy/vault_anchor.so \
  --program-id program-keypair.json --url <cluster> --keypair <hot-wallet>
solana program set-upgrade-authority <PROGRAM_ID> --url <cluster> \
  --keypair <hot-wallet> --final   # optional but recommended
```

Then set `DEPOSIT_PROGRAM_ID=<PROGRAM_ID>`,
`GATEWAY_HOT_WALLET=<path>` (and `DEPOSIT_ADDRESS_MODE=true`,
`DEPOSIT_MASTER_KEY=<openssl rand -base64 32>`) in the gateway env.

## Verified live (devnet)

Program `HJfP8oj6MyigUYo3LHnk8JHRA11xMWDwQ1uxVoCuB1M4`:
- create → plain payment → sweep → cold wallet received the exact amount,
  vault back at rent minimum
- hostile `sweep` to a different destination: rejected on-chain with
  `AnchorError ... Error Code: WrongColdWallet` before handler code runs
- gateway e2e: invoice → vault created at payment time → plain transfer →
  `confirmed` → swept → `finalized`; cold wallet balance exact to the lamport

## Audits

None. Prototype: get a professional audit before holding real money on
mainnet.

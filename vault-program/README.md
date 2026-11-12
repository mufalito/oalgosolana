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

There is exactly ONE instruction. Anchor discriminant: first 8 bytes of
`sha256("global:sweep")`.

| Method | Accounts | Effect |
|---|---|---|
| `sweep(invoice_id: [u8;16])` | `[vault (SystemAccount, seeds=["vault", merchant, invoice_id]), merchant (mut), system]` | Transfers the vault's ENTIRE balance to the merchant via system-program CPI signed by the PDA; the account ends at 0 lamports and closes |

## Design: derived vaults

A vault address is `PDA(["vault", merchant, invoice_id])`. It does NOT exist
on-chain until the customer's own SOL transfer creates it (the customer's
payment pays the account rent). Because the account is system-owned, the
program can sweep it via a system-program CPI under its PDA signature — no
`create_vault`, no stored state, no per-invoice secrets, and no create
transaction that could fail silently or be front-run (a v1-style create
whose cold-wallet argument is attacker-chosen is impossible here: the
destination is part of the address).

Full drain: the sweep moves every lamport including the rent and the account
closes. Nothing is stranded.

## Threat model

- **Gateway server fully compromised:** the attacker can hand customers
  different addresses for NEW invoices (detectable: the merchant can
  recompute every valid address offline), withhold sweeps (DoS), and burn
  hot-wallet SOL on fees. They cannot redirect or steal money already in a
  vault, and cannot change any vault's destination.
- **Program upgrade authority compromised:** attacker can upgrade the
  program and drain all vaults. **Mitigate by revoking or multi-sig-ing the
  upgrade authority after deployment** (`solana program set-upgrade-authority
  --final` makes the program immutable).
- Anyone may call `sweep` — safe by construction, and it lets the merchant
  run an independent sweeper as a checkpoint on the gateway.

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

Program `6eASRQNLVAwpZGremorEuZwNu2tSYUrDbMUoih7BUx8g` (v2, derived vaults):
- invoice → derived address only (nothing on-chain) → plain transfer
  creates the vault → sweep → merchant received the full amount including
  rent; vault ends at 0 lamports
- hostile `sweep` naming a different merchant: rejected on-chain
  (`AnchorError ... ConstraintSeeds` — the passed vault does not match the
  address derived from the attacker's merchant)
- gateway e2e: invoice → plain transfer → `confirmed` → sweep → `finalized`
  in ~4s; merchant balance exact to the lamport

## Audits

None. Prototype: get a professional audit before holding real money on
mainnet.

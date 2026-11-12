
oalgosolana architecture review
Nov 12, 2026 · @Someone
A per-invoice vault address is the right answer to "no memos or references", and Relay's deposit-address program does the same thing on mainnet. The problem is that the gateway creates every vault on-chain at invoice time. That costs about 0.001 SOL per invoice whether or not it is paid, and it opens a way to redirect funds. Derive the address and never create it.
Reference mode is already invisible to wallet users, because the wallet adds the reference from the QR code. The vault matters for exchange withdrawals and pasted addresses. Four issues follow, biggest first.
1. Derive the vault address, never create it
A PDA can receive SOL without existing as an account, so create_vault is not needed.
Hand out the address derived from ["vault", merchant_wallet, invoice_id] and send nothing on-chain at invoice time. When SOL arrives, sweep re-derives the address and moves the whole balance to the merchant, signing with the seeds.

Today
Derived address
At invoice creation
create_vault transaction paid by the hot wallet
Nothing on-chain
Cost of an unpaid invoice
About 0.001 SOL of rent, never reclaimed
Zero
What fixes the destination
An argument to create_vault, set by whoever calls it first
The address itself
Secrets stored
Encrypted seed and a master key
None
Left in the vault after a sweep
The rent
Nothing
// Untested sketch: adjust to your Anchor version.
// Same shape as the Anchor docs example for a CPI with a PDA signer.
pub fn sweep(ctx: Context<Sweep>, invoice_id: [u8; 16]) -> Result<()> {
    // Whole balance, so nothing is left behind.
    let amount = ctx.accounts.vault.lamports();
    let merchant = ctx.accounts.merchant.key();
    let seeds: &[&[u8]] = &[b"vault", merchant.as_ref(), &invoice_id, &[ctx.bumps.vault]];
    system_program::transfer(
        CpiContext::new(
            ctx.accounts.system_program.to_account_info(),
            system_program::Transfer {
                from: ctx.accounts.vault.to_account_info(),
                to: ctx.accounts.merchant.to_account_info(),
            },
        )
        .with_signer(&[seeds]),
        amount,
    )
}

#[derive(Accounts)]
#[instruction(invoice_id: [u8; 16])]
pub struct Sweep<'info> {
    #[account(mut, seeds = [b"vault", merchant.key().as_ref(), invoice_id.as_ref()], bump)]
    pub vault: SystemAccount<'info>,
    /// CHECK: bound by the vault seeds; another destination derives another address.
    #[account(mut)]
    pub merchant: UncheckedAccount<'info>,
    pub system_program: Program<'info, System>,
}
This also closes a redirect hole. Today the cold wallet is an argument and is not in the seeds (vault-program/src/lib.rs:17-23). The gateway sends the create with skipPreflight and never checks that it landed (internal/vault/vault.go:69-78).
If that create lands failed, for example when the hot wallet cannot cover rent, the seed is public on-chain. Anyone can then create the vault with their own wallet and sweep the customer's payment.
The seed is not a secret in any case, because it is an account in the create transaction. The deposit_keys table and DEPOSIT_MASTER_KEY can go.
A compromised server can still give customers a different address for new invoices. The program only protects money already in a vault. With derived addresses the merchant's own backend can recompute the address and catch that.
One limit: the first transfer to an empty address must cover the rent minimum for an empty account, 650,240 lamports (about 0.00065 SOL) today. Set a minimum invoice amount and read the value from the RPC, because rent is being cut in steps.
2. A sent sweep is treated as a landed sweep
Sweeps are sent once and never verified, so money can sit in a vault while the invoice reads finalized.
• A sweep that lands failed still finalizes the invoice and fires the webhook. The check reads confirmationStatus and ignores err (internal/vault/vault.go:159-168).
• A dropped sweep is never re-sent. Once sweep_tx is set, the code only waits on that signature (internal/watcher/deposit.go:108-126).
• Confirmed invoices stop being polled at expires_at (internal/watcher/watcher.go:71). A sweep still pending at expiry is abandoned.
• The late-deposit sweeper is never wired up (cmd/server/main.go:136-140). Funds that arrive after expiry stay in the vault with no record.
Deposit mode exists for exchange and pasted-address payers, who are the ones that pay late, short or twice. This path is the normal case, not an edge case.
Make the sweeper read the chain instead of its own history. Any known vault with a balance gets swept, and again on the next tick until it is empty, whatever the invoice status.
3. Detection skips invoices and miscounts amounts
The watcher only looks at the 50 oldest open invoices, and its totals include the gateway's own rent.
• The poll is ORDER BY id LIMIT 50 with no cursor or claim (internal/watcher/watcher.go:70-72). Fifty unpaid invoices with a long expiry leave every newer invoice unwatched, and paid ones expire. The README's FOR UPDATE SKIP LOCKED is not in the code.
• The deposit total sums positive balance changes over the last 10 signatures (internal/watcher/deposit.go:43-72). That counts the vault-creation rent, so every invoice accepts about 0.001 SOL less than its amount. Invoices below that confirm with no payment.
Poll balances instead: one getMultipleAccounts call covers 100 addresses. Use a balance as the trigger to sweep and the confirmed sweep as the receipt. The sweep carries the invoice id, so one cursor over the program's transactions finds every payment.
4. USDC needs a decision before more is built
Deposit mode is SOL only, and tokens are where a PDA address gets awkward.
• A token account must exist before USDC can arrive, about 0.0015 SOL of rent today. That is a deposit, not a charge: the sweep can close the account and return the rent to whoever paid it.
• Some senders refuse off-curve recipients. Ledger Live blocks token sends to them unless it is given an existing token account. Bybit and Bitget reportedly cannot withdraw to PDAs; Coinbase, Binance, Kraken and OKX reportedly can.
Suggested default: keep the PDA, pre-create the token account per invoice, and reclaim the rent at sweep or expiry. Before committing, test real sends from the wallets and exchanges your customers use.
The fallback is a normal keypair address for USDC invoices. It works with every sender but puts keys on the server, which is what the vault avoids.
Smaller, worth a look
• Reference mode lets the first transaction carrying the reference decide the invoice. If it fails validation the invoice is failed for good (internal/watcher/watcher.go:112-129). Skip bad candidates and keep looking.
• Reference-mode USDC probably never validates. Validate requires the merchant wallet among the transaction's accounts (internal/chain/validate.go:31-33), and a standard token transfer lists only token accounts. This is untested; try one payment on devnet.
• The README's security claims run ahead of the code: no private keys, replica-safe polling, late funds swept. Re-check them after the changes above.
Basis and sources
This is a static read of commit 1c6791d. Nothing was built or run, and the code sketch is untested. Rent figures were read from mainnet on 12 November 2026. Exchange behaviour is as reported by Squads, not tested.
• Relay deposit-address program: a derived deposit address with a permissionless sweep. The repository's audits do not cover this program.
• Anchor docs: CPI with a PDA signer
• Squads: sending assets between exchanges and PDA addresses
• Ledger Live recipient validation
• Solana exchange integration guide
• Status of the stepped rent cuts

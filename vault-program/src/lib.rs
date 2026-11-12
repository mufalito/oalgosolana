//! Deposit vault program v2 — derived vaults.
//!
//! There is NO create instruction and NO stored state. A vault is just the
//! PDA `["vault", merchant, invoice_id]`: it never exists on-chain until the
//! customer's own plain SOL transfer creates it (their payment pays the
//! account rent). Funds can leave in exactly one way: `sweep` transfers the
//! ENTIRE balance to the merchant embedded in the vault's seeds — a
//! different destination derives a different address, so no argument, no
//! authority, and no account data can redirect money. The full drain zeroes
//! the account, so nothing (not even rent) is left behind.
//!
//! Anyone may call sweep; that is safe by construction and lets the merchant
//! run an independent sweeper as a checkpoint on the gateway.

use anchor_lang::prelude::*;
use anchor_lang::system_program;

declare_id!("6eASRQNLVAwpZGremorEuZwNu2tSYUrDbMUoih7BUx8g");

#[program]
pub mod vault_anchor {
    use super::*;

    /// Transfers the vault's entire balance to the merchant bound into the
    /// vault's PDA seeds. The account ends at zero lamports and is removed.
    pub fn sweep(ctx: Context<Sweep>, invoice_id: [u8; 16]) -> Result<()> {
        let vault = ctx.accounts.vault.to_account_info();
        let amount = vault.lamports();
        if amount == 0 {
            return Ok(());
        }
        let seeds: &[&[u8]] = &[
            b"vault",
            ctx.accounts.merchant.key.as_ref(),
            &invoice_id,
            &[ctx.bumps.vault],
        ];
        let signer = [seeds];
        let cpi = CpiContext::new(
            *ctx.accounts.system_program.to_account_info().key,
            system_program::Transfer {
                from: vault,
                to: ctx.accounts.merchant.to_account_info(),
            },
        )
        .with_signer(&signer);
        system_program::transfer(cpi, amount)
    }
}

#[derive(Accounts)]
#[instruction(invoice_id: [u8; 16])]
pub struct Sweep<'info> {
    /// The vault: an ordinary system account (created by the customer's
    /// payment) at the PDA derived from the merchant and invoice id.
    /// System-owned, so the system program can move its lamports under our
    /// PDA signature.
    #[account(
        mut,
        seeds = [b"vault", merchant.key().as_ref(), invoice_id.as_ref()],
        bump
    )]
    pub vault: SystemAccount<'info>,
    /// CHECK: bound by the vault seeds — sweeping "to someone else" derives a
    /// different vault address and fails the seeds check above.
    #[account(mut)]
    pub merchant: UncheckedAccount<'info>,
    pub system_program: Program<'info, System>,
}

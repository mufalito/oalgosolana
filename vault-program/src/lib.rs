//! Deposit vault program v2 — derived vaults.
//!
//! A vault is the PDA `["vault", merchant, invoice_id]`. It never exists
//! on-chain until money arrives:
//!
//! - SOL: the customer's plain transfer creates the vault (and pays its
//!   rent). `sweep` moves the ENTIRE balance to the merchant embedded in
//!   the seeds and the account closes.
//! - SPL tokens: the customer's wallet creates the vault's associated token
//!   account (paying its rent) and sends tokens. `sweep_spl` transfers the
//!   full token balance to the merchant's ATA and CLOSES the vault ATA,
//!   returning its rent to the merchant.
//!
//! In both cases the destination is bound by the PDA seeds — no argument,
//! no authority, and no stored state can redirect funds. Anyone may call
//! sweep; that is safe by construction and lets the merchant run an
//! independent sweeper as a checkpoint on the gateway.

use anchor_lang::prelude::*;
use anchor_lang::system_program;
use anchor_spl::associated_token::AssociatedToken;
use anchor_spl::token::{self, Token, TokenAccount};

declare_id!("6eASRQNLVAwpZGremorEuZwNu2tSYUrDbMUoih7BUx8g");

#[program]
pub mod vault_anchor {
    use super::*;

    /// Transfers the vault's entire SOL balance to the merchant bound into
    /// the vault's PDA seeds. The account ends at zero lamports and closes.
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

    /// Transfers the vault's entire token balance of `mint` to the
    /// merchant's ATA (creating it if needed, rent paid by the fee payer),
    /// then closes the vault ATA and returns its rent to the merchant.
    pub fn sweep_spl(ctx: Context<SweepSpl>, invoice_id: [u8; 16]) -> Result<()> {
        let amount = ctx.accounts.vault_ata.amount;
        let seeds: &[&[u8]] = &[
            b"vault",
            ctx.accounts.merchant.key.as_ref(),
            &invoice_id,
            &[ctx.bumps.vault],
        ];
        let signer = [seeds];
        if amount > 0 {
            token::transfer(
                CpiContext::new(
                    *ctx.accounts.token_program.to_account_info().key,
                    token::Transfer {
                        from: ctx.accounts.vault_ata.to_account_info(),
                        to: ctx.accounts.merchant_ata.to_account_info(),
                        authority: ctx.accounts.vault.to_account_info(),
                    },
                )
                .with_signer(&signer),
                amount,
            )?;
        }
        token::close_account(CpiContext::new_with_signer(
            *ctx.accounts.token_program.to_account_info().key,
            token::CloseAccount {
                account: ctx.accounts.vault_ata.to_account_info(),
                destination: ctx.accounts.merchant.to_account_info(),
                authority: ctx.accounts.vault.to_account_info(),
            },
            &signer,
        ))
    }
}

#[derive(Accounts)]
#[instruction(invoice_id: [u8; 16])]
pub struct Sweep<'info> {
    /// The vault: an ordinary system account (created by the customer's
    /// payment) at the PDA derived from the merchant and invoice id.
    #[account(
        mut,
        seeds = [b"vault", merchant.key.as_ref(), invoice_id.as_ref()],
        bump
    )]
    pub vault: SystemAccount<'info>,
    /// CHECK: bound by the vault seeds — sweeping "to someone else" derives a
    /// different vault address and fails the seeds check above.
    #[account(mut)]
    pub merchant: UncheckedAccount<'info>,
    pub system_program: Program<'info, System>,
}

#[derive(Accounts)]
#[instruction(invoice_id: [u8; 16])]
pub struct SweepSpl<'info> {
    /// The vault PDA. It may not exist on-chain at all (token-only payment);
    /// only its address matters, as the token authority and rent recipient
    /// are derived from the seeds.
    /// CHECK: address is seeds-constrained; no data is read.
    #[account(
        seeds = [b"vault", merchant.key.as_ref(), invoice_id.as_ref()],
        bump
    )]
    pub vault: UncheckedAccount<'info>,

    /// The vault's token account, owned (authority) by the vault PDA.
    #[account(
        mut,
        associated_token::mint = mint,
        associated_token::authority = vault,
    )]
    pub vault_ata: Account<'info, TokenAccount>,

    /// CHECK: bound by the vault seeds.
    #[account(mut)]
    pub merchant: UncheckedAccount<'info>,

    /// The merchant's token account, created on first sweep if missing
    /// (rent paid by the fee payer — the gateway hot wallet).
    #[account(
        init_if_needed,
        associated_token::mint = mint,
        associated_token::authority = merchant,
        payer = payer,
    )]
    pub merchant_ata: Account<'info, TokenAccount>,

    /// CHECK: the mint of the swept token; bound via the ATA constraints.
    #[account(address = mint.key())]
    pub mint: UncheckedAccount<'info>,

    #[account(mut)]
    pub payer: Signer<'info>,
    pub token_program: Program<'info, Token>,
    pub associated_token_program: Program<'info, AssociatedToken>,
    pub system_program: Program<'info, System>,
}

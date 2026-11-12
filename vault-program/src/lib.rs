//! Deposit vault program (Anchor rewrite of vault-program/).
//!
//! Each vault is a PDA storing the merchant cold wallet at creation. Funds
//! can only leave via `sweep`, and only to that stored wallet — enforced by
//! the account constraint, checked by the runtime before handler code runs.

use anchor_lang::prelude::*;

declare_id!("HJfP8oj6MyigUYo3LHnk8JHRA11xMWDwQ1uxVoCuB1M4");

#[program]
pub mod vault_anchor {
    use super::*;

    /// Creates a vault PDA seeded by the invoice seed key. Rent is paid by
    /// the gateway hot wallet (tx payer).
    pub fn create_vault(ctx: Context<CreateVault>, cold_wallet: Pubkey) -> Result<()> {
        let zero = [0u8; 32];
        require!(cold_wallet.as_ref() != zero, VaultError::ZeroColdWallet);
        ctx.accounts.vault.cold_wallet = cold_wallet;
        ctx.accounts.vault.seed_key = ctx.accounts.seed_key.key();
        Ok(())
    }

    /// Moves everything above the rent-exempt minimum to the cold wallet
    /// recorded at creation. Any other destination fails the account
    /// constraint before this code runs.
    pub fn sweep(ctx: Context<Sweep>) -> Result<()> {
        let rent = Rent::get()?;
        let minimum = rent.minimum_balance(SPACE);
        let vault_info = ctx.accounts.vault.to_account_info();
        let balance = vault_info.lamports();
        if balance <= minimum {
            return Ok(()); // nothing sweepable yet
        }
        let amount = balance - minimum;
        **vault_info.lamports.borrow_mut() -= amount;
        **ctx.accounts.cold_wallet.lamports.borrow_mut() += amount;
        Ok(())
    }
}

/// Anchor discriminator (8) + cold_wallet (32) + seed_key (32).
pub const SPACE: usize = 8 + 32 + 32;

#[account]
pub struct Vault {
    /// The ONLY address funds can ever leave to.
    pub cold_wallet: Pubkey,
    /// Second PDA seed, stored to re-check seeds on sweep.
    pub seed_key: Pubkey,
}

#[error_code]
pub enum VaultError {
    #[msg("cold wallet must not be the zero pubkey")]
    ZeroColdWallet,
    #[msg("sweep destination is not the vault's cold wallet")]
    WrongColdWallet,
}

#[derive(Accounts)]
pub struct CreateVault<'info> {
    #[account(
        init,
        seeds = [b"vault", seed_key.key().as_ref()],
        bump,
        space = SPACE,
        payer = payer,
    )]
    pub vault: Account<'info, Vault>,
    /// Seed marker account: the invoice seed pubkey (no data, not a signer).
    /// CHECK: used only as a PDA seed.
    pub seed_key: UncheckedAccount<'info>,
    #[account(mut)]
    pub payer: Signer<'info>,
    pub system_program: Program<'info, System>,
}

#[derive(Accounts)]
pub struct Sweep<'info> {
    #[account(mut, seeds = [b"vault", vault.seed_key.as_ref()], bump)]
    pub vault: Account<'info, Vault>,
    /// CHECK: destination is constrained to the cold wallet fixed at
    /// creation — this is the core security invariant.
    #[account(
        mut,
        constraint = cold_wallet.key() == vault.cold_wallet @ VaultError::WrongColdWallet
    )]
    pub cold_wallet: UncheckedAccount<'info>,
}

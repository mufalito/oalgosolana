// Package vault builds transactions for the deposit-vault program
// (vault-program/): PDAs that can only release funds to the cold wallet
// fixed at creation. The gateway holds a hot wallet key that can create
// vaults and trigger sweeps but can never redirect funds.
package vault

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/gagliardetto/solana-go"

	"github.com/mufalito/oalgosolana/internal/chain"
)

// Anchor instruction discriminants: first 8 bytes of
// sha256("global:<method_name>"). Computed from vault-anchor/src/lib.rs.
var (
	DiscCreate = anchorSighash("global:create_vault")
	DiscSweep  = anchorSighash("global:sweep")
)

func anchorSighash(name string) []byte {
	h := sha256.Sum256([]byte(name))
	return h[:8]
}

// VaultSeed is the PDA seed prefix used by the program.
var VaultSeed = []byte("vault")

// Address derives the vault PDA for a seed key.
func Address(seed solana.PublicKey, programID solana.PublicKey) (solana.PublicKey, error) {
	pk, _, err := solana.FindProgramAddress([][]byte{VaultSeed, seed.Bytes()}, programID)
	return pk, err
}

// CreateInstruction builds the create_vault instruction. Accounts:
// [vault PDA (writable), seed key, payer (signer, writable), system].
func CreateInstruction(programID, seed, coldWallet, payer solana.PublicKey) (solana.Instruction, solana.PublicKey, error) {
	vaultAddr, err := Address(seed, programID)
	if err != nil {
		return nil, vaultAddr, err
	}
	data := make([]byte, 0, 8+32)
	data = append(data, DiscCreate...)
	data = append(data, coldWallet.Bytes()...)
	return solana.NewInstruction(
		programID,
		solana.AccountMetaSlice{
			{PublicKey: vaultAddr, IsWritable: true, IsSigner: false},
			{PublicKey: seed, IsWritable: false, IsSigner: false},
			{PublicKey: payer, IsWritable: true, IsSigner: true},
			{PublicKey: solana.SystemProgramID},
		},
		data,
	), vaultAddr, nil
}

// Sweeper creates vaults and sweeps them via the on-chain program.
type Sweeper struct {
	RPC        *chain.RPCDetector
	ProgramID  solana.PublicKey
	HotWallet  solana.PrivateKey
	ColdWallet solana.PublicKey
}

// CreateVault deploys a new vault PDA for the seed key; returns its address.
func (s *Sweeper) CreateVault(ctx context.Context, seed solana.PublicKey) (solana.PublicKey, error) {
	ix, vaultAddr, err := CreateInstruction(s.ProgramID, seed, s.ColdWallet, s.HotWallet.PublicKey())
	if err != nil {
		return vaultAddr, err
	}
	if _, err := s.sendTx(ctx, ix); err != nil {
		return vaultAddr, fmt.Errorf("create vault: %w", err)
	}
	return vaultAddr, nil
}

// Sweep triggers the program's sweep for a vault; funds go to the cold wallet
// hardcoded in the vault (the hot wallet cannot redirect them).
func (s *Sweeper) Sweep(ctx context.Context, vaultAddr solana.PublicKey) (string, error) {
	ix := solana.NewInstruction(
		s.ProgramID,
		solana.AccountMetaSlice{
			{PublicKey: vaultAddr, IsWritable: true, IsSigner: false},
			{PublicKey: s.ColdWallet, IsWritable: true, IsSigner: false},
			{PublicKey: solana.SystemProgramID},
		},
		DiscSweep,
	)
	return s.sendTx(ctx, ix)
}

func (s *Sweeper) sendTx(ctx context.Context, ix solana.Instruction) (string, error) {
	bh, err := s.blockhash(ctx)
	if err != nil {
		return "", err
	}
	tx, err := solana.NewTransaction([]solana.Instruction{ix}, bh,
		solana.TransactionPayer(s.HotWallet.PublicKey()))
	if err != nil {
		return "", err
	}
	hw := s.HotWallet
	if _, err := tx.Sign(func(k solana.PublicKey) *solana.PrivateKey {
		if k.Equals(hw.PublicKey()) {
			return &hw
		}
		return nil
	}); err != nil {
		return "", err
	}
	wire, err := tx.MarshalBinary()
	if err != nil {
		return "", err
	}
	return s.RPC.SendTransaction(ctx, wire)
}

func (s *Sweeper) blockhash(ctx context.Context) (solana.Hash, error) {
	bhs, err := s.RPC.GetLatestBlockhash(ctx)
	if err != nil {
		return solana.Hash{}, err
	}
	return solana.HashFromBase58(bhs)
}

// SeedFromSecret reverses the gateway's storage convention: in vault mode the
// encrypted "secret" holds the 32-byte seed pubkey (there is no private key).
func SeedFromSecret(b []byte) (solana.PublicKey, error) {
	if len(b) != 32 {
		return solana.PublicKey{}, fmt.Errorf("seed must be 32 bytes")
	}
	var pk solana.PublicKey
	copy(pk[:], b)
	return pk, nil
}

// DepositSweeperAdapter adapts the vault program sweeper to the watcher's
// DepositSweeper interface. In vault mode the decrypted "secret" is the
// 32-byte vault seed pubkey (no private key exists).
type DepositSweeperAdapter struct {
	S *Sweeper
}

func (a *DepositSweeperAdapter) Sweep(ctx context.Context, secret []byte) (string, error) {
	seed, err := SeedFromSecret(secret)
	if err != nil {
		return "", err
	}
	addr, err := Address(seed, a.S.ProgramID)
	if err != nil {
		return "", err
	}
	return a.S.Sweep(ctx, addr)
}

func (a *DepositSweeperAdapter) Finalized(ctx context.Context, sig string) (bool, error) {
	statuses, err := a.S.RPC.GetSignatureStatuses(ctx, []string{sig})
	if err != nil {
		return false, err
	}
	if len(statuses) == 0 || statuses[0] == nil {
		return false, nil
	}
	return statuses[0].ConfirmationStatus == "finalized", nil
}

// NewSeed generates a fresh random seed pubkey for a vault.
func NewSeed() (solana.PublicKey, error) {
	kp, err := solana.NewRandomPrivateKey()
	if err != nil {
		return solana.PublicKey{}, err
	}
	return kp.PublicKey(), nil
}

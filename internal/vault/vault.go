// Package vault is the gateway client for the deposit-vault program v2
// (vault-program/): derived vaults. A vault is the PDA
// ["vault", merchant, invoice_id]; it never exists on-chain until the
// customer's payment creates it. sweep moves the ENTIRE balance to the
// merchant embedded in the seeds — no create instruction, no stored state,
// no way to redirect funds. The hot wallet only pays tx fees.
package vault

import (
	"context"
	"crypto/sha256"

	"github.com/gagliardetto/solana-go"
	"github.com/google/uuid"

	"github.com/mufalito/oalgosolana/internal/chain"
)

// Anchor instruction discriminant: sha256("global:sweep")[0..8].
var discSweep = anchorSighash("global:sweep")

func anchorSighash(name string) []byte {
	h := sha256.Sum256([]byte(name))
	return h[:8]
}

// Address derives the vault PDA for a merchant and invoice id (UUID string).
func Address(merchant solana.PublicKey, invoiceID string, programID solana.PublicKey) (solana.PublicKey, error) {
	id, err := InvoiceIDBytes(invoiceID)
	if err != nil {
		return solana.PublicKey{}, err
	}
	pk, _, err := solana.FindProgramAddress(
		[][]byte{[]byte("vault"), merchant.Bytes(), id[:]}, programID)
	return pk, err
}

// InvoiceIDBytes parses a UUID string into its 16 raw bytes.
func InvoiceIDBytes(invoiceID string) ([16]byte, error) {
	var b [16]byte
	id, err := uuid.Parse(invoiceID)
	if err != nil {
		return b, err
	}
	copy(b[:], id[:])
	return b, nil
}

// Sweeper triggers sweeps of derived vaults (hot wallet pays fees only).
type Sweeper struct {
	RPC       *chain.RPCDetector
	ProgramID solana.PublicKey
	Merchant  solana.PublicKey
	HotWallet solana.PrivateKey
}

// SweepVault sends the sweep instruction for an invoice's vault and returns
// the sweep signature. Safe for anyone to call: funds go only to the
// merchant bound in the vault's seeds.
func (s *Sweeper) SweepVault(ctx context.Context, invoiceID string) (string, error) {
	id, err := InvoiceIDBytes(invoiceID)
	if err != nil {
		return "", err
	}
	vaultAddr, err := Address(s.Merchant, invoiceID, s.ProgramID)
	if err != nil {
		return "", err
	}
	data := make([]byte, 0, 8+16)
	data = append(data, discSweep...)
	data = append(data, id[:]...)
	ix := solana.NewInstruction(
		s.ProgramID,
		solana.AccountMetaSlice{
			{PublicKey: vaultAddr, IsWritable: true, IsSigner: false},
			{PublicKey: s.Merchant, IsWritable: true, IsSigner: false},
			{PublicKey: solana.SystemProgramID},
		},
		data,
	)
	return s.sendTx(ctx, ix)
}

func (s *Sweeper) sendTx(ctx context.Context, ix solana.Instruction) (string, error) {
	bhs, err := s.RPC.GetLatestBlockhash(ctx)
	if err != nil {
		return "", err
	}
	bh, err := solana.HashFromBase58(bhs)
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

// SweepState classifies a sent sweep: "pending" (in flight), "landed"
// (finalized successfully) or "failed" (errored on-chain or dropped).
func (s *Sweeper) SweepState(ctx context.Context, sig string) (string, error) {
	statuses, err := s.RPC.GetSignatureStatuses(ctx, []string{sig})
	if err != nil {
		return "", err
	}
	if len(statuses) == 0 || statuses[0] == nil {
		// not in the status cache: treat as dropped after the fact; the
		// caller only re-sends when the vault still holds funds, which is
		// harmless if the sweep is merely slow (double sweep of a partial
		// drain is safe — the second transfer just finds less or nothing).
		return "failed", nil
	}
	st := statuses[0]
	if st.Err != nil {
		return "failed", nil
	}
	if st.ConfirmationStatus == "finalized" {
		return "landed", nil
	}
	return "pending", nil
}

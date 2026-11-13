package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/rpc"
)

func main() {
	lamports := os.Args[1]
	ctx := context.Background()
	rc := rpc.New("https://api.devnet.solana.com")
	payer, _ := solana.PrivateKeyFromSolanaKeygenFile("/tmp/vault/hot.json")
	vault := mustPK("6JbAzenE4icN6rRTdTdkk1UXtCbvA1rWdqxbNnJsghsP")
	recent, _ := rc.GetLatestBlockhash(ctx, rpc.CommitmentConfirmed)
	amt := uint64(0)
	for _, c := range lamports { amt = amt*10 + uint64(c-'0') }
	tx, _ := solana.NewTransaction([]solana.Instruction{system.NewTransferInstruction(amt, payer.PublicKey(), vault).Build()},
		recent.Value.Blockhash, solana.TransactionPayer(payer.PublicKey()))
	p := payer
	tx.Sign(func(k solana.PublicKey) *solana.PrivateKey { if k.Equals(p.PublicKey()) { return &p }; return nil })
	sig, err := rc.SendTransactionWithOpts(ctx, tx, rpc.TransactionOpts{SkipPreflight: true})
	if err != nil { panic(err) }
	fmt.Println("PAID(" + lamports + ")=" + sig.String())
}
func mustPK(s string) solana.PublicKey { pk, err := solana.PublicKeyFromBase58(s); if err != nil { panic(err) }; return pk }

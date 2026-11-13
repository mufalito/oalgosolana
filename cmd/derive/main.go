// Command derive prints the deposit vault address for a merchant/program/
// invoice triple, letting merchants independently verify the addresses the
// gateway hands out (offline; pure derivation, no RPC).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/gagliardetto/solana-go"

	"github.com/mufalito/oalgosolana/internal/vault"
)

func main() {
	merchant := flag.String("merchant", "", "merchant (cold) wallet, base58")
	program := flag.String("program", "", "vault program id, base58")
	invoice := flag.String("invoice", "", "invoice id (UUID)")
	mint := flag.String("mint", "", "optional SPL mint: also print the vault's token account")
	flag.Parse()
	m, err := solana.PublicKeyFromBase58(*merchant)
	if err != nil {
		fail("merchant", err)
	}
	p, err := solana.PublicKeyFromBase58(*program)
	if err != nil {
		fail("program", err)
	}
	if *invoice == "" {
		fail("invoice", fmt.Errorf("required"))
	}
	addr, err := vault.Address(m, *invoice, p)
	if err != nil {
		fail("derive", err)
	}
	fmt.Println(addr.String())
	if *mint != "" {
		mi, err := solana.PublicKeyFromBase58(*mint)
		if err != nil {
			fail("mint", err)
		}
		ata, err := vault.VaultATA(addr, mi)
		if err != nil {
			fail("ata", err)
		}
		fmt.Println(ata.String())
	}
}

func fail(what string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
	os.Exit(1)
}

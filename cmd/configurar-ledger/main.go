package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"vaiJunto/blockchain"
)

func main() {
	var emissores []string
	var publica string
	for _, id := range []string{"empresaA", "empresaB", "empresaC"} {
		c, e := blockchain.CarregarOuCriarCarteiraArquivo(filepath.Join("dados", id, "validador_key.json"))
		if e != nil {
			panic(e)
		}
		emissores = append(emissores, c.Endereco)
		if id == "empresaA" {
			publica = c.ChavePublica
		}
	}
	fmt.Printf("VJ_LEDGER_LIDER_ID=empresaA\nVJ_LEDGER_LIDER_PUBLICA=%s\nVJ_LEDGER_LIDER_URL=http://empresaA:9081\nVJ_EMISSORES=%s\n", publica, strings.Join(emissores, ","))
	_ = os.Stdout.Sync()
}

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"vaiJunto/blockchain"
)

func main() {
	modo := flag.String("modo", "servidor", "servidor, carteira, transferir ou emitir")
	id := flag.String("id", "empresaA", "empresa")
	porta := flag.String("http", ":9181", "porta")
	lider := flag.String("lider-url", "http://localhost:9181", "URL do líder")
	pub := flag.String("lider-publica", "", "chave pública confiável do líder")
	peers := flag.String("peers", "", "URLs dos seguidores")
	arquivo := flag.String("cadeia", "blockchain.json", "arquivo da cadeia")
	key := flag.String("carteira", "carteira.json", "arquivo da carteira")
	dest := flag.String("destino", "", "endereço destinatário")
	valor := flag.Uint64("valor", 0, "tokens inteiros")
	nonce := flag.Uint64("nonce", 1, "nonce da transação")
	flag.Parse()
	if *modo != "servidor" {
		c, e := blockchain.CarregarOuCriarCarteiraArquivo(*key)
		if e != nil {
			log.Fatal(e)
		}
		if *modo == "carteira" {
			json.NewEncoder(os.Stdout).Encode(c.SomentePublica())
			return
		}
		var tx blockchain.Transacao
		switch *modo {
		case "transferir":
			tx, e = blockchain.NovaTransacaoTransferencia(c, *dest, *valor, *nonce, time.Now().Unix())
		case "emitir":
			tx, e = blockchain.NovaTransacaoEmissao(c, *dest, *valor, *nonce, time.Now().Unix())
		default:
			log.Fatal("modo inválido")
		}
		if e != nil {
			log.Fatal(e)
		}
		json.NewEncoder(os.Stdout).Encode(tx)
		return
	}
	var c *blockchain.Carteira
	var err error
	if *id == "empresaA" {
		c, err = blockchain.CarregarOuCriarCarteiraArquivo(*key)
		if err != nil {
			log.Fatal(err)
		}
		if *pub == "" {
			*pub = c.ChavePublica
		}
	}
	end, err := blockchain.DerivarEndereco(*pub)
	if err != nil {
		log.Fatal(err)
	}
	bc, err := blockchain.NovaBlockchainComEstado(*arquivo, end)
	if err != nil {
		log.Fatal(err)
	}
	r := &blockchain.RedeLedger{Cadeia: bc, ID: *id, LiderID: "empresaA", LiderURL: *lider, LiderPublica: *pub, Carteira: c, Peers: strings.Split(*peers, ",")}
	h, err := r.Handler()
	if err != nil {
		log.Fatal(err)
	}
	if *id != "empresaA" {
		go func() {
			for {
				if err := r.Sincronizar(); err != nil {
					log.Print(err)
				}
				time.Sleep(3 * time.Second)
			}
		}()
	}
	fmt.Println("Ledger", *id, *porta)
	log.Fatal((&http.Server{Addr: *porta, Handler: h, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
}

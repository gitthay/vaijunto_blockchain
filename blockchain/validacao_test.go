package blockchain

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// Testa três nós independentes, sem Docker e sem modificar dados reais.
func TestValidacaoDistribuida(t *testing.T) {
	ids := []string{"empresaA", "empresaB", "empresaC"}
	var validadores, usuarios [3]*Carteira
	var caminhos [3]string
	var nos [3]*RedeLedger
	var servidores [3]*httptest.Server
	var emissores []string
	for i := range ids {
		var err error
		validadores[i], err = GerarCarteira()
		if err != nil {
			t.Fatal(err)
		}
		usuarios[i], err = GerarCarteira()
		if err != nil {
			t.Fatal(err)
		}
		emissores = append(emissores, validadores[i].Endereco)
		caminhos[i] = filepath.Join(t.TempDir(), "blockchain.json")
	}
	iniciar := func(i int) {
		bc, err := NovaBlockchainComEstado(caminhos[i], emissores...)
		if err != nil {
			t.Fatal(err)
		}
		r := &RedeLedger{Cadeia: bc, ID: ids[i], LiderID: ids[0], LiderPublica: validadores[0].ChavePublica, Carteira: validadores[i]}
		h, err := r.Handler()
		if err != nil {
			t.Fatal(err)
		}
		s := httptest.NewServer(h)
		t.Cleanup(s.Close)
		nos[i], servidores[i] = r, s
		if i != 0 {
			r.LiderURL = servidores[0].URL
		}
	}
	for i := range ids {
		iniciar(i)
	}
	nos[0].Peers = []string{servidores[1].URL, servidores[2].URL}
	transferir := func(origem, destino int, valor, nonce uint64) Transacao {
		tx, err := NovaTransacaoTransferencia(usuarios[origem], usuarios[destino].Endereco, valor, nonce, time.Now().Unix())
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
	enviar := func(no int, tx Transacao, replicas int) {
		t.Helper()
		r, err := nos[no].Enviar(tx)
		if err != nil || !r.Confirmada || r.Replicas != replicas {
			t.Fatalf("envio %s: %+v, %v", ids[no], r, err)
		}
	}
	conferir := func(saldos, nonces [3]uint64) {
		t.Helper()
		for i, no := range nos {
			if err := ValidarCadeia(no.Cadeia.ObterBlocos()); err != nil {
				t.Fatal(err)
			}
			if no.Cadeia.Altura() != nos[0].Cadeia.Altura() || no.Cadeia.UltimoBloco().Hash != nos[0].Cadeia.UltimoBloco().Hash {
				t.Fatalf("cadeia divergente em %s", ids[i])
			}
			e := no.Cadeia.Estado()
			if e.TotalEmitido() != 300 {
				t.Fatalf("oferta incorreta em %s", ids[i])
			}
			for j, u := range usuarios {
				if e.Saldo(u.Endereco) != saldos[j] || e.ProximoNonce(u.Endereco) != nonces[j] {
					t.Fatalf("%s usuário %d: saldo %d, nonce %d; esperado %d, %d", ids[i], j, e.Saldo(u.Endereco), e.ProximoNonce(u.Endereco), saldos[j], nonces[j])
				}
			}
		}
	}
	for i := range ids {
		tx, err := NovaTransacaoEmissao(validadores[i], usuarios[i].Endereco, 100, 1, time.Now().Unix())
		if err != nil {
			t.Fatal(err)
		}
		enviar(i, tx, 3)
	}
	conferir([3]uint64{100, 100, 100}, [3]uint64{1, 1, 1})
	t.Log("recargas nos três servidores")
	tx := transferir(0, 1, 40, 1)
	enviar(0, tx, 3)
	enviar(1, transferir(1, 2, 20, 1), 3)
	enviar(2, transferir(2, 0, 10, 1), 3)
	conferir([3]uint64{70, 120, 110}, [3]uint64{2, 2, 2})
	t.Log("transferências A -> B -> C -> A")
	altura := nos[0].Cadeia.Altura()
	enviar(2, tx, 3)
	if nos[0].Cadeia.Altura() != altura {
		t.Fatal("replay criou bloco")
	}
	if _, err := nos[1].Enviar(transferir(0, 1, 71, 2)); err == nil {
		t.Fatal("saldo insuficiente aceito")
	}
	conferir([3]uint64{70, 120, 110}, [3]uint64{2, 2, 2})
	t.Log("replay e saldo insuficiente")
	txs := [2]Transacao{transferir(0, 1, 60, 2), transferir(0, 2, 60, 2)}
	type resultado struct {
		i   int
		r   ResultadoTransferencia
		err error
	}
	resultados := make(chan resultado, 2)
	partida := make(chan struct{})
	for i := range txs {
		go func(i int) { <-partida; r, err := nos[i+1].Enviar(txs[i]); resultados <- resultado{i, r, err} }(i)
	}
	close(partida)
	vencedor := -1
	for range txs {
		r := <-resultados
		if r.err == nil {
			if !r.r.Confirmada || vencedor != -1 {
				t.Fatal("resultado concorrente inválido")
			}
			vencedor = r.i
		}
	}
	if vencedor < 0 || nos[0].Cadeia.Altura() != altura+1 {
		t.Fatal("esperava exatamente um gasto confirmado")
	}
	saldos := [3]uint64{10, 120, 110}
	saldos[vencedor+1] += 60
	conferir(saldos, [3]uint64{3, 2, 2})
	t.Log("gasto duplo concorrente bloqueado")
	servidores[2].Close()
	enviar(1, transferir(0, 1, 5, 3), 2)
	if nos[2].Cadeia.Altura() == nos[0].Cadeia.Altura() {
		t.Fatal("nó offline deveria estar atrasado")
	}
	iniciar(2)
	nos[0].Peers = []string{servidores[1].URL, servidores[2].URL}
	if err := nos[2].Sincronizar(); err != nil {
		t.Fatal(err)
	}
	saldos[0] -= 5
	saldos[1] += 5
	conferir(saldos, [3]uint64{4, 2, 2})
	t.Log("recuperação de C após reabrir dados persistidos")
}

package blockchain

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestTransferenciaEntreTresServidores(t *testing.T) {
	lider, _ := GerarCarteira()
	emissorB, _ := GerarCarteira()
	alice, _ := GerarCarteira()
	bob, _ := GerarCarteira()
	criar := func(id string) *RedeLedger {
		bc, err := NovaBlockchainComEstado(filepath.Join(t.TempDir(), "blockchain.json"), lider.Endereco, emissorB.Endereco)
		if err != nil {
			t.Fatal(err)
		}
		return &RedeLedger{Cadeia: bc, ID: id, LiderID: "empresaA", LiderPublica: lider.ChavePublica}
	}
	a, b, c := criar("empresaA"), criar("empresaB"), criar("empresaC")
	a.Carteira = lider
	iniciar := func(r *RedeLedger) *httptest.Server {
		h, err := r.Handler()
		if err != nil {
			t.Fatal(err)
		}
		s := httptest.NewServer(h)
		t.Cleanup(s.Close)
		return s
	}
	sa, sb, sc := iniciar(a), iniciar(b), iniciar(c)
	a.Peers = []string{sb.URL, sc.URL}
	b.LiderURL = sa.URL
	c.LiderURL = sa.URL
	emitir, _ := NovaTransacaoEmissao(emissorB, alice.Endereco, 100, 1, time.Now().Unix())
	if res, err := b.enviar(emitir); err != nil || !res.Confirmada || res.Replicas != 3 {
		t.Fatalf("emissão: %+v %v", res, err)
	}
	tx, _ := NovaTransacaoTransferencia(alice, bob.Endereco, 50, 1, time.Now().Unix())
	if res, err := c.enviar(tx); err != nil || !res.Confirmada || res.Replicas != 3 {
		t.Fatalf("transferência: %+v %v", res, err)
	}
	for _, r := range []*RedeLedger{a, b, c} {
		if r.Cadeia.Estado().Saldo(alice.Endereco) != 50 || r.Cadeia.Estado().Saldo(bob.Endereco) != 50 || r.Cadeia.UltimoBloco().Hash != a.Cadeia.UltimoBloco().Hash {
			t.Fatal("réplicas divergentes")
		}
	}
	if _, err := b.enviar(tx); err != nil || a.Cadeia.Altura() != 3 {
		t.Fatal("reenvio não idempotente")
	}
	conflito, _ := NovaTransacaoTransferencia(alice, bob.Endereco, 1, 1, time.Now().Unix())
	if _, err := b.enviar(conflito); err == nil {
		t.Fatal("nonce duplicado aceito")
	}
	excesso, _ := NovaTransacaoTransferencia(alice, bob.Endereco, 51, 2, time.Now().Unix())
	if _, err := c.enviar(excesso); err == nil {
		t.Fatal("saldo insuficiente aceito")
	}
	sc.Close()
	tx2, _ := NovaTransacaoTransferencia(alice, bob.Endereco, 10, 2, time.Now().Unix())
	if res, err := b.enviar(tx2); err != nil || !res.Confirmada || res.Replicas != 2 {
		t.Fatalf("réplica offline: %+v %v", res, err)
	}
	if err := c.Sincronizar(); err != nil {
		t.Fatal(err)
	}
	if c.Cadeia.Estado().Saldo(bob.Endereco) != 60 {
		t.Fatal("sincronização falhou")
	}
}

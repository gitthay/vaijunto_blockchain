package blockchain

import (
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

func TestEscrowDistribuido(t *testing.T) {
	lider, p, m1 := carteiras(t)
	m2 := GerarCarteiraDeterministica("motorista2")
	caminhos := []string{filepath.Join(t.TempDir(), "a.json"), filepath.Join(t.TempDir(), "b.json"), filepath.Join(t.TempDir(), "c.json")}
	var redes []*RedeLedger
	var servidores []*httptest.Server
	for i, id := range []string{"A", "B", "C"} {
		bc, err := NovaBlockchainComEstado(caminhos[i], lider.Endereco)
		if err != nil {
			t.Fatal(err)
		}
		r := &RedeLedger{Cadeia: bc, ID: id, LiderID: "A", LiderPublica: lider.ChavePublica}
		if i == 0 {
			r.Carteira = lider
		}
		h, err := r.Handler()
		if err != nil {
			t.Fatal(err)
		}
		s := httptest.NewServer(h)
		t.Cleanup(s.Close)
		redes = append(redes, r)
		servidores = append(servidores, s)
	}
	redes[0].Peers = []string{servidores[1].URL, servidores[2].URL}
	redes[1].LiderURL = servidores[0].URL
	redes[2].LiderURL = servidores[0].URL
	enviar := func(i int, tx Transacao) {
		t.Helper()
		r, err := redes[i].Enviar(tx)
		if err != nil || !r.Confirmada {
			t.Fatalf("envio: %+v %v", r, err)
		}
	}
	enviar(1, mustEmitir(t, lider, p.Endereco, 100, 1))
	payload := PayloadCriarEscrow{ReservaID: "RES-1", ValorTotal: 90, Trechos: []PagamentoTrecho{{"T1", m1.Endereco, 20}, {"T2", m2.Endereco, 30}, {"T3", m1.Endereco, 40}}}
	criar, err := NovaTransacaoCriarEscrow(p, payload, 1, tsBase)
	if err != nil {
		t.Fatal(err)
	}
	enviar(2, criar)
	for _, r := range redes {
		e := r.Cadeia.Estado()
		if e.Saldo(p.Endereco) != 10 || e.SaldoBloqueado(p.Endereco) != 90 || e.Saldo(m1.Endereco) != 0 || e.Saldo(m2.Endereco) != 0 || e.TotalEmitido() != 100 {
			t.Fatal("bloqueio incorreto")
		}
	}
	if _, err := redes[1].Enviar(mustTransferir(t, p, m1.Endereco, 11, 2)); err == nil {
		t.Fatal("gastou tokens bloqueados")
	}
	invasor, _ := NovaTransacaoConcluirViagem(m1, "RES-1", 1, tsBase)
	if _, err := redes[1].Enviar(invasor); err == nil {
		t.Fatal("conclusão não autorizada")
	}
	concluir, err := NovaTransacaoConcluirViagem(p, "RES-1", 2, tsBase)
	if err != nil {
		t.Fatal(err)
	}
	enviar(1, concluir)
	enviar(2, concluir)
	repetida, _ := NovaTransacaoConcluirViagem(p, "RES-1", 3, tsBase)
	if _, err := redes[2].Enviar(repetida); err == nil {
		t.Fatal("pagamento repetido")
	}
	for i, r := range redes {
		e := r.Cadeia.Estado()
		if e.Saldo(p.Endereco) != 10 || e.SaldoBloqueado(p.Endereco) != 0 || e.Saldo(m1.Endereco) != 60 || e.Saldo(m2.Endereco) != 30 || e.TotalEmitido() != 100 || e.NonceAtual(p.Endereco) != 2 || e.Raiz() != redes[0].Cadeia.Estado().Raiz() {
			t.Fatal("split ou réplica incorretos")
		}
		bc, err := NovaBlockchainComEstado(caminhos[i], lider.Endereco)
		if err != nil || bc.Estado().Raiz() != e.Raiz() {
			t.Fatalf("reconstrução: %v", err)
		}
		x, ok := bc.Estado().Escrow("RES-1")
		if !ok || !x.Concluido {
			t.Fatal("escrow não persistido")
		}
	}
}
func TestEscrowRollback(t *testing.T) {
	em, p, m := carteiras(t)
	e := NovoEstado(em.Endereco)
	if err := e.AplicarTransacao(mustEmitir(t, em, p.Endereco, 100, 1)); err != nil {
		t.Fatal(err)
	}
	payload := PayloadCriarEscrow{ReservaID: "R", ValorTotal: 90, Trechos: []PagamentoTrecho{{"T1", m.Endereco, 90}}}
	criar, _ := NovaTransacaoCriarEscrow(p, payload, 1, tsBase)
	concluir, _ := NovaTransacaoConcluirViagem(p, "R", 2, tsBase)
	raiz := e.Raiz()
	excesso := mustTransferir(t, p, m.Endereco, 11, 3)
	if err := e.AplicarBloco(Bloco{Transacoes: []Transacao{criar, concluir, excesso}}); !errors.Is(err, ErrSaldoInsuficiente) {
		t.Fatalf("esperava saldo insuficiente: %v", err)
	}
	if e.Raiz() != raiz || e.Saldo(m.Endereco) != 0 || e.NonceAtual(p.Endereco) != 0 {
		t.Fatal("rollback parcial")
	}
	if _, ok := e.Escrow("R"); ok {
		t.Fatal("escrow persistido após falha")
	}
	payload.ValorTotal = 91
	invalida, _ := NovaTransacaoCriarEscrow(p, payload, 1, tsBase)
	if err := e.AplicarTransacao(invalida); !errors.Is(err, ErrPayloadInvalido) {
		t.Fatalf("total incorreto: %v", err)
	}
	if err := e.AplicarTransacao(criar); err != nil {
		t.Fatal(err)
	}
	duplicada, _ := NovaTransacaoCriarEscrow(p, PayloadCriarEscrow{ReservaID: "R", ValorTotal: 1, Trechos: []PagamentoTrecho{{"T1", m.Endereco, 1}}}, 2, tsBase)
	if err := e.AplicarTransacao(duplicada); !errors.Is(err, ErrEscrowExistente) {
		t.Fatalf("escrow duplicado: %v", err)
	}
	x, _ := e.Escrow("R")
	x.Trechos[0].Valor = 1
	x2, _ := e.Escrow("R")
	if x2.Trechos[0].Valor != 90 {
		t.Fatal("estado exposto")
	}
}

func TestEscrowCancelamentoPersistente(t *testing.T) {
	em, p, m := carteiras(t)
	path := filepath.Join(t.TempDir(), "blockchain.json")
	bc, err := NovaBlockchainComEstado(path, em.Endereco)
	if err != nil {
		t.Fatal(err)
	}
	appendTx := func(tx Transacao) error {
		b, e := NovoBloco(bc.UltimoBloco(), []Transacao{tx}, "empresaA", em, tsBase)
		if e != nil {
			return e
		}
		return bc.AdicionarBloco(b)
	}
	if err = appendTx(mustEmitir(t, em, p.Endereco, 100, 1)); err != nil {
		t.Fatal(err)
	}
	criar, _ := NovaTransacaoCriarEscrow(p, PayloadCriarEscrow{ReservaID: "R", ValorTotal: 60, Trechos: []PagamentoTrecho{{"T", m.Endereco, 60}}}, 1, tsBase)
	if err = appendTx(criar); err != nil {
		t.Fatal(err)
	}
	bc, err = NovaBlockchainComEstado(path, em.Endereco)
	if err != nil {
		t.Fatal(err)
	}
	if bc.Estado().SaldoBloqueado(p.Endereco) != 60 {
		t.Fatal("bloqueio perdido")
	}
	invasor, _ := NovaTransacaoCancelarEscrow(m, "R", 1, tsBase)
	if err = appendTx(invasor); !errors.Is(err, ErrEscrowNaoAutorizado) {
		t.Fatalf("invasor: %v", err)
	}
	cancelar, _ := NovaTransacaoCancelarEscrow(p, "R", 2, tsBase)
	if err = appendTx(cancelar); err != nil {
		t.Fatal(err)
	}
	bc, err = NovaBlockchainComEstado(path, em.Endereco)
	if err != nil {
		t.Fatal(err)
	}
	x, ok := bc.Estado().Escrow("R")
	if !ok || !x.Cancelado || x.Concluido || bc.Estado().Saldo(p.Endereco) != 100 || bc.Estado().SaldoBloqueado(p.Endereco) != 0 || bc.Estado().Saldo(m.Endereco) != 0 {
		t.Fatal("estorno incorreto")
	}
	repetido, _ := NovaTransacaoCancelarEscrow(p, "R", 3, tsBase)
	concluir, _ := NovaTransacaoConcluirViagem(p, "R", 3, tsBase)
	for _, tx := range []Transacao{repetido, concluir} {
		if err = appendTx(tx); !errors.Is(err, ErrEscrowCancelado) {
			t.Fatalf("estado terminal: %v", err)
		}
	}
	if bc.Estado().NonceAtual(p.Endereco) != 2 || bc.Estado().TotalEmitido() != 100 {
		t.Fatal("estado alterado")
	}
}

func TestEscrowCorridaCancelamentoConclusao(t *testing.T) {
	em, p, m := carteiras(t)
	e := NovoEstado(em.Endereco)
	if err := e.AplicarTransacao(mustEmitir(t, em, p.Endereco, 100, 1)); err != nil {
		t.Fatal(err)
	}
	criar, _ := NovaTransacaoCriarEscrow(p, PayloadCriarEscrow{ReservaID: "R", ValorTotal: 60, Trechos: []PagamentoTrecho{{"T", m.Endereco, 60}}}, 1, tsBase)
	if err := e.AplicarTransacao(criar); err != nil {
		t.Fatal(err)
	}
	a, _ := NovaTransacaoCancelarEscrow(p, "R", 2, tsBase)
	b, _ := NovaTransacaoConcluirViagem(p, "R", 2, tsBase)
	var wg sync.WaitGroup
	result := make(chan error, 2)
	for _, tx := range []Transacao{a, b} {
		wg.Add(1)
		go func(tx Transacao) { defer wg.Done(); result <- e.AplicarTransacao(tx) }(tx)
	}
	wg.Wait()
	close(result)
	sucesso := 0
	for err := range result {
		if err == nil {
			sucesso++
		}
	}
	x, _ := e.Escrow("R")
	if sucesso != 1 || x.Cancelado == x.Concluido || e.Saldo(p.Endereco)+e.Saldo(m.Endereco) != 100 || e.SaldoBloqueado(p.Endereco) != 0 {
		t.Fatal("decisões terminais conflitantes")
	}
}

func TestEscrowValidacaoEEstadosTerminais(t *testing.T) {
	em, p, m := carteiras(t)
	e := NovoEstado(em.Endereco)
	if err := e.AplicarTransacao(mustEmitir(t, em, p.Endereco, 100, 1)); err != nil {
		t.Fatal(err)
	}
	casos := []struct {
		nome     string
		payload  PayloadCriarEscrow
		esperado error
	}{
		{"saldo insuficiente", PayloadCriarEscrow{"R", []PagamentoTrecho{{"T", m.Endereco, 101}}, 101}, ErrSaldoInsuficiente},
		{"trecho duplicado", PayloadCriarEscrow{"R", []PagamentoTrecho{{"T", m.Endereco, 20}, {"T", m.Endereco, 20}}, 40}, ErrPayloadInvalido},
		{"autopagamento", PayloadCriarEscrow{"R", []PagamentoTrecho{{"T", p.Endereco, 20}}, 20}, ErrPayloadInvalido},
		{"sem trechos", PayloadCriarEscrow{"R", nil, 20}, ErrPayloadInvalido},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			raiz := e.Raiz()
			tx, err := NovaTransacaoCriarEscrow(p, c.payload, 1, tsBase)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.AplicarTransacao(tx); !errors.Is(err, c.esperado) {
				t.Fatalf("esperado %v; obtido %v", c.esperado, err)
			}
			if e.Raiz() != raiz || e.NonceAtual(p.Endereco) != 0 {
				t.Fatal("rejeição alterou estado")
			}
		})
	}
	inexistente, err := NovaTransacaoConcluirViagem(p, "inexistente", 1, tsBase)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.AplicarTransacao(inexistente); !errors.Is(err, ErrEscrowInexistente) {
		t.Fatalf("escrow inexistente: %v", err)
	}
	criar, err := NovaTransacaoCriarEscrow(p, PayloadCriarEscrow{"R", []PagamentoTrecho{{"T", m.Endereco, 60}}, 60}, 1, tsBase)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.AplicarTransacao(criar); err != nil {
		t.Fatal(err)
	}
	concluir, err := NovaTransacaoConcluirViagem(p, "R", 2, tsBase)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.AplicarTransacao(concluir); err != nil {
		t.Fatal(err)
	}
	raiz := e.Raiz()
	cancelar, err := NovaTransacaoCancelarEscrow(p, "R", 3, tsBase)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.AplicarTransacao(cancelar); !errors.Is(err, ErrEscrowConcluido) {
		t.Fatalf("cancelamento após pagamento: %v", err)
	}
	if err = e.AplicarTransacao(concluir); !errors.Is(err, ErrNonceInvalido) {
		t.Fatalf("replay: %v", err)
	}
	if e.Raiz() != raiz || e.Saldo(p.Endereco) != 40 || e.Saldo(m.Endereco) != 60 || e.SaldoBloqueado(p.Endereco) != 0 {
		t.Fatal("estado terminal alterado")
	}
}

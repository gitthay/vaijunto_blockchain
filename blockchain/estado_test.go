package blockchain

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

const tsBase = int64(1767226000)

func carteiras(t *testing.T) (emissor, alice, bob *Carteira) {
	t.Helper()
	return GerarCarteiraDeterministica("emissor"),
		GerarCarteiraDeterministica("alice"),
		GerarCarteiraDeterministica("bob")
}

func mustEmitir(t *testing.T, em *Carteira, dest string, valor, nonce uint64) Transacao {
	t.Helper()
	tx, err := NovaTransacaoEmissao(em, dest, valor, nonce, tsBase)
	if err != nil {
		t.Fatalf("erro ao criar emissão: %v", err)
	}
	return tx
}

func mustTransferir(t *testing.T, o *Carteira, dest string, valor, nonce uint64) Transacao {
	t.Helper()
	tx, err := NovaTransacaoTransferencia(o, dest, valor, nonce, tsBase)
	if err != nil {
		t.Fatalf("erro ao criar transferência: %v", err)
	}
	return tx
}

func TestEmissaoEControleDeSaldo(t *testing.T) {
	em, alice, bob := carteiras(t)
	e := NovoEstado(em.Endereco)

	if err := e.AplicarTransacao(mustEmitir(t, em, alice.Endereco, 100, 1)); err != nil {
		t.Fatalf("emissão válida rejeitada: %v", err)
	}
	if e.Saldo(alice.Endereco) != 100 || e.TotalEmitido() != 100 {
		t.Fatalf("saldo/oferta incorretos: %d / %d", e.Saldo(alice.Endereco), e.TotalEmitido())
	}

	if err := e.AplicarTransacao(mustTransferir(t, alice, bob.Endereco, 30, 1)); err != nil {
		t.Fatalf("transferência válida rejeitada: %v", err)
	}
	if e.Saldo(alice.Endereco) != 70 || e.Saldo(bob.Endereco) != 30 {
		t.Fatalf("saldos após transferência incorretos: alice=%d bob=%d", e.Saldo(alice.Endereco), e.Saldo(bob.Endereco))
	}
	if e.TotalEmitido() != 100 {
		t.Fatalf("transferência não pode alterar a oferta total")
	}
}

func TestRejeitaSaldoInsuficiente(t *testing.T) {
	em, alice, bob := carteiras(t)
	e := NovoEstado(em.Endereco)
	_ = e.AplicarTransacao(mustEmitir(t, em, alice.Endereco, 50, 1))
	raiz := e.Raiz()

	err := e.AplicarTransacao(mustTransferir(t, alice, bob.Endereco, 51, 1))
	if !errors.Is(err, ErrSaldoInsuficiente) {
		t.Fatalf("esperava ErrSaldoInsuficiente, obtido: %v", err)
	}
	if e.Raiz() != raiz || e.NonceAtual(alice.Endereco) != 0 {
		t.Fatalf("transação rejeitada não pode alterar saldos nem consumir nonce")
	}
	// Quem nunca recebeu nada também não pode gastar
	if err := e.AplicarTransacao(mustTransferir(t, bob, alice.Endereco, 1, 1)); !errors.Is(err, ErrSaldoInsuficiente) {
		t.Fatalf("saldo zero deveria ser insuficiente, obtido: %v", err)
	}
}

func TestNonceSequencialBloqueiaReplayELacunas(t *testing.T) {
	em, alice, bob := carteiras(t)
	e := NovoEstado(em.Endereco)
	_ = e.AplicarTransacao(mustEmitir(t, em, alice.Endereco, 100, 1))

	tx1 := mustTransferir(t, alice, bob.Endereco, 10, 1)
	if err := e.AplicarTransacao(tx1); err != nil {
		t.Fatalf("tx válida rejeitada: %v", err)
	}

	// Replay da mesma transação (mesmo ID/assinatura)
	if err := e.AplicarTransacao(tx1); !errors.Is(err, ErrNonceInvalido) {
		t.Fatalf("replay deveria ser rejeitado por nonce, obtido: %v", err)
	}
	// Gasto duplo: outra transação com o mesmo nonce
	if err := e.AplicarTransacao(mustTransferir(t, alice, bob.Endereco, 20, 1)); !errors.Is(err, ErrNonceInvalido) {
		t.Fatalf("nonce repetido deveria ser rejeitado, obtido: %v", err)
	}
	// Lacuna de nonce
	if err := e.AplicarTransacao(mustTransferir(t, alice, bob.Endereco, 5, 3)); !errors.Is(err, ErrNonceInvalido) {
		t.Fatalf("lacuna de nonce deveria ser rejeitada, obtido: %v", err)
	}
	// Nonce zero
	if err := e.AplicarTransacao(mustTransferir(t, alice, bob.Endereco, 5, 0)); !errors.Is(err, ErrNonceInvalido) {
		t.Fatalf("nonce 0 deveria ser rejeitado, obtido: %v", err)
	}
	// O próximo correto continua funcionando
	if err := e.AplicarTransacao(mustTransferir(t, alice, bob.Endereco, 5, 2)); err != nil {
		t.Fatalf("nonce correto rejeitado: %v", err)
	}
	if e.Saldo(alice.Endereco) != 85 || e.Saldo(bob.Endereco) != 15 || e.ProximoNonce(alice.Endereco) != 3 {
		t.Fatalf("estado final incorreto")
	}
}

func TestGastoDuplo(t *testing.T) {
	em, alice, bob := carteiras(t)
	carol := GerarCarteiraDeterministica("carol")
	e := NovoEstado(em.Endereco)
	_ = e.AplicarTransacao(mustEmitir(t, em, alice.Endereco, 100, 1))

	// Alice tenta gastar os mesmos 100 tokens para Bob e Carol em sequência.
	if err := e.AplicarTransacao(mustTransferir(t, alice, bob.Endereco, 100, 1)); err != nil {
		t.Fatal(err)
	}
	if err := e.AplicarTransacao(mustTransferir(t, alice, carol.Endereco, 100, 2)); !errors.Is(err, ErrSaldoInsuficiente) {
		t.Fatalf("segundo gasto deveria falhar por saldo, obtido: %v", err)
	}
}

func TestEmissaoSomenteDeEmissorAutorizado(t *testing.T) {
	em, alice, _ := carteiras(t)
	e := NovoEstado(em.Endereco)

	// Alice tenta se auto-emitir tokens
	err := e.AplicarTransacao(mustEmitir(t, alice, alice.Endereco, 1000, 1))
	if !errors.Is(err, ErrEmissorNaoAutorizado) {
		t.Fatalf("emissão por não autorizado deveria falhar, obtido: %v", err)
	}
	if e.Saldo(alice.Endereco) != 0 || e.TotalEmitido() != 0 {
		t.Fatalf("emissão não autorizada não pode criar saldo")
	}
}

func TestAssinaturaInvalidaERemetenteForjado(t *testing.T) {
	em, alice, bob := carteiras(t)
	e := NovoEstado(em.Endereco)
	_ = e.AplicarTransacao(mustEmitir(t, em, alice.Endereco, 100, 1))

	// Adulterar valor após assinar
	tx := mustTransferir(t, alice, bob.Endereco, 10, 1)
	tx.Payload = json.RawMessage(`{"destinatario":"` + bob.Endereco + `","valor":99}`)
	if err := e.AplicarTransacao(tx); err == nil {
		t.Fatalf("transação adulterada deveria ser rejeitada")
	}

	// Bob tenta gastar saldo de Alice declarando o endereço dela com a chave dele
	forjada := Transacao{
		Tipo: TxTransferirTokens, Remetente: alice.Endereco, ChavePublica: bob.ChavePublica,
		Nonce: 1, Timestamp: tsBase,
		Payload: json.RawMessage(`{"destinatario":"` + bob.Endereco + `","valor":100}`),
	}
	dados, _ := forjada.BytesAssinaveis()
	forjada.ID = HashSHA256Hex(dados)
	forjada.Assinatura, _ = AssinarDados(bob.ChavePrivada, dados)
	if err := e.AplicarTransacao(forjada); err == nil {
		t.Fatalf("remetente forjado deveria ser rejeitado")
	}
	if e.Saldo(alice.Endereco) != 100 {
		t.Fatalf("saldo de Alice não pode mudar")
	}
}

func TestPayloadInvalido(t *testing.T) {
	em, alice, _ := carteiras(t)
	e := NovoEstado(em.Endereco)

	casos := map[string]struct {
		dest  string
		valor uint64
		err   error
	}{
		"valor zero":          {alice.Endereco, 0, ErrValorInvalido},
		"destinatário vazio":  {"", 10, ErrPayloadInvalido},
		"endereço malformado": {"VJ-xyz", 10, ErrPayloadInvalido},
	}
	for nome, c := range casos {
		tx := mustEmitir(t, em, c.dest, c.valor, 1)
		if err := e.AplicarTransacao(tx); !errors.Is(err, c.err) {
			t.Errorf("%s: esperava %v, obtido %v", nome, c.err, err)
		}
	}

	// Autotransferência
	_ = e.AplicarTransacao(mustEmitir(t, em, alice.Endereco, 10, 1))
	if err := e.AplicarTransacao(mustTransferir(t, alice, alice.Endereco, 1, 1)); !errors.Is(err, ErrPayloadInvalido) {
		t.Errorf("autotransferência deveria ser rejeitada, obtido: %v", err)
	}

	// Tipo ainda não suportado
	tx := Transacao{Tipo: TxAvaliar, Nonce: 1, Timestamp: tsBase, Payload: json.RawMessage(`{}`)}
	_ = tx.Assinar(GerarCarteiraDeterministica("carol"))
	if err := e.AplicarTransacao(tx); !errors.Is(err, ErrTipoNaoSuportado) {
		t.Errorf("tipo não suportado deveria ser rejeitado, obtido: %v", err)
	}
}

func TestOverflowDeSaldo(t *testing.T) {
	em, alice, _ := carteiras(t)
	e := NovoEstado(em.Endereco)
	_ = e.AplicarTransacao(mustEmitir(t, em, alice.Endereco, ^uint64(0), 1))
	if err := e.AplicarTransacao(mustEmitir(t, em, alice.Endereco, 1, 2)); !errors.Is(err, ErrOverflowSaldo) {
		t.Fatalf("overflow deveria ser rejeitado, obtido: %v", err)
	}
}

func TestBlocoAtomico(t *testing.T) {
	em, alice, bob := carteiras(t)
	val := GerarCarteiraDeterministica("empresaA")
	e := NovoEstado(em.Endereco)
	g := CriarBlocoGenesis()

	// 1ª tx válida, 2ª estoura o saldo: o bloco inteiro deve ser rejeitado.
	txs := []Transacao{
		mustEmitir(t, em, alice.Endereco, 50, 1),
		mustTransferir(t, alice, bob.Endereco, 80, 1),
	}
	b, err := NovoBloco(g, txs, "empresaA", val, tsBase+1)
	if err != nil {
		t.Fatal(err)
	}
	raiz := e.Raiz()
	if err := e.AplicarBloco(b); !errors.Is(err, ErrSaldoInsuficiente) {
		t.Fatalf("esperava ErrSaldoInsuficiente, obtido: %v", err)
	}
	if e.Raiz() != raiz || e.Saldo(alice.Endereco) != 0 {
		t.Fatalf("bloco rejeitado não pode deixar efeitos parciais")
	}

	// Dentro do mesmo bloco, transações dependentes em ordem são válidas.
	txs[1] = mustTransferir(t, alice, bob.Endereco, 20, 1)
	b2, _ := NovoBloco(g, txs, "empresaA", val, tsBase+1)
	if err := e.AplicarBloco(b2); err != nil {
		t.Fatalf("bloco válido rejeitado: %v", err)
	}
	if e.Saldo(alice.Endereco) != 30 || e.Saldo(bob.Endereco) != 20 {
		t.Fatalf("saldos incorretos após bloco")
	}
}

func TestDeterminismoEReconstrucao(t *testing.T) {
	em, alice, bob := carteiras(t)
	val := GerarCarteiraDeterministica("empresaA")
	g := CriarBlocoGenesis()

	b1, _ := NovoBloco(g, []Transacao{mustEmitir(t, em, alice.Endereco, 100, 1)}, "empresaA", val, tsBase+1)
	b2, _ := NovoBloco(b1, []Transacao{
		mustTransferir(t, alice, bob.Endereco, 40, 1),
		mustTransferir(t, alice, bob.Endereco, 10, 2),
	}, "empresaA", val, tsBase+2)
	blocos := []Bloco{g, b1, b2}

	e1, err := ReconstruirEstado(blocos, em.Endereco)
	if err != nil {
		t.Fatal(err)
	}
	e2, _ := ReconstruirEstado(blocos, em.Endereco)
	if e1.Raiz() != e2.Raiz() {
		t.Fatalf("mesmos blocos devem gerar a mesma raiz de estado")
	}
	if e1.Saldo(alice.Endereco) != 50 || e1.Saldo(bob.Endereco) != 50 {
		t.Fatalf("saldos reconstruídos incorretos")
	}

	// Aplicação incremental == reconstrução
	e3 := NovoEstado(em.Endereco)
	_ = e3.AplicarBloco(b1)
	_ = e3.AplicarBloco(b2)
	if e3.Raiz() != e1.Raiz() {
		t.Fatalf("aplicação incremental difere da reconstrução")
	}

	// Ordem diferente de blocos viola o nonce e é rejeitada
	if _, err := ReconstruirEstado([]Bloco{g, b2, b1}, em.Endereco); err == nil {
		t.Fatalf("blocos fora de ordem deveriam ser rejeitados")
	}
	// Estado diferente => raiz diferente
	if e1.Raiz() == NovoEstado(em.Endereco).Raiz() {
		t.Fatalf("raízes de estados distintos não podem coincidir")
	}
}

func TestBlockchainComEstadoRejeitaEPersiste(t *testing.T) {
	em, alice, bob := carteiras(t)
	val := GerarCarteiraDeterministica("empresaA")
	caminho := filepath.Join(t.TempDir(), "empresaA", "blockchain.json")

	bc, err := NovaBlockchainComEstado(caminho, em.Endereco)
	if err != nil {
		t.Fatal(err)
	}

	b1, _ := NovoBloco(bc.UltimoBloco(), []Transacao{mustEmitir(t, em, alice.Endereco, 100, 1)}, "empresaA", val, tsBase+1)
	if err := bc.AdicionarBloco(b1); err != nil {
		t.Fatalf("bloco válido rejeitado: %v", err)
	}

	// Bloco estruturalmente válido mas com saldo insuficiente: rejeitado e cadeia intacta.
	ruim, _ := NovoBloco(bc.UltimoBloco(), []Transacao{mustTransferir(t, alice, bob.Endereco, 500, 1)}, "empresaA", val, tsBase+2)
	if err := bc.AdicionarBloco(ruim); !errors.Is(err, ErrSaldoInsuficiente) {
		t.Fatalf("esperava ErrSaldoInsuficiente, obtido: %v", err)
	}
	if bc.Altura() != 2 || bc.Estado().Saldo(alice.Endereco) != 100 {
		t.Fatalf("bloco rejeitado não pode alterar cadeia nem estado")
	}

	// Replay de bloco com transação já consumida
	replay, _ := NovoBloco(bc.UltimoBloco(), []Transacao{b1.Transacoes[0]}, "empresaA", val, tsBase+3)
	if err := bc.AdicionarBloco(replay); !errors.Is(err, ErrNonceInvalido) {
		t.Fatalf("replay de transação deveria ser rejeitado, obtido: %v", err)
	}

	// Transferência válida
	b2, _ := NovoBloco(bc.UltimoBloco(), []Transacao{mustTransferir(t, alice, bob.Endereco, 25, 1)}, "empresaA", val, tsBase+4)
	if err := bc.AdicionarBloco(b2); err != nil {
		t.Fatal(err)
	}

	// Recarga do disco reconstrói o mesmo estado
	rec, err := NovaBlockchainComEstado(caminho, em.Endereco)
	if err != nil {
		t.Fatalf("erro ao recarregar: %v", err)
	}
	if rec.Estado().Raiz() != bc.Estado().Raiz() || rec.Estado().Saldo(bob.Endereco) != 25 {
		t.Fatalf("estado recarregado difere do original")
	}
}

func TestConcorrenciaNaoGeraGastoDuplo(t *testing.T) {
	em, alice, bob := carteiras(t)
	e := NovoEstado(em.Endereco)
	_ = e.AplicarTransacao(mustEmitir(t, em, alice.Endereco, 100, 1))

	// 20 goroutines tentam gastar 100 tokens usando o mesmo nonce: só uma pode vencer.
	var wg sync.WaitGroup
	var mu sync.Mutex
	sucessos := 0
	for i := 0; i < 20; i++ {
		tx := mustTransferir(t, alice, bob.Endereco, 100, 1)
		tx.Timestamp += int64(i) // IDs distintos
		_ = tx.Assinar(alice)
		wg.Add(1)
		go func(tx Transacao) {
			defer wg.Done()
			if e.AplicarTransacao(tx) == nil {
				mu.Lock()
				sucessos++
				mu.Unlock()
			}
		}(tx)
	}
	wg.Wait()
	if sucessos != 1 || e.Saldo(bob.Endereco) != 100 || e.Saldo(alice.Endereco) != 0 {
		t.Fatalf("esperava exatamente 1 sucesso, obtido %d (alice=%d bob=%d)", sucessos, e.Saldo(alice.Endereco), e.Saldo(bob.Endereco))
	}
}

func TestEstadoJSONGeradoAoLadoDaBlockchain(t *testing.T) {
	em, alice, _ := carteiras(t)
	val := GerarCarteiraDeterministica("empresaA")
	pasta := filepath.Join(t.TempDir(), "dados", "empresaA")
	bc, err := NovaBlockchainComEstado(filepath.Join(pasta, "blockchain.json"), em.Endereco)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(bc.ArquivoEstado()) != pasta {
		t.Fatalf("estado.json deve ficar na pasta da empresa, ficou em %s", bc.ArquivoEstado())
	}

	b1, _ := NovoBloco(bc.UltimoBloco(), []Transacao{mustEmitir(t, em, alice.Endereco, 100, 1)}, "empresaA", val, tsBase+1)
	if err := bc.AdicionarBloco(b1); err != nil {
		t.Fatal(err)
	}

	dados, err := os.ReadFile(bc.ArquivoEstado())
	if err != nil {
		t.Fatalf("estado.json não foi gerado: %v", err)
	}
	var snap SnapshotEstado
	if err := json.Unmarshal(dados, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Altura != 2 || snap.HashTopo != b1.Hash || snap.Saldos[alice.Endereco] != 100 ||
		snap.Nonces[em.Endereco] != 1 || snap.RaizEstado != bc.Estado().Raiz() {
		t.Fatalf("snapshot inconsistente: %+v", snap)
	}
}

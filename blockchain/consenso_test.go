package blockchain

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

type noTeste struct {
	id       string
	carteira *Carteira
	cadeia   *Blockchain
	consenso *NoConsenso
	servidor *httptest.Server
}

// subirConsorcio4Nos sobe 4 nós reais (empresaA, empresaB, empresaC, empresaD) em diretórios
// isolados com servidores HTTP independentes e quórum BFT = 3 de 4.
func subirConsorcio4Nos(t *testing.T) ([]*noTeste, *Carteira) {
	t.Helper()
	ids := []string{"empresaA", "empresaB", "empresaC", "empresaD"}
	dir := t.TempDir()

	carteirasNos := make([]*Carteira, len(ids))
	autorizados := make(map[string]string, len(ids))
	emissores := make([]string, 0, len(ids))
	for i, id := range ids {
		c := GerarCarteiraDeterministica("validador-" + id)
		carteirasNos[i] = c
		autorizados[c.Endereco] = c.ChavePublica
		emissores = append(emissores, c.Endereco)
	}

	nos := make([]*noTeste, len(ids))
	for i, id := range ids {
		caminho := filepath.Join(dir, id, "blockchain.json")
		bc, err := NovaBlockchainComEstado(caminho, emissores...)
		if err != nil {
			t.Fatalf("erro ao criar blockchain de %s: %v", id, err)
		}
		no, err := NovoNoConsenso(ConfigConsenso{
			ValidadorID:            id,
			Carteira:               carteirasNos[i],
			Cadeia:                 bc,
			TotalValidadores:       len(ids),
			ValidadoresAutorizados: autorizados,
		})
		if err != nil {
			t.Fatalf("erro ao criar consenso de %s: %v", id, err)
		}
		mux := http.NewServeMux()
		no.RegistrarRotasHTTP(mux)
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)

		nos[i] = &noTeste{
			id:       id,
			carteira: carteirasNos[i],
			cadeia:   bc,
			consenso: no,
			servidor: srv,
		}
	}

	// Interliga cada nó com os outros 3 peers
	for i := range nos {
		var peers []string
		for j := range nos {
			if i != j {
				peers = append(peers, nos[j].servidor.URL)
			}
		}
		nos[i].consenso.AtualizarPeers(peers, len(nos))
	}

	return nos, carteirasNos[0]
}

func TestQuorumMinimoBFT(t *testing.T) {
	if q := QuorumMinimo(4); q != 3 {
		t.Fatalf("para 4 empresas o quórum BFT esperado é 3, obtido %d", q)
	}
	if q := QuorumMinimo(3); q != 2 {
		t.Fatalf("para 3 empresas o quórum esperado é 2, obtido %d", q)
	}
}

func TestConsenso4NosProducaoEConfirmacao(t *testing.T) {
	nos, emissorA := subirConsorcio4Nos(t)
	maria := GerarCarteiraDeterministica("maria")
	carlos := GerarCarteiraDeterministica("carlos")

	txEmissao, err := NovaTransacaoEmissao(emissorA, maria.Endereco, 200, 1, 1767226000)
	if err != nil {
		t.Fatal(err)
	}
	txTransf, err := NovaTransacaoTransferencia(maria, carlos.Endereco, 75, 1, 1767226001)
	if err != nil {
		t.Fatal(err)
	}

	bloco, err := nos[0].consenso.ProporEConfirmarBloco([]Transacao{txEmissao, txTransf})
	if err != nil {
		t.Fatalf("proposta válida deveria atingir quórum nos 4 nós: %v", err)
	}
	if len(bloco.Votos) != 4 {
		t.Fatalf("esperava 4 votos assinados, obtido %d", len(bloco.Votos))
	}

	raizEsperada := nos[0].cadeia.Estado().Raiz()
	for _, n := range nos {
		if n.cadeia.Altura() != 2 {
			t.Fatalf("%s deveria ter altura 2, obteve %d", n.id, n.cadeia.Altura())
		}
		if n.cadeia.UltimoBloco().Hash != bloco.Hash {
			t.Fatalf("%s possui hash divergente", n.id)
		}
		if n.cadeia.Estado().Raiz() != raizEsperada {
			t.Fatalf("%s possui raiz de estado divergente", n.id)
		}
		if n.cadeia.Estado().Saldo(maria.Endereco) != 125 || n.cadeia.Estado().Saldo(carlos.Endereco) != 75 {
			t.Fatalf("%s possui saldos incorretos", n.id)
		}
	}
}

func TestConsensoToleranciaAQuedaDe1NoESincronizacaoPosQueda(t *testing.T) {
	nos, emissorA := subirConsorcio4Nos(t)
	maria := GerarCarteiraDeterministica("maria")

	// Derruba empresaD (1 dos 4 nós fica offline)
	nos[3].servidor.Close()

	// Com 3 de 4 nós ativos (quórum = 3), a rede DEVE continuar operando normalmente
	tx1, _ := NovaTransacaoEmissao(emissorA, maria.Endereco, 150, 1, 1767226100)
	bloco1, err := nos[0].consenso.ProporEConfirmarBloco([]Transacao{tx1})
	if err != nil {
		t.Fatalf("rede com 3/4 nós online deveria atingir quórum: %v", err)
	}
	if len(bloco1.Votos) != 3 {
		t.Fatalf("esperava 3 votos (com empresaD offline), obtido %d", len(bloco1.Votos))
	}

	// EmpresaD ainda está no gênesis (altura 1) enquanto A, B e C avançaram para altura 2
	if nos[3].cadeia.Altura() != 1 {
		t.Fatalf("empresaD offline deveria continuar na altura 1, obteve %d", nos[3].cadeia.Altura())
	}

	// EmpresaD volta e sincroniza automaticamente com os peers saudáveis
	atualizou, err := nos[3].consenso.SincronizarComPeers()
	if err != nil {
		t.Fatalf("erro na sincronização pós-queda da empresaD: %v", err)
	}
	if !atualizou {
		t.Fatalf("empresaD deveria ter atualizado sua cadeia ao voltar online")
	}
	if nos[3].cadeia.Altura() != 2 || nos[3].cadeia.UltimoBloco().Hash != bloco1.Hash {
		t.Fatalf("empresaD não convergiu para o topo do consórcio após sincronização")
	}
	if nos[3].cadeia.Estado().Saldo(maria.Endereco) != 150 {
		t.Fatalf("saldo reconstruído na empresaD incorreto: %d", nos[3].cadeia.Estado().Saldo(maria.Endereco))
	}
}

func TestConsensoRejeitaSemQuorum(t *testing.T) {
	nos, emissorA := subirConsorcio4Nos(t)
	maria := GerarCarteiraDeterministica("maria")

	// Derruba 2 dos 4 nós (empresaC e empresaD) -> restam apenas 2 online (< quórum 3)
	nos[2].servidor.Close()
	nos[3].servidor.Close()

	tx1, _ := NovaTransacaoEmissao(emissorA, maria.Endereco, 50, 1, 1767226200)
	if _, err := nos[0].consenso.ProporEConfirmarBloco([]Transacao{tx1}); !errors.Is(err, ErrQuorumNaoAtingido) {
		t.Fatalf("com apenas 2/4 nós online deveria falhar com ErrQuorumNaoAtingido, obtido: %v", err)
	}
	if nos[0].cadeia.Altura() != 1 {
		t.Fatalf("bloco sem quórum não pode ser gravado na cadeia")
	}
}

func TestConsensoRejeitaNoAdulteradoAutomaticamente(t *testing.T) {
	nos, emissorA := subirConsorcio4Nos(t)
	maria := GerarCarteiraDeterministica("maria")
	carlos := GerarCarteiraDeterministica("carlos")

	// 1. Confirma bloco legítimo #1 nos 4 nós
	tx1, _ := NovaTransacaoEmissao(emissorA, maria.Endereco, 100, 1, 1767226300)
	if _, err := nos[0].consenso.ProporEConfirmarBloco([]Transacao{tx1}); err != nil {
		t.Fatalf("bloco #1 legítimo falhou: %v", err)
	}

	// 2. Simula empresaD adulterando o próprio histórico para tentar forjar uma cadeia "mais longa"
	// com 2 blocos falsos assinados apenas por ela (sem quórum dos outros 3 validadores)
	txFalsa1, _ := NovaTransacaoTransferencia(maria, carlos.Endereco, 100, 1, 1767226301)
	blocoFalso1, _ := NovoBloco(nos[3].cadeia.UltimoBloco(), []Transacao{txFalsa1}, "empresaD", nos[3].carteira, 1767226302)
	txFalsa2, _ := NovaTransacaoTransferencia(carlos, maria.Endereco, 10, 1, 1767226303)
	blocoFalso2, _ := NovoBloco(blocoFalso1, []Transacao{txFalsa2}, "empresaD", nos[3].carteira, 1767226304)

	// Injeta diretamente na cadeia local da empresaD (como se o tutor adulterasse o nó)
	if err := nos[3].cadeia.AdicionarBloco(blocoFalso1); err != nil {
		t.Fatal(err)
	}
	if err := nos[3].cadeia.AdicionarBloco(blocoFalso2); err != nil {
		t.Fatal(err)
	}
	if nos[3].cadeia.Altura() != 4 {
		t.Fatalf("setup do nó malicioso deveria ter altura 4")
	}

	// 3. EmpresaA sincroniza com todos os peers (inclusive a empresaD adulterada que tem altura 4 > 2):
	// DEVE rejeitar automaticamente a cadeia da empresaD por falta de quórum!
	atualizou, err := nos[0].consenso.SincronizarComPeers()
	if err != nil {
		t.Fatalf("sincronização não deveria retornar erro fatal ao ignorar peer adulterado: %v", err)
	}
	if atualizou || nos[0].cadeia.Altura() != 2 {
		t.Fatalf("empresaA NÃO pode aceitar cadeia adulterada da empresaD (altura=%d)", nos[0].cadeia.Altura())
	}

	// 4. Mesmo com empresaD divergente/adulterada, os 3 nós honestos (A, B, C) continuam operando!
	txLegitima2, _ := NovaTransacaoTransferencia(maria, carlos.Endereco, 40, 1, 1767226310)
	bloco2, err := nos[0].consenso.ProporEConfirmarBloco([]Transacao{txLegitima2})
	if err != nil {
		t.Fatalf("os 3 nós honestos deveriam continuar confirmando blocos mesmo com empresaD adulterada: %v", err)
	}
	if len(bloco2.Votos) != 3 {
		t.Fatalf("esperava 3 votos dos nós honestos, obtido %d", len(bloco2.Votos))
	}

	// 5. Além disso, se empresaD tentar enviar um bloco com transação adulterada para empresaA avaliar,
	// AvaliarProposta rejeita imediatamente!
	txAdulterada := txLegitima2
	txAdulterada.Payload = json.RawMessage(`{"destinatario":"` + carlos.Endereco + `","valor":9999}`)
	blocoAdulterado := bloco2
	blocoAdulterado.Transacoes = []Transacao{txAdulterada}
	if _, err := nos[0].consenso.AvaliarProposta(blocoAdulterado); err == nil {
		t.Fatalf("AvaliarProposta deveria rejeitar bloco com transação adulterada")
	}
}

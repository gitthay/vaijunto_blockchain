package blockchain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBlocoGenesisDeterministico(t *testing.T) {
	g1 := CriarBlocoGenesis()
	g2 := CriarBlocoGenesis()

	if g1.Hash == "" || g1.Hash != g2.Hash {
		t.Fatalf("bloco gênesis deveria ser determinístico entre nós: %s != %s", g1.Hash, g2.Hash)
	}
	if err := g1.ValidarEstrutura(nil); err != nil {
		t.Fatalf("bloco gênesis válido foi rejeitado: %v", err)
	}
}

func TestMerkleRootETransacaoAssinada(t *testing.T) {
	usuario, err := GerarCarteira()
	if err != nil {
		t.Fatalf("erro ao gerar carteira: %v", err)
	}

	if raizVazia := CalcularMerkleRoot(nil); raizVazia == "" {
		t.Fatalf("Merkle Root vazia não pode ser string vazia")
	}

	var txs []Transacao
	for i := uint64(1); i <= 3; i++ {
		tx := Transacao{
			Tipo:      TxEmitirTokens,
			Nonce:     i,
			Timestamp: time.Now().Unix(),
			Payload:   json.RawMessage(`{"valor":100}`),
		}
		if err := tx.Assinar(usuario); err != nil {
			t.Fatalf("erro ao assinar tx #%d: %v", i, err)
		}
		if err := tx.ValidarAssinatura(); err != nil {
			t.Fatalf("tx #%d válida foi rejeitada: %v", i, err)
		}
		txs = append(txs, tx)
	}

	raiz1 := CalcularMerkleRoot(txs)
	raiz2 := CalcularMerkleRoot(txs)
	if raiz1 != raiz2 {
		t.Fatalf("Merkle Root deve ser determinística: %s != %s", raiz1, raiz2)
	}

	// Alterar 1 transação deve mudar a Merkle Root
	txsModificadas := append([]Transacao{}, txs...)
	txsModificadas[1].ID = HashSHA256Hex([]byte("adulterado"))
	if CalcularMerkleRoot(txsModificadas) == raiz1 {
		t.Fatalf("Merkle Root deveria mudar quando uma transação é alterada")
	}
}

func TestEncadeamentoEPersistenciaIsolada(t *testing.T) {
	dir := t.TempDir()
	caminho := filepath.Join(dir, "empresaA", "blockchain.json")

	bc, err := NovaBlockchain(caminho)
	if err != nil {
		t.Fatalf("erro ao criar blockchain: %v", err)
	}
	if bc.Altura() != 1 {
		t.Fatalf("altura inicial esperada 1 (gênesis), obtida %d", bc.Altura())
	}

	valA := GerarCarteiraDeterministica("empresaA")
	valB := GerarCarteiraDeterministica("empresaB")
	passageiro, _ := GerarCarteira()

	tx := Transacao{
		Tipo:      TxCriarEscrow,
		Nonce:     1,
		Timestamp: 1767226000,
		Payload:   json.RawMessage(`{"reserva_id":"RES-10","valor_total":80}`),
	}
	if err := tx.Assinar(passageiro); err != nil {
		t.Fatalf("erro ao assinar transação: %v", err)
	}

	bloco1, err := NovoBloco(bc.UltimoBloco(), []Transacao{tx}, "empresaA", valA, 1767226001)
	if err != nil {
		t.Fatalf("erro ao criar bloco 1: %v", err)
	}

	votoB, err := bloco1.AssinarVoto("empresaB", valB)
	if err != nil {
		t.Fatalf("erro ao assinar voto da empresaB: %v", err)
	}
	bloco1.Votos = append(bloco1.Votos, votoB)

	if err := bc.AdicionarBloco(bloco1); err != nil {
		t.Fatalf("erro ao adicionar bloco 1 válido: %v", err)
	}

	// Recarrega do disco e verifica se manteve integridade e altura 2
	bcRecarregada, err := NovaBlockchain(caminho)
	if err != nil {
		t.Fatalf("erro ao recarregar blockchain persistida: %v", err)
	}
	if bcRecarregada.Altura() != 2 {
		t.Fatalf("altura recarregada esperada 2, obtida %d", bcRecarregada.Altura())
	}
	if bcRecarregada.UltimoBloco().Hash != bloco1.Hash {
		t.Fatalf("hash do último bloco difere após recarga")
	}
}

func TestDetectarAdulteracaoDeBlocoNoDisco(t *testing.T) {
	dir := t.TempDir()
	caminho := filepath.Join(dir, "empresaB", "blockchain.json")

	bc, err := NovaBlockchain(caminho)
	if err != nil {
		t.Fatalf("erro ao criar blockchain: %v", err)
	}

	valA := GerarCarteiraDeterministica("empresaA")
	passageiro, _ := GerarCarteira()

	tx := Transacao{
		Tipo:      TxAvaliar,
		Nonce:     1,
		Timestamp: 1767226100,
		Payload:   json.RawMessage(`{"reserva_id":"RES-22","nota":1,"comentario":"atrasou"}`),
	}
	_ = tx.Assinar(passageiro)

	bloco1, err := NovoBloco(bc.UltimoBloco(), []Transacao{tx}, "empresaA", valA, 1767226101)
	if err != nil {
		t.Fatalf("erro ao criar bloco 1: %v", err)
	}
	if err := bc.AdicionarBloco(bloco1); err != nil {
		t.Fatalf("erro ao adicionar bloco 1: %v", err)
	}

	// Cenário 1: Adulteração direta no arquivo blockchain.json (tenta mudar comentário de "atrasou" para "otimo")
	conteudoOriginal, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("erro ao ler blockchain.json: %v", err)
	}
	conteudoAdulterado := strings.Replace(string(conteudoOriginal), `"atrasou"`, `"otimo"`, 1)
	if conteudoAdulterado == string(conteudoOriginal) {
		t.Fatalf("falha no setup do teste: conteúdo do blockchain.json não foi alterado")
	}
	if err := os.WriteFile(caminho, []byte(conteudoAdulterado), 0644); err != nil {
		t.Fatalf("erro ao gravar arquivo adulterado: %v", err)
	}

	if _, err := NovaBlockchain(caminho); err == nil {
		t.Fatalf("NovaBlockchain deveria rejeitar blockchain.json adulterado em disco")
	}

	// Cenário 2: Atacante tenta recalcular MerkleRoot e Hash do bloco sem possuir a chave do passageiro/proponente
	blocos := bc.ObterBlocos()
	blocos[1].Transacoes[0].Payload = json.RawMessage(`{"reserva_id":"RES-22","nota":5,"comentario":"otimo"}`)
	blocos[1].MerkleRoot = CalcularMerkleRoot(blocos[1].Transacoes)
	blocos[1].Hash, _ = blocos[1].CalcularHash()

	if err := ValidarCadeia(blocos); err == nil {
		t.Fatalf("ValidarCadeia deveria rejeitar bloco adulterado mesmo com hash recalculado")
	}
}

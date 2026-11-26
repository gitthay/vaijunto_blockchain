package blockchain

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGerarCarteiraEDerivarEndereco(t *testing.T) {
	carteira, err := GerarCarteira()
	if err != nil {
		t.Fatalf("erro inesperado ao gerar carteira: %v", err)
	}

	if !strings.HasPrefix(carteira.Endereco, PrefixoEndereco) {
		t.Fatalf("endereço deveria começar com %s, recebido: %s", PrefixoEndereco, carteira.Endereco)
	}

	derivado, err := DerivarEndereco(carteira.ChavePublica)
	if err != nil {
		t.Fatalf("erro ao derivar endereço: %v", err)
	}
	if derivado != carteira.Endereco {
		t.Fatalf("endereço derivado (%s) difere do endereço da carteira (%s)", derivado, carteira.Endereco)
	}

	pub := carteira.SomentePublica()
	if pub.ChavePrivada != "" {
		t.Fatalf("SomentePublica não deveria expor a chave privada")
	}
}

func TestAssinaturaEVerificacaoValidaEAdulterada(t *testing.T) {
	passageiro, err := GerarCarteira()
	if err != nil {
		t.Fatalf("erro ao gerar carteira: %v", err)
	}

	dadosOriginais := []byte(`{"tipo":"CRIAR_ESCROW","reserva_id":"RES-1","valor":45.0}`)
	assinatura, err := AssinarDados(passageiro.ChavePrivada, dadosOriginais)
	if err != nil {
		t.Fatalf("erro ao assinar dados: %v", err)
	}

	// Caso 1: assinatura legítima deve ser aceita
	if err := ValidarIdentidadeAssinada(passageiro.Endereco, passageiro.ChavePublica, dadosOriginais, assinatura); err != nil {
		t.Fatalf("assinatura válida foi rejeitada: %v", err)
	}

	// Caso 2: adulteração de 1 caractere nos dados deve ser rejeitada
	dadosAdulterados := []byte(`{"tipo":"CRIAR_ESCROW","reserva_id":"RES-1","valor":450.0}`)
	if err := ValidarIdentidadeAssinada(passageiro.Endereco, passageiro.ChavePublica, dadosAdulterados, assinatura); err == nil {
		t.Fatalf("dados adulterados deveriam ser rejeitados")
	}
}

func TestImpedirFalsificacaoDeIdentidade(t *testing.T) {
	vitima, err := GerarCarteira()
	if err != nil {
		t.Fatalf("erro ao gerar carteira da vítima: %v", err)
	}
	atacante, err := GerarCarteira()
	if err != nil {
		t.Fatalf("erro ao gerar carteira do atacante: %v", err)
	}

	payload := []byte(`{"tipo":"AVALIAR","reserva_id":"RES-99","nota":1}`)

	// Atacante assina com a própria chave privada tentando se passar pela vítima
	assinaturaAtacante, err := AssinarDados(atacante.ChavePrivada, payload)
	if err != nil {
		t.Fatalf("erro ao assinar: %v", err)
	}

	// Tentativa 1: envia endereço da vítima + chave pública do atacante
	if err := ValidarIdentidadeAssinada(vitima.Endereco, atacante.ChavePublica, payload, assinaturaAtacante); err == nil {
		t.Fatalf("deveria rejeitar endereço da vítima com chave pública do atacante")
	}

	// Tentativa 2: envia endereço da vítima + chave pública da vítima, mas assinatura feita pelo atacante
	if err := ValidarIdentidadeAssinada(vitima.Endereco, vitima.ChavePublica, payload, assinaturaAtacante); err == nil {
		t.Fatalf("deveria rejeitar assinatura feita por outra chave privada")
	}
}

func TestCarregarOuCriarCarteiraArquivo(t *testing.T) {
	dirTemp := t.TempDir()
	caminho := filepath.Join(dirTemp, "validador_key.json")

	c1, err := CarregarOuCriarCarteiraArquivo(caminho)
	if err != nil {
		t.Fatalf("erro ao criar carteira em arquivo: %v", err)
	}

	c2, err := CarregarOuCriarCarteiraArquivo(caminho)
	if err != nil {
		t.Fatalf("erro ao recarregar carteira do arquivo: %v", err)
	}

	if c1.Endereco != c2.Endereco || c1.ChavePublica != c2.ChavePublica || c1.ChavePrivada != c2.ChavePrivada {
		t.Fatalf("carteira recarregada não corresponde à carteira original")
	}
}

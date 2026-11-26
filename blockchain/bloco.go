package blockchain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// TipoTransacao identifica cada operação imutável registrada no livro-razão do consórcio.
type TipoTransacao string

const (
	TxEmitirTokens      TipoTransacao = "EMITIR_TOKENS"
	TxTransferirTokens  TipoTransacao = "TRANSFERIR_TOKENS"
	TxCriarEscrow       TipoTransacao = "CRIAR_ESCROW"
	TxConcluirViagem    TipoTransacao = "CONCLUIR_VIAGEM"
	TxCancelarReserva   TipoTransacao = "CANCELAR_RESERVA"
	TxAvaliar           TipoTransacao = "AVALIAR"
	TxRegistrarAusencia TipoTransacao = "REGISTRAR_AUSENCIA"
	TxDenunciar         TipoTransacao = "DENUNCIAR"
)

const (
	HashGenesisAnterior = "0000000000000000000000000000000000000000000000000000000000000000"
	TimestampGenesis    = int64(1767225600) // 2026-01-01T00:00:00Z (fixo para todos os nós)
)

// Transacao representa uma operação assinada digitalmente por um participante do consórcio.
type Transacao struct {
	ID           string          `json:"id"`
	Tipo         TipoTransacao   `json:"tipo"`
	Remetente    string          `json:"remetente"`
	ChavePublica string          `json:"chave_publica"`
	Nonce        uint64          `json:"nonce"`
	Timestamp    int64           `json:"timestamp"`
	Payload      json.RawMessage `json:"payload"`
	Assinatura   string          `json:"assinatura"`
}

// VotoValidador registra a assinatura digital de uma empresa validadora aprovando um bloco.
type VotoValidador struct {
	ValidadorID  string `json:"validador_id"`
	Endereco     string `json:"endereco"`
	ChavePublica string `json:"chave_publica"`
	Assinatura   string `json:"assinatura"`
}

// Bloco representa uma unidade imutável da blockchain, encadeada ao bloco anterior via SHA-256.
type Bloco struct {
	Indice               uint64          `json:"indice"`
	Timestamp            int64           `json:"timestamp"`
	HashAnterior         string          `json:"hash_anterior"`
	MerkleRoot           string          `json:"merkle_root"`
	Transacoes           []Transacao     `json:"transacoes"`
	ProponenteID         string          `json:"proponente_id"`
	ProponenteEndereco   string          `json:"proponente_endereco"`
	ProponentePubKey     string          `json:"proponente_chave_publica"`
	Hash                 string          `json:"hash"`
	AssinaturaProponente string          `json:"assinatura_proponente"`
	Votos                []VotoValidador `json:"votos,omitempty"`
}

// BytesAssinaveis serializa de forma determinística os campos da transação que entram no ID e na assinatura.
// O payload JSON é compactado para evitar divergências de espaços/quebras de linha entre nós.
func (tx Transacao) BytesAssinaveis() ([]byte, error) {
	payloadCanonico := []byte("null")
	if len(bytes.TrimSpace(tx.Payload)) > 0 {
		var buf bytes.Buffer
		if err := json.Compact(&buf, tx.Payload); err != nil {
			return nil, fmt.Errorf("payload JSON inválido na transação: %w", err)
		}
		payloadCanonico = buf.Bytes()
	}

	return SerializarParaAssinatura(struct {
		Tipo         TipoTransacao   `json:"tipo"`
		Remetente    string          `json:"remetente"`
		ChavePublica string          `json:"chave_publica"`
		Nonce        uint64          `json:"nonce"`
		Timestamp    int64           `json:"timestamp"`
		Payload      json.RawMessage `json:"payload"`
	}{
		Tipo:         tx.Tipo,
		Remetente:    tx.Remetente,
		ChavePublica: tx.ChavePublica,
		Nonce:        tx.Nonce,
		Timestamp:    tx.Timestamp,
		Payload:      json.RawMessage(payloadCanonico),
	})
}

// CalcularID retorna o hash SHA-256 determinístico da transação.
func (tx Transacao) CalcularID() (string, error) {
	dados, err := tx.BytesAssinaveis()
	if err != nil {
		return "", err
	}
	return HashSHA256Hex(dados), nil
}

// Assinar vincula a identidade da carteira à transação, calcula o ID e gera a assinatura Ed25519.
func (tx *Transacao) Assinar(carteira *Carteira) error {
	if carteira == nil {
		return errors.New("carteira não pode ser nula ao assinar transação")
	}
	tx.Remetente = carteira.Endereco
	tx.ChavePublica = carteira.ChavePublica

	dados, err := tx.BytesAssinaveis()
	if err != nil {
		return err
	}
	tx.ID = HashSHA256Hex(dados)

	assinatura, err := AssinarDados(carteira.ChavePrivada, dados)
	if err != nil {
		return err
	}
	tx.Assinatura = assinatura
	return nil
}

// ValidarAssinatura verifica a integridade do ID e a autenticidade criptográfica da transação.
func (tx Transacao) ValidarAssinatura() error {
	if strings.TrimSpace(string(tx.Tipo)) == "" {
		return errors.New("tipo da transação não pode ser vazio")
	}
	dados, err := tx.BytesAssinaveis()
	if err != nil {
		return err
	}
	idEsperado := HashSHA256Hex(dados)
	if tx.ID != idEsperado {
		return fmt.Errorf("ID da transação inválido: esperado %s, encontrado %s", idEsperado, tx.ID)
	}
	return ValidarIdentidadeAssinada(tx.Remetente, tx.ChavePublica, dados, tx.Assinatura)
}

// CalcularMerkleRoot constrói a raiz da Árvore de Merkle (SHA-256) das transações do bloco.
// Qualquer alteração em qualquer transação altera a Merkle Root e invalida o hash do bloco.
func CalcularMerkleRoot(transacoes []Transacao) string {
	if len(transacoes) == 0 {
		return HashSHA256Hex([]byte("VAZIO"))
	}

	nivel := make([]string, len(transacoes))
	for i, tx := range transacoes {
		if tx.ID != "" {
			nivel[i] = tx.ID
		} else {
			id, _ := tx.CalcularID()
			nivel[i] = id
		}
	}

	for len(nivel) > 1 {
		if len(nivel)%2 != 0 {
			// Duplica o último elemento em níveis ímpares (padrão clássico de Merkle Tree)
			nivel = append(nivel, nivel[len(nivel)-1])
		}
		proximo := make([]string, 0, len(nivel)/2)
		for i := 0; i < len(nivel); i += 2 {
			combinado := HashSHA256Hex([]byte(nivel[i] + nivel[i+1]))
			proximo = append(proximo, combinado)
		}
		nivel = proximo
	}

	return nivel[0]
}

// CalcularHash calcula o SHA-256 determinístico do cabeçalho do bloco.
// AssinaturaProponente e Votos não entram no cabeçalho porque assinam justamente este Hash.
func (b Bloco) CalcularHash() (string, error) {
	dados, err := SerializarParaAssinatura(struct {
		Indice             uint64 `json:"indice"`
		Timestamp          int64  `json:"timestamp"`
		HashAnterior       string `json:"hash_anterior"`
		MerkleRoot         string `json:"merkle_root"`
		ProponenteID       string `json:"proponente_id"`
		ProponenteEndereco string `json:"proponente_endereco"`
		ProponentePubKey   string `json:"proponente_chave_publica"`
	}{
		Indice:             b.Indice,
		Timestamp:          b.Timestamp,
		HashAnterior:       b.HashAnterior,
		MerkleRoot:         b.MerkleRoot,
		ProponenteID:       b.ProponenteID,
		ProponenteEndereco: b.ProponenteEndereco,
		ProponentePubKey:   b.ProponentePubKey,
	})
	if err != nil {
		return "", err
	}
	return HashSHA256Hex(dados), nil
}

// CriarBlocoGenesis retorna o bloco inicial (índice 0) determinístico e idêntico em todos os nós do consórcio.
func CriarBlocoGenesis() Bloco {
	b := Bloco{
		Indice:       0,
		Timestamp:    TimestampGenesis,
		HashAnterior: HashGenesisAnterior,
		MerkleRoot:   CalcularMerkleRoot(nil),
		Transacoes:   []Transacao{},
		ProponenteID: "GENESIS",
	}
	hash, _ := b.CalcularHash()
	b.Hash = hash
	return b
}

// NovoBloco cria um bloco válido encadeado ao bloco anterior, assinado pelo nó proponente.
func NovoBloco(anterior Bloco, transacoes []Transacao, proponenteID string, carteira *Carteira, timestamp int64) (Bloco, error) {
	if carteira == nil {
		return Bloco{}, errors.New("carteira do validador proponente não pode ser nula")
	}
	if strings.TrimSpace(proponenteID) == "" {
		return Bloco{}, errors.New("identificador do proponente não pode ser vazio")
	}

	copiaTxs := make([]Transacao, len(transacoes))
	copy(copiaTxs, transacoes)
	for i, tx := range copiaTxs {
		if err := tx.ValidarAssinatura(); err != nil {
			return Bloco{}, fmt.Errorf("transação #%d (%s) inválida: %w", i, tx.ID, err)
		}
	}

	bloco := Bloco{
		Indice:             anterior.Indice + 1,
		Timestamp:          timestamp,
		HashAnterior:       anterior.Hash,
		MerkleRoot:         CalcularMerkleRoot(copiaTxs),
		Transacoes:         copiaTxs,
		ProponenteID:       proponenteID,
		ProponenteEndereco: carteira.Endereco,
		ProponentePubKey:   carteira.ChavePublica,
	}

	hash, err := bloco.CalcularHash()
	if err != nil {
		return Bloco{}, err
	}
	bloco.Hash = hash

	assinatura, err := AssinarDados(carteira.ChavePrivada, []byte(bloco.Hash))
	if err != nil {
		return Bloco{}, err
	}
	bloco.AssinaturaProponente = assinatura

	// O próprio proponente já registra seu voto assinado no bloco
	bloco.Votos = []VotoValidador{
		{
			ValidadorID:  proponenteID,
			Endereco:     carteira.Endereco,
			ChavePublica: carteira.ChavePublica,
			Assinatura:   assinatura,
		},
	}

	return bloco, nil
}

// AssinarVoto gera um voto criptográfico de um nó validador aprovando este bloco.
func (b Bloco) AssinarVoto(validadorID string, carteira *Carteira) (VotoValidador, error) {
	if carteira == nil {
		return VotoValidador{}, errors.New("carteira do validador não pode ser nula")
	}
	if strings.TrimSpace(b.Hash) == "" {
		return VotoValidador{}, errors.New("bloco sem hash calculado")
	}
	assinatura, err := AssinarDados(carteira.ChavePrivada, []byte(b.Hash))
	if err != nil {
		return VotoValidador{}, err
	}
	return VotoValidador{
		ValidadorID:  validadorID,
		Endereco:     carteira.Endereco,
		ChavePublica: carteira.ChavePublica,
		Assinatura:   assinatura,
	}, nil
}

// ValidarEstrutura verifica integralmente o encadeamento, Merkle Root, hash,
// assinaturas das transações, assinatura do proponente e votos dos validadores.
func (b Bloco) ValidarEstrutura(anterior *Bloco) error {
	if b.Indice == 0 {
		genesis := CriarBlocoGenesis()
		if b.Hash != genesis.Hash || b.HashAnterior != genesis.HashAnterior ||
			b.MerkleRoot != genesis.MerkleRoot || b.Timestamp != genesis.Timestamp ||
			len(b.Transacoes) != 0 || b.ProponenteID != genesis.ProponenteID {
			return errors.New("bloco gênesis adulterado ou incompatível com o consórcio")
		}
		return nil
	}

	if anterior == nil {
		return fmt.Errorf("bloco #%d requer referência ao bloco anterior", b.Indice)
	}
	if b.Indice != anterior.Indice+1 {
		return fmt.Errorf("índice inválido: esperado %d, encontrado %d", anterior.Indice+1, b.Indice)
	}
	if b.HashAnterior != anterior.Hash {
		return fmt.Errorf("encadeamento quebrado no bloco #%d: hash_anterior não bate com o bloco #%d", b.Indice, anterior.Indice)
	}

	// 1. Valida cada transação do bloco
	for i, tx := range b.Transacoes {
		if err := tx.ValidarAssinatura(); err != nil {
			return fmt.Errorf("bloco #%d contém transação #%d inválida: %w", b.Indice, i, err)
		}
	}

	// 2. Valida Merkle Root
	merkleEsperada := CalcularMerkleRoot(b.Transacoes)
	if b.MerkleRoot != merkleEsperada {
		return fmt.Errorf("bloco #%d adulterado: merkle_root esperada %s, encontrada %s", b.Indice, merkleEsperada, b.MerkleRoot)
	}

	// 3. Valida Hash do bloco
	hashEsperado, err := b.CalcularHash()
	if err != nil {
		return err
	}
	if b.Hash != hashEsperado {
		return fmt.Errorf("bloco #%d adulterado: hash esperado %s, encontrado %s", b.Indice, hashEsperado, b.Hash)
	}

	// 4. Valida assinatura do proponente sobre o hash do bloco
	if err := ValidarIdentidadeAssinada(b.ProponenteEndereco, b.ProponentePubKey, []byte(b.Hash), b.AssinaturaProponente); err != nil {
		return fmt.Errorf("assinatura do proponente inválida no bloco #%d: %w", b.Indice, err)
	}

	// 5. Valida votos dos validadores (sem duplicidade)
	vistos := make(map[string]bool)
	for i, voto := range b.Votos {
		if vistos[voto.Endereco] {
			return fmt.Errorf("bloco #%d possui voto duplicado do validador %s", b.Indice, voto.Endereco)
		}
		vistos[voto.Endereco] = true
		if err := ValidarIdentidadeAssinada(voto.Endereco, voto.ChavePublica, []byte(b.Hash), voto.Assinatura); err != nil {
			return fmt.Errorf("voto #%d inválido no bloco #%d: %w", i, b.Indice, err)
		}
	}

	return nil
}

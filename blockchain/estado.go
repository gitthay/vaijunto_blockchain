package blockchain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

// Erros sentinela da máquina de estado. Use errors.Is para identificá-los.
var (
	ErrSaldoInsuficiente    = errors.New("saldo insuficiente")
	ErrNonceInvalido        = errors.New("nonce inválido")
	ErrEmissorNaoAutorizado = errors.New("remetente não autorizado a emitir tokens")
	ErrTipoNaoSuportado     = errors.New("tipo de transação não suportado pela máquina de estado")
	ErrPayloadInvalido      = errors.New("payload inválido")
	ErrValorInvalido        = errors.New("valor deve ser maior que zero")
	ErrOverflowSaldo        = errors.New("overflow de saldo")
)

// PayloadEmitirTokens é o payload de uma transação EMITIR_TOKENS (emissão/recarga).
type PayloadEmitirTokens struct {
	Destinatario string `json:"destinatario"`
	Valor        uint64 `json:"valor"`
}

// PayloadTransferirTokens é o payload de uma transação TRANSFERIR_TOKENS.
type PayloadTransferirTokens struct {
	Destinatario string `json:"destinatario"`
	Valor        uint64 `json:"valor"`
}

// Estado é a máquina de estados determinística do ledger: saldos por endereço e
// último nonce consumido por endereço. É função exclusiva da sequência de blocos
// aplicados, portanto todos os nós que aplicarem os mesmos blocos chegam ao mesmo estado.
//
// Regras:
//   - Nonce sequencial por remetente: a primeira transação usa nonce 1 e cada
//     seguinte deve usar exatamente último+1 (impede replay, reordenação e gasto duplo).
//   - EMITIR_TOKENS só é aceita de endereços autorizados (emissores) e cria saldo.
//   - TRANSFERIR_TOKENS exige saldo suficiente do remetente.
//   - Um bloco é atômico: se qualquer transação for inválida, o bloco inteiro é
//     rejeitado e o estado permanece intacto.
type Estado struct {
	mu        sync.RWMutex
	saldos    map[string]uint64
	nonces    map[string]uint64 // último nonce consumido por endereço
	emissores map[string]bool
	escrows   map[string]Escrow
	emitido   uint64 // total de tokens emitidos (oferta total)
}

// NovoEstado cria um estado vazio. Os endereços informados são os únicos
// autorizados a assinar transações EMITIR_TOKENS (ex.: validadores do consórcio).
func NovoEstado(emissores ...string) *Estado {
	e := &Estado{
		escrows:   make(map[string]Escrow),
		saldos:    make(map[string]uint64),
		nonces:    make(map[string]uint64),
		emissores: make(map[string]bool, len(emissores)),
	}
	for _, end := range emissores {
		if end = strings.TrimSpace(end); end != "" {
			e.emissores[end] = true
		}
	}
	return e
}

// ReconstruirEstado deriva o estado do zero aplicando, em ordem, os blocos informados
// (o gênesis é aceito e ignorado por não conter transações).
func ReconstruirEstado(blocos []Bloco, emissores ...string) (*Estado, error) {
	e := NovoEstado(emissores...)
	for i := range blocos {
		if blocos[i].Indice == 0 {
			continue
		}
		if err := e.AplicarBloco(blocos[i]); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// ---------------------------------------------------------------------------
// Consultas
// ---------------------------------------------------------------------------

// Saldo retorna o saldo de tokens do endereço (0 se desconhecido).
func (e *Estado) Saldo(endereco string) uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.saldos[endereco]
}

// NonceAtual retorna o último nonce consumido pelo endereço (0 se nunca transacionou).
func (e *Estado) NonceAtual(endereco string) uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.nonces[endereco]
}

// ProximoNonce retorna o nonce que a próxima transação do endereço deve usar.
func (e *Estado) ProximoNonce(endereco string) uint64 {
	return e.NonceAtual(endereco) + 1
}

// TotalEmitido retorna a oferta total de tokens emitidos.
func (e *Estado) TotalEmitido() uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.emitido
}

// EhEmissor informa se o endereço pode emitir tokens.
func (e *Estado) EhEmissor(endereco string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.emissores[endereco]
}

// Saldos retorna uma cópia de todos os saldos.
func (e *Estado) Saldos() map[string]uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c := make(map[string]uint64, len(e.saldos))
	for k, v := range e.saldos {
		c[k] = v
	}
	return c
}

// SnapshotEstado é a visão serializável (JSON) do estado, gravada em dados/<empresa>/estado.json.
// É um dado DERIVADO: a fonte da verdade é blockchain.json e o estado é sempre reconstruído dele no boot.
type SnapshotEstado struct {
	Altura       int               `json:"altura"`
	HashTopo     string            `json:"hash_topo"`
	RaizEstado   string            `json:"raiz_estado"`
	TotalEmitido uint64            `json:"total_emitido"`
	Emissores    []string          `json:"emissores"`
	Saldos       map[string]uint64 `json:"saldos"`
	Nonces       map[string]uint64 `json:"nonces"`
	Escrows      map[string]Escrow `json:"escrows,omitempty"`
}

// Snapshot retorna uma cópia serializável do estado (altura e hash do topo são preenchidos por quem chama).
func (e *Estado) Snapshot() SnapshotEstado {
	e.mu.RLock()
	defer e.mu.RUnlock()
	snap := SnapshotEstado{
		RaizEstado:   e.raizSemLock(),
		Escrows:      copiarEscrows(e.escrows),
		TotalEmitido: e.emitido,
		Emissores:    make([]string, 0, len(e.emissores)),
		Saldos:       make(map[string]uint64, len(e.saldos)),
		Nonces:       make(map[string]uint64, len(e.nonces)),
	}
	for k := range e.emissores {
		snap.Emissores = append(snap.Emissores, k)
	}
	sort.Strings(snap.Emissores)
	for k, v := range e.saldos {
		snap.Saldos[k] = v
	}
	for k, v := range e.nonces {
		snap.Nonces[k] = v
	}
	return snap
}

// Raiz retorna um hash SHA-256 determinístico de todo o estado (saldos, nonces e
// oferta total, em ordem lexicográfica de endereço). Nós com o mesmo histórico de
// blocos obtêm a mesma raiz, o que permite comparar estados rapidamente.
func (e *Estado) Raiz() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.raizSemLock()
}

func (e *Estado) raizSemLock() string {
	enderecos := make(map[string]struct{}, len(e.saldos)+len(e.nonces))
	for k := range e.saldos {
		enderecos[k] = struct{}{}
	}
	for k := range e.nonces {
		enderecos[k] = struct{}{}
	}
	ordenados := make([]string, 0, len(enderecos))
	for k := range enderecos {
		ordenados = append(ordenados, k)
	}
	sort.Strings(ordenados)

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "emitido:%d\n", e.emitido)
	for _, k := range ordenados {
		fmt.Fprintf(&buf, "%s|%d|%d\n", k, e.saldos[k], e.nonces[k])
	}
	if len(e.escrows) > 0 {
		dados, _ := json.Marshal(e.escrows)
		buf.Write(dados)
	}
	return HashSHA256Hex(buf.Bytes())
}

// ---------------------------------------------------------------------------
// Aplicação de transações e blocos
// ---------------------------------------------------------------------------

// visaoEstado é uma sobreposição (overlay) sobre o estado base: acumula as
// mudanças de um bloco sem tocar no estado real até a confirmação (commit).
type visaoEstado struct {
	base    *Estado
	saldos  map[string]uint64
	nonces  map[string]uint64
	emitido uint64
	escrows map[string]Escrow
}

func novaVisao(base *Estado) *visaoEstado {
	return &visaoEstado{
		base:    base,
		escrows: make(map[string]Escrow),
		saldos:  make(map[string]uint64),
		nonces:  make(map[string]uint64),
		emitido: base.emitido,
	}
}

func (v *visaoEstado) saldo(end string) uint64 {
	if s, ok := v.saldos[end]; ok {
		return s
	}
	return v.base.saldos[end]
}

func (v *visaoEstado) nonce(end string) uint64 {
	if n, ok := v.nonces[end]; ok {
		return n
	}
	return v.base.nonces[end]
}

// aplicar valida e aplica uma transação na visão. Em caso de erro a visão não é alterada.
func (v *visaoEstado) aplicar(tx Transacao) error {
	if err := tx.ValidarAssinatura(); err != nil {
		return fmt.Errorf("transação %s com assinatura inválida: %w", tx.ID, err)
	}

	// Nonce estritamente sequencial: bloqueia replay (nonce antigo), lacunas e duplicatas.
	atual := v.nonce(tx.Remetente)
	if atual == math.MaxUint64 || tx.Nonce != atual+1 {
		return fmt.Errorf("%w: remetente %s esperava nonce %d, recebeu %d",
			ErrNonceInvalido, tx.Remetente, atual+1, tx.Nonce)
	}

	switch tx.Tipo {
	case TxEmitirTokens:
		var p PayloadEmitirTokens
		if err := decodificarPayload(tx.Payload, &p); err != nil {
			return err
		}
		if !v.base.emissores[tx.Remetente] {
			return fmt.Errorf("%w: %s", ErrEmissorNaoAutorizado, tx.Remetente)
		}
		if err := validarDestinatarioEValor(p.Destinatario, p.Valor); err != nil {
			return err
		}
		novoSaldo := v.saldo(p.Destinatario) + p.Valor
		if novoSaldo < p.Valor || v.emitido+p.Valor < p.Valor {
			return fmt.Errorf("%w: destinatário %s", ErrOverflowSaldo, p.Destinatario)
		}
		v.saldos[p.Destinatario] = novoSaldo
		v.emitido += p.Valor

	case TxTransferirTokens:
		var p PayloadTransferirTokens
		if err := decodificarPayload(tx.Payload, &p); err != nil {
			return err
		}
		if err := validarDestinatarioEValor(p.Destinatario, p.Valor); err != nil {
			return err
		}
		if p.Destinatario == tx.Remetente {
			return fmt.Errorf("%w: remetente e destinatário não podem ser o mesmo endereço", ErrPayloadInvalido)
		}
		saldoOrigem := v.saldo(tx.Remetente)
		if saldoOrigem < p.Valor {
			return fmt.Errorf("%w: %s possui %d e tentou transferir %d",
				ErrSaldoInsuficiente, tx.Remetente, saldoOrigem, p.Valor)
		}
		novoDestino := v.saldo(p.Destinatario) + p.Valor
		if novoDestino < p.Valor {
			return fmt.Errorf("%w: destinatário %s", ErrOverflowSaldo, p.Destinatario)
		}
		v.saldos[tx.Remetente] = saldoOrigem - p.Valor
		v.saldos[p.Destinatario] = novoDestino

	case TxCriarEscrow, TxConcluirViagem, TxCancelarReserva:
		if err := v.aplicarEscrow(tx); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: %s", ErrTipoNaoSuportado, tx.Tipo)
	}

	v.nonces[tx.Remetente] = tx.Nonce
	return nil
}

func (v *visaoEstado) confirmar() {
	for k, s := range v.saldos {
		v.base.saldos[k] = s
	}
	for k, n := range v.nonces {
		v.base.nonces[k] = n
	}
	for k, escrow := range v.escrows {
		v.base.escrows[k] = escrow
	}
	v.base.emitido = v.emitido
}

// ValidarTransacao verifica, sem alterar o estado, se a transação seria aceita agora
// (assinatura, nonce == próximo, saldo e autorização). Útil para rejeitar na entrada.
func (e *Estado) ValidarTransacao(tx Transacao) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return novaVisao(e).aplicar(tx)
}

// ValidarTransacoes verifica, sem alterar o estado, se o lote seria aceito em ordem
// (transações dependentes, como nonces consecutivos do mesmo usuário, são consideradas).
func (e *Estado) ValidarTransacoes(txs []Transacao) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	v := novaVisao(e)
	for i, tx := range txs {
		if err := v.aplicar(tx); err != nil {
			return fmt.Errorf("transação #%d (%s): %w", i, tx.ID, err)
		}
	}
	return nil
}

// AplicarTransacao valida e aplica uma única transação de forma atômica.
func (e *Estado) AplicarTransacao(tx Transacao) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	v := novaVisao(e)
	if err := v.aplicar(tx); err != nil {
		return err
	}
	v.confirmar()
	return nil
}

// AplicarBloco aplica todas as transações do bloco em ordem. É atômico: se qualquer
// transação falhar (saldo insuficiente, nonce inválido etc.), nada é alterado e o
// erro indica qual transação causou a rejeição.
func (e *Estado) AplicarBloco(b Bloco) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	v := novaVisao(e)
	for i, tx := range b.Transacoes {
		if err := v.aplicar(tx); err != nil {
			return fmt.Errorf("bloco #%d rejeitado, transação #%d (%s): %w", b.Indice, i, tx.ID, err)
		}
	}
	v.confirmar()
	return nil
}

// ---------------------------------------------------------------------------
// Construtores de transação (emissão inicial para testes/demonstração e transferência)
// ---------------------------------------------------------------------------

// NovaTransacaoEmissao cria e assina uma transação EMITIR_TOKENS (recarga/emissão inicial).
// A carteira informada precisa ser um emissor autorizado no Estado.
func NovaTransacaoEmissao(emissor *Carteira, destinatario string, valor, nonce uint64, timestamp int64) (Transacao, error) {
	return novaTransacaoToken(TxEmitirTokens, emissor, destinatario, valor, nonce, timestamp)
}

// NovaTransacaoTransferencia cria e assina uma transação TRANSFERIR_TOKENS.
func NovaTransacaoTransferencia(origem *Carteira, destinatario string, valor, nonce uint64, timestamp int64) (Transacao, error) {
	return novaTransacaoToken(TxTransferirTokens, origem, destinatario, valor, nonce, timestamp)
}

func novaTransacaoToken(tipo TipoTransacao, carteira *Carteira, destinatario string, valor, nonce uint64, timestamp int64) (Transacao, error) {
	payload, err := json.Marshal(struct {
		Destinatario string `json:"destinatario"`
		Valor        uint64 `json:"valor"`
	}{destinatario, valor})
	if err != nil {
		return Transacao{}, err
	}
	tx := Transacao{Tipo: tipo, Nonce: nonce, Timestamp: timestamp, Payload: payload}
	if err := tx.Assinar(carteira); err != nil {
		return Transacao{}, err
	}
	return tx, nil
}

// ---------------------------------------------------------------------------
// Auxiliares
// ---------------------------------------------------------------------------

// decodificarPayload rejeita campos desconhecidos para evitar ambiguidades entre nós.
func decodificarPayload(raw json.RawMessage, destino interface{}) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("%w: payload vazio", ErrPayloadInvalido)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(destino); err != nil {
		return fmt.Errorf("%w: %v", ErrPayloadInvalido, err)
	}
	return nil
}

func validarDestinatarioEValor(destinatario string, valor uint64) error {
	if err := validarFormatoEndereco(destinatario); err != nil {
		return fmt.Errorf("%w: destinatário %q: %v", ErrPayloadInvalido, destinatario, err)
	}
	if valor == 0 {
		return ErrValorInvalido
	}
	return nil
}

// validarFormatoEndereco confere o formato "VJ-" + 40 caracteres hexadecimais minúsculos.
func validarFormatoEndereco(end string) error {
	if !strings.HasPrefix(end, PrefixoEndereco) {
		return fmt.Errorf("endereço deve começar com %q", PrefixoEndereco)
	}
	corpo := end[len(PrefixoEndereco):]
	if len(corpo) != 40 {
		return errors.New("endereço deve ter 40 caracteres hexadecimais após o prefixo")
	}
	for _, c := range corpo {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return errors.New("endereço contém caractere não hexadecimal")
		}
	}
	return nil
}

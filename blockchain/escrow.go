package blockchain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

var (
	ErrEscrowExistente     = errors.New("reserva já possui escrow")
	ErrEscrowInexistente   = errors.New("escrow inexistente")
	ErrEscrowCancelado     = errors.New("escrow já cancelado")
	ErrEscrowConcluido     = errors.New("escrow já concluído")
	ErrEscrowNaoAutorizado = errors.New("somente o passageiro titular pode operar o escrow")
)

type PagamentoTrecho struct {
	TrechoID  string `json:"trecho_id"`
	Motorista string `json:"motorista"` // endereço criptográfico, não e-mail
	Valor     uint64 `json:"valor"`
}
type PayloadCriarEscrow struct {
	ReservaID  string            `json:"reserva_id"`
	Trechos    []PagamentoTrecho `json:"trechos"`
	ValorTotal uint64            `json:"valor_total"`
}
type PayloadConcluirViagem struct {
	ReservaID string `json:"reserva_id"`
}
type Escrow struct {
	ReservaID  string            `json:"reserva_id"`
	Passageiro string            `json:"passageiro"`
	Trechos    []PagamentoTrecho `json:"trechos"`
	ValorTotal uint64            `json:"valor_total"`
	Concluido  bool              `json:"concluido"`
	Cancelado  bool              `json:"cancelado,omitempty"`
}

func copiarEscrow(e Escrow) Escrow {
	e.Trechos = append([]PagamentoTrecho(nil), e.Trechos...)
	return e
}
func copiarEscrows(m map[string]Escrow) map[string]Escrow {
	out := make(map[string]Escrow, len(m))
	for k, e := range m {
		out[k] = copiarEscrow(e)
	}
	return out
}
func (e *Estado) Escrow(reservaID string) (Escrow, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	x, ok := e.escrows[reservaID]
	return copiarEscrow(x), ok
}
func (e *Estado) SaldoBloqueado(endereco string) uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var n uint64
	for _, x := range e.escrows {
		if x.Passageiro == endereco && !x.Concluido && !x.Cancelado {
			n += x.ValorTotal
		}
	}
	return n
}
func (v *visaoEstado) escrow(id string) (Escrow, bool) {
	if x, ok := v.escrows[id]; ok {
		return x, true
	}
	x, ok := v.base.escrows[id]
	return x, ok
}
func idValido(id string) bool { return id != "" && id == strings.TrimSpace(id) && len(id) <= 256 }
func (v *visaoEstado) aplicarEscrow(tx Transacao) error {
	switch tx.Tipo {
	case TxCriarEscrow:
		var p PayloadCriarEscrow
		if err := decodificarPayload(tx.Payload, &p); err != nil {
			return err
		}
		if !idValido(p.ReservaID) || len(p.Trechos) == 0 || p.ValorTotal == 0 {
			return ErrPayloadInvalido
		}
		if _, ok := v.escrow(p.ReservaID); ok {
			return ErrEscrowExistente
		}
		vistos := map[string]bool{}
		var total uint64
		for _, t := range p.Trechos {
			if !idValido(t.TrechoID) || vistos[t.TrechoID] || t.Motorista == tx.Remetente {
				return ErrPayloadInvalido
			}
			vistos[t.TrechoID] = true
			if err := validarDestinatarioEValor(t.Motorista, t.Valor); err != nil {
				return err
			}
			if math.MaxUint64-total < t.Valor {
				return ErrOverflowSaldo
			}
			total += t.Valor
		}
		if total != p.ValorTotal {
			return fmt.Errorf("%w: soma dos trechos difere do total", ErrPayloadInvalido)
		}
		saldo := v.saldo(tx.Remetente)
		if saldo < total {
			return ErrSaldoInsuficiente
		}
		v.saldos[tx.Remetente] = saldo - total
		v.escrows[p.ReservaID] = Escrow{ReservaID: p.ReservaID, Passageiro: tx.Remetente, Trechos: p.Trechos, ValorTotal: total}
	case TxConcluirViagem, TxCancelarReserva:
		var p PayloadConcluirViagem
		if err := decodificarPayload(tx.Payload, &p); err != nil {
			return err
		}
		if !idValido(p.ReservaID) {
			return ErrPayloadInvalido
		}
		x, ok := v.escrow(p.ReservaID)
		if !ok {
			return ErrEscrowInexistente
		}
		if x.Passageiro != tx.Remetente {
			return ErrEscrowNaoAutorizado
		}
		if x.Concluido {
			return ErrEscrowConcluido
		}
		if x.Cancelado {
			return ErrEscrowCancelado
		}
		if tx.Tipo == TxCancelarReserva {
			saldo := v.saldo(x.Passageiro)
			if math.MaxUint64-saldo < x.ValorTotal {
				return ErrOverflowSaldo
			}
			v.saldos[x.Passageiro] = saldo + x.ValorTotal
			x.Cancelado = true
			v.escrows[p.ReservaID] = x
			return nil
		}
		// Acumula pagamentos repetidos ao mesmo motorista antes de modificar a visão.
		novos := map[string]uint64{}
		for _, t := range x.Trechos {
			s, ok := novos[t.Motorista]
			if !ok {
				s = v.saldo(t.Motorista)
			}
			if math.MaxUint64-s < t.Valor {
				return ErrOverflowSaldo
			}
			novos[t.Motorista] = s + t.Valor
		}
		for k, s := range novos {
			v.saldos[k] = s
		}
		x.Concluido = true
		v.escrows[p.ReservaID] = x
	}
	return nil
}
func NovaTransacaoCriarEscrow(c *Carteira, p PayloadCriarEscrow, nonce uint64, timestamp int64) (Transacao, error) {
	return novaTransacaoEscrow(TxCriarEscrow, c, p, nonce, timestamp)
}
func NovaTransacaoConcluirViagem(c *Carteira, reservaID string, nonce uint64, timestamp int64) (Transacao, error) {
	return novaTransacaoEscrow(TxConcluirViagem, c, PayloadConcluirViagem{ReservaID: reservaID}, nonce, timestamp)
}
func novaTransacaoEscrow(tipo TipoTransacao, c *Carteira, p any, nonce uint64, timestamp int64) (Transacao, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return Transacao{}, err
	}
	tx := Transacao{Tipo: tipo, Nonce: nonce, Timestamp: timestamp, Payload: raw}
	if err = tx.Assinar(c); err != nil {
		return Transacao{}, err
	}
	return tx, nil
}

// NovaTransacaoCancelarEscrow devolve integralmente o crédito ao titular.
func NovaTransacaoCancelarEscrow(c *Carteira, reservaID string, nonce uint64, timestamp int64) (Transacao, error) {
	return novaTransacaoEscrow(TxCancelarReserva, c, PayloadConcluirViagem{ReservaID: reservaID}, nonce, timestamp)
}

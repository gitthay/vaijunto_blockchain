package utils

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"strings"
	"vaiJunto/blockchain"
)

const AcaoCotarReserva TipoAcao = "COTAR_RESERVA"

type CotacaoReserva struct {
	Itinerario Itinerario                    `json:"itinerario"`
	Escrow     blockchain.PayloadCriarEscrow `json:"escrow"`
	Saldo      uint64                        `json:"saldo"`
	Nonce      uint64                        `json:"nonce"`
}
type ReservaComTokens struct {
	Itinerario Itinerario           `json:"itinerario"`
	Transacao  blockchain.Transacao `json:"transacao"`
}

// Reconsulta preço e motorista no servidor proprietário; nunca confia no preço do cliente.
func cotarTrechos(it Itinerario, id string) (Itinerario, blockchain.PayloadCriarEscrow, error) {
	p := blockchain.PayloadCriarEscrow{ReservaID: id}
	out := Itinerario{}
	if len(it.Trechos) == 0 || len(it.Trechos) > 100 {
		return out, p, fmt.Errorf("itinerário inválido")
	}
	for i, t := range it.Trechos {
		srv := strings.TrimRight(t.ServidorURL, "/")
		if srv == "" {
			srv = ServidorAtualURL
		}
		var d ArestaTrecho
		var end string
		if t.CaronaID == "" || t.Origem == "" || t.Destino == "" {
			return out, p, fmt.Errorf("trecho inválido")
		}
		if srv == ServidorAtualURL {
			mu.Lock()
			cs, err := CarregarCaronas()
			var ok bool
			if err == nil {
				d, ok = obterDetalheTrechoEmListaCaronas(cs, t.CaronaID, t.Origem, t.Destino)
			}
			mu.Unlock()
			if err != nil || !ok {
				return out, p, fmt.Errorf("trecho local indisponível")
			}
			end, err = enderecoDoUsuario(d.MotoristaID)
			if err != nil {
				return out, p, err
			}
		} else {
			muPeers.RLock()
			_, ok := PeersConsorcio[srv]
			muPeers.RUnlock()
			if !ok {
				return out, p, fmt.Errorf("servidor não pertence ao consórcio")
			}
			q := url.Values{"carona_id": {t.CaronaID}, "origem": {t.Origem}, "destino": {t.Destino}}
			resp, err := clienteHTTP.Get(srv + "/api/caronas/detalhe?" + q.Encode())
			if err != nil {
				return out, p, err
			}
			err = json.NewDecoder(resp.Body).Decode(&d)
			resp.Body.Close()
			if err != nil || resp.StatusCode != 200 {
				return out, p, fmt.Errorf("trecho remoto indisponível")
			}
			resp, err = clienteHTTP.Get(srv + "/api/ledger/endereco?email=" + url.QueryEscape(d.MotoristaID))
			if err != nil {
				return out, p, err
			}
			var e RespostaEndereco
			err = json.NewDecoder(resp.Body).Decode(&e)
			resp.Body.Close()
			if err != nil || resp.StatusCode != 200 || e.Email != d.MotoristaID {
				return out, p, fmt.Errorf("carteira do motorista indisponível")
			}
			end = e.Endereco
		}
		if d.CaronaID != t.CaronaID || d.Origem != t.Origem || d.Destino != t.Destino || d.AssentosLivre <= 0 {
			return out, p, fmt.Errorf("trecho indisponível")
		}
		if d.Preco <= 0 || math.IsNaN(d.Preco) || math.IsInf(d.Preco, 0) || math.Trunc(d.Preco) != d.Preco || d.Preco >= 1<<53 {
			return out, p, fmt.Errorf("preço deve ser inteiro positivo em tokens")
		}
		v := uint64(d.Preco)
		if math.MaxUint64-p.ValorTotal < v {
			return out, p, fmt.Errorf("total excedido")
		}
		p.ValorTotal += v
		d.ServidorURL = srv
		out.Trechos = append(out.Trechos, d)
		out.PrecoTotal += d.Preco
		p.Trechos = append(p.Trechos, blockchain.PagamentoTrecho{TrechoID: fmt.Sprintf("%s-%d", id, i), Motorista: end, Valor: v})
	}
	return out, p, nil
}
func CotarReservaTokens(email string, it Itinerario) (CotacaoReserva, error) {
	var c CotacaoReserva
	if cadeiaIndisponivel() {
		return c, fmt.Errorf("ledger indisponível")
	}
	if err := sincronizarLedger(); err != nil {
		return c, err
	}
	end, err := enderecoDoUsuario(email)
	if err != nil {
		return c, err
	}
	var b [16]byte
	if _, err = rand.Read(b[:]); err != nil {
		return c, err
	}
	c.Itinerario, c.Escrow, err = cotarTrechos(it, "RES-"+hex.EncodeToString(b[:]))
	if err != nil {
		return c, err
	}
	c.Saldo = CadeiaLocal.Estado().Saldo(end)
	c.Nonce = CadeiaLocal.Estado().ProximoNonce(end)
	if c.Saldo < c.Escrow.ValorTotal {
		return c, fmt.Errorf("saldo insuficiente: possui %d tokens, necessita %d", c.Saldo, c.Escrow.ValorTotal)
	}
	return c, nil
}
func ReservarComTokens(email string, req ReservaComTokens) (*Reserva, error) {
	if cadeiaIndisponivel() {
		return nil, fmt.Errorf("ledger indisponível")
	}
	end, err := enderecoDoUsuario(email)
	if err != nil {
		return nil, err
	}
	if req.Transacao.Tipo != blockchain.TxCriarEscrow || req.Transacao.Remetente != end {
		return nil, fmt.Errorf("transação não autorizada")
	}
	if err = req.Transacao.ValidarAssinatura(); err != nil {
		return nil, err
	}
	var p blockchain.PayloadCriarEscrow
	if err = json.Unmarshal(req.Transacao.Payload, &p); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(p.ReservaID, "RES-") || len(p.ReservaID) != 36 {
		return nil, fmt.Errorf("identificador inválido")
	}
	if _, err = hex.DecodeString(p.ReservaID[4:]); err != nil {
		return nil, err
	}
	it, esperado, err := cotarTrechos(req.Itinerario, p.ReservaID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(p, esperado) {
		return nil, fmt.Errorf("cotação alterada; solicite nova cotação")
	}
	if err = sincronizarLedger(); err != nil {
		return nil, err
	}
	if err = CadeiaLocal.Estado().ValidarTransacao(req.Transacao); err != nil {
		return nil, err
	}
	return coordenarReserva2PC(email, it, p.ReservaID, func() error {
		muCadeia.Lock()
		defer muCadeia.Unlock()
		_, err := submeterTransacaoLocked(req.Transacao)
		return err
	})
}

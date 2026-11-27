package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"vaiJunto/blockchain"
)

const AcaoConcluirViagem TipoAcao = "CONCLUIR_VIAGEM"

// Validacao da conclusao no servidor de origem; o passageiro declara a chegada.
func validarConclusaoReserva(r Reserva, agora time.Time) error {
	if len(r.Itinerario) == 0 {
		return fmt.Errorf("reserva sem trechos")
	}
	for _, t := range r.Itinerario {
		if t.ServidorOffline || t.HorarioChegada.IsZero() || agora.Before(t.HorarioChegada) {
			return fmt.Errorf("trecho ainda não concluído ou indisponível")
		}
	}
	return nil
}

func reservaConfirmadaEmTodos(r Reserva) error {
	vistos := map[string]bool{}
	for _, t := range r.Itinerario {
		srv := strings.TrimRight(t.ServidorURL, "/")
		if srv == "" || srv == ServidorAtualURL || vistos[srv] {
			continue
		}
		vistos[srv] = true
		muPeers.RLock()
		_, ok := PeersConsorcio[srv]
		muPeers.RUnlock()
		if !ok {
			return fmt.Errorf("servidor desconhecido")
		}
		resp, err := clienteHTTP.Get(srv + "/api/reservas/status?reserva_id=" + url.QueryEscape(r.ID))
		if err != nil {
			return err
		}
		var status struct {
			Existe bool `json:"existe"`
		}
		err = json.NewDecoder(resp.Body).Decode(&status)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || !status.Existe {
			return fmt.Errorf("reserva não confirmada em todos os servidores")
		}
	}
	return nil
}

func OperarEscrowReserva(req MensagemRequisicao) MensagemResposta {
	if cadeiaIndisponivel() {
		return respostaFalha("ledger indisponível")
	}
	var tx blockchain.Transacao
	if err := json.Unmarshal(req.Payload, &tx); err != nil {
		return respostaFalha("transação inválida")
	}
	tipo := blockchain.TxCancelarReserva
	if req.Acao == AcaoConcluirViagem {
		tipo = blockchain.TxConcluirViagem
	}
	if tx.Tipo != tipo {
		return respostaFalha("tipo inválido")
	}
	end, err := enderecoDoUsuario(req.Usuario)
	if err != nil || end != tx.Remetente {
		return respostaFalha("titular inválido")
	}
	if err = tx.ValidarAssinatura(); err != nil {
		return respostaFalha(err.Error())
	}
	var p blockchain.PayloadConcluirViagem
	if err = json.Unmarshal(tx.Payload, &p); err != nil || p.ReservaID == "" {
		return respostaFalha("reserva inválida")
	}
	muCoordenador2PC.Lock()
	defer muCoordenador2PC.Unlock()
	muCadeia.Lock()
	defer muCadeia.Unlock()
	if err = sincronizarLedger(); err != nil {
		return respostaFalha(err.Error())
	}
	x, ok := CadeiaLocal.Estado().Escrow(p.ReservaID)
	if !ok || x.Passageiro != end {
		return respostaFalha("escrow não encontrado para este titular")
	}
	if x.Concluido || x.Cancelado {
		return respostaFalha("escrow já finalizado")
	}
	if tipo == blockchain.TxConcluirViagem {
		if err = validarConclusaoLocal(tx); err != nil {
			return respostaFalha(err.Error())
		}
	}

	if _, err = submeterTransacaoLocked(tx); err != nil {
		return respostaFalha(err.Error())
	}
	if tipo == blockchain.TxCancelarReserva {
		// O estado terminal está no ledger. Cada participante reconcilia sua reserva local.
		reconciliarEscrowsCancelados()
		return respostaOK("Escrow cancelado; tokens devolvidos. Liberação dos assentos em reconciliação.", visaoSaldo(end))
	}
	return respostaOK("Viagem concluída; pagamento distribuído atomicamente.", visaoSaldo(end))
}

// Reexecutada no heartbeat: não cria transações financeiras nem depende de chaves do passageiro.
func reconciliarEscrowsCancelados() {
	if CadeiaLocal == nil || CadeiaLocal.Estado() == nil {
		return
	}
	mu.Lock()
	rs, err := CarregarReservas()
	mu.Unlock()
	if err != nil {
		return
	}
	for _, r := range rs {
		x, ok := CadeiaLocal.Estado().Escrow(r.ID)
		if ok && x.Cancelado {
			LiberarAssentosLocais(r.ID, r.Itinerario)
		}
	}
}

// Valida usando dados do coordenador da reserva e consultas aos participantes.
func validarConclusaoLocal(tx blockchain.Transacao) error {
	if tx.Tipo != blockchain.TxConcluirViagem {
		return fmt.Errorf("tipo inválido")
	}
	if err := tx.ValidarAssinatura(); err != nil {
		return err
	}
	var p blockchain.PayloadConcluirViagem
	if err := json.Unmarshal(tx.Payload, &p); err != nil {
		return err
	}
	mu.Lock()
	rs, err := CarregarReservas()
	cs, errC := CarregarCaronas()
	mu.Unlock()
	if err != nil {
		return err
	}
	if errC != nil {
		return errC
	}
	for _, r := range rs {
		if r.ID != p.ReservaID || r.CoordenadorURL != ServidorAtualURL {
			continue
		}
		end, err := enderecoDoUsuario(r.PassageiroID)
		if err != nil || end != tx.Remetente {
			return fmt.Errorf("titular inválido")
		}
		for _, tr := range r.Itinerario {
			if tr.ServidorURL != "" && tr.ServidorURL != ServidorAtualURL {
				continue
			}
			ativa := false
			for _, c := range cs {
				if c.ID == tr.CaronaID && c.Ativa {
					ativa = true
					break
				}
			}
			if !ativa {
				return fmt.Errorf("carona local cancelada ou inexistente")
			}
		}
		if err = reservaConfirmadaEmTodos(r); err != nil {
			return err
		}
		PreencherDetalhesRemotosReserva(&r)
		return validarConclusaoReserva(r, time.Now())
	}
	return fmt.Errorf("reserva confirmada não encontrada neste coordenador")
}

// O líder financeiro não aceita conclusão pela API de transações sem consultar
// o servidor coordenador da reserva. A rede do consórcio é considerada confiável.
func autorizarConclusaoDistribuida(tx blockchain.Transacao) error {
	if tx.Tipo != blockchain.TxConcluirViagem {
		return nil
	}
	if validarConclusaoLocal(tx) == nil {
		return nil
	}
	muPeers.RLock()
	peers := make([]string, 0, len(PeersConsorcio))
	for p := range PeersConsorcio {
		peers = append(peers, p)
	}
	muPeers.RUnlock()
	raw, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	for _, p := range peers {
		resp, err := clienteHTTP.Post(p+"/api/ledger/validar-conclusao", "application/json", bytes.NewReader(raw))
		if err != nil {
			continue
		}
		var ok bool
		err = json.NewDecoder(resp.Body).Decode(&ok)
		resp.Body.Close()
		if err == nil && resp.StatusCode == http.StatusOK && ok {
			return nil
		}
	}
	return fmt.Errorf("nenhum coordenador confirmou a conclusão da reserva")
}
func handlerValidarConclusao(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método inválido", 405)
		return
	}
	var tx blockchain.Transacao
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&tx); err != nil {
		http.Error(w, "transação inválida", 400)
		return
	}
	if err := validarConclusaoLocal(tx); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(true)
}

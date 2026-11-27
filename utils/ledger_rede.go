package utils

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"vaiJunto/blockchain"
)

var redeFinanceira *blockchain.RedeLedger

func iniciarLedgerDistribuido(mux *http.ServeMux) error {
	muPeers.RLock()
	peers := make([]string, 0, len(PeersConsorcio))
	for p := range PeersConsorcio {
		peers = append(peers, p)
	}
	muPeers.RUnlock()
	if len(peers) == 0 {
		return nil
	}
	lider := os.Getenv("VJ_LEDGER_LIDER_ID")
	if lider == "" {
		lider = "empresaA"
	}
	publica := os.Getenv("VJ_LEDGER_LIDER_PUBLICA")
	endereco := strings.TrimRight(os.Getenv("VJ_LEDGER_LIDER_URL"), "/")
	if publica == "" || endereco == "" {
		return fmt.Errorf("configure VJ_LEDGER_LIDER_PUBLICA, VJ_LEDGER_LIDER_URL e VJ_EMISSORES em todos os servidores")
	}
	r := &blockchain.RedeLedger{Cadeia: CadeiaLocal, ID: EmpresaAtualID, LiderID: lider, LiderURL: endereco, LiderPublica: publica, Carteira: CarteiraValidador, Peers: peers}
	h, err := r.Handler()
	if err != nil {
		return err
	}
	redeFinanceira = r
	mux.Handle("/ledger/", h)
	mux.HandleFunc("/api/ledger/endereco", func(w http.ResponseWriter, q *http.Request) {
		if q.Method != http.MethodGet {
			http.Error(w, "método inválido", 405)
			return
		}
		email := strings.TrimSpace(q.URL.Query().Get("email"))
		end, err := enderecoDoUsuario(email)
		if err != nil {
			http.NotFound(w, q)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RespostaEndereco{Email: email, Endereco: end})
	})
	go func() {
		for {
			_ = r.Sincronizar()
			time.Sleep(2 * time.Second)
		}
	}()
	return nil
}
func sincronizarLedger() error {
	if redeFinanceira == nil {
		return nil
	}
	if err := redeFinanceira.Sincronizar(); err != nil {
		return fmt.Errorf("ledger indisponível: %w", err)
	}
	return nil
}
func submeterDistribuido(tx blockchain.Transacao) (blockchain.Bloco, error) {
	r, err := redeFinanceira.Enviar(tx)
	if err != nil {
		return blockchain.Bloco{}, err
	}
	if err = sincronizarLedger(); err != nil {
		return blockchain.Bloco{}, fmt.Errorf("transação gravada no coordenador; sincronização pendente: %w", err)
	}
	if !r.Confirmada {
		return blockchain.Bloco{}, fmt.Errorf("transação gravada no coordenador, replicação pendente; consulte o saldo antes de repetir")
	}
	for _, b := range CadeiaLocal.ObterBlocos() {
		for _, t := range b.Transacoes {
			if t.ID == tx.ID {
				return b, nil
			}
		}
	}
	return blockchain.Bloco{}, fmt.Errorf("transação ainda não sincronizada")
}
func buscarEnderecoRemoto(email string) (string, error) {
	muPeers.RLock()
	peers := make([]string, 0, len(PeersConsorcio))
	for p := range PeersConsorcio {
		peers = append(peers, p)
	}
	muPeers.RUnlock()
	encontrado := ""
	incompleto := false
	for _, p := range peers {
		resp, err := clienteHTTP.Get(p + "/api/ledger/endereco?email=" + url.QueryEscape(strings.TrimSpace(email)))
		if err != nil {
			incompleto = true
			continue
		}
		var v RespostaEndereco
		if resp.StatusCode == 200 {
			err = json.NewDecoder(resp.Body).Decode(&v)
			if err == nil && v.Endereco != "" {
				if encontrado != "" && encontrado != v.Endereco {
					resp.Body.Close()
					return "", fmt.Errorf("e-mail ambíguo; use o endereço VJ-...")
				}
				encontrado = v.Endereco
			} else {
				incompleto = true
			}
		} else if resp.StatusCode != 404 {
			incompleto = true
		}
		resp.Body.Close()
	}
	if incompleto {
		return "", fmt.Errorf("busca incompleta: servidor indisponível; use o endereço VJ-...")
	}
	if encontrado == "" {
		return "", fmt.Errorf("destinatário não encontrado")
	}
	return encontrado, nil
}

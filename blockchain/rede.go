package blockchain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RedeLedger usa um coordenador fixo: somente ele ordena operações financeiras.
// Os demais nós recebem a cadeia assinada. Não é Raft nem oferece failover.
type RedeLedger struct {
	Cadeia                              *Blockchain
	ID, LiderID, LiderURL, LiderPublica string
	Carteira                            *Carteira
	Peers                               []string
	AutorizarTransacao                  func(Transacao) error
	mu                                  sync.Mutex
	cliente                             *http.Client
}

func (r *RedeLedger) validar(blocos []Bloco) error {
	if err := ValidarCadeia(blocos); err != nil {
		return err
	}
	for _, b := range blocos[1:] {
		if b.ProponenteID != r.LiderID || b.ProponentePubKey != r.LiderPublica {
			return fmt.Errorf("proponente não autorizado")
		}
	}
	return nil
}
func (r *RedeLedger) aceitar(blocos []Bloco) error {
	if err := r.validar(blocos); err != nil {
		return err
	}
	local := r.Cadeia.ObterBlocos()
	if len(blocos) < len(local) {
		return fmt.Errorf("cadeia antiga")
	}
	for i, b := range local {
		if blocos[i].Hash != b.Hash {
			return fmt.Errorf("histórico divergente")
		}
	}
	if len(blocos) == len(local) {
		return nil
	}
	return r.Cadeia.SubstituirCadeia(blocos)
}
func (r *RedeLedger) request(method, url string, entrada, saida interface{}) error {
	var body bytes.Buffer
	if entrada != nil {
		if err := json.NewEncoder(&body).Encode(entrada); err != nil {
			return err
		}
	}
	req, err := http.NewRequest(method, strings.TrimRight(url, "/"), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.cliente.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d de %s: %s", resp.StatusCode, url, mensagemHTTP(resp))
	}
	return json.NewDecoder(resp.Body).Decode(saida)
}

type ResultadoTransferencia struct {
	ID         string `json:"id"`
	Confirmada bool   `json:"confirmada"`
	Replicas   int    `json:"replicas"`
	Mensagem   string `json:"mensagem"`
}

func (r *RedeLedger) replicar() int {
	n := 1
	blocos := r.Cadeia.ObterBlocos()
	vistos := map[string]bool{}
	for _, p := range r.Peers {
		p = strings.TrimRight(p, "/")
		if p == "" || vistos[p] {
			continue
		}
		vistos[p] = true
		var ok bool
		if r.request("POST", p+"/ledger/replicar", blocos, &ok) == nil && ok {
			n++
		}
	}
	return n
}
func (r *RedeLedger) enviar(tx Transacao) (ResultadoTransferencia, error) {
	if r.ID != r.LiderID {
		var out ResultadoTransferencia
		err := r.request("POST", r.LiderURL+"/ledger/transacao", tx, &out)
		return out, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := tx.ValidarAssinatura(); err != nil {
		return ResultadoTransferencia{}, err
	}
	if tx.Tipo != TxTransferirTokens && tx.Tipo != TxEmitirTokens && tx.Tipo != TxCriarEscrow && tx.Tipo != TxConcluirViagem && tx.Tipo != TxCancelarReserva {
		return ResultadoTransferencia{}, fmt.Errorf("tipo não suportado")
	}
	existe := false
	for _, b := range r.Cadeia.ObterBlocos() {
		for _, t := range b.Transacoes {
			if t.ID == tx.ID {
				existe = true
			}
		}
	}
	if !existe {
		if r.AutorizarTransacao != nil {
			if err := r.AutorizarTransacao(tx); err != nil {
				return ResultadoTransferencia{}, err
			}
		}
		if err := r.Cadeia.Estado().ValidarTransacao(tx); err != nil {
			return ResultadoTransferencia{}, err
		}
		b, err := NovoBloco(r.Cadeia.UltimoBloco(), []Transacao{tx}, r.ID, r.Carteira, time.Now().Unix())
		if err != nil {
			return ResultadoTransferencia{}, err
		}
		if err = r.Cadeia.AdicionarBloco(b); err != nil {
			return ResultadoTransferencia{}, err
		}
	}
	n := r.replicar()
	out := ResultadoTransferencia{ID: tx.ID, Confirmada: n >= 2, Replicas: n, Mensagem: "gravada no coordenador; replicação pendente"}
	if out.Confirmada {
		out.Mensagem = "gravada e replicada em pelo menos dois nós"
	}
	return out, nil
}
func (r *RedeLedger) Handler() (http.Handler, error) {
	if r.Cadeia == nil || r.Cadeia.Estado() == nil || r.LiderPublica == "" || r.LiderID == "" || r.ID == "" {
		return nil, fmt.Errorf("configuração incompleta")
	}
	if r.ID == r.LiderID && (r.Carteira == nil || r.Carteira.ChavePublica != r.LiderPublica) {
		return nil, fmt.Errorf("chave do líder incorreta")
	}
	if err := r.validar(r.Cadeia.ObterBlocos()); err != nil {
		return nil, err
	}
	r.cliente = &http.Client{Timeout: 3 * time.Second}
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v interface{}) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /ledger/transacao", func(w http.ResponseWriter, q *http.Request) {
		var tx Transacao
		if err := json.NewDecoder(http.MaxBytesReader(w, q.Body, 1<<20)).Decode(&tx); err != nil {
			http.Error(w, "JSON inválido", 400)
			return
		}
		out, err := r.enviar(tx)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		write(w, out)
	})
	mux.HandleFunc("POST /ledger/replicar", func(w http.ResponseWriter, q *http.Request) {
		if r.ID == r.LiderID {
			http.Error(w, "coordenador não aceita replicação", 403)
			return
		}
		var b []Bloco
		if err := json.NewDecoder(http.MaxBytesReader(w, q.Body, 32<<20)).Decode(&b); err != nil {
			http.Error(w, "JSON inválido", 400)
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if err := r.aceitar(b); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		write(w, true)
	})
	mux.HandleFunc("GET /ledger/cadeia", func(w http.ResponseWriter, q *http.Request) { write(w, r.Cadeia.ObterBlocos()) })
	mux.HandleFunc("GET /ledger/saldo", func(w http.ResponseWriter, q *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		end := q.URL.Query().Get("endereco")
		e := r.Cadeia.Estado()
		write(w, map[string]uint64{"saldo": e.Saldo(end), "nonce": e.ProximoNonce(end)})
	})
	return mux, nil
}

// Sincronizar recupera nós atrasados sem aceitar bifurcações.
func (r *RedeLedger) Sincronizar() error {
	if r.ID == r.LiderID {
		return nil
	}
	var b []Bloco
	if err := r.request("GET", r.LiderURL+"/ledger/cadeia", nil, &b); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(b) < r.Cadeia.Altura() {
		return nil
	}
	return r.aceitar(b)
}

func mensagemHTTP(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return strings.TrimSpace(string(b))
}
func (r *RedeLedger) Enviar(tx Transacao) (ResultadoTransferencia, error) { return r.enviar(tx) }

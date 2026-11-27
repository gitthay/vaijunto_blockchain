package blockchain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Erros sentinela do protocolo de consenso distribuído.
var (
	ErrQuorumNaoAtingido       = errors.New("quórum mínimo de validadores não atingido")
	ErrValidadorNaoAutorizado  = errors.New("validador não autorizado no consórcio")
	ErrPropostaRejeitada       = errors.New("bloco proposto rejeitado na validação")
	ErrCadeiaAdulterada        = errors.New("cadeia adulterada ou sem quórum válido")
	ErrSemTransacoesParaBloco  = errors.New("nenhuma transação informada para compor o bloco")
)

// ConfigConsenso define os parâmetros de um nó validador no consórcio permissionado.
type ConfigConsenso struct {
	ValidadorID            string
	Carteira               *Carteira
	Cadeia                 *Blockchain
	Peers                  []string          // URLs HTTP dos demais nós do consórcio
	TotalValidadores       int               // Total de empresas do consórcio (ex.: 4 -> quórum BFT = 3)
	ValidadoresAutorizados map[string]string // Opcional: mapa endereco -> chavePublicaHex dos nós autorizados
	TimeoutHTTP            time.Duration
}

// IdentidadeNo descreve a identidade pública e o estado do topo da cadeia de um nó do consórcio.
type IdentidadeNo struct {
	ValidadorID  string `json:"validador_id"`
	Endereco     string `json:"endereco"`
	ChavePublica string `json:"chave_publica"`
	Altura       int    `json:"altura"`
	HashTopo     string `json:"hash_topo"`
	QuorumMinimo int    `json:"quorum_minimo"`
}

// NoConsenso coordena a proposição de blocos, votação assinada (Ed25519),
// tolerância à queda de nós, sincronização pós-queda e rejeição automática de históricos adulterados.
type NoConsenso struct {
	mu                     sync.Mutex
	validadorID            string
	carteira               *Carteira
	cadeia                 *Blockchain
	peers                  []string
	totalValidadores       int
	validadoresAutorizados map[string]string
	clienteHTTP            *http.Client
}

// QuorumMinimo calcula o número mínimo de assinaturas de validadores para confirmar um bloco.
// Para N=4 empresas (3f+1, f=1), retorna 3 (2f+1): tolera 1 nó fora do ar OU 1 nó adulterado.
// Para N=3, retorna 2 (maioria simples). Para N<=1, retorna 1.
func QuorumMinimo(totalValidadores int) int {
	if totalValidadores <= 1 {
		return 1
	}
	if totalValidadores == 2 {
		return 2
	}
	// Fórmula BFT: floor(2*N/3) + 1
	// N=3 -> 3? Não: para N=3, 2f+1 com f=1 é 3 (não tolera 1 queda).
	// Para N=3 usamos maioria simples (2); para N>=4 usamos BFT clássico (2*N/3 + 1):
	// N=4 -> 3, N=5 -> 4, N=6 -> 5, N=7 -> 5.
	if totalValidadores == 3 {
		return 2
	}
	return (2*totalValidadores)/3 + 1
}

// NovoNoConsenso inicializa um nó de consenso pronto para operar na rede P2P HTTP do consórcio.
func NovoNoConsenso(cfg ConfigConsenso) (*NoConsenso, error) {
	if cfg.Carteira == nil {
		return nil, errors.New("carteira do validador é obrigatória para o consenso")
	}
	if cfg.Cadeia == nil {
		return nil, errors.New("instância da blockchain é obrigatória para o consenso")
	}
	if strings.TrimSpace(cfg.ValidadorID) == "" {
		return nil, errors.New("validadorID é obrigatório para o consenso")
	}

	peersLimpos := make([]string, 0, len(cfg.Peers))
	for _, p := range cfg.Peers {
		if u := strings.TrimRight(strings.TrimSpace(p), "/"); u != "" {
			peersLimpos = append(peersLimpos, u)
		}
	}

	total := cfg.TotalValidadores
	if total <= 0 {
		total = len(peersLimpos) + 1
	}

	autorizados := make(map[string]string, len(cfg.ValidadoresAutorizados))
	for end, pub := range cfg.ValidadoresAutorizados {
		if strings.TrimSpace(end) != "" && strings.TrimSpace(pub) != "" {
			autorizados[strings.TrimSpace(end)] = strings.TrimSpace(pub)
		}
	}

	timeout := cfg.TimeoutHTTP
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	return &NoConsenso{
		validadorID:            cfg.ValidadorID,
		carteira:               cfg.Carteira,
		cadeia:                 cfg.Cadeia,
		peers:                  peersLimpos,
		totalValidadores:       total,
		validadoresAutorizados: autorizados,
		clienteHTTP:            &http.Client{Timeout: timeout},
	}, nil
}

// Quorum retorna o quórum mínimo exigido por este nó para confirmar blocos.
func (n *NoConsenso) Quorum() int {
	return QuorumMinimo(n.totalValidadores)
}

// AtualizarPeers atualiza a lista de URLs dos pares (útil quando os peers são configurados dinamicamente).
func (n *NoConsenso) AtualizarPeers(peers []string, totalValidadores int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	peersLimpos := make([]string, 0, len(peers))
	for _, p := range peers {
		if u := strings.TrimRight(strings.TrimSpace(p), "/"); u != "" {
			peersLimpos = append(peersLimpos, u)
		}
	}
	n.peers = peersLimpos
	if totalValidadores > 0 {
		n.totalValidadores = totalValidadores
	} else {
		n.totalValidadores = len(peersLimpos) + 1
	}
}

// Identidade retorna o resumo público da identidade e do estado atual da cadeia deste nó.
func (n *NoConsenso) Identidade() IdentidadeNo {
	topo := n.cadeia.UltimoBloco()
	return IdentidadeNo{
		ValidadorID:  n.validadorID,
		Endereco:     n.carteira.Endereco,
		ChavePublica: n.carteira.ChavePublica,
		Altura:       n.cadeia.Altura(),
		HashTopo:     topo.Hash,
		QuorumMinimo: n.Quorum(),
	}
}

// ValidarQuorumBloco verifica se o bloco possui assinaturas válidas e distintas
// em quantidade >= QuorumMinimo, respeitando a lista de validadores autorizados (quando definida).
func (n *NoConsenso) ValidarQuorumBloco(bloco Bloco) error {
	if bloco.Indice == 0 {
		return nil
	}

	if err := n.verificarValidadorAutorizado(bloco.ProponenteEndereco, bloco.ProponentePubKey); err != nil {
		return fmt.Errorf("proponente do bloco #%d rejeitado: %w", bloco.Indice, err)
	}

	vistos := make(map[string]bool)
	votosValidos := 0
	for i, voto := range bloco.Votos {
		if vistos[voto.Endereco] {
			return fmt.Errorf("bloco #%d contém voto duplicado de %s", bloco.Indice, voto.Endereco)
		}
		if err := n.verificarValidadorAutorizado(voto.Endereco, voto.ChavePublica); err != nil {
			return fmt.Errorf("voto #%d no bloco #%d rejeitado: %w", i, bloco.Indice, err)
		}
		if err := ValidarIdentidadeAssinada(voto.Endereco, voto.ChavePublica, []byte(bloco.Hash), voto.Assinatura); err != nil {
			return fmt.Errorf("assinatura inválida no voto #%d do bloco #%d: %w", i, bloco.Indice, err)
		}
		vistos[voto.Endereco] = true
		votosValidos++
	}

	quorum := n.Quorum()
	if votosValidos < quorum {
		return fmt.Errorf("%w: bloco #%d possui %d voto(s) válido(s), mínimo exigido %d",
			ErrQuorumNaoAtingido, bloco.Indice, votosValidos, quorum)
	}
	return nil
}

// ValidarCadeiaComQuorum valida integralmente estrutura, assinaturas, quórum de todos os blocos (>0)
// e coerência na máquina de estados (quando ativa).
func (n *NoConsenso) ValidarCadeiaComQuorum(blocos []Bloco) error {
	if err := ValidarCadeia(blocos); err != nil {
		return fmt.Errorf("%w: %v", ErrCadeiaAdulterada, err)
	}
	for i := 1; i < len(blocos); i++ {
		if err := n.ValidarQuorumBloco(blocos[i]); err != nil {
			return fmt.Errorf("%w: %v", ErrCadeiaAdulterada, err)
		}
	}
	if n.cadeia.Estado() != nil {
		if _, err := ReconstruirEstado(blocos, n.cadeia.emissores...); err != nil {
			return fmt.Errorf("%w: falha na máquina de estado: %v", ErrCadeiaAdulterada, err)
		}
	}
	return nil
}

// ProporEConfirmarBloco cria um bloco candidato com as transações informadas, coleta votos
// assinados dos peers em paralelo, verifica o quórum e confirma o bloco em toda a rede.
func (n *NoConsenso) ProporEConfirmarBloco(transacoes []Transacao) (Bloco, error) {
	if len(transacoes) == 0 {
		return Bloco{}, ErrSemTransacoesParaBloco
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	// 1. Pré-valida transações na máquina de estados local (rejeita gasto duplo/nonce antes de propor)
	if est := n.cadeia.Estado(); est != nil {
		if err := est.ValidarTransacoes(transacoes); err != nil {
			return Bloco{}, fmt.Errorf("%w: %v", ErrPropostaRejeitada, err)
		}
	}

	// 2. Cria bloco candidato encadeado ao topo atual (já contém o 1º voto: o do proponente)
	bloco, err := NovoBloco(n.cadeia.UltimoBloco(), transacoes, n.validadorID, n.carteira, time.Now().Unix())
	if err != nil {
		return Bloco{}, fmt.Errorf("%w: %v", ErrPropostaRejeitada, err)
	}

	// 3. Solicita votos assinados aos pares em paralelo
	votosColetados := n.coletarVotosPeers(bloco)
	vistos := map[string]bool{n.carteira.Endereco: true}
	for _, voto := range votosColetados {
		if vistos[voto.Endereco] {
			continue
		}
		if err := n.verificarValidadorAutorizado(voto.Endereco, voto.ChavePublica); err != nil {
			continue
		}
		if err := ValidarIdentidadeAssinada(voto.Endereco, voto.ChavePublica, []byte(bloco.Hash), voto.Assinatura); err != nil {
			continue
		}
		vistos[voto.Endereco] = true
		bloco.Votos = append(bloco.Votos, voto)
	}

	// 4. Exige quórum mínimo antes de confirmar
	if err := n.ValidarQuorumBloco(bloco); err != nil {
		return Bloco{}, err
	}

	// 5. Persiste localmente
	if err := n.cadeia.AdicionarBloco(bloco); err != nil {
		return Bloco{}, err
	}

	// 6. Propaga bloco confirmado (com todos os votos) para os demais nós
	n.propagarBlocoConfirmado(bloco)

	return bloco, nil
}

// AvaliarProposta valida um bloco proposto por outro nó e, se estiver íntegro e válido
// na máquina de estados, retorna o voto assinado deste validador.
func (n *NoConsenso) AvaliarProposta(bloco Bloco) (VotoValidador, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := n.verificarValidadorAutorizado(bloco.ProponenteEndereco, bloco.ProponentePubKey); err != nil {
		return VotoValidador{}, fmt.Errorf("%w: %v", ErrPropostaRejeitada, err)
	}

	ultimo := n.cadeia.UltimoBloco()
	if err := bloco.ValidarEstrutura(&ultimo); err != nil {
		return VotoValidador{}, fmt.Errorf("%w: %v", ErrPropostaRejeitada, err)
	}

	if est := n.cadeia.Estado(); est != nil {
		if err := est.ValidarTransacoes(bloco.Transacoes); err != nil {
			return VotoValidador{}, fmt.Errorf("%w: %v", ErrPropostaRejeitada, err)
		}
	}

	return bloco.AssinarVoto(n.validadorID, n.carteira)
}

// ReceberBlocoConfirmado valida estrutura, quórum de votos e máquina de estados de um bloco
// confirmado por outro nó, aplicando-o localmente de forma idempotente.
func (n *NoConsenso) ReceberBlocoConfirmado(bloco Bloco) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Idempotência: se o bloco exato já está gravado neste índice, nada a fazer
	if existente, ok := n.cadeia.ObterBlocoPorIndice(bloco.Indice); ok {
		if existente.Hash == bloco.Hash {
			return nil
		}
		return fmt.Errorf("%w: conflito de hash no índice #%d", ErrCadeiaAdulterada, bloco.Indice)
	}

	if err := n.ValidarQuorumBloco(bloco); err != nil {
		return err
	}

	return n.cadeia.AdicionarBloco(bloco)
}

// SincronizarComPeers consulta a cadeia de todos os peers online, rejeita automaticamente
// qualquer cadeia adulterada ou sem quórum e adota a cadeia válida mais longa.
// Retorna true se a cadeia local foi atualizada.
func (n *NoConsenso) SincronizarComPeers() (bool, error) {
	n.mu.Lock()
	peers := append([]string(nil), n.peers...)
	n.mu.Unlock()

	var melhorCadeia []Bloco
	alturaAtual := n.cadeia.Altura()

	for _, peerURL := range peers {
		cadeiaPeer, err := n.buscarCadeiaPeer(peerURL)
		if err != nil {
			continue // Peer offline ou indisponível
		}
		if len(cadeiaPeer) <= alturaAtual && len(cadeiaPeer) <= len(melhorCadeia) {
			continue
		}
		// Rejeita automaticamente qualquer cadeia adulterada, com assinatura inválida ou sem quórum
		if err := n.ValidarCadeiaComQuorum(cadeiaPeer); err != nil {
			continue
		}
		if len(cadeiaPeer) > len(melhorCadeia) && len(cadeiaPeer) > alturaAtual {
			melhorCadeia = cadeiaPeer
		}
	}

	if len(melhorCadeia) == 0 {
		return false, nil
	}

	if err := n.cadeia.SubstituirCadeia(melhorCadeia); err != nil {
		return false, err
	}
	return true, nil
}

// ============================================================================
// Comunicação HTTP P2P do Consenso
// ============================================================================

// RegistrarRotasHTTP acopla os endpoints REST da blockchain ao mux do servidor da empresa.
func (n *NoConsenso) RegistrarRotasHTTP(mux *http.ServeMux) {
	mux.HandleFunc("/api/blockchain/identidade", n.handlerIdentidade)
	mux.HandleFunc("/api/blockchain/propor", n.handlerPropor)
	mux.HandleFunc("/api/blockchain/confirmar", n.handlerConfirmar)
	mux.HandleFunc("/api/blockchain/cadeia", n.handlerCadeia)
	mux.HandleFunc("/api/blockchain/estado", n.handlerEstado)
}

func (n *NoConsenso) handlerIdentidade(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(n.Identidade())
}

func (n *NoConsenso) handlerPropor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	var bloco Bloco
	if err := json.NewDecoder(r.Body).Decode(&bloco); err != nil {
		http.Error(w, "bloco JSON inválido", http.StatusBadRequest)
		return
	}
	voto, err := n.AvaliarProposta(bloco)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(voto)
}

func (n *NoConsenso) handlerConfirmar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	var bloco Bloco
	if err := json.NewDecoder(r.Body).Decode(&bloco); err != nil {
		http.Error(w, "bloco JSON inválido", http.StatusBadRequest)
		return
	}
	if err := n.ReceberBlocoConfirmado(bloco); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "CONFIRMADO",
		"indice": bloco.Indice,
		"hash":   bloco.Hash,
	})
}

func (n *NoConsenso) handlerCadeia(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(n.cadeia.ObterBlocos())
}

func (n *NoConsenso) handlerEstado(w http.ResponseWriter, r *http.Request) {
	est := n.cadeia.Estado()
	if est == nil {
		http.Error(w, "máquina de estado não habilitada", http.StatusNotFound)
		return
	}
	snap := est.Snapshot()
	topo := n.cadeia.UltimoBloco()
	snap.Altura = n.cadeia.Altura()
	snap.HashTopo = topo.Hash

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

func (n *NoConsenso) coletarVotosPeers(bloco Bloco) []VotoValidador {
	payload, err := json.Marshal(bloco)
	if err != nil {
		return nil
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		votos []VotoValidador
	)

	for _, peerURL := range n.peers {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			resp, err := n.clienteHTTP.Post(url+"/api/blockchain/propor", "application/json", bytes.NewReader(payload))
			if err != nil || resp == nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return
			}
			var voto VotoValidador
			if err := json.NewDecoder(resp.Body).Decode(&voto); err != nil {
				return
			}
			mu.Lock()
			votos = append(votos, voto)
			mu.Unlock()
		}(peerURL)
	}
	wg.Wait()
	return votos
}

func (n *NoConsenso) propagarBlocoConfirmado(bloco Bloco) {
	payload, err := json.Marshal(bloco)
	if err != nil {
		return
	}

	var wg sync.WaitGroup
	for _, peerURL := range n.peers {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			resp, err := n.clienteHTTP.Post(url+"/api/blockchain/confirmar", "application/json", bytes.NewReader(payload))
			if err == nil && resp != nil {
				resp.Body.Close()
			}
		}(peerURL)
	}
	wg.Wait()
}

func (n *NoConsenso) buscarCadeiaPeer(peerURL string) ([]Bloco, error) {
	resp, err := n.clienteHTTP.Get(peerURL + "/api/blockchain/cadeia")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status HTTP %d ao consultar cadeia em %s", resp.StatusCode, peerURL)
	}
	var blocos []Bloco
	if err := json.NewDecoder(resp.Body).Decode(&blocos); err != nil {
		return nil, err
	}
	return blocos, nil
}

func (n *NoConsenso) verificarValidadorAutorizado(endereco, chavePublica string) error {
	if len(n.validadoresAutorizados) == 0 {
		return nil
	}
	pubEsperada, ok := n.validadoresAutorizados[endereco]
	if !ok || pubEsperada != chavePublica {
		return fmt.Errorf("%w: %s", ErrValidadorNaoAutorizado, endereco)
	}
	return nil
}

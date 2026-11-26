package blockchain

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Blockchain gerencia a cadeia local de blocos de uma empresa do consórcio
// com validação estrutural contínua e persistência isolada em disco.
type Blockchain struct {
	mu             sync.RWMutex
	blocos         []Bloco
	caminhoArquivo string

	// estado é opcional: quando presente (NovaBlockchainComEstado), todo bloco só é
	// aceito se suas transações forem válidas na máquina de estado (saldo e nonce).
	estado    *Estado
	emissores []string
}

// NovaBlockchain carrega uma blockchain existente de caminhoArquivo (validando toda a cadeia)
// ou cria uma nova iniciada pelo bloco gênesis determinístico e salva em disco.
func NovaBlockchain(caminhoArquivo string) (*Blockchain, error) {
	return novaBlockchain(caminhoArquivo, false, nil)
}

// NovaBlockchainComEstado é como NovaBlockchain, mas mantém a máquina de estado (saldos e
// nonces) derivada dos blocos e rejeita automaticamente blocos com transações inválidas
// (saldo insuficiente, nonce inválido, emissão por endereço não autorizado).
// Os emissores são os endereços autorizados a emitir tokens (EMITIR_TOKENS).
func NovaBlockchainComEstado(caminhoArquivo string, emissores ...string) (*Blockchain, error) {
	return novaBlockchain(caminhoArquivo, true, emissores)
}

func novaBlockchain(caminhoArquivo string, comEstado bool, emissores []string) (*Blockchain, error) {
	if strings.TrimSpace(caminhoArquivo) == "" {
		return nil, errors.New("caminho do arquivo da blockchain não pode ser vazio")
	}

	bc := &Blockchain{
		caminhoArquivo: caminhoArquivo,
	}
	if comEstado {
		bc.emissores = append([]string(nil), emissores...)
		bc.estado = NovoEstado(bc.emissores...)
	}

	if dados, err := os.ReadFile(caminhoArquivo); err == nil && len(dados) > 0 {
		var blocos []Bloco
		if err := json.Unmarshal(dados, &blocos); err != nil {
			return nil, fmt.Errorf("erro ao decodificar blockchain em %s: %w", caminhoArquivo, err)
		}
		if err := ValidarCadeia(blocos); err != nil {
			return nil, fmt.Errorf("blockchain local adulterada ou corrompida em %s: %w", caminhoArquivo, err)
		}
		if comEstado {
			estado, err := ReconstruirEstado(blocos, bc.emissores...)
			if err != nil {
				return nil, fmt.Errorf("estado inconsistente com a blockchain em %s: %w", caminhoArquivo, err)
			}
			bc.estado = estado
		}
		bc.blocos = blocos
		bc.salvarEstadoSemLock()
		return bc, nil
	}

	// Inicializa com o bloco gênesis determinístico
	bc.blocos = []Bloco{CriarBlocoGenesis()}
	if err := bc.salvarSemLock(); err != nil {
		return nil, err
	}
	bc.salvarEstadoSemLock()
	return bc, nil
}

// ValidarCadeia verifica toda a sequência de blocos desde o gênesis (índice 0) até o topo.
func ValidarCadeia(blocos []Bloco) error {
	if len(blocos) == 0 {
		return errors.New("cadeia vazia: bloco gênesis ausente")
	}

	for i := range blocos {
		var anterior *Bloco
		if i > 0 {
			anterior = &blocos[i-1]
		}
		if err := blocos[i].ValidarEstrutura(anterior); err != nil {
			return err
		}
	}
	return nil
}

// AdicionarBloco valida estruturalmente o novo bloco em relação ao topo atual,
// anexa à cadeia e persiste de forma atômica em disco.
func (bc *Blockchain) AdicionarBloco(bloco Bloco) error {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	ultimo := bc.blocos[len(bc.blocos)-1]
	if err := bloco.ValidarEstrutura(&ultimo); err != nil {
		return err
	}

	// Pré-validação na máquina de estado (somente leitura): se falhar, o bloco é rejeitado.
	if bc.estado != nil {
		if err := bc.estado.ValidarTransacoes(bloco.Transacoes); err != nil {
			return fmt.Errorf("bloco #%d rejeitado pela máquina de estado: %w", bloco.Indice, err)
		}
	}

	bc.blocos = append(bc.blocos, bloco)
	if err := bc.salvarSemLock(); err != nil {
		// Rollback em memória caso a escrita em disco falhe
		bc.blocos = bc.blocos[:len(bc.blocos)-1]
		return err
	}
	if bc.estado != nil {
		// Já validado acima sob o mesmo lock; não pode falhar.
		if err := bc.estado.AplicarBloco(bloco); err != nil {
			return fmt.Errorf("falha inesperada ao aplicar estado: %w", err)
		}
		bc.salvarEstadoSemLock()
	}
	return nil
}

// Estado retorna a máquina de estado da cadeia (nil se criada sem NovaBlockchainComEstado).
func (bc *Blockchain) Estado() *Estado {
	return bc.estado
}

// SubstituirCadeia substitui a cadeia local por uma cadeia recebida de pares (usado na sincronização pós-queda),
// aceitando somente se a nova cadeia for válida e estritamente mais longa.
func (bc *Blockchain) SubstituirCadeia(novaCadeia []Bloco) error {
	if err := ValidarCadeia(novaCadeia); err != nil {
		return fmt.Errorf("cadeia recebida rejeitada na validação: %w", err)
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()

	if len(novaCadeia) <= len(bc.blocos) {
		return errors.New("cadeia recebida não é mais longa que a cadeia local")
	}

	var novoEstado *Estado
	if bc.estado != nil {
		var err error
		novoEstado, err = ReconstruirEstado(novaCadeia, bc.emissores...)
		if err != nil {
			return fmt.Errorf("cadeia recebida rejeitada pela máquina de estado: %w", err)
		}
	}

	copia := make([]Bloco, len(novaCadeia))
	copy(copia, novaCadeia)

	anterior := bc.blocos
	bc.blocos = copia
	if err := bc.salvarSemLock(); err != nil {
		bc.blocos = anterior
		return err
	}
	if novoEstado != nil {
		bc.estado = novoEstado
		bc.salvarEstadoSemLock()
	}
	return nil
}

// UltimoBloco retorna uma cópia do último bloco confirmado na cadeia.
func (bc *Blockchain) UltimoBloco() Bloco {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.blocos[len(bc.blocos)-1]
}

// Altura retorna o número total de blocos (incluindo o gênesis).
func (bc *Blockchain) Altura() int {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return len(bc.blocos)
}

// ObterBlocos retorna uma cópia segura de todos os blocos da cadeia.
func (bc *Blockchain) ObterBlocos() []Bloco {
	bc.mu.RLock()
	defer bc.mu.RUnlock()

	copia := make([]Bloco, len(bc.blocos))
	copy(copia, bc.blocos)
	return copia
}

// ObterBlocoPorIndice retorna o bloco correspondente ao índice informado.
func (bc *Blockchain) ObterBlocoPorIndice(indice uint64) (Bloco, bool) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()

	if int(indice) >= len(bc.blocos) {
		return Bloco{}, false
	}
	return bc.blocos[indice], true
}

// CaminhoArquivo retorna o caminho em disco onde esta instância persiste sua cadeia.
func (bc *Blockchain) CaminhoArquivo() string {
	return bc.caminhoArquivo
}

// salvarSemLock persiste a blockchain em disco de forma atômica (arquivo temporário + rename),
// evitando corrupção caso o contêiner seja interrompido durante a gravação.
func (bc *Blockchain) salvarSemLock() error {
	dir := filepath.Dir(bc.caminhoArquivo)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("erro ao criar diretório da blockchain: %w", err)
		}
	}

	dados, err := json.MarshalIndent(bc.blocos, "", "  ")
	if err != nil {
		return fmt.Errorf("erro ao serializar blockchain: %w", err)
	}

	arquivoTemp := bc.caminhoArquivo + ".tmp"
	if err := os.WriteFile(arquivoTemp, dados, 0644); err != nil {
		return fmt.Errorf("erro ao gravar arquivo temporário da blockchain: %w", err)
	}
	if err := os.Rename(arquivoTemp, bc.caminhoArquivo); err != nil {
		_ = os.Remove(arquivoTemp)
		return fmt.Errorf("erro ao substituir arquivo da blockchain: %w", err)
	}
	return nil
}

// ArquivoEstado retorna o caminho do snapshot JSON do estado (mesma pasta do blockchain.json).
func (bc *Blockchain) ArquivoEstado() string {
	return filepath.Join(filepath.Dir(bc.caminhoArquivo), "estado.json")
}

// salvarEstadoSemLock grava o snapshot do estado em estado.json (atômico: temp + rename).
// É um dado derivado de blockchain.json, então uma falha aqui não invalida blocos já
// confirmados: o arquivo é regenerado no próximo bloco ou no próximo boot.
func (bc *Blockchain) salvarEstadoSemLock() {
	if bc.estado == nil {
		return
	}
	snap := bc.estado.Snapshot()
	topo := bc.blocos[len(bc.blocos)-1]
	snap.Altura = len(bc.blocos)
	snap.HashTopo = topo.Hash

	dados, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return
	}
	arq := bc.ArquivoEstado()
	temp := arq + ".tmp"
	if err := os.WriteFile(temp, dados, 0644); err != nil {
		return
	}
	if err := os.Rename(temp, arq); err != nil {
		_ = os.Remove(temp)
	}
}

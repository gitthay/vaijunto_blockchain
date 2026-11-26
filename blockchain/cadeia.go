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
}

// NovaBlockchain carrega uma blockchain existente de caminhoArquivo (validando toda a cadeia)
// ou cria uma nova iniciada pelo bloco gênesis determinístico e salva em disco.
func NovaBlockchain(caminhoArquivo string) (*Blockchain, error) {
	if strings.TrimSpace(caminhoArquivo) == "" {
		return nil, errors.New("caminho do arquivo da blockchain não pode ser vazio")
	}

	bc := &Blockchain{
		caminhoArquivo: caminhoArquivo,
	}

	if dados, err := os.ReadFile(caminhoArquivo); err == nil && len(dados) > 0 {
		var blocos []Bloco
		if err := json.Unmarshal(dados, &blocos); err != nil {
			return nil, fmt.Errorf("erro ao decodificar blockchain em %s: %w", caminhoArquivo, err)
		}
		if err := ValidarCadeia(blocos); err != nil {
			return nil, fmt.Errorf("blockchain local adulterada ou corrompida em %s: %w", caminhoArquivo, err)
		}
		bc.blocos = blocos
		return bc, nil
	}

	// Inicializa com o bloco gênesis determinístico
	bc.blocos = []Bloco{CriarBlocoGenesis()}
	if err := bc.salvarSemLock(); err != nil {
		return nil, err
	}
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

	bc.blocos = append(bc.blocos, bloco)
	if err := bc.salvarSemLock(); err != nil {
		// Rollback em memória caso a escrita em disco falhe
		bc.blocos = bc.blocos[:len(bc.blocos)-1]
		return err
	}
	return nil
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

	copia := make([]Bloco, len(novaCadeia))
	copy(copia, novaCadeia)

	anterior := bc.blocos
	bc.blocos = copia
	if err := bc.salvarSemLock(); err != nil {
		bc.blocos = anterior
		return err
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

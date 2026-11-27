package blockchain

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const PrefixoEndereco = "VJ-"

// Carteira representa a identidade criptográfica (Ed25519) de um usuário (passageiro/motorista)
// ou de um nó validador (empresa do consórcio).
type Carteira struct {
	Endereco     string `json:"endereco"`
	ChavePublica string `json:"chave_publica"`
	ChavePrivada string `json:"chave_privada,omitempty"`
}

// GerarCarteira cria um novo par de chaves Ed25519 aleatório e deriva o endereço público.
func GerarCarteira() (*Carteira, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("erro ao gerar par de chaves Ed25519: %w", err)
	}

	pubHex := hex.EncodeToString(pub)
	privHex := hex.EncodeToString(priv)
	endereco, err := DerivarEndereco(pubHex)
	if err != nil {
		return nil, err
	}

	return &Carteira{
		Endereco:     endereco,
		ChavePublica: pubHex,
		ChavePrivada: privHex,
	}, nil
}

// GerarCarteiraDeterministica cria uma carteira Ed25519 a partir de uma semente textual.
// Útil para testes automatizados e reprodução controlada de nós validadores.
func GerarCarteiraDeterministica(semente string) *Carteira {
	seed := sha256.Sum256([]byte(semente))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := priv.Public().(ed25519.PublicKey)

	pubHex := hex.EncodeToString(pub)
	privHex := hex.EncodeToString(priv)
	endereco, _ := DerivarEndereco(pubHex)

	return &Carteira{
		Endereco:     endereco,
		ChavePublica: pubHex,
		ChavePrivada: privHex,
	}
}

// DerivarEndereco gera um identificador público único e determinístico a partir da chave pública Ed25519.
// Formato: "VJ-" + primeiros 40 caracteres hex do SHA-256(chavePublica).
func DerivarEndereco(chavePublicaHex string) (string, error) {
	pubBytes, err := decodificarChavePublica(chavePublicaHex)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(pubBytes)
	return PrefixoEndereco + hex.EncodeToString(hash[:])[:40], nil
}

// SomentePublica retorna uma cópia da carteira sem a chave privada, segura para compartilhamento na rede.
func (c *Carteira) SomentePublica() Carteira {
	if c == nil {
		return Carteira{}
	}
	return Carteira{
		Endereco:     c.Endereco,
		ChavePublica: c.ChavePublica,
	}
}

// CarregarOuCriarCarteiraArquivo lê a carteira de um arquivo JSON ou cria e salva uma nova
// com permissão restrita (0600) caso ainda não exista.
func CarregarOuCriarCarteiraArquivo(caminho string) (*Carteira, error) {
	if strings.TrimSpace(caminho) == "" {
		return nil, errors.New("caminho da carteira não pode ser vazio")
	}

	if c, err := CarregarCarteiraArquivo(caminho); err == nil {
		return c, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	carteira, err := GerarCarteira()
	if err != nil {
		return nil, err
	}

	dir := filepath.Dir(caminho)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("erro ao criar diretório da carteira: %w", err)
		}
	}

	conteudo, err := json.MarshalIndent(carteira, "", "  ")
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(caminho, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return CarregarCarteiraArquivo(caminho)
	}
	if err != nil {
		return nil, err
	}
	_, err = f.Write(conteudo)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("erro ao salvar carteira em %s: %w", caminho, err)
	}

	return carteira, nil
}

// CarregarCarteiraArquivo valida uma carteira existente sem criar ou sobrescrever chaves.
func CarregarCarteiraArquivo(caminho string) (*Carteira, error) {
	dados, err := os.ReadFile(caminho)
	if err != nil {
		return nil, err
	}
	var c Carteira
	if err := json.Unmarshal(dados, &c); err != nil {
		return nil, err
	}
	end, err := DerivarEndereco(c.ChavePublica)
	if err != nil {
		return nil, err
	}
	if c.Endereco != "" && c.Endereco != end {
		return nil, errors.New("endereço inconsistente")
	}
	priv, err := decodificarChavePrivada(c.ChavePrivada)
	if err != nil {
		return nil, err
	}
	if hex.EncodeToString(ed25519.NewKeyFromSeed(priv.Seed()).Public().(ed25519.PublicKey)) != c.ChavePublica || hex.EncodeToString(ed25519.NewKeyFromSeed(priv.Seed())) != c.ChavePrivada {
		return nil, errors.New("par de chaves inconsistente")
	}
	c.Endereco = end
	return &c, nil
}

// HashSHA256Hex retorna o resumo criptográfico SHA-256 dos dados em formato hexadecimal.
func HashSHA256Hex(dados []byte) string {
	soma := sha256.Sum256(dados)
	return hex.EncodeToString(soma[:])
}

// SerializarParaAssinatura serializa uma estrutura em JSON determinístico para assinatura/verificação.
func SerializarParaAssinatura(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

// AssinarDados assina um conjunto de bytes utilizando a chave privada Ed25519 (em hex)
// e retorna a assinatura digital em formato hexadecimal.
func AssinarDados(chavePrivadaHex string, dados []byte) (string, error) {
	privBytes, err := decodificarChavePrivada(chavePrivadaHex)
	if err != nil {
		return "", err
	}
	assinatura := ed25519.Sign(privBytes, dados)
	return hex.EncodeToString(assinatura), nil
}

// AssinarObjeto serializa uma estrutura e assina seus bytes com a chave privada informada.
func AssinarObjeto(chavePrivadaHex string, objeto interface{}) (string, error) {
	dados, err := SerializarParaAssinatura(objeto)
	if err != nil {
		return "", fmt.Errorf("erro ao serializar objeto para assinatura: %w", err)
	}
	return AssinarDados(chavePrivadaHex, dados)
}

// VerificarAssinatura verifica se a assinatura Ed25519 corresponde aos dados e à chave pública informada.
func VerificarAssinatura(chavePublicaHex string, dados []byte, assinaturaHex string) (bool, error) {
	pubBytes, err := decodificarChavePublica(chavePublicaHex)
	if err != nil {
		return false, err
	}

	sigBytes, err := hex.DecodeString(strings.TrimSpace(assinaturaHex))
	if err != nil {
		return false, fmt.Errorf("assinatura hexadecimal inválida: %w", err)
	}
	if len(sigBytes) != ed25519.SignatureSize {
		return false, fmt.Errorf("tamanho de assinatura inválido: esperado %d bytes, recebido %d", ed25519.SignatureSize, len(sigBytes))
	}

	return ed25519.Verify(pubBytes, dados, sigBytes), nil
}

// ValidarIdentidadeAssinada garante duas propriedades essenciais de segurança:
// 1) A chave pública pertence exatamente ao endereço remetente declarado (impede falsificação de identidade).
// 2) A assinatura Ed25519 é válida para os dados exatos (impede adulteração e garante não-repúdio).
func ValidarIdentidadeAssinada(enderecoDeclarado, chavePublicaHex string, dados []byte, assinaturaHex string) error {
	enderecoDerivado, err := DerivarEndereco(chavePublicaHex)
	if err != nil {
		return fmt.Errorf("chave pública inválida: %w", err)
	}
	if strings.TrimSpace(enderecoDeclarado) != "" && enderecoDeclarado != enderecoDerivado {
		return fmt.Errorf("identidade inconsistente: endereço declarado %s não corresponde à chave pública (%s)", enderecoDeclarado, enderecoDerivado)
	}

	valida, err := VerificarAssinatura(chavePublicaHex, dados, assinaturaHex)
	if err != nil {
		return fmt.Errorf("falha na verificação da assinatura: %w", err)
	}
	if !valida {
		return errors.New("assinatura digital inválida: dados adulterados ou assinados por outra chave")
	}
	return nil
}

func decodificarChavePublica(chavePublicaHex string) (ed25519.PublicKey, error) {
	pubBytes, err := hex.DecodeString(strings.TrimSpace(chavePublicaHex))
	if err != nil {
		return nil, fmt.Errorf("chave pública hexadecimal inválida: %w", err)
	}
	if len(pubBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("tamanho de chave pública inválido: esperado %d bytes, recebido %d", ed25519.PublicKeySize, len(pubBytes))
	}
	return ed25519.PublicKey(pubBytes), nil
}

func decodificarChavePrivada(chavePrivadaHex string) (ed25519.PrivateKey, error) {
	privBytes, err := hex.DecodeString(strings.TrimSpace(chavePrivadaHex))
	if err != nil {
		return nil, fmt.Errorf("chave privada hexadecimal inválida: %w", err)
	}
	if len(privBytes) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("tamanho de chave privada inválido: esperado %d bytes, recebido %d", ed25519.PrivateKeySize, len(privBytes))
	}
	return ed25519.PrivateKey(privBytes), nil
}

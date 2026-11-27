package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"vaiJunto/blockchain"
)

// Carteira é a identidade Ed25519 do usuário (alias para facilitar o uso nos clientes).
type Carteira = blockchain.Carteira

// DiretorioCarteiras é a pasta local (na máquina do cliente) onde ficam as chaves privadas dos usuários.
const DiretorioCarteiras = "carteiras"

var caracteresInvalidosNome = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// CaminhoCarteiraUsuario retorna carteiras/<email-sanitizado>_<hash>.json.
// O hash evita colisão entre e-mails que ficam iguais após a sanitização.
func CaminhoCarteiraUsuario(email string) string {
	email = strings.TrimSpace(email)
	soma := sha256.Sum256([]byte(email))
	nome := caracteresInvalidosNome.ReplaceAllString(email, "_")
	return filepath.Join(DiretorioCarteiras, nome+"_"+hex.EncodeToString(soma[:])[:8]+".json")
}

// CarregarCarteiraDoUsuario carrega a carteira do usuário ou cria uma nova (permissão 0600) no primeiro acesso.
func CarregarCarteiraDoUsuario(email string) (*Carteira, error) {
	return blockchain.CarregarOuCriarCarteiraArquivo(CaminhoCarteiraUsuario(email))
}

// CarregarCarteiraExistente nunca cria uma identidade durante o login.
func CarregarCarteiraExistente(email string) (*Carteira, error) {
	return blockchain.CarregarCarteiraArquivo(CaminhoCarteiraUsuario(email))
}

// PrepararCarteiraCadastro reutiliza uma carteira local existente ou cria uma nova.
func PrepararCarteiraCadastro(email string) (*Carteira, error) {
	c, err := CarregarCarteiraExistente(email)
	if err == nil || !os.IsNotExist(err) {
		return c, err
	}
	return CarregarCarteiraDoUsuario(email)
}

// ChavePublicaDoUsuario retorna a chave pública da carteira do usuário ("" se não for possível carregá-la).
func ChavePublicaDoUsuario(email string) string {
	c, err := CarregarCarteiraExistente(email)
	if err != nil {
		return ""
	}
	return c.ChavePublica
}

// EnviarFunc envia uma requisição ao servidor (já com reconexão automática) e devolve a resposta.
type EnviarFunc func(req MensagemRequisicao) (MensagemResposta, bool)

// MenuTokens exibe o submenu da carteira de tokens (saldo, recarga e transferência).
// As transações de transferência são assinadas aqui, no cliente: a chave privada nunca é enviada.
func MenuTokens(email string, carteira *Carteira, enviar EnviarFunc, ler func(string) string) {
	for {
		fmt.Println("\n------------ CARTEIRA DE TOKENS ------------")
		fmt.Println("1. Ver saldo")
		fmt.Println("2. Recarregar tokens (criar moedas)")
		fmt.Println("3. Transferir tokens")
		fmt.Println("0. Voltar")
		fmt.Println("--------------------------------------------")

		switch ler("Escolha uma opção: ") {
		case "1":
			v, ok := consultarSaldoCliente(email, enviar)
			if !ok {
				continue
			}
			fmt.Printf("\nEndereço : %s\n", v.Endereco)
			fmt.Printf("Saldo    : %d tokens\n", v.Saldo)
			fmt.Printf("Próx. nonce: %d | Total emitido na rede: %d | Blocos: %d\n", v.ProximoNonce, v.TotalEmitido, v.AlturaCadeia)

		case "2":
			valor, ok := lerValorTokens(fmt.Sprintf("Quantidade de tokens (1 a %d, 0 para voltar): ", RecargaMaximaPadrao), ler)
			if !ok {
				continue
			}
			payload, _ := json.Marshal(PayloadRecarga{Valor: valor})
			resp, enviou := enviar(MensagemRequisicao{Acao: AcaoRecarregarTokens, Usuario: email, Payload: payload})
			if !enviou {
				continue
			}
			imprimirResultado(resp)

		case "3":
			destino := ler("Destinatário (e-mail ou endereço VJ-..., 0 para voltar): ")
			if destino == "" || destino == "0" {
				continue
			}
			if !strings.HasPrefix(destino, blockchain.PrefixoEndereco) {
				payload, _ := json.Marshal(PayloadConsultarEndereco{Email: destino})
				resp, enviou := enviar(MensagemRequisicao{Acao: AcaoConsultarEndereco, Usuario: email, Payload: payload})
				if !enviou {
					continue
				}
				if !resp.Sucesso {
					fmt.Println("\n[ERRO]:", resp.Mensagem)
					continue
				}
				var re RespostaEndereco
				if err := json.Unmarshal(resp.Payload, &re); err != nil || re.Endereco == "" {
					fmt.Println("\n[ERRO]: resposta de endereço inválida.")
					continue
				}
				destino = re.Endereco
				fmt.Println("Destinatário:", destino)
			}
			valor, ok := lerValorTokens("Quantidade de tokens a transferir (0 para voltar): ", ler)
			if !ok {
				continue
			}
			v, ok := consultarSaldoCliente(email, enviar)
			if !ok {
				continue
			}
			tx, err := blockchain.NovaTransacaoTransferencia(carteira, destino, valor, v.ProximoNonce, time.Now().Unix())
			if err != nil {
				fmt.Println("\n[ERRO]: não foi possível assinar a transação:", err)
				continue
			}
			payload, _ := json.Marshal(tx)
			resp, enviou := enviar(MensagemRequisicao{Acao: AcaoTransferirTokens, Usuario: email, Payload: payload})
			if !enviou {
				continue
			}
			imprimirResultado(resp)

		case "0":
			return
		default:
			fmt.Println("Opção inválida.")
		}
	}
}

func consultarSaldoCliente(email string, enviar EnviarFunc) (RespostaSaldo, bool) {
	resp, ok := enviar(MensagemRequisicao{Acao: AcaoSaldoTokens, Usuario: email})
	if !ok {
		return RespostaSaldo{}, false
	}
	if !resp.Sucesso {
		fmt.Println("\n[ERRO]:", resp.Mensagem)
		return RespostaSaldo{}, false
	}
	var v RespostaSaldo
	if err := json.Unmarshal(resp.Payload, &v); err != nil {
		fmt.Println("\n[ERRO]: resposta de saldo inválida.")
		return RespostaSaldo{}, false
	}
	return v, true
}

func lerValorTokens(rotulo string, ler func(string) string) (uint64, bool) {
	for {
		txt := ler(rotulo)
		if txt == "0" || txt == "" {
			return 0, false
		}
		v, err := strconv.ParseUint(strings.TrimSpace(txt), 10, 64)
		if err != nil || v == 0 {
			fmt.Println("[ERRO]: informe um número inteiro maior que zero.")
			continue
		}
		return v, true
	}
}

func imprimirResultado(resp MensagemResposta) {
	if resp.Sucesso {
		fmt.Println("\n[SUCESSO]:", resp.Mensagem)
	} else {
		fmt.Println("\n[ERRO]:", resp.Mensagem)
	}
}

// ChavePublicaCadastroSimulador cria carteiras apenas para contas de teste novas.
func ChavePublicaCadastroSimulador(email string) string {
	c, err := PrepararCarteiraCadastro(email)
	if err != nil {
		return ""
	}
	return c.ChavePublica
}

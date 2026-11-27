package utils

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"vaiJunto/blockchain"
)

// Ações TCP de tokens (cliente ↔ servidor da própria empresa).
const (
	AcaoSaldoTokens       TipoAcao = "SALDO_TOKENS"
	AcaoRecarregarTokens  TipoAcao = "RECARREGAR_TOKENS"
	AcaoTransferirTokens  TipoAcao = "TRANSFERIR_TOKENS"
	AcaoConsultarEndereco TipoAcao = "CONSULTAR_ENDERECO"
)

// RecargaMaximaPadrao é o limite de tokens por recarga (emissão de teste/demonstração).
// Pode ser alterado com a variável de ambiente VJ_RECARGA_MAX (0 desativa as recargas).
const RecargaMaximaPadrao uint64 = 1000

// RespostaSaldo é o payload devolvido por SALDO_TOKENS (e após recarga/transferência).
type RespostaSaldo struct {
	Endereco     string `json:"endereco"`
	Saldo        uint64 `json:"saldo"`
	ProximoNonce uint64 `json:"proximo_nonce"`
	TotalEmitido uint64 `json:"total_emitido"`
	AlturaCadeia int    `json:"altura_cadeia"`
	BlocoIndice  uint64 `json:"bloco_indice,omitempty"`
	BlocoHash    string `json:"bloco_hash,omitempty"`
}

// PayloadRecarga é o payload de RECARREGAR_TOKENS.
type PayloadRecarga struct {
	Valor uint64 `json:"valor"`
}

// PayloadConsultarEndereco é o payload de CONSULTAR_ENDERECO.
type PayloadConsultarEndereco struct {
	Email string `json:"email"`
}

// RespostaEndereco é o payload devolvido por CONSULTAR_ENDERECO.
type RespostaEndereco struct {
	Email    string `json:"email"`
	Endereco string `json:"endereco"`
}

// muCadeia serializa a criação de blocos: cada bloco precisa ser construído sobre o topo
// atual da cadeia e usar o próximo nonce correto, então dois blocos não podem ser montados em paralelo.
var muCadeia sync.Mutex

// RecargaMaxima retorna o limite de tokens por recarga neste servidor.
func RecargaMaxima() uint64 {
	v := strings.TrimSpace(os.Getenv("VJ_RECARGA_MAX"))
	if v == "" {
		return RecargaMaximaPadrao
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return RecargaMaximaPadrao
	}
	return n
}

// TratarAcaoTokens processa as ações de tokens. O segundo retorno é false quando a ação não é de tokens.
func TratarAcaoTokens(req MensagemRequisicao) (MensagemResposta, bool) {
	switch req.Acao {
	case AcaoSaldoTokens:
		return acaoSaldo(req), true
	case AcaoRecarregarTokens:
		return acaoRecarregar(req), true
	case AcaoTransferirTokens:
		return acaoTransferir(req), true
	case AcaoConsultarEndereco:
		return acaoConsultarEndereco(req), true
	}
	return MensagemResposta{}, false
}

func respostaOK(msg string, payload interface{}) MensagemResposta {
	var raw json.RawMessage
	if payload != nil {
		raw, _ = json.Marshal(payload)
	}
	return MensagemResposta{Sucesso: true, Mensagem: msg, Payload: raw}
}

func respostaFalha(msg string) MensagemResposta {
	return MensagemResposta{Sucesso: false, Mensagem: msg}
}

func enderecoDoUsuario(email string) (string, error) {
	u, err := ObterUsuarioPorEmail(email)
	if err != nil {
		return "", err
	}
	if u.Endereco == "" {
		return "", errors.New("este usuário ainda não possui carteira (faça login com um cliente atualizado)")
	}
	return u.Endereco, nil
}

func visaoSaldo(endereco string) RespostaSaldo {
	estado := CadeiaLocal.Estado()
	return RespostaSaldo{
		Endereco:     endereco,
		Saldo:        estado.Saldo(endereco),
		ProximoNonce: estado.ProximoNonce(endereco),
		TotalEmitido: estado.TotalEmitido(),
		AlturaCadeia: CadeiaLocal.Altura(),
	}
}

func cadeiaIndisponivel() bool {
	return CadeiaLocal == nil || CadeiaLocal.Estado() == nil || CarteiraValidador == nil
}

func acaoSaldo(req MensagemRequisicao) MensagemResposta {
	if cadeiaIndisponivel() {
		return respostaFalha("Blockchain com estado indisponível neste servidor.")
	}
	end, err := enderecoDoUsuario(req.Usuario)
	if err != nil {
		return respostaFalha("Carteira indisponível: " + err.Error())
	}
	if err := sincronizarLedger(); err != nil {
		return respostaFalha(err.Error())
	}
	v := visaoSaldo(end)
	return respostaOK(fmt.Sprintf("Saldo: %d tokens.", v.Saldo), v)
}

// acaoRecarregar emite tokens para o usuário (EMITIR_TOKENS assinada pelo validador da empresa).
func acaoRecarregar(req MensagemRequisicao) MensagemResposta {
	if cadeiaIndisponivel() {
		return respostaFalha("Blockchain com estado indisponível neste servidor.")
	}
	var p PayloadRecarga
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		return respostaFalha("Payload de recarga inválido.")
	}
	limite := RecargaMaxima()
	if limite == 0 {
		return respostaFalha("Recargas de tokens estão desativadas neste servidor.")
	}
	if p.Valor == 0 || p.Valor > limite {
		return respostaFalha(fmt.Sprintf("Valor da recarga deve estar entre 1 e %d tokens.", limite))
	}
	end, err := enderecoDoUsuario(req.Usuario)
	if err != nil {
		return respostaFalha("Carteira indisponível: " + err.Error())
	}

	muCadeia.Lock()
	defer muCadeia.Unlock()

	if err := sincronizarLedger(); err != nil {
		return respostaFalha(err.Error())
	}
	nonce := CadeiaLocal.Estado().ProximoNonce(CarteiraValidador.Endereco)
	tx, err := blockchain.NovaTransacaoEmissao(CarteiraValidador, end, p.Valor, nonce, time.Now().Unix())
	if err != nil {
		return respostaFalha("Erro ao criar transação de emissão: " + err.Error())
	}
	bloco, err := submeterTransacaoLocked(tx)
	if err != nil {
		return respostaFalha(mensagemErroToken(err))
	}
	RegistrarLog("[TOKENS] Emissão de %d tokens para %s (%s) no bloco #%d", p.Valor, req.Usuario, end, bloco.Indice)

	v := visaoSaldo(end)
	v.BlocoIndice, v.BlocoHash = bloco.Indice, bloco.Hash
	return respostaOK(fmt.Sprintf("Recarga de %d tokens confirmada no bloco #%d. Saldo: %d.", p.Valor, bloco.Indice, v.Saldo), v)
}

// acaoTransferir recebe uma transação TRANSFERIR_TOKENS JÁ ASSINADA pelo cliente (a chave privada
// nunca sai da máquina do usuário) e a registra em um novo bloco se a máquina de estado aceitar.
func acaoTransferir(req MensagemRequisicao) MensagemResposta {
	if cadeiaIndisponivel() {
		return respostaFalha("Blockchain com estado indisponível neste servidor.")
	}
	var tx blockchain.Transacao
	if err := json.Unmarshal(req.Payload, &tx); err != nil {
		return respostaFalha("Transação de transferência inválida.")
	}
	if tx.Tipo != blockchain.TxTransferirTokens {
		return respostaFalha("Esta ação aceita apenas transações do tipo TRANSFERIR_TOKENS.")
	}
	end, err := enderecoDoUsuario(req.Usuario)
	if err != nil {
		return respostaFalha("Carteira indisponível: " + err.Error())
	}
	if tx.Remetente != end {
		return respostaFalha("O remetente da transação não corresponde à carteira do usuário autenticado.")
	}

	muCadeia.Lock()
	defer muCadeia.Unlock()

	bloco, err := submeterTransacaoLocked(tx)
	if err != nil {
		return respostaFalha(mensagemErroToken(err))
	}
	RegistrarLog("[TOKENS] Transferência de %s registrada no bloco #%d (tx %s)", req.Usuario, bloco.Indice, tx.ID)

	v := visaoSaldo(end)
	v.BlocoIndice, v.BlocoHash = bloco.Indice, bloco.Hash
	return respostaOK(fmt.Sprintf("Transferência confirmada no bloco #%d. Saldo: %d.", bloco.Indice, v.Saldo), v)
}

// acaoConsultarEndereco resolve o e-mail de um usuário cadastrado nesta empresa para o endereço da carteira.
func acaoConsultarEndereco(req MensagemRequisicao) MensagemResposta {
	var p PayloadConsultarEndereco
	if err := json.Unmarshal(req.Payload, &p); err != nil || strings.TrimSpace(p.Email) == "" {
		return respostaFalha("Informe o e-mail do destinatário.")
	}
	end, err := enderecoDoUsuario(strings.TrimSpace(p.Email))
	if err != nil {
		end, err = buscarEnderecoRemoto(p.Email)
		if err != nil {
			return respostaFalha(err.Error())
		}
	}
	return respostaOK("Endereço encontrado.", RespostaEndereco{Email: p.Email, Endereco: end})
}

// submeterTransacaoLocked monta um bloco com a transação sobre o topo atual, assinado pelo validador
// da empresa, e o adiciona à cadeia. A máquina de estado rejeita saldo insuficiente e nonce inválido.
// O chamador DEVE estar segurando muCadeia.
func submeterTransacaoLocked(tx blockchain.Transacao) (blockchain.Bloco, error) {
	if redeFinanceira != nil {
		return submeterDistribuido(tx)
	}
	bloco, err := blockchain.NovoBloco(CadeiaLocal.UltimoBloco(), []blockchain.Transacao{tx},
		EmpresaAtualID, CarteiraValidador, time.Now().Unix())
	if err != nil {
		return blockchain.Bloco{}, err
	}
	if err := CadeiaLocal.AdicionarBloco(bloco); err != nil {
		return blockchain.Bloco{}, err
	}
	return bloco, nil
}

// mensagemErroToken traduz os erros da máquina de estado em mensagens claras para o usuário.
func mensagemErroToken(err error) string {
	switch {
	case errors.Is(err, blockchain.ErrSaldoInsuficiente):
		return "Saldo insuficiente para esta operação."
	case errors.Is(err, blockchain.ErrNonceInvalido):
		return "Nonce inválido: a transação já foi usada ou está fora de ordem. Tente novamente."
	case errors.Is(err, blockchain.ErrValorInvalido):
		return "O valor deve ser maior que zero."
	case errors.Is(err, blockchain.ErrPayloadInvalido):
		return "Dados inválidos (confira o endereço do destinatário e não transfira para si mesmo)."
	case errors.Is(err, blockchain.ErrOverflowSaldo):
		return "Operação rejeitada: estouro do limite de saldo."
	case errors.Is(err, blockchain.ErrEmissorNaoAutorizado):
		return "Este servidor não está autorizado a emitir tokens."
	}
	return "Transação rejeitada: " + err.Error()
}

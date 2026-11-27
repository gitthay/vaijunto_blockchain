package utils

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"vaiJunto/blockchain"
)

// prepararEmpresa isola uma instância de servidor em um diretório temporário
// (dados/<empresa>/ é criado relativo ao diretório de trabalho).
func prepararEmpresa(t *testing.T, empresa string) {
	t.Helper()
	dir := t.TempDir()
	antigo, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(antigo) })
	if err := ConfigurarInstancia(empresa, "http://localhost:9999"); err != nil {
		t.Fatalf("ConfigurarInstancia: %v", err)
	}
}

func cadastrar(t *testing.T, email, semente string) *blockchain.Carteira {
	t.Helper()
	c := blockchain.GerarCarteiraDeterministica(semente)
	ok, msg, _, err := AutenticarOuCadastrarUsuarioComChave(email, "123", TipoPassageiro, c.ChavePublica)
	if err != nil || !ok {
		t.Fatalf("cadastro de %s falhou: %v %s", email, err, msg)
	}
	return c
}

func chamar(t *testing.T, acao TipoAcao, usuario string, payload interface{}) MensagemResposta {
	t.Helper()
	var raw json.RawMessage
	if payload != nil {
		raw, _ = json.Marshal(payload)
	}
	r, tratada := TratarAcaoTokens(MensagemRequisicao{Acao: acao, Usuario: usuario, Payload: raw})
	if !tratada {
		t.Fatalf("ação %s não foi tratada", acao)
	}
	return r
}

func saldoDe(t *testing.T, email string) RespostaSaldo {
	t.Helper()
	r := chamar(t, AcaoSaldoTokens, email, nil)
	if !r.Sucesso {
		t.Fatalf("saldo de %s falhou: %s", email, r.Mensagem)
	}
	var v RespostaSaldo
	_ = json.Unmarshal(r.Payload, &v)
	return v
}

func transferir(t *testing.T, email string, c *blockchain.Carteira, destino string, valor uint64) MensagemResposta {
	t.Helper()
	nonce := saldoDe(t, email).ProximoNonce
	tx, err := blockchain.NovaTransacaoTransferencia(c, destino, valor, nonce, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	return chamar(t, AcaoTransferirTokens, email, tx)
}

func TestFluxoRecargaSaldoETransferencia(t *testing.T) {
	prepararEmpresa(t, "empresaTeste")
	alice := cadastrar(t, "alice@x.com", "alice")
	bob := cadastrar(t, "bob@x.com", "bob")

	if v := saldoDe(t, "alice@x.com"); v.Saldo != 0 || v.ProximoNonce != 1 || v.Endereco != alice.Endereco {
		t.Fatalf("saldo inicial incorreto: %+v", v)
	}

	// Cria moedas (recarga)
	if r := chamar(t, AcaoRecarregarTokens, "alice@x.com", PayloadRecarga{Valor: 100}); !r.Sucesso {
		t.Fatalf("recarga falhou: %s", r.Mensagem)
	}
	if v := saldoDe(t, "alice@x.com"); v.Saldo != 100 || v.TotalEmitido != 100 || v.AlturaCadeia != 2 {
		t.Fatalf("saldo após recarga incorreto: %+v", v)
	}

	// Resolve o destinatário por e-mail e transfere
	r := chamar(t, AcaoConsultarEndereco, "alice@x.com", PayloadConsultarEndereco{Email: "bob@x.com"})
	var re RespostaEndereco
	_ = json.Unmarshal(r.Payload, &re)
	if !r.Sucesso || re.Endereco != bob.Endereco {
		t.Fatalf("resolução de endereço falhou: %+v", r)
	}
	if r := transferir(t, "alice@x.com", alice, re.Endereco, 40); !r.Sucesso {
		t.Fatalf("transferência falhou: %s", r.Mensagem)
	}
	if a, b := saldoDe(t, "alice@x.com"), saldoDe(t, "bob@x.com"); a.Saldo != 60 || b.Saldo != 40 || a.ProximoNonce != 2 {
		t.Fatalf("saldos finais incorretos: alice=%+v bob=%+v", a, b)
	}
}

func TestRejeicoes(t *testing.T) {
	prepararEmpresa(t, "empresaTeste")
	alice := cadastrar(t, "alice@x.com", "alice")
	bob := cadastrar(t, "bob@x.com", "bob")
	chamar(t, AcaoRecarregarTokens, "alice@x.com", PayloadRecarga{Valor: 50})

	// Saldo insuficiente
	if r := transferir(t, "alice@x.com", alice, bob.Endereco, 51); r.Sucesso || !strings.Contains(r.Mensagem, "Saldo insuficiente") {
		t.Fatalf("esperava saldo insuficiente, obtido: %+v", r)
	}

	// Replay: reenviar a mesma transação já confirmada
	tx, _ := blockchain.NovaTransacaoTransferencia(alice, bob.Endereco, 10, 1, time.Now().Unix())
	if r := chamar(t, AcaoTransferirTokens, "alice@x.com", tx); !r.Sucesso {
		t.Fatalf("1ª transferência deveria passar: %s", r.Mensagem)
	}
	if r := chamar(t, AcaoTransferirTokens, "alice@x.com", tx); r.Sucesso || !strings.Contains(r.Mensagem, "Nonce inválido") {
		t.Fatalf("replay deveria ser rejeitado por nonce, obtido: %+v", r)
	}

	// Saldos não mudaram com as rejeições
	if a := saldoDe(t, "alice@x.com"); a.Saldo != 40 {
		t.Fatalf("saldo de alice deveria ser 40, é %d", a.Saldo)
	}

	// Transação assinada por Alice enviada pela sessão de Bob
	txAlice, _ := blockchain.NovaTransacaoTransferencia(alice, bob.Endereco, 1, 2, time.Now().Unix())
	if r := chamar(t, AcaoTransferirTokens, "bob@x.com", txAlice); r.Sucesso {
		t.Fatalf("remetente diferente do usuário autenticado deveria ser rejeitado")
	}

	// Tipo errado (tentar emitir tokens pelo caminho de transferência)
	emissao, _ := blockchain.NovaTransacaoEmissao(alice, alice.Endereco, 1000, 2, time.Now().Unix())
	if r := chamar(t, AcaoTransferirTokens, "alice@x.com", emissao); r.Sucesso {
		t.Fatalf("emissão pelo caminho de transferência deveria ser rejeitada")
	}

	// Limites de recarga
	if r := chamar(t, AcaoRecarregarTokens, "alice@x.com", PayloadRecarga{Valor: RecargaMaximaPadrao + 1}); r.Sucesso {
		t.Fatalf("recarga acima do limite deveria ser rejeitada")
	}
	if r := chamar(t, AcaoRecarregarTokens, "alice@x.com", PayloadRecarga{Valor: 0}); r.Sucesso {
		t.Fatalf("recarga de zero deveria ser rejeitada")
	}
	t.Setenv("VJ_RECARGA_MAX", "0")
	if r := chamar(t, AcaoRecarregarTokens, "alice@x.com", PayloadRecarga{Valor: 1}); r.Sucesso {
		t.Fatalf("recarga desativada deveria ser rejeitada")
	}
}

func TestUsuarioSemCarteira(t *testing.T) {
	prepararEmpresa(t, "empresaTeste")
	// Cliente antigo: cadastra sem chave pública
	if ok, _, _, err := AutenticarOuCadastrarUsuarioComChave("velho@x.com", "123", TipoPassageiro, ""); !ok || err != nil {
		t.Fatal("cadastro sem chave deveria funcionar")
	}
	if r := chamar(t, AcaoSaldoTokens, "velho@x.com", nil); r.Sucesso {
		t.Fatalf("usuário sem carteira não deveria ter saldo")
	}
	if r := chamar(t, AcaoRecarregarTokens, "velho@x.com", PayloadRecarga{Valor: 10}); r.Sucesso {
		t.Fatalf("usuário sem carteira não deveria receber recarga")
	}
	if r := chamar(t, AcaoConsultarEndereco, "velho@x.com", PayloadConsultarEndereco{Email: "naoexiste@x.com"}); r.Sucesso {
		t.Fatalf("e-mail inexistente deveria falhar")
	}
	// Ação que não é de tokens não deve ser tratada aqui
	if _, tratada := TratarAcaoTokens(MensagemRequisicao{Acao: AcaoListarReservas}); tratada {
		t.Fatalf("ação de reservas não pode ser tratada pela camada de tokens")
	}
}

func TestRecargasConcorrentesNaoColidemNonce(t *testing.T) {
	prepararEmpresa(t, "empresaTeste")
	cadastrar(t, "alice@x.com", "alice")

	var wg sync.WaitGroup
	var mu sync.Mutex
	falhas := 0
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _ := TratarAcaoTokens(MensagemRequisicao{
				Acao: AcaoRecarregarTokens, Usuario: "alice@x.com",
				Payload: json.RawMessage(`{"valor":2}`),
			})
			if !r.Sucesso {
				mu.Lock()
				falhas++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if v := saldoDe(t, "alice@x.com"); falhas != 0 || v.Saldo != 50 || v.AlturaCadeia != 26 {
		t.Fatalf("esperava 25 recargas (saldo 50, 26 blocos), obtido falhas=%d %+v", falhas, v)
	}
}

func TestEstadoPersisteAposReiniciar(t *testing.T) {
	prepararEmpresa(t, "empresaTeste")
	alice := cadastrar(t, "alice@x.com", "alice")
	bob := cadastrar(t, "bob@x.com", "bob")
	chamar(t, AcaoRecarregarTokens, "alice@x.com", PayloadRecarga{Valor: 100})
	transferir(t, "alice@x.com", alice, bob.Endereco, 25)
	antes := CadeiaLocal.Estado().Raiz()

	// "Reinicia" o servidor: recarrega carteira do validador, blockchain.json e reconstrói o estado
	if err := ConfigurarInstancia("empresaTeste", "http://localhost:9999"); err != nil {
		t.Fatal(err)
	}
	if CadeiaLocal.Estado().Raiz() != antes {
		t.Fatalf("estado reconstruído difere do original")
	}
	if a, b := saldoDe(t, "alice@x.com"), saldoDe(t, "bob@x.com"); a.Saldo != 75 || b.Saldo != 25 || a.ProximoNonce != 2 {
		t.Fatalf("saldos após reinício incorretos: %+v %+v", a, b)
	}
	// E o JSON do estado existe na pasta da empresa
	if _, err := os.Stat("dados/empresaTeste/estado.json"); err != nil {
		t.Fatalf("estado.json não encontrado em dados/empresaTeste: %v", err)
	}
}

func TestCaminhoCarteiraUsuarioSeguro(t *testing.T) {
	a := CaminhoCarteiraUsuario("a/b@x.com")
	b := CaminhoCarteiraUsuario("a_b@x.com")
	if a == b {
		t.Fatalf("e-mails diferentes não podem compartilhar o mesmo arquivo de carteira")
	}
	if strings.Contains(strings.TrimPrefix(a, DiretorioCarteiras), "/b") {
		t.Fatalf("caminho não pode conter separadores vindos do e-mail: %s", a)
	}
}

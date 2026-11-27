package utils

import (
	"os"
	"testing"
	"vaiJunto/blockchain"
)

func TestCadastroLoginSeparados(t *testing.T) {
	prepararEmpresa(t, "empresaAuth")
	email := "conta@example.com"
	c := blockchain.GerarCarteiraDeterministica("auth-test")
	verificar := func(cadastro bool, senha string, tipo TipoUsuario, chave string, esperado bool) {
		t.Helper()
		ok, msg, _, err := AutenticarConta(email, senha, tipo, chave, cadastro)
		if err != nil || ok != esperado {
			t.Fatalf("cadastro=%v: %v %s %v", cadastro, ok, msg, err)
		}
	}
	verificar(false, "senha", TipoMotorista, c.ChavePublica, false)
	verificar(true, "senha", TipoMotorista, c.ChavePublica, true)
	verificar(true, "senha", TipoMotorista, c.ChavePublica, false)
	verificar(false, "errada", TipoMotorista, c.ChavePublica, false)
	verificar(false, "senha", TipoPassageiro, c.ChavePublica, false)
	verificar(false, "senha", TipoMotorista, "", false)
	verificar(false, "senha", TipoMotorista, blockchain.GerarCarteiraDeterministica("outra").ChavePublica, false)
	verificar(false, "senha", TipoMotorista, c.ChavePublica, true)
}

func TestLoginNaoCriaCarteira(t *testing.T) {
	prepararEmpresa(t, "empresaCarteiraAuth")
	email := "carteira@example.com"
	if _, err := CarregarCarteiraExistente(email); !os.IsNotExist(err) {
		t.Fatalf("esperava arquivo ausente: %v", err)
	}
	if _, err := os.Stat(CaminhoCarteiraUsuario(email)); !os.IsNotExist(err) {
		t.Fatal("login criou carteira")
	}
	c, err := PrepararCarteiraCadastro(email)
	if err != nil {
		t.Fatal(err)
	}
	d, err := CarregarCarteiraExistente(email)
	if err != nil || c.ChavePublica != d.ChavePublica {
		t.Fatal("carteira não preservada", err)
	}
	if err := os.WriteFile(CaminhoCarteiraUsuario(email), []byte("invalido"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepararCarteiraCadastro(email); err == nil {
		t.Fatal("carteira corrompida substituída")
	}
}

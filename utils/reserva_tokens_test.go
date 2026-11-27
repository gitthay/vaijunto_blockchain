package utils

import (
	"testing"
	"time"
	"vaiJunto/blockchain"
)

func TestReservaComTokens(t *testing.T) {
	prepararEmpresa(t, "empresaTeste")
	LimparEstado2PCMemoria()
	t.Cleanup(LimparEstado2PCMemoria)
	alice := cadastrar(t, "alice@x.com", "alice-reserva")
	motorista := cadastrar(t, "motorista@x.com", "motorista-reserva")
	agora := time.Now().Add(time.Hour)
	if err := SalvarCarona(Carona{ID: "CAR-1", MotoristaID: "motorista@x.com", Ativa: true, AssentosTot: 2, Trechos: []Trecho{{Origem: "A", Destino: "B", Preco: 40, AssentosLivre: 2, HorarioPartida: agora, HorarioChegada: agora.Add(time.Hour)}}}); err != nil {
		t.Fatal(err)
	}
	it := Itinerario{PrecoTotal: 1, Trechos: []ArestaTrecho{{CaronaID: "CAR-1", Origem: "A", Destino: "B", Preco: 1}}}
	if _, err := CotarReservaTokens("alice@x.com", it); err == nil {
		t.Fatal("cotação sem saldo aceita")
	}
	if r := chamar(t, AcaoRecarregarTokens, "alice@x.com", PayloadRecarga{Valor: 100}); !r.Sucesso {
		t.Fatal(r.Mensagem)
	}
	c, err := CotarReservaTokens("alice@x.com", it)
	if err != nil {
		t.Fatal(err)
	}
	if c.Escrow.ValorTotal != 40 || c.Escrow.Trechos[0].Motorista != motorista.Endereco {
		t.Fatal("cotação aceitou dados do cliente")
	}
	tx, err := blockchain.NovaTransacaoCriarEscrow(alice, c.Escrow, c.Nonce, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	r, err := ReservarComTokens("alice@x.com", ReservaComTokens{Itinerario: it, Transacao: tx})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != c.Escrow.ReservaID || CadeiaLocal.Estado().Saldo(alice.Endereco) != 60 || CadeiaLocal.Estado().SaldoBloqueado(alice.Endereco) != 40 || CadeiaLocal.Estado().Saldo(motorista.Endereco) != 0 {
		t.Fatal("estado financeiro incorreto")
	}
	if _, err = ReservarComTokens("alice@x.com", ReservaComTokens{Itinerario: it, Transacao: tx}); err == nil {
		t.Fatal("replay aceito")
	}
	cs, err := CarregarCaronas()
	if err != nil || cs[0].Trechos[0].AssentosLivre != 1 {
		t.Fatal("assentos incorretos")
	}
}

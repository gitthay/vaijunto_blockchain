package utils

import (
	"encoding/json"
	"testing"
	"time"
	"vaiJunto/blockchain"
)

func TestValidarConclusaoReserva(t *testing.T) {
	agora := time.Now()
	r := Reserva{Itinerario: []ArestaTrecho{{HorarioChegada: agora.Add(-time.Hour)}, {HorarioChegada: agora.Add(time.Hour)}}}
	if validarConclusaoReserva(r, agora) == nil {
		t.Fatal("conclusão antecipada")
	}
	r.Itinerario[1].HorarioChegada = agora.Add(-time.Minute)
	if err := validarConclusaoReserva(r, agora); err != nil {
		t.Fatal(err)
	}
	r.Itinerario[1].ServidorOffline = true
	if validarConclusaoReserva(r, agora) == nil {
		t.Fatal("trecho indisponível")
	}
}

func TestCicloReservaTokensLocal(t *testing.T) {
	prepararEmpresa(t, "empresaTeste")
	LimparEstado2PCMemoria()
	t.Cleanup(LimparEstado2PCMemoria)
	p := cadastrar(t, "p@x.com", "p-ciclo")
	m := cadastrar(t, "m@x.com", "m-ciclo")
	if r := chamar(t, AcaoRecarregarTokens, "p@x.com", PayloadRecarga{Valor: 100}); !r.Sucesso {
		t.Fatal(r.Mensagem)
	}
	reservar := func(id string, chegada time.Time) *Reserva {
		t.Helper()
		if err := SalvarCarona(Carona{ID: id, MotoristaID: "m@x.com", Ativa: true, AssentosTot: 2, Trechos: []Trecho{{Origem: "A", Destino: "B", Preco: 40, AssentosLivre: 2, HorarioPartida: chegada.Add(-time.Hour), HorarioChegada: chegada}}}); err != nil {
			t.Fatal(err)
		}
		it := Itinerario{Trechos: []ArestaTrecho{{CaronaID: id, Origem: "A", Destino: "B"}}}
		c, err := CotarReservaTokens("p@x.com", it)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := blockchain.NovaTransacaoCriarEscrow(p, c.Escrow, c.Nonce, time.Now().Unix())
		if err != nil {
			t.Fatal(err)
		}
		r, err := ReservarComTokens("p@x.com", ReservaComTokens{Itinerario: it, Transacao: tx})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	operar := func(acao TipoAcao, tx blockchain.Transacao) MensagemResposta {
		raw, _ := json.Marshal(tx)
		return OperarEscrowReserva(MensagemRequisicao{Acao: acao, Usuario: "p@x.com", Payload: raw})
	}
	futura := reservar("F", time.Now().Add(time.Hour))
	tx, _ := blockchain.NovaTransacaoConcluirViagem(p, futura.ID, 2, time.Now().Unix())
	if operar(AcaoConcluirViagem, tx).Sucesso {
		t.Fatal("pagamento antecipado")
	}
	tx, _ = blockchain.NovaTransacaoCancelarEscrow(p, futura.ID, 2, time.Now().Unix())
	if r := operar(AcaoCancelarReserva, tx); !r.Sucesso {
		t.Fatal(r.Mensagem)
	}
	if CadeiaLocal.Estado().Saldo(p.Endereco) != 100 {
		t.Fatal("estorno")
	}
	cs, _ := CarregarCaronas()
	if cs[0].Trechos[0].AssentosLivre != 2 {
		t.Fatal("assento não liberado")
	}
	passada := reservar("P", time.Now().Add(-time.Minute))
	tx, _ = blockchain.NovaTransacaoConcluirViagem(p, passada.ID, 4, time.Now().Unix())
	if r := operar(AcaoConcluirViagem, tx); !r.Sucesso {
		t.Fatal(r.Mensagem)
	}
	if CadeiaLocal.Estado().Saldo(m.Endereco) != 40 {
		t.Fatal("pagamento")
	}
	rs, err := ListarReservasPassageiro("p@x.com")
	if err != nil || len(rs) != 1 || rs[0].Status != "CONCLUIDA" {
		t.Fatalf("status: %+v %v", rs, err)
	}
}
